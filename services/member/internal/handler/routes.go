package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/member/internal/auth"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/store"
)

type Deps struct {
	Member      *MemberHandler
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

			r.With(middleware.RequirePermission("members:view")).Get("/members", d.Member.List)
			r.With(middleware.RequirePermission("members:view")).Get("/members/{id}", d.Member.Get)
			r.With(middleware.RequirePermission("members:create")).Post("/members", d.Member.Create)
			r.With(middleware.RequirePermission("members:approve")).Post("/members/{id}/approve", d.Member.Approve)
			r.With(middleware.RequirePermission("members:approve")).Post("/members/{id}/reject", d.Member.Reject)
			r.With(middleware.RequirePermission("members:edit")).Post("/members/{id}/status", d.Member.SetStatus)

			// Documents.
			r.With(middleware.RequirePermission("members:create")).
				Post("/members/{id}/documents/{kind}", d.Member.UploadDocument)
			r.With(middleware.RequirePermission("members:view")).
				Get("/members/{id}/documents/{kind}", d.Member.DownloadDocument)
		})
	})
	return r
}
