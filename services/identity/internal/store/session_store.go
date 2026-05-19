// Refresh-token / session store.
//
// Tokens are stored hashed; the raw token is returned to the client
// once and never persisted. On refresh we look up by hash, mark the
// old token revoked, and issue a new one with parent_id linking the
// chain. If a revoked token is ever presented, we revoke the whole
// chain (signal of theft).

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type SessionStore struct {
	pool *pgxpool.Pool
}

func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool}
}

type CreateRefreshInput struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	TokenHash []byte
	ParentID  *uuid.UUID
	UserAgent string
	IP        string
	ExpiresAt time.Time
}

func (s *SessionStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateRefreshInput) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO refresh_tokens (tenant_id, user_id, token_hash, parent_id, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,'')::inet, $7)
		RETURNING id
	`, in.TenantID, in.UserID, in.TokenHash, in.ParentID, in.UserAgent, in.IP, in.ExpiresAt).Scan(&id)
	return id, err
}

func (s *SessionStore) ByHashTx(ctx context.Context, tx pgx.Tx, hash []byte) (*domain.RefreshToken, error) {
	var rt domain.RefreshToken
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, token_hash, parent_id, COALESCE(user_agent,''),
		       COALESCE(host(ip),''), expires_at, revoked_at, created_at
		FROM refresh_tokens WHERE token_hash = $1
	`, hash).Scan(
		&rt.ID, &rt.TenantID, &rt.UserID, &rt.TokenHash, &rt.ParentID,
		&rt.UserAgent, &rt.IP, &rt.ExpiresAt, &rt.RevokedAt, &rt.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

func (s *SessionStore) RevokeTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE refresh_tokens
		SET revoked_at = now(), revoked_reason = $2
		WHERE id = $1 AND revoked_at IS NULL
	`, id, reason)
	return err
}

// RevokeChainTx revokes all tokens descended from a root parent. Used
// when a revoked token is replayed (signal that the chain is compromised).
func (s *SessionStore) RevokeChainTx(ctx context.Context, tx pgx.Tx, rootID uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		WITH RECURSIVE chain(id) AS (
		  SELECT id FROM refresh_tokens WHERE id = $1
		  UNION ALL
		  SELECT rt.id FROM refresh_tokens rt
		  JOIN chain c ON rt.parent_id = c.id
		)
		UPDATE refresh_tokens
		SET revoked_at = COALESCE(revoked_at, now()),
		    revoked_reason = COALESCE(revoked_reason, $2)
		WHERE id IN (SELECT id FROM chain)
	`, rootID, reason)
	return err
}

// RevokeAllForUserTx revokes every active session for a user — used by
// logout-all and on password change.
func (s *SessionStore) RevokeAllForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE refresh_tokens
		SET revoked_at = now(), revoked_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID, reason)
	return err
}
