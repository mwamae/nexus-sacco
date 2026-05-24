// Loan application persistence, plus the gatherer that builds the
// per-member scoring inputs from members + shares + deposits + loans.

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

	"github.com/nexussacco/savings/internal/domain"
)

type LoanApplicationStore struct {
	pool *pgxpool.Pool
}

func NewLoanApplicationStore(pool *pgxpool.Pool) *LoanApplicationStore {
	return &LoanApplicationStore{pool: pool}
}

const loanAppCols = `
	id, tenant_id, application_no, counterparty_id, product_id, status,
	requested_amount, requested_term_months, purpose_category_id, purpose_note,
	preferred_disbursement_channel,
	employment_type, employer_name, employer_payroll_contact,
	monthly_net_income, other_income, monthly_expenses, monthly_existing_obligations,
	credit_score, risk_band, affordability_pass, dti_ratio, net_disposable_income,
	computed_max_amount, computed_max_installment,
	recommended_amount, recommended_term_months, scoring_details, scoring_flags, scored_at,
	workflow_instance_id,
	approved_amount, approved_term_months, approved_interest_rate_pct,
	approved_at, approved_by, approval_conditions,
	decline_category, decline_reason,
	offer_letter_path, offer_sent_at, offer_expires_at, offer_accepted_at,
	notes, created_at, updated_at, created_by
`

func scanApp(row pgx.Row) (*domain.LoanApplication, error) {
	var a domain.LoanApplication
	err := row.Scan(
		&a.ID, &a.TenantID, &a.ApplicationNo, &a.CounterpartyID, &a.ProductID, &a.Status,
		&a.RequestedAmount, &a.RequestedTermMonths, &a.PurposeCategoryID, &a.PurposeNote,
		&a.PreferredDisbursementChannel,
		&a.EmploymentType, &a.EmployerName, &a.EmployerPayrollContact,
		&a.MonthlyNetIncome, &a.OtherIncome, &a.MonthlyExpenses, &a.MonthlyExistingObligations,
		&a.CreditScore, &a.RiskBand, &a.AffordabilityPass, &a.DTIRatio, &a.NetDisposableIncome,
		&a.ComputedMaxAmount, &a.ComputedMaxInstallment,
		&a.RecommendedAmount, &a.RecommendedTermMonths, &a.ScoringDetails, &a.ScoringFlags, &a.ScoredAt,
		&a.WorkflowInstanceID,
		&a.ApprovedAmount, &a.ApprovedTermMonths, &a.ApprovedInterestRatePct,
		&a.ApprovedAt, &a.ApprovedBy, &a.ApprovalConditions,
		&a.DeclineCategory, &a.DeclineReason,
		&a.OfferLetterPath, &a.OfferSentAt, &a.OfferExpiresAt, &a.OfferAcceptedAt,
		&a.Notes, &a.CreatedAt, &a.UpdatedAt, &a.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateTx inserts a brand-new draft/pending_validation application.
// The application_no is generated using the shared sequence machinery.
func (s *LoanApplicationStore) CreateTx(ctx context.Context, tx pgx.Tx, in *domain.LoanApplication) (*domain.LoanApplication, error) {
	appNo, err := nextSeq(ctx, tx, "loan_application", "LA")
	if err != nil {
		return nil, err
	}
	in.ApplicationNo = appNo
	// Phase D sub-PR 3: in.CounterpartyID is a counterparty.id directly
	// (the URL contract and the frontend payload both carry counterparty.id).
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_applications (
			tenant_id, application_no, counterparty_id, product_id, status,
			requested_amount, requested_term_months,
			purpose_category_id, purpose_note, preferred_disbursement_channel,
			employment_type, employer_name, employer_payroll_contact,
			monthly_net_income, other_income, monthly_expenses, monthly_existing_obligations,
			notes, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5, $6,
			$7, $8, $9,
			$10, $11, $12,
			$13, $14, $15, $16,
			$17, $18
		)
		RETURNING `+loanAppCols,
		in.ApplicationNo, in.CounterpartyID, in.ProductID, string(in.Status),
		in.RequestedAmount, in.RequestedTermMonths,
		in.PurposeCategoryID, in.PurposeNote, in.PreferredDisbursementChannel,
		in.EmploymentType, in.EmployerName, in.EmployerPayrollContact,
		in.MonthlyNetIncome, in.OtherIncome, in.MonthlyExpenses, in.MonthlyExistingObligations,
		in.Notes, in.CreatedBy,
	)
	return scanApp(row)
}

func (s *LoanApplicationStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanApplication, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanAppCols+` FROM loan_applications WHERE id = $1`, id)
	a, err := scanApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

type AppListFilter struct {
	Status    string
	CounterpartyID  *uuid.UUID
	ProductID *uuid.UUID
	Q         string
	Limit     int
	Offset    int
}

type AppListItem struct {
	Application domain.LoanApplication `json:"application"`
	MemberNo    string                 `json:"member_no"`
	MemberName  string                 `json:"member_name"`
	ProductCode string                 `json:"product_code"`
	ProductName string                 `json:"product_name"`
}

func (s *LoanApplicationStore) ListTx(ctx context.Context, tx pgx.Tx, f AppListFilter) ([]AppListItem, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += fmt.Sprintf(" AND a.status = $%d", idx)
		args = append(args, f.Status)
		idx++
	}
	if f.CounterpartyID != nil {
		// Filter by the counterparty bridge (Phase D sub-PR 1) — the
		// caller still passes a members.id, but we resolve through the
		// indexed counterparty_id column.
		where += fmt.Sprintf(" AND a.counterparty_id = $%d", idx)
		args = append(args, *f.CounterpartyID)
		idx++
	}
	if f.ProductID != nil {
		where += fmt.Sprintf(" AND a.product_id = $%d", idx)
		args = append(args, *f.ProductID)
		idx++
	}
	if f.Q != "" {
		where += fmt.Sprintf(" AND (m.full_name ILIKE $%d OR m.member_no ILIKE $%d OR a.application_no ILIKE $%d)", idx, idx, idx)
		args = append(args, "%"+f.Q+"%")
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM loan_applications a JOIN members m ON m.counterparty_id = a.counterparty_id JOIN loan_products p ON p.id = a.product_id "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s, m.member_no, m.full_name, p.code, p.name
		FROM loan_applications a
		JOIN members m ON m.counterparty_id = a.counterparty_id
		JOIN loan_products p ON p.id = a.product_id
		%s
		ORDER BY a.created_at DESC
		LIMIT $%d OFFSET $%d
	`, prefixCols(loanAppCols, "a"), where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AppListItem
	for rows.Next() {
		var it AppListItem
		dest := []any{
			&it.Application.ID, &it.Application.TenantID, &it.Application.ApplicationNo,
			&it.Application.CounterpartyID, &it.Application.ProductID, &it.Application.Status,
			&it.Application.RequestedAmount, &it.Application.RequestedTermMonths,
			&it.Application.PurposeCategoryID, &it.Application.PurposeNote, &it.Application.PreferredDisbursementChannel,
			&it.Application.EmploymentType, &it.Application.EmployerName, &it.Application.EmployerPayrollContact,
			&it.Application.MonthlyNetIncome, &it.Application.OtherIncome,
			&it.Application.MonthlyExpenses, &it.Application.MonthlyExistingObligations,
			&it.Application.CreditScore, &it.Application.RiskBand, &it.Application.AffordabilityPass,
			&it.Application.DTIRatio, &it.Application.NetDisposableIncome,
			&it.Application.ComputedMaxAmount, &it.Application.ComputedMaxInstallment,
			&it.Application.RecommendedAmount, &it.Application.RecommendedTermMonths,
			&it.Application.ScoringDetails, &it.Application.ScoringFlags, &it.Application.ScoredAt,
			&it.Application.WorkflowInstanceID,
			&it.Application.ApprovedAmount, &it.Application.ApprovedTermMonths, &it.Application.ApprovedInterestRatePct,
			&it.Application.ApprovedAt, &it.Application.ApprovedBy, &it.Application.ApprovalConditions,
			&it.Application.DeclineCategory, &it.Application.DeclineReason,
			&it.Application.OfferLetterPath, &it.Application.OfferSentAt, &it.Application.OfferExpiresAt, &it.Application.OfferAcceptedAt,
			&it.Application.Notes, &it.Application.CreatedAt, &it.Application.UpdatedAt, &it.Application.CreatedBy,
			&it.MemberNo, &it.MemberName, &it.ProductCode, &it.ProductName,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// SaveScoringTx writes the scoring outputs onto the application + flips
// its status. Caller decides the next status (auto-approve / decline /
// pending_approval) based on the score result.
func (s *LoanApplicationStore) SaveScoringTx(
	ctx context.Context, tx pgx.Tx,
	appID uuid.UUID, result *domain.ScoreResult, scoringDetailsJSON, scoringFlagsJSON []byte, nextStatus domain.LoanAppStatus,
) (*domain.LoanApplication, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_applications SET
			status = $2,
			credit_score = $3,
			risk_band = $4,
			affordability_pass = $5,
			dti_ratio = $6,
			net_disposable_income = $7,
			computed_max_amount = $8,
			computed_max_installment = $9,
			recommended_amount = $10,
			recommended_term_months = $11,
			scoring_details = $12,
			scoring_flags = $13,
			scored_at = now()
		WHERE id = $1
		RETURNING `+loanAppCols,
		appID, string(nextStatus),
		result.OverallScore, result.RiskBand, result.AffordabilityPass,
		result.DTIRatio, result.NetDisposableIncome,
		result.ComputedMaxAmount, result.ComputedMaxInstallment,
		result.RecommendedAmount, result.RecommendedTermMonths,
		scoringDetailsJSON, scoringFlagsJSON,
	)
	return scanApp(row)
}

// SetStatusTx is a low-level setter for transitions that don't carry
// scoring data (approve / decline / cancel / move to offer / accept).
type AppTransition struct {
	To                  domain.LoanAppStatus
	By                  uuid.UUID
	ApprovedAmount      *decimal.Decimal
	ApprovedTermMonths  *int
	ApprovedInterestPct *decimal.Decimal
	ApprovalConditions  *string
	DeclineCategory     *string
	DeclineReason       *string
	OfferLetterPath     *string
	OfferExpiresAt      *time.Time
}

func (s *LoanApplicationStore) UpdateStatusTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID, t AppTransition) (*domain.LoanApplication, error) {
	fields := []string{"status = $2"}
	args := []any{appID, string(t.To)}
	idx := 3

	switch t.To {
	case domain.AppApproved, domain.AppApprovedWithConditions:
		fields = append(fields, fmt.Sprintf("approved_at = now(), approved_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.ApprovedAmount != nil {
			fields = append(fields, fmt.Sprintf("approved_amount = $%d", idx))
			args = append(args, *t.ApprovedAmount); idx++
		}
		if t.ApprovedTermMonths != nil {
			fields = append(fields, fmt.Sprintf("approved_term_months = $%d", idx))
			args = append(args, *t.ApprovedTermMonths); idx++
		}
		if t.ApprovedInterestPct != nil {
			fields = append(fields, fmt.Sprintf("approved_interest_rate_pct = $%d", idx))
			args = append(args, *t.ApprovedInterestPct); idx++
		}
		if t.ApprovalConditions != nil {
			fields = append(fields, fmt.Sprintf("approval_conditions = $%d", idx))
			args = append(args, *t.ApprovalConditions); idx++
		}
	case domain.AppDeclined:
		if t.DeclineCategory != nil {
			fields = append(fields, fmt.Sprintf("decline_category = $%d", idx))
			args = append(args, *t.DeclineCategory); idx++
		}
		if t.DeclineReason != nil {
			fields = append(fields, fmt.Sprintf("decline_reason = $%d", idx))
			args = append(args, *t.DeclineReason); idx++
		}
	case domain.AppOfferSent:
		fields = append(fields, "offer_sent_at = now()")
		if t.OfferLetterPath != nil {
			fields = append(fields, fmt.Sprintf("offer_letter_path = $%d", idx))
			args = append(args, *t.OfferLetterPath); idx++
		}
		if t.OfferExpiresAt != nil {
			fields = append(fields, fmt.Sprintf("offer_expires_at = $%d", idx))
			args = append(args, *t.OfferExpiresAt); idx++
		}
	case domain.AppOfferAccepted:
		fields = append(fields, "offer_accepted_at = now()")
	}
	q := fmt.Sprintf(`UPDATE loan_applications SET %s WHERE id = $1 RETURNING `+loanAppCols, joinFields(fields))
	row := tx.QueryRow(ctx, q, args...)
	out, err := scanApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

// ─────────── Scoring inputs gatherer ───────────

// GatherScoringInputsTx assembles the ScoringInputs struct for one
// member from the system's internal sources. The scorer is pure; this
// is the one place that hits the DB.
func (s *LoanApplicationStore) GatherScoringInputsTx(
	ctx context.Context, tx pgx.Tx,
	memberID, productID uuid.UUID,
) (*domain.ScoringInputs, error) {
	in := &domain.ScoringInputs{}

	// Member status + membership duration.
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT status::text, created_at FROM members WHERE id = $1
	`, memberID).Scan(&in.MemberStatus, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	in.MembershipMonths = int(time.Since(createdAt).Hours() / 24 / 30)

	// Shares.
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(a.shares_held, 0)
		FROM share_accounts a WHERE a.counterparty_id = $1
	`, memberID).Scan(&in.SharesHeld); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var parValue decimal.Decimal
	_ = tx.QueryRow(ctx, `SELECT share_par_value FROM tenant_operations`).Scan(&parValue)
	in.ShareCapital = decimal.NewFromInt(int64(in.SharesHeld)).Mul(parValue)

	// Deposit balance + 12-month inflow stats. PR 2: split the
	// single SUM into segment-keyed sums via a join on
	// deposit_products. The scorer's new BOSA-only basis depends on
	// distinguishing the regulatory bucket. Legacy bases still see
	// (BosaBalance + FosaBalance) when the BOSA_FOSA tenant flag is
	// off (preserving pre-PR-1 ceiling math); the scorer routes
	// that combination internally.
	_ = tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN dp.segment = 'bosa' THEN da.current_balance ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN dp.segment = 'fosa' THEN da.current_balance ELSE 0 END), 0)
		FROM deposit_accounts da
		JOIN deposit_products dp ON dp.id = da.product_id
		WHERE da.counterparty_id = $1 AND da.status = 'active'
	`, memberID).Scan(&in.BosaBalance, &in.FosaBalance)

	_ = tx.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(amount), 0)
		FROM deposit_transactions
		WHERE counterparty_id = $1
		  AND txn_type IN ('deposit', 'transfer_in', 'opening_balance')
		  AND posted_at > now() - INTERVAL '12 months'
	`, memberID).Scan(&in.DepositTxnCount12mo, &in.TotalDeposited12mo)
	if in.TotalDeposited12mo.GreaterThan(decimal.Zero) {
		in.AvgMonthlyDeposit = in.TotalDeposited12mo.Div(decimal.NewFromInt(12)).Round(2)
	}

	// Loan history.
	var writtenOff int
	_ = tx.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status IN ('active', 'in_arrears', 'restructured') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'in_arrears' OR days_past_due > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('active', 'in_arrears', 'restructured') AND product_id = $2 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'settled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'written_off' THEN 1 ELSE 0 END), 0)
		FROM loans WHERE counterparty_id = $1
	`, memberID, productID).Scan(
		&in.ActiveLoans, &in.ActiveLoansInArrears, &in.ActiveLoansSameProduct,
		&in.SettledLoans, &writtenOff,
	)
	in.HasWrittenOffLoan = writtenOff > 0
	// Cleanly-settled: settled with no recorded arrears. The DPD tracking
	// from Phase 6d will let us refine; for now, treat settled = clean.
	in.SettledLoansCleanly = in.SettledLoans

	return in, nil
}
