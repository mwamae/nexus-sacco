// Persistence for mpesa_distribution_runs + the inbound-event
// lifecycle the distributor mutates.
//
// Phase 3 (defer-apply):
//   - CreateRunTx persists the engine's Plan into a fresh
//     mpesa_distribution_runs row with status='pending'.
//   - MarkRunPostedTx flips it to 'posted' once the GL outbox row
//     for the cash leg is written. Phase 3.5 changes 'posted' to
//     mean "splits applied to deposit/loan/fee tables" — the column
//     stays the same, the semantics deepen.
//   - The inbound-event mutations (status, attempts, error_text,
//     posted_at, distribution_run_id, locked_*) all live here too
//     so the distributor has one ergonomic call surface.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type DistributionRunStore struct {
	pool *pgxpool.Pool
}

func NewDistributionRunStore(pool *pgxpool.Pool) *DistributionRunStore {
	return &DistributionRunStore{pool: pool}
}

// CreateRunInput bundles what the distributor knows after engine.Run
// returns. splits is the marshalled JSON (the engine package owns the
// shape; this store treats it as opaque).
type CreateRunInput struct {
	TenantID            uuid.UUID
	InboundEventID      uuid.UUID
	ResolvedMemberID    *uuid.UUID
	ResolvedVia         string // mpesa_resolved_via enum value or empty
	Amount              decimal.Decimal
	Splits              json.RawMessage
	CashAccountCode     string
	ClearingAccountCode string
}

// CreateRunTx writes one mpesa_distribution_runs row. Returns the
// new id. Status defaults to 'pending'; MarkRunPostedTx flips it
// once the cash leg has been queued in posting_outbox.
func (s *DistributionRunStore) CreateRunTx(ctx context.Context, tx pgx.Tx, in CreateRunInput) (uuid.UUID, error) {
	if in.TenantID == uuid.Nil || in.InboundEventID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("distribution_run: tenant_id + inbound_event_id required")
	}
	if len(in.Splits) == 0 {
		// Empty splits is a legitimate outcome (unallocated event); the
		// applier still has a row to lock the inbound event against.
		in.Splits = json.RawMessage("[]")
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO mpesa_distribution_runs
			(tenant_id, inbound_event_id, splits, status,
			 cash_account_code, clearing_account_code,
			 resolved_member_id, resolved_via, amount)
		VALUES ($1, $2, $3, 'pending', $4, $5, $6,
		        NULLIF($7,'')::mpesa_resolved_via, $8)
		RETURNING id
	`, in.TenantID, in.InboundEventID, in.Splits,
		in.CashAccountCode, in.ClearingAccountCode,
		in.ResolvedMemberID, in.ResolvedVia, in.Amount.StringFixed(2),
	).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// MarkRunPostedTx flips a run from 'pending' → 'posted'. Called
// after the cash-leg posting_outbox row has been written.
func (s *DistributionRunStore) MarkRunPostedTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, journalRef *uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_distribution_runs
		   SET status = 'posted',
		       posting_journal_id = $2,
		       posted_at = now()
		 WHERE id = $1
	`, runID, journalRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRunFailedTx flips a run from 'pending' → 'failed' and stamps
// the error reason. Called by the distributor on a hard-fail.
func (s *DistributionRunStore) MarkRunFailedTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, errText string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_distribution_runs
		   SET status = 'failed', error = $2
		 WHERE id = $1
	`, runID, errText)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────── Inbound-event distributor-side mutations ───────────

// LeaseNextTx picks the oldest 'received' event for the tenant
// using SELECT … FOR UPDATE SKIP LOCKED. Returns ErrNotFound when
// the queue is empty. The row is locked for the duration of the
// caller's tx. The caller MUST commit (or roll back) within a
// reasonable time so other workers can pick up new arrivals.
//
// `attempts` filter: rows that have already hard-failed
// (attempts >= 6) are excluded so a stuck row doesn't permanently
// poison the lease loop.
func (s *DistributionRunStore) LeaseNextTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, workerID uuid.UUID) (uuid.UUID, error) {
	var eventID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM mpesa_inbound_events
		 WHERE tenant_id = $1
		   AND status = 'received'
		   AND attempts < 6
		 ORDER BY received_at ASC
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED
	`, tenantID).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE mpesa_inbound_events
		   SET locked_at = now(), locked_by = $2
		 WHERE id = $1
	`, eventID, workerID); err != nil {
		return uuid.Nil, err
	}
	return eventID, nil
}

// MarkDistributedTx flips an inbound event to status='distributed'
// after a successful run + cash-leg post. Caller is responsible for
// also persisting the distribution_run_id on the event row.
func (s *DistributionRunStore) MarkDistributedTx(ctx context.Context, tx pgx.Tx, eventID, runID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_inbound_events
		   SET status               = 'distributed',
		       distribution_run_id  = $2,
		       posted_at            = now(),
		       error_text           = NULL,
		       locked_at            = NULL,
		       locked_by            = NULL
		 WHERE id = $1
	`, eventID, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordAttemptFailureTx increments attempts + stamps the error
// reason. Status stays 'received' so the next lease can pick it up,
// unless attempts reached the hard-fail threshold (6) — in which
// case the caller flips status='failed' separately.
func (s *DistributionRunStore) RecordAttemptFailureTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, errText string) (int, error) {
	var attempts int
	err := tx.QueryRow(ctx, `
		UPDATE mpesa_inbound_events
		   SET attempts   = attempts + 1,
		       error_text = $2,
		       locked_at  = NULL,
		       locked_by  = NULL
		 WHERE id = $1
		 RETURNING attempts
	`, eventID, errText).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return attempts, err
}

// MarkEventFailedTx is the hard-fail flip. Called when attempts has
// reached 6. The status moves to 'failed'; the alert workflow task
// is created by the distributor in the same tx.
func (s *DistributionRunStore) MarkEventFailedTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_inbound_events
		   SET status     = 'failed',
		       locked_at  = NULL,
		       locked_by  = NULL
		 WHERE id = $1
	`, eventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
