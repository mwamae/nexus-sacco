// MFA challenge store. Tenant-scoped via RLS; mfa_token + code are
// hashed (SHA-256) before storage and never returned by the API.

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

type MFAStore struct {
	pool *pgxpool.Pool
}

func NewMFAStore(pool *pgxpool.Pool) *MFAStore { return &MFAStore{pool: pool} }

type MFAChallenge struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	UserID        uuid.UUID
	Purpose       string
	MFATokenHash  []byte
	CodeHash      []byte
	ExpiresAt     time.Time
	UsedAt        *time.Time
	Attempts      int
	IP            string
	UserAgent     string
	CreatedAt     time.Time
}

type CreateChallengeInput struct {
	TenantID     uuid.UUID
	UserID       uuid.UUID
	Purpose      string // "login" | "enable_mfa"
	MFATokenHash []byte
	CodeHash     []byte
	ExpiresAt    time.Time
	IP           string
	UserAgent    string
}

func (s *MFAStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateChallengeInput) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO mfa_challenges
		  (tenant_id, user_id, purpose, mfa_token_hash, code_hash, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,'')::inet, NULLIF($8,''))
		RETURNING id
	`, in.TenantID, in.UserID, in.Purpose, in.MFATokenHash, in.CodeHash, in.ExpiresAt, in.IP, in.UserAgent).
		Scan(&id)
	return id, err
}

func (s *MFAStore) ByMFATokenHashTx(ctx context.Context, tx pgx.Tx, hash []byte) (*MFAChallenge, error) {
	var c MFAChallenge
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, purpose, mfa_token_hash, code_hash,
		       expires_at, used_at, attempts, COALESCE(host(ip),''), COALESCE(user_agent,''), created_at
		FROM mfa_challenges WHERE mfa_token_hash = $1
	`, hash).Scan(
		&c.ID, &c.TenantID, &c.UserID, &c.Purpose, &c.MFATokenHash, &c.CodeHash,
		&c.ExpiresAt, &c.UsedAt, &c.Attempts, &c.IP, &c.UserAgent, &c.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *MFAStore) MarkUsedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE mfa_challenges SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	return err
}

// IncrementAttemptsTx bumps attempts and returns the new count.
func (s *MFAStore) IncrementAttemptsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		UPDATE mfa_challenges SET attempts = attempts + 1 WHERE id = $1
		RETURNING attempts
	`, id).Scan(&n)
	return n, err
}

// ───────── Token + code generation ─────────

// NewMFAToken returns a 32-byte URL-safe opaque token + its SHA-256 hash.
// Returned to the client after a successful first-factor login; submitted
// back with the OTP code to /v1/auth/mfa/verify.
func NewMFAToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

// HashMFAToken hashes a raw mfa_token for lookup.
func HashMFAToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// NewOTPCode returns a uniformly-random 6-digit string and its hash.
// Uses crypto/rand — rejects modulo bias by re-rolling outside [0, 1_000_000).
func NewOTPCode() (code string, hash []byte, err error) {
	const max = 1_000_000
	buf := make([]byte, 4)
	for {
		if _, err := rand.Read(buf); err != nil {
			return "", nil, err
		}
		n := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
		// Filter to remove modulo bias.
		if n >= (uint32(1<<32-1)/max)*max {
			continue
		}
		code = fmt.Sprintf("%06d", n%max)
		break
	}
	h := sha256.Sum256([]byte(code))
	return code, h[:], nil
}

// HashOTPCode hashes a code submitted by the client.
func HashOTPCode(code string) []byte {
	h := sha256.Sum256([]byte(code))
	return h[:]
}
