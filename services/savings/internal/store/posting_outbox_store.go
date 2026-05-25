// Read + replay surface for posting_outbox.
//
// Writes go through posting.Client.PostTx directly (it owns the
// JSON wire shape). This store is the read side + ops actions
// (replay) the /v1/finance/posting-outbox endpoint exposes.

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostingOutboxStore struct {
	pool *pgxpool.Pool
}

func NewPostingOutboxStore(pool *pgxpool.Pool) *PostingOutboxStore {
	return &PostingOutboxStore{pool: pool}
}

// PostingOutboxRow is the API-facing shape.
type PostingOutboxRow struct {
	ID           uuid.UUID       `json:"id"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	Payload      json.RawMessage `json:"payload"`
	Attempts     int             `json:"attempts"`
	LastError    *string         `json:"last_error,omitempty"`
	EnqueuedAt   time.Time       `json:"enqueued_at"`
	DispatchedAt *time.Time      `json:"dispatched_at,omitempty"`
	PostedJEID   *uuid.UUID      `json:"posted_je_id,omitempty"`
}

// ListStuckTx returns outbox rows that the dispatcher has tried
// >= 3 times without landing. Operators monitor this list to spot
// rows where the underlying CoA / account / accounting service is
// genuinely broken (the row has tried + failed enough times that
// auto-retry isn't going to clear it without intervention).
func (s *PostingOutboxStore) ListStuckTx(ctx context.Context, tx pgx.Tx, limit int) ([]PostingOutboxRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, payload, attempts, last_error, enqueued_at, dispatched_at, posted_je_id
		  FROM posting_outbox
		 WHERE dispatched_at IS NULL
		   AND attempts >= 3
		 ORDER BY attempts DESC, enqueued_at
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PostingOutboxRow{}
	for rows.Next() {
		var r PostingOutboxRow
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.Payload, &r.Attempts, &r.LastError,
			&r.EnqueuedAt, &r.DispatchedAt, &r.PostedJEID,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplayTx resets a single row's retry counter so the dispatcher
// picks it up on the next tick. Refuses to replay an already-
// dispatched row (would invite double-post).
func (s *PostingOutboxStore) ReplayTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*PostingOutboxRow, error) {
	row := tx.QueryRow(ctx, `
		UPDATE posting_outbox
		   SET attempts = 0, last_error = NULL
		 WHERE id = $1 AND dispatched_at IS NULL
		RETURNING id, tenant_id, payload, attempts, last_error, enqueued_at, dispatched_at, posted_je_id
	`, id)
	var r PostingOutboxRow
	if err := row.Scan(
		&r.ID, &r.TenantID, &r.Payload, &r.Attempts, &r.LastError,
		&r.EnqueuedAt, &r.DispatchedAt, &r.PostedJEID,
	); err != nil {
		return nil, err
	}
	return &r, nil
}
