// Webhook-side middleware: IP allow-list + paybill token verification.
//
// Both routes (validation, confirmation) are public — they have no
// JWT bearer. The IP allow-list and the high-entropy per-paybill
// token are the only two auth checks. We layer them in that order
// so an attacker probing a random URL never reaches the DB lookup.
//
// IP allow-list is loaded from MPESA_TRUSTED_IPS (CSV). In sandbox
// it may be empty (the operator hasn't pinned the right IPs yet);
// when MPESA_ENV is "production" an empty list is rejected at
// startup — see config.ValidateWebhookSecurity in main.go.

package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type webhookCtxKey int

const (
	keyWebhookPaybillID webhookCtxKey = iota
	keyWebhookToken
)

// IPAllowList compiles a CSV of CIDRs / bare IPs into a fast matcher.
// `allowEmpty` says whether an empty list is acceptable; the caller
// passes false in production. A nil matcher (no list) permits every
// request and emits a single warning log at startup time.
type IPAllowList struct {
	nets   []*net.IPNet
	ips    map[string]struct{}
	any    bool // empty list, accept all
	logger *slog.Logger
}

func NewIPAllowList(csv string, logger *slog.Logger) (*IPAllowList, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		logger.Warn("MPESA_TRUSTED_IPS is empty — webhook IP allow-list disabled")
		return &IPAllowList{any: true, logger: logger}, nil
	}
	a := &IPAllowList{ips: map[string]struct{}{}, logger: logger}
	for _, raw := range strings.Split(csv, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			_, n, err := net.ParseCIDR(raw)
			if err != nil {
				return nil, err
			}
			a.nets = append(a.nets, n)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, &net.ParseError{Type: "IP", Text: raw}
		}
		a.ips[ip.String()] = struct{}{}
	}
	return a, nil
}

// Permits returns true if the request's caller IP matches the allow
// list. Reads X-Forwarded-For first (one hop only — anything more is
// proxy soup we don't trust) and falls back to r.RemoteAddr.
func (a *IPAllowList) Permits(r *http.Request) bool {
	if a == nil || a.any {
		return true
	}
	remote := callerIP(r)
	if remote == nil {
		return false
	}
	if _, ok := a.ips[remote.String()]; ok {
		return true
	}
	for _, n := range a.nets {
		if n.Contains(remote) {
			return true
		}
	}
	return false
}

// Middleware returns a chi-compatible middleware that 403s when the
// caller IP is outside the allow list. Logs the rejection so operators
// can populate the allow list from real Safaricom traffic.
func (a *IPAllowList) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Permits(r) {
			a.logger.Warn("webhook IP rejected",
				"remote", r.RemoteAddr, "xff", r.Header.Get("X-Forwarded-For"), "path", r.URL.Path)
			http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: source IP not allow-listed"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// callerIP picks the best-guess source IP. Order:
//   1. First entry of X-Forwarded-For (only when the proxy is trusted
//      — in this codebase the ingress already strips inbound XFFs,
//      so we treat the leftmost as canonical).
//   2. r.RemoteAddr (drop the :port).
// Returns nil when the input doesn't parse — caller treats that as
// "not allowed".
func callerIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// WithWebhookContext stashes the URL paybill id + query token on the
// request context so the handler can pick them up without re-parsing.
// The middleware that follows (WebhookTokenGate) is what actually
// validates them; this just makes the values available to that.
func WithWebhookContext(paybillID uuid.UUID, token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), keyWebhookPaybillID, paybillID)
			ctx = context.WithValue(ctx, keyWebhookToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WebhookPaybillIDFrom returns the paybill_id parsed by the handler
// from the URL. Available only after WithWebhookContext.
func WebhookPaybillIDFrom(r *http.Request) (uuid.UUID, bool) {
	v, ok := r.Context().Value(keyWebhookPaybillID).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// WebhookTokenFrom returns the ?token=… the caller supplied.
func WebhookTokenFrom(r *http.Request) string {
	v, _ := r.Context().Value(keyWebhookToken).(string)
	return v
}
