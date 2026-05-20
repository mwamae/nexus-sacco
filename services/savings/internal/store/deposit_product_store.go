// Deposit product configuration — CRUD against deposit_products.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/domain"
)

type DepositProductStore struct {
	pool *pgxpool.Pool
}

func NewDepositProductStore(pool *pgxpool.Pool) *DepositProductStore {
	return &DepositProductStore{pool: pool}
}

const productCols = `
	id, tenant_id, code, name, product_type, description, is_active,
	min_opening_balance, min_operating_balance, max_balance,
	min_deposit_amount, max_deposit_amount,
	min_withdrawal_amount, max_withdrawal_amount,
	notice_period_days, max_withdrawals_per_month, partial_withdrawal_allowed,
	large_withdrawal_threshold,
	lock_in_months, default_term_months, maturity_action,
	eligibility, requires_approval_to_open,
	withdrawal_window_start_month, withdrawal_window_end_month,
	maintenance_fee, maintenance_fee_frequency,
	early_withdrawal_penalty_pct, below_min_balance_fee, dormancy_fee_monthly,
	created_at, updated_at, created_by
`

func scanProduct(row pgx.Row) (*domain.DepositProduct, error) {
	var p domain.DepositProduct
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Code, &p.Name, &p.ProductType, &p.Description, &p.IsActive,
		&p.MinOpeningBalance, &p.MinOperatingBalance, &p.MaxBalance,
		&p.MinDepositAmount, &p.MaxDepositAmount,
		&p.MinWithdrawalAmount, &p.MaxWithdrawalAmount,
		&p.NoticePeriodDays, &p.MaxWithdrawalsPerMonth, &p.PartialWithdrawalAllowed,
		&p.LargeWithdrawalThreshold,
		&p.LockInMonths, &p.DefaultTermMonths, &p.MaturityAction,
		&p.Eligibility, &p.RequiresApprovalToOpen,
		&p.WithdrawalWindowStartMonth, &p.WithdrawalWindowEndMonth,
		&p.MaintenanceFee, &p.MaintenanceFeeFrequency,
		&p.EarlyWithdrawalPenaltyPct, &p.BelowMinBalanceFee, &p.DormancyFeeMonthly,
		&p.CreatedAt, &p.UpdatedAt, &p.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateTx inserts a product. tenant_id is supplied by the GUC (current_tenant_id()).
func (s *DepositProductStore) CreateTx(ctx context.Context, tx pgx.Tx, p *domain.DepositProduct) (*domain.DepositProduct, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO deposit_products (
			tenant_id, code, name, product_type, description, is_active,
			min_opening_balance, min_operating_balance, max_balance,
			min_deposit_amount, max_deposit_amount,
			min_withdrawal_amount, max_withdrawal_amount,
			notice_period_days, max_withdrawals_per_month, partial_withdrawal_allowed,
			large_withdrawal_threshold,
			lock_in_months, default_term_months, maturity_action,
			eligibility, requires_approval_to_open,
			withdrawal_window_start_month, withdrawal_window_end_month,
			maintenance_fee, maintenance_fee_frequency,
			early_withdrawal_penalty_pct, below_min_balance_fee, dormancy_fee_monthly,
			created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10,
			$11, $12,
			$13, $14, $15,
			$16,
			$17, $18, $19,
			$20, $21,
			$22, $23,
			$24, $25,
			$26, $27, $28,
			$29
		)
		RETURNING `+productCols,
		p.Code, p.Name, p.ProductType, p.Description, p.IsActive,
		p.MinOpeningBalance, p.MinOperatingBalance, p.MaxBalance,
		p.MinDepositAmount, p.MaxDepositAmount,
		p.MinWithdrawalAmount, p.MaxWithdrawalAmount,
		p.NoticePeriodDays, p.MaxWithdrawalsPerMonth, p.PartialWithdrawalAllowed,
		p.LargeWithdrawalThreshold,
		p.LockInMonths, p.DefaultTermMonths, p.MaturityAction,
		p.Eligibility, p.RequiresApprovalToOpen,
		p.WithdrawalWindowStartMonth, p.WithdrawalWindowEndMonth,
		p.MaintenanceFee, p.MaintenanceFeeFrequency,
		p.EarlyWithdrawalPenaltyPct, p.BelowMinBalanceFee, p.DormancyFeeMonthly,
		p.CreatedBy,
	)
	return scanProduct(row)
}

func (s *DepositProductStore) UpdateTx(ctx context.Context, tx pgx.Tx, p *domain.DepositProduct) (*domain.DepositProduct, error) {
	row := tx.QueryRow(ctx, `
		UPDATE deposit_products SET
			name = $2, description = $3, is_active = $4,
			min_opening_balance = $5, min_operating_balance = $6, max_balance = $7,
			min_deposit_amount = $8, max_deposit_amount = $9,
			min_withdrawal_amount = $10, max_withdrawal_amount = $11,
			notice_period_days = $12, max_withdrawals_per_month = $13, partial_withdrawal_allowed = $14,
			large_withdrawal_threshold = $15,
			lock_in_months = $16, default_term_months = $17, maturity_action = $18,
			eligibility = $19, requires_approval_to_open = $20,
			withdrawal_window_start_month = $21, withdrawal_window_end_month = $22,
			maintenance_fee = $23, maintenance_fee_frequency = $24,
			early_withdrawal_penalty_pct = $25, below_min_balance_fee = $26, dormancy_fee_monthly = $27
		WHERE id = $1
		RETURNING `+productCols,
		p.ID, p.Name, p.Description, p.IsActive,
		p.MinOpeningBalance, p.MinOperatingBalance, p.MaxBalance,
		p.MinDepositAmount, p.MaxDepositAmount,
		p.MinWithdrawalAmount, p.MaxWithdrawalAmount,
		p.NoticePeriodDays, p.MaxWithdrawalsPerMonth, p.PartialWithdrawalAllowed,
		p.LargeWithdrawalThreshold,
		p.LockInMonths, p.DefaultTermMonths, p.MaturityAction,
		p.Eligibility, p.RequiresApprovalToOpen,
		p.WithdrawalWindowStartMonth, p.WithdrawalWindowEndMonth,
		p.MaintenanceFee, p.MaintenanceFeeFrequency,
		p.EarlyWithdrawalPenaltyPct, p.BelowMinBalanceFee, p.DormancyFeeMonthly,
	)
	prod, err := scanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return prod, err
}

func (s *DepositProductStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DepositProduct, error) {
	row := tx.QueryRow(ctx, `SELECT `+productCols+` FROM deposit_products WHERE id = $1`, id)
	p, err := scanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *DepositProductStore) GetByCodeTx(ctx context.Context, tx pgx.Tx, code string) (*domain.DepositProduct, error) {
	row := tx.QueryRow(ctx, `SELECT `+productCols+` FROM deposit_products WHERE code = $1`, code)
	p, err := scanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *DepositProductStore) ListTx(ctx context.Context, tx pgx.Tx, includeInactive bool) ([]domain.DepositProduct, error) {
	q := `SELECT ` + productCols + ` FROM deposit_products`
	if !includeInactive {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY product_type, name`
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DepositProduct
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *DepositProductStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	// Refuse delete when accounts exist; archive via is_active = false instead.
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM deposit_accounts WHERE product_id = $1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("cannot delete product with %d existing account(s); deactivate it instead", n)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM deposit_products WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
