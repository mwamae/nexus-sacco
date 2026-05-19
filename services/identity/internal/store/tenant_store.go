// Tenant store — global table (no RLS).

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type TenantStore struct {
	pool *pgxpool.Pool
}

func NewTenantStore(pool *pgxpool.Pool) *TenantStore {
	return &TenantStore{pool: pool}
}

type CreateTenantInput struct {
	Slug         string
	Name         string
	LegalName    string
	Kind         domain.TenantKind
	CountryCode  string
	CurrencyCode string
	LicenseNo    string
}

func (s *TenantStore) Create(ctx context.Context, in CreateTenantInput) (*domain.Tenant, error) {
	var t domain.Tenant
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, name, legal_name, kind, country_code, currency_code, license_no)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, NULLIF($7,''))
		RETURNING id, slug, name, COALESCE(legal_name,''), kind, status,
		          country_code, currency_code, COALESCE(license_no,''),
		          created_at, updated_at
	`, in.Slug, in.Name, in.LegalName, in.Kind, in.CountryCode, in.CurrencyCode, in.LicenseNo).
		Scan(&t.ID, &t.Slug, &t.Name, &t.LegalName, &t.Kind, &t.Status,
			&t.CountryCode, &t.CurrencyCode, &t.LicenseNo,
			&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}
	return &t, nil
}

func (s *TenantStore) BySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	return s.queryOne(ctx, `WHERE slug = $1`, slug)
}

func (s *TenantStore) ByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	return s.queryOne(ctx, `WHERE id = $1`, id)
}

func (s *TenantStore) List(ctx context.Context, limit int) ([]*domain.Tenant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, COALESCE(legal_name,''), kind, status,
		       country_code, currency_code, COALESCE(license_no,''),
		       created_at, updated_at
		FROM tenants ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Tenant
	for rows.Next() {
		var t domain.Tenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.LegalName, &t.Kind, &t.Status,
			&t.CountryCode, &t.CurrencyCode, &t.LicenseNo,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *TenantStore) queryOne(ctx context.Context, where string, args ...any) (*domain.Tenant, error) {
	var t domain.Tenant
	q := `SELECT id, slug, name, COALESCE(legal_name,''), kind, status,
	             country_code, currency_code, COALESCE(license_no,''),
	             created_at, updated_at
	      FROM tenants ` + where
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&t.ID, &t.Slug, &t.Name, &t.LegalName, &t.Kind, &t.Status,
		&t.CountryCode, &t.CurrencyCode, &t.LicenseNo,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

var ErrNotFound = errors.New("store: not found")
