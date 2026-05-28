// Loan repayment / settlement / reversal HTTP handlers (Phase 6d).
//
//   POST /v1/loans/{loan_id}/repay              record a repayment via any channel
//   POST /v1/loans/{loan_id}/settle             pay off the full outstanding balance
//   POST /v1/loan-transactions/{txn_id}/reverse reverse a repayment
//   GET  /v1/loans/{loan_id}/payoff             figure for "what's it cost to clear this loan today"
//   POST /v1/loans/{loan_id}/recalc-dpd         manual DPD recompute (testing / corrections)
//   GET  /v1/loans/arrears-summary              tenant-wide arrears by classification band

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/postingops"
	"github.com/nexussacco/savings/internal/receiptops"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type LoanRepaymentHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	Deposits       *store.DepositStore
	Loans          *store.LoanStore
	Approvals      *store.ApprovalsStore
	// Receipts + VirtualTills drive the inline-panel receipt writes.
	Receipts     *store.ReceiptStore
	VirtualTills *store.VirtualTillStore
	Notifier     *notifier.Client
	Posting      *posting.Client
	Logger       *slog.Logger

	// wf-routed queueing — same shape as DepositHandler.
	Workflow       *workflowclient.Client
	SavingsSelfURL string
}

// ─────────── Repay ───────────

type repayReq struct {
	Amount             decimal.Decimal `json:"amount"`
	Channel            string          `json:"channel"`               // 'mpesa' | 'auto_savings' | 'bank' | 'payroll' | 'teller'
	ChannelRef         string          `json:"channel_ref"`
	Narration          string          `json:"narration"`
	ValueDate          string          `json:"value_date,omitempty"`  // YYYY-MM-DD
	DebitSavingsAcct   *uuid.UUID      `json:"debit_savings_account_id,omitempty"` // for channel='auto_savings'
}

type repayResp struct {
	Transaction domain.LoanTransaction       `json:"transaction"`
	Allocation  store.RepaymentAllocation    `json:"allocation"`
	Loan        domain.Loan                  `json:"loan"`
	DPD         *store.DPDResult             `json:"dpd"`
}

func (h *LoanRepaymentHandler) Repay(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in repayReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be > 0"))
		return
	}
	if in.Channel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel is required"))
		return
	}
	if strings.EqualFold(in.Channel, "cash") {
		// Inline cash repayment is blocked — the Collection Desk owns
		// every cash-touching path so the till session reconciles.
		// UI hard-blocks Cash in the modal; this is the server-side
		// hard stop. The loan_id is included in the deep link so the
		// teller lands on a pre-filled receipt form.
		err := httpx.ErrCashInlineBlocked(loanID.String())
		err.Details = map[string]any{
			"deep_link":   "/collect/receipts/new?loan_id=" + loanID.String(),
			"reason":      "no_till_session",
			"action":      "Open a till and use the Collection Desk to receipt cash repayments.",
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel == "auto_savings" && in.DebitSavingsAcct == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("debit_savings_account_id is required for channel='auto_savings'"))
		return
	}
	if in.ValueDate != "" {
		if _, err := time.Parse("2006-01-02", in.ValueDate); err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	payload := LoanRepaymentPayload{
		LoanID:           loanID,
		Amount:           in.Amount,
		Channel:          in.Channel,
		ChannelRef:       in.ChannelRef,
		Narration:        in.Narration,
		ValueDate:        in.ValueDate,
		DebitSavingsAcct: in.DebitSavingsAcct,
	}

	var result *LoanRepayResult
	var pending *domain.PendingApproval
	var receipt *domain.Receipt
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
		if err != nil {
			return err
		}
		memberID := loan.CounterpartyID
		if toggles.LoanRepayment {
			amount := in.Amount
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindLoanRepayment,
				Title:           "Repayment on loan " + loan.LoanNo,
				SubjectID:       loanID,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Amount:          &amount,
				Payload:         payload,
				MakerUserID:     userID,
				SummarySuffix:   " — " + amount.StringFixed(2),
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			rec, rerr := h.writeInlineRepaymentReceipt(r.Context(), tx, tid, memberID, loanID, userID,
				in.Channel, in.ChannelRef, amount, in.Narration,
				domain.ReceiptDraft, domain.LinePending, nil)
			if rerr != nil {
				return rerr
			}
			if len(rec.Lines) > 0 {
				if aerr := h.Receipts.AttachApprovalTx(r.Context(), tx, rec.Lines[0].ID, pa.ID); aerr != nil {
					return aerr
				}
			}
			receipt = rec
			return nil
		}
		res, err := h.ExecuteRepaymentTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		if perr := h.postRepaymentToGLTx(r.Context(), tx, tid, res, in.Channel); perr != nil {
			return perr
		}
		rec, rerr := h.writeInlineRepaymentReceipt(r.Context(), tx, tid, memberID, loanID, userID,
			in.Channel, in.ChannelRef, res.Transaction.Amount, in.Narration,
			domain.ReceiptPosted, domain.LinePosted, &res.Transaction.ID)
		if rerr != nil {
			return rerr
		}
		receipt = rec
		result = res
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeLoanRepayErr(w, r, err)
		return
	}
	_ = receipt // surfaced via /v1/receipts
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	h.emitRepayment(r, tid, userID, result)
	httpx.Created(w, result)
}

// writeInlineRepaymentReceipt persists a single-line loan_repayment
// receipt for the inline Repayment panel. Repayment channels that
// can't ride the receipts table (auto_savings — funded by member's
// savings, not external cash) silently skip the receipt write.
func (h *LoanRepaymentHandler) writeInlineRepaymentReceipt(
	ctx context.Context, tx pgx.Tx,
	tenantID, memberID, loanID, userID uuid.UUID,
	channel, channelRef string,
	amount decimal.Decimal, narration string,
	headerStatus domain.ReceiptStatus, lineStatus domain.ReceiptLineStatus,
	postedTxnID *uuid.UUID,
) (*domain.Receipt, error) {
	if h.Receipts == nil || h.VirtualTills == nil {
		return &domain.Receipt{}, nil
	}
	// Translate the repayment-side channel string to a ReceiptChannel.
	// auto_savings funds from the member's own savings — there's no
	// teller-side receipt for that.
	rc := mapRepaymentChannelToReceipt(channel)
	if rc == "" {
		return &domain.Receipt{}, nil
	}
	lid := loanID
	rec, err := receiptops.WriteTx(ctx, tx, receiptops.Deps{
		Receipts:     h.Receipts,
		VirtualTills: h.VirtualTills,
	}, receiptops.WriteInput{
		TenantID:       tenantID,
		CounterpartyID: memberID,
		CashierUserID:  userID,
		Channel:        rc,
		ChannelRef:     channelRef,
		ChannelAmount:  amount,
		Narration:      narration,
		Source:         "inline_loan_repayment",
		HeaderStatus:   headerStatus,
		Lines: []receiptops.LineInput{{
			Kind:            domain.LineLoanRepayment,
			Amount:          amount,
			TargetAccountID: &lid,
			Status:          lineStatus,
			PostedTxnID:     postedTxnID,
		}},
	})
	if err != nil {
		if errors.Is(err, receiptops.ErrUnsupportedChannel) {
			return &domain.Receipt{}, nil
		}
		return nil, err
	}
	return rec, nil
}

// mapRepaymentChannelToReceipt converts the free-form repayment
// channel string to a typed ReceiptChannel. "" returned for channels
// that don't generate teller receipts.
func mapRepaymentChannelToReceipt(channel string) domain.ReceiptChannel {
	switch strings.ToLower(channel) {
	case "mpesa":
		return domain.RCMpesa
	case "airtel_money":
		return domain.RCAirtelMoney
	case "bank", "bank_transfer":
		return domain.RCBankTransfer
	case "cheque":
		return domain.RCCheque
	case "standing_order":
		return domain.RCStandingOrder
	case "auto_savings", "payroll", "internal", "":
		return ""
	}
	return ""
}

// postRepaymentToGL — auto-post per the SACCO accounting rules:
//
//   Debit  Cash / M-Pesa / Bank            (total repaid)
//   Credit Member Loans Receivable          (principal portion)
//   Credit Loan Interest Income             (interest portion)
//   Credit Penalty Income                   (penalty portion)
//   Credit Loan Processing Fee Income       (fees portion — close enough as the bucket name doesn't distinguish)
//
// We only emit the credit lines that are non-zero so the journal entry
// stays clean.
// postRepaymentToGLTx — wrapper, body moved into postingops.
// The approval executor calls postingops.PostLoanRepaymentTx directly.
func (h *LoanRepaymentHandler) postRepaymentToGLTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, result *LoanRepayResult, channel string) error {
	if result == nil {
		return nil
	}
	return postingops.PostLoanRepaymentTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
	}, postingops.LoanRepaymentInput{
		TenantID:  tenantID,
		TxnID:     result.Transaction.ID,
		LoanNo:    result.Loan.LoanNo,
		Amount:    result.Transaction.Amount,
		Principal: result.Allocation.Principal,
		Interest:  result.Allocation.Interest,
		Penalty:   result.Allocation.Penalty,
		Fees:      result.Allocation.Fees,
		Channel:   channel,
	})
}

func repaymentCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "mpesa":
		return "1030"
	case "bank", "bank_transfer":
		return "1020"
	case "auto_savings":
		return "2000" // Repayment came from the member's savings account
	case "payroll":
		return "1020" // Payroll deduction flowing through bank
	default:
		return "1000" // Cash / teller default
	}
}

// emitRepayment fires LOAN_REPAYMENT_RECEIVED post-commit. We also
// fire LOAN_SETTLED if the repayment took the outstanding balance
// to zero — a single repayment can close the loan, so both events
// can land in a single dispatch.
func (h *LoanRepaymentHandler) emitRepayment(
	r *http.Request, tenantID, actorID uuid.UUID, result *LoanRepayResult,
) {
	if h.Notifier == nil || result == nil {
		return
	}
	var member *store.CounterpartyView
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Counterparties.GetByIDTx(r.Context(), tx, result.Loan.CounterpartyID)
		return err
	})
	if member == nil {
		return
	}
	sourceModule := "savings.loans"
	recordID := result.Transaction.ID
	deepLink := "/loans/" + result.Loan.ID.String()
	mid := member.ID
	basePayload := map[string]any{
		"member_no":           member.MemberNo,
		"full_name":           member.FullName,
		"loan_no":             result.Loan.LoanNo,
		"amount":              result.Transaction.Amount.String(),
		"principal_balance":   result.Loan.PrincipalBalance.String(),
		"interest_balance":    result.Loan.InterestBalance.String(),
		"fees_balance":        result.Loan.FeesBalance.String(),
		"penalty_balance":     result.Loan.PenaltyBalance.String(),
		"days_past_due":       result.Loan.DaysPastDue,
	}
	h.Notifier.Notify(r.Context(), notifier.Request{
		TenantID:          tenantID,
		EventCode:         "LOAN_REPAYMENT_RECEIVED",
		RecipientMemberID: &mid,
		RecipientName:     member.FullName,
		RecipientPhone:    strNilIfEmpty(member.Phone),
		RecipientEmail:    strNilIfEmpty(member.Email),
		SourceModule:      &sourceModule,
		SourceRecordID:    &recordID,
		DeepLink:          &deepLink,
		InitiatedBy:       nonZeroUUID(actorID),
		Payload:           basePayload,
	})
	if result.Loan.Status == domain.LoanSettled {
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tenantID,
			EventCode:         "LOAN_SETTLED",
			RecipientMemberID: &mid,
			RecipientName:     member.FullName,
			RecipientPhone:    strNilIfEmpty(member.Phone),
			RecipientEmail:    strNilIfEmpty(member.Email),
			SourceModule:      &sourceModule,
			SourceRecordID:    &recordID,
			DeepLink:          &deepLink,
			InitiatedBy:       nonZeroUUID(actorID),
			Payload:           basePayload,
		})
	}
}

// ─────────── Payoff figure ───────────

type payoffResp struct {
	Loan        domain.Loan     `json:"loan"`
	Payoff      decimal.Decimal `json:"payoff"`
	Breakdown   struct {
		PrincipalBalance decimal.Decimal `json:"principal_balance"`
		InterestBalance  decimal.Decimal `json:"interest_balance"`
		FeesBalance      decimal.Decimal `json:"fees_balance"`
		PenaltyBalance   decimal.Decimal `json:"penalty_balance"`
	} `json:"breakdown"`
}

func (h *LoanRepaymentHandler) Payoff(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out payoffResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Accrue first so the figure reflects "as of today".
		if _, err := h.Loans.AccrueDueInterestTx(r.Context(), tx, loanID, time.Now(), userID); err != nil {
			return err
		}
		loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
		if err != nil {
			return err
		}
		out.Loan = *loan
		out.Payoff = loan.PenaltyBalance.Add(loan.InterestBalance).Add(loan.PrincipalBalance).Add(loan.FeesBalance)
		out.Breakdown.PrincipalBalance = loan.PrincipalBalance
		out.Breakdown.InterestBalance = loan.InterestBalance
		out.Breakdown.FeesBalance = loan.FeesBalance
		out.Breakdown.PenaltyBalance = loan.PenaltyBalance
		return nil
	})
	if err != nil {
		writeLoanRepayErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Settle (early settlement) ───────────

type settleReq struct {
	Channel          string     `json:"channel"`
	ChannelRef       string     `json:"channel_ref"`
	Narration        string     `json:"narration"`
	DebitSavingsAcct *uuid.UUID `json:"debit_savings_account_id,omitempty"`
}

func (h *LoanRepaymentHandler) Settle(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in settleReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := LoanSettlePayload{
		LoanID:           loanID,
		Channel:          in.Channel,
		ChannelRef:       in.ChannelRef,
		Narration:        in.Narration,
		DebitSavingsAcct: in.DebitSavingsAcct,
	}
	var result *LoanRepayResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanSettle {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.CounterpartyID
			payoff := loan.PenaltyBalance.Add(loan.InterestBalance).Add(loan.PrincipalBalance).Add(loan.FeesBalance)
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindLoanSettle,
				Title:           "Early settle loan " + loan.LoanNo,
				SubjectID:       loanID,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Amount:          &payoff,
				Payload:         payload,
				MakerUserID:     userID,
				SummarySuffix:   " — payoff " + payoff.StringFixed(2),
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteSettleTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeLoanRepayErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

// ─────────── Reverse ───────────

type reverseReq struct {
	Reason string `json:"reason"`
}

func (h *LoanRepaymentHandler) Reverse(w http.ResponseWriter, r *http.Request) {
	txnID, err := parseUUIDParam(r, "txn_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in reverseReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := LoanReversePayload{TxnID: txnID, Reason: in.Reason}
	var result *LoanReverseResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanReverse {
			orig, err := h.Loans.GetTxnTx(r.Context(), tx, txnID)
			if err != nil {
				return err
			}
			amt := orig.Amount.Abs()
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:      tid,
				Kind:          domain.ApprovalKindLoanReverse,
				Title:         "Reverse loan txn " + orig.TxnNo,
				SubjectID:     orig.LoanID,
				SubjectLoanID: &orig.LoanID,
				Amount:        &amt,
				Payload:       payload,
				MakerUserID:   userID,
				SummarySuffix: " — " + amt.StringFixed(2),
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteReverseTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeLoanRepayErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

// ─────────── Manual DPD recompute ───────────

func (h *LoanRepaymentHandler) RecalcDPD(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var res *store.DPDResult
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if _, err := h.Loans.AccrueDueInterestTx(r.Context(), tx, loanID, time.Now(), userID); err != nil {
			return err
		}
		var err error
		res, err = h.Loans.RecalcDPDTx(r.Context(), tx, loanID, time.Now())
		return err
	})
	if err != nil {
		writeLoanRepayErr(w, r, err)
		return
	}
	httpx.OK(w, res)
}

// ─────────── Arrears summary ───────────

func (h *LoanRepaymentHandler) ArrearsSummary(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var bands []store.ArrearsBand
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		bands, err = h.Loans.ArrearsSummaryTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if bands == nil {
		bands = []store.ArrearsBand{}
	}
	// Compute totals + NPL ratio (substandard+doubtful+loss / total outstanding).
	var totalOut, nplOut decimal.Decimal
	var totalLoans, nplLoans int
	for _, b := range bands {
		totalOut = totalOut.Add(b.TotalOutstanding)
		totalLoans += b.LoanCount
		switch b.Classification {
		case "substandard", "doubtful", "loss":
			nplOut = nplOut.Add(b.TotalOutstanding)
			nplLoans += b.LoanCount
		}
	}
	var nplPct decimal.Decimal
	if totalOut.GreaterThan(decimal.Zero) {
		nplPct = nplOut.Mul(decimal.NewFromInt(100)).Div(totalOut).Round(2)
	}
	httpx.OK(w, map[string]any{
		"bands":              bands,
		"total_loans":        totalLoans,
		"total_outstanding":  totalOut,
		"npl_loan_count":     nplLoans,
		"npl_outstanding":    nplOut,
		"npl_ratio_pct":      nplPct,
	})
}

// ─────────── Run DPD for an entire tenant (CLI helper) ───────────

// RunDPDForTenant runs the daily DPD + accrual sweep. If `collections`
// is non-nil, also auto-opens collection cases for newly-in-arrears
// loans + marks past-promised PTPs as broken.
func RunDPDForTenant(ctx context.Context, h *LoanRepaymentHandler, tenantID uuid.UUID, asOf time.Time, actorID uuid.UUID, collections *store.LoanCollectionsStore) (int, error) {
	var processed int
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ids, err := h.Loans.LoansForDPDRecalcTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := h.Loans.AccrueDueInterestTx(ctx, tx, id, asOf, actorID); err != nil {
				return err
			}
			if _, err := h.Loans.RecalcDPDTx(ctx, tx, id, asOf); err != nil {
				return err
			}
			if collections != nil {
				if err := AfterDPDRecalcTx(ctx, tx, h.Loans, collections, id, asOf); err != nil {
					return err
				}
			}
			processed++
		}
		// Mark overdue PTPs as broken + escalate priority on those cases.
		if collections != nil {
			if _, err := collections.MarkOverduePTPsBrokenTx(ctx, tx, asOf); err != nil {
				return err
			}
			if err := collections.EscalatePriorityOnBrokenPTPsTx(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	return processed, err
}

// ─────────── Error mapping ───────────

func writeLoanRepayErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrInsufficientBalance),
		errors.Is(err, domain.ErrLoanNotRepayable):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
