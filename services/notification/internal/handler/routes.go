// HTTP routes. Two scopes:
//
//   /v1/...                  — tenant-scoped (subdomain → tenant_id),
//                              JWT required, RLS applies.
//   /v1/platform/...         — platform-admin only (no tenant). Enforced
//                              by middleware.RequirePlatformAdmin.
//   /internal/v1/...         — internal service-to-service, no JWT,
//                              X-Internal-Token gate.
//   /webhooks/...            — provider callbacks, no JWT.
//   /d/{token}               — public time-limited PDF downloads.

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
	Notify          *Handler
	PlatformDrivers *PlatformDriversHandler
	Credits         *CreditsHandler
	PlatformCredits *PlatformCreditsHandler
	SMSWebhook      *SMSWebhookHandler
	SSE             *SSEHandler
	PDF             *PDFHandler
	OTP             *OTPHandler
	Campaign        *CampaignHandler
	Scheduler       *SchedulerHandler
	Template        *TemplateHandler
	TenantStore     *store.TenantStore
	Issuer          *auth.TokenIssuer
	AppDomain       string
	Logger          *slog.Logger
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

	// Webhook — tenant in URL path so we can look up the right delivery row.
	// Auth is the AT-supplied secret verified against platform SMS config.
	r.Post("/webhooks/at/delivery/{tenant_id}", d.SMSWebhook.ATDeliveryReport)

	// Public time-limited PDF download by token. No JWT (the token is the auth).
	r.Get("/d/{token}", d.PDF.PublicDownload)

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

		// Platform-admin scope. Subdomain is the platform host, so no
		// tenant context is set — we authenticate the user and check
		// for the platform-admin claim instead of RequireTenant.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequirePlatformAdmin)

			r.Route("/platform", func(r chi.Router) {
				// Shared driver config (SMTP + SMS) — only platform admins.
				r.Get("/notification-config/smtp", d.PlatformDrivers.GetSMTP)
				r.Put("/notification-config/smtp", d.PlatformDrivers.UpdateSMTP)
				r.Post("/notification-config/smtp/test", d.PlatformDrivers.TestSMTP)
				r.Get("/notification-config/sms", d.PlatformDrivers.GetSMS)
				r.Put("/notification-config/sms", d.PlatformDrivers.UpdateSMS)
				r.Post("/notification-config/sms/test", d.PlatformDrivers.TestSMS)

				// Tenant credit management.
				r.Get("/credits/tenants", d.PlatformCredits.ListTenants)
				r.Get("/credits/tenants/{tenant_id}", d.PlatformCredits.TenantDetail)
				r.Post("/credits/tenants/{tenant_id}/topup", d.PlatformCredits.Topup)
				r.Get("/credits/tenants/{tenant_id}/ledger", d.PlatformCredits.Ledger)
				r.Get("/credits/tenants/{tenant_id}/pricing", d.PlatformCredits.GetPricing)
				r.Put("/credits/tenants/{tenant_id}/pricing", d.PlatformCredits.UpdatePricing)
				r.Get("/credits/topup-requests", d.PlatformCredits.ListTopupRequests)
				r.Post("/credits/topup-requests/{id}/fulfill", d.PlatformCredits.FulfillTopupRequest)
				r.Post("/credits/topup-requests/{id}/reject", d.PlatformCredits.RejectTopupRequest)
				r.Post("/credits/tenants/{tenant_id}/adjustments", d.PlatformCredits.RequestAdjustment)
				r.Get("/credits/adjustments", d.PlatformCredits.ListAdjustments)
				r.Post("/credits/adjustments/{id}/approve", d.PlatformCredits.ApproveAdjustment)
				r.Post("/credits/adjustments/{id}/reject", d.PlatformCredits.RejectAdjustment)
				r.Get("/credits/usage-summary", d.PlatformCredits.UsageSummary)
			})
		})

		// Tenant scope.
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

			// Credit visibility — tenant-side. The provider configuration
			// endpoints from prior stages are gone; tenants only see their
			// balance and usage now.
			r.Get("/credits", d.Credits.Overview)
			r.Get("/credits/ledger", d.Credits.Ledger)
			r.Put("/credits/threshold/{channel}", d.Credits.SetLowBalanceThreshold)
			r.Get("/credits/topup-requests", d.Credits.ListTopupRequests)
			r.Post("/credits/topup-requests", d.Credits.CreateTopupRequest)
			r.Post("/credits/topup-requests/{id}/cancel", d.Credits.CancelTopupRequest)
			r.Get("/credits/blocked", d.Credits.ListBlocked)
			r.Post("/credits/blocked/{id}/retry", d.Credits.RetryBlocked)

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
