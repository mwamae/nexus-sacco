// Loan product configuration — CRUD against loan_products plus a
// thin helper for the per-tenant purpose category list.

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

type LoanProductStore struct {
	pool *pgxpool.Pool
}

func NewLoanProductStore(pool *pgxpool.Pool) *LoanProductStore {
	return &LoanProductStore{pool: pool}
}

const loanProductCols = `
	id, tenant_id, code, name, category, description, is_active,
	min_amount, max_amount, multiplier_basis, multiplier_value,
	min_term_months, max_term_months, default_term_months, grace_period_months,
	interest_rate_pct, interest_method, repayment_method,
	processing_fee, processing_fee_is_pct, processing_fee_timing,
	insurance_fee, insurance_fee_is_pct, insurance_fee_timing,
	appraisal_fee, appraisal_fee_is_pct, appraisal_fee_timing,
	penalty_rate_pct,
	min_guarantors, max_guarantor_exposure_pct, guarantor_must_be_member,
	collateral_requirement,
	min_membership_months, min_shares_required, allow_concurrent,
	workflow_definition_code, auto_approval_threshold, auto_approval_min_score,
	allow_topup, allow_refinance,
	created_at, updated_at, created_by
`

func scanLoanProduct(row pgx.Row) (*domain.LoanProduct, error) {
	var p domain.LoanProduct
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Category, &p.Description, &p.IsActive,
		&p.MinAmount, &p.MaxAmount, &p.MultiplierBasis, &p.MultiplierValue,
		&p.MinTermMonths, &p.MaxTermMonths, &p.DefaultTermMonths, &p.GracePeriodMonths,
		&p.InterestRatePct, &p.InterestMethod, &p.RepaymentMethod,
		&p.ProcessingFee, &p.ProcessingFeeIsPct, &p.ProcessingFeeTiming,
		&p.InsuranceFee, &p.InsuranceFeeIsPct, &p.InsuranceFeeTiming,
		&p.AppraisalFee, &p.AppraisalFeeIsPct, &p.AppraisalFeeTiming,
		&p.PenaltyRatePct,
		&p.MinGuarantors, &p.MaxGuarantorExposurePct, &p.GuarantorMustBeMember,
		&p.CollateralRequirement,
		&p.MinMembershipMonths, &p.MinSharesRequired, &p.AllowConcurrent,
		&p.WorkflowDefinitionCode, &p.AutoApprovalThreshold, &p.AutoApprovalMinScore,
		&p.AllowTopup, &p.AllowRefinance,
		&p.CreatedAt, &p.UpdatedAt, &p.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *LoanProductStore) CreateTx(ctx context.Context, tx pgx.Tx, p *domain.LoanProduct) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_products (
			tenant_id, code, name, category, description, is_active,
			min_amount, max_amount, multiplier_basis, multiplier_value,
			min_term_months, max_term_months, default_term_months, grace_period_months,
			interest_rate_pct, interest_method, repayment_method,
			processing_fee, processing_fee_is_pct, processing_fee_timing,
			insurance_fee, insurance_fee_is_pct, insurance_fee_timing,
			appraisal_fee, appraisal_fee_is_pct, appraisal_fee_timing,
			penalty_rate_pct,
			min_guarantors, max_guarantor_exposure_pct, guarantor_must_be_member,
			collateral_requirement,
			min_membership_months, min_shares_required, allow_concurrent,
			workflow_definition_code, auto_approval_threshold, auto_approval_min_score,
			allow_topup, allow_refinance,
			created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $16,
			$17, $18, $19,
			$20, $21, $22,
			$23, $24, $25,
			$26,
			$27, $28, $29,
			$30,
			$31, $32, $33,
			$34, $35, $36,
			$37, $38,
			$39
		)
		RETURNING `+loanProductCols,
		p.Code, p.Name, p.Category, p.Description, p.IsActive,
		p.MinAmount, p.MaxAmount, p.MultiplierBasis, p.MultiplierValue,
		p.MinTermMonths, p.MaxTermMonths, p.DefaultTermMonths, p.GracePeriodMonths,
		p.InterestRatePct, p.InterestMethod, p.RepaymentMethod,
		p.ProcessingFee, p.ProcessingFeeIsPct, p.ProcessingFeeTiming,
		p.InsuranceFee, p.InsuranceFeeIsPct, p.InsuranceFeeTiming,
		p.AppraisalFee, p.AppraisalFeeIsPct, p.AppraisalFeeTiming,
		p.PenaltyRatePct,
		p.MinGuarantors, p.MaxGuarantorExposurePct, p.GuarantorMustBeMember,
		p.CollateralRequirement,
		p.MinMembershipMonths, p.MinSharesRequired, p.AllowConcurrent,
		p.WorkflowDefinitionCode, p.AutoApprovalThreshold, p.AutoApprovalMinScore,
		p.AllowTopup, p.AllowRefinance,
		p.CreatedBy,
	)
	return scanLoanProduct(row)
}

func (s *LoanProductStore) UpdateTx(ctx context.Context, tx pgx.Tx, p *domain.LoanProduct) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_products SET
			name = $2, category = $3, description = $4, is_active = $5,
			min_amount = $6, max_amount = $7, multiplier_basis = $8, multiplier_value = $9,
			min_term_months = $10, max_term_months = $11, default_term_months = $12, grace_period_months = $13,
			interest_rate_pct = $14, interest_method = $15, repayment_method = $16,
			processing_fee = $17, processing_fee_is_pct = $18, processing_fee_timing = $19,
			insurance_fee = $20, insurance_fee_is_pct = $21, insurance_fee_timing = $22,
			appraisal_fee = $23, appraisal_fee_is_pct = $24, appraisal_fee_timing = $25,
			penalty_rate_pct = $26,
			min_guarantors = $27, max_guarantor_exposure_pct = $28, guarantor_must_be_member = $29,
			collateral_requirement = $30,
			min_membership_months = $31, min_shares_required = $32, allow_concurrent = $33,
			workflow_definition_code = $34, auto_approval_threshold = $35, auto_approval_min_score = $36,
			allow_topup = $37, allow_refinance = $38
		WHERE id = $1
		RETURNING `+loanProductCols,
		p.ID,
		p.Name, p.Category, p.Description, p.IsActive,
		p.MinAmount, p.MaxAmount, p.MultiplierBasis, p.MultiplierValue,
		p.MinTermMonths, p.MaxTermMonths, p.DefaultTermMonths, p.GracePeriodMonths,
		p.InterestRatePct, p.InterestMethod, p.RepaymentMethod,
		p.ProcessingFee, p.ProcessingFeeIsPct, p.ProcessingFeeTiming,
		p.InsuranceFee, p.InsuranceFeeIsPct, p.InsuranceFeeTiming,
		p.AppraisalFee, p.AppraisalFeeIsPct, p.AppraisalFeeTiming,
		p.PenaltyRatePct,
		p.MinGuarantors, p.MaxGuarantorExposurePct, p.GuarantorMustBeMember,
		p.CollateralRequirement,
		p.MinMembershipMonths, p.MinSharesRequired, p.AllowConcurrent,
		p.WorkflowDefinitionCode, p.AutoApprovalThreshold, p.AutoApprovalMinScore,
		p.AllowTopup, p.AllowRefinance,
	)
	out, err := scanLoanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

func (s *LoanProductStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanProductCols+` FROM loan_products WHERE id = $1`, id)
	p, err := scanLoanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *LoanProductStore) ListTx(ctx context.Context, tx pgx.Tx, includeInactive bool) ([]domain.LoanProduct, error) {
	q := `SELECT ` + loanProductCols + ` FROM loan_products`
	if !includeInactive {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY category, name`
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanProduct
	for rows.Next() {
		p, err := scanLoanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *LoanProductStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM loan_applications WHERE product_id = $1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("cannot delete loan product with %d existing application(s); deactivate it instead", n)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM loan_products WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────── Purpose categories ───────────

func (s *LoanProductStore) ListPurposeCategoriesTx(ctx context.Context, tx pgx.Tx, includeInactive bool) ([]domain.LoanPurposeCategory, error) {
	q := `SELECT id, tenant_id, code, name, is_active, created_at FROM loan_purpose_categories`
	if !includeInactive {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY name`
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanPurposeCategory
	for rows.Next() {
		var c domain.LoanPurposeCategory
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Code, &c.Name, &c.IsActive, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *LoanProductStore) CreatePurposeCategoryTx(ctx context.Context, tx pgx.Tx, c *domain.LoanPurposeCategory) (*domain.LoanPurposeCategory, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_purpose_categories (tenant_id, code, name, is_active)
		VALUES (current_tenant_id(), $1, $2, COALESCE($3, true))
		RETURNING id, tenant_id, code, name, is_active, created_at
	`, c.Code, c.Name, c.IsActive)
	var out domain.LoanPurposeCategory
	if err := row.Scan(&out.ID, &out.TenantID, &out.Code, &out.Name, &out.IsActive, &out.CreatedAt); err != nil {
		return nil, err
	}
	return &out, nil
}
