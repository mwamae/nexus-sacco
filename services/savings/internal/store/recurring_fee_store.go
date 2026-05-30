// DSID Phase 2.2 — per-product recurring fees.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type RecurringFeeStore struct {
	pool *pgxpool.Pool
}

func NewRecurringFeeStore(pool *pgxpool.Pool) *RecurringFeeStore {
	return &RecurringFeeStore{pool: pool}
}

type RecurringFee struct {
	ID           uuid.UUID       `json:"id"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	ProductID    uuid.UUID       `json:"product_id"`
	FeeKind      string          `json:"fee_kind"`
	Amount       decimal.Decimal `json:"amount"`
	Frequency    string          `json:"frequency"` // 'monthly','quarterly','annual'
	GLCreditCode string          `json:"gl_credit_code"`
	Active       bool            `json:"active"`
	StartsOn     time.Time       `json:"starts_on"`
	EndsOn       *time.Time      `json:"ends_on,omitempty"`
	Notes        *string         `json:"notes,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	CreatedBy    uuid.UUID       `json:"created_by"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

const rfCols = `
	id, tenant_id, product_id, fee_kind, amount, frequency::text,
	gl_credit_code, active, starts_on, ends_on, notes,
	created_at, created_by, updated_at
`

func scanRF(row pgx.Row) (*RecurringFee, error) {
	var f RecurringFee
	err := row.Scan(
		&f.ID, &f.TenantID, &f.ProductID, &f.FeeKind, &f.Amount, &f.Frequency,
		&f.GLCreditCode, &f.Active, &f.StartsOn, &f.EndsOn, &f.Notes,
		&f.CreatedAt, &f.CreatedBy, &f.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}

type CreateRecurringFeeInput struct {
	TenantID     uuid.UUID
	ProductID    uuid.UUID
	FeeKind      string
	Amount       decimal.Decimal
	Frequency    string
	GLCreditCode string
	StartsOn     time.Time
	EndsOn       *time.Time
	Notes        string
	CreatedBy    uuid.UUID
}

func (s *RecurringFeeStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateRecurringFeeInput) (*RecurringFee, error) {
	return scanRF(tx.QueryRow(ctx, `
		INSERT INTO deposit_product_recurring_fees
		    (tenant_id, product_id, fee_kind, amount, frequency,
		     gl_credit_code, starts_on, ends_on, notes, created_by)
		VALUES ($1, $2, $3, $4, $5::recurring_fee_frequency,
		        $6, $7, $8, NULLIF($9, ''), $10)
		RETURNING `+rfCols,
		in.TenantID, in.ProductID, in.FeeKind, in.Amount, in.Frequency,
		in.GLCreditCode, in.StartsOn, in.EndsOn, in.Notes, in.CreatedBy,
	))
}

func (s *RecurringFeeStore) ListByProductTx(ctx context.Context, tx pgx.Tx, productID uuid.UUID) ([]RecurringFee, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+rfCols+`
		  FROM deposit_product_recurring_fees
		 WHERE product_id = $1
		 ORDER BY active DESC, fee_kind
	`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecurringFee{}
	for rows.Next() {
		f, err := scanRF(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *RecurringFeeStore) ListActiveAllTx(ctx context.Context, tx pgx.Tx) ([]RecurringFee, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+rfCols+`
		  FROM deposit_product_recurring_fees
		 WHERE active = true
		   AND starts_on <= CURRENT_DATE
		   AND (ends_on IS NULL OR ends_on >= CURRENT_DATE)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecurringFee{}
	for rows.Next() {
		f, err := scanRF(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

type UpdateRecurringFeeInput struct {
	ID           uuid.UUID
	Amount       *decimal.Decimal
	GLCreditCode *string
	Active       *bool
	EndsOn       *time.Time
	Notes        *string
}

func (s *RecurringFeeStore) UpdateTx(ctx context.Context, tx pgx.Tx, in UpdateRecurringFeeInput) (*RecurringFee, error) {
	set := []string{"updated_at = now()"}
	args := []any{in.ID}
	if in.Amount != nil {
		args = append(args, *in.Amount)
		set = append(set, fmt.Sprintf("amount = $%d", len(args)))
	}
	if in.GLCreditCode != nil {
		args = append(args, *in.GLCreditCode)
		set = append(set, fmt.Sprintf("gl_credit_code = $%d", len(args)))
	}
	if in.Active != nil {
		args = append(args, *in.Active)
		set = append(set, fmt.Sprintf("active = $%d", len(args)))
	}
	if in.EndsOn != nil {
		args = append(args, *in.EndsOn)
		set = append(set, fmt.Sprintf("ends_on = $%d", len(args)))
	}
	if in.Notes != nil {
		args = append(args, *in.Notes)
		set = append(set, fmt.Sprintf("notes = NULLIF($%d, '')", len(args)))
	}
	q := `UPDATE deposit_product_recurring_fees SET `
	for i, s := range set {
		if i > 0 {
			q += ", "
		}
		q += s
	}
	q += ` WHERE id = $1 RETURNING ` + rfCols
	return scanRF(tx.QueryRow(ctx, q, args...))
}

func (s *RecurringFeeStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM deposit_product_recurring_fees WHERE id = $1`, id)
	return err
}

// ─────────── Charge attempts ───────────

type RecurringFeeCharge struct {
	ID              uuid.UUID       `json:"id"`
	AccountID       uuid.UUID       `json:"account_id"`
	FeeDefinitionID uuid.UUID       `json:"fee_definition_id"`
	PeriodLabel     string          `json:"period_label"`
	Amount          decimal.Decimal `json:"amount"`
	ChargedAt       time.Time       `json:"charged_at"`
	PostedTxnID     *uuid.UUID      `json:"posted_txn_id,omitempty"`
	Status          string          `json:"status"` // 'posted' | 'waived' | 'insufficient_funds'
}

type RecordRecurringFeeChargeInput struct {
	TenantID        uuid.UUID
	AccountID       uuid.UUID
	FeeDefinitionID uuid.UUID
	PeriodLabel     string
	Amount          decimal.Decimal
	Status          string
	PostedTxnID     *uuid.UUID
}

func (s *RecurringFeeStore) RecordChargeTx(ctx context.Context, tx pgx.Tx, in RecordRecurringFeeChargeInput) (*RecurringFeeCharge, bool, error) {
	var c RecurringFeeCharge
	var inserted bool
	err := tx.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO deposit_account_recurring_fee_charges
			    (tenant_id, account_id, fee_definition_id, period_label,
			     amount, status, posted_txn_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (account_id, fee_definition_id, period_label) DO NOTHING
			RETURNING id, account_id, fee_definition_id, period_label,
			          amount, charged_at, posted_txn_id, status, true AS inserted
		)
		SELECT * FROM ins
		UNION ALL
		SELECT id, account_id, fee_definition_id, period_label,
		       amount, charged_at, posted_txn_id, status, false AS inserted
		  FROM deposit_account_recurring_fee_charges
		 WHERE account_id = $2 AND fee_definition_id = $3 AND period_label = $4
		 LIMIT 1
	`,
		in.TenantID, in.AccountID, in.FeeDefinitionID, in.PeriodLabel,
		in.Amount, in.Status, in.PostedTxnID,
	).Scan(
		&c.ID, &c.AccountID, &c.FeeDefinitionID, &c.PeriodLabel,
		&c.Amount, &c.ChargedAt, &c.PostedTxnID, &c.Status, &inserted,
	)
	return &c, inserted, err
}

// PeriodLabel formats the period key per frequency.
func PeriodLabel(frequency string, t time.Time) string {
	t = t.UTC()
	switch frequency {
	case "monthly":
		return t.Format("2006-01")
	case "quarterly":
		q := (int(t.Month())-1)/3 + 1
		return fmt.Sprintf("%d-Q%d", t.Year(), q)
	case "annual":
		return t.Format("2006")
	}
	return t.Format("2006-01-02")
}
