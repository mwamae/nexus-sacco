// HTTP router. Permissions follow the platform's standard
// tenant:settings:* scopes — anyone who can edit tenant settings can
// stand up a paybill; anyone who can view tenant settings can run the
// test-auth round-trip.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/mpesa/internal/auth"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
)

type Deps struct {
	Paybill        *PaybillHandler
	Webhook        *WebhookHandler
	InboundEvents  *InboundEventsHandler
	TenantStore    *store.TenantStore
	Issuer         *auth.TokenIssuer
	IPAllowList    *middleware.IPAllowList
	AppDomain      string
	Logger         *slog.Logger
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

	// ─────────── Public webhooks (no JWT, IP allow-list + token) ───────────
	// These routes do NOT pass through ResolveTenant — tenant context
	// is recovered from the (paybill_id, webhook_token) pair inside
	// the handler via the SECURITY DEFINER lookup.
	r.Route("/v1/mpesa/c2b", func(r chi.Router) {
		r.Use(d.IPAllowList.Middleware)
		r.Post("/{paybill_id}/validation",   d.Webhook.Validation)
		r.Post("/{paybill_id}/confirmation", d.Webhook.Confirmation)
	})

	// ─────────── Admin / staff JWT routes ───────────
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Post("/mpesa/paybills", d.Paybill.Create)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Post("/mpesa/paybills/{id}/credentials", d.Paybill.PutCredential)
			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/mpesa/paybills/{id}/test-auth", d.Paybill.TestAuth)

			// Staff-facing "recent inbound traffic" list — used by
			// the Settings UI panel.
			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/mpesa/c2b/events", d.InboundEvents.List)
		})
	})
	return r
}
