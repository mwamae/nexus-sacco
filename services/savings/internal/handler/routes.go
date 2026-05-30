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
	// WFTerminal receives the workflow callback-dispatcher's POST
	// when a savings-owned wf_instance reaches a terminal state.
	// Optional — early-boot tests + the FinanceHealth-only main.go
	// variants leave it nil and the route below short-circuits to 404.
	WFTerminal *WorkflowTerminalCallbackHandler
	// LoanDashboard backs GET /v1/loans/dashboard — Phase 1
	// aggregator that returns every KPI the Loans → Dashboard page
	// renders in one round trip. Optional in test main.go variants
	// (route omits when nil).
	LoanDashboard *LoanDashboardHandler
	// LoanReportsP2 backs the Loans Phase 2 reporting endpoints
	// under /v1/loans/reports/*. Optional in test main.go variants.
	LoanReportsP2 *LoanReportsPhase2Handler
	// SASRA backs the SASRA quarterly extract endpoints. Permission-
	// gated on loans:sasra (sensitive — generates the file SACCOs
	// upload to the regulator's portal).
	SASRA *SASRAHandler
	// LoanProvisioningV2 backs the Phase 3 provisioning endpoints
	// mounted under /v1/loans/provisioning/*. Reads loan_dpd_snapshots
	// + ecl_rate_matrix. Optional in test main.go variants.
	LoanProvisioningV2 *LoanProvisioningV2Handler
	// LoanPolicy backs /v1/loans/policy/* (DPD thresholds + ECL matrix
	// editor) and /v1/loans/{id}/classification-history. Optional.
	LoanPolicy *LoanPolicyHandler
	// GuarantorSMSPolicy — Phase 5. Mounted at
	// /v1/loans/policy/guarantor-sms (GET + PUT). Same loans:policy:write
	// permission as LoanPolicy.
	GuarantorSMSPolicy *GuarantorSMSPolicyHandler
	// LoanCollectionsEvents — Phase 4 workflow surface mounted under
	// /v1/loans/{loan_id}/collections/* plus tenant-wide reads
	// (/v1/loans/collections/queue, /v1/loans/collections/ptp-summary).
	// Sits alongside the legacy /v1/collection-cases/* surface.
	LoanCollectionsEvents *LoanCollectionsEventsHandler
	// DividendOffset — Phase 4 dividend offset preview + manual post.
	// Mounted at /v1/dividends/runs/{run_id}/arrears-offset-*.
	DividendOffset *DividendOffsetHandler
	// Checkoff — Phase 5 salary check-off batch upload + validate + post.
	Checkoff *CheckoffHandler
	// CRB — Phase 6 credit reference bureau pulls.
	CRB *CRBHandler
	// Insurance — Phase 6 credit-life insurance policy management.
	Insurance *InsuranceHandler
	// GuarantorCapacity — answers "how much can this counterparty
	// commit as a new guarantee right now?" Used by the
	// new-loan-application form to show inline capacity hints.
	GuarantorCapacity *GuarantorCapacityHandler
	// QualifyingAmount — pre-application snapshot of the multiplier
	// ceiling so the form can show "you qualify for up to KES X" and
	// block over-ceiling submissions (which the scorer auto-declines).
	QualifyingAmount *QualifyingAmountHandler
	// GuarantorConsent — admin respond-with-proof + portal self-service.
	GuarantorConsent *GuarantorConsentHandler
	// PublicConsent — no-auth /p/guarantor-consent/* endpoints driven
	// by tokenised SMS links. Mounted outside the auth middleware.
	PublicConsent *PublicGuarantorConsentHandler
	// Collateral — Phase 1.5a lifecycle endpoints (verify/value/pledge/
	// release) + security-coverage card. Mounted under /v1.
	Collateral *CollateralHandler
	// Phase 1.5b — charge / insurance / custody / auction endpoints.
	CollateralAdvanced *CollateralAdvancedHandler
	// Phase 1.5b — third-party pledger admin endpoints (issue SMS,
	// record offline consent).
	PledgerConsent *PledgerConsentHandler
	// Phase 1.5b — public pledger consent endpoints (no auth).
	PublicPledgerConsent *PublicPledgerConsentHandler
	// Phase 1.5b — collateral reports (exposure, by-kind, valuations
	// expiring, insurance expiring, charge registration status).
	CollateralReports *CollateralReportsHandler
	// Phase-1 follow-up — Documents / Comments / Score history.
	LoanDocs        *LoanDocumentsHandler
	LoanComments    *LoanCommentsHandler
	PublicComments  *PublicCommentsHandler
	// Phase-1 follow-up — valuation report file upload + download.
	ValuationReport *ValuationReportHandler
	// DSID Phase 2.1 — member statement PDFs + email; WHT remittance.
	MemberStatementsPDF *MemberStatementsPDFHandler
	WHTRemittance       *WHTRemittanceHandler
	// DSID Phase 2.2 — standing orders.
	StandingOrders *StandingOrdersHandler
	// DSID Phase 2.2 — dormant account reactivation.
	DepositReactivation *DepositReactivationHandler
	// DSID Phase 2.2 — joint accounts.
	JointAccounts *JointAccountsHandler
	// DSID Phase 2.2 — per-product recurring fees.
	RecurringFees *RecurringFeesHandler
	// Health is the /healthz handler — produced by NewHealthBuilder().Handler(...)
	// in main. Falls back to a trivial {status:ok} when nil (early-boot
	// tests + the FinanceHealth-only main.go variants).
	Health      http.HandlerFunc
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

	// /healthz — DB + accounting probes + outbox-lag self-report,
	// built on the shared healthx.Builder. Falls back to the trivial
	// {status:ok} when the Builder isn't wired (early-boot tests +
	// the FinanceHealth-only main.go variants).
	if d.Health != nil {
		r.Get("/healthz", d.Health)
		// /v1/finance/health — same payload exposed under the
		// /v1/finance/* tree so the admin UI's vite proxy can route
		// it through the existing per-prefix entries. Plain /healthz
		// stays as the container/LB-facing probe; this is the
		// UI-facing alias.
		r.Get("/v1/finance/health", d.Health)
	} else {
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
	}

	// /healthz/finance — config-integrity probe for the GL pipeline.
	// 503 when any fee_catalog.gl_credit_code resolves to a missing
	// or inactive chart_of_accounts row. Mounted ABOVE auth so
	// deployment automation can gate rollouts on it without a token.
	if d.FinanceHealth != nil {
		r.Get("/healthz/finance", d.FinanceHealth.Handle)
	}

	// ─────────── Public guarantor-consent endpoints ───────────
	//
	// /p/* is the no-auth wing for SMS-link-driven flows. The URL
	// token IS the credential; ID + OTP verification on the public
	// page proves the visitor IS the named guarantor.
	//
	// Per-IP and per-token token-bucket rate limiters defend against
	// enumeration + OTP brute-force. ResolveTenant is a no-op when
	// the request has no slug (default localhost / dev) — the public
	// handler discovers the tenant from the token itself and sets
	// app.tenant_id explicitly inside its tx.
	if d.PublicConsent != nil {
		ipLimiter := middleware.NewRateLimiter(20, 1, middleware.KeyByIP)
		tokenLimiter := middleware.NewRateLimiter(10, 0.25, middleware.KeyByURLParam("token"))
		r.Route("/p/guarantor-consent/{token}", func(r chi.Router) {
			r.Use(ipLimiter.Middleware)
			r.Use(tokenLimiter.Middleware)
			r.Get("/", d.PublicConsent.Get)
			r.Post("/verify-id", d.PublicConsent.VerifyID)
			r.Post("/verify-otp", d.PublicConsent.VerifyOTP)
			r.Post("/respond", d.PublicConsent.Respond)
		})
	}
	if d.PublicPledgerConsent != nil {
		ipLimiter := middleware.NewRateLimiter(20, 1, middleware.KeyByIP)
		tokenLimiter := middleware.NewRateLimiter(10, 0.25, middleware.KeyByURLParam("token"))
		r.Route("/p/pledger-consent/{token}", func(r chi.Router) {
			r.Use(ipLimiter.Middleware)
			r.Use(tokenLimiter.Middleware)
			r.Get("/", d.PublicPledgerConsent.Get)
			r.Post("/verify-id", d.PublicPledgerConsent.VerifyID)
			r.Post("/verify-otp", d.PublicPledgerConsent.VerifyOTP)
			r.Post("/respond", d.PublicPledgerConsent.Respond)
		})
	}

	// DSID Phase 2.2 — joint withdrawal SMS consent (no auth).
	if d.JointAccounts != nil {
		ipLimiter := middleware.NewRateLimiter(20, 1, middleware.KeyByIP)
		tokenLimiter := middleware.NewRateLimiter(10, 0.25, middleware.KeyByURLParam("token"))
		r.Route("/p/joint-withdrawal/{token}", func(r chi.Router) {
			r.Use(ipLimiter.Middleware)
			r.Use(tokenLimiter.Middleware)
			r.Get("/", d.JointAccounts.PublicGet)
			r.Post("/respond", d.JointAccounts.PublicRespond)
		})
	}

	// Phase-1 follow-up — public member-reply route for external comments.
	// Token in URL is the credential; no auth header required.
	if d.PublicComments != nil {
		ipLimiter := middleware.NewRateLimiter(20, 1, middleware.KeyByIP)
		tokenLimiter := middleware.NewRateLimiter(15, 0.5, middleware.KeyByURLParam("token"))
		r.Route("/p/comments/{token}", func(r chi.Router) {
			r.Use(ipLimiter.Middleware)
			r.Use(tokenLimiter.Middleware)
			r.Get("/", d.PublicComments.Get)
			r.Post("/reply", d.PublicComments.Reply)
		})
	}

	// Service-to-service endpoints — no JWT auth. Each handler does
	// its own token / source check. Lives under /internal so it can
	// be firewalled at the ingress.
	r.Route("/internal/v1", func(r chi.Router) {
		r.Post("/pending-approvals/{approval_id}/resolve", d.Approvals.ResolveFromWorkflow)
		r.Post("/loan-applications/{app_id}/resolve", d.LoanApp.ResolveFromWorkflow)
		// workflow callback-dispatcher delivers terminal-state POSTs
		// for cash kinds (cash_deposit, share_purchase, …) here.
		// Routes by inst.process_kind to wf_callbacks/<kind>.go.
		// Wired in cmd/server/main.go via wf_callbacks.Registry.
		if d.WFTerminal != nil {
			r.Post("/workflow-terminal-action", d.WFTerminal.Handle)
		}
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

			// Loans Phase 4 — dividend offset preview + manual post.
			if d.DividendOffset != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/dividends/runs/{run_id}/arrears-offset-preview", d.DividendOffset.Preview)
				r.With(middleware.RequirePermission("dividends:approve")).Post("/dividends/runs/{run_id}/arrears-offset-postings", d.DividendOffset.PostOffsets)
			}
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
			// Loans Phase 5 — top-up + refinance.
			r.With(middleware.RequirePermission("loans:topup")).Post("/loan-applications/topup", d.LoanApp.TopUp)
			r.With(middleware.RequirePermission("loans:refinance")).Post("/loan-applications/refinance", d.LoanApp.Refinance)
			// Loans Phase 5 — group (org-as-borrower) applications + officer consents.
			r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications/group", d.LoanApp.CreateGroup)
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/group-officers", d.LoanApp.ListGroupOfficers)
			r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications/{app_id}/group-officers/{consent_id}/respond", d.LoanApp.RespondGroupOfficer)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/group-apportionment", d.LoanApp.GetGroupApportionment)

			// Loans Phase 6 — CRB + insurance.
			if d.CRB != nil {
				r.With(middleware.RequirePermission("loans:crb:pull")).Post("/loans/crb/pulls", d.CRB.Pull)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/crb/pulls", d.CRB.ListByMember)
			}
			if d.Insurance != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/insurance-policy", d.Insurance.Get)
				r.With(middleware.RequirePermission("loans:insurance:configure")).Post("/loans/{loan_id}/insurance-policy", d.Insurance.Place)
			}

			// Loans Phase 5 — salary check-off.
			if d.Checkoff != nil {
				r.With(middleware.RequirePermission("loans:checkoff:upload")).Post("/loans/checkoff/batches", d.Checkoff.Upload)
				r.With(middleware.RequirePermission("loans:checkoff:upload")).Post("/loans/checkoff/batches/{id}/validate", d.Checkoff.Validate)
				r.With(middleware.RequirePermission("loans:checkoff:upload")).Post("/loans/checkoff/batches/{id}/rows/{row_id}/resolve", d.Checkoff.ResolveRow)
				r.With(middleware.RequirePermission("loans:checkoff:post")).Post("/loans/checkoff/batches/{id}/post", d.Checkoff.Post)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/checkoff/batches", d.Checkoff.List)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/checkoff/batches/{id}", d.Checkoff.Get)
			}
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

			// ─────────── Collateral (Phase 1.5a) ───────────
			//
			// Lifecycle endpoints. The state-transition matrix is enforced
			// server-side; invalid transitions return 409.
			if d.Collateral != nil {
				// Per-application + per-loan + per-item listing.
				r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications/{app_id}/collateral", d.Collateral.CreateForApplication)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/collateral", d.Collateral.ListByApplication)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/collateral", d.Collateral.ListByLoan)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}", d.Collateral.Get)
				r.With(middleware.RequirePermission("loans:apply")).Patch("/collateral/{id}", d.Collateral.Patch)
				r.With(middleware.RequirePermission("loans:apply")).Delete("/collateral/{id}", d.Collateral.Delete)
				// Field-team verification (inspect + photos).
				r.With(middleware.RequirePermission("loans:verify_collateral")).Post("/collateral/{id}/verify", d.Collateral.Verify)
				// Soft reject — collateral stays on the row for resubmission.
				r.With(middleware.RequirePermission("loans:verify_collateral")).Post("/collateral/{id}/reject", d.Collateral.Reject)
				// Valuation desk attaches market + FSV + report.
				r.With(middleware.RequirePermission("loans:value_collateral")).Post("/collateral/{id}/valuation", d.Collateral.Valuation)
				// Approve-time pledge + post-settlement release.
				r.With(middleware.RequirePermission("loans:approve")).Post("/collateral/{id}/pledge", d.Collateral.Pledge)
				r.With(middleware.RequirePermission("loans:approve")).Post("/collateral/{id}/release", d.Collateral.Release)
				// Phase-1 follow-up — terminal flip to 'auctioned' before
				// recording the granular auction events. loans:auction
				// guards because it also frees the internal lien.
				r.With(middleware.RequirePermission("loans:auction")).Post("/collateral/{id}/mark-auctioned", d.Collateral.MarkAuctioned)
				// Security-coverage card for the application detail page.
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/security-coverage", d.Collateral.SecurityCoverage)
				// Phase 1.5b — Member 360 "Pledges given" tab.
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-collateral/by-counterparty/{counterparty_id}", d.Collateral.PledgesGivenByCounterparty)
			}

			// Phase 1.5b — charge / insurance / custody / auction sub-endpoints.
			if d.CollateralAdvanced != nil {
				// Charge registration — legal-filing recording.
				r.With(middleware.RequirePermission("loans:charge_registration")).Post("/collateral/{id}/charge", d.CollateralAdvanced.RecordCharge)
				r.With(middleware.RequirePermission("loans:charge_registration")).Post("/collateral/{id}/charge/discharge", d.CollateralAdvanced.DischargeCharge)
				// Insurance — record + history.
				r.With(middleware.RequirePermission("loans:insurance_record")).Post("/collateral/{id}/insurance", d.CollateralAdvanced.RecordInsurance)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}/insurance/history", d.CollateralAdvanced.GetInsuranceHistory)
				// Custody — movements + timeline.
				r.With(middleware.RequirePermission("loans:custody")).Post("/collateral/{id}/custody", d.CollateralAdvanced.RecordCustody)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}/custody", d.CollateralAdvanced.GetCustodyTimeline)
				// Auction event log.
				r.With(middleware.RequirePermission("loans:auction")).Post("/collateral/{id}/auction-event", d.CollateralAdvanced.RecordAuctionEvent)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}/auction-events", d.CollateralAdvanced.GetAuctionEvents)
			}

			// Phase 1.5b — third-party pledger admin endpoints.
			if d.PledgerConsent != nil {
				r.With(middleware.RequirePermission("loans:apply")).Post("/collateral/{id}/pledger/issue", d.PledgerConsent.IssueForCollateral)
				r.With(middleware.RequirePermission("loans:apply")).Post("/collateral/{id}/pledger/offline-consent", d.PledgerConsent.AdminRecordOfflineConsent)
			}

			// Phase-1 follow-up — valuation report upload + download +
			// other collateral file streams (ownership doc + verification
			// photos). The latter two read the path off loan_collateral
			// and stream from filestore; both are loans:view only.
			if d.ValuationReport != nil {
				r.With(middleware.RequirePermission("loans:value_collateral")).Post("/collateral/{id}/valuation-report", d.ValuationReport.Upload)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral-valuations/{id}/report", d.ValuationReport.Download)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}/ownership-doc", d.ValuationReport.OwnershipDocDownload)
				r.With(middleware.RequirePermission("loans:view")).Get("/collateral/{id}/verification-photos/{idx}", d.ValuationReport.VerificationPhotoDownload)
			}

			// Phase-1 follow-up — Documents tab.
			if d.LoanDocs != nil {
				r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications/{app_id}/documents", d.LoanDocs.UploadForApplication)
				r.With(middleware.RequirePermission("loans:apply")).Post("/loans/{loan_id}/documents", d.LoanDocs.UploadForLoan)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/documents", d.LoanDocs.ListForApplication)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/documents", d.LoanDocs.ListForLoan)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-documents/{id}/download", d.LoanDocs.Download)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/documents/bundle.pdf", d.LoanDocs.BundleApplication)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/documents/bundle.pdf", d.LoanDocs.BundleLoan)
				r.With(middleware.RequirePermission("loans:apply")).Post("/loan-documents/{id}/review", d.LoanDocs.Review)
				r.With(middleware.RequirePermission("loans:apply")).Delete("/loan-documents/{id}", d.LoanDocs.Delete)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/required-documents-status", d.LoanDocs.RequiredStatus)
			}

			// DSID Phase 2.1 — member statement PDFs + email.
			if d.MemberStatementsPDF != nil {
				r.With(middleware.RequirePermission("members:view")).Get("/members/{counterparty_id}/statements/deposits.pdf", d.MemberStatementsPDF.DepositsPDF)
				r.With(middleware.RequirePermission("members:view")).Get("/members/{counterparty_id}/statements/shares.pdf", d.MemberStatementsPDF.SharesPDF)
				r.With(middleware.RequirePermission("members:view")).Get("/members/{counterparty_id}/statements/interest.pdf", d.MemberStatementsPDF.InterestPDF)
				r.With(middleware.RequirePermission("members:view")).Get("/members/{counterparty_id}/statements/dividend.pdf", d.MemberStatementsPDF.DividendPDF)
				r.With(middleware.RequirePermission("members:view")).Post("/members/{counterparty_id}/statements/email", d.MemberStatementsPDF.Email)
			}

			// DSID Phase 2.1 — WHT iTax remittance export.
			if d.WHTRemittance != nil {
				r.With(middleware.RequirePermission("loans:reports")).Get("/tax/wht-remittance.csv", d.WHTRemittance.CSV)
				r.With(middleware.RequirePermission("loans:reports")).Get("/tax/wht-remittance.json", d.WHTRemittance.JSON)
			}

			// DSID Phase 2.2 — Dormancy reactivation.
			if d.DepositReactivation != nil {
				r.With(middleware.RequirePermission("savings:approve")).Post("/deposit-accounts/{account_id}/reactivate", d.DepositReactivation.Reactivate)
			}

			// DSID Phase 2.2 — Per-product recurring fees.
			if d.RecurringFees != nil {
				r.With(middleware.RequirePermission("deposits:configure")).Get("/deposit-products/{product_id}/recurring-fees", d.RecurringFees.List)
				r.With(middleware.RequirePermission("deposits:configure")).Post("/deposit-products/{product_id}/recurring-fees", d.RecurringFees.Create)
				r.With(middleware.RequirePermission("deposits:configure")).Patch("/deposit-product-recurring-fees/{id}", d.RecurringFees.Patch)
				r.With(middleware.RequirePermission("deposits:configure")).Delete("/deposit-product-recurring-fees/{id}", d.RecurringFees.Delete)
			}

			// DSID Phase 2.2 — Joint accounts.
			if d.JointAccounts != nil {
				r.With(middleware.RequirePermission("members:view")).Get("/deposit-accounts/{account_id}/joint-owners", d.JointAccounts.ListOwners)
				r.With(middleware.RequirePermission("members:edit")).Post("/deposit-accounts/{account_id}/joint-owners", d.JointAccounts.AddOwner)
				r.With(middleware.RequirePermission("members:edit")).Delete("/deposit-accounts/{account_id}/joint-owners/{counterparty_id}", d.JointAccounts.RemoveOwner)
				r.With(middleware.RequirePermission("members:edit")).Put("/deposit-accounts/{account_id}/joint-config", d.JointAccounts.PutConfig)
				r.With(middleware.RequirePermission("members:view")).Get("/deposit-accounts/{account_id}/pending-withdrawals", d.JointAccounts.ListPendingWithdrawals)
			}

			// DSID Phase 2.2 — Standing orders.
			if d.StandingOrders != nil {
				r.With(middleware.RequirePermission("members:edit")).Post("/members/{counterparty_id}/standing-orders", d.StandingOrders.Create)
				r.With(middleware.RequirePermission("members:view")).Get("/members/{counterparty_id}/standing-orders", d.StandingOrders.ListByMember)
				r.With(middleware.RequirePermission("members:edit")).Patch("/standing-orders/{id}", d.StandingOrders.Patch)
				r.With(middleware.RequirePermission("members:edit")).Delete("/standing-orders/{id}", d.StandingOrders.Delete)
				r.With(middleware.RequirePermission("members:view")).Get("/standing-orders/{id}/runs", d.StandingOrders.ListRuns)
				r.With(middleware.RequirePermission("members:edit")).Post("/standing-orders/{id}/resume", d.StandingOrders.Resume)
			}

			// Phase-1 follow-up — Score history.
			if d.LoanApp != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/score/history", d.LoanApp.GetScoreHistory)
			}

			// Phase-1 follow-up — Comments tab.
			if d.LoanComments != nil {
				r.With(middleware.RequirePermission("loans:apply")).Post("/loan-applications/{app_id}/comments", d.LoanComments.PostForApplication)
				r.With(middleware.RequirePermission("loans:apply")).Post("/loans/{loan_id}/comments", d.LoanComments.PostForLoan)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-applications/{app_id}/comments", d.LoanComments.ListForApplication)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/comments", d.LoanComments.ListForLoan)
				r.With(middleware.RequirePermission("loans:apply")).Patch("/loan-comments/{id}", d.LoanComments.Edit)
				r.With(middleware.RequirePermission("loans:apply")).Post("/loan-comments/{id}/pin", d.LoanComments.Pin)
				r.With(middleware.RequirePermission("loans:apply")).Delete("/loan-comments/{id}", d.LoanComments.SoftDelete)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-comments/templates", d.LoanComments.ListTemplates)
				r.With(middleware.RequirePermission("loans:view")).Get("/loan-comments/search", d.LoanComments.Search)
			}

			// Phase 1.5b — collateral reports. Mounted under
			// /v1/loans/reports/collateral-* alongside the existing
			// Phase 2 loan reports. Permission loans:reports.
			if d.CollateralReports != nil {
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-exposure", d.CollateralReports.Exposure)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-exposure.csv", d.CollateralReports.ExposureCSV)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-by-kind", d.CollateralReports.ByKind)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-valuations-expiring", d.CollateralReports.ValuationsExpiring)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-insurance-expiring", d.CollateralReports.InsuranceExpiring)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/collateral-charge-status", d.CollateralReports.ChargeRegistrationStatus)
			}

			// Guarantor consent
			r.With(middleware.RequirePermission("loans:guarantee")).Post("/loan-guarantees/{guarantee_id}/respond", d.LoanApp.GuaranteeRespond)
			// Member-scoped — every loan this member guarantees, joined
			// with borrower + loan number for the Member Profile People tab.
			r.With(middleware.RequirePermission("loans:view")).Get("/loan-guarantees/by-counterparty/{counterparty_id}", d.LoanApp.ListByGuarantor)

			// Admin-captured consent with proof upload (multipart). Same
			// permission as the existing /respond endpoint — any user
			// who can respond can also upload proof.
			if d.GuarantorConsent != nil {
				r.With(middleware.RequirePermission("loans:guarantee")).Post(
					"/loan-guarantees/{guarantee_id}/respond-with-proof",
					d.GuarantorConsent.AdminRespond,
				)
				// Member-portal self-service. Gated on the portal:self
				// permission (granted to the Member role in identity 0037).
				// The handler additionally enforces "you can only respond
				// to your own guarantees" via the user→member bridge.
				r.With(middleware.RequirePermission("portal:self")).Get(
					"/portal/guarantorships",
					d.GuarantorConsent.PortalList,
				)
				r.With(middleware.RequirePermission("portal:self")).Post(
					"/portal/guarantorships/{guarantee_id}/respond",
					d.GuarantorConsent.PortalRespond,
				)
			}

			// ─────────── Loan offer + acceptance + disbursement ───────────
			r.With(middleware.RequirePermission("loans:offer")).Post("/loan-applications/{app_id}/send-offer", d.Loan.SendOffer)
			r.With(middleware.RequirePermission("loans:offer")).Post("/loan-applications/{app_id}/accept-offer", d.Loan.AcceptOffer)
			r.With(middleware.RequirePermission("loans:disburse")).Post("/loans/{loan_id}/disburse", d.Loan.Disburse)

			r.With(middleware.RequirePermission("loans:view")).Get("/loans", d.Loan.List)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/arrears-summary", d.LoanRepay.ArrearsSummary)
			// Loans Phase 1 — single-call dashboard aggregator.
			// Must register BEFORE /loans/{loan_id} so chi doesn't
			// match "dashboard" as a loan id param.
			if d.LoanDashboard != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/dashboard", d.LoanDashboard.Get)
			}
			// Loans Phase 2 — reporting endpoints. Same registration-
			// order caveat: these must come BEFORE /loans/{loan_id}.
			if d.LoanReportsP2 != nil {
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/par",                d.LoanReportsP2.PAR)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/par/history",        d.LoanReportsP2.PARHistory)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/portfolio/history",  d.LoanReportsP2.PortfolioHistory)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/aging-buckets",      d.LoanReportsP2.Aging)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/vintage",            d.LoanReportsP2.Vintage)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/officers",           d.LoanReportsP2.Officers)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/disbursements",      d.LoanReportsP2.Disbursements)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/repayments",         d.LoanReportsP2.Repayments)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/guarantor-exposure", d.LoanReportsP2.GuarantorExposure)
				r.With(middleware.RequirePermission("loans:reports")).Get("/loans/reports/top-n",              d.LoanReportsP2.TopN)
			}
			if d.SASRA != nil {
				r.With(middleware.RequirePermission("loans:sasra")).Get("/loans/reports/sasra",          d.SASRA.Generate)
				r.With(middleware.RequirePermission("loans:sasra")).Post("/loans/reports/sasra/verify", d.SASRA.Verify)
				r.With(middleware.RequirePermission("loans:sasra")).Get("/tenant/sasra-column-overrides",  d.SASRA.GetOverride)
				r.With(middleware.RequirePermission("loans:sasra")).Put("/tenant/sasra-column-overrides", d.SASRA.PutOverride)
			}
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}", d.Loan.GetLoan)
			r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/payoff", d.LoanRepay.Payoff)

			// ─────────── Repayment + settlement + reversal + DPD ───────────
			r.With(middleware.RequirePermission("savings:transact")).Post("/loans/{loan_id}/repay", d.LoanRepay.Repay)
			r.With(middleware.RequirePermission("savings:approve")).Post("/loans/{loan_id}/settle", d.LoanRepay.Settle)
			r.With(middleware.RequirePermission("loans:reverse")).Post("/loan-transactions/{txn_id}/reverse", d.LoanRepay.Reverse)
			r.With(middleware.RequirePermission("loans:view")).Post("/loans/{loan_id}/recalc-dpd", d.LoanRepay.RecalcDPD)

			// ─────────── Collections (legacy /v1/collection-cases/* — Phase 6e) ───────────
			r.With(middleware.RequirePermission("collections:view")).Get("/collection-cases", d.LoanCollect.ListCases)
			r.With(middleware.RequirePermission("collections:view")).Get("/collection-cases/{case_id}", d.LoanCollect.GetCase)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/assign", d.LoanCollect.Assign)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/close", d.LoanCollect.CloseCase)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/contacts", d.LoanCollect.LogContact)
			r.With(middleware.RequirePermission("collections:act")).Post("/collection-cases/{case_id}/promises", d.LoanCollect.CreatePTP)
			r.With(middleware.RequirePermission("collections:act")).Post("/promises/{ptp_id}/resolve", d.LoanCollect.ResolvePTP)

			// ─────────── Loans Phase 4 — collections workflow ───────────
			if d.LoanCollectionsEvents != nil {
				cev := d.LoanCollectionsEvents
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/calls", cev.LogCall)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/visits", cev.LogVisit)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/notes", cev.Note)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/ptp", cev.CreatePTP)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/ptp/{ptp_id}/cancel", cev.CancelPTP)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/escalate", cev.Escalate)
				r.With(middleware.RequirePermission("loans:collect:legal")).Post("/loans/{loan_id}/collections/legal-handover", cev.LegalHandover)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/sms", cev.SendSMS)
				r.With(middleware.RequirePermission("loans:collect")).Post("/loans/{loan_id}/collections/letter", cev.GenerateLetter)
				r.With(middleware.RequirePermission("loans:collect:assign")).Post("/loans/{loan_id}/collections/assign", cev.Assign)
				r.With(middleware.RequirePermission("loans:collect:assign")).Post("/loans/{loan_id}/collections/unassign", cev.Unassign)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/collections/events", cev.Events)
				r.With(middleware.RequirePermission("loans:collect")).Get("/loans/collections/queue", cev.Queue)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/collections/ptp-summary", cev.PTPSummary)
			}

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

			// ─────────── Loan loss provisioning (legacy /v1/provisioning/*) ───────────
			// Kept mounted for one release alongside the Phase 3 v2 endpoints below.
			// The legacy UI calls these; the new UI calls /v1/loans/provisioning/*.
			r.With(middleware.RequirePermission("loans:view")).Get("/provisioning/runs", d.Provisioning.List)
			r.With(middleware.RequirePermission("loans:view")).Get("/provisioning/runs/{run_id}", d.Provisioning.Get)
			r.With(middleware.RequirePermission("interest:run")).Post("/provisioning/runs", d.Provisioning.Create)
			r.With(middleware.RequirePermission("interest:post")).Post("/provisioning/runs/{run_id}/post", d.Provisioning.Post)
			r.With(middleware.RequirePermission("interest:post")).Post("/provisioning/runs/{run_id}/supersede", d.Provisioning.Supersede)

			// ─────────── Loans Phase 3 — provisioning v2 ───────────
			if d.LoanProvisioningV2 != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/provisioning/runs", d.LoanProvisioningV2.List)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/provisioning/runs/{run_id}", d.LoanProvisioningV2.Get)
				r.With(middleware.RequirePermission("loans:provisioning:run")).Post("/loans/provisioning/runs", d.LoanProvisioningV2.Create)
				r.With(middleware.RequirePermission("loans:provisioning:post")).Post("/loans/provisioning/runs/{run_id}/post", d.LoanProvisioningV2.Post)
				r.With(middleware.RequirePermission("loans:provisioning:run")).Post("/loans/provisioning/runs/{run_id}/cancel", d.LoanProvisioningV2.Cancel)
			}

			// ─────────── Loans Phase 3 — policy admin + classification history ───────────
			if d.LoanPolicy != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/policy", d.LoanPolicy.Get)
				r.With(middleware.RequirePermission("loans:policy:write")).Put("/loans/policy/thresholds", d.LoanPolicy.UpdateThresholds)
				r.With(middleware.RequirePermission("loans:policy:write")).Put("/loans/policy/ecl-matrix", d.LoanPolicy.UpdateMatrix)
				r.With(middleware.RequirePermission("loans:policy:write")).Put("/loans/policy/dividend-offset", d.LoanPolicy.UpdateDividendOffsetPolicy)
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/{loan_id}/classification-history", d.LoanPolicy.LoanClassificationHistory)
			}
			if d.GuarantorSMSPolicy != nil {
				r.With(middleware.RequirePermission("loans:view")).Get("/loans/policy/guarantor-sms", d.GuarantorSMSPolicy.Get)
				r.With(middleware.RequirePermission("loans:policy:write")).Put("/loans/policy/guarantor-sms", d.GuarantorSMSPolicy.Update)
			}

			// ─────────── Collection Desk (single cashier's counter) ───────────
			r.With(middleware.RequirePermission("members:view")).Get("/counterparties/{id}/outstanding", d.Collection.Outstanding)
			if d.GuarantorCapacity != nil {
				// Mounted under /v1/loans/* (not /v1/counterparties/*)
				// because the admin SPA dev proxy routes the
				// /api/v1/counterparties prefix to the member service.
				// See web/admin/vite.config.ts.
				r.With(middleware.RequirePermission("loans:apply")).Get("/loans/guarantor-capacity", d.GuarantorCapacity.Get)
			}
			if d.QualifyingAmount != nil {
				r.With(middleware.RequirePermission("loans:apply")).Get("/loans/qualifying-amount", d.QualifyingAmount.Get)
			}
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
