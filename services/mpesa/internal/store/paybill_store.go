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
	CreatedBy   *uuid.UUID
}

func (s *PaybillStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreatePaybillInput) (*domain.Paybill, error) {
	var p domain.Paybill
	err := tx.QueryRow(ctx, `
		INSERT INTO mpesa_paybills
		  (tenant_id, label, shortcode, purpose, scope, environment, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, label, shortcode, purpose, scope, environment, status,
		          distribution_policy_id, created_by, created_at, updated_at
	`, in.TenantID, in.Label, in.Shortcode, in.Purpose, in.Scope, in.Environment, in.CreatedBy).Scan(
		&p.ID, &p.TenantID, &p.Label, &p.Shortcode, &p.Purpose, &p.Scope, &p.Environment, &p.Status,
		&p.DistributionPolicyID, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PaybillStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Paybill, error) {
	var p domain.Paybill
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, label, shortcode, purpose, scope, environment, status,
		       distribution_policy_id, created_by, created_at, updated_at
		  FROM mpesa_paybills WHERE id = $1
	`, id).Scan(
		&p.ID, &p.TenantID, &p.Label, &p.Shortcode, &p.Purpose, &p.Scope, &p.Environment, &p.Status,
		&p.DistributionPolicyID, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
