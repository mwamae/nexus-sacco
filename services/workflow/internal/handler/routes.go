package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/workflow/internal/auth"
	"github.com/nexussacco/workflow/internal/middleware"
	"github.com/nexussacco/workflow/internal/store"
)

type Deps struct {
	Definitions *DefinitionHandler
	Instances   *InstanceHandler
	InboxStatus *InboxStatusHandler
	TenantStore *store.TenantStore
	Issuer      *auth.TokenIssuer
	AppDomain   string
	Logger      *slog.Logger

	// Health is the /healthz handler built on shared/healthx.Builder
	// in main. Nil → falls back to the trivial {status:ok} response.
	Health http.HandlerFunc
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))
	r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

	if d.Health != nil {
		r.Get("/healthz", d.Health)
	} else {
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
	}

	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			// Definitions — workflow:configure to mutate, workflow:view to read.
			r.With(middleware.RequirePermission("workflow:view")).Get("/workflows", d.Definitions.List)
			r.With(middleware.RequirePermission("workflow:view")).Get("/workflows/{id}", d.Definitions.Get)
			r.With(middleware.RequirePermission("workflow:configure")).Post("/workflows", d.Definitions.Create)
			r.With(middleware.RequirePermission("workflow:configure")).Post("/workflows/{id}/activation", d.Definitions.SetActivation)

			// Dashboard.
			r.With(middleware.RequirePermission("workflow:view")).Get("/workflows/dashboard", d.Instances.Dashboard)

			// Instances. workflow:view to list/inspect; actions gated by level
			// role + permission inside the handler. Create requires workflow:view
			// so host services can call in with their own token.
			r.With(middleware.RequirePermission("workflow:view")).Get("/workflow-instances", d.Instances.List)
			r.With(middleware.RequirePermission("workflow:view")).Get("/workflow-instances/{id}", d.Instances.Get)
			r.With(middleware.RequirePermission("workflow:view")).Post("/workflow-instances", d.Instances.Create)
			r.With(middleware.RequirePermission("workflow:view")).Post("/workflow-instances/{id}/actions", d.Instances.Action)

			// Unified Inbox additions: claim/release lock + threaded comments.
			r.With(middleware.RequirePermission("workflow:view")).Post("/workflow-instances/{id}/claim", d.Instances.Claim)
			r.With(middleware.RequirePermission("workflow:view")).Post("/workflow-instances/{id}/release", d.Instances.Release)
			r.With(middleware.RequirePermission("workflow:view")).Post("/workflow-instances/{id}/comments", d.Instances.Comment)

			// Inbox-status probe — frontend gates the /cash-approvals
			// deprecation banner off this.
			r.With(middleware.RequirePermission("workflow:view")).Get("/inbox-status", d.InboxStatus.Get)
		})
	})
	return r
}
