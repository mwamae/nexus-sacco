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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/auth"
	"github.com/nexussacco/accounting/internal/config"
	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
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
	runBackfill := flag.Bool("run-backfill", false,
		"backfill the GL with prior-period adjustment JEs for every account whose subledger disagrees with the GL. Iterates all tenants. Backdated to each tenant's FY start. Contra-account is 3010 Retained Earnings. Idempotent — re-running on a clean state is a no-op.")
	backfillDryRun := flag.Bool("backfill-dry-run", true,
		"when used with -run-backfill, print the proposed JEs to stdout without committing. Default true — must explicitly pass -backfill-dry-run=false to commit.")
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

	if *runBackfill {
		runBackfillAcrossTenants(ctx, pool, tenants, reportStore, engine, *backfillDryRun, logger)
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

// ─────────── Backfill ───────────
//
// Posts prior-period-adjustment JEs to bring the GL into line with
// every subledger that drifted before its handler was outbox-wired.
// Re-runnable: each backfill JE has a deterministic source_ref of
// `backfill-<code>-<tenant_slug>`, and the accounting service's
// dedup on (source_module, source_ref) means a second run will skip
// already-posted backfills. Dry-run mode prints the proposed JEs
// without committing.
//
// Iteration: every tenant in the directory. Per tenant: run the
// reconciliation report; for each row with |delta| >= 1 KES, build a
// balanced JE with the contra-leg on 3010 Retained Earnings (prior-
// period adjustment convention). Backdated to the tenant's FY start.
func runBackfillAcrossTenants(
	ctx context.Context,
	pool *db.Pool,
	tenants *store.TenantStore,
	reports *store.ReportStore,
	engine *posting.Engine,
	dryRun bool,
	logger *slog.Logger,
) {
	mode := "DRY-RUN (no commits)"
	if !dryRun {
		mode = "EXECUTE (committing)"
	}
	logger.Info("backfill: starting", "mode", mode)

	type tenantSnap struct {
		ID   uuid.UUID
		Slug string
	}
	rows, err := pool.Query(ctx, `SELECT id, slug FROM tenants ORDER BY slug`)
	if err != nil {
		logger.Error("backfill: list tenants", "err", err)
		os.Exit(1)
	}
	var tenantList []tenantSnap
	for rows.Next() {
		var t tenantSnap
		if err := rows.Scan(&t.ID, &t.Slug); err != nil {
			rows.Close()
			logger.Error("backfill: scan tenant", "err", err)
			os.Exit(1)
		}
		tenantList = append(tenantList, t)
	}
	rows.Close()
	_ = tenants // silence unused param when List() not used
	reconStore := store.NewReconciliationStore(pool.Pool)

	type proposed struct {
		Tenant   string
		AsOf     time.Time
		Code     string
		Name     string
		DR, CR   string
		Amount   string
	}
	var allProposed []proposed
	var allPosted int

	for _, t := range tenantList {
		var fyStart time.Time
		var report *store.SubledgerReconciliationReport
		if err := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
			s, err := reports.FiscalYearStartTx(ctx, tx, time.Now())
			if err != nil {
				return err
			}
			fyStart = s
			rep, err := reconStore.ReconciliationTx(ctx, tx, time.Now())
			if err != nil {
				return err
			}
			report = rep
			return nil
		}); err != nil {
			logger.Error("backfill: tenant", "slug", t.Slug, "err", err)
			continue
		}
		logger.Info("backfill: tenant",
			"slug", t.Slug, "fy_start", fyStart.Format("2006-01-02"),
			"overall_status", report.OverallStatus, "rows", len(report.Rows))

		if report.OverallStatus == "ok" {
			continue
		}

		for _, row := range report.Rows {
			if row.Status == "ok" {
				continue
			}
			gl, _ := decimal.NewFromString(row.GLBalance.String())
			sub, _ := decimal.NewFromString(row.SubledgerBalance.String())
			// drift = subledger - GL (signed); the JE moves the GL by
			// abs(drift) in the right direction to close the gap.
			drift := sub.Sub(gl)
			if drift.Abs().LessThan(decimal.NewFromInt(1)) {
				continue
			}

			dr, cr := backfillPolarity(row.Code, drift)
			p := proposed{
				Tenant: t.Slug, AsOf: fyStart,
				Code: row.Code, Name: row.Name,
				DR: dr, CR: cr, Amount: drift.Abs().StringFixed(2),
			}
			allProposed = append(allProposed, p)

			if dryRun {
				continue
			}

			// Real run: post via the standard engine.
			if err := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
				_, perr := engine.PostTx(ctx, tx, posting.PostInput{
					EntryDate:    fyStart,
					ValueDate:    fyStart,
					EntryType:    domain.TypeAuto,
					SourceModule: "ops.backfill",
					SourceRef:    fmt.Sprintf("backfill-%s-%s", row.Code, t.Slug),
					Narration: fmt.Sprintf(
						"Backfill: bring %s (%s) into line with subledger — prior-period drift from before the outbox/GL wiring shipped",
						row.Code, row.Name),
					Lines: []posting.Line{
						{AccountCode: dr, Debit: drift.Abs(), Narration: "Backfill DR"},
						{AccountCode: cr, Credit: drift.Abs(), Narration: "Backfill CR"},
					},
				})
				return perr
			}); err != nil {
				logger.Error("backfill: post failed",
					"slug", t.Slug, "code", row.Code,
					"drift", drift.StringFixed(2), "err", err)
				os.Exit(1)
			}
			allPosted++
			logger.Info("backfill: posted",
				"slug", t.Slug, "code", row.Code,
				"DR", dr, "CR", cr, "amount", drift.Abs().StringFixed(2))
		}
	}

	// Summary
	fmt.Fprintf(os.Stdout, "\n==== Backfill %s ====\n", mode)
	fmt.Fprintf(os.Stdout, "Tenants scanned: %d · proposed JEs: %d · posted: %d\n\n",
		len(tenantList), len(allProposed), allPosted)
	if len(allProposed) == 0 {
		fmt.Fprintln(os.Stdout, "Nothing to backfill — all tenants reconciled.")
		return
	}
	fmt.Fprintf(os.Stdout, "%-20s %-12s %-4s %-30s %-4s %-4s %14s\n",
		"TENANT", "ENTRY DATE", "CODE", "ACCOUNT", "DR", "CR", "AMOUNT")
	for _, p := range allProposed {
		fmt.Fprintf(os.Stdout, "%-20s %-12s %-4s %-30s %-4s %-4s %14s\n",
			p.Tenant, p.AsOf.Format("2006-01-02"), p.Code, truncate(p.Name, 30),
			p.DR, p.CR, p.Amount)
	}
	if dryRun {
		fmt.Fprintln(os.Stdout, "\nDRY-RUN — re-run with -backfill-dry-run=false to commit.")
	}
}

// backfillPolarity picks the DR/CR account codes for a drift on the
// given subledger account. Contra is always 3010 Retained Earnings
// (prior-period-adjustment convention). The direction depends on the
// account's natural balance side and which side of the drift the GL
// is on:
//
//   subledger > GL on a credit-natural account (2000/3000/2050/2200):
//     CR <account> = drift, DR 3010 = drift  (liability/equity goes up)
//
//   subledger > GL on a debit-natural account (1100/1110):
//     DR <account> = drift, CR 3010 = drift  (asset goes up)
//
//   subledger > GL on a contra-asset (1120 Loan Loss Provision —
//   credit-natural):
//     CR 1120 = drift, DR 3010 = drift  (provision goes up; equity down)
//
// When drift is negative (GL > subledger), the same accounts swap
// sides.
func backfillPolarity(code string, drift decimal.Decimal) (dr, cr string) {
	const contra = "3010"
	debitNormal := isDebitNormalAccount(code)
	subledgerHigher := drift.GreaterThan(decimal.Zero)
	switch {
	case debitNormal && subledgerHigher:
		return code, contra
	case debitNormal && !subledgerHigher:
		return contra, code
	case !debitNormal && subledgerHigher:
		return contra, code
	default:
		return code, contra
	}
}

func isDebitNormalAccount(code string) bool {
	// Mirror the ReconciliationStore.staticAccountSpecs.DebitNormal
	// settings: only 1100 + 1110 are debit-natural in the recon set.
	// 1120 is a contra-asset (credit-natural); 2000/2050/2100/2200/
	// 3000 are liabilities/equity (credit-natural).
	switch code {
	case "1100", "1110":
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
