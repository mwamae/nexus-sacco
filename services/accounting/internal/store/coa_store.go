// Chart of Accounts persistence. CRUD + lookup-by-code (the latter is
// the hot path for the posting engine when it resolves rule lines to
// account ids).

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/accounting/internal/domain"
)

var ErrNotFound = errors.New("not found")

type CoAStore struct {
	pool *pgxpool.Pool
}

func NewCoAStore(pool *pgxpool.Pool) *CoAStore {
	return &CoAStore{pool: pool}
}

const accountCols = `
	id, tenant_id, code, name, class, type, parent_id, normal_balance,
	currency_code, is_active, is_system_locked, description,
	created_at, updated_at
`

func scanAccount(row pgx.Row) (*domain.Account, error) {
	var a domain.Account
	var class, nb string
	err := row.Scan(
		&a.ID, &a.TenantID, &a.Code, &a.Name, &class, &a.Type, &a.ParentID, &nb,
		&a.CurrencyCode, &a.IsActive, &a.IsSystemLocked, &a.Description,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.Class = domain.AccountClass(class)
	a.NormalBalance = domain.NormalBalance(nb)
	return &a, nil
}

func (s *CoAStore) ListTx(ctx context.Context, tx pgx.Tx, activeOnly bool) ([]domain.Account, error) {
	where := ""
	if activeOnly {
		where = "WHERE is_active = true"
	}
	rows, err := tx.Query(ctx, `SELECT `+accountCols+` FROM chart_of_accounts `+where+` ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *CoAStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Account, error) {
	row := tx.QueryRow(ctx, `SELECT `+accountCols+` FROM chart_of_accounts WHERE id = $1`, id)
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *CoAStore) GetByCodeTx(ctx context.Context, tx pgx.Tx, code string) (*domain.Account, error) {
	row := tx.QueryRow(ctx, `SELECT `+accountCols+` FROM chart_of_accounts WHERE code = $1`, code)
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

type CreateAccountInput struct {
	Code          string
	Name          string
	Class         domain.AccountClass
	Type          string
	ParentID      *uuid.UUID
	NormalBalance domain.NormalBalance
	CurrencyCode  string
	IsActive      bool
	Description   *string
}

func (s *CoAStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateAccountInput) (*domain.Account, error) {
	if in.CurrencyCode == "" {
		in.CurrencyCode = "KES"
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO chart_of_accounts
		    (tenant_id, code, name, class, type, parent_id, normal_balance,
		     currency_code, is_active, is_system_locked, description)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, false, $9)
		RETURNING `+accountCols,
		in.Code, in.Name, string(in.Class), in.Type, in.ParentID,
		string(in.NormalBalance), in.CurrencyCode, in.IsActive, in.Description,
	)
	return scanAccount(row)
}

type UpdateAccountInput struct {
	Name        string
	Type        string
	ParentID    *uuid.UUID
	IsActive    bool
	Description *string
}

// UpdateTx — disallows changes to system-locked accounts. The class
// + normal_balance + code stay immutable to prevent rewriting history.
func (s *CoAStore) UpdateTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, in UpdateAccountInput) (*domain.Account, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE chart_of_accounts
		SET name = $2, type = $3, parent_id = $4, is_active = $5, description = $6,
		    updated_at = now()
		WHERE id = $1 AND is_system_locked = false
	`, id, in.Name, in.Type, in.ParentID, in.IsActive, in.Description)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		// Either not found OR system-locked. Disambiguate so the
		// handler can return a clearer status.
		row := tx.QueryRow(ctx, `SELECT is_system_locked FROM chart_of_accounts WHERE id = $1`, id)
		var locked bool
		if scanErr := row.Scan(&locked); scanErr != nil {
			return nil, ErrNotFound
		}
		if locked {
			return nil, ErrSystemLocked
		}
		return nil, ErrNotFound
	}
	return s.GetTx(ctx, tx, id)
}

var ErrSystemLocked = errors.New("system-locked account; cannot modify")
