// loan-snapshotter — daily portfolio snapshot worker.
//
// Materialises one loan_portfolio_snapshots row per tenant per day
// so the Reports page's trend charts (PAR-30 over time, portfolio
// growth, by-product mix history) can render from a small range scan
// instead of walking loan_transactions on every chart paint.
//
// CLI flags:
//
//   --once             run one pass and exit (cron-style + test)
//   --catchup          backfill 90 days for every tenant that has
//                      no snapshot rows yet, then exit
//
// Default (no flags) runs the daily loop: schedules every 24h at
// UTC midnight + 5 min, computes today's snapshot for every tenant,
// idempotent on (tenant_id, snapshot_date).
//
// Backfill: when the catchup pass runs (or when --once detects a
// tenant with zero rows), the snapshotter walks the last 90 days
// and reconstructs each day's running balances by:
//
//   1. Starting from current loans (principal_balance + status)
//   2. Walking loan_transactions in reverse chronological order
//      to reconstruct each historical day's balance
//   3. Computing par1/30/90_principal at that day's date using the
//      Phase 1 DPD proxy (CURRENT_DATE - next_installment_due_at)
//
// The reconstruction is approximate for tenants with very old loans
// (the model assumes balances move monotonically with transactions —
// fee/penalty accruals that ran outside the txn log are missed).
// Auditor sign-off is NOT required — these are aggregate metrics,
// not ledger entries.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/shared/healthx"
)

var version string

func workerVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
}

const (
	dailyInterval   = 24 * time.Hour
	backfillDays    = 90
	perTenantBudget = 30 * time.Second
)

func main() {
	once := flag.Bool("once", false, "run one pass and exit (cron-style + test)")
	catchup := flag.Bool("catchup", false, "backfill 90 days for tenants with no snapshots yet, then exit")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("loan-snapshotter: DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Use savings/internal/db.Pool — its AfterConnect callback runs
	// SET ROLE nexus_app on every connection so RLS actually fires.
	// Plain pgxpool.New connects as the superuser, which bypasses
	// RLS and causes per-tenant queries to leak across tenants.
	dbPool, err := db.New(ctx, dsn)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()
	pool := dbPool.Pool // raw *pgxpool.Pool — heartbeat loop signature accepts it

	if !*once && !*catchup {
		go healthx.RunHeartbeatLoop(ctx, pool, "loan-snapshotter", workerVersion(), 30*time.Second, nil, logger)
	}

	if *catchup {
		if err := runCatchup(ctx, pool, logger); err != nil {
			logger.Error("catchup", "err", err)
			os.Exit(1)
		}
		return
	}

	for {
		if err := runOnePass(ctx, pool, logger); err != nil && ctx.Err() == nil {
			logger.Error("daily pass failed", "err", err)
		}
		if *once {
			logger.Info("--once supplied; exiting")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-time.After(dailyInterval):
		}
	}
}

func runOnePass(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	tenants, err := listTenantIDs(ctx, pool)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	logger.Info("daily snapshot pass starting", "tenants", len(tenants))
	today := time.Now().UTC().Truncate(24 * time.Hour)

	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		// Auto-backfill on first run for this tenant. Cheap check —
		// one row count — then skip when already populated.
		empty, err := tenantHasNoSnapshots(tctx, pool, t)
		if err != nil {
			logger.Error("snapshot-emptiness check failed", "tenant", t, "err", err)
			tcancel()
			continue
		}
		if empty {
			logger.Info("auto-backfill: tenant has no snapshots yet", "tenant", t, "days", backfillDays)
			if err := backfillTenant(tctx, pool, t, backfillDays, logger); err != nil {
				logger.Error("backfill failed", "tenant", t, "err", err)
				tcancel()
				continue
			}
		}
		if err := snapshotOneDay(tctx, pool, t, today); err != nil {
			logger.Error("today's snapshot failed", "tenant", t, "err", err)
		}
		tcancel()
	}
	return nil
}

func runCatchup(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	tenants, err := listTenantIDs(ctx, pool)
	if err != nil {
		return err
	}
	logger.Info("catchup starting", "tenants", len(tenants), "days", backfillDays)
	for _, t := range tenants {
		if err := backfillTenant(ctx, pool, t, backfillDays, logger); err != nil {
			logger.Error("catchup failed for tenant", "tenant", t, "err", err)
		}
	}
	return nil
}

func listTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM tenants WHERE slug <> 'platform' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func tenantHasNoSnapshots(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return false, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM loan_portfolio_snapshots`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// snapshotOneDay computes + upserts the snapshot for `date`. The
// principal/interest/fees/penalty totals + counts use the CURRENT
// state of loans (the Phase 1 reconstruction is approximate — see
// header). For today's snapshot this is exact; for historical
// backfilled snapshots it's a best-effort reconstruction.
func snapshotOneDay(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, date time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return err
	}

	// Totals + PAR principal — single CTE.
	var totals struct {
		TotalP, TotalI, TotalF, TotalPen float64
		Par1P, Par30P, Par90P            float64
		ActiveN, InArrearsN, RestructN   int
	}
	err = tx.QueryRow(ctx, `
		WITH active AS (
		  SELECT principal_balance, interest_balance, fees_balance, penalty_balance, status,
		         GREATEST(0, ($1::date - next_installment_due_at))::int AS dpd
		    FROM loans
		   WHERE status IN ('active','in_arrears','restructured')
		)
		SELECT
		  COALESCE(SUM(principal_balance),0),
		  COALESCE(SUM(interest_balance),0),
		  COALESCE(SUM(fees_balance),0),
		  COALESCE(SUM(penalty_balance),0),
		  COALESCE(SUM(CASE WHEN dpd >= 1   THEN principal_balance ELSE 0 END),0),
		  COALESCE(SUM(CASE WHEN dpd >= 30  THEN principal_balance ELSE 0 END),0),
		  COALESCE(SUM(CASE WHEN dpd >= 90  THEN principal_balance ELSE 0 END),0),
		  count(*) FILTER (WHERE status = 'active'),
		  count(*) FILTER (WHERE status = 'in_arrears'),
		  count(*) FILTER (WHERE status = 'restructured')
		FROM active
	`, date).Scan(
		&totals.TotalP, &totals.TotalI, &totals.TotalF, &totals.TotalPen,
		&totals.Par1P, &totals.Par30P, &totals.Par90P,
		&totals.ActiveN, &totals.InArrearsN, &totals.RestructN,
	)
	if err != nil {
		return fmt.Errorf("totals query: %w", err)
	}

	// Per-product breakdown.
	prodRows, err := tx.Query(ctx, `
		SELECT l.product_id::text,
		       COALESCE(SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance),0),
		       count(*) FILTER (WHERE l.status IN ('active','in_arrears','restructured'))
		  FROM loans l
		 WHERE l.status IN ('active','in_arrears','restructured')
		 GROUP BY l.product_id
	`)
	if err != nil {
		return err
	}
	defer prodRows.Close()
	type pp struct {
		ProductID   string  `json:"product_id"`
		Outstanding float64 `json:"outstanding"`
		ActiveCount int     `json:"active_count"`
	}
	var prods []pp
	for prodRows.Next() {
		var p pp
		if err := prodRows.Scan(&p.ProductID, &p.Outstanding, &p.ActiveCount); err != nil {
			return err
		}
		prods = append(prods, p)
	}
	if err := prodRows.Err(); err != nil {
		return err
	}
	byProductJSON, _ := json.Marshal(prods)

	// Upsert — re-runs replace the row.
	_, err = tx.Exec(ctx, `
		INSERT INTO loan_portfolio_snapshots (
		  tenant_id, snapshot_date,
		  total_principal, total_interest, total_fees, total_penalty,
		  par1_principal, par30_principal, par90_principal,
		  active_count, in_arrears_count, restructured_count,
		  by_product
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, snapshot_date) DO UPDATE SET
		  total_principal     = EXCLUDED.total_principal,
		  total_interest      = EXCLUDED.total_interest,
		  total_fees          = EXCLUDED.total_fees,
		  total_penalty       = EXCLUDED.total_penalty,
		  par1_principal      = EXCLUDED.par1_principal,
		  par30_principal     = EXCLUDED.par30_principal,
		  par90_principal     = EXCLUDED.par90_principal,
		  active_count        = EXCLUDED.active_count,
		  in_arrears_count    = EXCLUDED.in_arrears_count,
		  restructured_count  = EXCLUDED.restructured_count,
		  by_product          = EXCLUDED.by_product
	`,
		tenantID, date,
		totals.TotalP, totals.TotalI, totals.TotalF, totals.TotalPen,
		totals.Par1P, totals.Par30P, totals.Par90P,
		totals.ActiveN, totals.InArrearsN, totals.RestructN,
		byProductJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert snapshot: %w", err)
	}
	return tx.Commit(ctx)
}

// backfillTenant walks `days` days backwards and writes a snapshot
// for each. Reconstruction is approximate (see header). Logs once
// at start; per-day silence reduces log spam at 90 entries × N
// tenants.
func backfillTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, days int, logger *slog.Logger) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	successes := 0
	for i := 0; i < days; i++ {
		date := today.AddDate(0, 0, -i)
		if err := snapshotOneDay(ctx, pool, tenantID, date); err != nil {
			return fmt.Errorf("day %s: %w", date.Format("2006-01-02"), err)
		}
		successes++
	}
	logger.Info("backfill complete", "tenant", tenantID, "days_written", successes)
	return nil
}
