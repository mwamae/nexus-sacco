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
	CoA          *CoAHandler
	Periods      *PeriodHandler
	Journals     *JournalHandler
	Reports      *ReportHandler
	FiscalYear   *FiscalYearHandler
	Bank         *BankHandler
	Cash         *CashHandler
	InternalPost *InternalPostHandler
	TenantStore  *store.TenantStore
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

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Internal service-to-service posting. No JWT — gated by the
	// shared X-Internal-Token header. Tenant id is passed in the body.
	r.Post("/internal/v1/post", d.InternalPost.Post)

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
			r.Get("/reports/balance-sheet", d.Reports.BalanceSheet)
			r.Get("/reports/income-statement", d.Reports.IncomeStatement)
			r.Get("/reports/changes-in-equity", d.Reports.ChangesInEquity)
			r.Get("/reports/cash-flow", d.Reports.CashFlow)
			r.Get("/reports/gl-detail/{account_id}", d.Reports.GLDetail)

			// Fiscal year close
			r.Get("/fiscal-years", d.FiscalYear.List)
			r.Post("/fiscal-years/{year}/close", d.FiscalYear.Close)

			// Bank reconciliation
			r.Get("/bank-accounts", d.Bank.ListAccounts)
			r.Post("/bank-accounts", d.Bank.CreateAccount)
			r.Get("/bank-accounts/{id}", d.Bank.GetAccount)
			r.Patch("/bank-accounts/{id}", d.Bank.UpdateAccount)
			r.Get("/bank-accounts/{id}/statements", d.Bank.ListStatements)
			r.Post("/bank-accounts/{id}/statements", d.Bank.UploadStatement)
			r.Get("/bank-accounts/{id}/reconciliation", d.Bank.Reconciliation)

			r.Get("/bank-statements/{id}", d.Bank.GetStatement)
			r.Get("/bank-statement-lines/{id}/suggest-matches", d.Bank.SuggestMatches)
			r.Post("/bank-statement-lines/{id}/match", d.Bank.Match)
			r.Post("/bank-statement-lines/{id}/unmatch", d.Bank.Unmatch)
			r.Post("/bank-statement-lines/{id}/exclude", d.Bank.Exclude)
			r.Post("/bank-statement-lines/{id}/post-adjustment", d.Bank.PostAdjustment)

			// Cash & Float management
			r.Get("/tills", d.Cash.ListTills)
			r.Post("/tills", d.Cash.CreateTill)
			r.Get("/tills/{id}", d.Cash.GetTill)
			r.Get("/tills/{id}/sessions", d.Cash.ListTillSessions)
			r.Post("/till-sessions", d.Cash.OpenSession)
			r.Get("/till-sessions/{id}", d.Cash.GetSession)
			r.Post("/till-sessions/{id}/close", d.Cash.CloseSession)
			r.Get("/cash-transfers", d.Cash.ListTransfers)
			r.Post("/cash-transfers", d.Cash.CreateTransfer)
			r.Get("/cash-position", d.Cash.CashPosition)
		})
	})

	return r
}
