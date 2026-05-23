// Read-only access to the member service's `members` table. We only need
// id → (full_name, member_no, status) for validation and presentation.
// The members table is RLS-scoped by tenant_id so reads inside a
// tenant-bound transaction are safe.

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MemberStore struct {
	pool *pgxpool.Pool
}

func NewMemberStore(pool *pgxpool.Pool) *MemberStore {
	return &MemberStore{pool: pool}
}

type MemberLite struct {
	ID       uuid.UUID
	MemberNo string
	FullName string
	Status   string // pending | active | dormant | suspended | blacklisted | exited | deceased | rejected
	Phone    string // empty if member opted out / never set
	Email    string
}

func (s *MemberStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*MemberLite, error) {
	var m MemberLite
	var phone, email *string
	err := tx.QueryRow(ctx, `
		SELECT id, member_no, full_name, status::text, phone, email
		FROM members WHERE id = $1
	`, id).Scan(&m.ID, &m.MemberNo, &m.FullName, &m.Status, &phone, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if phone != nil {
		m.Phone = *phone
	}
	if email != nil {
		m.Email = *email
	}
	return &m, nil
}

// TouchActivityTx bumps members.last_activity_at so the dormancy detector
// sees the share movement as recent activity. Best-effort: missing column
// (older schema) is tolerated by callers.
func (s *MemberStore) TouchActivityTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE members SET last_activity_at = now() WHERE id = $1
	`, id)
	return err
}

// GetByCounterpartyTx is the Phase D sub-PR 2b lookup. Post-2a all
// savings tables key off counterparty_id, so handlers naturally have
// a counterparty.id in hand (e.g., loan.CounterpartyID, acct.CounterpartyID)
// and want the member who owns that counterparty bridge. Returns
// ErrNotFound when the counterparty belongs to an institutional row
// (no matching members entry) — that case predates institutional
// savings support and surfaces as 404 to the caller, preserving the
// pre-Phase D "not a member" failure mode.
func (s *MemberStore) GetByCounterpartyTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (*MemberLite, error) {
	var m MemberLite
	var phone, email *string
	err := tx.QueryRow(ctx, `
		SELECT id, member_no, full_name, status::text, phone, email
		FROM members WHERE counterparty_id = $1
	`, cpID).Scan(&m.ID, &m.MemberNo, &m.FullName, &m.Status, &phone, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if phone != nil {
		m.Phone = *phone
	}
	if email != nil {
		m.Email = *email
	}
	return &m, nil
}

// TouchActivityByCounterpartyTx mirrors TouchActivityTx but locates
// the member through the counterparty bridge. Same dormancy-runner
// invariant; no-op if no member matches the counterparty.
func (s *MemberStore) TouchActivityByCounterpartyTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE members SET last_activity_at = now() WHERE counterparty_id = $1
	`, cpID)
	return err
}
