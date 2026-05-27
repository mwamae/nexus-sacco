package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type Deps struct {
	Share       *ShareHandler
	Deposit     *DepositHandler
	Product     *ProductHandler
	Interest    *InterestHandler
	Dividend    *DividendHandler
	LoanProduct *LoanProductHandler
	LoanApp     *LoanApplicationHandler
	Loan        *LoanHandler
	LoanRepay   *LoanRepaymentHandler
	LoanCollect *LoanCollectionsHandler
	LoanReports *LoanReportsHandler
	Provisioning *ProvisioningHandler
	MemberStmt   *MemberStatementHandler
	MemberLedger *MemberLedgerHandler
	Approvals   *PendingApprovalsHandler
	Collection  *CollectionDeskHandler
	VirtualTill *VirtualTillHandler
	BOSAExit    *BOSAExitHandler
	Outbox        *PostingOutboxHandler
	FeesSummary   *FeesSummaryHandler
	FinanceHealth *FinanceHealthHandler
	TenantStore   *store.TenantStore
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

	// /healthz/finance — config-integrity probe for the GL pipeline.
	// 503 when any fee_catalog.gl_credit_code resolves to a missing
	// or inactive chart_of_accounts row. Mounted ABOVE auth so
	// deployment automation can gate rollouts on it without a token.
	if d.FinanceHealth != nil {
		r.Get("/healthz/finance", d.FinanceHealth.Handle)
	}

	// Service-to-service endpoints — no JWT auth. Each handler does
	// its own token / source check. Lives under /internal so it can
	// be firewalled at the ingress.
	r.Route("/internal/v1", func(r chi.Router) {
		r.Post("/pending-approvals/{approval_id}/resolve", d.Approvals.ResolveFromWorkflow)
		r.Post("/loan-applications/{app_id}/resolve", d.LoanApp.ResolveFromWorkflow)
		// Phase 4 — mpesa's B2C result handler calls this once
		// Daraja confirms a disbursement landed on the member's
		// phone. The handler does its own X-Internal-Token check.
		r.Post("/loans/{loan_id}/finalize-disbursement", d.Loan.FinalizeDisbursement)
		// Inverse of the above: mpesa's B2C reverse handler calls
		// this when Safaricom bounces a disbursement we'd marked
		// sent. Flips the loan back to pending_disbursement + posts
		// a reversing GL entry. Same internal-token gate.
		r.Post("/loans/{loan_id}/reverse-disbursement", d.Loan.ReverseDisbursement)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			// ─────────── Share policy + register ───────────
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-policy", d.Share.GetPolicy)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Put("/share-policy", d.Share.UpdatePolicy)
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts", d.Share.List)
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts/summary", d.Share.Summary)
			r.With(middleware.RequirePermission("shares:bonus_issue")).
				Post("/share-accounts/bonus-issue", d.Share.BonusIssue)
			// Single-id getter for the admin AccountRef resolver.
			// Lighter than GetByMember (no liens/cert/policy).
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts/{id}", d.Share.GetAccountByID)

			// Virtual tills — list-all (≤5 per tenant) feeds the
			// TillLabel resolver cache.
			r.With(middleware.RequirePermission("savings:view")).
				Get("/virtual-tills", d.VirtualTill.List)

			r.Route("/share-accounts/by-counterparty/{counterparty_id}", func(r chi.Router) {
				r.With(middleware.RequirePermission("shares:view")).Get("/", d.Share.GetByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/transactions", d.Share.HistoryByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/certificate", d.Share.CurrentCertificate)
				r.With(middleware.RequirePermission("shares:buy")).Post("/purchase", d.Share.Purchase)
				r.With(middleware.RequirePermission("shares:transfer")).Post("/transfer", d.Share.Transfer)
				// Share redemption removed — share capital is equity; an
				// exiting member must transfer their shares to another
				// active member via /transfer.
				r.With(middleware.RequirePermission("shares:adjust")).Post("/adjust", d.Share.Adjust)
				r.With(middleware.RequirePermission("shares:lien")).Post("/lien", d.Share.PlaceLien)
			})
			r.With(middleware.RequirePermission("shares:lien")).
				Post("/share-liens/{lien_id}/release", d.Share.ReleaseLien)

			// ─────────── Deposit products (config) ───────────
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-products", d.Product.List)
			r.With(middleware.RequirePermission("deposits:configure")).
				Post("/deposit-products", d.Product.Create)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-products/{product_id}", d.Product.Get)
			r.With(middleware.RequirePermission("deposits:configure")).
				Put("/deposit-products/{product_id}", d.Product.Update)
			r.With(middleware.RequirePermission("deposits:configure")).
				Delete("/deposit-products/{product_id}", d.Product.Delete)

			// ─────────── Deposit accounts ───────────
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts", d.Deposit.ListAccounts)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts/summary", d.Deposit.Summary)
			r.With(middleware.RequirePermission("savings:transact")).
				Post("/deposit-accounts", d.Deposit.Open)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts/by-counterparty/{counterparty_id}", d.Deposit.AccountsByMember)

			r.Route("/deposit-accounts/{account_id}", func(r chi.Router) {
				r.With(middleware.RequirePermission("savings:view")).Get("/", d.Deposit.GetAccount)
				r.With(middleware.RequirePermission("savings:view")).Get("/statement", d.Deposit.Statement)
				r.With(middleware.RequirePermission("savings:transact")).Post("/deposit", d.Deposit.Deposit)
				r.With(middleware.RequirePermission("savings:transact")).Post("/withdraw", d.Deposit.Withdraw)
				r.With(middleware.RequirePermission("savings:transact")).Post("/withdrawal-notice", d.Deposit.GiveWithdrawalNotice)
				r.With(middleware.RequirePermission("savings:transact")).Post("/transfer", d.Deposit.TransferBetweenOwn)
				r.With(middleware.RequirePermission("deposits:reverse")).Post("/reverse", d.Deposit.Reverse)
				r.With(middleware.RequirePermission("savings:approve")).Post("/adjust", d.Deposit.Adjust)
			})

			// ─────────── BOSA exit ───────────
			// BOSA accounts can't drain via /withdraw (which 403s
			// with BOSA_WITHDRAW_FORBIDDEN). Officers route the
			// member's refund through this endpoint; the request
			// queues a Board-level approval (kind member_bosa_exit)
			// and the executor lands in a later PR.
			r.With(middleware.RequirePermission("savings:approve")).
				Post("/bosa/exit/{account_id}", d.BOSAExit.RequestExit)

			// ─────────── Interest engine ───────────
			r.With(middleware.RequirePermission("interest:view")).Get("/interest-runs", d.Interest.ListRuns)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs", d.Interest.CreateRun)
			r.With(middleware.RequirePermission("interest:view")).Get("/interest-runs/{run_id}", d.Interest.GetRunWithLines)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/compute", d.Interest.Compute)
			r.With(middleware.RequirePermission("interest:run")).Patch("/interest-run-lines/{line_id}", d.Interest.UpdateLinePayout)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/submit", d.Interest.Submit)
			r.With(middleware.RequirePermission("interest:approve")).Post("/interest-runs/{run_id}/approve", d.Interest.Approve)
			r.With(middleware.RequirePermission("interest:post")).Post("/interest-runs/{run_id}/post", d.Interest.Post)
			r.With(middleware.RequirePermission("interest:post")).Post("/interest-runs/{run_id}/lock", d.Interest.Lock)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/cancel", d.Interest.Cancel)

			// ─────────── WHT reports ───────────
			r.With(middleware.RequirePermission("interest:view")).Get("/wht-schedule", d.Interest.WHTSchedule)
			r.With(middleware.RequirePermission("interest:view")).Get("/wht-certificate/{counterparty_id}", d.Interest.WHTCertificate)

			// ─────────── Dividend engine ───────────
			r.With(middleware.RequirePermission("dividends:view")).Get("/dividend-runs", d.Dividend.ListRuns)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs", d.Dividend.CreateRun)
			r.With(middleware.RequirePermission("dividends:view")).Get("/dividend-runs/{run_id}", d.Dividend.GetRun)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/compute", d.Dividend.Compute)
			r.With(middleware.RequirePermission("dividends:run")).Patch("/dividend-run-lines/{line_id}", d.Dividend.UpdateLinePayout)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/submit", d.Dividend.Submit)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/approve", d.Dividend.Approve)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/post", d.Dividend.Post)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/lock", d.Dividend.Lock)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/cancel", d.Dividend.Cancel)

			// ─────────── Loan products + purpose categories ───────────
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-products", d.LoanProduct.List)
			r.With(middleware.RequirePermission("loans:configure")).Post("/loan-products", d.LoanProduct.Create)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-products/{product_id}", d.LoanProduct.Get)
			r.With(middleware.RequirePermission("loans:configure")).Put("/loan-products/{product_id}", d.LoanProduct.Update)
			r.With(middleware.RequirePermission("loans:configure")).Delete("/loan-products/{product_id}", d.LoanProduct.Delete)

			r.With(middleware.RequirePermission("loans:view")).Get("/loan-purpose-categories", d.LoanProduct.ListPurposeCategories)
			r.With(middleware.RequirePermission("loans:configure")).Post("/loan-purpose-categories", d.LoanProduct.CreatePurposeCategory)

			// ─────────── Loan applications + scoring ───────────
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications", d.LoanApp.List)
			r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications", d.LoanApp.Create)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}", d.LoanApp.Get)
			r.With(middleware.RequirePermission("loans:assess")).Post("/loan-applications/{app_id}/score", d.LoanApp.ReScore)
			r.With(middleware.RequirePermission("loans:approve")).Post("/loan-applications/{app_id}/approve", d.LoanApp.Approve)
			r.With(middleware.RequirePermission("loans:approve")).Post("/loan-applications/{app_id}/decline", d.LoanApp.Decline)
			// Unified Inbox CTA (PR #4) — replaces the inline approve/
			// decline buttons when the tenant has unified_inbox_enabled.
			// loans:assess covers both credit officers and reviewers
			// (who are the ones initiating the workflow); the actual
			// decision is gated per-level inside the workflow service.
			r.With(middleware.RequirePermission("loans:assess")).Post("/loan-applications/{app_id}/submit-for-decision", d.LoanApp.SubmitForDecision)

			// Guarantor consent
			r.With(middleware.RequirePermission("loans:guarantee")).Post("/loan-guarantees/{guarantee_id}/respond", d.LoanApp.GuaranteeRespond)
			// Member-scoped — every loan this member guarantees, joined
			// with borrower + loan number for the Member Profile People tab.
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-guarantees/by-counterparty/{counterparty_id}", d.LoanApp.ListByGuarantor)

			// ─────────── Loan offer + acceptance + disbursement ───────────
			r.With(middleware.RequirePermission("loans:offer")).Post("/loan-applications/{app_id}/send-offer", d.Loan.SendOffer)
			r.With(middleware.RequirePermission("loans:offer")).Post("/loan-applications/{app_id}/accept-offer", d.Loan.AcceptOffer)
			r.With(middleware.RequirePermission("loans:disburse")).Post("/loans/{loan_id}/disburse", d.Loan.Disburse)

			r.With(middleware.RequirePermission("loans:view")).Get("/loans", d.Loan.List)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/arrears-summary", d.LoanRepay.ArrearsSummary)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}", d.Loan.GetLoan)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/payoff", d.LoanRepay.Payoff)

			// ─────────── Repayment + settlement + reversal + DPD ───────────
			r.With(middleware.RequirePermission("savings:transact")).Post("/loans/{loan_id}/repay", d.LoanRepay.Repay)
			r.With(middleware.RequirePermission("savings:approve")).Post("/loans/{loan_id}/settle", d.LoanRepay.Settle)
			r.With(middleware.RequirePermission("loans:reverse")).Post("/loan-transactions/{txn_id}/reverse", d.LoanRepay.Reverse)
			r.With(middleware.RequirePermission("loans:view")).Post("/loans/{loan_id}/recalc-dpd", d.LoanRepay.RecalcDPD)

			// ─────────── Collections ───────────
			r.With(middleware.RequirePermission("collections:view")).Get("/collection-cases", d.LoanCollect.ListCases)
			r.With(middleware.RequirePermission("collections:view")).Get("/collection-cases/{case_id}", d.LoanCollect.GetCase)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/assign", d.LoanCollect.Assign)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/close", d.LoanCollect.CloseCase)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/contacts", d.LoanCollect.LogContact)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/promises", d.LoanCollect.CreatePTP)
			r.With(middleware.RequirePermission("collections:act")).Post("/promises/{ptp_id}/resolve", d.LoanCollect.ResolvePTP)

			// ─────────── Restructuring ───────────
			r.With(middleware.RequirePermission("loans:restructure")).Post("/loans/{loan_id}/reschedule", d.LoanCollect.Reschedule)
			r.With(middleware.RequirePermission("loans:restructure")).Post("/loans/{loan_id}/moratorium", d.LoanCollect.Moratorium)
			r.With(middleware.RequirePermission("loans:restructure")).Post("/loans/{loan_id}/settlement-discount", d.LoanCollect.SettlementDiscount)
			r.With(middleware.RequirePermission("loans:restructure")).Post("/loans/{loan_id}/topup-intent", d.LoanCollect.TopupIntent)
			r.With(middleware.RequirePermission("loans:restructure")).Post("/loans/{loan_id}/refinance-intent", d.LoanCollect.RefinanceIntent)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/restructurings", d.LoanCollect.ListRestructurings)

			// ─────────── Loan reports (Phase 6f) ───────────
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/portfolio", d.LoanReports.Portfolio)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/aging", d.LoanReports.Aging)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/maturing", d.LoanReports.Maturing)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/restructured", d.LoanReports.Restructured)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/writeoffs", d.LoanReports.WriteoffRegister)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/crb-submission", d.LoanReports.CRB)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-reports/by-counterparty/{counterparty_id}", d.LoanReports.MemberHistory)
			r.With(middleware.RequirePermission("loans:writeoff")).Post("/loans/{loan_id}/writeoff", d.LoanReports.WriteOff)

			// ─────────── Member 360° statement ───────────
			// Path uses /member-statements/ rather than /members/{id}/statement
			// because the latter prefix is proxied to the member service.
			r.With(middleware.RequirePermission("members:view")).Get("/member-statements/{counterparty_id}", d.MemberStmt.Get)
			// Unified ledger — UNION ALL across deposit / loan / share
			// transactions for this member, cursor-paginated by posted_at.
			// Named member-ledger (not /members/{id}/ledger) to keep the
			// /v1/members/* prefix routable to the member service while
			// this lives on savings.
			r.With(middleware.RequirePermission("members:view")).Get("/member-ledger/{counterparty_id}", d.MemberLedger.Get)

			// ─────────── Loan loss provisioning (Phase 11/3) ───────────
			r.With(middleware.RequirePermission("loans:view")).Get("/provisioning/runs", d.Provisioning.List)
			r.With(middleware.RequirePermission("loans:view")).Get("/provisioning/runs/{run_id}", d.Provisioning.Get)
			r.With(middleware.RequirePermission("interest:run")).Post("/provisioning/runs", d.Provisioning.Create)
			r.With(middleware.RequirePermission("interest:post")).Post("/provisioning/runs/{run_id}/post", d.Provisioning.Post)
			r.With(middleware.RequirePermission("interest:post")).Post("/provisioning/runs/{run_id}/supersede", d.Provisioning.Supersede)

			// ─────────── Collection Desk (single cashier's counter) ───────────
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}/outstanding", d.Collection.Outstanding)
			r.With(middleware.RequirePermission("savings:transact")).Get("/till-sessions/current", d.Collection.CurrentTillSession)
			r.With(middleware.RequirePermission("savings:transact")).Post("/receipts", d.Collection.CreateReceipt)
			r.With(middleware.RequirePermission("savings:transact")).Get("/receipts", d.Collection.ListReceipts)
			r.With(middleware.RequirePermission("savings:transact")).Get("/receipts/{id}", d.Collection.GetReceipt)
			r.With(middleware.RequirePermission("approvals:act")).Post("/receipts/{id}/lines/{line_id}/void", d.Collection.VoidLine)
			r.With(middleware.RequirePermission("savings:transact")).Post("/receipts/{id}/pdf", d.Collection.RenderPDF)
			r.With(middleware.RequirePermission("savings:view")).Get("/fees", d.Collection.ListFees)
			r.With(middleware.RequirePermission("tenant:settings:edit")).Post("/fees", d.Collection.CreateFee)
			// PR fee-coa — admin recovery for receipts whose
			// fee/welfare lines crashed at posting (most commonly
			// because the fee_catalog GL code didn't resolve in
			// the CoA; accounting 0012 + savings 0031 fixed the
			// underlying mapping).
			r.With(middleware.RequirePermission("tenant:settings:edit")).Post("/fees/replay-failed", d.Collection.ReplayFailedFeeLines)

			// Posting outbox — stuck-row viewer + per-row replay.
			// Same trust level as the fee-catalog admin paths.
			r.With(middleware.RequirePermission("tenant:settings:edit")).Get("/finance/posting-outbox", d.Outbox.ListStuck)
			r.With(middleware.RequirePermission("tenant:settings:edit")).Post("/finance/posting-outbox/{id}/replay", d.Outbox.Replay)

			// ─────────── Fees & Collections summary report ───────────
			// JSON; the matching XLSX export lives on the accounting
			// service at /v1/exports/fees-summary.xlsx so it can ride
			// the existing downloadReport() helper.
			r.With(middleware.RequirePermission("tenant:settings:view")).Get("/reports/fees-summary", d.FeesSummary.Summary)

			// ─────────── Maker-checker (Phase 7b) ───────────
			r.With(middleware.RequirePermission("approvals:view")).Get("/pending-approvals", d.Approvals.List)
			r.With(middleware.RequirePermission("approvals:view")).Get("/pending-approvals/{approval_id}", d.Approvals.Get)
			r.With(middleware.RequirePermission("approvals:act")).Post("/pending-approvals/{approval_id}/approve", d.Approvals.Approve)
			r.With(middleware.RequirePermission("approvals:act")).Post("/pending-approvals/{approval_id}/decline", d.Approvals.Decline)
			r.With(middleware.RequirePermission("savings:transact")).Post("/pending-approvals/{approval_id}/cancel", d.Approvals.Cancel)
			r.With(middleware.RequirePermission("approvals:view")).Get("/approval-settings", d.Approvals.GetSettings)
			r.With(middleware.RequirePermission("tenant:settings:edit")).Put("/approval-settings", d.Approvals.UpdateSettings)
			// Recent-changes audit feed for the Settings → Approvals
			// → Recent changes panel. Same edit permission as the
			// toggle PUT — readers of the changelog hold the same
			// trust level as editors.
			r.With(middleware.RequirePermission("tenant:settings:edit")).Get("/approval-settings/changes", d.Approvals.ListSettingsChanges)
		})

		// Workflow callbacks — public-ish (no auth) since the workflow
		// service POSTs from the same network. Each validates by looking
		// up the run via workflow_instance_id.
		r.Post("/interest-runs/callback", d.Interest.WorkflowCallback)
		r.Post("/dividend-runs/callback", d.Dividend.WorkflowCallback)
	})
	return r
}
