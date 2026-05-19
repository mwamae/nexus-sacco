// Route registration. Uses chi for grouping + path params.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type Deps struct {
	Auth        *AuthHandler
	Tenant      *TenantHandler
	User        *UserHandler
	TenantStore *store.TenantStore
	Issuer      *auth.TokenIssuer
	AppDomain   string
	Logger      *slog.Logger
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))
	r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

	// Health
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Route("/v1", func(r chi.Router) {

		// Auth endpoints accept BOTH tenant subdomain (member login) and
		// platform host (platform-admin login). The Login handler picks
		// the right tenant context internally.
		r.Post("/auth/login", d.Auth.Login)
		r.Post("/auth/refresh", d.Auth.Refresh)
		r.Post("/auth/logout", d.Auth.Logout)
		r.Post("/auth/mfa/verify", d.Auth.MFAVerify)
		r.Post("/auth/password/forgot", d.Auth.PasswordForgot)
		r.Post("/auth/password/reset", d.Auth.PasswordReset)

		// Authenticated routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))

			r.Get("/auth/me", d.Auth.Me)
			r.Post("/auth/mfa/email/enable", d.Auth.MFAEnableStart)
			r.Post("/auth/mfa/email/enable/confirm", d.Auth.MFAEnableConfirm)
			r.Post("/auth/mfa/disable", d.Auth.MFADisable)
			r.Post("/auth/password/change", d.Auth.PasswordChange)

			// Tenant-scoped routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireTenant)
				r.Get("/tenant", d.Tenant.Current)
				r.With(middleware.RequirePermission("users:view")).Get("/users", d.User.List)
				r.With(middleware.RequirePermission("users:invite")).Post("/users", d.User.Invite)
				r.With(middleware.RequirePermission("roles:view")).Get("/roles", d.User.ListRoles)
			})

			// Platform-scoped routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePlatform)
				r.Get("/platform/tenants", d.Tenant.List)
				r.Post("/platform/tenants", d.Tenant.Create)
			})
		})
	})

	return r
}
