// HTTP router. Permissions follow the platform's standard
// tenant:settings:* scopes — anyone who can edit tenant settings can
// stand up a paybill; anyone who can view tenant settings can run the
// test-auth round-trip.

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/mpesa/internal/auth"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/metrics"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
)

type Deps struct {
	Paybill       *PaybillHandler
	Webhook       *WebhookHandler
	InboundEvents *InboundEventsHandler
	B2C           *B2CHandler
	TenantStore   *store.TenantStore
	Issuer        *auth.TokenIssuer
	IPAllowList   *middleware.IPAllowList
	// Phase 6 — /readyz uses the pool to check posting_outbox lag.
	// nil-safe: when omitted, /readyz degrades to a 200 OK so tests
	// + the in-process unit tests don't need a live DB.
	Pool      *db.Pool
	AppDomain string
	Logger    *slog.Logger
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"mpesa"}`))
	})

	// Phase 6 — Prometheus metrics. Public + unauthenticated by
	// convention (most observability stacks scrape over a private
	// network; the ingress is responsible for blocking the public
	// internet from hitting this).
	r.Get("/metrics", metrics.HandlerForDefault().ServeHTTP)

	// /readyz — 503 when the posting_outbox dispatcher is lagging
	// (oldest undispatched row > 60s old). Used by the orchestrator
	// to gate rolling deploys + by the load balancer to drain
	// traffic away from a lagging instance.
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if d.Pool == nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","note":"no pool wired — readyz is unconditional"}`))
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		var oldest *time.Time
		err := d.Pool.QueryRow(ctx, `
			SELECT MIN(enqueued_at) FROM posting_outbox
			 WHERE dispatched_at IS NULL
		`).Scan(&oldest)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"degraded","error":"posting_outbox query failed"}`))
			return
		}
		var lag time.Duration
		if oldest != nil {
			lag = time.Since(*oldest)
		}
		if lag > 60*time.Second {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"degraded","posting_outbox_lag_seconds":` + lagText(lag) + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","posting_outbox_lag_seconds":` + lagText(lag) + `}`))
	})

	// ─────────── Daraja-facing webhooks (no "mpesa" in URL) ───────────
	//
	// Safaricom's Daraja portal refuses validation/confirmation/result
	// URLs that contain the literal substring "mpesa". To comply, the
	// public webhook surface lives at /v1/c2b/* and /v1/b2c/* — the
	// staff/internal routes stay under /v1/mpesa/* (different prefix,
	// no shadowing).
	r.Route("/v1/c2b", func(r chi.Router) {
		r.Use(d.IPAllowList.Middleware)
		r.Post("/{paybill_id}/validation", d.Webhook.Validation)
		r.Post("/{paybill_id}/confirmation", d.Webhook.Confirmation)
	})

	if d.B2C != nil {
		r.Route("/v1/b2c", func(r chi.Router) {
			r.Use(d.IPAllowList.Middleware)
			r.Post("/{paybill_id}/result", d.B2C.Result)
			r.Post("/{paybill_id}/timeout", d.B2C.Timeout)
			r.Post("/{paybill_id}/reverse", d.B2C.Reverse)
		})
	}

	// ─────────── Staff + internal routes (keep "mpesa" prefix) ───────────
	//
	// Not exposed to Safaricom — these are admin UI calls + service-to-
	// service handoffs. The /v1/mpesa/c2b/events route is the staff
	// read surface and the /v1/mpesa/b2c/requests route is the
	// internal-token-gated enqueue path (savings → mpesa).
	r.Route("/v1/mpesa", func(r chi.Router) {
		// Internal enqueue — gated by X-Internal-Token inside the
		// handler; sits outside the JWT group so service-to-service
		// callers don't need a tenant subdomain.
		if d.B2C != nil {
			r.Post("/b2c/requests", d.B2C.Enqueue)
		}

		// JWT + tenant-scoped admin endpoints.
		r.Group(func(r chi.Router) {
			r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/paybills", d.Paybill.List)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Post("/paybills", d.Paybill.Create)
			r.With(middleware.RequirePermission("mpesa:credentials:rotate")).
				Post("/paybills/{id}/credentials", d.Paybill.PutCredential)
			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/paybills/{id}/test-auth", d.Paybill.TestAuth)
			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/c2b/events", d.InboundEvents.List)
		})
	})
	return r
}

func lagText(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs < 0 {
		secs = 0
	}
	if secs == 0 {
		return "0"
	}
	buf := make([]byte, 0, 16)
	for secs > 0 {
		buf = append([]byte{byte('0' + secs%10)}, buf...)
		secs /= 10
	}
	return string(buf)
}
