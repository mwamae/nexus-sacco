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

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/handler"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

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

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	notifyClient := notifier.New(cfg.NotificationURL, cfg.NotificationInternalToken, logger)
	postingClient := posting.New(cfg.AccountingURL, cfg.AccountingInternalToken, logger)
	shareH := &handler.ShareHandler{
		DB:        pool,
		Tenants:   tenants,
		Members:   members,
		Counterparties: counterparties,
		Shares:    shareStore,
		Approvals: approvalsStore,
		Notifier:  notifyClient,
		Posting:   postingClient,
		Logger:    logger,
	}
	productH := &handler.ProductHandler{
		DB:       pool,
		Products: productStore,
		Logger:   logger,
	}
	depositH := &handler.DepositHandler{
		DB:        pool,
		Tenants:   tenants,
		Members:   members,
		Counterparties: counterparties,
		Products:  productStore,
		Deposits:  depositStore,
		Approvals: approvalsStore,
		Notifier:  notifyClient,
		Posting:   postingClient,
		Logger:    logger,
	}
	interestH := &handler.InterestHandler{
		DB:                  pool,
		Tenants:             tenants,
		Members:             members,
		Counterparties: counterparties,
		Products:            productStore,
		Deposits:            depositStore,
		Shares:              shareStore,
		Interest:            interestStore,
		Notifier:            notifyClient,
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
		DB:           pool,
		Tenants:      tenants,
		Members:      members,
		Counterparties: counterparties,
		LoanProducts: loanProductStore,
		Applications: loanAppStore,
		Guarantees:   loanGuarStore,
		Notifier:     notifyClient,
		Logger:       logger,
	}
	loanH := &handler.LoanHandler{
		DB:           pool,
		Tenants:      tenants,
		Members:      members,
		Counterparties: counterparties,
		LoanProducts: loanProductStore,
		Applications: loanAppStore,
		Guarantees:   loanGuarStore,
		Loans:        loanStore,
		Deposits:     depositStore,
		Approvals:    approvalsStore,
		Notifier:     notifyClient,
		Posting:      postingClient,
		Logger:       logger,
	}
	loanRepayH := &handler.LoanRepaymentHandler{
		DB:        pool,
		Tenants:   tenants,
		Members:   members,
		Counterparties: counterparties,
		Deposits:  depositStore,
		Loans:     loanStore,
		Approvals: approvalsStore,
		Notifier:  notifyClient,
		Posting:   postingClient,
		Logger:    logger,
	}
	loanCollectH := &handler.LoanCollectionsHandler{
		DB:          pool,
		Tenants:     tenants,
		Members:     members,
		Counterparties: counterparties,
		Loans:       loanStore,
		Collections: loanCollectionsStore,
		Restructure: loanRestructureStore,
		Approvals:   approvalsStore,
		Notifier:    notifyClient,
		Logger:      logger,
	}
	loanReportsH := &handler.LoanReportsHandler{
		DB:        pool,
		Reports:   loanReportsStore,
		Loans:     loanStore,
		Approvals: approvalsStore,
		Logger:    logger,
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
	approvalsH := &handler.PendingApprovalsHandler{
		DB:          pool,
		Approvals:   approvalsStore,
		Deposit:     depositH,
		Share:       shareH,
		Loan:        loanH,
		LoanRepay:   loanRepayH,
		LoanCollect: loanCollectH,
		LoanReports: loanReportsH,
		Logger:      logger,
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
		Logger:         logger,
	}
	dividendH := &handler.DividendHandler{
		DB:                  pool,
		Tenants:             tenants,
		Members:             members,
		Counterparties: counterparties,
		Deposits:            depositStore,
		Shares:              shareStore,
		Dividends:           dividendStore,
		Notifier:            notifyClient,
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

	router := handler.Routes(handler.Deps{
		Share:       shareH,
		Deposit:     depositH,
		Product:     productH,
		Interest:    interestH,
		Dividend:    dividendH,
		LoanProduct: loanProductH,
		LoanApp:     loanAppH,
		Loan:        loanH,
		LoanRepay:   loanRepayH,
		LoanCollect: loanCollectH,
		LoanReports:  loanReportsH,
		Provisioning: provisioningH,
		MemberStmt:   memberStmtH,
		MemberLedger: memberLedgerH,
		Approvals:    approvalsH,
		Collection:   collectionDeskH,
		TenantStore:  tenants,
		Issuer:      issuer,
		AppDomain:   cfg.AppDomain,
		Logger:      logger,
	})

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
