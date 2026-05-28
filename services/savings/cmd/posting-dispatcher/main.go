// Posting outbox dispatcher.
//
// Drains posting_outbox rows that the in-tx PostTx writes from the
// savings + member services have queued. For each pending row:
//
//   1. Mark the row "in-flight" with FOR UPDATE SKIP LOCKED so two
//      dispatcher instances can run side-by-side without double-
//      posting.
//   2. Replay the payload against the accounting service via the
//      existing HTTP path (accounting is idempotent on
//      (source_module, source_ref) so safe to retry).
//   3. On success: stamp dispatched_at + posted_je_id, commit.
//      On failure: bump attempts + last_error, commit. Backoff +
//      hard-fail at attempts >= 12 are read by the poll query so
//      the row drops out of the pending set automatically.
//
// Exponential backoff between attempts: NOT_BEFORE = enqueued_at +
// 2^attempts * 5s, capped at 1h. Hard-fail at 12 attempts (roughly
// 4h of retries spread across a day) → the stuck-viewer endpoint
// at /v1/finance/posting-outbox?status=stuck surfaces these for
// on-call.
//
// Graceful shutdown on SIGINT/SIGTERM: finish the in-flight row,
// then exit.

package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/shared/healthx"
)

// version is overridden at link time. Reported in worker_heartbeats
// so the system-health dashboard can confirm every worker is on the
// expected SHA after a rollout.
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
	pollInterval    = 1 * time.Second
	maxAttempts     = 12
	backoffBaseUnit = 5 * time.Second
	backoffMaxUnit  = 1 * time.Hour
)

type outboxRow struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Payload    json.RawMessage
	Attempts   int
	EnqueuedAt time.Time
}

// outboxJSON mirrors the shape posting.Client.PostTx writes into
// the payload column. Kept here (not imported) so the dispatcher
// stays decoupled from the in-tx writer's exact struct.
type outboxJSON struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	EntryDate    string    `json:"entry_date,omitempty"`
	ValueDate    string    `json:"value_date,omitempty"`
	SourceModule string    `json:"source_module"`
	SourceRef    string    `json:"source_ref"`
	Narration    string    `json:"narration"`
	Lines        []struct {
		AccountCode string `json:"account_code"`
		Debit       string `json:"debit,omitempty"`
		Credit      string `json:"credit,omitempty"`
		Narration   string `json:"narration,omitempty"`
	} `json:"lines"`
}

func main() {
	once := flag.Bool("once", false, "drain one batch and exit (useful for cron-style deployments)")
	catchup := flag.Bool("catchup", false,
		"backlog drain mode — iterate until pending=0, rate-limited to 5/sec so the accounting service doesn't fall over. Run this once after upgrading from a pre-dispatcher version of savings.")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	postingClient, perr := posting.New(cfg.AccountingURL, cfg.AccountingInternalToken, logger)
	if perr != nil {
		logger.Error("posting-dispatcher: cannot start without accounting",
			"err", perr,
			"accounting_url", cfg.AccountingURL,
			"hint", "ACCOUNTING_SERVICE_URL is the env var (default http://localhost:8086). Set SAVINGS_ALLOW_NO_ACCOUNTING=true for tests only.")
		os.Exit(1)
	}
	if postingClient.DryRun {
		logger.Warn("posting-dispatcher: DRY-RUN mode — outbox rows will NOT be replayed. SAVINGS_ALLOW_NO_ACCOUNTING is set.")
	}

	// Heartbeat — every 30s upsert worker_heartbeats. Goroutine; the
	// signal-aware ctx kills it on shutdown. Skipped in -once mode
	// because that's a single-shot cron-style run, not a long-lived
	// worker the dashboard would expect to see a fresh heartbeat
	// from.
	if !*once {
		go healthx.RunHeartbeatLoop(
			ctx, pool, "posting-dispatcher", workerVersion(),
			30*time.Second, nil, logger,
		)
	}

	// Catchup mode — backlog drain at 5/sec, exits when pending=0.
	// Pre-dispatcher savings deploys accumulated outbox rows that
	// were never replayed; flushing the whole backlog at full
	// dispatcher speed risks overwhelming a freshly-started
	// accounting service.
	if *catchup {
		runCatchup(ctx, pool, postingClient, logger)
		return
	}

	logger.Info("posting-dispatcher: starting",
		"poll_interval", pollInterval, "max_attempts", maxAttempts,
		"once", *once)

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		drained, err := drain(ctx, pool, postingClient, logger)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("drain loop", "err", err)
		}
		if *once {
			logger.Info("posting-dispatcher: --once supplied, exiting", "drained", drained)
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("posting-dispatcher: shutting down")
			return
		case <-tick.C:
		}
	}
}

// runCatchup walks the entire pending backlog at a steady 5/sec
// (200ms between rows). Logs a structured summary at the end so the
// operator running the catchup pass has the count of dispatched +
// failed + hard-failed rows.
func runCatchup(ctx context.Context, pool *pgxpool.Pool, client *posting.Client, logger *slog.Logger) {
	const catchupRatePerSec = 5
	rowInterval := time.Second / catchupRatePerSec

	start := time.Now()
	stats := struct {
		Iterations   int
		RowsHandled  int
		HardFailures int
	}{}
	logger.Info("posting-dispatcher: catchup mode starting",
		"rate_per_sec", catchupRatePerSec, "max_attempts", maxAttempts)

	for {
		if ctx.Err() != nil {
			logger.Warn("posting-dispatcher: catchup interrupted", "rows_handled", stats.RowsHandled)
			break
		}
		rows, err := pickPending(ctx, pool)
		if err != nil {
			logger.Error("catchup pickPending", "err", err)
			break
		}
		if len(rows) == 0 {
			break
		}
		stats.Iterations++
		for _, r := range rows {
			if ctx.Err() != nil {
				break
			}
			processRow(ctx, pool, client, logger, r)
			stats.RowsHandled++
			if r.Attempts+1 >= maxAttempts {
				stats.HardFailures++
			}
			time.Sleep(rowInterval)
		}
	}

	// Final pending count for the summary.
	var pending int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FROM posting_outbox WHERE dispatched_at IS NULL
	`).Scan(&pending)

	logger.Info("posting-dispatcher: catchup complete",
		"duration", time.Since(start).Round(time.Millisecond),
		"iterations", stats.Iterations,
		"rows_handled", stats.RowsHandled,
		"hard_failures_this_run", stats.HardFailures,
		"pending_remaining", pending,
		"hint", "If pending_remaining > 0 the remaining rows have hit max_attempts. Inspect via /v1/finance/posting-outbox?status=stuck.")
}

// drain pulls every currently-pending row in one pass. Returns the
// count handled (success + failure).
func drain(ctx context.Context, pool *pgxpool.Pool, client *posting.Client, logger *slog.Logger) (int, error) {
	rows, err := pickPending(ctx, pool)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, r := range rows {
		processRow(ctx, pool, client, logger, r)
		if ctx.Err() != nil {
			return len(rows), nil
		}
	}
	return len(rows), nil
}

// pickPending — read-only scan; the row-level FOR UPDATE happens
// per-row inside processRow so two dispatchers don't race on the
// same row. We re-fetch each row's lock state with SKIP LOCKED in
// the per-row tx; this initial scan just narrows the candidate set
// so the dispatcher doesn't burn CPU walking the whole table on
// every tick.
func pickPending(ctx context.Context, pool *pgxpool.Pool) ([]outboxRow, error) {
	// Postgres can't filter by a CASE-derived NOT_BEFORE in a
	// partial index, so we do it inline. attempts >= maxAttempts
	// short-circuits the row out — hard-failed rows stop being
	// considered.
	const q = `
		SELECT id, tenant_id, payload, attempts, enqueued_at
		  FROM posting_outbox
		 WHERE dispatched_at IS NULL
		   AND attempts < $1
		   AND enqueued_at + (LEAST(POW(2, attempts) * $2, $3) || ' seconds')::interval <= now()
		 ORDER BY enqueued_at
		 LIMIT 100
	`
	pgRows, err := pool.Query(ctx, q,
		maxAttempts,
		backoffBaseUnit.Seconds(),
		backoffMaxUnit.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer pgRows.Close()
	var out []outboxRow
	for pgRows.Next() {
		var r outboxRow
		if err := pgRows.Scan(&r.ID, &r.TenantID, &r.Payload, &r.Attempts, &r.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, pgRows.Err()
}

// processRow runs one outbox row end-to-end inside its own tx. The
// row is row-locked via SKIP LOCKED so two dispatcher instances
// don't double-post.
func processRow(ctx context.Context, pool *pgxpool.Pool, client *posting.Client, logger *slog.Logger, r outboxRow) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("begin tx", "row", r.ID, "err", err)
		return
	}
	defer tx.Rollback(ctx)

	// RLS scope — the row is tenant-scoped; set the GUC for any
	// post-update queries the dispatcher might fan to later.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`,
		r.TenantID.String(),
	); err != nil {
		logger.Error("set tenant", "row", r.ID, "err", err)
		return
	}

	// Re-fetch with FOR UPDATE SKIP LOCKED to claim ownership.
	var stillPending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM posting_outbox
		   WHERE id = $1 AND dispatched_at IS NULL
		   FOR UPDATE SKIP LOCKED
		)
	`, r.ID).Scan(&stillPending); err != nil {
		logger.Error("claim row", "row", r.ID, "err", err)
		return
	}
	if !stillPending {
		// Another dispatcher beat us to it, or the row was already
		// dispatched between pick and process. No-op.
		return
	}

	var p outboxJSON
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		recordFailure(ctx, tx, logger, r, fmt.Errorf("payload unmarshal: %w", err))
		_ = tx.Commit(ctx)
		return
	}

	in := posting.PostInput{
		TenantID:     p.TenantID,
		SourceModule: p.SourceModule,
		SourceRef:    p.SourceRef,
		Narration:    p.Narration,
	}
	if p.EntryDate != "" {
		if t, e := time.Parse("2006-01-02", p.EntryDate); e == nil {
			in.EntryDate = t
		}
	}
	if p.ValueDate != "" {
		if t, e := time.Parse("2006-01-02", p.ValueDate); e == nil {
			in.ValueDate = t
		}
	}
	for _, l := range p.Lines {
		ln := posting.Line{AccountCode: l.AccountCode, Narration: l.Narration}
		if l.Debit != "" {
			d, derr := decimal.NewFromString(l.Debit)
			if derr != nil {
				recordFailure(ctx, tx, logger, r, fmt.Errorf("parse debit %q: %w", l.Debit, derr))
				_ = tx.Commit(ctx)
				return
			}
			ln.Debit = d
		}
		if l.Credit != "" {
			d, derr := decimal.NewFromString(l.Credit)
			if derr != nil {
				recordFailure(ctx, tx, logger, r, fmt.Errorf("parse credit %q: %w", l.Credit, derr))
				_ = tx.Commit(ctx)
				return
			}
			ln.Credit = d
		}
		in.Lines = append(in.Lines, ln)
	}

	if err := client.Post(ctx, in); err != nil {
		recordFailure(ctx, tx, logger, r, err)
		_ = tx.Commit(ctx)
		return
	}

	// Source ref doubles as the JE handle on the upstream row;
	// stamp it as posted_je_id so the audit chain reads back.
	// (The accounting service returns its own internal JE id but
	// we don't propagate it — handlers already stamp source_ref as
	// the synthetic JE handle.)
	jeID, _ := uuid.Parse(p.SourceRef)
	if _, err := tx.Exec(ctx, `
		UPDATE posting_outbox
		   SET dispatched_at = now(), posted_je_id = $2, last_error = NULL
		 WHERE id = $1
	`, r.ID, nullableUUID(jeID)); err != nil {
		logger.Error("stamp dispatched", "row", r.ID, "err", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("commit success", "row", r.ID, "err", err)
		return
	}
	logger.Info("posting-dispatcher: posted",
		"row", r.ID, "tenant", r.TenantID,
		"source_module", p.SourceModule, "source_ref", p.SourceRef)
}

func recordFailure(ctx context.Context, tx pgx.Tx, logger *slog.Logger, r outboxRow, err error) {
	logger.Warn("posting-dispatcher: post failed",
		"row", r.ID, "tenant", r.TenantID,
		"attempt", r.Attempts+1, "err", err)
	if _, uerr := tx.Exec(ctx, `
		UPDATE posting_outbox
		   SET attempts = attempts + 1,
		       last_error = $2
		 WHERE id = $1
	`, r.ID, err.Error()); uerr != nil {
		logger.Error("stamp failure", "row", r.ID, "err", uerr)
	}
	if r.Attempts+1 >= maxAttempts {
		logger.Error("posting-dispatcher: row hard-failed (on-call should investigate via /v1/finance/posting-outbox?status=stuck)",
			"row", r.ID, "tenant", r.TenantID,
			"source_ref", "see payload", "attempts", r.Attempts+1)
	}
}

// nullableUUID returns nil when the uuid is the zero value — keeps
// posted_je_id NULL rather than stamping the all-zeros uuid when
// the source_ref isn't UUID-shaped (e.g. application_fee_payments'
// "app-fee-<id>:..." composite refs).
func nullableUUID(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}
