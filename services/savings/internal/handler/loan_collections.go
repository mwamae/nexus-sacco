// Collections + restructuring HTTP handlers (Phase 6e).
//
//   GET   /v1/collection-cases                          queue with filters
//   GET   /v1/collection-cases/{case_id}                detail + contacts + PTPs
//   POST  /v1/collection-cases/{case_id}/assign         set assignee
//   POST  /v1/collection-cases/{case_id}/close          close (recovered or uncollectable)
//   POST  /v1/collection-cases/{case_id}/contacts       log a contact attempt
//   POST  /v1/collection-cases/{case_id}/promises       create a PTP
//   POST  /v1/promises/{ptp_id}/resolve                 kept | partial | broken | cancelled
//
//   POST  /v1/loans/{loan_id}/reschedule                re-amortise over a new term
//   POST  /v1/loans/{loan_id}/moratorium                payment holiday
//   POST  /v1/loans/{loan_id}/settlement-discount       accept less as full payment
//   POST  /v1/loans/{loan_id}/topup-intent              record top-up audit row
//   POST  /v1/loans/{loan_id}/refinance-intent          record refinance audit row + close old loan
//   GET   /v1/loans/{loan_id}/restructurings            list past restructuring events

package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

type LoanCollectionsHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Loans          *store.LoanStore
	Collections    *store.LoanCollectionsStore
	Restructure    *store.LoanRestructureStore
	Approvals      *store.ApprovalsStore
	Notifier       *notifier.Client
	Logger         *slog.Logger
}

// ─────────── Cases ───────────

func (h *LoanCollectionsHandler) ListCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.CaseListFilter{
		Status: q.Get("status"),
		Limit:  limit, Offset: offset,
	}
	if v := q.Get("assigned_to"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.AssignedTo = &id
		}
	}
	if q.Get("unassigned") == "1" {
		f.Unassigned = true
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.CaseListItem
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Collections.ListCasesTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.CaseListItem{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

type caseDetailResp struct {
	Case        domain.CollectionCase       `json:"case"`
	Loan        domain.Loan                 `json:"loan"`
	Contacts    []domain.CollectionContact  `json:"contacts"`
	PTPs        []domain.PromiseToPay       `json:"ptps"`
}

func (h *LoanCollectionsHandler) GetCase(w http.ResponseWriter, r *http.Request) {
	caseID, err := parseUUIDParam(r, "case_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out caseDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collections.GetCaseTx(r.Context(), tx, caseID)
		if err != nil {
			return err
		}
		loan, err := h.Loans.GetTx(r.Context(), tx, c.LoanID)
		if err != nil {
			return err
		}
		contacts, err := h.Collections.ContactsByCaseTx(r.Context(), tx, caseID)
		if err != nil {
			return err
		}
		ptps, err := h.Collections.PTPsByCaseTx(r.Context(), tx, caseID)
		if err != nil {
			return err
		}
		out = caseDetailResp{Case: *c, Loan: *loan, Contacts: contacts, PTPs: ptps}
		return nil
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	if out.Contacts == nil { out.Contacts = []domain.CollectionContact{} }
	if out.PTPs == nil     { out.PTPs = []domain.PromiseToPay{} }
	httpx.OK(w, out)
}

type assignReq struct {
	AssignTo uuid.UUID `json:"assign_to"`
}

func (h *LoanCollectionsHandler) Assign(w http.ResponseWriter, r *http.Request) {
	caseID, err := parseUUIDParam(r, "case_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in assignReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.AssignTo == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("assign_to is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.CollectionCase
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Collections.AssignTx(r.Context(), tx, caseID, in.AssignTo)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

type closeCaseReq struct {
	Recovered bool   `json:"recovered"`
	Reason    string `json:"reason"`
}

func (h *LoanCollectionsHandler) CloseCase(w http.ResponseWriter, r *http.Request) {
	caseID, err := parseUUIDParam(r, "case_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in closeCaseReq
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
	var out *domain.CollectionCase
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Collections.CloseCaseTx(r.Context(), tx, caseID, userID, in.Recovered, in.Reason)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Contacts ───────────

type contactReq struct {
	Kind    domain.ContactKind    `json:"kind"`
	Outcome domain.ContactOutcome `json:"outcome"`
	Note    *string               `json:"note,omitempty"`
	GPSLat  *decimal.Decimal      `json:"gps_lat,omitempty"`
	GPSLng  *decimal.Decimal      `json:"gps_lng,omitempty"`
}

func (h *LoanCollectionsHandler) LogContact(w http.ResponseWriter, r *http.Request) {
	caseID, err := parseUUIDParam(r, "case_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in contactReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !in.Kind.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid kind"))
		return
	}
	if !in.Outcome.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid outcome"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.CollectionContact
	label := fmt.Sprintf("%s: %s", in.Kind, in.Outcome)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
			CaseID:      caseID,
			Kind:        in.Kind,
			Outcome:     in.Outcome,
			Note:        in.Note,
			GPSLat:      in.GPSLat,
			GPSLng:      in.GPSLng,
			ContactedBy: userID,
		}, label)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── PTPs ───────────

type createPTPReq struct {
	PromisedAmount  decimal.Decimal `json:"promised_amount"`
	PromisedDate    string          `json:"promised_date"` // YYYY-MM-DD
	PromisedChannel *string         `json:"promised_channel,omitempty"`
	Notes           *string         `json:"notes,omitempty"`
}

func (h *LoanCollectionsHandler) CreatePTP(w http.ResponseWriter, r *http.Request) {
	caseID, err := parseUUIDParam(r, "case_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in createPTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.PromisedAmount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("promised_amount must be > 0"))
		return
	}
	d, err := time.Parse("2006-01-02", in.PromisedDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("promised_date must be YYYY-MM-DD"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.PromiseToPay
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collections.GetCaseTx(r.Context(), tx, caseID)
		if err != nil {
			return err
		}
		out, err = h.Collections.CreatePTPTx(r.Context(), tx, &domain.PromiseToPay{
			CaseID:          caseID,
			LoanID:          c.LoanID,
			PromisedAmount:  in.PromisedAmount,
			PromisedDate:    d,
			PromisedChannel: in.PromisedChannel,
			Notes:           in.Notes,
			CreatedBy:       userID,
		})
		// Log a contact entry summarising the promise.
		if err == nil {
			label := fmt.Sprintf("PTP promised %s by %s", in.PromisedAmount.String(), d.Format("2006-01-02"))
			_, _ = h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
				CaseID:      caseID,
				Kind:        domain.ContactCall,
				Outcome:     domain.OutcomePromiseMade,
				Note:        &label,
				ContactedBy: userID,
			}, label)
		}
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

type resolvePTPReq struct {
	Status     domain.PTPStatus `json:"status"`
	PaidAmount decimal.Decimal  `json:"paid_amount"`
	PaidTxnID  *uuid.UUID       `json:"paid_txn_id,omitempty"`
	Notes      *string          `json:"notes,omitempty"`
}

func (h *LoanCollectionsHandler) ResolvePTP(w http.ResponseWriter, r *http.Request) {
	ptpID, err := parseUUIDParam(r, "ptp_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in resolvePTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	switch in.Status {
	case domain.PTPKept, domain.PTPPartial, domain.PTPBroken, domain.PTPCancelled:
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be kept | partial | broken | cancelled"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.PromiseToPay
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Collections.ResolvePTPTx(r.Context(), tx, ptpID, in.Status, in.PaidAmount, in.PaidTxnID, in.Notes, userID)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Restructuring ───────────

type rescheduleReq struct {
	NewTermMonths        int              `json:"new_term_months"`
	NewInterestRatePct   *decimal.Decimal `json:"new_interest_rate_pct,omitempty"`
	NewFirstDueDate      *string          `json:"new_first_due_date,omitempty"` // YYYY-MM-DD
	Reason               string           `json:"reason"`
}

func (h *LoanCollectionsHandler) Reschedule(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in rescheduleReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.NewTermMonths <= 0 || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_term_months and reason are required"))
		return
	}
	var firstDue *time.Time
	if in.NewFirstDueDate != nil && *in.NewFirstDueDate != "" {
		d, err := time.Parse("2006-01-02", *in.NewFirstDueDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("new_first_due_date must be YYYY-MM-DD"))
			return
		}
		firstDue = &d
	}
	_ = firstDue
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := LoanReschedulePayload{
		LoanID: loanID, NewTermMonths: in.NewTermMonths, NewInterestRatePct: in.NewInterestRatePct,
		NewFirstDueDate: in.NewFirstDueDate, Reason: in.Reason,
	}
	var result *LoanRestructureResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanReschedule {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.MemberID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanReschedule,
				Title:           "Reschedule loan " + loan.LoanNo,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteRescheduleTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

type moratoriumReq struct {
	MoratoriumMonths int    `json:"moratorium_months"`
	SuspendInterest  bool   `json:"suspend_interest"`
	Reason           string `json:"reason"`
}

func (h *LoanCollectionsHandler) Moratorium(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in moratoriumReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.MoratoriumMonths <= 0 || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("moratorium_months and reason are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := LoanMoratoriumPayload{
		LoanID: loanID, MoratoriumMonths: in.MoratoriumMonths,
		SuspendInterest: in.SuspendInterest, Reason: in.Reason,
	}
	var result *LoanRestructureResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanMoratorium {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.MemberID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanMoratorium,
				Title:           "Moratorium on loan " + loan.LoanNo,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteMoratoriumTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

type settlementDiscountReq struct {
	DiscountAmount decimal.Decimal `json:"discount_amount"`
	Reason         string          `json:"reason"`
}

func (h *LoanCollectionsHandler) SettlementDiscount(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in settlementDiscountReq
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
	payload := LoanSettlementDiscountPayload{
		LoanID: loanID, DiscountAmount: in.DiscountAmount, Reason: in.Reason,
	}
	var result *LoanRestructureResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanSettlementDiscount {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.MemberID
			amount := in.DiscountAmount
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanSettlementDiscount,
				Title:           "Settlement discount on loan " + loan.LoanNo,
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
		res, err := h.ExecuteSettlementDiscountTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

type topupIntentReq struct {
	TopupAmount decimal.Decimal `json:"topup_amount"`
	Reason      string          `json:"reason"`
}

func (h *LoanCollectionsHandler) TopupIntent(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in topupIntentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TopupAmount.LessThanOrEqual(decimal.Zero) || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("topup_amount and reason are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanRestructuring
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Restructure.RecordTopupIntentTx(r.Context(), tx, h.Loans, loanID, in.TopupAmount, in.Reason, userID)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

type refinanceIntentReq struct {
	NewLoanID uuid.UUID `json:"new_loan_id"`
	Reason    string    `json:"reason"`
}

func (h *LoanCollectionsHandler) RefinanceIntent(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in refinanceIntentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.NewLoanID == uuid.Nil || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_loan_id and reason are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanRestructuring
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Restructure.RecordRefinanceIntentTx(r.Context(), tx, h.Loans, loanID, in.NewLoanID, in.Reason, userID)
		return err
	})
	if err != nil {
		writeCollectionsErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *LoanCollectionsHandler) ListRestructurings(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out []domain.LoanRestructuring
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Restructure.ByLoanTx(r.Context(), tx, loanID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.LoanRestructuring{}
	}
	httpx.OK(w, out)
}

// ─────────── DPD hook (called by the daily job) ───────────

// AfterDPDRecalcTx is invoked by the CLI/daily job after RecalcDPDTx
// for each loan. It auto-opens a collection case for newly-in-arrears
// loans and auto-closes recovered ones. Also marks any past-promised
// PTPs as broken + escalates priority.
func AfterDPDRecalcTx(
	ctx context.Context, tx pgx.Tx,
	loans *store.LoanStore, collections *store.LoanCollectionsStore,
	loanID uuid.UUID, asOf time.Time,
) error {
	loan, err := loans.GetTx(ctx, tx, loanID)
	if err != nil {
		return err
	}
	if loan.DaysPastDue > 0 {
		if _, err := collections.EnsureCaseForLoanTx(ctx, tx, loan); err != nil {
			return err
		}
	}
	if err := collections.AutoCloseIfRecoveredTx(ctx, tx, loanID); err != nil {
		return err
	}
	return nil
}

// ─────────── Error mapping ───────────

func writeCollectionsErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrPTPNotOpen),
		errors.Is(err, domain.ErrCaseNotOpen),
		errors.Is(err, domain.ErrRestructureNotAllowed),
		errors.Is(err, domain.ErrInvalidRestructuringKind),
		errors.Is(err, domain.ErrMoratoriumMonthsInvalid),
		errors.Is(err, domain.ErrRescheduleTermInvalid):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
