// Fiscal year close persistence.
//
// One row per (tenant, year) closure. Tracks the closing journal entry,
// the rolled-up P&L, and audit metadata. Closing is a one-shot
// operation per year — re-opening requires a manual reversal of the
// closing journal and a row delete (admin-only path; not exposed via
// HTTP yet).

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type FiscalYearStore struct {
	pool *pgxpool.Pool
}

func NewFiscalYearStore(pool *pgxpool.Pool) *FiscalYearStore {
	return &FiscalYearStore{pool: pool}
}

type FiscalYearClose struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	Year            int             `json:"year"`
	FYStart         time.Time       `json:"fy_start"`
	FYEnd           time.Time       `json:"fy_end"`
	ClosingEntryID  uuid.UUID       `json:"closing_entry_id"`
	TotalIncome     decimal.Decimal `json:"total_income"`
	TotalExpense    decimal.Decimal `json:"total_expense"`
	NetSurplus      decimal.Decimal `json:"net_surplus"`
	IncomeAccounts  int             `json:"income_accounts"`
	ExpenseAccounts int             `json:"expense_accounts"`
	ClosedAt        time.Time       `json:"closed_at"`
	ClosedBy        uuid.UUID       `json:"closed_by"`
	Notes           *string         `json:"notes,omitempty"`
}

var ErrYearAlreadyClosed = errors.New("fiscal year is already closed")

func (s *FiscalYearStore) ListTx(ctx context.Context, tx pgx.Tx) ([]FiscalYearClose, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, year, fy_start, fy_end, closing_entry_id,
		       total_income, total_expense, net_surplus,
		       income_accounts, expense_accounts, closed_at, closed_by, notes
		  FROM fiscal_year_closes
		 ORDER BY year DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FiscalYearClose{}
	for rows.Next() {
		var r FiscalYearClose
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.Year, &r.FYStart, &r.FYEnd, &r.ClosingEntryID,
			&r.TotalIncome, &r.TotalExpense, &r.NetSurplus,
			&r.IncomeAccounts, &r.ExpenseAccounts, &r.ClosedAt, &r.ClosedBy, &r.Notes,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *FiscalYearStore) IsClosedTx(ctx context.Context, tx pgx.Tx, year int) (bool, error) {
	var count int
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM fiscal_year_closes WHERE year = $1`, year,
	).Scan(&count)
	return count > 0, err
}

// ClosingLine — one leg of the closing journal entry.
type ClosingLine struct {
	AccountCode string
	AccountID   uuid.UUID
	AccountName string
	Class       string
	Balance     decimal.Decimal // natural-side balance pre-close
}

// PLBalancesAsOfTx — every income + expense account with a non-zero
// projected balance through `asOf`. Used to build the closing entry.
//
// For income accounts (normal=credit), Balance is credits − debits.
// For expense accounts (normal=debit), Balance is debits − credits.
// In both cases, a positive Balance means there's a P&L impact to zero.
func (s *FiscalYearStore) PLBalancesAsOfTx(ctx context.Context, tx pgx.Tx, asOf time.Time) ([]ClosingLine, error) {
	// CASE WHEN guard — without it, the LEFT JOIN journal_lines pulls in
	// every line for the account regardless of whether its entry passed
	// the date filter, which would close out future-dated P&L into the
	// current year. See BalanceSheetTx for the longer explanation.
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.class,
		       COALESCE(SUM(CASE WHEN je.id IS NOT NULL THEN l.debit - l.credit ELSE 0 END), 0) AS net
		  FROM chart_of_accounts a
		  LEFT JOIN journal_lines l   ON l.account_id = a.id
		  LEFT JOIN journal_entries je ON je.id = l.entry_id
		                              AND je.status = 'posted'
		                              AND je.entry_date <= $1
		 WHERE a.class IN ('income', 'expense')
		   AND a.is_active = true
		 GROUP BY a.id, a.code, a.name, a.class
		 ORDER BY a.code
	`, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClosingLine
	for rows.Next() {
		var (
			id    uuid.UUID
			code  string
			name  string
			class string
			net   decimal.Decimal
		)
		if err := rows.Scan(&id, &code, &name, &class, &net); err != nil {
			return nil, err
		}
		balance := net
		if class == "income" {
			balance = balance.Neg() // income normal=credit; positive means credit balance
		}
		if balance.IsZero() {
			continue
		}
		out = append(out, ClosingLine{
			AccountCode: code,
			AccountID:   id,
			AccountName: name,
			Class:       class,
			Balance:     balance,
		})
	}
	return out, rows.Err()
}

// RecordCloseTx inserts the audit row. Caller must have already posted
// the closing journal entry within the same tx.
func (s *FiscalYearStore) RecordCloseTx(
	ctx context.Context, tx pgx.Tx,
	in FiscalYearClose,
) (*FiscalYearClose, error) {
	id := uuid.New()
	row := tx.QueryRow(ctx, `
		INSERT INTO fiscal_year_closes (
		  id, tenant_id, year, fy_start, fy_end, closing_entry_id,
		  total_income, total_expense, net_surplus,
		  income_accounts, expense_accounts,
		  closed_by, notes
		) VALUES ($1, current_tenant_id(), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12,''))
		RETURNING id, tenant_id, closed_at
	`,
		id, in.Year, in.FYStart, in.FYEnd, in.ClosingEntryID,
		in.TotalIncome, in.TotalExpense, in.NetSurplus,
		in.IncomeAccounts, in.ExpenseAccounts,
		in.ClosedBy, derefStr(in.Notes),
	)
	if err := row.Scan(&in.ID, &in.TenantID, &in.ClosedAt); err != nil {
		return nil, err
	}
	return &in, nil
}

// LockAllPeriodsInYearTx flips every monthly period in `year` to closed.
// Used right after posting the closing journal so no further entries
// can land in the year.
func (s *FiscalYearStore) LockAllPeriodsInYearTx(ctx context.Context, tx pgx.Tx, year int, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE accounting_periods
		   SET status = 'closed',
		       closed_at = COALESCE(closed_at, now()),
		       closed_by = COALESCE(closed_by, $2),
		       notes = COALESCE(notes,'') ||
		               CASE WHEN status = 'open' THEN E'\nLocked by year-end close' ELSE '' END,
		       updated_at = now()
		 WHERE year = $1 AND status = 'open'
	`, year, userID)
	return err
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
