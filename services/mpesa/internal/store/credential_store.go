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
//
// Goes through the SECURITY DEFINER function mpesa_credentials_write
// (migration 0011) because ON CONFLICT DO UPDATE needs table-level
// SELECT — which migration 0002 deliberately revoked from nexus_app
// to prevent bulk reads of ciphertext. The function runs as the
// table owner, does the upsert internally, and returns only the
// metadata columns the caller audits. Row tenant_id is asserted
// against the caller's session GUC inside the function for defense
// in depth.
func (s *CredentialStore) PutTx(ctx context.Context, tx pgx.Tx, in PutCredentialInput) (*domain.CredentialMetadata, error) {
	var m domain.CredentialMetadata
	err := tx.QueryRow(ctx, `
		SELECT out_id, out_paybill_id, out_kind, out_key_id, out_updated_at
		  FROM mpesa_credentials_write($1, $2, $3, $4, $5, $6)
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

