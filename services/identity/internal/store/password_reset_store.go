// Password reset store. Tokens are 32 random bytes returned to the
// caller once and stored as SHA-256 hashes server-side. Single-use:
// the row's used_at is set on success; replay → 401.

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

type PasswordResetStore struct {
	pool *pgxpool.Pool
}

func NewPasswordResetStore(pool *pgxpool.Pool) *PasswordResetStore {
	return &PasswordResetStore{pool: pool}
}

type PasswordReset struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	TokenHash []byte
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

type CreatePasswordResetInput struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	TokenHash []byte
	ExpiresAt time.Time
}

func (s *PasswordResetStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreatePasswordResetInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO password_resets (tenant_id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, in.TenantID, in.UserID, in.TokenHash, in.ExpiresAt)
	return err
}

func (s *PasswordResetStore) ByTokenHashTx(ctx context.Context, tx pgx.Tx, hash []byte) (*PasswordReset, error) {
	var p PasswordReset
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, token_hash, expires_at, used_at, created_at
		FROM password_resets WHERE token_hash = $1
	`, hash).Scan(&p.ID, &p.TenantID, &p.UserID, &p.TokenHash, &p.ExpiresAt, &p.UsedAt, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *PasswordResetStore) MarkUsedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	return err
}

// InvalidateOutstandingTx revokes every still-valid reset row for a user
// after they successfully reset their password. Stops a stale link from
// being usable a second time.
func (s *PasswordResetStore) InvalidateOutstandingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE password_resets SET used_at = now()
		WHERE user_id = $1 AND used_at IS NULL
	`, userID)
	return err
}

// NewResetToken returns 32 random URL-safe bytes plus their SHA-256 hash.
func NewResetToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

// HashResetToken hashes a raw token submitted by the client for lookup.
func HashResetToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
