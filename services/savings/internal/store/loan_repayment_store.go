// Loan repayment + interest accrual + DPD recalculation (Phase 6d).
//
// All of these mutate the loan's cached balances + schedule rows. They
// run inside the tenant-bound pgx.Tx provided by the handler. Each is
// idempotent where it matters (interest accrual, DPD recalc).

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

// ─────────── Waterfall ───────────

// ParseWaterfall returns the components in the order tenant-configured.
// Unknown tokens are skipped silently. Defaults to ['penalty','interest','principal','fees'].
func ParseWaterfall(raw string) []string {
	if raw == "" {
		return []string{"penalty", "interest", "principal", "fees"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "penalty", "interest", "principal", "fees":
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"penalty", "interest", "principal", "fees"}
	}
	return out
}

// ─────────── Interest accrual ───────────

// AccrueDueInterestTx finds every schedule row in this loan whose
// due_date is on/before asOf and whose accrued_at is NULL, then posts
// an interest_accrual loan transaction and stamps the row. Idempotent.
// Cancelled schedule rows are skipped (those came from a previous
// rescheduling and are no longer authoritative).
//
// Returns the number of rows accrued.
func (s *LoanStore) AccrueDueInterestTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, asOf time.Time, userID uuid.UUID) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, installment_no, interest_due, fee_due
		FROM loan_repayment_schedule
		WHERE loan_id = $1
		  AND due_date <= $2
		  AND accrued_at IS NULL
		  AND status NOT IN ('paid', 'cancelled')
		ORDER BY installment_no
	`, loanID, asOf)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id          uuid.UUID
		installment int
		interest    decimal.Decimal
		fee         decimal.Decimal
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.installment, &p.interest, &p.fee); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	rows.Close()

	if len(todo) == 0 {
		return 0, nil
	}
	loan, err := s.GetTx(ctx, tx, loanID)
	if err != nil {
		return 0, err
	}
	accrued := 0
	for _, p := range todo {
		var txnID *uuid.UUID
		if p.interest.GreaterThan(decimal.Zero) {
			narration := fmt.Sprintf("Interest accrued · installment %d · %s", p.installment, loan.LoanNo)
			ch := "internal"
			t, err := s.PostTxnTx(ctx, tx, PostLoanInput{
				Loan:              loan,
				TxnType:           domain.LoanTxnInterestAccrual,
				Amount:            p.interest,
				InterestComponent: p.interest,
				Channel:           &ch,
				Narration:         &narration,
				InstallmentNo:     &p.installment,
				InitiatedBy:       userID,
			})
			if err != nil {
				return accrued, err
			}
			txnID = &t.ID
		}
		if p.fee.GreaterThan(decimal.Zero) {
			narration := fmt.Sprintf("Fee accrued · installment %d · %s", p.installment, loan.LoanNo)
			ch := "internal"
			if _, err := s.PostTxnTx(ctx, tx, PostLoanInput{
				Loan:           loan,
				TxnType:        domain.LoanTxnFeeCharge,
				Amount:         p.fee,
				FeeComponent:   p.fee,
				Channel:        &ch,
				Narration:      &narration,
				InstallmentNo:  &p.installment,
				InitiatedBy:    userID,
			}); err != nil {
				return accrued, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE loan_repayment_schedule
			   SET accrued_at = now(),
			       accrued_interest_txn_id = $2
			 WHERE id = $1
		`, p.id, txnID); err != nil {
			return accrued, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE loans
			   SET interest_charged = interest_charged + $2,
			       interest_balance = interest_balance + $2,
			       fees_charged     = fees_charged + $3,
			       fees_balance     = fees_balance + $3
			 WHERE id = $1
		`, loanID, p.interest, p.fee); err != nil {
			return accrued, err
		}
		accrued++
	}
	return accrued, nil
}

// ─────────── Repayment posting ───────────

type RepaymentInput struct {
	Loan        *domain.Loan
	Amount      decimal.Decimal
	Channel     string
	ChannelRef  string
	Narration   string
	ValueDate   time.Time
	InitiatedBy uuid.UUID
}

type RepaymentAllocation struct {
	Penalty   decimal.Decimal
	Interest  decimal.Decimal
	Principal decimal.Decimal
	Fees      decimal.Decimal
	Suspense  decimal.Decimal
}

func (a RepaymentAllocation) Total() decimal.Decimal {
	return a.Penalty.Add(a.Interest).Add(a.Principal).Add(a.Fees).Add(a.Suspense)
}

func (s *LoanStore) PostRepaymentTx(
	ctx context.Context, tx pgx.Tx,
	in RepaymentInput, waterfall []string,
) (*domain.LoanTransaction, *RepaymentAllocation, error) {
	if in.Loan == nil {
		return nil, nil, fmt.Errorf("repayment: loan is required")
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, fmt.Errorf("repayment: amount must be > 0")
	}
	loan := in.Loan
	if loan.Status != domain.LoanActive && loan.Status != domain.LoanInArrears && loan.Status != domain.LoanRestructured {
		return nil, nil, fmt.Errorf("%w (loan is %s)", domain.ErrLoanNotRepayable, loan.Status)
	}

	alloc := &RepaymentAllocation{}
	remaining := in.Amount

	for _, component := range waterfall {
		if remaining.LessThanOrEqual(decimal.Zero) {
			break
		}
		var cap decimal.Decimal
		switch component {
		case "penalty":
			cap = loan.PenaltyBalance
		case "interest":
			cap = loan.InterestBalance
		case "principal":
			cap = loan.PrincipalBalance
		case "fees":
			cap = loan.FeesBalance
		}
		if cap.LessThanOrEqual(decimal.Zero) {
			continue
		}
		amount := decMin(remaining, cap)
		switch component {
		case "penalty":
			alloc.Penalty = amount
		case "interest":
			if _, err := s.applyToSchedule(ctx, tx, loan.ID, "interest", amount); err != nil {
				return nil, nil, err
			}
			alloc.Interest = amount
		case "principal":
			if _, err := s.applyToSchedule(ctx, tx, loan.ID, "principal", amount); err != nil {
				return nil, nil, err
			}
			alloc.Principal = amount
		case "fees":
			if _, err := s.applyToSchedule(ctx, tx, loan.ID, "fees", amount); err != nil {
				return nil, nil, err
			}
			alloc.Fees = amount
		}
		remaining = remaining.Sub(amount)
	}
	if remaining.GreaterThan(decimal.Zero) {
		alloc.Suspense = remaining
	}

	if _, err := tx.Exec(ctx, `
		UPDATE loans SET
			penalty_paid     = penalty_paid    + $2,
			penalty_balance  = penalty_balance - $2,
			interest_paid    = interest_paid   + $3,
			interest_balance = interest_balance - $3,
			principal_repaid = principal_repaid + $4,
			principal_balance = principal_balance - $4,
			fees_paid        = fees_paid       + $5,
			fees_balance     = fees_balance    - $5,
			last_repayment_at = now()
		WHERE id = $1
	`, loan.ID, alloc.Penalty, alloc.Interest, alloc.Principal, alloc.Fees); err != nil {
		return nil, nil, err
	}

	narr := in.Narration
	if narr == "" {
		narr = "Repayment · " + in.Channel
	}
	ch := in.Channel
	cref := in.ChannelRef
	t, err := s.PostTxnTx(ctx, tx, PostLoanInput{
		Loan:               loan,
		TxnType:            domain.LoanTxnRepayment,
		Amount:             in.Amount.Neg(),
		PrincipalComponent: alloc.Principal,
		InterestComponent:  alloc.Interest,
		FeeComponent:       alloc.Fees,
		PenaltyComponent:   alloc.Penalty,
		Channel:            &ch,
		ChannelRef:         &cref,
		Narration:          &narr,
		InitiatedBy:        in.InitiatedBy,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.recomputeNextDueTx(ctx, tx, loan.ID); err != nil {
		return nil, nil, err
	}
	if _, err := s.RecalcDPDTx(ctx, tx, loan.ID, in.ValueDate); err != nil {
		return nil, nil, err
	}

	// Phase 5 follow-up — when this repayment settled the loan
	// (recomputeNextDueTx flips status='settled' once all schedule
	// rows are paid), release every still-committing guarantee tied
	// to it. The EXISTS guard means this is a no-op for partial
	// repayments that didn't actually close the loan.
	if _, err := tx.Exec(ctx, `
		UPDATE loan_guarantees
		   SET status      = 'released',
		       released_at = COALESCE(released_at, now()),
		       notes       = COALESCE(notes, '') ||
		                     CASE WHEN COALESCE(notes,'') = '' THEN '' ELSE E'\n' END ||
		                     '[released] loan settled by repayment ' || $2
		 WHERE loan_id = $1
		   AND status IN ('pending_consent','accepted','called_upon')
		   AND EXISTS (
		     SELECT 1 FROM loans WHERE id = $1 AND status IN ('settled','closed')
		   )
	`, loan.ID, t.ID); err != nil {
		return nil, nil, fmt.Errorf("release guarantees on settle: %w", err)
	}

	// Phase 4 — auto-resolve any open PTP on this loan against the new
	// repayment. Best-effort: collections store wired via SetCollections;
	// nil-safe so unit tests + tools that boot without the wire don't break.
	if s.collections != nil {
		resolved, err := s.collections.AutoResolveOpenPTPOnRepaymentTx(
			ctx, tx, loan.ID, t.ID, in.Amount, in.ValueDate,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("auto-resolve PTP: %w", err)
		}
		if resolved != nil && resolved.Status == domain.PTPKept {
			amtCopy := in.Amount
			_, _ = s.collections.LogEventTx(ctx, tx, loan.ID, &resolved.CaseID,
				domain.EventPTPKept, &in.InitiatedBy,
				[]byte(`{"ptp_id":"`+resolved.ID.String()+`","txn_id":"`+t.ID.String()+`"}`),
				nil, &amtCopy, nil,
			)
		}
	}
	return t, alloc, nil
}

func (s *LoanStore) applyToSchedule(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, component string, amount decimal.Decimal) (decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, installment_no,
		       principal_due, interest_due, fee_due,
		       principal_paid, interest_paid, fee_paid,
		       status
		FROM loan_repayment_schedule
		WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
		ORDER BY installment_no
	`, loanID)
	if err != nil {
		return decimal.Zero, err
	}
	type row struct {
		id                                     uuid.UUID
		installmentNo                          int
		principalDue, interestDue, feeDue      decimal.Decimal
		principalPaid, interestPaid, feePaid   decimal.Decimal
		status                                 string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.id, &r.installmentNo,
			&r.principalDue, &r.interestDue, &r.feeDue,
			&r.principalPaid, &r.interestPaid, &r.feePaid,
			&r.status,
		); err != nil {
			rows.Close()
			return decimal.Zero, err
		}
		todo = append(todo, r)
	}
	rows.Close()

	applied := decimal.Zero
	remaining := amount
	for _, r := range todo {
		if remaining.LessThanOrEqual(decimal.Zero) {
			break
		}
		var due, paid decimal.Decimal
		switch component {
		case "principal":
			due, paid = r.principalDue, r.principalPaid
		case "interest":
			due, paid = r.interestDue, r.interestPaid
		case "fees":
			due, paid = r.feeDue, r.feePaid
		}
		outstanding := due.Sub(paid)
		if outstanding.LessThanOrEqual(decimal.Zero) {
			continue
		}
		pay := decMin(remaining, outstanding)
		newPaid := paid.Add(pay)
		var newStatus string
		var newPaidAt *time.Time
		var totalDue = r.principalDue.Add(r.interestDue).Add(r.feeDue)
		var totalPaidAfter = r.principalPaid.Add(r.interestPaid).Add(r.feePaid).Add(pay)
		if totalPaidAfter.GreaterThanOrEqual(totalDue) {
			newStatus = "paid"
			now := time.Now()
			newPaidAt = &now
		} else if totalPaidAfter.GreaterThan(decimal.Zero) {
			newStatus = "partially_paid"
		} else {
			newStatus = r.status
		}
		var q string
		switch component {
		case "principal":
			q = `UPDATE loan_repayment_schedule SET principal_paid = $2, status = $3, paid_at = COALESCE(paid_at, $4) WHERE id = $1`
		case "interest":
			q = `UPDATE loan_repayment_schedule SET interest_paid = $2, status = $3, paid_at = COALESCE(paid_at, $4) WHERE id = $1`
		case "fees":
			q = `UPDATE loan_repayment_schedule SET fee_paid = $2, status = $3, paid_at = COALESCE(paid_at, $4) WHERE id = $1`
		}
		if _, err := tx.Exec(ctx, q, r.id, newPaid, newStatus, newPaidAt); err != nil {
			return applied, err
		}
		applied = applied.Add(pay)
		remaining = remaining.Sub(pay)
	}
	return applied, nil
}

// recomputeNextDueTx finds the earliest unpaid installment and stamps
// loans.next_installment_due_at / next_installment_amount accordingly.
// Cancelled rows are excluded (they came from a prior reschedule).
//
// Special case: when all balances are zero (full prepayment), every
// remaining unpaid row is snapped to paid and the loan flips to settled.
func (s *LoanStore) recomputeNextDueTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) error {
	loan, err := s.GetTx(ctx, tx, loanID)
	if err != nil {
		return err
	}
	fullyCleared := loan.PrincipalBalance.LessThanOrEqual(decimal.Zero) &&
		loan.InterestBalance.LessThanOrEqual(decimal.Zero) &&
		loan.FeesBalance.LessThanOrEqual(decimal.Zero) &&
		loan.PenaltyBalance.LessThanOrEqual(decimal.Zero)
	if fullyCleared {
		if _, err := tx.Exec(ctx, `
			UPDATE loan_repayment_schedule
			   SET status = 'paid',
			       paid_at = COALESCE(paid_at, now())
			 WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
		`, loanID); err != nil {
			return err
		}
		var totalCount int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM loan_repayment_schedule WHERE loan_id = $1 AND status != 'cancelled'`, loanID).Scan(&totalCount); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE loans
			   SET status = 'settled',
			       settled_at = COALESCE(settled_at, now()),
			       installments_paid = $2,
			       next_installment_due_at = NULL,
			       next_installment_amount = NULL,
			       days_past_due = 0,
			       arrears_classification = 'performing'
			 WHERE id = $1
		`, loanID, totalCount)
		return err
	}

	if loan.Status == domain.LoanSettled {
		if _, err := tx.Exec(ctx, `
			UPDATE loans SET status = 'active', settled_at = NULL WHERE id = $1
		`, loanID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE loan_repayment_schedule
			   SET status = CASE
			       WHEN principal_paid >= principal_due AND interest_paid >= interest_due AND fee_paid >= fee_due THEN 'paid'
			       WHEN principal_paid > 0 OR interest_paid > 0 OR fee_paid > 0 THEN 'partially_paid'
			       ELSE 'pending'
			   END,
			   paid_at = CASE
			       WHEN principal_paid >= principal_due AND interest_paid >= interest_due AND fee_paid >= fee_due THEN paid_at
			       ELSE NULL
			   END
			 WHERE loan_id = $1 AND status != 'cancelled'
		`, loanID); err != nil {
			return err
		}
	}
	var nextDate *time.Time
	var nextAmount *decimal.Decimal
	var paidCount int
	row := tx.QueryRow(ctx, `
		SELECT
		  (SELECT due_date FROM loan_repayment_schedule
		     WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
		     ORDER BY installment_no LIMIT 1),
		  (SELECT (total_due - principal_paid - interest_paid - fee_paid) FROM loan_repayment_schedule
		     WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
		     ORDER BY installment_no LIMIT 1),
		  (SELECT COUNT(*) FROM loan_repayment_schedule WHERE loan_id = $1 AND status = 'paid')
	`, loanID)
	if err := row.Scan(&nextDate, &nextAmount, &paidCount); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE loans
		   SET next_installment_due_at = $2,
		       next_installment_amount = $3,
		       installments_paid = $4
		 WHERE id = $1
	`, loanID, nextDate, nextAmount, paidCount)
	return err
}

// ─────────── DPD + classification ───────────

func ClassifyDPD(dpd, substandardDays, doubtfulDays, lossDays int) string {
	switch {
	case dpd <= 0:
		return "performing"
	case dpd < substandardDays:
		return "watch"
	case dpd < doubtfulDays:
		return "substandard"
	case dpd < lossDays:
		return "doubtful"
	default:
		return "loss"
	}
}

type DPDResult struct {
	LoanID          uuid.UUID
	DaysPastDue     int
	Classification  string
	StatusChanged   bool
	PreviousStatus  domain.LoanStatus
	NewStatus       domain.LoanStatus
}

func (s *LoanStore) RecalcDPDTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, asOf time.Time) (*DPDResult, error) {
	var sub, doub, loss int
	if err := tx.QueryRow(ctx, `
		SELECT dpd_substandard_days, dpd_doubtful_days, dpd_loss_days FROM tenant_operations
	`).Scan(&sub, &doub, &loss); err != nil {
		return nil, err
	}
	var earliestDue *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT MIN(due_date) FROM loan_repayment_schedule
		 WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled') AND due_date <= $2
	`, loanID, asOf).Scan(&earliestDue); err != nil {
		return nil, err
	}
	dpd := 0
	if earliestDue != nil {
		dpd = int(asOf.Sub(*earliestDue).Hours() / 24)
		if dpd < 0 {
			dpd = 0
		}
	}
	classification := ClassifyDPD(dpd, sub, doub, loss)

	loan, err := s.GetTx(ctx, tx, loanID)
	if err != nil {
		return nil, err
	}
	res := &DPDResult{
		LoanID: loanID, DaysPastDue: dpd, Classification: classification,
		PreviousStatus: loan.Status, NewStatus: loan.Status,
	}
	switch loan.Status {
	case domain.LoanActive, domain.LoanInArrears:
		if dpd > 0 {
			res.NewStatus = domain.LoanInArrears
		} else {
			res.NewStatus = domain.LoanActive
		}
	}
	res.StatusChanged = res.NewStatus != res.PreviousStatus

	if _, err := tx.Exec(ctx, `
		UPDATE loans
		   SET days_past_due = $2,
		       arrears_classification = $3,
		       status = $4,
		       last_arrears_calc_at = now()
		 WHERE id = $1
	`, loanID, dpd, classification, string(res.NewStatus)); err != nil {
		return nil, err
	}

	// Bridge to the collections queue. Runs from EVERY caller of
	// RecalcDPDTx now, not just the nightly cron — repayment-posting,
	// repayment-reversal, and the manual /recalc-dpd endpoint all
	// trigger it too. EnsureCaseForLoanTx is idempotent on (loan_id +
	// open status), so re-firing for a loan that's already enqueued
	// is a no-op. Recovery (auto-close on dpd == 0) is still owned by
	// the cron's AfterDPDRecalcTx — only opening lives here, because
	// only opening is the bug that strands loans off the queue.
	if s.collections != nil && dpd > 0 {
		// Re-read after the UPDATE so EnsureCaseForLoanTx sees the
		// fresh dpd + classification when stamping the case.
		freshLoan, gerr := s.GetTx(ctx, tx, loanID)
		if gerr != nil {
			return nil, gerr
		}
		if _, gerr := s.collections.EnsureCaseForLoanTx(ctx, tx, freshLoan); gerr != nil {
			return nil, gerr
		}
	}
	return res, nil
}

// LoansForDPDRecalcTx returns every loan id eligible for daily DPD
// recompute (active / in_arrears / restructured). The CLI iterates this.
func (s *LoanStore) LoansForDPDRecalcTx(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM loans
		WHERE status IN ('active', 'in_arrears', 'restructured')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─────────── Reversal ───────────

func (s *LoanStore) ReverseRepaymentTx(
	ctx context.Context, tx pgx.Tx,
	txnID uuid.UUID, reason string, userID uuid.UUID,
) (*domain.LoanTransaction, error) {
	orig, err := s.GetTxnTx(ctx, tx, txnID)
	if err != nil {
		return nil, err
	}
	if orig.TxnType != domain.LoanTxnRepayment {
		return nil, fmt.Errorf("only repayment transactions can be reversed (got %s)", orig.TxnType)
	}
	if orig.ReversedByTxnID != nil {
		return nil, fmt.Errorf("transaction already reversed")
	}
	loan, err := s.GetTx(ctx, tx, orig.LoanID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET
			penalty_paid      = penalty_paid    - $2,
			penalty_balance   = penalty_balance + $2,
			interest_paid     = interest_paid   - $3,
			interest_balance  = interest_balance + $3,
			principal_repaid  = principal_repaid - $4,
			principal_balance = principal_balance + $4,
			fees_paid         = fees_paid       - $5,
			fees_balance      = fees_balance    + $5
		WHERE id = $1
	`, loan.ID, orig.PenaltyComponent, orig.InterestComponent,
		orig.PrincipalComponent, orig.FeeComponent); err != nil {
		return nil, err
	}
	if err := s.reverseScheduleAllocation(ctx, tx, orig.LoanID, orig.PrincipalComponent, "principal"); err != nil {
		return nil, err
	}
	if err := s.reverseScheduleAllocation(ctx, tx, orig.LoanID, orig.InterestComponent, "interest"); err != nil {
		return nil, err
	}
	if err := s.reverseScheduleAllocation(ctx, tx, orig.LoanID, orig.FeeComponent, "fees"); err != nil {
		return nil, err
	}
	narration := "Reversal of " + orig.TxnNo + " · " + reason
	ch := "internal"
	rev, err := s.PostTxnTx(ctx, tx, PostLoanInput{
		Loan:               loan,
		TxnType:            domain.LoanTxnReversal,
		Amount:             orig.Amount.Neg(),
		PrincipalComponent: orig.PrincipalComponent.Neg(),
		InterestComponent:  orig.InterestComponent.Neg(),
		FeeComponent:       orig.FeeComponent.Neg(),
		PenaltyComponent:   orig.PenaltyComponent.Neg(),
		Channel:            &ch,
		Narration:          &narration,
		ReversesTxnID:      &orig.ID,
		InitiatedBy:        userID,
		AuthorizedBy:       &userID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE loan_transactions SET reversed_by_txn_id = $2 WHERE id = $1`, orig.ID, rev.ID); err != nil {
		return nil, err
	}
	if err := s.recomputeNextDueTx(ctx, tx, orig.LoanID); err != nil {
		return nil, err
	}
	if _, err := s.RecalcDPDTx(ctx, tx, orig.LoanID, time.Now()); err != nil {
		return nil, err
	}
	return rev, nil
}

func (s *LoanStore) reverseScheduleAllocation(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, amount decimal.Decimal, component string) error {
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	var paidCol string
	switch component {
	case "principal":
		paidCol = "principal_paid"
	case "interest":
		paidCol = "interest_paid"
	case "fees":
		paidCol = "fee_paid"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT id, principal_due, interest_due, fee_due,
		       principal_paid, interest_paid, fee_paid, status
		FROM loan_repayment_schedule
		WHERE loan_id = $1 AND %s > 0 AND status != 'cancelled'
		ORDER BY installment_no DESC
	`, paidCol), loanID)
	if err != nil {
		return err
	}
	type row struct {
		id                                          uuid.UUID
		principalDue, interestDue, feeDue           decimal.Decimal
		principalPaid, interestPaid, feePaid        decimal.Decimal
		status                                      string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.id, &r.principalDue, &r.interestDue, &r.feeDue,
			&r.principalPaid, &r.interestPaid, &r.feePaid, &r.status,
		); err != nil {
			rows.Close()
			return err
		}
		rs = append(rs, r)
	}
	rows.Close()
	remaining := amount
	for _, r := range rs {
		if remaining.LessThanOrEqual(decimal.Zero) {
			break
		}
		var compPaid decimal.Decimal
		switch component {
		case "principal":
			compPaid = r.principalPaid
		case "interest":
			compPaid = r.interestPaid
		case "fees":
			compPaid = r.feePaid
		}
		take := decMin(remaining, compPaid)
		newPaid := compPaid.Sub(take)
		var totalPaidAfter decimal.Decimal
		switch component {
		case "principal":
			totalPaidAfter = newPaid.Add(r.interestPaid).Add(r.feePaid)
		case "interest":
			totalPaidAfter = r.principalPaid.Add(newPaid).Add(r.feePaid)
		case "fees":
			totalPaidAfter = r.principalPaid.Add(r.interestPaid).Add(newPaid)
		}
		newStatus := "pending"
		if totalPaidAfter.GreaterThan(decimal.Zero) {
			newStatus = "partially_paid"
		}
		var q string
		switch component {
		case "principal":
			q = `UPDATE loan_repayment_schedule SET principal_paid = $2, status = $3, paid_at = CASE WHEN status='paid' AND $3 != 'paid' THEN NULL ELSE paid_at END WHERE id = $1`
		case "interest":
			q = `UPDATE loan_repayment_schedule SET interest_paid = $2, status = $3, paid_at = CASE WHEN status='paid' AND $3 != 'paid' THEN NULL ELSE paid_at END WHERE id = $1`
		case "fees":
			q = `UPDATE loan_repayment_schedule SET fee_paid = $2, status = $3, paid_at = CASE WHEN status='paid' AND $3 != 'paid' THEN NULL ELSE paid_at END WHERE id = $1`
		}
		if _, err := tx.Exec(ctx, q, r.id, newPaid, newStatus); err != nil {
			return err
		}
		remaining = remaining.Sub(take)
	}
	return nil
}

// ─────────── Helpers ───────────

func decMin(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

func (s *LoanStore) GetWaterfallTx(ctx context.Context, tx pgx.Tx) ([]string, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT loan_repayment_waterfall FROM tenant_operations`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ParseWaterfall(""), nil
	}
	if err != nil {
		return nil, err
	}
	return ParseWaterfall(raw), nil
}

func (s *LoanStore) PayoffFigureTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) (decimal.Decimal, error) {
	l, err := s.GetTx(ctx, tx, loanID)
	if err != nil {
		return decimal.Zero, err
	}
	return l.PenaltyBalance.Add(l.InterestBalance).Add(l.PrincipalBalance).Add(l.FeesBalance), nil
}

func (s *LoanStore) GetTxnTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanTransaction, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, loan_id, counterparty_id, txn_no, txn_type,
		       amount, principal_component, interest_component, fee_component, penalty_component,
		       value_date, channel, channel_ref, narration,
		       reverses_txn_id, reversed_by_txn_id, installment_no,
		       posted_at, initiated_by, authorized_by
		FROM loan_transactions WHERE id = $1
	`, id)
	var t domain.LoanTransaction
	err := row.Scan(
		&t.ID, &t.TenantID, &t.LoanID, &t.CounterpartyID, &t.TxnNo, &t.TxnType,
		&t.Amount, &t.PrincipalComponent, &t.InterestComponent, &t.FeeComponent, &t.PenaltyComponent,
		&t.ValueDate, &t.Channel, &t.ChannelRef, &t.Narration,
		&t.ReversesTxnID, &t.ReversedByTxnID, &t.InstallmentNo,
		&t.PostedAt, &t.InitiatedBy, &t.AuthorizedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// ─────────── Tenant-wide arrears summary ───────────

type ArrearsBand struct {
	Classification   string          `json:"classification"`
	LoanCount        int             `json:"loan_count"`
	TotalOutstanding decimal.Decimal `json:"total_outstanding"`
}

func (s *LoanStore) ArrearsSummaryTx(ctx context.Context, tx pgx.Tx) ([]ArrearsBand, error) {
	rows, err := tx.Query(ctx, `
		SELECT arrears_classification,
		       COUNT(*),
		       COALESCE(SUM(principal_balance + interest_balance + fees_balance + penalty_balance), 0)
		FROM loans
		WHERE status IN ('active', 'in_arrears', 'restructured')
		GROUP BY arrears_classification
		ORDER BY
		  CASE arrears_classification
		    WHEN 'performing'  THEN 1
		    WHEN 'watch'       THEN 2
		    WHEN 'substandard' THEN 3
		    WHEN 'doubtful'    THEN 4
		    WHEN 'loss'        THEN 5
		    ELSE 6
		  END
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArrearsBand
	for rows.Next() {
		var b ArrearsBand
		if err := rows.Scan(&b.Classification, &b.LoanCount, &b.TotalOutstanding); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
