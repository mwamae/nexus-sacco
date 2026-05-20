// Invite store. Same single-use, hashed-at-rest pattern as password_resets:
// 32 random bytes returned to the inviter once, stored as SHA-256.

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InviteStore struct {
	pool *pgxpool.Pool
}

func NewInviteStore(pool *pgxpool.Pool) *InviteStore {
	return &InviteStore{pool: pool}
}

type Invite struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	InvitedBy  *uuid.UUID
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	CreatedAt  time.Time
}

type CreateInviteInput struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	TokenHash []byte
	InvitedBy *uuid.UUID
	ExpiresAt time.Time
}

func (s *InviteStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateInviteInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_invites (tenant_id, user_id, token_hash, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, in.TenantID, in.UserID, in.TokenHash, in.InvitedBy, in.ExpiresAt)
	return err
}

func (s *InviteStore) ByTokenHashTx(ctx context.Context, tx pgx.Tx, hash []byte) (*Invite, error) {
	var i Invite
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, token_hash, invited_by, expires_at, accepted_at, created_at
		FROM user_invites WHERE token_hash = $1
	`, hash).Scan(&i.ID, &i.TenantID, &i.UserID, &i.TokenHash, &i.InvitedBy, &i.ExpiresAt, &i.AcceptedAt, &i.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &i, err
}

func (s *InviteStore) MarkAcceptedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE user_invites SET accepted_at = now() WHERE id = $1 AND accepted_at IS NULL`, id)
	return err
}

// InvalidateOutstandingTx burns any still-open invite rows for the user.
// Called on accept (defense in depth) and on resend (so a fresh link
// supersedes the old one).
func (s *InviteStore) InvalidateOutstandingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE user_invites SET accepted_at = now()
		WHERE user_id = $1 AND accepted_at IS NULL
	`, userID)
	return err
}

// NewInviteToken returns 32 random URL-safe bytes plus their SHA-256 hash.
func NewInviteToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

// HashInviteToken hashes a raw token submitted by the client for lookup.
func HashInviteToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
