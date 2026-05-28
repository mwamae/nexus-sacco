// Maker-checker executors for loan repayment / settlement / reversal
// (stage 3). Mirror the existing Repay / Settle / Reverse inline paths
// so the dispatcher can replay them at approval time.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/postingops"
	"github.com/nexussacco/savings/internal/store"
)

// ─────────── Payloads ───────────

type LoanRepaymentPayload struct {
	LoanID           uuid.UUID       `json:"loan_id"`
	Amount           decimal.Decimal `json:"amount"`
	Channel          string          `json:"channel"`
	ChannelRef       string          `json:"channel_ref"`
	Narration        string          `json:"narration"`
	ValueDate        string          `json:"value_date,omitempty"`
	DebitSavingsAcct *uuid.UUID      `json:"debit_savings_account_id,omitempty"`
}

type LoanSettlePayload struct {
	LoanID           uuid.UUID  `json:"loan_id"`
	Channel          string     `json:"channel"`
	ChannelRef       string     `json:"channel_ref"`
	Narration        string     `json:"narration"`
	DebitSavingsAcct *uuid.UUID `json:"debit_savings_account_id,omitempty"`
}

type LoanReversePayload struct {
	TxnID  uuid.UUID `json:"txn_id"`
	Reason string    `json:"reason"`
}

// ─────────── Result shapes ───────────

type LoanRepayResult struct {
	Transaction domain.LoanTransaction    `json:"transaction"`
	Allocation  store.RepaymentAllocation `json:"allocation"`
	Loan        domain.Loan               `json:"loan"`
	DPD         *store.DPDResult          `json:"dpd"`
}

type LoanReverseResult struct {
	Reversal domain.LoanTransaction `json:"reversal"`
	Loan     domain.Loan            `json:"loan"`
}

// ─────────── Executors ───────────

func (h *LoanRepaymentHandler) ExecuteRepaymentTx(
	ctx context.Context, tx pgx.Tx,
	p LoanRepaymentPayload, makerID uuid.UUID,
) (*LoanRepayResult, error) {
	valueDate := time.Now()
	if p.ValueDate != "" {
		d, err := time.Parse("2006-01-02", p.ValueDate)
		if err != nil {
			return nil, httpx.ErrBadRequest("value_date must be YYYY-MM-DD")
		}
		valueDate = d
	}
	loan, err := h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	if _, err := h.Loans.AccrueDueInterestTx(ctx, tx, p.LoanID, valueDate, makerID); err != nil {
		return nil, err
	}
	loan, err = h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	if p.Channel == "auto_savings" {
		if p.DebitSavingsAcct == nil {
			return nil, httpx.ErrBadRequest("debit_savings_account_id is required for channel='auto_savings'")
		}
		acct, err := h.Deposits.GetAccountTx(ctx, tx, *p.DebitSavingsAcct)
		if err != nil {
			return nil, err
		}
		if acct.CounterpartyID != loan.CounterpartyID {
			return nil, httpx.ErrBadRequest("savings account does not belong to this member")
		}
		if acct.AvailableBalance.LessThan(p.Amount) {
			return nil, domain.ErrInsufficientBalance
		}
		narration := "Loan repayment · " + loan.LoanNo
		internal := domain.DepChannelInternal
		depTxn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnWithdrawal,
			Amount:      p.Amount,
			Channel:     &internal,
			Narration:   &narration,
			InitiatedBy: makerID,
		})
		if err != nil {
			return nil, err
		}
		if p.ChannelRef == "" {
			p.ChannelRef = depTxn.TxnNo
		}
	}
	waterfall, err := h.Loans.GetWaterfallTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	txn, alloc, err := h.Loans.PostRepaymentTx(ctx, tx, store.RepaymentInput{
		Loan: loan, Amount: p.Amount,
		Channel: p.Channel, ChannelRef: p.ChannelRef, Narration: p.Narration,
		ValueDate: valueDate, InitiatedBy: makerID,
	}, waterfall)
	if err != nil {
		return nil, err
	}
	dpd, err := h.Loans.RecalcDPDTx(ctx, tx, p.LoanID, valueDate)
	if err != nil {
		return nil, err
	}
	updated, err := h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	return &LoanRepayResult{Transaction: *txn, Allocation: *alloc, Loan: *updated, DPD: dpd}, nil
}

func (h *LoanRepaymentHandler) ExecuteSettleTx(
	ctx context.Context, tx pgx.Tx,
	p LoanSettlePayload, makerID uuid.UUID,
) (*LoanRepayResult, error) {
	if _, err := h.Loans.AccrueDueInterestTx(ctx, tx, p.LoanID, time.Now(), makerID); err != nil {
		return nil, err
	}
	loan, err := h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	payoff := loan.PenaltyBalance.Add(loan.InterestBalance).Add(loan.PrincipalBalance).Add(loan.FeesBalance)
	if payoff.LessThanOrEqual(decimal.Zero) {
		return nil, httpx.ErrConflict("loan has no outstanding balance — already settled?")
	}
	if p.Channel == "auto_savings" {
		if p.DebitSavingsAcct == nil {
			return nil, httpx.ErrBadRequest("debit_savings_account_id is required for channel='auto_savings'")
		}
		acct, err := h.Deposits.GetAccountTx(ctx, tx, *p.DebitSavingsAcct)
		if err != nil {
			return nil, err
		}
		if acct.CounterpartyID != loan.CounterpartyID {
			return nil, httpx.ErrBadRequest("savings account does not belong to this member")
		}
		if acct.AvailableBalance.LessThan(payoff) {
			return nil, domain.ErrInsufficientBalance
		}
		narration := "Loan settlement · " + loan.LoanNo
		internal := domain.DepChannelInternal
		depTxn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
			Account: acct, TxnType: domain.TxnWithdrawal,
			Amount: payoff, Channel: &internal, Narration: &narration, InitiatedBy: makerID,
		})
		if err != nil {
			return nil, err
		}
		if p.ChannelRef == "" {
			p.ChannelRef = depTxn.TxnNo
		}
	}
	waterfall, err := h.Loans.GetWaterfallTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	narr := p.Narration
	if narr == "" {
		narr = "Settlement · " + loan.LoanNo
	}
	txn, alloc, err := h.Loans.PostRepaymentTx(ctx, tx, store.RepaymentInput{
		Loan: loan, Amount: payoff,
		Channel: p.Channel, ChannelRef: p.ChannelRef, Narration: narr,
		ValueDate: time.Now(), InitiatedBy: makerID,
	}, waterfall)
	if err != nil {
		return nil, err
	}
	dpd, err := h.Loans.RecalcDPDTx(ctx, tx, p.LoanID, time.Now())
	if err != nil {
		return nil, err
	}
	updated, err := h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	return &LoanRepayResult{Transaction: *txn, Allocation: *alloc, Loan: *updated, DPD: dpd}, nil
}

func (h *LoanRepaymentHandler) ExecuteReverseTx(
	ctx context.Context, tx pgx.Tx,
	p LoanReversePayload, makerID uuid.UUID,
) (*LoanReverseResult, error) {
	rev, err := h.Loans.ReverseRepaymentTx(ctx, tx, p.TxnID, p.Reason, makerID)
	if err != nil {
		return nil, err
	}
	loan, err := h.Loans.GetTx(ctx, tx, rev.LoanID)
	if err != nil {
		return nil, err
	}
	return &LoanReverseResult{Reversal: *rev, Loan: *loan}, nil
}

// ── wf_callbacks-facing Run wrappers ──────────────────────────────

// RunRepaymentTx — wf_callbacks wrapper for loan_repayment.
func (h *LoanRepaymentHandler) RunRepaymentTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanRepaymentPayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_repayment context: %w", err)
	}
	res, err := h.ExecuteRepaymentTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.postRepaymentToGLTx(ctx, tx, tenantID, res, env.Payload.Channel); err != nil {
		return uuid.Nil, err
	}
	return res.Transaction.ID, nil
}

// RunSettleTx — wf_callbacks wrapper for loan_settle. Posts via the
// settlement-specific postingops helper (distinct source_module from
// regular repayments).
func (h *LoanRepaymentHandler) RunSettleTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanSettlePayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_repayment context: %w", err)
	}
	res, err := h.ExecuteSettleTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := postingops.PostLoanSettleTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
	}, postingops.LoanSettleInput{
		TenantID:  tenantID,
		TxnID:     res.Transaction.ID,
		LoanNo:    res.Loan.LoanNo,
		Amount:    res.Transaction.Amount,
		Principal: res.Allocation.Principal,
		Interest:  res.Allocation.Interest,
		Penalty:   res.Allocation.Penalty,
		Fees:      res.Allocation.Fees,
		Channel:   env.Payload.Channel,
	}); err != nil {
		return uuid.Nil, err
	}
	return res.Transaction.ID, nil
}

// RunReverseTx — wf_callbacks wrapper for loan_reverse. Posts via
// the repayment-reversal-specific postingops helper.
func (h *LoanRepaymentHandler) RunReverseTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanReversePayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_repayment context: %w", err)
	}
	res, err := h.ExecuteReverseTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	origTxnID := uuid.Nil
	if res.Reversal.ReversesTxnID != nil {
		origTxnID = *res.Reversal.ReversesTxnID
	}
	if err := postingops.PostLoanRepaymentReversalTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
	}, postingops.LoanRepaymentReversalInput{
		TenantID:      tenantID,
		ReversalTxnID: res.Reversal.ID,
		OriginalTxnID: origTxnID,
		LoanNo:        res.Loan.LoanNo,
		Amount:        res.Reversal.Amount,
		Principal:     res.Reversal.PrincipalComponent,
		Interest:      res.Reversal.InterestComponent,
		Penalty:       res.Reversal.PenaltyComponent,
		Fees:          res.Reversal.FeeComponent,
		Channel:       "",
		Reason:        env.Payload.Reason,
	}); err != nil {
		return uuid.Nil, err
	}
	return res.Reversal.ID, nil
}
