// Workflow callback dispatcher.
//
// Drains wf_instances rows that the engine flipped to
// callback_status='pending' (see store.InstanceStore.MarkCallbackPendingTx).
// For each row:
//
//   1. Claim the row inside a tx via SELECT … FOR UPDATE SKIP LOCKED so
//      two dispatcher instances can run side-by-side without
//      double-posting. Set callback_status='in_flight' for ops-side
//      visibility while the POST is outstanding.
//   2. POST the instance JSON to wf_instances.callback_url. The host
//      module's endpoint (e.g. savings' /internal/v1/workflow-terminal-action)
//      runs its executor inside its own tx and returns 2xx on success.
//   3. On 2xx: stamp callback_status='delivered' + callback_delivered_at,
//      clear callback_last_error. Write an ActCallbackFired audit row.
//   4. On non-2xx or transport error: bump callback_attempts, set
//      callback_status back to 'pending' (or 'failed:…' at the cap),
//      compute callback_next_attempt_at via exponential backoff.
//
// Exponential backoff between attempts: NEXT_ATTEMPT = now() +
// 2^attempts * 5s, capped at 1h. Hard-fail at 12 attempts — the row
// drops out of the index because the partial index predicate stops
// matching ('failed:…' isn't 'pending').
//
// This binary replaces the fire-and-forget goroutine that used to
// live in services/workflow/internal/handler/instances.go — that
// goroutine lost the POST when savings was momentarily unavailable
// and required manual replay. The dispatcher gives us the same
// retry-and-DLQ semantics the savings posting-dispatcher uses.
//
// Graceful shutdown on SIGINT/SIGTERM: finish the in-flight row,
// then exit.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	batchSize       = 50
	httpTimeout     = 10 * time.Second
)

// candidate is the per-row snapshot the dispatcher picks up. We pull
// the full row JSON here rather than re-querying inside the per-row
// tx because the POST body wants the whole instance shape and we
// already have it after the claim query.
type candidate struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	CallbackURL  string
	Attempts     int
	InstanceJSON json.RawMessage
	Status       string // approved / rejected / cancelled — for the X-Nexus-Workflow-Event header
}

func main() {
	once := flag.Bool("once", false, "drain one batch and exit (useful for tests + cron-style deployments)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("callback-dispatcher: DATABASE_URL is required")
		os.Exit(1)
	}
	internalToken := os.Getenv("WORKFLOW_INTERNAL_TOKEN")
	if internalToken == "" {
		// Not fatal — the host module may run without internal-token
		// gating in dev. Loud warning so prod misconfig is visible.
		logger.Warn("callback-dispatcher: WORKFLOW_INTERNAL_TOKEN is empty; callbacks will POST without auth header. Set this in prod.")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	httpClient := &http.Client{Timeout: httpTimeout}

	// Heartbeat — every 30s upsert worker_heartbeats. Goroutine; the
	// signal-aware ctx kills it on shutdown. Skipped in -once mode
	// because that's a single-shot run, not a long-lived worker the
	// dashboard would expect to see a fresh heartbeat from.
	if !*once {
		go healthx.RunHeartbeatLoop(
			ctx, pool, "callback-dispatcher", workerVersion(),
			30*time.Second, nil, logger,
		)
	}

	logger.Info("callback-dispatcher: starting",
		"poll_interval", pollInterval, "max_attempts", maxAttempts,
		"batch_size", batchSize, "once", *once)

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		drained, err := drain(ctx, pool, httpClient, internalToken, logger)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("drain loop", "err", err)
		}
		if *once {
			logger.Info("callback-dispatcher: --once supplied, exiting", "drained", drained)
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("callback-dispatcher: shutting down")
			return
		case <-tick.C:
		}
	}
}

// drain pulls one batch of pending rows. Each row goes through its
// own tx so a slow/failing POST doesn't stall the batch.
func drain(ctx context.Context, pool *pgxpool.Pool, client *http.Client, token string, logger *slog.Logger) (int, error) {
	rows, err := pickPending(ctx, pool)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, r := range rows {
		if ctx.Err() != nil {
			return len(rows), nil
		}
		processRow(ctx, pool, client, token, logger, r)
	}
	return len(rows), nil
}

// pickPending — read-only candidate set. The per-row FOR UPDATE
// SKIP LOCKED happens inside processRow's tx; this scan just narrows
// the candidates so the dispatcher doesn't walk the whole table on
// every tick.
//
// Predicate matches the partial index wf_instances_callback_ready_idx
// (see migration 0011) so this is an index scan.
func pickPending(ctx context.Context, pool *pgxpool.Pool) ([]candidate, error) {
	const q = `
		SELECT id, tenant_id, callback_url, callback_attempts, status
		  FROM wf_instances
		 WHERE callback_url IS NOT NULL
		   AND callback_status = 'pending'
		   AND callback_attempts < $1
		   AND (callback_next_attempt_at IS NULL OR callback_next_attempt_at <= now())
		 ORDER BY callback_next_attempt_at NULLS FIRST, id
		 LIMIT $2
	`
	pgRows, err := pool.Query(ctx, q, maxAttempts, batchSize)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer pgRows.Close()
	var out []candidate
	for pgRows.Next() {
		var c candidate
		if err := pgRows.Scan(&c.ID, &c.TenantID, &c.CallbackURL, &c.Attempts, &c.Status); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, pgRows.Err()
}

// processRow runs one row end-to-end. Three database transactions:
//
//   tx1: claim the row (set callback_status='in_flight'), commit
//        immediately so the row's claimed state is visible to other
//        dispatcher replicas + to ops watching the table.
//   POST: outside any tx — the HTTP round trip can take seconds and
//         we don't want to hold a row lock that long.
//   tx2: write the outcome (delivered or pending-with-bumped-attempts
//        or terminal failure), audit row, commit.
func processRow(ctx context.Context, pool *pgxpool.Pool, client *http.Client, token string, logger *slog.Logger, c candidate) {
	// ── tx1 — claim ──────────────────────────────────────────────
	claimed, instanceJSON, err := claim(ctx, pool, c)
	if err != nil {
		logger.Error("callback claim", "id", c.ID, "err", err)
		return
	}
	if !claimed {
		// Another dispatcher beat us to it — fine, move on.
		return
	}
	c.InstanceJSON = instanceJSON

	// ── HTTP POST ────────────────────────────────────────────────
	deliveryErr := postOnce(ctx, client, token, c)

	// ── tx2 — record outcome ─────────────────────────────────────
	if deliveryErr == nil {
		if err := recordDelivered(ctx, pool, c); err != nil {
			// Outcome write failed but the POST succeeded; log loud
			// and continue — the row will be re-tried next tick because
			// callback_status didn't flip out of 'in_flight'. The host
			// module's executor should be idempotent on the
			// (process_kind, instance_id) tuple.
			logger.Error("callback delivered but outcome-write failed",
				"id", c.ID, "err", err)
		}
		return
	}

	// Failure path.
	nextAttempts := c.Attempts + 1
	if nextAttempts >= maxAttempts {
		if err := recordHardFailure(ctx, pool, c, deliveryErr.Error()); err != nil {
			logger.Error("callback hard-fail outcome-write", "id", c.ID, "err", err)
		}
		logger.Error("callback hard-failed (max attempts)",
			"id", c.ID, "tenant", c.TenantID, "attempts", nextAttempts, "err", deliveryErr)
		return
	}
	backoff := computeBackoff(nextAttempts)
	if err := recordRetry(ctx, pool, c, nextAttempts, deliveryErr.Error(), backoff); err != nil {
		logger.Error("callback retry outcome-write", "id", c.ID, "err", err)
		return
	}
	logger.Warn("callback retry scheduled",
		"id", c.ID, "tenant", c.TenantID, "attempts", nextAttempts,
		"next_in", backoff.Round(time.Second), "err", deliveryErr)
}

// claim flips callback_status to 'in_flight' inside a tx that also
// re-fetches the row JSON. Uses SELECT … FOR UPDATE SKIP LOCKED so
// a second dispatcher replica racing us gets nothing back.
func claim(ctx context.Context, pool *pgxpool.Pool, c candidate) (bool, json.RawMessage, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("begin claim tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// RLS scope — the row is tenant-scoped; set the GUC for the row
	// lock check.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`,
		c.TenantID.String(),
	); err != nil {
		return false, nil, fmt.Errorf("set tenant: %w", err)
	}

	// Lock the row + re-read the full JSON for the POST body. If the
	// row was already claimed by another dispatcher (callback_status
	// not 'pending' anymore) the WHERE clause filters it out.
	var instanceJSON json.RawMessage
	err = tx.QueryRow(ctx, `
		UPDATE wf_instances
		   SET callback_status = 'in_flight'
		 WHERE id = $1
		   AND callback_status = 'pending'
		   AND id IN (
		     SELECT id FROM wf_instances
		      WHERE id = $1
		      FOR UPDATE SKIP LOCKED
		   )
	   RETURNING to_jsonb(wf_instances)
	`, c.ID).Scan(&instanceJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, tx.Commit(ctx)
	}
	if err != nil {
		return false, nil, fmt.Errorf("claim row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, fmt.Errorf("commit claim: %w", err)
	}
	return true, instanceJSON, nil
}

// postOnce fires the HTTP call. Returns nil on 2xx, an error
// otherwise (transport failure, non-2xx body, malformed response).
// The error message is what gets stored in callback_last_error.
func postOnce(ctx context.Context, client *http.Client, token string, c candidate) error {
	body, err := json.Marshal(map[string]any{
		"tenant_id":    c.TenantID,
		"instance":     json.RawMessage(c.InstanceJSON),
		"event":        c.Status,
		"delivered_at": time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexus-workflow-callback/1")
	req.Header.Set("X-Nexus-Workflow-Event", c.Status)
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close()
	// Drain a small response body for connection reuse; cap at 8KiB
	// so a misbehaving host doesn't pin memory.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet := string(respBody)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	return fmt.Errorf("status:%d body:%s", resp.StatusCode, snippet)
}

func recordDelivered(ctx context.Context, pool *pgxpool.Pool, c candidate) error {
	return withTenantTx(ctx, pool, c.TenantID, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE wf_instances
			   SET callback_status        = 'delivered',
			       callback_delivered_at  = $2,
			       callback_attempts      = callback_attempts + 1,
			       callback_last_error    = NULL,
			       callback_next_attempt_at = NULL
			 WHERE id = $1
		`, c.ID, now); err != nil {
			return err
		}
		return writeCallbackAudit(ctx, tx, c.TenantID, c.ID, "delivered")
	})
}

func recordRetry(ctx context.Context, pool *pgxpool.Pool, c candidate, nextAttempts int, errMsg string, backoff time.Duration) error {
	return withTenantTx(ctx, pool, c.TenantID, func(tx pgx.Tx) error {
		nextAt := time.Now().Add(backoff)
		_, err := tx.Exec(ctx, `
			UPDATE wf_instances
			   SET callback_status        = 'pending',
			       callback_attempts      = $2,
			       callback_last_error    = $3,
			       callback_next_attempt_at = $4
			 WHERE id = $1
		`, c.ID, nextAttempts, errMsg, nextAt)
		return err
	})
}

func recordHardFailure(ctx context.Context, pool *pgxpool.Pool, c candidate, errMsg string) error {
	return withTenantTx(ctx, pool, c.TenantID, func(tx pgx.Tx) error {
		failureStatus := "failed:" + truncForStatus(errMsg)
		if _, err := tx.Exec(ctx, `
			UPDATE wf_instances
			   SET callback_status        = $2,
			       callback_attempts      = callback_attempts + 1,
			       callback_last_error    = $3,
			       callback_next_attempt_at = NULL
			 WHERE id = $1
		`, c.ID, failureStatus, errMsg); err != nil {
			return err
		}
		return writeCallbackAudit(ctx, tx, c.TenantID, c.ID, failureStatus)
	})
}

// writeCallbackAudit mirrors the action workflow's existing
// ActCallbackFired path so the Inbox's "history" tab shows the
// dispatcher's outcomes.
func writeCallbackAudit(ctx context.Context, tx pgx.Tx, tenantID, instanceID uuid.UUID, status string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO wf_actions (tenant_id, instance_id, action, comments)
		VALUES ($1, $2, 'callback_fired', $3)
	`, tenantID, instanceID, status)
	return err
}

func withTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// computeBackoff returns the wait before the n-th retry. Same shape
// as the posting-dispatcher: 2^attempts * 5s, capped at 1h.
func computeBackoff(attempts int) time.Duration {
	exp := math.Pow(2, float64(attempts))
	d := time.Duration(exp) * backoffBaseUnit
	if d > backoffMaxUnit {
		return backoffMaxUnit
	}
	return d
}

// truncForStatus shortens a long error message so callback_status
// doesn't grow unbounded (the full message lives in
// callback_last_error). 80-char cap keeps the column readable in
// the Inbox's "callback: failed:…" surface.
func truncForStatus(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:77] + "…"
}

// Silence unused-import warning when a build flag prunes a branch
// above (kept for parity with posting-dispatcher's style).
var _ = sql.ErrNoRows
