// /healthz — operational health probe for the savings service.
//
// Reports:
//   • status                     "ok" | "degraded"
//   • outbox_pending             count of posting_outbox rows with dispatched_at IS NULL
//   • outbox_oldest_age_seconds  age of the oldest pending row
//   • accounting_reachable       TCP-level dial probe (no auth round-trip)
//
// "degraded" fires when outbox_oldest_age_seconds > the threshold
// (env SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S, default 60). That's
// the visible signal the dispatcher has stopped draining.
//
// Public — NOT behind auth or tenant scoping. Health checks must
// always work. The outbox query intentionally runs on the unscoped
// pool (h.DB.Pool, not h.DB.WithTenantTx) — health is platform-wide.

package handler

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
)

type HealthHandler struct {
	DB            *db.Pool
	AccountingURL string
	// LagThresholdSec is the cutoff above which status flips to
	// "degraded". Zero defaults to envOr/60.
	LagThresholdSec int
}

type healthzResponse struct {
	Status                 string `json:"status"`
	OutboxPending          int    `json:"outbox_pending"`
	OutboxOldestAgeSeconds int    `json:"outbox_oldest_age_seconds"`
	AccountingReachable    bool   `json:"accounting_reachable"`
}

func (h *HealthHandler) Handle(w http.ResponseWriter, r *http.Request) {
	var (
		pending int
		oldest  *time.Time
	)
	// Cheap query — short context so a hung DB doesn't stall LB
	// rotation.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_ = h.DB.Pool.QueryRow(ctx, `
		SELECT count(*), min(enqueued_at)
		  FROM posting_outbox
		 WHERE dispatched_at IS NULL
	`).Scan(&pending, &oldest)

	oldestAge := 0
	if oldest != nil {
		oldestAge = int(time.Since(*oldest).Seconds())
	}
	threshold := h.LagThresholdSec
	if threshold <= 0 {
		threshold = envIntOr("SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S", 60)
	}
	status := "ok"
	if oldestAge > threshold {
		status = "degraded"
	}
	accountingOK := canDial(h.AccountingURL, 500*time.Millisecond)
	httpx.OK(w, healthzResponse{
		Status:                 status,
		OutboxPending:          pending,
		OutboxOldestAgeSeconds: oldestAge,
		AccountingReachable:    accountingOK,
	})
}

// canDial does a TCP-level connect to the host:port from the URL.
// Doesn't auth or HTTP-round-trip — health checks should be cheap
// and not depend on the upstream's full surface working.
func canDial(rawURL string, timeout time.Duration) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			host += ":443"
		default:
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func envIntOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
