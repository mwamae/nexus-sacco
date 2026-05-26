// Loan-repayment executor. Applies a pre-computed Allocation against
// one loan row in the same tenant tx the caller opened, posts the
// matching loan_transactions row, and queues the GL outbox entry.
//
// IMPORTANT — what this executor does NOT do (yet):
//   • Doesn't walk loan_repayment_schedule to mark installments paid.
//     The savings handlers' repayment endpoint does that today; the
//     mpesa-driven path lands the money in the right balance columns
//     but doesn't refresh per-installment status. A follow-up job
//     (phase 4 or a savings cron) reconciles the schedule.
//   • Doesn't recompute next_installment_* fields.
//   • Doesn't re-run the DPD classification — last_arrears_calc_at
//     stays unchanged; the daily DPD batch picks the row up on its
//     next pass.
//
// This was a deliberate scope decision (see phase 3.5 Step-0 brief).
// The shared write contract is the loan_transactions row + the
// updated balance columns; everything else is post-hoc.

package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/finance/posting"
	"github.com/nexussacco/finance/types"
)

// RepayLoanTx is the externally-stable entry point. Callers (savings
// handler + mpesa applier) hand over the (tenant, loan, allocation)
// triple; the executor reads the loan FOR UPDATE, applies the
// component balances atomically, writes the loan_transactions row,
// and queues the GL outbox entry.
func RepayLoanTx(ctx context.Context, tx pgx.Tx, in types.RepayLoanInput) (*types.Result, error) {
	if in.LoanID == uuid.Nil || in.TenantID == uuid.Nil {
		return nil, errors.New("finance/executor: tenant_id + loan_id required")
	}
	total := in.Allocation.Total()
	if total.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("finance/executor: allocation must sum > 0")
	}

	// 1. Lock the loan row + read what we need.
	var (
		counterpartyID, productID uuid.UUID
		penaltyBal, interestBal   decimal.Decimal
		principalBal, feesBal     decimal.Decimal
		status                    string
	)
	err := tx.QueryRow(ctx, `
		SELECT counterparty_id, product_id, status::text,
		       COALESCE(penalty_balance, 0), COALESCE(interest_balance, 0),
		       COALESCE(principal_balance, 0), COALESCE(fees_balance, 0)
		  FROM loans WHERE id = $1
		   FOR UPDATE
	`, in.LoanID).Scan(&counterpartyID, &productID, &status,
		&penaltyBal, &interestBal, &principalBal, &feesBal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("finance/executor: loan %s not found", in.LoanID)
		}
		return nil, fmt.Errorf("read loan: %w", err)
	}
	if status != "active" && status != "in_arrears" && status != "restructured" {
		return nil, fmt.Errorf("finance/executor: loan status %q does not accept repayments", status)
	}

	// 2. Reject allocations that exceed component balances. The
	// engine should already cap these via the balance lookups, but
	// the executor is the last line of defence — a stale Plan
	// (e.g. balance changed between plan + apply) must not
	// overpay any single component.
	if in.Allocation.Penalty.GreaterThan(penaltyBal) {
		return nil, fmt.Errorf("finance/executor: penalty %s exceeds balance %s",
			in.Allocation.Penalty.StringFixed(2), penaltyBal.StringFixed(2))
	}
	if in.Allocation.Interest.GreaterThan(interestBal) {
		return nil, fmt.Errorf("finance/executor: interest %s exceeds balance %s",
			in.Allocation.Interest.StringFixed(2), interestBal.StringFixed(2))
	}
	if in.Allocation.Principal.GreaterThan(principalBal) {
		return nil, fmt.Errorf("finance/executor: principal %s exceeds balance %s",
			in.Allocation.Principal.StringFixed(2), principalBal.StringFixed(2))
	}
	if in.Allocation.Fees.GreaterThan(feesBal) {
		return nil, fmt.Errorf("finance/executor: fees %s exceeds balance %s",
			in.Allocation.Fees.StringFixed(2), feesBal.StringFixed(2))
	}

	// 3. Apply balance updates atomically.
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET
			penalty_paid      = penalty_paid      + $2,
			penalty_balance   = penalty_balance   - $2,
			interest_paid     = interest_paid     + $3,
			interest_balance  = interest_balance  - $3,
			principal_repaid  = principal_repaid  + $4,
			principal_balance = principal_balance - $4,
			fees_paid         = fees_paid         + $5,
			fees_balance      = fees_balance      - $5,
			last_repayment_at = now()
		 WHERE id = $1
	`, in.LoanID, in.Allocation.Penalty, in.Allocation.Interest,
		in.Allocation.Principal, in.Allocation.Fees); err != nil {
		return nil, fmt.Errorf("update loan balances: %w", err)
	}

	// 4. Write the loan_transactions row. Negative amount marks
	// it as a repayment (same convention savings uses). txn_no is
	// NOT NULL with no default — we synthesize a tenant-scoped
	// identifier from the current date + the new row's uuid.
	narr := in.Narration
	if narr == "" {
		narr = "Repayment · " + in.Channel
	}
	valueDate := in.ValueDate
	if valueDate.IsZero() {
		valueDate = time.Now().UTC()
	}
	var txnID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO loan_transactions (
			tenant_id, loan_id, counterparty_id,
			txn_no, txn_type, amount,
			principal_component, interest_component, fee_component, penalty_component,
			value_date, channel, channel_ref, narration,
			initiated_by, external_validation_ref
		) VALUES (
			$1, $2, $3,
			'LTX-' || to_char(now(), 'YYYYMMDD') || '-' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 8),
			'repayment', $4,
			$5, $6, $7, $8,
			$9, NULLIF($10,''), NULLIF($11,''), NULLIF($12,''),
			$13, NULLIF($14,'')
		)
		RETURNING id
	`, in.TenantID, in.LoanID, counterpartyID, total.Neg(),
		in.Allocation.Principal, in.Allocation.Interest, in.Allocation.Fees, in.Allocation.Penalty,
		valueDate, in.Channel, in.ChannelRef, narr,
		in.InitiatedBy, in.ExternalValidationRef,
	).Scan(&txnID); err != nil {
		return nil, fmt.Errorf("insert loan_transactions: %w", err)
	}

	// 5. Queue the GL outbox row. DR cash channel; CR loan principal /
	// interest receivable / fees / penalty per component. Account codes
	// come from a future loan-product mapping — for phase 3.5 we use
	// the platform-default codes documented in CoA.
	outboxID, err := postRepaymentGL(ctx, tx, in, txnID, valueDate)
	if err != nil {
		return nil, err
	}
	return &types.Result{TxnID: txnID, OutboxID: outboxID}, nil
}

// postRepaymentGL writes the GL outbox row for a repayment. Two-line
// minimum: DR cash (or M-PESA clearing), CR loan principal. Multi-
// component repayments fan out to multiple CR lines.
func postRepaymentGL(
	ctx context.Context, tx pgx.Tx,
	in types.RepayLoanInput, txnID uuid.UUID, valueDate time.Time,
) (uuid.UUID, error) {
	// Account-code defaults match the platform CoA. Real CoA per-
	// product mapping lands in phase 4.
	const (
		cashClearing    = "1099" // M-PESA clearing; phase 3.5 moves out of clearing into the receivables
		loanPrincipal   = "1200"
		loanInterest    = "4100" // interest income
		loanFees        = "4200" // loan fees income
		loanPenalty     = "4250" // penalty income
	)
	lines := []posting.Line{}
	total := in.Allocation.Total()
	lines = append(lines, posting.Line{
		AccountCode: cashClearing, Debit: total,
		Narration: "M-PESA clearing → loan repayment",
	})
	if !in.Allocation.Principal.IsZero() {
		lines = append(lines, posting.Line{AccountCode: loanPrincipal, Credit: in.Allocation.Principal, Narration: "Loan principal"})
	}
	if !in.Allocation.Interest.IsZero() {
		lines = append(lines, posting.Line{AccountCode: loanInterest, Credit: in.Allocation.Interest, Narration: "Loan interest"})
	}
	if !in.Allocation.Fees.IsZero() {
		lines = append(lines, posting.Line{AccountCode: loanFees, Credit: in.Allocation.Fees, Narration: "Loan fees"})
	}
	if !in.Allocation.Penalty.IsZero() {
		lines = append(lines, posting.Line{AccountCode: loanPenalty, Credit: in.Allocation.Penalty, Narration: "Loan penalty"})
	}
	srcRef := txnID.String()
	if in.ExternalValidationRef != "" {
		srcRef = in.ExternalValidationRef + ":" + txnID.String()
	}
	return posting.PostTx(ctx, tx, posting.Input{
		TenantID:     in.TenantID,
		EntryDate:    valueDate,
		ValueDate:    valueDate,
		SourceModule: "finance.executor.repay_loan",
		SourceRef:    srcRef,
		Narration:    "Loan repayment · " + in.Channel,
		Lines:        lines,
	})
}
