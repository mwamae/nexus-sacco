// Loans Phase 4 — collections workflow HTTP surface.
//
// Mounted under /v1/loans/{loan_id}/collections/* plus two tenant-wide
// reads (/v1/loans/collections/queue and /v1/loans/collections/ptp-summary).
//
// Layers on top of the legacy Phase 6e LoanCollectionsStore — re-uses
// EnsureCaseForLoanTx, CreatePTPTx, LogContactTx; adds the Phase 4
// LogEventTx + ReassignTx + UnassignTx + CancelPTPTx + queue methods.
//
// Every operator action writes:
//   1. Whichever legacy row the action implies (contact / PTP / case
//      status change) so the existing /v1/collection-cases/* UI keeps
//      showing accurate state.
//   2. A loan_collection_events row capturing the workflow signal so
//      the Phase 4 timeline + queue can render it.
//
// Permissions:
//   loans:collect          — most actions
//   loans:collect:assign   — assign / unassign
//   loans:collect:legal    — legal handover
//   loans:view             — read-only timeline + queue + PTP summary

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

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type LoanCollectionsEventsHandler struct {
	DB          *db.Pool
	Loans       *store.LoanStore
	Collections *store.LoanCollectionsStore
	Notifier    *notifier.Client
	Logger      *slog.Logger
	// Phase-1 follow-up — single seam for loan_documents writes.
	// Letter PDF persistence routes through this now (the previous
	// inline INSERT wrote to non-existent columns file_path/mime_type
	// — the row was silently failing to persist).
	Docs *store.LoanDocumentStore
}

// ─────────── helpers ───────────

func parseLoanID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, "loan_id"))
}

func mustUser(r *http.Request) (uuid.UUID, *httpx.APIError) {
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		return uuid.Nil, httpx.ErrUnauthorized("user identity required")
	}
	return uid, nil
}

func mustLoan(ctx context.Context, tx pgx.Tx, loans *store.LoanStore, loanID uuid.UUID) (*domain.Loan, error) {
	l, err := loans.GetTx(ctx, tx, loanID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, httpx.ErrNotFound("loan not found")
		}
		return nil, fmt.Errorf("load loan: %w", err)
	}
	return l, nil
}

// ensureCaseTx is a tiny convenience — most action endpoints require a
// case; if one doesn't exist yet (loan came in good standing and the
// officer is logging an early-warning event) we open it now.
func ensureCaseTx(ctx context.Context, tx pgx.Tx, collections *store.LoanCollectionsStore, loan *domain.Loan) (*domain.CollectionCase, error) {
	c, err := collections.EnsureCaseForLoanTx(ctx, tx, loan)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func detailsJSON(m map[string]any) json.RawMessage {
	if m == nil {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(m)
	return b
}

// ─────────── Call ───────────

type logCallReq struct {
	Outcome         string  `json:"outcome"`
	Note            string  `json:"note"`
	DurationSeconds int     `json:"duration_seconds"`
	PromisedAmount  string  `json:"promised_amount"`  // optional
	PromisedDate    string  `json:"promised_date"`    // optional YYYY-MM-DD
}

func (h *LoanCollectionsEventsHandler) LogCall(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	uid, aerr := mustUser(r)
	if aerr != nil {
		httpx.WriteErr(w, r, aerr); return
	}
	var in logCallReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	outcome := normaliseCallOutcome(in.Outcome)
	if outcome == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid call outcome"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	var resp map[string]any
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil {
			return err
		}
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil {
			return err
		}
		notePtr := optStr(in.Note)
		contact, err := h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
			CaseID: c.ID, Kind: domain.ContactCall, Outcome: domain.ContactOutcome(outcome),
			Note: notePtr, ContactedBy: uid,
		}, "call: "+outcome)
		if err != nil {
			return err
		}

		// Phase 4 follow-up — also emit a call_attempt event linked to
		// the contact via source_contact_id. The timeline UNION dedupes
		// contacts that have a referencing event so nothing renders
		// twice; queries against loan_collection_events.kind can now
		// find calls.
		if _, err := h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventCallAttempt, &uid,
			detailsJSON(map[string]any{
				"source_contact_id": contact.ID,
				"outcome":           outcome,
				"duration_seconds":  in.DurationSeconds,
			}),
			nil, nil, nil,
		); err != nil {
			return err
		}

		// If the call yielded a PTP, create it + emit ptp_created event.
		var ptp *domain.PromiseToPay
		if outcome == "promise_made" && in.PromisedAmount != "" && in.PromisedDate != "" {
			amt, err := decimal.NewFromString(in.PromisedAmount)
			if err != nil || !amt.IsPositive() {
				return httpx.ErrBadRequest("promised_amount must be a positive decimal")
			}
			pdate, err := time.Parse("2006-01-02", in.PromisedDate)
			if err != nil {
				return httpx.ErrBadRequest("promised_date must be YYYY-MM-DD")
			}
			created, err := h.Collections.CreatePTPTx(r.Context(), tx, &domain.PromiseToPay{
				CaseID: c.ID, LoanID: loan.ID,
				PromisedAmount: amt, PromisedDate: pdate, CreatedBy: uid, Notes: notePtr,
			})
			if err != nil {
				return err
			}
			ptp = created
			if _, err := h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID, domain.EventPTPCreated,
				&uid,
				detailsJSON(map[string]any{"ptp_id": created.ID, "source": "call"}),
				nil, &amt, &pdate,
			); err != nil {
				return err
			}
		}

		resp = map[string]any{"case_id": c.ID, "ptp": ptp}
		return nil
	})
	if err != nil {
		writeCollErr(w, r, err); return
	}
	httpx.Created(w, resp)
}

// normaliseCallOutcome — accept both Phase 4 names (reached_promised,
// reached_refused, reached_dispute, voicemail) and legacy enum values
// (promise_made, refused, dispute, left_message, etc.). Returns the
// LEGACY enum value (what the contacts table accepts).
func normaliseCallOutcome(s string) string {
	switch strings.ToLower(s) {
	case "reached_promised", "promise_made":
		return "promise_made"
	case "reached_refused", "refused":
		return "refused"
	case "reached_dispute", "dispute":
		return "dispute"
	case "voicemail", "left_message":
		return "left_message"
	case "no_answer":
		return "no_answer"
	case "wrong_number":
		return "wrong_number"
	case "reached":
		return "reached"
	case "busy":
		return "busy"
	}
	return ""
}

// ─────────── Visit ───────────

type logVisitReq struct {
	Outcome   string  `json:"outcome"`
	Note      string  `json:"note"`
	GeoLat    *string `json:"geo_lat,omitempty"`
	GeoLng    *string `json:"geo_lng,omitempty"`
	PhotoPath *string `json:"photo_path,omitempty"`
}

func (h *LoanCollectionsEventsHandler) LogVisit(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in logVisitReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	outcome := normaliseVisitOutcome(in.Outcome)
	if outcome == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid visit outcome"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		notePtr := optStr(in.Note)
		var lat, lng *decimal.Decimal
		if in.GeoLat != nil {
			if v, err := decimal.NewFromString(*in.GeoLat); err == nil { lat = &v }
		}
		if in.GeoLng != nil {
			if v, err := decimal.NewFromString(*in.GeoLng); err == nil { lng = &v }
		}
		contact, err := h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
			CaseID: c.ID, Kind: domain.ContactVisit, Outcome: domain.ContactOutcome(outcome),
			Note: notePtr, GPSLat: lat, GPSLng: lng, ContactedBy: uid,
		}, "visit: "+outcome)
		if err != nil {
			return err
		}
		// Phase 4 follow-up — also emit a field_visit event linked
		// back to the contact. Timeline dedupes via source_contact_id.
		evDetails := map[string]any{
			"source_contact_id": contact.ID,
			"outcome":           outcome,
		}
		if in.GeoLat != nil && in.GeoLng != nil {
			evDetails["geo_lat"] = *in.GeoLat
			evDetails["geo_lng"] = *in.GeoLng
		}
		if in.PhotoPath != nil {
			evDetails["photo_path"] = *in.PhotoPath
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventFieldVisit, &uid,
			detailsJSON(evDetails),
			nil, nil, nil,
		)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	w.WriteHeader(http.StatusCreated)
}

func normaliseVisitOutcome(s string) string {
	switch strings.ToLower(s) {
	case "found_promised", "promise_made":
		return "promise_made"
	case "found_refused", "refused":
		return "refused"
	case "found_dispute", "dispute":
		return "dispute"
	case "not_found_home", "visited_not_home":
		return "visited_not_home"
	// Phase 4 follow-up — migration 0043 added these as first-class
	// loan_contact_outcome enum values; no longer collapsed.
	case "not_found_work":
		return "not_found_work"
	case "moved":
		return "moved"
	}
	return ""
}

// ─────────── Note ───────────

type noteReq struct {
	Text string `json:"text"`
}

func (h *LoanCollectionsEventsHandler) Note(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in noteReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if strings.TrimSpace(in.Text) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("text required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventNote, &uid,
			detailsJSON(map[string]any{"text": in.Text}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	w.WriteHeader(http.StatusCreated)
}

// ─────────── PTP ───────────

type ptpCreateReq struct {
	PromisedAmount string `json:"promised_amount"`
	PromisedDate   string `json:"promised_date"`
	Note           string `json:"note"`
}

func (h *LoanCollectionsEventsHandler) CreatePTP(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in ptpCreateReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	amt, err := decimal.NewFromString(in.PromisedAmount)
	if err != nil || !amt.IsPositive() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("promised_amount must be a positive decimal"))
		return
	}
	pdate, err := time.Parse("2006-01-02", in.PromisedDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("promised_date must be YYYY-MM-DD"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var ptp *domain.PromiseToPay
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		notePtr := optStr(in.Note)
		created, err := h.Collections.CreatePTPTx(r.Context(), tx, &domain.PromiseToPay{
			CaseID: c.ID, LoanID: loan.ID,
			PromisedAmount: amt, PromisedDate: pdate, CreatedBy: uid, Notes: notePtr,
		})
		if err != nil { return err }
		ptp = created
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventPTPCreated, &uid,
			detailsJSON(map[string]any{"ptp_id": created.ID}),
			nil, &amt, &pdate)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.Created(w, ptp)
}

type ptpCancelReq struct {
	Reason string `json:"reason"`
}

func (h *LoanCollectionsEventsHandler) CancelPTP(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	ptpID, err := uuid.Parse(chi.URLParam(r, "ptp_id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid ptp_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in ptpCancelReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var ptp *domain.PromiseToPay
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		ptp, err = h.Collections.CancelPTPTx(r.Context(), tx, ptpID, in.Reason, uid)
		if err != nil { return err }
		// Defensive: ensure the ptp belongs to the loan in the URL.
		if ptp.LoanID != loanID {
			return httpx.ErrBadRequest("ptp_id does not belong to this loan")
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loanID, &ptp.CaseID,
			domain.EventPTPCancelled, &uid,
			detailsJSON(map[string]any{"ptp_id": ptp.ID, "reason": in.Reason}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.OK(w, ptp)
}

// ─────────── Escalation ───────────

type escalateReq struct {
	ToRole string `json:"to_role"`
	Reason string `json:"reason"`
}

func (h *LoanCollectionsEventsHandler) Escalate(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in escalateReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if in.ToRole == "" || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to_role and reason are required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		// Bump case priority.
		if _, err := tx.Exec(r.Context(), `
			UPDATE loan_collection_cases
			   SET priority = LEAST(100, priority + 10),
			       last_action = $2
			 WHERE id = $1
		`, c.ID, "escalated to "+in.ToRole); err != nil {
			return err
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventEscalation, &uid,
			detailsJSON(map[string]any{"to_role": in.ToRole, "reason": in.Reason}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	w.WriteHeader(http.StatusCreated)
}

// ─────────── Legal handover ───────────

type legalHandoverReq struct {
	Reason       string   `json:"reason"`
	AttachedDocs []string `json:"attached_docs"`
}

func (h *LoanCollectionsEventsHandler) LegalHandover(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in legalHandoverReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		// Flip the case status to escalated_legal.
		if _, err := tx.Exec(r.Context(), `
			UPDATE loan_collection_cases
			   SET status = 'escalated_legal', last_action = 'handed to legal'
			 WHERE id = $1
		`, c.ID); err != nil {
			return err
		}
		// Open a legal case stub (Phase 6e table).
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO loan_legal_cases (
			  tenant_id, loan_id, collection_case_id,
			  legal_firm, instruction_date, status, notes, created_by
			) VALUES (current_tenant_id(), $1, $2, 'INTERNAL', CURRENT_DATE, 'demand_letter_sent', $3, $4)
		`, loan.ID, c.ID, in.Reason, uid); err != nil {
			return err
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventLegalHandover, &uid,
			detailsJSON(map[string]any{"reason": in.Reason, "attached_docs": in.AttachedDocs}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	w.WriteHeader(http.StatusCreated)
}

// ─────────── SMS (manual) ───────────

type sendSMSReq struct {
	Message string `json:"message"`
}

func (h *LoanCollectionsEventsHandler) SendSMS(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in sendSMSReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if strings.TrimSpace(in.Message) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("message required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var caseID uuid.UUID
	var counterpartyID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		caseID = c.ID
		counterpartyID = loan.CounterpartyID

		contact, err := h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
			CaseID: c.ID, Kind: domain.ContactSMS, Outcome: domain.OutcomeReached,
			Note: &in.Message, ContactedBy: uid,
		}, "sms sent")
		if err != nil { return err }
		// Phase 4 follow-up — distinct manual_sms event kind (was
		// previously auto_sms with payload.trigger='manual', which made
		// queries grouping by kind blur officer + system sends).
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventManualSMS, &uid,
			detailsJSON(map[string]any{
				"source_contact_id": contact.ID,
				"message":           in.Message,
			}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }

	// Best-effort fire-and-forget notification dispatch — never blocks.
	if h.Notifier != nil {
		go h.Notifier.Notify(context.Background(), notifier.Request{
			TenantID:          tid,
			EventCode:         "loan.collections.manual_sms",
			Channels:          []notifier.Channel{notifier.ChannelSMS},
			RecipientMemberID: &counterpartyID,
			SourceModule:      strPtr("savings.collections"),
			SourceRecordID:    &caseID,
			Payload:           map[string]any{"message": in.Message, "loan_id": loanID},
			InitiatedBy:       &uid,
		})
	}
	w.WriteHeader(http.StatusCreated)
}

// ─────────── Letter (PDF) ───────────

type letterReq struct {
	Kind     string `json:"kind"`
	Delivery string `json:"delivery"` // email | physical
}

func (h *LoanCollectionsEventsHandler) GenerateLetter(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in letterReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	kind := domain.CollectionLetterKind(in.Kind)
	if !kind.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind must be pre_collection | demand | final_demand | legal_notice"))
		return
	}
	if in.Delivery == "" { in.Delivery = "email" }
	if in.Delivery != "email" && in.Delivery != "physical" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("delivery must be email or physical"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	if h.Notifier == nil || h.Notifier.BaseURL == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("notifier client disabled — cannot generate letter PDF in this environment"))
		return
	}

	var counterpartyID uuid.UUID
	var caseID uuid.UUID
	var payload map[string]any

	if err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		caseID = c.ID
		counterpartyID = loan.CounterpartyID
		var memberName, tenantName string
		_ = tx.QueryRow(r.Context(),
			`SELECT cd.full_name FROM counterparty_directory cd WHERE cd.counterparty_id = $1`,
			loan.CounterpartyID).Scan(&memberName)
		_ = tx.QueryRow(r.Context(),
			`SELECT name FROM tenants WHERE id = $1`, tid).Scan(&tenantName)
		payload = map[string]any{
			"tenant_name":       tenantName,
			"member_name":       memberName,
			"loan_no":           loan.LoanNo,
			"dpd_days":          loan.DaysPastDue,
			"principal_balance": loan.PrincipalBalance.StringFixed(2),
			"interest_balance":  loan.InterestBalance.StringFixed(2),
			"penalty_balance":   loan.PenaltyBalance.StringFixed(2),
			"total_outstanding": loan.PrincipalBalance.
				Add(loan.InterestBalance).Add(loan.FeesBalance).Add(loan.PenaltyBalance).StringFixed(2),
			"letter_kind":  string(kind),
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		}
		return nil
	}); err != nil {
		writeCollErr(w, r, err); return
	}

	// Render the PDF via the notifier (the notification service owns
	// the templates). Document type aligns to "loan_collections_letter"
	// — Phase 4 wires the templates in S6.
	pdfResp, err := h.Notifier.GeneratePDF(r.Context(), notifier.PDFGenerateRequest{
		TenantID:        tid,
		DocumentType:    "loan_" + kind.LoanDocKind(),
		SubjectLoanID:   &loanID,
		SubjectLabel:    "collections letter: " + string(kind),
		Payload:         payload,
		GeneratedBy:     &uid,
	})
	if err != nil {
		// PDF unavailable → degrade: still log the intent so the
		// timeline shows we tried.
		h.Logger.Warn("collections letter pdf render failed; logging event without document", "loan_id", loanID, "err", err)
	}

	// Persist the loan_documents row + emit event + log contact, all in one tx.
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Insert document only if PDF rendered. Uses the central store
		// so the supersede + expiry plumbing stays consistent with
		// every other loan_documents writer.
		if pdfResp != nil && h.Docs != nil {
			desc := fmt.Sprintf("Collections letter: %s", string(kind))
			if _, derr := h.Docs.InsertTx(r.Context(), tx, store.InsertInput{
				LoanID:      &loanID,
				Kind:        domain.LoanDocKind(kind.LoanDocKind()),
				Description: &desc,
				StoragePath: pdfResp.StoragePath,
				Mime:        "application/pdf",
				SizeBytes:   0, // notification service didn't return size
				UploadedBy:  uid,
			}); derr != nil {
				return derr
			}
		}
		contact, err := h.Collections.LogContactTx(r.Context(), tx, &domain.CollectionContact{
			CaseID: caseID, Kind: domain.ContactLetter, Outcome: domain.OutcomeReached,
			Note: strPtr("letter: " + string(kind)), ContactedBy: uid,
		}, "letter: "+string(kind))
		if err != nil { return err }

		letterKindCopy := kind
		details := map[string]any{
			"kind":              string(kind),
			"delivery":          in.Delivery,
			"source_contact_id": contact.ID, // Phase 4 follow-up — timeline dedupe pointer
		}
		if pdfResp != nil {
			details["document_id"] = pdfResp.ID
			details["storage_path"] = pdfResp.StoragePath
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loanID, &caseID,
			domain.EventLetterGenerated, &uid,
			detailsJSON(details),
			&letterKindCopy, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }

	if in.Delivery == "email" && h.Notifier != nil {
		go h.Notifier.Notify(context.Background(), notifier.Request{
			TenantID:          tid,
			EventCode:         "loan.collections.letter_email",
			Channels:          []notifier.Channel{notifier.ChannelEmail},
			RecipientMemberID: &counterpartyID,
			SourceModule:      strPtr("savings.collections"),
			SourceRecordID:    &caseID,
			Payload:           payload,
			InitiatedBy:       &uid,
		})
	}

	httpx.Created(w, map[string]any{
		"kind":     string(kind),
		"delivery": in.Delivery,
		"pdf":      pdfResp,
	})
}

// ─────────── Assignment ───────────

type assignLoanReq struct {
	OfficerID string `json:"officer_id"`
}

func (h *LoanCollectionsEventsHandler) Assign(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in assignLoanReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	officerID, err := uuid.Parse(in.OfficerID)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("officer_id must be a UUID")); return }
	tid, _ := middleware.TenantIDFrom(r)
	var asgn *domain.LoanAssignment
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := mustLoan(r.Context(), tx, h.Loans, loanID)
		if err != nil { return err }
		c, err := ensureCaseTx(r.Context(), tx, h.Collections, loan)
		if err != nil { return err }
		asgn, err = h.Collections.ReassignTx(r.Context(), tx, c.ID, loan.ID, officerID, uid)
		if err != nil { return err }
		_, err = h.Collections.LogEventTx(r.Context(), tx, loan.ID, &c.ID,
			domain.EventAssigned, &uid,
			detailsJSON(map[string]any{"officer_id": officerID}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.Created(w, asgn)
}

type unassignReq struct {
	Reason string `json:"reason"`
}

func (h *LoanCollectionsEventsHandler) Unassign(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	uid, aerr := mustUser(r)
	if aerr != nil { httpx.WriteErr(w, r, aerr); return }
	var in unassignReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if in.Reason == "" { in.Reason = "manual unassign" }
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collections.GetCaseByLoanTx(r.Context(), tx, loanID)
		if err != nil { return err }
		if err := h.Collections.UnassignTx(r.Context(), tx, c.ID, uid, in.Reason); err != nil {
			return err
		}
		_, err = h.Collections.LogEventTx(r.Context(), tx, loanID, &c.ID,
			domain.EventUnassigned, &uid,
			detailsJSON(map[string]any{"reason": in.Reason}),
			nil, nil, nil)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Timeline (events for a loan) ───────────

func (h *LoanCollectionsEventsHandler) Events(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseLoanID(r)
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	tid, _ := middleware.TenantIDFrom(r)
	var events []domain.CollectionEvent
	var contacts []domain.CollectionContact
	var ptps []domain.PromiseToPay
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collections.GetCaseByLoanTx(r.Context(), tx, loanID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		events, err = h.Collections.EventsByLoanTx(r.Context(), tx, loanID, 200)
		if err != nil { return err }
		if c != nil {
			contacts, err = h.Collections.ContactsByCaseTx(r.Context(), tx, c.ID)
			if err != nil { return err }
			ptps, err = h.Collections.PTPsByCaseTx(r.Context(), tx, c.ID)
			if err != nil { return err }
		}
		return nil
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.OK(w, map[string]any{
		"events":   events,
		"contacts": contacts,
		"ptps":     ptps,
	})
}

// ─────────── Queue ───────────

func (h *LoanCollectionsEventsHandler) Queue(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	f := store.QueueFilter{
		PTPStatus: q.Get("ptp_status"),
	}
	if s := q.Get("officer_id"); s != "" {
		if u, err := uuid.Parse(s); err == nil {
			f.OfficerID = &u
		}
	}
	if q.Get("unassigned") == "true" {
		f.Unassigned = true
	}
	if s := q.Get("dpd_min"); s != "" {
		if n, err := strconv.Atoi(s); err == nil { f.DPDMin = &n }
	}
	if s := q.Get("dpd_max"); s != "" {
		if n, err := strconv.Atoi(s); err == nil { f.DPDMax = &n }
	}
	if s := q.Get("product_id"); s != "" {
		if u, err := uuid.Parse(s); err == nil { f.ProductID = &u }
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil { f.Limit = n }
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil { f.Offset = n }
	}
	var rows []store.QueueRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Collections.QueueTx(r.Context(), tx, f)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": rows, "total": len(rows)})
}

func (h *LoanCollectionsEventsHandler) PTPSummary(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var sum *store.PTPSummary
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		sum, err = h.Collections.PTPSummaryTx(r.Context(), tx)
		return err
	})
	if err != nil { writeCollErr(w, r, err); return }
	httpx.OK(w, sum)
}

// ─────────── helpers ───────────

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strPtr(s string) *string { return &s }

func writeCollErr(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		httpx.WriteErr(w, r, apiErr); return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound("not found"))
	case errors.Is(err, domain.ErrPTPNotOpen):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	case errors.Is(err, domain.ErrInvalidEventKind):
		httpx.WriteErr(w, r, httpx.ErrBadRequest(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
