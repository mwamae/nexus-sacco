// Credentials store. Writes go straight to the table (the app role
// has INSERT/UPDATE/DELETE); reads go via the SECURITY DEFINER
// function mpesa_credentials_read so a bulk SELECT is impossible.

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/mpesa/internal/domain"
)

type CredentialStore struct {
	pool *pgxpool.Pool
}

func NewCredentialStore(pool *pgxpool.Pool) *CredentialStore { return &CredentialStore{pool: pool} }

type PutCredentialInput struct {
	TenantID   uuid.UUID
	PaybillID  uuid.UUID
	Kind       domain.CredentialKind
	KeyID      string
	Ciphertext []byte
	CreatedBy  *uuid.UUID
}

// PutTx inserts a credential or replaces the previous value for the
// same (paybill_id, kind). The ciphertext is opaque to this store —
// envelope encryption happens in the handler before the call.
func (s *CredentialStore) PutTx(ctx context.Context, tx pgx.Tx, in PutCredentialInput) (*domain.CredentialMetadata, error) {
	var m domain.CredentialMetadata
	err := tx.QueryRow(ctx, `
		INSERT INTO mpesa_paybill_credentials
		  (tenant_id, paybill_id, kind, key_id, ciphertext, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (paybill_id, kind) DO UPDATE
		  SET key_id     = EXCLUDED.key_id,
		      ciphertext = EXCLUDED.ciphertext,
		      updated_at = now(),
		      created_by = COALESCE(EXCLUDED.created_by, mpesa_paybill_credentials.created_by)
		RETURNING id, paybill_id, kind, key_id, updated_at
	`, in.TenantID, in.PaybillID, in.Kind, in.KeyID, in.Ciphertext, in.CreatedBy).Scan(
		&m.ID, &m.PaybillID, &m.Kind, &m.KeyID, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ReadTx pulls a single credential's key_id + ciphertext via the
// SECURITY DEFINER function. The function enforces tenant scope on
// its own (it reads app.tenant_id internally), so the caller can't
// accidentally bypass RLS by forgetting to wrap the call.
func (s *CredentialStore) ReadTx(ctx context.Context, tx pgx.Tx, paybillID uuid.UUID, kind domain.CredentialKind) (keyID string, ciphertext []byte, err error) {
	err = tx.QueryRow(ctx, `SELECT key_id, ciphertext FROM mpesa_credentials_read($1, $2)`, paybillID, kind).
		Scan(&keyID, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return keyID, ciphertext, nil
}

