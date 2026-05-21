// Tenant store — global table (no RLS).
// Branch + contact rows ARE tenant-scoped; their methods take a pgx.Tx that
// the caller is expected to have bound to app.tenant_id via WithTenantTx.

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
	Slug           string
	Name           string
	LegalName      string
	Kind           domain.TenantKind
	CountryCode    string
	CurrencyCode   string
	LicenseNo      string
	RegistrationNo string
	TaxPIN         string
	BillingPlan    domain.BillingPlan
}

func (s *TenantStore) Create(ctx context.Context, in CreateTenantInput) (*domain.Tenant, error) {
	plan := in.BillingPlan
	if plan == "" {
		plan = domain.BillingStarter
	}
	var t domain.Tenant
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tenants (
		  slug, name, legal_name, kind, country_code, currency_code,
		  license_no, registration_no, tax_pin, billing_plan
		)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10)
		RETURNING `+tenantSelectCols+`
	`, in.Slug, in.Name, in.LegalName, in.Kind, in.CountryCode, in.CurrencyCode,
		in.LicenseNo, in.RegistrationNo, in.TaxPIN, plan).
		Scan(tenantScanDests(&t)...)
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
		SELECT `+tenantSelectCols+`
		FROM tenants ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Tenant
	for rows.Next() {
		var t domain.Tenant
		if err := rows.Scan(tenantScanDests(&t)...); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *TenantStore) queryOne(ctx context.Context, where string, args ...any) (*domain.Tenant, error) {
	var t domain.Tenant
	q := `SELECT ` + tenantSelectCols + ` FROM tenants ` + where
	err := s.pool.QueryRow(ctx, q, args...).Scan(tenantScanDests(&t)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ─────────── Branches ───────────

type BranchInput struct {
	Code            string
	Name            string
	Kind            domain.BranchKind
	County          string
	SubCounty       string
	PhysicalAddress string
	Phone           string
}

// ReplaceBranchesTx wipes the tenant's branches and re-inserts the given
// rows in order. Caller's tx must have app.tenant_id bound (WithTenantTx).
func (s *TenantStore) ReplaceBranchesTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, rows []BranchInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_branches WHERE tenant_id = $1`, tenantID); err != nil {
		return err
	}
	for i, b := range rows {
		kind := b.Kind
		if kind == "" {
			kind = domain.BranchBranch
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenant_branches
			  (tenant_id, code, name, kind, county, sub_county, physical_address, phone, position)
			VALUES
			  ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9)
		`, tenantID, b.Code, b.Name, kind, b.County, b.SubCounty, b.PhysicalAddress, b.Phone, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *TenantStore) BranchesForTenantTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]*domain.TenantBranch, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, code, name, kind,
		       COALESCE(county,''), COALESCE(sub_county,''),
		       COALESCE(physical_address,''), COALESCE(phone,''),
		       position
		FROM tenant_branches WHERE tenant_id = $1
		ORDER BY position
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TenantBranch
	for rows.Next() {
		var b domain.TenantBranch
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Code, &b.Name, &b.Kind,
			&b.County, &b.SubCounty, &b.PhysicalAddress, &b.Phone, &b.Position); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// ─────────── Contacts ───────────

type ContactInput struct {
	FullName string
	Title    string
	Email    string
	Phone    string
}

// AddContactTx inserts a single new contact, appending it to the end
// of the tenant's contact list. Used by the per-tenant "Add contact"
// flow that's separate from the bulk-replace used at tenant creation.
func (s *TenantStore) AddContactTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in ContactInput) (*domain.TenantContact, error) {
	var nextPos int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(position)+1, 0) FROM tenant_contacts WHERE tenant_id = $1`,
		tenantID,
	).Scan(&nextPos); err != nil {
		return nil, err
	}
	var c domain.TenantContact
	err := tx.QueryRow(ctx, `
		INSERT INTO tenant_contacts (tenant_id, full_name, title, email, phone, position)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6)
		RETURNING id, tenant_id, full_name, COALESCE(title,''), COALESCE(email,''), COALESCE(phone,''), position
	`, tenantID, in.FullName, in.Title, in.Email, in.Phone, nextPos,
	).Scan(&c.ID, &c.TenantID, &c.FullName, &c.Title, &c.Email, &c.Phone, &c.Position)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateContactTx edits an existing contact in place. Position is left
// unchanged; if reordering is needed we'd add a separate endpoint.
func (s *TenantStore) UpdateContactTx(ctx context.Context, tx pgx.Tx, contactID uuid.UUID, in ContactInput) (*domain.TenantContact, error) {
	var c domain.TenantContact
	err := tx.QueryRow(ctx, `
		UPDATE tenant_contacts
		SET full_name = $2,
		    title     = NULLIF($3,''),
		    email     = NULLIF($4,''),
		    phone     = NULLIF($5,'')
		WHERE id = $1
		RETURNING id, tenant_id, full_name, COALESCE(title,''), COALESCE(email,''), COALESCE(phone,''), position
	`, contactID, in.FullName, in.Title, in.Email, in.Phone,
	).Scan(&c.ID, &c.TenantID, &c.FullName, &c.Title, &c.Email, &c.Phone, &c.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteContactTx removes a single contact row. Positions of remaining
// contacts are not renumbered — they're sparse (e.g. 0, 2, 3) but the
// ORDER BY position in ContactsForTenantTx still returns them in the
// right sequence.
func (s *TenantStore) DeleteContactTx(ctx context.Context, tx pgx.Tx, contactID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM tenant_contacts WHERE id = $1`, contactID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *TenantStore) ReplaceContactsTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, rows []ContactInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_contacts WHERE tenant_id = $1`, tenantID); err != nil {
		return err
	}
	for i, c := range rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenant_contacts (tenant_id, full_name, title, email, phone, position)
			VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6)
		`, tenantID, c.FullName, c.Title, c.Email, c.Phone, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *TenantStore) ContactsForTenantTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]*domain.TenantContact, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, full_name, COALESCE(title,''), COALESCE(email,''), COALESCE(phone,''), position
		FROM tenant_contacts WHERE tenant_id = $1
		ORDER BY position
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TenantContact
	for rows.Next() {
		var c domain.TenantContact
		if err := rows.Scan(&c.ID, &c.TenantID, &c.FullName, &c.Title, &c.Email, &c.Phone, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ─────────── Helpers ───────────

const tenantSelectCols = `id, slug, name, COALESCE(legal_name,''), kind, status,
	country_code, currency_code, COALESCE(license_no,''),
	COALESCE(registration_no,''), COALESCE(tax_pin,''),
	billing_plan,
	operations_frozen, users_locked, transactions_disabled,
	created_at, updated_at`

func tenantScanDests(t *domain.Tenant) []any {
	return []any{
		&t.ID, &t.Slug, &t.Name, &t.LegalName, &t.Kind, &t.Status,
		&t.CountryCode, &t.CurrencyCode, &t.LicenseNo,
		&t.RegistrationNo, &t.TaxPIN,
		&t.BillingPlan,
		&t.Restrictions.OperationsFrozen,
		&t.Restrictions.UsersLocked,
		&t.Restrictions.TransactionsDisabled,
		&t.CreatedAt, &t.UpdatedAt,
	}
}

// SetStatus changes the tenant's lifecycle status.
func (s *TenantStore) SetStatus(ctx context.Context, id uuid.UUID, status domain.TenantStatus) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tenants SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRestrictions flips operational toggles. Pass any field nil to leave it unchanged.
func (s *TenantStore) SetRestrictions(ctx context.Context, id uuid.UUID, ops, users, txns *bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenants
		SET operations_frozen     = COALESCE($2, operations_frozen),
		    users_locked          = COALESCE($3, users_locked),
		    transactions_disabled = COALESCE($4, transactions_disabled)
		WHERE id = $1
	`, id, ops, users, txns)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Archive flips status to 'archived' and turns on all three restriction
// toggles in one statement.
func (s *TenantStore) Archive(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tenants
		SET status = 'archived',
		    operations_frozen     = true,
		    users_locked          = true,
		    transactions_disabled = true
		WHERE id = $1
	`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

var ErrNotFound = errors.New("store: not found")
