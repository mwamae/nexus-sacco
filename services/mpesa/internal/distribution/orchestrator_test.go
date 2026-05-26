// Orchestrator integration tests against the live DB.
//
// Covers the spec's three replay/safety properties:
//   - End-to-end happy path: a 'received' event is leased, the plan
//     is built + persisted, the cash leg is queued in posting_outbox,
//     and the event flips to 'distributed' with distribution_run_id
//     set.
//   - Replay safety: re-running on a 'distributed' row is a no-op
//     (the engine refuses the second pass because status != received).
//   - Hard-fail path: after HardFailAttempts the event flips to
//     'failed' and a workflow task is created.

package distribution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

func TestOrchestrator_HappyPath_LeasesAndDistributes(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}

	// Seed an inbound event in 'received' status. Resolver verdict
	// is unallocated so the engine produces zero splits and routes
	// the full amount through the cash-leg posting only — exercises
	// the orchestrator without needing live members.
	eventID := seedReceivedEvent(t, dbPool, tenantID, "4000.00")

	o := buildOrch(pool)

	workerID := uuid.New()
	var runID uuid.UUID
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		leased, err := o.Runs.LeaseNextTx(context.Background(), tx, tenantID, workerID)
		if err != nil {
			return err
		}
		if leased != eventID {
			return fmt.Errorf("lease picked the wrong event: want %s got %s", eventID, leased)
		}
		res, err := o.Process(context.Background(), tx, tenantID, leased)
		if err != nil {
			return err
		}
		runID = res.RunID
		return nil
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	// Assertions: event flipped to distributed + run row exists +
	// posting_outbox row queued for the cash leg.
	var status string
	var distRunID *uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT status::text, distribution_run_id FROM mpesa_inbound_events WHERE id = $1`, eventID,
	).Scan(&status, &distRunID); err != nil {
		t.Fatalf("re-read event: %v", err)
	}
	if status != "distributed" {
		t.Errorf("status: want distributed, got %q", status)
	}
	if distRunID == nil || *distRunID != runID {
		t.Errorf("distribution_run_id: want %s, got %v", runID, distRunID)
	}

	var outboxCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM posting_outbox
		  WHERE tenant_id = $1
		    AND payload->>'source_module' = 'mpesa.distribution.cash_leg'
		    AND payload->>'source_ref'    = $2`,
		tenantID, eventID.String(),
	).Scan(&outboxCount)
	if outboxCount != 1 {
		t.Errorf("posting_outbox: want 1 cash-leg row, got %d", outboxCount)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM posting_outbox WHERE payload->>'source_ref' = $1`, eventID.String())
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_distribution_runs WHERE inbound_event_id = $1`, eventID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_inbound_events WHERE id = $1`, eventID)
	})
}

func TestOrchestrator_ReplayOnDistributed_IsNoop(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}

	eventID := seedReceivedEvent(t, dbPool, tenantID, "1500.00")
	o := buildOrch(pool)
	workerID := uuid.New()

	// First pass: success.
	_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		if _, err := o.Runs.LeaseNextTx(context.Background(), tx, tenantID, workerID); err != nil {
			return err
		}
		_, err := o.Process(context.Background(), tx, tenantID, eventID)
		return err
	})

	// Second pass: simulate a replay by manually calling Process
	// on the now-distributed event id. Should refuse (return an
	// error containing "skipping replay") and NOT write any new
	// rows.
	var newRunCount int
	_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		_, err := o.Process(context.Background(), tx, tenantID, eventID)
		if err == nil {
			t.Error("replay: expected an error, got nil")
		}
		return err // rolls the tx back (no half-write)
	})

	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mpesa_distribution_runs WHERE inbound_event_id = $1`, eventID,
	).Scan(&newRunCount)
	if newRunCount != 1 {
		t.Errorf("replay must not create a 2nd run, got %d", newRunCount)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM posting_outbox WHERE payload->>'source_ref' = $1`, eventID.String())
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_distribution_runs WHERE inbound_event_id = $1`, eventID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_inbound_events WHERE id = $1`, eventID)
	})
}

func TestOrchestrator_HardFail_AfterSixAttempts(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}

	eventID := seedReceivedEvent(t, dbPool, tenantID, "100.00")
	o := buildOrch(pool)

	// Drive attempts to HardFailAttempts directly via RecordFailure.
	// Each call simulates "Process returned an error" — the test
	// doesn't actually need a failing Process call; we exercise
	// the increment + hard-fail logic in isolation.
	for i := 1; i <= HardFailAttempts; i++ {
		var attemptsBefore int
		_ = pool.QueryRow(context.Background(),
			`SELECT attempts FROM mpesa_inbound_events WHERE id = $1`, eventID).Scan(&attemptsBefore)
		err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			return o.RecordFailure(context.Background(), tx, tenantID, eventID,
				fmt.Errorf("simulated failure attempt %d", i))
		})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}

	var status string
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT status::text, attempts FROM mpesa_inbound_events WHERE id = $1`, eventID,
	).Scan(&status, &attempts); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if attempts != HardFailAttempts {
		t.Errorf("attempts: want %d, got %d", HardFailAttempts, attempts)
	}
	if status != "failed" {
		t.Errorf("status: want failed, got %q", status)
	}

	// Workflow task should have been created on the final attempt.
	var wfCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wf_instances
		  WHERE tenant_id = $1
		    AND subject_kind = 'mpesa_inbound_event'
		    AND subject_id   = $2
		    AND process_kind = 'mpesa_unallocated_reconciliation'`,
		tenantID, eventID,
	).Scan(&wfCount)
	if wfCount < 1 {
		t.Errorf("expected a hard-fail workflow task, got %d", wfCount)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM wf_instances WHERE subject_kind='mpesa_inbound_event' AND subject_id = $1`, eventID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_inbound_events WHERE id = $1`, eventID)
	})
}

// ─── helpers ───

func openTestPool(t *testing.T) (*pgxpool.Pool, uuid.UUID, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants LIMIT 1`).Scan(&tenantID); err != nil {
		pool.Close()
		t.Skipf("no tenant: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool, tenantID, func() {}
}

func seedReceivedEvent(t *testing.T, dbPool *db.Pool, tenantID uuid.UUID, amount string) uuid.UUID {
	t.Helper()
	var paybillID uuid.UUID
	uniq := fmt.Sprintf("dist%07d", time.Now().UnixNano()%10000000)
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_paybills
				(tenant_id, label, shortcode, purpose, scope, environment, webhook_token)
			VALUES ($1, 'p3-test', $2, 'collection', '{member_deposits}', 'sandbox', encode(gen_random_bytes(24),'hex'))
			RETURNING id
		`, tenantID, uniq).Scan(&paybillID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed paybill: %v", err)
	}
	var eventID uuid.UUID
	err = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_inbound_events
				(tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
				 amount, msisdn, bill_ref, raw_payload, status, resolved_via)
			VALUES ($1, $2, $3, $4, now(), $5, '254712345678', '??unmatched',
			        '{}'::jsonb, 'received', 'unallocated')
			RETURNING id
		`, tenantID, paybillID, uniq, "TX-"+uniq, amount).Scan(&eventID)
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}
	t.Cleanup(func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybills WHERE id = $1`, paybillID)
			return nil
		})
	})
	return eventID
}

func buildOrch(pool *pgxpool.Pool) *Orchestrator {
	return &Orchestrator{
		Events:              store.NewInboundEventStore(pool),
		Runs:                store.NewDistributionRunStore(pool),
		Balances:            store.NewDistributionBalances(pool),
		Audit:               store.NewAuditStore(pool),
		Workflow:            workflowclient.New(),
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		CashAccountCode:     "1030",
		ClearingAccountCode: "1099",
	}
}

// Silence dec import warnings when this file is the only consumer
// of decimal in the package — engine_test.go imports it too.
var _ = decimal.Zero
var _ = json.Marshal
