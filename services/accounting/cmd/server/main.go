// Accounting service entry point — Stage 11 foundation.
//
// Listens on :8086 (configurable via ACCOUNTING_HTTP_ADDR), serves
// Chart of Accounts CRUD, period management, manual journal entries
// with maker/checker, and trial-balance + GL-detail reports.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/accounting/internal/auth"
	"github.com/nexussacco/accounting/internal/config"
	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/handler"
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	runRecon := flag.String("run-reconciliation", "",
		"run the subledger reconciliation report for the named tenant slug, print JSON to stdout, exit non-zero on overall_status=error (for ops cron + email-on-failure)")
	reconAsOf := flag.String("recon-as-of", "",
		"reconciliation as-of date in YYYY-MM-DD (defaults to today)")
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
		logger.Info("accounting migrations applied")
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	coaStore := store.NewCoAStore(pool.Pool)
	periodStore := store.NewPeriodStore(pool.Pool)
	journalStore := store.NewJournalStore(pool.Pool)
	reportStore := store.NewReportStore(pool.Pool)
	fyStore := store.NewFiscalYearStore(pool.Pool)
	fyProposalStore := store.NewFiscalYearProposalStore(pool.Pool)
	bankStore := store.NewBankStore(pool.Pool)
	cashStore := store.NewCashStore(pool.Pool)
	fixedAssetStore := store.NewFixedAssetStore(pool.Pool)
	budgetStore := store.NewBudgetStore(pool.Pool)

	engine := &posting.Engine{
		CoA:      coaStore,
		Periods:  periodStore,
		Journals: journalStore,
	}

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	router := handler.Routes(handler.Deps{
		CoA:      &handler.CoAHandler{DB: pool, CoA: coaStore, Logger: logger},
		Periods:  &handler.PeriodHandler{DB: pool, Periods: periodStore, Logger: logger},
		Journals: &handler.JournalHandler{
			DB: pool, CoA: coaStore, Journals: journalStore,
			Periods: periodStore, Engine: engine, Logger: logger,
			// PR #7 — Unified Inbox workflow integration.
			WorkflowURL:           cfg.WorkflowURL,
			AccountingSelfURL:     cfg.AccountingSelfURL,
			WorkflowInternalToken: cfg.WorkflowInternalToken,
			HTTP:                  &http.Client{Timeout: 10 * time.Second},
		},
		Reports: &handler.ReportHandler{
			DB:         pool,
			Reports:    reportStore,
			ReconStore: store.NewReconciliationStore(pool.Pool),
			Logger:     logger,
		},
		FiscalYear: &handler.FiscalYearHandler{
			DB: pool, FY: fyStore, Proposals: fyProposalStore,
			Periods: periodStore, Engine: engine, Logger: logger,
			WorkflowURL:           cfg.WorkflowURL,
			AccountingSelfURL:     cfg.AccountingSelfURL,
			WorkflowInternalToken: cfg.WorkflowInternalToken,
			HTTP:                  &http.Client{Timeout: 10 * time.Second},
		},
		Bank: &handler.BankHandler{
			DB: pool, Bank: bankStore, CoA: coaStore, Engine: engine, Logger: logger,
		},
		Cash: &handler.CashHandler{
			DB: pool, Cash: cashStore, Engine: engine, Logger: logger,
		},
		FixedAssets: &handler.FixedAssetsHandler{
			DB: pool, Assets: fixedAssetStore, CoA: coaStore, Engine: engine, Logger: logger,
		},
		Budget: &handler.BudgetHandler{
			DB: pool, Budgets: budgetStore, Logger: logger,
		},
		Export: &handler.ExportHandler{
			DB: pool, Reports: reportStore, Budgets: budgetStore,
			Tenants: tenants, Logger: logger,
		},
		InternalPost: &handler.InternalPostHandler{
			DB: pool, Engine: engine,
			InternalToken: cfg.InternalToken, Logger: logger,
		},
		TenantStore: tenants,
		Issuer:      issuer,
		AppDomain:   cfg.AppDomain,
		Logger:      logger,
	})

	// CLI: subledger reconciliation. One-shot mode for ops cron;
	// emits JSON to stdout and exits with status code reflecting the
	// overall_status:
	//   • 0 → ok
	//   • 1 → warn  (drift exists but within tolerance)
	//   • 2 → error (one or more accounts diverged beyond tolerance)
	// Shell wrapper for daily check:
	//   0 6 * * * acct -run-reconciliation=tujenge || mail finance@…
	if *runRecon != "" {
		asOf := time.Now()
		if *reconAsOf != "" {
			t, perr := time.Parse("2006-01-02", *reconAsOf)
			if perr != nil {
				logger.Error("reconciliation: -recon-as-of must be YYYY-MM-DD", "err", perr)
				os.Exit(1)
			}
			asOf = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
		}
		t, terr := tenants.BySlug(ctx, *runRecon)
		if terr != nil {
			logger.Error("reconciliation: tenant lookup", "slug", *runRecon, "err", terr)
			os.Exit(1)
		}
		reconStore := store.NewReconciliationStore(pool.Pool)
		var report *store.SubledgerReconciliationReport
		rerr := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
			var err error
			report, err = reconStore.ReconciliationTx(ctx, tx, asOf)
			return err
		})
		if rerr != nil {
			logger.Error("reconciliation: run", "err", rerr)
			os.Exit(1)
		}
		body, _ := json.MarshalIndent(report, "", "  ")
		_, _ = os.Stdout.Write(body)
		_, _ = os.Stdout.WriteString("\n")
		switch report.OverallStatus {
		case "error":
			os.Exit(2)
		case "warn":
			os.Exit(1)
		}
		return
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	go func() {
		logger.Info("accounting listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10_000_000_000)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
}

func newLogger(level, env string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: l}
	if env == "production" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
