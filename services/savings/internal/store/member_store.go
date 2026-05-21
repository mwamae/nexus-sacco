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
