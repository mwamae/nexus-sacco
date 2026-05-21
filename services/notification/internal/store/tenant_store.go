// Read-only access to the identity-owned `tenants` table. We only need
// slug → id resolution for the subdomain middleware.

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// ErrNotFound lives in event_store.go — re-used across stores.

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

// TimezoneTx returns the tenant's configured IANA timezone (e.g.
// "Africa/Nairobi"). Missing tenant_region rows fall back to UTC so
// existing tenants from before this column was seeded still function.
func (s *TenantStore) TimezoneTx(ctx context.Context, tx pgx.Tx) (string, error) {
	var tz string
	err := tx.QueryRow(ctx, `SELECT timezone FROM tenant_region LIMIT 1`).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "UTC", nil
	}
	if err != nil {
		return "", err
	}
	if tz == "" {
		return "UTC", nil
	}
	return tz, nil
}
