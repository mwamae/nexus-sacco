// Loan restructuring store — reschedule + moratorium fully built out;
// topup / refinance / settlement_discount have lighter implementations
// (audit row + minimal state changes; full origination cycle for top-up
// flows through the existing application path).
//
// Each function returns the loan_restructurings audit row.

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

type LoanRestructureStore struct {
	pool *pgxpool.Pool
}

func NewLoanRestructureStore(pool *pgxpool.Pool) *LoanRestructureStore {
	return &LoanRestructureStore{pool: pool}
}

const restructureCols = `
	id, tenant_id, loan_id, kind, reason,
	previous_principal_balance, previous_interest_balance,
	previous_term_months, previous_interest_rate_pct, previous_repayment_method, previous_status,
	new_term_months, new_interest_rate_pct,
	topup_amount, refinance_new_loan_id,
	moratorium_months, moratorium_suspend_interest,
	discount_amount, discount_writeoff_txn_id,
	workflow_instance_id, authorized_at, authorized_by,
	created_at, created_by
`

func scanRestructuring(row pgx.Row) (*domain.LoanRestructuring, error) {
	var r domain.LoanRestructuring
	err := row.Scan(
		&r.ID, &r.TenantID, &r.LoanID, &r.Kind, &r.Reason,
		&r.PreviousPrincipalBalance, &r.PreviousInterestBalance,
		&r.PreviousTermMonths, &r.PreviousInterestRatePct, &r.PreviousRepaymentMethod, &r.PreviousStatus,
		&r.NewTermMonths, &r.NewInterestRatePct,
		&r.TopupAmount, &r.RefinanceNewLoanID,
		&r.MoratoriumMonths, &r.MoratoriumSuspendInterest,
		&r.DiscountAmount, &r.DiscountWriteoffTxnID,
		&r.WorkflowInstanceID, &r.AuthorizedAt, &r.AuthorizedBy,
		&r.CreatedAt, &r.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ─────────── Reschedule ───────────

// RescheduleTx re-amortises the remaining outstanding principal over a
// new term (and optionally a new interest rate). The existing schedule
// rows are archived (status='cancelled') and a fresh schedule is
// generated starting from a new first-due-date.
//
// The loan's principal_balance is preserved; only the schedule changes.
type RescheduleInput struct {
	LoanID            uuid.UUID
	NewTermMonths     int
	NewInterestRatePct *decimal.Decimal
	NewFirstDueDate   *time.Time
	Reason            string
	By                uuid.UUID
}

func (s *LoanRestructureStore) RescheduleTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	in RescheduleInput,
) (*domain.LoanRestructuring, *domain.Loan, error) {
	if in.NewTermMonths <= 0 {
		return nil, nil, domain.ErrRescheduleTermInvalid
	}
	loan, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	if loan.Status != domain.LoanActive && loan.Status != domain.LoanInArrears && loan.Status != domain.LoanRestructured {
		return nil, nil, domain.ErrRestructureNotAllowed
	}

	// Capture previous snapshot.
	rate := loan.InterestRatePct
	if in.NewInterestRatePct != nil {
		rate = *in.NewInterestRatePct
	}
	firstDue := time.Now().AddDate(0, 1, 0)
	if in.NewFirstDueDate != nil {
		firstDue = *in.NewFirstDueDate
	}

	// Generate the new schedule against the outstanding principal.
	newSchedule := domain.GenerateSchedule(
		loan.PrincipalBalance, rate,
		in.NewTermMonths, 0, // grace not re-applied on reschedule
		firstDue.AddDate(0, -1, 0), // GenerateSchedule adds (grace+1) → cancel that to land on firstDue
		loan.InterestMethod, loan.RepaymentMethod,
	)

	// Archive the existing schedule (mark unpaid rows cancelled).
	if _, err := tx.Exec(ctx, `
		UPDATE loan_repayment_schedule SET status = 'cancelled'
		WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
	`, in.LoanID); err != nil {
		return nil, nil, err
	}

	// Insert the new rows. Renumber to start above the archived max so
	// the (loan_id, installment_no) unique constraint isn't violated.
	var startNo int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(installment_no), 0) FROM loan_repayment_schedule WHERE loan_id = $1`, in.LoanID).Scan(&startNo); err != nil {
		return nil, nil, err
	}
	for i, row := range newSchedule {
		_, err := tx.Exec(ctx, `
			INSERT INTO loan_repayment_schedule (
				tenant_id, loan_id, installment_no, due_date,
				principal_due, interest_due, fee_due, total_due, outstanding_after
			) VALUES (
				current_tenant_id(), $1, $2, $3,
				$4, $5, $6, $7, $8
			)
		`, in.LoanID, startNo+i+1, row.DueDate,
			row.PrincipalDue, row.InterestDue, row.FeeDue, row.TotalDue, row.OutstandingAfter)
		if err != nil {
			return nil, nil, fmt.Errorf("insert rescheduled row: %w", err)
		}
	}

	// Update loan: term, rate, status='restructured', next-due refreshed.
	firstAmount := decimal.Zero
	if len(newSchedule) > 0 {
		firstAmount = newSchedule[0].TotalDue
	}
	row := tx.QueryRow(ctx, `
		UPDATE loans
		   SET term_months = $2,
		       interest_rate_pct = $3,
		       installment_count = $2,
		       first_due_date = $4,
		       next_installment_due_at = $4,
		       next_installment_amount = $5,
		       status = 'restructured',
		       days_past_due = 0,
		       arrears_classification = 'performing'
		 WHERE id = $1
		 RETURNING `+loanCols,
		in.LoanID, in.NewTermMonths, rate, firstDue, firstAmount)
	updatedLoan, err := scanLoan(row)
	if err != nil {
		return nil, nil, err
	}

	// Audit row.
	prevTerm := loan.TermMonths
	prevRate := loan.InterestRatePct
	prevMethod := loan.RepaymentMethod
	prevStatus := loan.Status
	rec, err := s.insertRestructuringTx(ctx, tx, &domain.LoanRestructuring{
		LoanID:                   in.LoanID,
		Kind:                     domain.RestructureReschedule,
		Reason:                   in.Reason,
		PreviousPrincipalBalance: &loan.PrincipalBalance,
		PreviousInterestBalance:  &loan.InterestBalance,
		PreviousTermMonths:       &prevTerm,
		PreviousInterestRatePct:  &prevRate,
		PreviousRepaymentMethod:  &prevMethod,
		PreviousStatus:           &prevStatus,
		NewTermMonths:            &in.NewTermMonths,
		NewInterestRatePct:       &rate,
		AuthorizedBy:             &in.By,
		CreatedBy:                in.By,
	})
	if err != nil {
		return nil, nil, err
	}
	return rec, updatedLoan, nil
}

// ─────────── Moratorium ───────────

// MoratoriumTx grants a payment holiday for N months: pushes every
// remaining unpaid installment's due_date forward by N months, updates
// next_installment_due_at, and flips status to 'restructured'.
//
// If suspendInterest is true, also stamps accrued_at on the deferred
// rows (skipping interest accrual during the holiday). Default is false
// (interest continues to accrue during the moratorium).
type MoratoriumInput struct {
	LoanID            uuid.UUID
	MoratoriumMonths  int
	SuspendInterest   bool
	Reason            string
	By                uuid.UUID
}

func (s *LoanRestructureStore) MoratoriumTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	in MoratoriumInput,
) (*domain.LoanRestructuring, *domain.Loan, error) {
	if in.MoratoriumMonths <= 0 {
		return nil, nil, domain.ErrMoratoriumMonthsInvalid
	}
	loan, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	if loan.Status != domain.LoanActive && loan.Status != domain.LoanInArrears && loan.Status != domain.LoanRestructured {
		return nil, nil, domain.ErrRestructureNotAllowed
	}

	// Push every unpaid installment forward.
	months := in.MoratoriumMonths
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE loan_repayment_schedule
		   SET due_date = due_date + INTERVAL '%d months'
		 WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
	`, months), in.LoanID); err != nil {
		return nil, nil, err
	}
	// If suspending interest during the moratorium, stamp accrued_at on
	// the now-deferred rows so the DPD job won't re-accrue them when
	// their original due_date passes.
	if in.SuspendInterest {
		if _, err := tx.Exec(ctx, `
			UPDATE loan_repayment_schedule
			   SET accrued_at = COALESCE(accrued_at, now())
			 WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
		`, in.LoanID); err != nil {
			return nil, nil, err
		}
	}
	// Recompute next-due + flip status.
	var nextDate *time.Time
	var nextAmount *decimal.Decimal
	row := tx.QueryRow(ctx, `
		SELECT
		  (SELECT due_date FROM loan_repayment_schedule WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled') ORDER BY installment_no LIMIT 1),
		  (SELECT (total_due - principal_paid - interest_paid - fee_paid) FROM loan_repayment_schedule WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled') ORDER BY installment_no LIMIT 1)
	`, in.LoanID)
	if err := row.Scan(&nextDate, &nextAmount); err != nil {
		return nil, nil, err
	}
	updateRow := tx.QueryRow(ctx, `
		UPDATE loans SET
			status = 'restructured',
			next_installment_due_at = $2,
			next_installment_amount = $3,
			days_past_due = 0,
			arrears_classification = 'performing'
		 WHERE id = $1
		 RETURNING `+loanCols, in.LoanID, nextDate, nextAmount)
	updatedLoan, err := scanLoan(updateRow)
	if err != nil {
		return nil, nil, err
	}
	// Audit row.
	prevTerm := loan.TermMonths
	prevRate := loan.InterestRatePct
	prevMethod := loan.RepaymentMethod
	prevStatus := loan.Status
	rec, err := s.insertRestructuringTx(ctx, tx, &domain.LoanRestructuring{
		LoanID:                    in.LoanID,
		Kind:                      domain.RestructureMoratorium,
		Reason:                    in.Reason,
		PreviousPrincipalBalance:  &loan.PrincipalBalance,
		PreviousInterestBalance:   &loan.InterestBalance,
		PreviousTermMonths:        &prevTerm,
		PreviousInterestRatePct:   &prevRate,
		PreviousRepaymentMethod:   &prevMethod,
		PreviousStatus:            &prevStatus,
		MoratoriumMonths:          &in.MoratoriumMonths,
		MoratoriumSuspendInterest: &in.SuspendInterest,
		AuthorizedBy:              &in.By,
		CreatedBy:                 in.By,
	})
	if err != nil {
		return nil, nil, err
	}
	return rec, updatedLoan, nil
}

// ─────────── Settlement discount ───────────

// SettlementDiscountTx accepts `discount_amount` less than the
// outstanding payoff as full and final. Posts a write_off transaction
// for the discounted amount, marks the loan settled.
type SettlementDiscountInput struct {
	LoanID         uuid.UUID
	DiscountAmount decimal.Decimal // amount being written off (forgiven)
	Reason         string
	By             uuid.UUID
}

func (s *LoanRestructureStore) SettlementDiscountTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	in SettlementDiscountInput,
) (*domain.LoanRestructuring, *domain.Loan, error) {
	if in.DiscountAmount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, fmt.Errorf("discount amount must be > 0")
	}
	loan, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	if loan.Status != domain.LoanActive && loan.Status != domain.LoanInArrears && loan.Status != domain.LoanRestructured {
		return nil, nil, domain.ErrRestructureNotAllowed
	}
	// Post a write_off transaction for the discount.
	narration := "Settlement discount · " + in.Reason
	ch := "internal"
	wo, err := loans.PostTxnTx(ctx, tx, PostLoanInput{
		Loan:               loan,
		TxnType:            domain.LoanTxnSettlementDiscount,
		Amount:             in.DiscountAmount.Neg(),
		PrincipalComponent: in.DiscountAmount,
		Channel:            &ch,
		Narration:          &narration,
		InitiatedBy:        in.By,
		AuthorizedBy:       &in.By,
	})
	if err != nil {
		return nil, nil, err
	}
	// Reduce all loan balances proportionally, marking the loan as settled.
	// Simplest path: zero out all balances; the discount txn captures the audit.
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET
			principal_balance = 0, interest_balance = 0, fees_balance = 0, penalty_balance = 0,
			status = 'settled', settled_at = now(),
			days_past_due = 0, arrears_classification = 'performing',
			next_installment_due_at = NULL, next_installment_amount = NULL
		 WHERE id = $1
	`, in.LoanID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE loan_repayment_schedule SET status='paid', paid_at = COALESCE(paid_at, now())
		WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
	`, in.LoanID); err != nil {
		return nil, nil, err
	}
	updated, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	prevStatus := loan.Status
	rec, err := s.insertRestructuringTx(ctx, tx, &domain.LoanRestructuring{
		LoanID:                   in.LoanID,
		Kind:                     domain.RestructureSettlementDiscount,
		Reason:                   in.Reason,
		PreviousPrincipalBalance: &loan.PrincipalBalance,
		PreviousInterestBalance:  &loan.InterestBalance,
		PreviousStatus:           &prevStatus,
		DiscountAmount:           &in.DiscountAmount,
		DiscountWriteoffTxnID:    &wo.ID,
		AuthorizedBy:             &in.By,
		CreatedBy:                in.By,
	})
	if err != nil {
		return nil, nil, err
	}
	return rec, updated, nil
}

// ─────────── Top-up / Refinance — light implementation ───────────
//
// Both are "create-a-new-loan" patterns that depend on the full
// application + scoring + offer + disburse pipeline. For Phase 6e we
// record the intent + log the audit row, and the operator drives the
// new-loan creation through the standard application UI. A subsequent
// phase will collapse this into a single guided flow.

func (s *LoanRestructureStore) RecordTopupIntentTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	loanID uuid.UUID, topupAmount decimal.Decimal, reason string, by uuid.UUID,
) (*domain.LoanRestructuring, error) {
	loan, err := loans.GetTx(ctx, tx, loanID)
	if err != nil {
		return nil, err
	}
	if loan.Status != domain.LoanActive {
		return nil, domain.ErrRestructureNotAllowed
	}
	prevStatus := loan.Status
	return s.insertRestructuringTx(ctx, tx, &domain.LoanRestructuring{
		LoanID:                   loanID,
		Kind:                     domain.RestructureTopup,
		Reason:                   reason,
		PreviousPrincipalBalance: &loan.PrincipalBalance,
		PreviousStatus:           &prevStatus,
		TopupAmount:              &topupAmount,
		AuthorizedBy:             &by,
		CreatedBy:                by,
	})
}

func (s *LoanRestructureStore) RecordRefinanceIntentTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	loanID, newLoanID uuid.UUID, reason string, by uuid.UUID,
) (*domain.LoanRestructuring, error) {
	loan, err := loans.GetTx(ctx, tx, loanID)
	if err != nil {
		return nil, err
	}
	prevStatus := loan.Status
	// Close the old loan once refinance is recorded.
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET status = 'closed', closed_at = now()
		WHERE id = $1
	`, loanID); err != nil {
		return nil, err
	}
	return s.insertRestructuringTx(ctx, tx, &domain.LoanRestructuring{
		LoanID:                   loanID,
		Kind:                     domain.RestructureRefinance,
		Reason:                   reason,
		PreviousPrincipalBalance: &loan.PrincipalBalance,
		PreviousStatus:           &prevStatus,
		RefinanceNewLoanID:       &newLoanID,
		AuthorizedBy:             &by,
		CreatedBy:                by,
	})
}

// ─────────── Read helpers ───────────

func (s *LoanRestructureStore) ByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) ([]domain.LoanRestructuring, error) {
	rows, err := tx.Query(ctx, `SELECT `+restructureCols+` FROM loan_restructurings WHERE loan_id = $1 ORDER BY created_at DESC`, loanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanRestructuring
	for rows.Next() {
		r, err := scanRestructuring(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *LoanRestructureStore) insertRestructuringTx(ctx context.Context, tx pgx.Tx, in *domain.LoanRestructuring) (*domain.LoanRestructuring, error) {
	authorizedAt := time.Now()
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_restructurings (
			tenant_id, loan_id, kind, reason,
			previous_principal_balance, previous_interest_balance,
			previous_term_months, previous_interest_rate_pct, previous_repayment_method, previous_status,
			new_term_months, new_interest_rate_pct,
			topup_amount, refinance_new_loan_id,
			moratorium_months, moratorium_suspend_interest,
			discount_amount, discount_writeoff_txn_id,
			authorized_at, authorized_by, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3,
			$4, $5,
			$6, $7, $8, $9,
			$10, $11,
			$12, $13,
			$14, $15,
			$16, $17,
			$18, $19, $20
		)
		RETURNING `+restructureCols,
		in.LoanID, string(in.Kind), in.Reason,
		in.PreviousPrincipalBalance, in.PreviousInterestBalance,
		in.PreviousTermMonths, in.PreviousInterestRatePct,
		ptrStrOrNil(stringOfRepayMethod(in.PreviousRepaymentMethod)), ptrStrOrNil(stringOfStatus(in.PreviousStatus)),
		in.NewTermMonths, in.NewInterestRatePct,
		in.TopupAmount, in.RefinanceNewLoanID,
		in.MoratoriumMonths, in.MoratoriumSuspendInterest,
		in.DiscountAmount, in.DiscountWriteoffTxnID,
		authorizedAt, in.AuthorizedBy, in.CreatedBy,
	)
	return scanRestructuring(row)
}

// Small helpers to flatten enum-typed pointers for the INSERT.
func stringOfRepayMethod(m *domain.LoanRepaymentMethod) *string {
	if m == nil {
		return nil
	}
	s := string(*m)
	return &s
}
func stringOfStatus(s *domain.LoanStatus) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}
func ptrStrOrNil(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

// ─────────── Sentinel for caller convenience ───────────

var ErrRestructureNotAllowed = errors.New("loan is not in a state that permits restructuring")
