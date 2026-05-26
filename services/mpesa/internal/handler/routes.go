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
	Paybill     *PaybillHandler
	TenantStore *store.TenantStore
	Issuer      *auth.TokenIssuer
	AppDomain   string
	Logger      *slog.Logger
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))
	r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"mpesa"}`))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Post("/mpesa/paybills", d.Paybill.Create)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Post("/mpesa/paybills/{id}/credentials", d.Paybill.PutCredential)
			r.With(middleware.RequirePermission("tenant:settings:view")).
				Get("/mpesa/paybills/{id}/test-auth", d.Paybill.TestAuth)
		})
	})
	return r
}
