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
	PDF         *PDFHandler
	OTP         *OTPHandler
	Campaign    *CampaignHandler
	Scheduler   *SchedulerHandler
	Template    *TemplateHandler
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
	r.Post("/internal/v1/pdf/generate", d.PDF.GenerateInternal)
	r.Post("/internal/v1/otp/request", d.OTP.RequestInternal)
	r.Post("/internal/v1/otp/verify", d.OTP.VerifyInternal)

	// Webhooks — tenant in URL path so RLS still applies. No JWT.
	r.Post("/webhooks/at/delivery/{tenant_id}", d.SMS.ATDeliveryReport)

	// Public time-limited PDF download by token. No JWT (the token is the auth).
	r.Get("/d/{token}", d.PDF.PublicDownload)

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
			r.Post("/notification-templates", d.Template.Create)
			r.Post("/notification-templates/preview", d.Template.Preview)
			r.Get("/notification-templates/{id}", d.Template.Get)
			r.Put("/notification-templates/{id}", d.Template.Update)
			r.Delete("/notification-templates/{id}", d.Template.Delete)
			r.Post("/notification-templates/{id}/clone", d.Template.Clone)

			r.Get("/notification-config/smtp", d.SMTP.Get)
			r.Put("/notification-config/smtp", d.SMTP.Update)
			r.Post("/notification-config/smtp/test", d.SMTP.Test)

			r.Get("/notification-config/sms", d.SMS.Get)
			r.Put("/notification-config/sms", d.SMS.Update)
			r.Post("/notification-config/sms/test", d.SMS.Test)

			r.Get("/pdf-documents", d.PDF.List)
			r.Get("/pdf-documents/{id}", d.PDF.Get)
			r.Get("/pdf-documents/{id}/download", d.PDF.Download)

			r.Get("/otp-settings", d.OTP.GetSettings)
			r.Put("/otp-settings", d.OTP.UpdateSettings)
			r.Get("/otp-requests", d.OTP.ListRequests)

			// Campaigns
			r.Get("/campaigns", d.Campaign.List)
			r.Post("/campaigns", d.Campaign.Create)
			r.Get("/campaigns/{id}", d.Campaign.Get)
			r.Post("/campaigns/{id}/preview", d.Campaign.Preview)
			r.Post("/campaigns/{id}/schedule", d.Campaign.Schedule)
			r.Post("/campaigns/{id}/send", d.Campaign.Send)
			r.Post("/campaigns/{id}/cancel", d.Campaign.Cancel)
			r.Get("/campaign-settings", d.Campaign.GetSettings)
			r.Put("/campaign-settings", d.Campaign.UpdateSettings)

			// Scheduled jobs
			r.Get("/scheduled-jobs", d.Scheduler.List)
			r.Post("/scheduled-jobs/preview-cron", d.Scheduler.PreviewCron)
			r.Get("/scheduled-jobs/{id}", d.Scheduler.Get)
			r.Put("/scheduled-jobs/{id}", d.Scheduler.Update)
			r.Post("/scheduled-jobs/{id}/run", d.Scheduler.Run)
			r.Get("/scheduled-jobs/{id}/runs", d.Scheduler.ListRuns)
		})
	})

	return r
}
