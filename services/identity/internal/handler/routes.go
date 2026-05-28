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
	Auth         *AuthHandler
	Tenant       *TenantHandler
	User         *UserHandler
	RBAC         *RBACHandler
	Settings     *SettingsHandler
	AuditH       *AuditHandler
	SystemHealth *SystemHealthHandler
	TenantStore  *store.TenantStore
	Issuer       *auth.TokenIssuer
	AppDomain    string
	Logger       *slog.Logger

	// Health is the /healthz handler built on shared/healthx.Builder
	// in main. Nil → falls back to the trivial {status:ok} string.
	Health http.HandlerFunc
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))
	r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

	// /healthz — shared healthx envelope (DB ping + version +
	// started_at). Falls back to the historical trivial response
	// when not wired (mostly bare-handler unit tests).
	if d.Health != nil {
		r.Get("/healthz", d.Health)
	} else {
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
	}

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
		r.Post("/auth/invite/accept", d.Auth.InviteAccept)

		// Authenticated routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))

			r.Get("/auth/me", d.Auth.Me)
			r.Post("/auth/mfa/email/enable", d.Auth.MFAEnableStart)
			r.Post("/auth/mfa/email/enable/confirm", d.Auth.MFAEnableConfirm)
			r.Post("/auth/mfa/disable", d.Auth.MFADisable)
			r.Post("/auth/password/change", d.Auth.PasswordChange)

			// Tenant-scoped (subdomain) routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireTenant)
				r.Get("/tenant", d.Tenant.Current)

				// Tenant settings
				r.With(middleware.RequirePermission("tenant:settings:view")).Get("/tenant/settings", d.Settings.Get)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Patch("/tenant/branding", d.Settings.UpdateBranding)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Post("/tenant/branding/logo", d.Settings.UploadLogo)
				r.With(middleware.RequirePermission("tenant:settings:view")).Get("/tenant/branding/logo", d.Settings.DownloadLogo)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Delete("/tenant/branding/logo", d.Settings.ClearLogo)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Patch("/tenant/region", d.Settings.UpdateRegion)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Patch("/tenant/operations", d.Settings.UpdateOperations)
				r.With(middleware.RequirePermission("tenant:settings:edit")).Patch("/tenant/membership", d.Settings.UpdateMembership)
			})

			// Staff management — works on tenant subdomain OR platform host.
			// The handler resolves the effective tenant (platform pseudo-tenant
			// when on platform host) so platform super-admins can manage
			// their own staff the same way tenant owners manage theirs.
			r.With(middleware.RequirePermission("roles:view")).Get("/permissions", d.RBAC.ListPermissions)
			r.With(middleware.RequirePermission("audit:view")).Get("/audit/by-target/{kind}/{id}", d.AuditH.ByTarget)

			// /v1/platform-status — slim read-only summary that any
			// authenticated user (tenant staff included) can poll to
			// learn whether the platform is operational. Returns
			// {overall_status, checked_at, message} only; no service
			// internals leak. The full aggregator moved to
			// /v1/platform/system-health under the RequirePlatform
			// group below.
			if d.SystemHealth != nil {
				r.Get("/platform-status", d.SystemHealth.GetForTenant)
			}
			r.With(middleware.RequirePermission("roles:view")).Get("/roles", d.RBAC.ListRoles)
			r.With(middleware.RequirePermission("roles:view")).Get("/roles/{id}", d.RBAC.GetRole)
			r.With(middleware.RequirePermission("roles:edit")).Post("/roles", d.RBAC.CreateRole)
			r.With(middleware.RequirePermission("roles:edit")).Patch("/roles/{id}", d.RBAC.UpdateRole)
			r.With(middleware.RequirePermission("roles:edit")).Delete("/roles/{id}", d.RBAC.DeleteRole)

			r.With(middleware.RequirePermission("users:view")).Get("/users", d.User.List)
			r.With(middleware.RequirePermission("users:view")).Get("/users/{id}", d.User.Get)
			r.With(middleware.RequirePermission("users:invite")).Post("/users/invite", d.User.Invite)
			r.With(middleware.RequirePermission("users:edit")).Patch("/users/{id}", d.User.Update)
			r.With(middleware.RequirePermission("users:suspend")).Post("/users/{id}/status", d.User.SetStatus)
			r.With(middleware.RequirePermission("users:invite")).Post("/users/{id}/invite/resend", d.User.ResendInvite)
			r.With(middleware.RequirePermission("roles:edit")).Post("/users/{id}/roles", d.User.AssignRole)
			r.With(middleware.RequirePermission("roles:edit")).Delete("/users/{id}/roles/{role_id}", d.User.UnassignRole)

			// Platform-scoped routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePlatform)

				// System Health aggregator — full fan-out across every
				// service's /healthz + worker_heartbeats + infrastructure.
				// Platform-only because service health is platform-wide;
				// tenants see the slim /v1/platform-status instead.
				if d.SystemHealth != nil {
					r.With(middleware.RequirePermission("platform:operations:view")).
						Get("/platform/system-health", d.SystemHealth.Get)
				}

				r.Get("/platform/tenants", d.Tenant.List)
				r.Post("/platform/tenants", d.Tenant.Create)
				r.Get("/platform/tenants/{id}", d.Tenant.Get)
				r.Post("/platform/tenants/{id}/status", d.Tenant.SetStatus)
				r.Post("/platform/tenants/{id}/restrictions", d.Tenant.SetRestrictions)
				r.Post("/platform/tenants/{id}/archive", d.Tenant.Archive)
				r.Get("/platform/tenants/{id}/export", d.Tenant.Export)
				r.Get("/platform/tenants/{id}/backup", d.Tenant.Backup)

				// Contacts (add / edit / remove)
				r.Post("/platform/tenants/{id}/contacts", d.Tenant.AddContact)
				r.Patch("/platform/tenants/{id}/contacts/{contact_id}", d.Tenant.UpdateContact)
				r.Delete("/platform/tenants/{id}/contacts/{contact_id}", d.Tenant.DeleteContact)

				// Staff users (list + invite). Reuses UserHandler internals.
				r.Get("/platform/tenants/{id}/users", d.Tenant.ListUsers)
				r.Post("/platform/tenants/{id}/users/invite", d.Tenant.InviteUser)
				r.Post("/platform/tenants/{id}/users/{user_id}/invite/resend", d.Tenant.ResendUserInvite)
				r.Post("/platform/tenants/{id}/users/{user_id}/suspend", d.Tenant.SuspendUser)
				r.Post("/platform/tenants/{id}/users/{user_id}/reactivate", d.Tenant.ReactivateUser)
				r.Post("/platform/tenants/{id}/users/{user_id}/password-reset", d.Tenant.ForcePasswordReset)
				r.Post("/platform/tenants/{id}/users/{user_id}/revoke", d.Tenant.RevokeUser)
			})
		})
	})

	return r
}
