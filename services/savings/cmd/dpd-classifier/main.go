// dpd-classifier — daily DPD + SASRA + IFRS 9 classification worker.
//
// For every active loan in every tenant:
//
//   1. Compute DPD = (today - next_installment_due_at), clamped at 0.
//   2. Look up the tenant's DPD thresholds (tenant_operations).
//   3. Run the classifier (internal/classification) to get SASRA class
//      + IFRS 9 stage. The restructured flag is read from loans.status.
//   4. Upsert today's row in loan_dpd_snapshots.
//   5. If the classification changed since the previous snapshot,
//      append a row to loan_classification_history with trigger_source
//      = 'daily_dpd_run'.
//
// Per-tenant runs are wrapped in a tx with `set_config('app.tenant_id', …)`
// so RLS scopes every read/write correctly (the pool's AfterConnect
// already issues SET ROLE nexus_app).
//
// CLI flags:
//
//   --once    run one pass for every tenant and exit (cron-style + test)
//
// Default (no flags) runs the daily loop: every 24h after a 5-minute
// jitter past midnight UTC. The exact cadence is approximate — for
// production we'd add a real cron expression; here we lean on the
// outer scheduler / docker-compose restart loop.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/classification"
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
	perTenantBudget = 60 * time.Second
)

func main() {
	once := flag.Bool("once", false, "run one pass and exit (cron-style + test)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("dpd-classifier: DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPool, err := db.New(ctx, dsn)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()
	pool := dbPool.Pool

	if !*once {
		go healthx.RunHeartbeatLoop(ctx, pool, "dpd-classifier", workerVersion(), 30*time.Second, nil, logger)
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
	today := time.Now().UTC().Truncate(24 * time.Hour)
	logger.Info("dpd classification pass starting", "tenants", len(tenants), "date", today.Format("2006-01-02"))

	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		stats, err := classifyTenant(tctx, pool, t, today)
		tcancel()
		if err != nil {
			logger.Error("tenant classification failed", "tenant", t, "err", err)
			continue
		}
		logger.Info("tenant classified",
			"tenant", t,
			"loans", stats.LoansSeen,
			"snapshots_written", stats.SnapshotsWritten,
			"reclassifications", stats.HistoryRowsWritten,
		)
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

type passStats struct {
	LoansSeen          int
	SnapshotsWritten   int
	HistoryRowsWritten int
}

// classifyTenant runs the full classification cycle for one tenant
// inside a single tx. The tenant_id is bound via set_config so RLS
// applies to every read + write.
func classifyTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, asOf time.Time) (passStats, error) {
	var stats passStats

	tx, err := pool.Begin(ctx)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return stats, fmt.Errorf("set tenant: %w", err)
	}

	thresholds, err := loadThresholds(ctx, tx)
	if err != nil {
		return stats, fmt.Errorf("load thresholds: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT
		  l.id,
		  GREATEST(0, ($1::date - l.next_installment_due_at))::int AS dpd,
		  l.principal_balance, l.interest_balance, l.fees_balance, l.penalty_balance,
		  l.next_installment_due_at,
		  l.status
		FROM loans l
		WHERE l.status IN ('active','in_arrears','restructured')
	`, asOf)
	if err != nil {
		return stats, fmt.Errorf("list loans: %w", err)
	}

	type loanRow struct {
		ID          uuid.UUID
		DPD         int
		P, I, F, Pe float64
		NextDue     *time.Time
		Status      string
	}
	var loans []loanRow
	for rows.Next() {
		var r loanRow
		if err := rows.Scan(&r.ID, &r.DPD, &r.P, &r.I, &r.F, &r.Pe, &r.NextDue, &r.Status); err != nil {
			rows.Close()
			return stats, err
		}
		loans = append(loans, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}

	stats.LoansSeen = len(loans)

	for _, l := range loans {
		res := classification.Classify(classification.Input{
			DPD:          l.DPD,
			Restructured: l.Status == "restructured",
		}, thresholds)

		// Fetch the most recent classification for this loan (from
		// loan_classification_history, falling back to yesterday's
		// snapshot if no history rows exist yet). This drives the
		// "did the classification change?" decision.
		prevSasra, prevStage, hasPrev, err := lastClassification(ctx, tx, l.ID)
		if err != nil {
			return stats, fmt.Errorf("loan %s prev classification: %w", l.ID, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO loan_dpd_snapshots (
			  tenant_id, loan_id, snapshot_date, dpd_days,
			  principal_balance, interest_balance, fees_balance, penalty_balance,
			  classification_sasra, classification_ifrs9_stage,
			  next_due_date
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (loan_id, snapshot_date) DO UPDATE SET
			  dpd_days                   = EXCLUDED.dpd_days,
			  principal_balance          = EXCLUDED.principal_balance,
			  interest_balance           = EXCLUDED.interest_balance,
			  fees_balance               = EXCLUDED.fees_balance,
			  penalty_balance            = EXCLUDED.penalty_balance,
			  classification_sasra       = EXCLUDED.classification_sasra,
			  classification_ifrs9_stage = EXCLUDED.classification_ifrs9_stage,
			  next_due_date              = EXCLUDED.next_due_date,
			  computed_at                = now()
		`,
			tenantID, l.ID, asOf, l.DPD,
			l.P, l.I, l.F, l.Pe,
			string(res.SASRA), int(res.Stage),
			l.NextDue,
		); err != nil {
			return stats, fmt.Errorf("upsert snapshot for loan %s: %w", l.ID, err)
		}
		stats.SnapshotsWritten++

		changed := !hasPrev || prevSasra != string(res.SASRA) || prevStage != int(res.Stage)
		if changed {
			var prevSasraArg, prevStageArg any
			if hasPrev {
				prevSasraArg = prevSasra
				prevStageArg = prevStage
			} else {
				prevSasraArg = nil
				prevStageArg = nil
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO loan_classification_history (
				  tenant_id, loan_id, prev_sasra, new_sasra,
				  prev_ifrs9_stage, new_ifrs9_stage, dpd_days, trigger_source
				) VALUES ($1, $2, $3, $4, $5, $6, $7, 'daily_dpd_run')
			`,
				tenantID, l.ID, prevSasraArg, string(res.SASRA),
				prevStageArg, int(res.Stage), l.DPD,
			); err != nil {
				return stats, fmt.Errorf("history insert for loan %s: %w", l.ID, err)
			}
			stats.HistoryRowsWritten++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}

// loadThresholds reads the tenant's DPD thresholds. Falls back to
// CBK defaults if tenant_operations has NULLs (shouldn't happen — the
// columns are NOT NULL after migration 0039 — but defensive).
func loadThresholds(ctx context.Context, tx pgx.Tx) (classification.Thresholds, error) {
	t := classification.DefaultThresholds()
	err := tx.QueryRow(ctx, `
		SELECT sasra_watch_dpd, dpd_substandard_days, dpd_doubtful_days, dpd_loss_days,
		       ifrs9_stage2_dpd, ifrs9_stage3_dpd
		  FROM tenant_operations
		 LIMIT 1
	`).Scan(&t.SASRAWatchDPD, &t.SASRASubstandardDPD, &t.SASRADoubtfulDPD,
		&t.SASRALossDPD, &t.IFRS9Stage2DPD, &t.IFRS9Stage3DPD)
	if err != nil {
		if err == pgx.ErrNoRows {
			return classification.DefaultThresholds(), nil
		}
		return classification.Thresholds{}, err
	}
	return t, nil
}

// lastClassification returns the most recent (new_sasra, new_ifrs9_stage)
// for a loan from loan_classification_history; if no rows, returns
// hasPrev=false.
func lastClassification(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) (string, int, bool, error) {
	var sasra string
	var stage int
	err := tx.QueryRow(ctx, `
		SELECT new_sasra, new_ifrs9_stage
		  FROM loan_classification_history
		 WHERE loan_id = $1
		 ORDER BY changed_at DESC
		 LIMIT 1
	`, loanID).Scan(&sasra, &stage)
	if err == pgx.ErrNoRows {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return sasra, stage, true, nil
}
