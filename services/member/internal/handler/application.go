// Unified Membership Application pipeline — HTTP surface.
//
//   POST   /v1/applications                                 capture + submit
//   GET    /v1/applications                                 queue, filters via query string
//   GET    /v1/applications/{id}                            detail + checklist + corrections
//   POST   /v1/applications/{id}/start-review               submitted → under_review (reviewer claims)
//   POST   /v1/applications/{id}/return-for-correction      under_review → returned_for_correction
//   POST   /v1/applications/{id}/resubmit                   returned → submitted (officer)
//   POST   /v1/applications/{id}/submit-for-approval        under_review → reviewed_pending_approval
//   POST   /v1/applications/{id}/approve                    reviewed_pending_approval → approved_active
//   POST   /v1/applications/{id}/decline                    reviewed_pending_approval → declined
//   POST   /v1/applications/{id}/return-to-reviewer         reviewed_pending_approval → under_review
//   POST   /v1/applications/{id}/withdraw                   any non-terminal → withdrawn
//   GET    /v1/applications/checklist-items?kind=…          per-kind checklist items
//   POST   /v1/applications/{id}/checklist                  reviewer upserts a response
//
// Phase D will wire the activation pipeline (member-row + share-acct
// + savings-acct + GL post + welcome notif) onto the
// approved-active transition. For now approval just flips status.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/member/internal/accounting"
	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/notifier"
	"github.com/nexussacco/member/internal/store"
)

type ApplicationHandler struct {
	DB             *db.Pool
	Applications   *store.ApplicationStore
	Members        *store.MemberStore
	Orgs           *store.OrgMemberStore   // required for institutional materialisation
	Counterparties *store.CounterpartyStore // Phase A dual-target mirror
	Accounting     *accounting.Client
	Notifier       *notifier.Client
	Logger         *slog.Logger
}

// ─────────── Create ───────────

type createApplicationReq struct {
	Kind            string                  `json:"kind"`            // individual | institutional
	ApplicantName   string                  `json:"applicant_name"`  // full name (individual) | registered name (institutional)
	EntityType      string                  `json:"entity_type,omitempty"`
	PrimaryPhone    string                  `json:"primary_phone,omitempty"`
	PrimaryEmail    string                  `json:"primary_email,omitempty"`
	BranchID        *uuid.UUID              `json:"branch_id,omitempty"`
	Payload         domain.ApplicantPayload `json:"applicant_payload"`

	// Registration-fee capture
	Fee *applicationFeeDTO `json:"registration_fee,omitempty"`
}

type applicationFeeDTO struct {
	AmountPaid       string `json:"amount_paid"`       // decimal as string
	PaymentChannel   string `json:"payment_channel"`
	PaymentReference string `json:"payment_reference"`
	PaymentDate      string `json:"payment_date"`       // YYYY-MM-DD
	ProofDocPath     string `json:"proof_doc_path,omitempty"`
	ShortfallNote    string `json:"shortfall_note,omitempty"`
}

func (h *ApplicationHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var req createApplicationReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	kind := domain.ApplicationKind(strings.ToLower(strings.TrimSpace(req.Kind)))
	if !kind.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind must be 'individual' or 'institutional'"))
		return
	}
	if strings.TrimSpace(req.ApplicantName) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("applicant_name is required"))
		return
	}

	var created *domain.MembershipApplication
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Read tenant_membership for fee snapshot. The table lives in
		// the identity service's schema but the database is shared.
		var collectFee bool
		var feeIndividual, feeInstitutional decimal.Decimal
		err := tx.QueryRow(r.Context(), `
			SELECT collect_registration_fee, registration_fee_individual, registration_fee_institutional
			  FROM tenant_membership WHERE tenant_id = $1
		`, tenantID).Scan(&collectFee, &feeIndividual, &feeInstitutional)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		feeRequired := collectFee
		feeAmountDue := decimal.Zero
		if feeRequired {
			if kind == domain.ApplicationKindIndividual {
				feeAmountDue = feeIndividual
			} else {
				feeAmountDue = feeInstitutional
			}
		}

		input := store.CreateApplicationInput{
			TenantID:      tenantID,
			Kind:          kind,
			ApplicantName: strings.TrimSpace(req.ApplicantName),
			EntityType:    strPtr(req.EntityType),
			PrimaryPhone:  strPtr(req.PrimaryPhone),
			PrimaryEmail:  strPtr(req.PrimaryEmail),
			BranchID:      req.BranchID,
			Payload:       req.Payload,
			FeeRequired:   feeRequired,
			FeeAmountDue:  feeAmountDue,
			FeeStatus:     "not_required",
			SubmittedBy:   actorID,
		}

		if feeRequired && req.Fee != nil {
			paid, perr := decimal.NewFromString(req.Fee.AmountPaid)
			if perr != nil || paid.IsNegative() {
				return httpx.ErrBadRequest("registration_fee.amount_paid must be a non-negative decimal")
			}
			input.FeeAmountPaid = paid
			input.FeePaymentChannel = strPtr(req.Fee.PaymentChannel)
			input.FeePaymentReference = strPtr(req.Fee.PaymentReference)
			if d := strings.TrimSpace(req.Fee.PaymentDate); d != "" {
				dt, err := time.Parse("2006-01-02", d)
				if err != nil {
					return httpx.ErrBadRequest("registration_fee.payment_date must be YYYY-MM-DD")
				}
				input.FeePaymentDate = &dt
			}
			input.FeeProofDocPath = strPtr(req.Fee.ProofDocPath)
			input.FeeShortfallNote = strPtr(req.Fee.ShortfallNote)

			switch {
			case paid.IsZero():
				input.FeeStatus = "not_paid"
			case paid.LessThan(feeAmountDue):
				input.FeeStatus = "shortfall"
			default:
				input.FeeStatus = "paid"
			}
		} else if feeRequired {
			input.FeeStatus = "not_paid"
		}

		var cerr error
		created, cerr = h.Applications.CreateTx(r.Context(), tx, input)
		return cerr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, created)
}

// ─────────── Queue list ───────────

func (h *ApplicationHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	filter := store.ApplicationListFilter{
		Kind:        q.Get("kind"),
		Status:      q.Get("status"),
		FeeStatus:   q.Get("fee_status"),
		Unassigned:  q.Get("unassigned") == "true",
		SearchTerm:  q.Get("q"),
	}
	if v := q.Get("branch_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.BranchID = &id
		}
	}
	if v := q.Get("submitted_by"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.SubmittedBy = &id
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			filter.DateFrom = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
			filter.DateTo = &to
		}
	}
	filter.Limit, _ = strconv.Atoi(q.Get("limit"))
	filter.Offset, _ = strconv.Atoi(q.Get("offset"))

	var items []domain.MembershipApplication
	var total int
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Applications.ListTx(r.Context(), tx, filter)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// ─────────── Detail ───────────

type applicationDetailResp struct {
	Application       *domain.MembershipApplication  `json:"application"`
	ChecklistItems    []domain.ChecklistItem         `json:"checklist_items"`
	ChecklistResponses []domain.ChecklistResponse    `json:"checklist_responses"`
	CorrectionHistory []domain.CorrectionEvent       `json:"correction_history"`
}

func (h *ApplicationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var resp applicationDetailResp
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.Application = app
		items, err := h.Applications.ListChecklistItemsTx(r.Context(), tx, app.Kind)
		if err != nil {
			return err
		}
		resp.ChecklistItems = items
		responses, err := h.Applications.ListChecklistResponsesTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.ChecklistResponses = responses
		history, err := h.Applications.ListCorrectionsTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.CorrectionHistory = history
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrApplicationNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("application not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── Transition helpers ───────────

type transitionReq struct {
	Note          string `json:"note,omitempty"`
	DeclineReason string `json:"decline_reason,omitempty"`
	Conditions    string `json:"conditions,omitempty"`
}

func (h *ApplicationHandler) transition(
	w http.ResponseWriter, r *http.Request,
	to domain.ApplicationStatus,
	requireNote bool,
	requireReason bool,
	correctionEvent string, // "" | "returned" | "resubmitted"
) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var req transitionReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	if requireNote && strings.TrimSpace(req.Note) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("note is required"))
		return
	}
	if requireReason && strings.TrimSpace(req.DeclineReason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decline_reason is required"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	var updated *domain.MembershipApplication
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		cur, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		updated, err = h.Applications.TransitionTx(r.Context(), tx, store.TransitionInput{
			ID:            cur.ID,
			From:          cur.Status,
			To:            to,
			ActorUserID:   actorID,
			Note:          req.Note,
			DeclineReason: req.DeclineReason,
			Conditions:    req.Conditions,
		})
		if err != nil {
			return err
		}
		if correctionEvent != "" {
			if err := h.Applications.AppendCorrectionTx(r.Context(), tx, id, correctionEvent, req.Note, actorID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrApplicationNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("application not found"))
		case errors.Is(err, domain.ErrIllegalAppTransition):
			httpx.WriteErr(w, r, httpx.ErrConflict("illegal status transition"))
		default:
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.OK(w, updated)
}

func (h *ApplicationHandler) StartReview(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusUnderReview, false, false, "")
}
func (h *ApplicationHandler) ReturnForCorrection(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusReturnedForCorrection, true, false, "returned")
}
func (h *ApplicationHandler) Resubmit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusSubmitted, true, false, "resubmitted")
}
func (h *ApplicationHandler) SubmitForApproval(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusReviewedPendingApp, false, false, "")
}
// Approve is the activation entry point. The status flip + member
// materialisation + share/savings account creation run in a single
// tenant-scoped tx so a halfway failure rolls everything back. The
// fee GL post + welcome notification run AFTER the tx commits — they
// can fail without unwinding the activation (the fee post is
// idempotent on source_ref so a retry is safe).
func (h *ApplicationHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var req transitionReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	var updated *domain.MembershipApplication
	var activation *store.ActivationResult
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		cur, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}

		// Read membership + share-policy settings the activation needs.
		var defaultDepositProductID *uuid.UUID
		if err := tx.QueryRow(r.Context(),
			`SELECT default_deposit_product_id FROM tenant_membership WHERE tenant_id = $1`,
			tenantID,
		).Scan(&defaultDepositProductID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var parValue decimal.Decimal
		if err := tx.QueryRow(r.Context(),
			`SELECT share_par_value FROM tenant_operations WHERE tenant_id = $1`,
			tenantID,
		).Scan(&parValue); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			// tenant_operations may not be initialised in test envs —
			// fall back to 100 (the default in the schema) so activation
			// doesn't bounce in test runs.
			parValue = decimal.NewFromInt(100)
		}

		// 1. Status flip.
		u, err := h.Applications.TransitionTx(r.Context(), tx, store.TransitionInput{
			ID:           cur.ID,
			From:         cur.Status,
			To:           domain.AppStatusApprovedActive,
			ActorUserID:  actorID,
			Conditions:   req.Conditions,
		})
		if err != nil {
			return err
		}
		updated = u

		// 2. In-tx materialisation. Order matters per kind:
		//
		//   institutional → ActivateApplicationTx (inserts org_members
		//     with no auto-opened savings accounts) → create CP →
		//     stamp materialized_counterparty_id. Safe single-call
		//     because there are no child rows whose BEFORE INSERT
		//     trigger depends on org_members.counterparty_id.
		//
		//   individual → 3 phases interleaved with the CP creation:
		//     (a) MaterialiseIndividualMemberTx — insert member row only
		//     (b) createCounterpartyFromApplicationTx — create CP +
		//         SetCounterpartyOnMemberTx (stamps members.counterparty_id)
		//     (c) OpenDefaultIndividualAccountsTx — insert share +
		//         optional deposit accounts. By the time the BEFORE
		//         INSERT trigger fires here, members.counterparty_id
		//         is already populated, so the per-row bridge gets
		//         filled in correctly. Skipping the split here is
		//         what causes the "share_account.counterparty_id = NULL"
		//         data corruption that sub-PR 1's read switchover
		//         can't see past. Failure at any phase rolls back
		//         the whole approval via tx rollback.
		var newCPID uuid.UUID
		if updated.Kind == domain.ApplicationKindInstitutional {
			act, err := h.Applications.ActivateApplicationTx(
				r.Context(), tx, updated, h.Members, h.Orgs,
				defaultDepositProductID, parValue, actorID,
			)
			if err != nil {
				return err
			}
			activation = act
			if h.Counterparties != nil {
				freshOrg, gerr := h.Orgs.ByIDTx(r.Context(), tx, act.OrgID)
				if gerr != nil {
					return fmt.Errorf("reload org for counterparty co-create: %w", gerr)
				}
				cpID, cerr := createCounterpartyForOrgTx(r.Context(), tx, h.Counterparties, tenantID, freshOrg, actorID)
				if cerr != nil {
					return fmt.Errorf("create counterparty (org): %w", cerr)
				}
				newCPID = cpID
			}
		} else {
			memberID, memberNo, err := h.Applications.MaterialiseIndividualMemberTx(
				r.Context(), tx, updated, h.Members, actorID,
			)
			if err != nil {
				return err
			}
			if h.Counterparties != nil {
				cpID, cerr := h.createCounterpartyFromApplicationTx(r.Context(), tx, tenantID, memberID, updated, actorID)
				if cerr != nil {
					return fmt.Errorf("create counterparty (individual): %w", cerr)
				}
				newCPID = cpID
			}
			act, err := h.Applications.OpenDefaultIndividualAccountsTx(
				r.Context(), tx, updated, memberID, memberNo,
				defaultDepositProductID, parValue, actorID,
			)
			if err != nil {
				return err
			}
			activation = act
		}

		// 2b. Stamp the application's materialized_counterparty_id
		// bridge if a counterparty was created above.
		if newCPID != uuid.Nil {
			if _, err := tx.Exec(r.Context(),
				`UPDATE membership_applications SET materialized_counterparty_id = $2 WHERE id = $1`,
				updated.ID, newCPID,
			); err != nil {
				return fmt.Errorf("stamp materialized_counterparty_id: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrApplicationNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("application not found"))
		case errors.Is(err, domain.ErrIllegalAppTransition):
			httpx.WriteErr(w, r, httpx.ErrConflict("illegal status transition"))
		default:
			h.Logger.Error("approve+activate", "app", id, "err", err)
			httpx.WriteErr(w, r, err)
		}
		return
	}

	// 3. Post the registration fee to the GL (best-effort, idempotent).
	if updated.FeeRequired && updated.FeeAmountPaid.GreaterThan(decimal.Zero) {
		if jeID := h.postFeeToGL(r, tenantID, updated, actorID, false); jeID != uuid.Nil {
			_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
				return h.Applications.SetFeeJournalEntryTx(r.Context(), tx, updated.ID, jeID)
			})
			updated.FeeJournalEntryID = &jeID
		}
	}

	// 4. Welcome notification (best-effort).
	if h.Notifier != nil && activation != nil {
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tenantID,
			EventCode:         "MEMBER_WELCOME",
			Priority:          "normal",
			Channels:          []notifier.Channel{notifier.ChannelSMS, notifier.ChannelEmail, notifier.ChannelInApp},
			RecipientMemberID: &activation.MemberID,
			RecipientName:     updated.ApplicantName,
			RecipientPhone:    updated.PrimaryPhone,
			RecipientEmail:    updated.PrimaryEmail,
			SourceModule:      strPtrConst("member.onboarding.activation"),
			SourceRecordID:    &updated.ID,
			Payload: map[string]any{
				"member_no":         activation.MemberNo,
				"share_account_no":  activation.ShareAccountNo,
				"deposit_account_no": derefStringPtr(activation.DepositAccountNo),
				"applicant_name":    updated.ApplicantName,
				"application_no":    updated.ApplicationNo,
			},
		})
	}

	// Re-fetch so the response reflects materialized_* + fee JE id.
	final, _ := h.refreshAfterActivation(r, tenantID, updated.ID)
	if final != nil {
		httpx.OK(w, map[string]any{
			"application": final,
			"activation":  activation,
		})
		return
	}
	httpx.OK(w, map[string]any{
		"application": updated,
		"activation":  activation,
	})
}

func (h *ApplicationHandler) refreshAfterActivation(r *http.Request, tenantID, id uuid.UUID) (*domain.MembershipApplication, error) {
	var fresh *domain.MembershipApplication
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		fresh, err = h.Applications.GetTx(r.Context(), tx, id)
		return err
	})
	return fresh, err
}

// postFeeToGL builds the registration-fee journal entry and posts it
// via the accounting client. Returns the entry id on success or
// uuid.Nil on disabled-client / failure (which is logged).
//
// Direction:
//   forward (refund=false): DR channel-cash / CR 4080 Registration Fee Income
//   reversal (refund=true): DR 4080 / CR channel-cash
func (h *ApplicationHandler) postFeeToGL(r *http.Request, tenantID uuid.UUID, app *domain.MembershipApplication, actorID uuid.UUID, refund bool) uuid.UUID {
	if h.Accounting == nil || app.FeeAmountPaid.IsZero() {
		return uuid.Nil
	}
	channel := ""
	if app.FeePaymentChannel != nil {
		channel = *app.FeePaymentChannel
	}
	cashAcct := registrationChannelCashAccount(channel)
	module := "member.onboarding.registration_fee"
	sourceRef := "app-" + app.ID.String()
	narration := "Registration fee · " + app.ApplicationNo + " · " + app.ApplicantName
	var lines []accounting.Line
	if refund {
		sourceRef = "app-" + app.ID.String() + "-refund"
		narration = "Registration fee REFUND · " + app.ApplicationNo + " · " + app.ApplicantName
		lines = []accounting.Line{
			{AccountCode: "4080", Debit: app.FeeAmountPaid, Narration: "Reverse registration fee income"},
			{AccountCode: cashAcct, Credit: app.FeeAmountPaid, Narration: "Cash paid back to applicant"},
		}
	} else {
		lines = []accounting.Line{
			{AccountCode: cashAcct, Debit: app.FeeAmountPaid, Narration: "Registration fee received"},
			{AccountCode: "4080", Credit: app.FeeAmountPaid, Narration: "Registration fee income"},
		}
	}
	res, err := h.Accounting.Post(r.Context(), accounting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: module,
		SourceRef:    sourceRef,
		Narration:    narration,
		Lines:        lines,
	})
	if err != nil {
		if !errors.Is(err, accounting.ErrDisabled) {
			h.Logger.Error("post registration fee to GL",
				"app", app.ID, "refund", refund, "err", err)
		}
		return uuid.Nil
	}
	_ = actorID
	return res.EntryID
}

// registrationChannelCashAccount maps a payment channel to its
// matching cash GL account. Mirrors the savings deposit posting
// channel→CoA helper.
// PostRefund — for declined applications with a paid registration
// fee that the tenant has marked refundable. Posts the reversal
// journal entry and flips fee_status to 'refunded'. Idempotent on
// the application id.
func (h *ApplicationHandler) PostRefund(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	if actorID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	var app *domain.MembershipApplication
	var refundable bool
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		a, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		app = a
		return tx.QueryRow(r.Context(),
			`SELECT COALESCE(fee_refundable_on_rejection, true) FROM tenant_membership WHERE tenant_id = $1`,
			tenantID,
		).Scan(&refundable)
	})
	if err != nil {
		if errors.Is(err, store.ErrApplicationNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("application not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if app.Status != domain.AppStatusDeclined {
		httpx.WriteErr(w, r, httpx.ErrConflict("refund only allowed on declined applications"))
		return
	}
	if !app.FeeRequired || app.FeeAmountPaid.IsZero() {
		httpx.WriteErr(w, r, httpx.ErrConflict("no registration fee was collected — nothing to refund"))
		return
	}
	if !refundable {
		httpx.WriteErr(w, r, httpx.ErrConflict("tenant has marked the registration fee non-refundable"))
		return
	}
	if app.FeeRefundJournalEntryID != nil {
		// Idempotent — refund already posted.
		httpx.OK(w, app)
		return
	}

	jeID := h.postFeeToGL(r, tenantID, app, actorID, true)
	if jeID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Applications.SetFeeRefundJournalEntryTx(r.Context(), tx, app.ID, jeID)
	})
	final, _ := h.refreshAfterActivation(r, tenantID, app.ID)
	if final != nil {
		httpx.OK(w, final)
		return
	}
	httpx.OK(w, app)
}

func registrationChannelCashAccount(channel string) string {
	switch channel {
	case "mpesa":
		return "1030"
	case "airtel_money":
		return "1040"
	case "bank_transfer":
		return "1020"
	case "cheque":
		return "1020"
	default: // cash + unknown fallback
		return "1000"
	}
}

func strPtrConst(s string) *string { return &s }

func derefStringPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (h *ApplicationHandler) Decline(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusDeclined, false, true, "")
}
func (h *ApplicationHandler) ReturnToReviewer(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusUnderReview, true, false, "")
}
func (h *ApplicationHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, domain.AppStatusWithdrawn, true, false, "")
}

// ─────────── Checklist ───────────

func (h *ApplicationHandler) ListChecklistItems(w http.ResponseWriter, r *http.Request) {
	kind := domain.ApplicationKind(strings.ToLower(r.URL.Query().Get("kind")))
	if !kind.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind must be 'individual' or 'institutional'"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var items []domain.ChecklistItem
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		items, err = h.Applications.ListChecklistItemsTx(r.Context(), tx, kind)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type checklistResponseReq struct {
	Code     string `json:"code"`
	Response string `json:"response"` // confirmed | flagged | n/a
	Note     string `json:"note,omitempty"`
}

func (h *ApplicationHandler) RespondChecklist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var req checklistResponseReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Code == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code is required"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	tenantID, _ := middleware.TenantIDFrom(r)

	var resp *domain.ChecklistResponse
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		resp, err = h.Applications.UpsertChecklistResponseTx(r.Context(), tx, id, req.Code, req.Response, req.Note, actorID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── helpers ───────────

func strPtr(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// createCounterpartyFromApplicationTx runs inside the Approve
// transaction. Creates a counterparty (kind=individual) that
// shadows the freshly-materialised member row, then stamps
// members.counterparty_id with the new id so the bridge is wired
// before commit. The `applicant_payload` JSONB on the application
// is the source of truth for the individual{...} bag — everything
// we wrote into the member row came from that payload, so reusing
// it here keeps a single canonical shape. Returns the new
// counterparty id so the caller can stamp
// membership_applications.materialized_counterparty_id.
func (h *ApplicationHandler) createCounterpartyFromApplicationTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, memberID uuid.UUID,
	app *domain.MembershipApplication,
	actorID uuid.UUID,
) (uuid.UUID, error) {
	// MembershipApplication.ApplicantPayload is already json.RawMessage
	// (free-form bag the FE captured at submission). We pass it through
	// as either the individual or institution slot depending on kind —
	// the same bag, the application-side schema (domain.ApplicantPayload)
	// is the documented decoder.
	payload := app.ApplicantPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	// Build a minimal contact bag from the application-level fields
	// the materialise step already considered authoritative.
	contactBytes, _ := json.Marshal(map[string]any{
		"phone": valOrEmpty(app.PrimaryPhone),
		"email": valOrEmpty(app.PrimaryEmail),
	})

	// Kind: 'individual' or — for institutional apps — a best-effort
	// guess from the application's free-form payload. Default 'other'.
	kind := domain.CounterpartyIndividual
	if app.Kind == domain.ApplicationKindInstitutional {
		kind = guessInstitutionalKind(app.ApplicantName, payload)
	}

	legacyNo := app.ApplicationNo
	cp, err := h.Counterparties.CreateTx(ctx, tx, store.CreateInput{
		TenantID:    tenantID,
		LegacyID:    &legacyNo,
		Kind:        kind,
		DisplayName: app.ApplicantName,
		Status:      domain.CPStatusActive,
		KYCState:    domain.CPKYCVerified,
		RiskBand:    domain.CPRiskNA,
		Individual:  payloadIfIndividual(kind, payload),
		Institution: payloadIfInstitutional(kind, payload),
		Contact:     contactBytes,
		CreatedBy:   ptrUUIDLocal(actorID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.Counterparties.SetCounterpartyOnMemberTx(ctx, tx, memberID, cp.ID); err != nil {
		return uuid.Nil, err
	}
	return cp.ID, nil
}

func valOrEmpty(p *string) string {
	if p == nil { return "" }
	return *p
}

func ptrUUIDLocal(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil { return nil }
	return &u
}

func payloadIfIndividual(k domain.CounterpartyKind, raw json.RawMessage) json.RawMessage {
	if k == domain.CounterpartyIndividual { return raw }
	return nil
}

func payloadIfInstitutional(k domain.CounterpartyKind, raw json.RawMessage) json.RawMessage {
	if k != domain.CounterpartyIndividual { return raw }
	return nil
}

// guessInstitutionalKind picks a counterparty_kind for an
// institutional application by sniffing the display name + payload
// JSON for shape hints. Defaults to 'other' so the CHECK constraint
// passes; a tenant can refine via PATCH after the fact.
func guessInstitutionalKind(name string, payload json.RawMessage) domain.CounterpartyKind {
	hint := strings.ToLower(name + " " + string(payload))
	switch {
	case strings.Contains(hint, "chama") || strings.Contains(hint, " group"):
		return domain.CounterpartyChama
	case strings.Contains(hint, "church") || strings.Contains(hint, "parish"):
		return domain.CounterpartyChurch
	case strings.Contains(hint, "school") || strings.Contains(hint, "academy"):
		return domain.CounterpartySchool
	case strings.Contains(hint, "ngo") || strings.Contains(hint, "foundation"):
		return domain.CounterpartyNGO
	case strings.Contains(hint, "limited") || strings.Contains(hint, "ltd") || strings.Contains(hint, "company"):
		return domain.CounterpartyCompany
	default:
		return domain.CounterpartyOther
	}
}
