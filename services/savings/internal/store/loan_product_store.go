// Loan product configuration — CRUD against loan_products + the
// per-product fee list in loan_product_fees, plus a thin helper for
// the per-tenant purpose category list.

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
	penalty_rate_pct,
	min_guarantors, max_guarantor_exposure_pct, guarantor_must_be_member,
	collateral_requirement,
	security_model, min_guarantor_cover_pct, min_collateral_cover_pct, accepted_collateral_kinds,
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
		&p.PenaltyRatePct,
		&p.MinGuarantors, &p.MaxGuarantorExposurePct, &p.GuarantorMustBeMember,
		&p.CollateralRequirement,
		&p.SecurityModel, &p.MinGuarantorCoverPct, &p.MinCollateralCoverPct, &p.AcceptedCollateralKinds,
		&p.MinMembershipMonths, &p.MinSharesRequired, &p.AllowConcurrent,
		&p.WorkflowDefinitionCode, &p.AutoApprovalThreshold, &p.AutoApprovalMinScore,
		&p.AllowTopup, &p.AllowRefinance,
		&p.CreatedAt, &p.UpdatedAt, &p.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	p.Fees = []domain.LoanProductFee{}
	return &p, nil
}

func (s *LoanProductStore) CreateTx(ctx context.Context, tx pgx.Tx, p *domain.LoanProduct) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_products (
			tenant_id, code, name, category, description, is_active,
			min_amount, max_amount, multiplier_basis, multiplier_value,
			min_term_months, max_term_months, default_term_months, grace_period_months,
			interest_rate_pct, interest_method, repayment_method,
			penalty_rate_pct,
			min_guarantors, max_guarantor_exposure_pct, guarantor_must_be_member,
			collateral_requirement,
			security_model, min_guarantor_cover_pct, min_collateral_cover_pct, accepted_collateral_kinds,
			min_membership_months, min_shares_required, allow_concurrent,
			workflow_definition_code, auto_approval_threshold, auto_approval_min_score,
			allow_topup, allow_refinance,
			created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $16,
			$17,
			$18, $19, $20,
			$21,
			$22, $23, $24, $25,
			$26, $27, $28,
			$29, $30, $31,
			$32, $33,
			$34
		)
		RETURNING `+loanProductCols,
		p.Code, p.Name, p.Category, p.Description, p.IsActive,
		p.MinAmount, p.MaxAmount, p.MultiplierBasis, p.MultiplierValue,
		p.MinTermMonths, p.MaxTermMonths, p.DefaultTermMonths, p.GracePeriodMonths,
		p.InterestRatePct, p.InterestMethod, p.RepaymentMethod,
		p.PenaltyRatePct,
		p.MinGuarantors, p.MaxGuarantorExposurePct, p.GuarantorMustBeMember,
		p.CollateralRequirement,
		p.SecurityModel, p.MinGuarantorCoverPct, p.MinCollateralCoverPct, p.AcceptedCollateralKinds,
		p.MinMembershipMonths, p.MinSharesRequired, p.AllowConcurrent,
		p.WorkflowDefinitionCode, p.AutoApprovalThreshold, p.AutoApprovalMinScore,
		p.AllowTopup, p.AllowRefinance,
		p.CreatedBy,
	)
	created, err := scanLoanProduct(row)
	if err != nil {
		return nil, err
	}
	if err := s.ReplaceFeesTx(ctx, tx, created.ID, p.Fees); err != nil {
		return nil, err
	}
	fees, err := s.FeesByProductTx(ctx, tx, created.ID)
	if err != nil {
		return nil, err
	}
	created.Fees = fees
	return created, nil
}

func (s *LoanProductStore) UpdateTx(ctx context.Context, tx pgx.Tx, p *domain.LoanProduct) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_products SET
			name = $2, category = $3, description = $4, is_active = $5,
			min_amount = $6, max_amount = $7, multiplier_basis = $8, multiplier_value = $9,
			min_term_months = $10, max_term_months = $11, default_term_months = $12, grace_period_months = $13,
			interest_rate_pct = $14, interest_method = $15, repayment_method = $16,
			penalty_rate_pct = $17,
			min_guarantors = $18, max_guarantor_exposure_pct = $19, guarantor_must_be_member = $20,
			collateral_requirement = $21,
			security_model = $22, min_guarantor_cover_pct = $23,
			min_collateral_cover_pct = $24, accepted_collateral_kinds = $25,
			min_membership_months = $26, min_shares_required = $27, allow_concurrent = $28,
			workflow_definition_code = $29, auto_approval_threshold = $30, auto_approval_min_score = $31,
			allow_topup = $32, allow_refinance = $33,
			updated_at = now()
		WHERE id = $1
		RETURNING `+loanProductCols,
		p.ID,
		p.Name, p.Category, p.Description, p.IsActive,
		p.MinAmount, p.MaxAmount, p.MultiplierBasis, p.MultiplierValue,
		p.MinTermMonths, p.MaxTermMonths, p.DefaultTermMonths, p.GracePeriodMonths,
		p.InterestRatePct, p.InterestMethod, p.RepaymentMethod,
		p.PenaltyRatePct,
		p.MinGuarantors, p.MaxGuarantorExposurePct, p.GuarantorMustBeMember,
		p.CollateralRequirement,
		p.SecurityModel, p.MinGuarantorCoverPct, p.MinCollateralCoverPct, p.AcceptedCollateralKinds,
		p.MinMembershipMonths, p.MinSharesRequired, p.AllowConcurrent,
		p.WorkflowDefinitionCode, p.AutoApprovalThreshold, p.AutoApprovalMinScore,
		p.AllowTopup, p.AllowRefinance,
	)
	out, err := scanLoanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.ReplaceFeesTx(ctx, tx, out.ID, p.Fees); err != nil {
		return nil, err
	}
	fees, err := s.FeesByProductTx(ctx, tx, out.ID)
	if err != nil {
		return nil, err
	}
	out.Fees = fees
	return out, nil
}

func (s *LoanProductStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanProduct, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanProductCols+` FROM loan_products WHERE id = $1`, id)
	p, err := scanLoanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	fees, err := s.FeesByProductTx(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	p.Fees = fees
	return p, nil
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
	out := []domain.LoanProduct{}
	for rows.Next() {
		p, err := scanLoanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate fees in a single query keyed by product_id, then fan out.
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(out))
	for _, p := range out {
		ids = append(ids, p.ID)
	}
	feeMap, err := s.feesByProductsTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if fs, ok := feeMap[out[i].ID]; ok {
			out[i].Fees = fs
		} else {
			out[i].Fees = []domain.LoanProductFee{}
		}
	}
	return out, nil
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

// ─────────── Fee sub-table ───────────

const loanProductFeeCols = `
	id, product_id, name, amount, is_pct, timing, display_order, gl_credit_code, created_at, updated_at
`

func scanLoanProductFee(row pgx.Row) (*domain.LoanProductFee, error) {
	var f domain.LoanProductFee
	err := row.Scan(
		&f.ID, &f.ProductID, &f.Name, &f.Amount, &f.IsPct, &f.Timing, &f.DisplayOrder, &f.GLCreditCode,
		&f.CreatedAt, &f.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *LoanProductStore) FeesByProductTx(ctx context.Context, tx pgx.Tx, productID uuid.UUID) ([]domain.LoanProductFee, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+loanProductFeeCols+` FROM loan_product_fees WHERE product_id = $1 ORDER BY display_order, name`,
		productID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.LoanProductFee{}
	for rows.Next() {
		f, err := scanLoanProductFee(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *LoanProductStore) feesByProductsTx(ctx context.Context, tx pgx.Tx, productIDs []uuid.UUID) (map[uuid.UUID][]domain.LoanProductFee, error) {
	out := make(map[uuid.UUID][]domain.LoanProductFee, len(productIDs))
	rows, err := tx.Query(ctx,
		`SELECT `+loanProductFeeCols+` FROM loan_product_fees WHERE product_id = ANY($1) ORDER BY product_id, display_order, name`,
		productIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanLoanProductFee(rows)
		if err != nil {
			return nil, err
		}
		out[f.ProductID] = append(out[f.ProductID], *f)
	}
	return out, rows.Err()
}

// ReplaceFeesTx deletes existing fees on the product and inserts the new
// list. Display order is taken from the slice position so the caller
// controls ordering implicitly. Empty `fees` is fine — the product is
// left with no fees, matching the user request to allow zero fees.
func (s *LoanProductStore) ReplaceFeesTx(ctx context.Context, tx pgx.Tx, productID uuid.UUID, fees []domain.LoanProductFee) error {
	if _, err := tx.Exec(ctx, `DELETE FROM loan_product_fees WHERE product_id = $1`, productID); err != nil {
		return err
	}
	for i, f := range fees {
		if f.Name == "" {
			return fmt.Errorf("fee at position %d has no name", i)
		}
		if f.Timing == "" {
			f.Timing = domain.FeeUpfront
		}
		if f.GLCreditCode == "" {
			// Same fallback as the migration 0034 default — keeps API
			// callers that haven't been updated to send the code from
			// blocking the insert.
			f.GLCreditCode = "4010"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO loan_product_fees (
				tenant_id, product_id, name, amount, is_pct, timing, display_order, gl_credit_code
			) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7)
		`, productID, f.Name, f.Amount, f.IsPct, string(f.Timing), i+1, f.GLCreditCode); err != nil {
			return err
		}
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
