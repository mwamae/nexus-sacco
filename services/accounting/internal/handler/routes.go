// HTTP routes for the accounting service.
//
//   /healthz                            liveness
//   /v1/coa                             chart of accounts CRUD
//   /v1/periods                         accounting period management
//   /v1/journal-entries                 manual entries (maker/checker)
//   /v1/reports/trial-balance           trial balance
//   /v1/reports/gl-detail/{account_id}  per-account ledger
//
// Foundation phase ships read+manual paths; the /internal/v1/post
// endpoint that other services will hit for auto-posting lands in
// the integration phase.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/accounting/internal/auth"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

type Deps struct {
	CoA         *CoAHandler
	Periods     *PeriodHandler
	Journals    *JournalHandler
	Reports     *ReportHandler
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

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			// Chart of Accounts
			r.Get("/coa", d.CoA.List)
			r.Post("/coa", d.CoA.Create)
			r.Get("/coa/{id}", d.CoA.Get)
			r.Patch("/coa/{id}", d.CoA.Update)

			// Accounting periods
			r.Get("/periods", d.Periods.List)
			r.Post("/periods/{id}/close", d.Periods.Close)
			r.Post("/periods/{id}/reopen", d.Periods.Reopen)

			// Journal entries — list / get / draft / approve / reject
			r.Get("/journal-entries", d.Journals.List)
			r.Post("/journal-entries", d.Journals.Create)
			r.Get("/journal-entries/{id}", d.Journals.Get)
			r.Post("/journal-entries/{id}/approve", d.Journals.Approve)
			r.Post("/journal-entries/{id}/reject", d.Journals.Reject)

			// Reports
			r.Get("/reports/trial-balance", d.Reports.TrialBalance)
			r.Get("/reports/gl-detail/{account_id}", d.Reports.GLDetail)
		})
	})

	return r
}
