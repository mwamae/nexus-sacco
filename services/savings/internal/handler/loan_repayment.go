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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type LoanRepaymentHandler struct {
	DB        *db.Pool
	Tenants   *store.TenantStore
	Members   *store.MemberStore
	Deposits  *store.DepositStore
	Loans     *store.LoanStore
	Approvals *store.ApprovalsStore
	Notifier  *notifier.Client
	Logger    *slog.Logger
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
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanRepayment {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.MemberID
			amount := in.Amount
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanRepayment,
				Title:           "Repayment on loan " + loan.LoanNo,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Amount:          &amount,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteRepaymentTx(r.Context(), tx, payload, userID)
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
	h.emitRepayment(r, tid, userID, result)
	httpx.Created(w, result)
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
	var member *store.MemberLite
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Members.GetTx(r.Context(), tx, result.Loan.MemberID)
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
			memberID := loan.MemberID
			payoff := loan.PenaltyBalance.Add(loan.InterestBalance).Add(loan.PrincipalBalance).Add(loan.FeesBalance)
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanSettle,
				Title:           "Early settle loan " + loan.LoanNo,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Amount:          &payoff,
				Payload:         payload,
				MakerUserID:     userID,
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
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:          domain.ApprovalKindLoanReverse,
				Title:         "Reverse loan txn " + orig.TxnNo,
				SubjectLoanID: &orig.LoanID,
				Amount:        &amt,
				Payload:       payload,
				MakerUserID:   userID,
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
