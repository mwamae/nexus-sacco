// Paybill CRUD + credential read-write surface.
//
// Credential reads go through the mpesa_credentials_read SECURITY
// DEFINER function — the app role has no SELECT grant on the
// ciphertext table directly, so `SELECT * FROM mpesa_paybill_credentials`
// from a handler would fail with a permission error. That guarantees
// the only documented access pattern is via this store.

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/mpesa/internal/domain"
)

type PaybillStore struct {
	pool *pgxpool.Pool
}

func NewPaybillStore(pool *pgxpool.Pool) *PaybillStore { return &PaybillStore{pool: pool} }

type CreatePaybillInput struct {
	TenantID    uuid.UUID
	Label       string
	Shortcode   string
	Purpose     domain.PaybillPurpose
	Scope       []string
	Environment domain.Environment
	// Optional. When empty, the store generates a 48-hex-char token.
	// Pre-supplied tokens are useful for tests + for a rotate flow
	// that wants to know the new value before the row commits.
	WebhookToken string
	CreatedBy    *uuid.UUID
}

const paybillCols = `id, tenant_id, label, shortcode, purpose, scope, environment, status,
		distribution_policy_id, strict_validation, allow_msisdn_fallback, webhook_token,
		created_by, created_at, updated_at`

func scanPaybill(row pgx.Row, p *domain.Paybill) error {
	return row.Scan(
		&p.ID, &p.TenantID, &p.Label, &p.Shortcode, &p.Purpose, &p.Scope, &p.Environment, &p.Status,
		&p.DistributionPolicyID, &p.StrictValidation, &p.AllowMSISDNFallback, &p.WebhookToken,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
}

func (s *PaybillStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreatePaybillInput) (*domain.Paybill, error) {
	// Generate the webhook token in SQL when the caller didn't supply
	// one. gen_random_bytes lives in pgcrypto which the dev DB already
	// has (used by gen_random_uuid()).
	var p domain.Paybill
	err := scanPaybill(tx.QueryRow(ctx, `
		INSERT INTO mpesa_paybills
		  (tenant_id, label, shortcode, purpose, scope, environment, webhook_token, created_by)
		VALUES (
		  $1, $2, $3, $4, $5, $6,
		  COALESCE(NULLIF($7,''), encode(gen_random_bytes(24), 'hex')),
		  $8
		)
		RETURNING `+paybillCols+`
	`, in.TenantID, in.Label, in.Shortcode, in.Purpose, in.Scope, in.Environment,
		in.WebhookToken, in.CreatedBy), &p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PaybillStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Paybill, error) {
	var p domain.Paybill
	err := scanPaybill(tx.QueryRow(ctx, `SELECT `+paybillCols+` FROM mpesa_paybills WHERE id = $1`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ByIDAndToken is the webhook entry-point lookup. Safaricom hits us
// at /v1/mpesa/c2b/{paybill_id}/... with no tenant subdomain, so the
// handler can't set app.tenant_id before the lookup. The query goes
// through a SECURITY DEFINER function (migration 0004) that bypasses
// RLS for this one (id, token) pair; once we have the tenant_id, the
// caller switches into a tenant-scoped tx for any further work.
//
// Returns ErrNotFound when either the id is unknown OR the token
// doesn't match — the caller cannot distinguish, so an attacker
// can't enumerate paybills by id alone.
func (s *PaybillStore) ByIDAndToken(ctx context.Context, id uuid.UUID, token string) (*domain.Paybill, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var p domain.Paybill
	err := scanPaybill(s.pool.QueryRow(ctx,
		`SELECT * FROM mpesa_paybill_resolve_by_token($1, $2)`, id, token), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
