// Savings service entry point.
//
// CLI flags:
//   -migrate   run pending migrations and exit
//
// With no flags it starts the HTTP server.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/handler"
	"github.com/nexussacco/savings/internal/handler/wf_callbacks"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

// receiptLineAdapter forwards wf_callbacks.ReceiptLineUpdater calls
// onto the savings receipt store. Lives here (not in wf_callbacks)
// so the wf_callbacks package stays free of store-package imports —
// the runner-pattern keeps the dependency arrows pointing one way.
type receiptLineAdapter struct{ s *store.ReceiptStore }

func (a receiptLineAdapter) FindReceiptLineByApprovalIDTx(ctx context.Context, tx pgx.Tx, approvalID uuid.UUID) (uuid.UUID, bool, error) {
	line, err := a.s.GetLineByApprovalIDTx(ctx, tx, approvalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return line.ID, true, nil
}
func (a receiptLineAdapter) MarkReceiptLinePostedTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, txnID uuid.UUID) error {
	return a.s.MarkLinePostedTx(ctx, tx, lineID, txnID)
}
func (a receiptLineAdapter) MarkReceiptLineDeclinedTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) error {
	return a.s.MarkLineDeclinedTx(ctx, tx, lineID)
}

// bootTime is captured at process start. Reported on /healthz so
// operators can see process uptime via (now - started_at). Module-level
// so the value persists across the migrate / snapshot / DPD branches.
var bootTime = time.Now().UTC()

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	runSnapshot := flag.String("run-snapshot", "", "run the deposit daily-balance snapshot job for the named tenant slug (date optional via -snapshot-date)")
	snapshotDate := flag.String("snapshot-date", "", "snapshot date in YYYY-MM-DD (defaults to today)")
	runDPD := flag.String("run-dpd", "", "run the daily loan DPD + interest-accrual job for the named tenant slug")
	dpdAsOf := flag.String("dpd-as-of", "", "DPD as-of date in YYYY-MM-DD (defaults to today)")
	flag.Parse()

	if *migrate {
		_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel, cfg.Env)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *migrate {
		if err := pool.Migrate(ctx); err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("savings migrations applied")
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	members := store.NewMemberStore(pool.Pool)
	counterparties := store.NewCounterpartyStore(pool.Pool)
	shareStore := store.NewShareStore(pool.Pool)
	productStore := store.NewDepositProductStore(pool.Pool)
	depositStore := store.NewDepositStore(pool.Pool)
	interestStore := store.NewInterestStore(pool.Pool)
	dividendStore := store.NewDividendStore(pool.Pool)
	loanProductStore := store.NewLoanProductStore(pool.Pool)
	loanAppStore := store.NewLoanApplicationStore(pool.Pool)
	loanGuarStore := store.NewLoanGuaranteeStore(pool.Pool)
	loanStore := store.NewLoanStore(pool.Pool)
	loanCollectionsStore := store.NewLoanCollectionsStore(pool.Pool)
	// Bridge: every RecalcDPDTx call now auto-enqueues a collections
	// case for loans that slip into arrears, not just the nightly
	// cron. See loan_store.go.SetCollections + loan_repayment_store.go
	// RecalcDPDTx for the contract.
	loanStore.SetCollections(loanCollectionsStore)
	loanRestructureStore := store.NewLoanRestructureStore(pool.Pool)
	loanReportsStore := store.NewLoanReportsStore(pool.Pool)
	approvalsStore := store.NewApprovalsStore(pool.Pool)
	receiptStore := store.NewReceiptStore(pool.Pool)
	virtualTillStore := store.NewVirtualTillStore(pool.Pool)
	feeCatalogStore := store.NewFeeCatalogStore(pool.Pool)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	notifyClient := notifier.New(cfg.NotificationURL, cfg.NotificationInternalToken, logger)
	// Fail-fast on a missing accounting URL — better to refuse to
	// start than to silently drop GL posts. The dry-run escape is
	// SAVINGS_ALLOW_NO_ACCOUNTING=true (tests only); production +
	// dev should always reach an accounting service.
	postingClient, perr := posting.New(cfg.AccountingURL, cfg.AccountingInternalToken, logger)
	if perr != nil {
		logger.Error("savings server: cannot start without accounting",
			"err", perr,
			"accounting_url", cfg.AccountingURL,
			"hint", "ACCOUNTING_SERVICE_URL is the env var (default http://localhost:8086). Set SAVINGS_ALLOW_NO_ACCOUNTING=true for tests only.")
		os.Exit(1)
	}
	shareH := &handler.ShareHandler{
		DB:             pool,
		Tenants:        tenants,
		Members:        members,
		Counterparties: counterparties,
		Shares:         shareStore,
		Approvals:      approvalsStore,
		Receipts:       receiptStore,
		VirtualTills:   virtualTillStore,
		Notifier:       notifyClient,
		Posting:        postingClient,
		Logger:         logger,
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	productH := &handler.ProductHandler{
		DB:       pool,
		Products: productStore,
		Logger:   logger,
	}
	depositH := &handler.DepositHandler{
		DB:             pool,
		Tenants:        tenants,
		Members:        members,
		Counterparties: counterparties,
		Products:       productStore,
		Deposits:       depositStore,
		Approvals:      approvalsStore,
		Receipts:       receiptStore,
		VirtualTills:   virtualTillStore,
		Notifier:       notifyClient,
		Posting:        postingClient,
		Logger:         logger,
		// Workflow path — when active wf_definition exists for
		// cash_deposit on the tenant, queueing routes through the
		// workflow engine instead of pending_approvals.
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	interestH := &handler.InterestHandler{
		DB:                  pool,
		Tenants:             tenants,
		Members:             members,
		Counterparties:      counterparties,
		Products:            productStore,
		Deposits:            depositStore,
		Shares:              shareStore,
		Interest:            interestStore,
		Notifier:            notifyClient,
		Posting:             postingClient,
		Logger:              logger,
		WorkflowURL:         cfg.WorkflowURL,
		SavingsSelfURL:      cfg.SavingsURL,
		WorkflowProcessKind: "interest_run_approval",
		HTTP:                &http.Client{Timeout: 10 * time.Second},
	}
	loanProductH := &handler.LoanProductHandler{
		DB:       pool,
		Products: loanProductStore,
		Logger:   logger,
	}
	loanAppH := &handler.LoanApplicationHandler{
		DB:                    pool,
		Tenants:               tenants,
		Members:               members,
		Counterparties:        counterparties,
		LoanProducts:          loanProductStore,
		Applications:          loanAppStore,
		Guarantees:            loanGuarStore,
		Notifier:              notifyClient,
		Logger:                logger,
		WorkflowURL:           cfg.WorkflowURL,
		WorkflowInternalToken: cfg.WorkflowInternalToken,
		SavingsSelfURL:        cfg.SavingsURL,
		HTTP:                  &http.Client{Timeout: 10 * time.Second},
	}
	loanH := &handler.LoanHandler{
		DB:              pool,
		Tenants:         tenants,
		Members:         members,
		Counterparties:  counterparties,
		LoanProducts:    loanProductStore,
		Applications:    loanAppStore,
		Guarantees:      loanGuarStore,
		Loans:           loanStore,
		Deposits:        depositStore,
		DepositProducts: productStore,
		Approvals:       approvalsStore,
		Notifier:        notifyClient,
		Posting:         postingClient,
		Logger:          logger,
		Workflow:        workflowclient.New(),
		SavingsSelfURL:  cfg.SavingsURL,
	}
	loanRepayH := &handler.LoanRepaymentHandler{
		DB:             pool,
		Tenants:        tenants,
		Members:        members,
		Counterparties: counterparties,
		Deposits:       depositStore,
		Loans:          loanStore,
		Approvals:      approvalsStore,
		Receipts:       receiptStore,
		VirtualTills:   virtualTillStore,
		Notifier:       notifyClient,
		Posting:        postingClient,
		Logger:         logger,
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	loanCollectH := &handler.LoanCollectionsHandler{
		DB:             pool,
		Tenants:        tenants,
		Members:        members,
		Counterparties: counterparties,
		Loans:          loanStore,
		Collections:    loanCollectionsStore,
		Restructure:    loanRestructureStore,
		Approvals:      approvalsStore,
		Notifier:       notifyClient,
		Logger:         logger,
		Posting:        postingClient,
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	loanReportsH := &handler.LoanReportsHandler{
		DB:             pool,
		Posting:        postingClient,
		Reports:        loanReportsStore,
		Loans:          loanStore,
		Approvals:      approvalsStore,
		Logger:         logger,
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	provisioningStore := store.NewProvisioningStore(pool)
	provisioningH := &handler.ProvisioningHandler{
		Store:   provisioningStore,
		Posting: postingClient,
		Logger:  logger,
	}
	memberStmtStore := store.NewMemberStatementStore(pool.Pool)
	memberStmtH := &handler.MemberStatementHandler{
		DB:         pool,
		Statements: memberStmtStore,
		Logger:     logger,
	}
	memberLedgerStore := store.NewMemberLedgerStore(pool.Pool)
	memberLedgerH := &handler.MemberLedgerHandler{
		DB:     pool,
		Ledger: memberLedgerStore,
		Logger: logger,
	}
	// Application-fee executor is shared between the legacy pending-
	// approvals dispatcher (wired below) and the wf_callbacks
	// registry. Extracted to a local so both can hold the same
	// instance.
	applicationFeesEx := &handler.ApplicationFeeExecutor{Posting: postingClient, Logger: logger}

	approvalsH := &handler.PendingApprovalsHandler{
		DB:          pool,
		Approvals:   approvalsStore,
		Deposit:     depositH,
		Share:       shareH,
		Loan:        loanH,
		LoanRepay:   loanRepayH,
		LoanCollect: loanCollectH,
		LoanReports: loanReportsH,
		Receipts:    receiptStore,
		// Wave 2 — ApplicationFee dispatcher executor. Collection
		// gets wired in below after collectionDeskH exists (the two
		// handlers reference each other; we break the cycle with a
		// post-construction assignment).
		ApplicationFees:       applicationFeesEx,
		WorkflowInternalToken: cfg.WorkflowInternalToken,
		Logger:                logger,
	}
	collectionDeskH := &handler.CollectionDeskHandler{
		DB:             pool,
		Receipts:       receiptStore,
		VirtualTills:   virtualTillStore,
		Approvals:      approvalsStore,
		Loans:          loanStore,
		LoanReports:    loanReportsStore,
		Shares:         shareStore,
		Tenants:        tenants,
		Counterparties: counterparties,
		Fees:           feeCatalogStore,
		Notifier:       notifyClient,
		Posting:        postingClient,
		Deposit:        depositH,
		LoanRepay:      loanRepayH,
		Logger:         logger,
		Workflow:       workflowclient.New(),
		SavingsSelfURL: cfg.SavingsURL,
	}
	// Cycle-breaker: the fee/welfare approval executor needs to
	// call back into collectionDeskH.postFeeLineTx. Constructed
	// after both handlers exist.
	approvalsH.Collection = collectionDeskH
	dividendH := &handler.DividendHandler{
		DB:                  pool,
		Tenants:             tenants,
		Members:             members,
		Counterparties:      counterparties,
		Products:            productStore,
		Deposits:            depositStore,
		Shares:              shareStore,
		Dividends:           dividendStore,
		Notifier:            notifyClient,
		Posting:             postingClient,
		Logger:              logger,
		WorkflowURL:         cfg.WorkflowURL,
		SavingsSelfURL:      cfg.SavingsURL,
		WorkflowProcessKind: "dividend_run_approval",
		HTTP:                &http.Client{Timeout: 10 * time.Second},
	}

	// CLI: run daily balance snapshot for a single tenant.
	if *runSnapshot != "" {
		t, err := tenants.BySlug(ctx, *runSnapshot)
		if err != nil {
			logger.Error("snapshot: tenant lookup", "slug", *runSnapshot, "err", err)
			os.Exit(1)
		}
		date := time.Now().UTC()
		if *snapshotDate != "" {
			d, err := time.Parse("2006-01-02", *snapshotDate)
			if err != nil {
				logger.Error("snapshot: invalid -snapshot-date", "err", err)
				os.Exit(1)
			}
			date = d
		}
		n, err := handler.RunDailySnapshot(ctx, depositH, t.ID, date)
		if err != nil {
			logger.Error("snapshot: failed", "err", err)
			os.Exit(1)
		}
		logger.Info("snapshot complete", "tenant", t.Slug, "date", date.Format("2006-01-02"), "accounts", n)
		return
	}

	// CLI: run daily DPD recompute + interest accrual.
	if *runDPD != "" {
		t, err := tenants.BySlug(ctx, *runDPD)
		if err != nil {
			logger.Error("dpd: tenant lookup", "slug", *runDPD, "err", err)
			os.Exit(1)
		}
		asOf := time.Now().UTC()
		if *dpdAsOf != "" {
			d, err := time.Parse("2006-01-02", *dpdAsOf)
			if err != nil {
				logger.Error("dpd: invalid -dpd-as-of", "err", err)
				os.Exit(1)
			}
			asOf = d
		}
		// Use a synthetic actor id for system-run accruals.
		actor := uuid.Nil
		n, err := handler.RunDPDForTenant(ctx, loanRepayH, t.ID, asOf, actor, loanCollectionsStore)
		if err != nil {
			logger.Error("dpd: failed", "err", err)
			os.Exit(1)
		}
		logger.Info("dpd complete", "tenant", t.Slug, "as_of", asOf.Format("2006-01-02"), "loans_processed", n)
		return
	}

	// Workflow callback registry. One Register call per migrated
	// kind; the dispatcher's HTTP POST is routed by process_kind
	// through this registry. Every cash-handling kind has a
	// dedicated callback file in wf_callbacks/<kind>.go.
	rla := receiptLineAdapter{s: receiptStore}
	wfRegistry := wf_callbacks.NewRegistry()
	wfRegistry.Register("cash_deposit", wf_callbacks.NewCashDepositCallback(depositH, rla))
	wfRegistry.Register("cash_withdrawal", wf_callbacks.NewCashWithdrawalCallback(depositH, rla))
	wfRegistry.Register("cash_account_transfer", wf_callbacks.NewCashAccountTransferCallback(depositH, rla))
	wfRegistry.Register("share_purchase", wf_callbacks.NewSharePurchaseCallback(shareH, rla))
	wfRegistry.Register("share_transfer", wf_callbacks.NewShareTransferCallback(shareH, rla))
	wfRegistry.Register("share_bonus_issue", wf_callbacks.NewShareBonusCallback(shareH, rla))
	wfRegistry.Register("share_lien", wf_callbacks.NewShareLienCallback(shareH, rla))
	wfRegistry.Register("loan_disbursement", wf_callbacks.NewLoanDisbursementCallback(loanH, rla))
	wfRegistry.Register("loan_repayment", wf_callbacks.NewLoanRepaymentCallback(loanRepayH, rla))
	wfRegistry.Register("loan_settle", wf_callbacks.NewLoanSettleCallback(loanRepayH, rla))
	wfRegistry.Register("loan_reverse", wf_callbacks.NewLoanReverseCallback(loanRepayH, rla))
	wfRegistry.Register("loan_write_off", wf_callbacks.NewLoanWriteoffCallback(loanReportsH, rla))
	wfRegistry.Register("loan_reschedule", wf_callbacks.NewLoanRescheduleCallback(loanCollectH, rla))
	wfRegistry.Register("loan_moratorium", wf_callbacks.NewLoanMoratoriumCallback(loanCollectH, rla))
	wfRegistry.Register("loan_settlement_discount", wf_callbacks.NewLoanSettlementDiscountCallback(loanCollectH, rla))
	wfRegistry.Register("fee_posting", wf_callbacks.NewFeePostingCallback(collectionDeskH, rla, "fee_posting"))
	wfRegistry.Register("welfare_posting", wf_callbacks.NewFeePostingCallback(collectionDeskH, rla, "welfare_posting"))
	if applicationFeesEx != nil {
		wfRegistry.Register("application_fee", wf_callbacks.NewApplicationFeeCallback(applicationFeesEx, rla))
	}
	wfRegistry.Register("member_bosa_exit", wf_callbacks.NewMemberBOSAExitCallback())
	logger.Info("wf_callbacks: registered", "kinds", wfRegistry.Kinds())

	router := handler.Routes(handler.Deps{
		Share:         shareH,
		Deposit:       depositH,
		Product:       productH,
		Interest:      interestH,
		Dividend:      dividendH,
		LoanProduct:   loanProductH,
		LoanApp:       loanAppH,
		Loan:          loanH,
		LoanRepay:     loanRepayH,
		LoanCollect:   loanCollectH,
		LoanReports:   loanReportsH,
		Provisioning:  provisioningH,
		MemberStmt:    memberStmtH,
		MemberLedger:  memberLedgerH,
		Approvals:     approvalsH,
		Collection:    collectionDeskH,
		VirtualTill:   &handler.VirtualTillHandler{DB: pool, Tills: virtualTillStore},
		BOSAExit: &handler.BOSAExitHandler{
			DB:             pool,
			Deposit:        depositH,
			Approvals:      approvalsStore,
			Workflow:       workflowclient.New(),
			SavingsSelfURL: cfg.SavingsURL,
		},
		Outbox:        &handler.PostingOutboxHandler{DB: pool, Outbox: store.NewPostingOutboxStore(pool.Pool)},
		FeesSummary:   &handler.FeesSummaryHandler{DB: pool, Store: store.NewFeesSummaryStore(pool.Pool)},
		FinanceHealth: &handler.FinanceHealthHandler{DB: pool, Logger: logger},
		WFTerminal: &handler.WorkflowTerminalCallbackHandler{
			DB:                    pool,
			Registry:              wfRegistry,
			WorkflowInternalToken: cfg.WorkflowInternalToken,
		},
		// Loans Phase 1 — single-call dashboard aggregator. 30s
		// per-tenant cache inside the handler keeps the dashboard's
		// 60s poll cheap.
		LoanDashboard: &handler.LoanDashboardHandler{DB: pool},
		// Loans Phase 2 — reporting endpoints. Per-endpoint TTL
		// cache inside the handler matches the prompt's matrix
		// (30s aging, 1h vintage, 1m officers, 1m top-N, 5m
		// guarantors, 5m trend history).
		LoanReportsP2: &handler.LoanReportsPhase2Handler{
			DB:    pool,
			Store: loanReportsStore,
		},
		// Loans Phase 2 — SASRA quarterly extract handler. CSV +
		// PDF formats; DRAFT watermark until the tenant admin
		// signs off on the column layout for the period.
		SASRA: &handler.SASRAHandler{DB: pool},
		Health: handler.NewHealthBuilder(
			pool, cfg.AccountingURL, buildVersion(), bootTime, 0,
		).Handler(500 * time.Millisecond),
		TenantStore:   tenants,
		Issuer:        issuer,
		AppDomain:     cfg.AppDomain,
		Logger:        logger,
	})

	// Boot-time integrity probe: log ERROR for any broken fee_catalog
	// → CoA mapping. Doesn't fail boot — /healthz/finance returns 503
	// for deployment automation to gate on. Surfaces broken catalogs
	// loudly in the startup log so on-call sees them without polling.
	handler.LogFinanceConfigOnBoot(ctx, pool, logger)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		logger.Info("savings service listening",
			"addr", cfg.HTTPAddr, "app_domain", cfg.AppDomain, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}

// buildVersion returns the version string baked in at build time
// (-ldflags "-X main.version=…") if set, falling back to env
// BUILD_VERSION, falling back to "dev". Reported on /healthz so the
// system-health aggregator can confirm every replica is on the
// expected SHA after a rollout.
func buildVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// version is overridden at link time. Left unset in dev builds.
var version string

func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	if env == "development" {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
