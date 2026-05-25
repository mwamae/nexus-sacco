// Late-fee-capture handlers for membership applications.
//
// Fee payments may be captured after submission. Each successful
// POST creates an application_fee_payments row + a GL journal entry
// (DR channel-cash / CR 4080 Registration Fee Income). The
// aggregate fee_* fields on the parent application are recomputed
// from those rows inside the same tx.
//
// Three endpoints:
//
//	POST   /v1/applications/{id}/fee-payments                 — record
//	POST   /v1/applications/{id}/fee-payments/{paymentId}/void — undo
//	GET    /v1/applications/{id}/fee-payments                  — list
//
// State guard: payments are accepted only when the application is
// in one of the "still in flight or successfully landed" statuses.
// Withdrawn / declined applications go through the existing
// /post-refund path for fee return, not this endpoint.

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/member/internal/accounting"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/store"
)

// Statuses where a late-fee capture is allowed. Note the spread:
// 'submitted' (officer realised the fee landed after they hit
// submit), all the review/approval mid-states, and 'approved_active'
// (cashier clearing a shortfall on an already-onboarded member).
// 'declined' and 'withdrawn' are intentionally excluded — those use
// /post-refund instead.
var allowFeePaymentStatuses = map[domain.ApplicationStatus]bool{
	domain.AppStatusSubmitted:                true,
	domain.AppStatusUnderReview:              true,
	domain.AppStatusReturnedForCorrection:    true,
	domain.AppStatusReviewedPendingApp:       true,
	domain.AppStatusApprovedActive:           true,
}

// recordFeeReq is the POST body — mirrors the spec literally.
type recordFeeReq struct {
	Amount           decimal.Decimal `json:"amount"`
	Channel          string          `json:"channel"`
	ChannelReference *string         `json:"channel_reference"`
	ValueDate        string          `json:"value_date"` // YYYY-MM-DD
	ProofDocPath     *string         `json:"proof_doc_path"`
	Note             *string         `json:"note"`
}

type recordFeeResp struct {
	Payment     *domain.ApplicationFeePayment   `json:"payment"`
	Application *domain.MembershipApplication   `json:"application"`
	// Idempotent hit — the handler returns 409 with the existing row
	// id rather than inserting again. When set, Payment is the
	// existing row.
	DuplicateOf *uuid.UUID `json:"duplicate_of,omitempty"`
}

// RecordFeePayment handles POST /v1/applications/{id}/fee-payments.
func (h *ApplicationHandler) RecordFeePayment(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid application id"))
		return
	}
	var req recordFeeReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	// ─── Field validation ────────────────────────────────────────
	if !req.Amount.GreaterThan(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	req.Channel = strings.ToLower(strings.TrimSpace(req.Channel))
	if !domain.ValidFeePaymentChannel(req.Channel) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid channel"))
		return
	}
	if domain.FeeChannelRequiresReference(req.Channel) &&
		(req.ChannelReference == nil || strings.TrimSpace(*req.ChannelReference) == "") {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel_reference is required for non-cash channels"))
		return
	}
	// Normalise the reference (trim, nil-when-cash-and-empty).
	if req.ChannelReference != nil {
		trimmed := strings.TrimSpace(*req.ChannelReference)
		if trimmed == "" {
			req.ChannelReference = nil
		} else {
			req.ChannelReference = &trimmed
		}
	}
	valueDate, err := time.Parse("2006-01-02", strings.TrimSpace(req.ValueDate))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
		return
	}
	if valueDate.After(time.Now()) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date cannot be in the future"))
		return
	}

	var (
		recorded    *domain.ApplicationFeePayment
		application *domain.MembershipApplication
		duplicate   *uuid.UUID
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Load application + state guard.
		app, gerr := h.Applications.GetTx(r.Context(), tx, appID)
		if gerr != nil {
			return gerr
		}
		if !allowFeePaymentStatuses[app.Status] {
			return domain.ErrFeePaymentForbiddenStatus
		}

		// Overpay guard. 150% headroom; the operator can override by
		// putting "OVERPAY" anywhere in the note, which produces a
		// permanent paper trail of the conscious decision.
		ceiling := app.FeeAmountDue.Mul(decimal.NewFromFloat(1.5))
		newTotal := app.FeeAmountPaid.Add(req.Amount)
		noteVal := ""
		if req.Note != nil {
			noteVal = *req.Note
		}
		if app.FeeAmountDue.GreaterThan(decimal.Zero) &&
			newTotal.GreaterThan(ceiling) &&
			!strings.Contains(strings.ToUpper(noteVal), "OVERPAY") {
			return domain.ErrFeePaymentOverpay
		}

		// Idempotency — first the new table, then a soft check against
		// the Collection Desk's receipts (shared DB). Either hit
		// surfaces as 409 with the existing row's id (or a synthetic
		// id when matched on a receipt).
		if dup, ferr := h.FeePayments.FindLiveByChannelRefTx(r.Context(), tx, req.Channel, req.ChannelReference); ferr != nil {
			return ferr
		} else if dup != nil {
			recorded = dup
			duplicate = &dup.ID
			return domain.ErrFeePaymentDuplicate
		}
		if serial, ferr := h.FeePayments.FindLiveReceiptByChannelRefTx(r.Context(), tx, req.ChannelReference); ferr != nil {
			return ferr
		} else if serial != "" {
			if h.Logger != nil {
				h.Logger.Warn("fee payment matches existing Collection Desk receipt",
					"application", appID, "receipt_serial", serial, "channel_ref", req.ChannelReference)
			}
			return domain.ErrFeePaymentDuplicate
		}

		// Insert the payment row.
		ins, ierr := h.FeePayments.InsertTx(r.Context(), tx, store.FeePaymentInsertInput{
			ApplicationID:    appID,
			Amount:           req.Amount,
			Channel:          req.Channel,
			ChannelReference: req.ChannelReference,
			ValueDate:        valueDate,
			ProofDocPath:     req.ProofDocPath,
			Note:             req.Note,
			CreatedBy:        actorID,
		})
		if ierr != nil {
			return ierr
		}

		// GL post (best-effort; disabled posting in dev stamps a
		// synthetic id so the row's posted_at fires either way).
		jeID, postErr := h.postFeePaymentToGL(r.Context(), tenantID, app, ins, false)
		if postErr != nil {
			return postErr
		}
		if jeID != uuid.Nil {
			if err := h.FeePayments.SetJournalEntryTx(r.Context(), tx, ins.ID, jeID); err != nil {
				return err
			}
			ins.JournalEntryID = &jeID
			now := time.Now()
			ins.PostedAt = &now
		}

		// Recompute aggregates on the parent app row.
		if _, err := h.FeePayments.RecomputeAggregatesTx(r.Context(), tx, appID); err != nil {
			return err
		}
		// Re-fetch so the response reflects the updated aggregates.
		fresh, err := h.Applications.GetTx(r.Context(), tx, appID)
		if err != nil {
			return err
		}
		recorded = ins
		application = fresh
		return nil
	})
	if err != nil {
		writeFeePaymentErr(w, r, err, recorded)
		return
	}
	httpx.OK(w, recordFeeResp{Payment: recorded, Application: application, DuplicateOf: duplicate})
}

// VoidFeePayment handles POST /v1/applications/{id}/fee-payments/{paymentId}/void.
type voidFeeReq struct {
	Reason string `json:"reason"`
}

func (h *ApplicationHandler) VoidFeePayment(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid application id"))
		return
	}
	paymentID, err := uuid.Parse(chi.URLParam(r, "paymentId"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid payment id"))
		return
	}
	var req voidFeeReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	var (
		voided      *domain.ApplicationFeePayment
		application *domain.MembershipApplication
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Pull the payment + the parent app (the app load also
		// authenticates that the payment belongs to this app — we
		// re-check below).
		existing, gerr := h.FeePayments.GetByIDTx(r.Context(), tx, paymentID)
		if gerr != nil {
			return gerr
		}
		if existing.ApplicationID != appID {
			return domain.ErrFeePaymentNotFound
		}
		if existing.VoidedAt != nil {
			return domain.ErrFeePaymentAlreadyVoided
		}
		app, aerr := h.Applications.GetTx(r.Context(), tx, appID)
		if aerr != nil {
			return aerr
		}

		// Void the row.
		v, verr := h.FeePayments.VoidTx(r.Context(), tx, paymentID, actorID, strings.TrimSpace(req.Reason))
		if verr != nil {
			return verr
		}

		// Post the reversal JE only if the original had one (no point
		// reversing a never-posted entry).
		if existing.JournalEntryID != nil {
			jeID, perr := h.postFeePaymentToGL(r.Context(), tenantID, app, existing, true)
			if perr != nil {
				return perr
			}
			_ = jeID // reversal id is captured in the GL service; not
			// stamped on the void row (we keep the original journal_entry_id
			// for the audit trail). A future cleanup may add a separate
			// reversal_journal_entry_id column.
		}

		if _, err := h.FeePayments.RecomputeAggregatesTx(r.Context(), tx, appID); err != nil {
			return err
		}
		fresh, err := h.Applications.GetTx(r.Context(), tx, appID)
		if err != nil {
			return err
		}
		voided = v
		application = fresh
		return nil
	})
	if err != nil {
		writeFeePaymentErr(w, r, err, nil)
		return
	}
	httpx.OK(w, map[string]any{"payment": voided, "application": application})
}

// RestampReceipts handles POST /v1/applications/{id}/restamp-receipts.
//
// Re-runs the materialise-time receipt stamper for ops use — useful
// when a previous materialise hit the best-effort warn-and-continue
// branch, or when a backfill is needed against an application that
// was materialised before this PR shipped. Idempotent: the
// underlying stamper SELECT-checks then INSERTs, so re-running it
// either creates the missing rows or returns the existing ids.
func (h *ApplicationHandler) RestampReceipts(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid application id"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var result *store.FeeReceiptStampResult
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		app, gerr := h.Applications.GetTx(r.Context(), tx, appID)
		if gerr != nil {
			return gerr
		}
		if app.MaterializedCounterpartyID == nil {
			return httpx.ErrConflict("application is not materialised — nothing to stamp")
		}
		res, serr := h.ReceiptStamper.StampTx(r.Context(), tx, store.FeeReceiptStampInput{
			TenantID:                   tenantID,
			ApplicationID:              app.ID,
			ApplicationNo:              app.ApplicationNo,
			MaterializedCounterpartyID: *app.MaterializedCounterpartyID,
		})
		if serr != nil {
			return serr
		}
		result = res
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, result)
}

// ListFeePayments handles GET /v1/applications/{id}/fee-payments.
func (h *ApplicationHandler) ListFeePayments(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid application id"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var out []domain.ApplicationFeePayment
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		ps, err := h.FeePayments.ListByApplicationTx(r.Context(), tx, appID)
		if err != nil {
			return err
		}
		out = ps
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"payments": out})
}

// postFeePaymentToGL fires a journal entry for one fee payment row.
// Mirrors postFeeToGL's structure but keyed on the payment id (so
// the accounting service's (source_module, source_ref) dedup catches
// retries on the same payment).
//
//	forward (refund=false): DR channel-cash / CR 4080 Registration Fee Income
//	reversal (refund=true): DR 4080 / CR channel-cash
//
// Returns uuid.Nil when posting is disabled (dev); the caller still
// stamps the row using a synthetic id mirroring the Collection
// Desk's behaviour.
func (h *ApplicationHandler) postFeePaymentToGL(
	ctx context.Context, tenantID uuid.UUID,
	app *domain.MembershipApplication, payment *domain.ApplicationFeePayment,
	refund bool,
) (uuid.UUID, error) {
	cashAcct := registrationChannelCashAccount(payment.Channel)
	module := "member.application.fee"
	sourceRef := "app-fee-" + payment.ID.String()
	narration := fmt.Sprintf("Fee payment · %s · %s",
		app.ApplicationNo, payment.Amount.StringFixed(2))
	var lines []accounting.Line
	if refund {
		sourceRef += "-void"
		narration = "VOID fee payment · " + app.ApplicationNo + " · " + payment.Amount.StringFixed(2)
		lines = []accounting.Line{
			{AccountCode: "4080", Debit: payment.Amount, Narration: "Reverse registration fee income"},
			{AccountCode: cashAcct, Credit: payment.Amount, Narration: "Refund out via " + payment.Channel},
		}
	} else {
		lines = []accounting.Line{
			{AccountCode: cashAcct, Debit: payment.Amount, Narration: "Cash in via " + payment.Channel},
			{AccountCode: "4080", Credit: payment.Amount, Narration: "Registration fee income"},
		}
	}
	res, err := h.Accounting.Post(ctx, accounting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		ValueDate:    payment.ValueDate,
		SourceModule: module,
		SourceRef:    sourceRef,
		Narration:    narration,
		Lines:        lines,
	})
	if err != nil {
		if errors.Is(err, accounting.ErrDisabled) {
			// Dev / disabled. Stamp a synthetic id so the row's
			// posted_at fires; mirrors the Collection Desk's
			// postFeeLineTx behaviour.
			if h.Logger != nil {
				h.Logger.Warn("fee payment GL post disabled — stamping synthetic JE id",
					"application", app.ID, "payment", payment.ID,
					"amount", payment.Amount.StringFixed(2))
			}
			return uuid.New(), nil
		}
		if h.Logger != nil {
			h.Logger.Error("post fee payment to GL",
				"application", app.ID, "payment", payment.ID, "refund", refund, "err", err)
		}
		return uuid.Nil, err
	}
	return res.EntryID, nil
}

// writeFeePaymentErr maps the typed sentinel errors to HTTP. The
// duplicate path optionally surfaces the existing row's id in the
// response body so the caller can show "this M-Pesa code was already
// recorded as payment X".
func writeFeePaymentErr(w http.ResponseWriter, r *http.Request, err error, dup *domain.ApplicationFeePayment) {
	switch {
	case errors.Is(err, domain.ErrFeePaymentForbiddenStatus):
		httpx.WriteErr(w, r, httpx.ErrConflict("application is in a status that does not allow fee payments — use post-refund instead"))
	case errors.Is(err, domain.ErrFeePaymentOverpay):
		httpx.WriteErr(w, r, httpx.E(http.StatusUnprocessableEntity, "fee_overpay",
			"total payments would exceed 150% of the fee due; void earlier rows or add the substring OVERPAY to the note"))
	case errors.Is(err, domain.ErrFeePaymentDuplicate):
		msg := "duplicate fee payment for the same channel + reference"
		if dup != nil {
			msg = fmt.Sprintf("duplicate fee payment — existing row %s", dup.ID.String())
		}
		httpx.WriteErr(w, r, httpx.ErrConflict(msg))
	case errors.Is(err, domain.ErrFeePaymentAlreadyVoided):
		httpx.WriteErr(w, r, httpx.ErrConflict("fee payment is already voided"))
	case errors.Is(err, domain.ErrFeePaymentNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound("fee payment not found"))
	default:
		httpx.WriteErr(w, r, err)
	}
}
