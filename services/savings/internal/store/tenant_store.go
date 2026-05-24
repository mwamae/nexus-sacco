// Read-only access to the identity-owned `tenants` table. We only need
// slug → id resolution for the subdomain middleware, and tenant operations
// (par value, min/max share holding) for share rules enforcement.

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

var ErrNotFound = errors.New("not found")

type TenantStore struct {
	pool *pgxpool.Pool
}

func NewTenantStore(pool *pgxpool.Pool) *TenantStore {
	return &TenantStore{pool: pool}
}

type Tenant struct {
	ID     uuid.UUID
	Slug   string
	Name   string
	Status string
}

func (s *TenantStore) BySlug(ctx context.Context, slug string) (*Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, status
		FROM tenants WHERE slug = $1
	`, slug).Scan(&t.ID, &t.Slug, &t.Name, &t.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SharePolicy returns the configured per-tenant share rules.
// Caller is responsible for being inside a tenant-bound transaction
// (or having no RLS, e.g. platform admin reads).
type SharePolicy struct {
	ParValue              decimal.Decimal
	MinSharesRequired     int
	MaxSharesPctOfCapital decimal.Decimal // 0 means unlimited
	CertificatePrefix     string
}

func (s *TenantStore) SharePolicyTx(ctx context.Context, tx pgx.Tx) (*SharePolicy, error) {
	var p SharePolicy
	err := tx.QueryRow(ctx, `
		SELECT share_par_value, min_shares_required, max_shares_pct_of_capital, share_certificate_prefix
		FROM tenant_operations
	`).Scan(&p.ParValue, &p.MinSharesRequired, &p.MaxSharesPctOfCapital, &p.CertificatePrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// BOSAFOSAEnabledTx reports whether the current tenant has opted into
// BOSA/FOSA segmentation. Defaults to false so any tenant that hasn't
// flipped the flag (i.e. every tenant on day one) keeps the legacy
// scoring behaviour without a surprise ceiling collapse.
//
// Reads via RLS — caller is responsible for being inside a
// tenant-scoped transaction. `tenant_id = $1`-style filtering would
// be redundant; the WHERE is implicit through current_tenant_id().
func (s *TenantStore) BOSAFOSAEnabledTx(ctx context.Context, tx pgx.Tx) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(bosa_fosa_enabled, false) FROM tenants LIMIT 1
	`).Scan(&enabled)
	if err != nil {
		return false, err
	}
	return enabled, nil
}
