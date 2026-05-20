package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type Deps struct {
	Share       *ShareHandler
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
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			// Tenant share policy.
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-policy", d.Share.GetPolicy)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Put("/share-policy", d.Share.UpdatePolicy)

			// ─────────── Share register (tenant-wide) ───────────
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts", d.Share.List)
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts/summary", d.Share.Summary)
			r.With(middleware.RequirePermission("shares:bonus_issue")).
				Post("/share-accounts/bonus-issue", d.Share.BonusIssue)

			// ─────────── Per-member operations ───────────
			// Routed under /share-accounts/by-member/{member_id}/* so the
			// Vite proxy can cleanly send them to the savings service
			// without colliding with /v1/members/* (member service).
			r.Route("/share-accounts/by-member/{member_id}", func(r chi.Router) {
				r.With(middleware.RequirePermission("shares:view")).Get("/", d.Share.GetByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/transactions", d.Share.HistoryByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/certificate", d.Share.CurrentCertificate)
				r.With(middleware.RequirePermission("shares:buy")).Post("/purchase", d.Share.Purchase)
				r.With(middleware.RequirePermission("shares:transfer")).Post("/transfer", d.Share.Transfer)
				r.With(middleware.RequirePermission("shares:redeem")).Post("/redeem", d.Share.Redeem)
				r.With(middleware.RequirePermission("shares:adjust")).Post("/adjust", d.Share.Adjust)
				r.With(middleware.RequirePermission("shares:lien")).Post("/lien", d.Share.PlaceLien)
			})

			// Lien release operates by lien id (one-off RPC).
			r.With(middleware.RequirePermission("shares:lien")).
				Post("/share-liens/{lien_id}/release", d.Share.ReleaseLien)
		})
	})
	return r
}
