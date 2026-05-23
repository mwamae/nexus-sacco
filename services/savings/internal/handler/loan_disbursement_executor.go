// Disbursement executor (stage 3). Replays the LoanHandler.Disburse
// inline body using stored payload + maker id.

package handler

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/store"
)

type LoanDisbursementPayload struct {
	LoanID          uuid.UUID  `json:"loan_id"`
	Channel         string     `json:"channel"`
	TargetAccountID *uuid.UUID `json:"target_account_id,omitempty"`
	ExternalRef     *string    `json:"external_ref,omitempty"`
	ValueDate       *string    `json:"value_date,omitempty"`
}

type LoanDisbursementResult struct {
	Loan         domain.Loan              `json:"loan"`
	Schedule     []domain.LoanInstallment `json:"schedule"`
	NetDisbursed decimal.Decimal          `json:"net_disbursed"`
	Fees         []domain.LoanTransaction `json:"fees"`
	Disbursement domain.LoanTransaction   `json:"disbursement"`
}

func (h *LoanHandler) ExecuteDisbursementTx(
	ctx context.Context, tx pgx.Tx,
	p LoanDisbursementPayload, makerID uuid.UUID,
) (*LoanDisbursementResult, error) {
	valueDate := time.Now()
	if p.ValueDate != nil && *p.ValueDate != "" {
		d, err := time.Parse("2006-01-02", *p.ValueDate)
		if err != nil {
			return nil, httpx.ErrBadRequest("value_date must be YYYY-MM-DD")
		}
		valueDate = d
	}
	loan, err := h.Loans.GetTx(ctx, tx, p.LoanID)
	if err != nil {
		return nil, err
	}
	if loan.Status != domain.LoanPendingDisbursement {
		return nil, domain.ErrAppNotDisbursable
	}
	product, err := h.LoanProducts.GetTx(ctx, tx, loan.ProductID)
	if err != nil {
		return nil, err
	}
	schedule := domain.GenerateSchedule(
		loan.Principal, loan.InterestRatePct,
		loan.TermMonths, loan.GracePeriodMonths,
		valueDate,
		loan.InterestMethod, loan.RepaymentMethod,
	)
	if err := h.Loans.SaveScheduleTx(ctx, tx, loan.ID, schedule); err != nil {
		return nil, err
	}
	// Upfront fees — iterate the per-product fee list. added_to_loan and
	// at_each_installment fees are not posted at disbursement time
	// (Phase 6d covers the deferred-fee plumbing); for now we treat
	// only upfront fees as cash-out at disbursement.
	var upfrontFees decimal.Decimal
	out := &LoanDisbursementResult{Fees: []domain.LoanTransaction{}}
	for _, f := range product.Fees {
		if f.Timing != domain.FeeUpfront {
			continue
		}
		amt := domain.ApplyFee(loan.Principal, f.Amount, f.IsPct)
		if amt.IsZero() {
			continue
		}
		upfrontFees = upfrontFees.Add(amt)
		ch := "internal"
		narration := f.Name + " · " + loan.LoanNo
		feeTxn, err := h.Loans.PostTxnTx(ctx, tx, store.PostLoanInput{
			Loan:         loan,
			TxnType:      domain.LoanTxnFeeCharge,
			Amount:       amt,
			FeeComponent: amt,
			Channel:      &ch,
			Narration:    &narration,
			InitiatedBy:  makerID,
		})
		if err != nil {
			return nil, err
		}
		out.Fees = append(out.Fees, *feeTxn)
	}
	netDisbursed := loan.Principal.Sub(upfrontFees)
	if netDisbursed.LessThan(decimal.Zero) {
		netDisbursed = decimal.Zero
	}
	channelRef := ""
	if p.ExternalRef != nil {
		channelRef = *p.ExternalRef
	}
	switch p.Channel {
	case "internal":
		if p.TargetAccountID == nil {
			return nil, httpx.ErrBadRequest("target_account_id is required when channel='internal'")
		}
		acct, err := h.Deposits.GetAccountTx(ctx, tx, *p.TargetAccountID)
		if err != nil {
			return nil, err
		}
		if acct.CounterpartyID != loan.CounterpartyID {
			return nil, httpx.ErrBadRequest("target deposit account does not belong to this member")
		}
		internal := domain.DepChannelInternal
		narration := "Loan disbursement · " + loan.LoanNo
		depTxn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnDeposit,
			Amount:      netDisbursed,
			Channel:     &internal,
			Narration:   &narration,
			InitiatedBy: makerID,
		})
		if err != nil {
			return nil, err
		}
		if channelRef == "" {
			channelRef = depTxn.TxnNo
		}
	}
	ch := p.Channel
	narration := "Net disbursement · " + loan.LoanNo
	dTxn, err := h.Loans.PostTxnTx(ctx, tx, store.PostLoanInput{
		Loan:               loan,
		TxnType:            domain.LoanTxnDisbursement,
		Amount:             loan.Principal,
		PrincipalComponent: loan.Principal,
		Channel:            &ch,
		ChannelRef:         &channelRef,
		Narration:          &narration,
		InitiatedBy:        makerID,
	})
	if err != nil {
		return nil, err
	}
	out.Disbursement = *dTxn
	firstInstallment := decimal.Zero
	var firstDue time.Time
	if len(schedule) > 0 {
		firstDue = schedule[0].DueDate
		firstInstallment = schedule[0].TotalDue
	} else {
		firstDue = valueDate.AddDate(0, 1, 0)
	}
	updated, err := h.Loans.MarkDisbursedTx(ctx, tx, loan.ID,
		netDisbursed, upfrontFees,
		p.Channel, channelRef, p.TargetAccountID,
		firstDue, firstInstallment, makerID)
	if err != nil {
		return nil, err
	}
	out.Loan = *updated
	out.NetDisbursed = netDisbursed
	if _, err := h.Applications.UpdateStatusTx(ctx, tx, loan.ApplicationID, store.AppTransition{
		To: domain.AppDisbursed, By: makerID,
	}); err != nil {
		return nil, err
	}
	fresh, err := h.Loans.ScheduleByLoanTx(ctx, tx, loan.ID)
	if err != nil {
		return nil, err
	}
	out.Schedule = fresh
	return out, nil
}
