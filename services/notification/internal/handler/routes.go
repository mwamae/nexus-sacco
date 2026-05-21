package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/notification/internal/auth"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type Deps struct {
	Notify      *Handler
	SMTP        *SMTPHandler
	SMS         *SMSHandler
	SSE         *SSEHandler
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

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Internal endpoint — no tenant subdomain, no JWT, X-Internal-Token gate.
	r.Post("/internal/v1/notify", d.Notify.Notify)

	// Webhooks — tenant in URL path so RLS still applies. No JWT.
	r.Post("/webhooks/at/delivery/{tenant_id}", d.SMS.ATDeliveryReport)

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			r.Get("/notifications/stream", d.SSE.Stream)
			r.Get("/notifications", d.Notify.Feed)
			r.Get("/notifications/unread", d.Notify.UnreadCount)
			r.Post("/notifications/mark-all-read", d.Notify.MarkAllRead)
			r.Post("/notifications/{id}/read", d.Notify.MarkRead)
			r.Get("/notifications/log", d.Notify.Log)
			r.Get("/notification-events", d.Notify.ListEvents)
			r.Get("/notification-templates", d.Notify.ListTemplates)

			r.Get("/notification-config/smtp", d.SMTP.Get)
			r.Put("/notification-config/smtp", d.SMTP.Update)
			r.Post("/notification-config/smtp/test", d.SMTP.Test)

			r.Get("/notification-config/sms", d.SMS.Get)
			r.Put("/notification-config/sms", d.SMS.Update)
			r.Post("/notification-config/sms/test", d.SMS.Test)
		})
	})

	return r
}
