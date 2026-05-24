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
	Member       *MemberHandler
	Org          *OrgHandler
	Status       *StatusHandler
	Applications  *ApplicationHandler
	Counterparties *CounterpartyHandler
	TenantStore   *store.TenantStore
	Issuer       *auth.TokenIssuer
	AppDomain    string
	Logger       *slog.Logger
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

			// Documents. Phase E A: route by counterparty.id directly;
			// the URL contract for member-level documents now matches the
			// savings-side /by-counterparty/ pattern from sub-PR 3.
			r.With(middleware.RequirePermission("members:create")).
				Post("/counterparties/{id}/documents/{kind}", d.Member.UploadDocument)
			r.With(middleware.RequirePermission("members:view")).
				Get("/counterparties/{id}/documents/{kind}", d.Member.DownloadDocument)

			// ─────────── Organisations (non-individual members) ───────────
			// Reuses the members:* permission catalog so existing roles
			// (tenant_owner, sacco_admin, branch_manager) work unchanged.
			r.With(middleware.RequirePermission("members:view")).Get("/orgs", d.Org.List)
			r.With(middleware.RequirePermission("members:view")).Get("/orgs/{id}", d.Org.Get)
			r.With(middleware.RequirePermission("members:create")).Post("/orgs", d.Org.Create)
			r.With(middleware.RequirePermission("members:approve")).Post("/orgs/{id}/approve", d.Org.Approve)
			r.With(middleware.RequirePermission("members:approve")).Post("/orgs/{id}/reject", d.Org.Reject)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/status", d.Org.SetStatus)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/kyc-status", d.Org.SetKYCStatus)

			// Org documents.
			r.With(middleware.RequirePermission("members:create")).Post("/orgs/{id}/documents/{kind}", d.Org.UploadDocument)
			r.With(middleware.RequirePermission("members:view")).Get("/orgs/{id}/documents/{kind}", d.Org.DownloadDocument)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/documents/{kind}/verify", d.Org.VerifyDocument)

			// Officials + per-official files + sanctions.
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/officials", d.Org.AddOfficial)
			r.With(middleware.RequirePermission("members:edit")).Delete("/orgs/{id}/officials/{official_id}", d.Org.DeleteOfficial)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/officials/{official_id}/sanctions", d.Org.ScreenOfficial)
			r.With(middleware.RequirePermission("members:create")).Post("/orgs/{id}/officials/{official_id}/files/{kind}", d.Org.UploadOfficialFile)
			r.With(middleware.RequirePermission("members:view")).Get("/orgs/{id}/officials/{official_id}/files/{kind}", d.Org.DownloadOfficialFile)

			// Signatories + mandate + banking + contacts.
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/signatories", d.Org.ReplaceSignatories)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/mandate", d.Org.SetMandate)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/banking", d.Org.UpsertBanking)
			r.With(middleware.RequirePermission("members:edit")).Post("/orgs/{id}/contacts", d.Org.ReplaceContacts)

			// ─────────── Status lifecycle ───────────
			// Phase E A: /counterparties/{id}/status-* — the URL value is
			// a counterparty.id directly; handler + store accept it
			// without an internal bridge.
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}/status-actions", d.Status.Actions)
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}/status-history", d.Status.History)
			r.With(middleware.RequirePermission("members:edit")).Post("/counterparties/{id}/status-change", d.Status.Change)
			r.With(middleware.RequirePermission("members:edit")).Post("/counterparties/{id}/status-supporting-doc", d.Status.UploadSupportingDoc)
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}/status-history/{change_id}/doc", d.Status.DownloadSupportingDoc)
			r.With(middleware.RequirePermission("members:view")).Get("/members/status/summary", d.Status.Summary)
			r.With(middleware.RequirePermission("members:view")).Get("/members/status/counts", d.Status.Counts)

			// ─────────── Unified counterparty register (Phase B) ───────────
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties", d.Counterparties.List)
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}", d.Counterparties.Get)
			r.With(middleware.RequirePermission("members:create")).Post("/counterparties", d.Counterparties.Create)
			r.With(middleware.RequirePermission("members:edit")).Patch("/counterparties/{id}", d.Counterparties.Patch)
			r.With(middleware.RequirePermission("members:edit")).Post("/members/dormancy/preview", d.Status.DormancyPreview)
			r.With(middleware.RequirePermission("members:edit")).Post("/members/dormancy/run", d.Status.DormancyRun)
			// PR #6 — Unified Inbox CTA for bulk dormancy.
			r.With(middleware.RequirePermission("members:edit")).Post("/members/dormancy/submit-for-approval", d.Status.SubmitDormancyForApproval)

			// ─────────── Membership applications (unified pipeline) ───────────
			r.With(middleware.RequirePermission("members:create")).Post("/applications", d.Applications.Create)
			r.With(middleware.RequirePermission("members:view")).Get("/applications", d.Applications.List)
			r.With(middleware.RequirePermission("members:view")).Get("/applications/checklist-items", d.Applications.ListChecklistItems)
			r.With(middleware.RequirePermission("members:view")).Get("/applications/{id}", d.Applications.Get)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/start-review", d.Applications.StartReview)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/return-for-correction", d.Applications.ReturnForCorrection)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/resubmit", d.Applications.Resubmit)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/submit-for-approval", d.Applications.SubmitForApproval)
			r.With(middleware.RequirePermission("members:approve")).Post("/applications/{id}/approve", d.Applications.Approve)
			r.With(middleware.RequirePermission("members:approve")).Post("/applications/{id}/decline", d.Applications.Decline)
			r.With(middleware.RequirePermission("members:approve")).Post("/applications/{id}/return-to-reviewer", d.Applications.ReturnToReviewer)
			// PR #8 — Unified Inbox CTA. Replaces the inline Approve/
			// Decline buttons when the tenant has unified_inbox_enabled.
			// members:edit (not members:approve) so credit officers /
			// reviewers can initiate; the actual decision is gated per-
			// level inside the workflow service.
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/submit-for-onboarding-decision", d.Applications.SubmitForOnboardingDecision)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/withdraw", d.Applications.Withdraw)
			r.With(middleware.RequirePermission("members:edit")).Post("/applications/{id}/checklist", d.Applications.RespondChecklist)
			r.With(middleware.RequirePermission("members:approve")).Post("/applications/{id}/post-refund", d.Applications.PostRefund)

			// Late-fee capture (post-submission). Each successful POST
			// inserts an application_fee_payments row + posts a GL
			// journal entry; the parent application's denormalised
			// fee_* fields are recomputed from those rows.
			r.With(middleware.RequirePermission("members:view")).
				Get("/applications/{id}/fee-payments", d.Applications.ListFeePayments)
			r.With(middleware.RequirePermission("members:edit")).
				Post("/applications/{id}/fee-payments", d.Applications.RecordFeePayment)
			r.With(middleware.RequirePermission("members:approve")).
				Post("/applications/{id}/fee-payments/{paymentId}/void", d.Applications.VoidFeePayment)
		})

		// Workflow callback — public-ish (no auth) but constrained: only
		// resolves proposals it knows about, and only the first time.
		r.Post("/members/status/callback", d.Status.WorkflowCallback)
	})
	// PR #6 — Unified Inbox callback for the dormancy gate. Lives
	// under /internal/v1 to match the rest of the consolidation
	// (savings, accounting); the handler's own X-Internal-Token +
	// User-Agent gate protects it.
	r.Post("/internal/v1/members/dormancy/resolve", d.Status.ResolveDormancyFromWorkflow)
	// PR #8 — Unified Inbox callback for membership-application
	// onboarding decisions.
	r.Post("/internal/v1/applications/{id}/resolve", d.Applications.ResolveFromWorkflow)
	return r
}
