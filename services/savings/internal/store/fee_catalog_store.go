// Fee-catalog persistence — list + lookup + admin create. RLS-scoped
// by tenant_id. The catalog is bounded (single-digit to dozens of
// entries per tenant) so no pagination on the list endpoint.

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

// ErrUnknownGLCode is returned by CreateTx when the caller-supplied
// gl_credit_code doesn't resolve against the tenant's chart_of_accounts.
// Before this guard shipped, a typo silently produced a fee_catalog
// row that crashed every receipt that picked it up (the cashier
// hit ErrUnknownAccount inside posting.Post and the whole receipt
// rolled back). The handler maps this to a 422 + a human-readable
// "unknown GL account X" message.
var ErrUnknownGLCode = errors.New("gl_credit_code does not resolve in the tenant's chart of accounts")

type FeeCatalogStore struct {
	pool *pgxpool.Pool
}

func NewFeeCatalogStore(pool *pgxpool.Pool) *FeeCatalogStore {
	return &FeeCatalogStore{pool: pool}
}

const feeCatCols = `
	id, tenant_id, code, label, description, amount_default, amount_editable,
	gl_credit_code, is_active, sort_order, created_at, updated_at
`

func scanFeeCatalog(row pgx.Row) (*domain.FeeCatalogEntry, error) {
	var e domain.FeeCatalogEntry
	err := row.Scan(
		&e.ID, &e.TenantID, &e.Code, &e.Label, &e.Description, &e.AmountDefault, &e.AmountEditable,
		&e.GLCreditCode, &e.IsActive, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListActiveTx returns the active subset of the catalog ordered by
// sort_order. Drives the Collection Desk's fee picker.
func (s *FeeCatalogStore) ListActiveTx(ctx context.Context, tx pgx.Tx) ([]domain.FeeCatalogEntry, error) {
	rows, err := tx.Query(ctx, `SELECT `+feeCatCols+` FROM fee_catalog WHERE is_active = true ORDER BY sort_order, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeeCatalogEntry
	for rows.Next() {
		e, err := scanFeeCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListAllTx returns every catalog entry (active + retired) for the
// admin "manage fees" view. Same ordering.
func (s *FeeCatalogStore) ListAllTx(ctx context.Context, tx pgx.Tx) ([]domain.FeeCatalogEntry, error) {
	rows, err := tx.Query(ctx, `SELECT `+feeCatCols+` FROM fee_catalog ORDER BY is_active DESC, sort_order, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FeeCatalogEntry
	for rows.Next() {
		e, err := scanFeeCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// GetByCodeTx is the lookup the Collection Desk fee-line executor
// uses to resolve a code → GL account + default amount at posting
// time. Returns ErrNotFound for an unknown code so the caller can
// surface 400 / "fee code not in catalog".
func (s *FeeCatalogStore) GetByCodeTx(ctx context.Context, tx pgx.Tx, code string) (*domain.FeeCatalogEntry, error) {
	row := tx.QueryRow(ctx, `SELECT `+feeCatCols+` FROM fee_catalog WHERE code = $1`, code)
	e, err := scanFeeCatalog(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// CreateInput is the admin-create payload. tenant scope comes from
// the tx's RLS context — caller doesn't pass it.
type CreateFeeCatalogInput struct {
	Code           string
	Label          string
	Description    *string
	AmountDefault  decimal.Decimal
	AmountEditable bool
	GLCreditCode   string
	SortOrder      int
}

func (s *FeeCatalogStore) CreateTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in CreateFeeCatalogInput) (*domain.FeeCatalogEntry, error) {
	in.Code = strings.TrimSpace(strings.ToLower(in.Code))
	in.GLCreditCode = strings.TrimSpace(in.GLCreditCode)
	if in.Code == "" || in.Label == "" || in.GLCreditCode == "" {
		return nil, fmt.Errorf("fee_catalog: code, label, gl_credit_code are required")
	}
	// Verify the GL code exists in the tenant's chart_of_accounts
	// BEFORE inserting so a typo can never produce a catalog row
	// that crashes downstream postings. SELECT 1 with RLS is enough
	// — the chart_of_accounts table is per-tenant + tenant-scoped
	// by policy.
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM chart_of_accounts
		   WHERE tenant_id = $1 AND code = $2 AND is_active = true
		)
	`, tenantID, in.GLCreditCode).Scan(&exists); err != nil {
		return nil, fmt.Errorf("fee_catalog: verify gl_credit_code: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrUnknownGLCode, in.GLCreditCode)
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO fee_catalog (
			tenant_id, code, label, description, amount_default, amount_editable, gl_credit_code, sort_order
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+feeCatCols,
		tenantID, in.Code, in.Label, in.Description, in.AmountDefault, in.AmountEditable, in.GLCreditCode, in.SortOrder,
	)
	return scanFeeCatalog(row)
}
