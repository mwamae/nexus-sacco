// Disbursement executor (stage 3). Replays the LoanHandler.Disburse
// inline body using stored payload + maker id.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

// RunDisbursementTx — wf_callbacks wrapper for loan_disbursement.
func (h *LoanHandler) RunDisbursementTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanDisbursementPayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_disbursement context: %w", err)
	}
	res, err := h.ExecuteDisbursementTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.postLoanDisbursementToGLTx(ctx, tx, tenantID, res, env.Payload.Channel); err != nil {
		return uuid.Nil, err
	}
	return res.Disbursement.ID, nil
}

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
	// FeeGLLines is the per-CoA-code aggregation of upfront fees,
	// ready for the disbursement GL post to credit directly.
	// Aggregated by gl_credit_code so two product fees mapped to the
	// same income code collapse into one journal line. Not serialised
	// over the HTTP response — internal handoff between the executor
	// and postLoanDisbursementToGLTx.
	FeeGLLines   []posting.Line           `json:"-"`
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
	//
	// Each fee row carries gl_credit_code (migration 0034). Aggregate
	// fee amounts by GL code so the eventual disbursement JE has one
	// CR line per income account, not per fee — keeps the journal
	// readable and matches how 4010/4020/4190 land on the Income
	// Statement.
	var upfrontFees decimal.Decimal
	feeByGLCode := map[string]decimal.Decimal{}
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
		code := f.GLCreditCode
		if code == "" {
			code = "4010" // defensive — store ReplaceFeesTx already defaults
		}
		feeByGLCode[code] = feeByGLCode[code].Add(amt)
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
	// Materialise the FeeGLLines slice in deterministic code order so
	// the JE payload is stable across runs (helps test assertions and
	// audit reads).
	codes := make([]string, 0, len(feeByGLCode))
	for c := range feeByGLCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	out.FeeGLLines = make([]posting.Line, 0, len(codes))
	for _, c := range codes {
		out.FeeGLLines = append(out.FeeGLLines, posting.Line{
			AccountCode: c,
			Credit:      feeByGLCode[c],
			Narration:   "Loan fee income (" + c + ")",
		})
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

	// Phase 5 — propagate insider flag from the source application
	// onto the loan row (denormalised for fast filtering on
	// /loans/reports#insider-loans and the loans list filter).
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET is_insider = a.is_insider, insider_category = a.insider_category
		  FROM loan_applications a
		 WHERE loans.id = $1 AND loans.application_id = a.id
	`, loan.ID); err != nil {
		return nil, err
	}

	// Phase 5 — top-up / refinance: settle parent loan(s) atomically.
	// The new loan's principal effectively covered them; no money
	// moves externally — pure internal ledger settle.
	if err := settleParentLoansIfTopupOrRefinance(ctx, tx, h, loan, makerID); err != nil {
		return nil, err
	}

	// Phase 5 — place a BOSA lien against the borrower's BOSA account.
	// Nil-safe so tests + non-BOSA tenants don't break.
	if h.BOSALiens != nil {
		if err := placeBOSALienOnDisburseTx(ctx, tx, h, updated, makerID); err != nil {
			// Lien failures are NON-fatal — log + continue so a missing
			// BOSA account doesn't block disbursement. The lien-or-not
			// state is observable in the bosa_liens table and the
			// /loans/register/{id} BOSA banner.
			h.Logger.Warn("bosa lien placement failed",
				"loan_id", updated.ID, "err", err.Error())
		}
	}

	// Phase 6 — place a credit-life insurance policy when the loan's
	// product has insurance_provider_id set. Non-fatal on failure;
	// admin can retry via POST /v1/loans/{id}/insurance-policy.
	// Stub adapters today; real vendor calls wire here in a follow-up.
	if _, ierr := placeInsuranceForLoanTx(ctx, tx, updated); ierr != nil {
		h.Logger.Warn("insurance placement failed",
			"loan_id", updated.ID, "err", ierr.Error())
	}
	return out, nil
}

// placeBOSALienOnDisburseTx finds the borrower's BOSA account and
// inserts an active lien for the current balance. Idempotent via the
// UNIQUE(loan_id) constraint on bosa_liens.
func placeBOSALienOnDisburseTx(
	ctx context.Context, tx pgx.Tx, h *LoanHandler,
	loan *domain.Loan, placedBy uuid.UUID,
) error {
	// Resolve the borrower's member_id via counterparty_directory.
	var memberID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT member_id FROM counterparty_directory WHERE counterparty_id = $1
	`, loan.CounterpartyID).Scan(&memberID); err != nil {
		return fmt.Errorf("resolve member: %w", err)
	}
	if memberID == nil {
		// Counterparty is institutional (group loan) — no individual
		// BOSA to lien. Phase 5 intentionally skips this; a follow-up
		// can add per-officer BOSA liens for group loans.
		return nil
	}
	acct, err := h.BOSALiens.FindBOSAAccountForMemberTx(ctx, tx, *memberID)
	if err != nil {
		return err
	}
	if acct == nil {
		return nil // member has no BOSA account; nothing to lien
	}
	if _, err := h.BOSALiens.PlaceTx(ctx, tx,
		acct.AccountID, loan.ID, *memberID, placedBy, acct.Balance,
	); err != nil {
		return err
	}
	return nil
}

// settleParentLoansIfTopupOrRefinance reads the source application,
// and if application_type is 'topup' or 'refinance', settles every
// parent loan (parent_loan_id + refinance_source_loan_ids) by posting
// an internal repayment for the payoff amount.
func settleParentLoansIfTopupOrRefinance(
	ctx context.Context, tx pgx.Tx, h *LoanHandler,
	newLoan *domain.Loan, makerID uuid.UUID,
) error {
	app, err := h.Applications.GetTx(ctx, tx, newLoan.ApplicationID)
	if err != nil {
		return fmt.Errorf("load source application: %w", err)
	}
	if app.ApplicationType != "topup" && app.ApplicationType != "refinance" {
		return nil
	}
	// Collect every parent loan id.
	var parentIDs []uuid.UUID
	if app.ParentLoanID != nil {
		parentIDs = append(parentIDs, *app.ParentLoanID)
	}
	if len(app.RefinanceSourceLoanIDs) > 0 {
		var ids []string
		if err := json.Unmarshal(app.RefinanceSourceLoanIDs, &ids); err == nil {
			for _, s := range ids {
				id, err := uuid.Parse(s)
				if err != nil {
					continue
				}
				// Skip if already in parentIDs (parent_loan_id is one of the source ids).
				dup := false
				for _, p := range parentIDs {
					if p == id {
						dup = true
						break
					}
				}
				if !dup {
					parentIDs = append(parentIDs, id)
				}
			}
		}
	}
	if len(parentIDs) == 0 {
		return nil
	}
	waterfall, err := h.Loans.GetWaterfallTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, parentID := range parentIDs {
		parent, err := h.Loans.GetTx(ctx, tx, parentID)
		if err != nil {
			return fmt.Errorf("load parent loan %s: %w", parentID, err)
		}
		if parent.Status == domain.LoanSettled {
			continue // already settled — idempotent against re-run
		}
		// Refresh outstanding before computing payoff in case fees /
		// interest moved between application + disbursement.
		if _, err := h.Loans.AccrueDueInterestTx(ctx, tx, parent.ID, time.Now(), makerID); err != nil {
			return err
		}
		parent, err = h.Loans.GetTx(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
		payoff := parent.PenaltyBalance.Add(parent.InterestBalance).Add(parent.PrincipalBalance).Add(parent.FeesBalance)
		if payoff.LessThanOrEqual(decimal.Zero) {
			continue
		}
		narr := fmt.Sprintf("Settled by %s · %s", app.ApplicationType, newLoan.LoanNo)
		channelRef := newLoan.LoanNo
		if _, _, err := h.Loans.PostRepaymentTx(ctx, tx, store.RepaymentInput{
			Loan: parent, Amount: payoff,
			Channel: "internal", ChannelRef: channelRef, Narration: narr,
			ValueDate: time.Now(), InitiatedBy: makerID,
		}, waterfall); err != nil {
			return fmt.Errorf("settle parent loan %s: %w", parent.LoanNo, err)
		}
	}
	return nil
}
