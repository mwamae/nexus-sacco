// Safaricom-facing webhook handlers for the C2B flow (phase 2).
//
// Two routes, both PUBLIC (no JWT bearer; the only credentials are
// the IP allow-list + the per-paybill webhook_token in the URL):
//
//   POST /v1/c2b/{paybill_id}/validation
//     Safaricom asks "is this account number known?" We default to
//     accept (ResultCode 0). Per-paybill strict_validation flips us
//     into rejecting unknown accounts with C2B00012.
//
//   POST /v1/c2b/{paybill_id}/confirmation
//     Safaricom hands us the finished transaction. Persist the raw
//     body verbatim; run the resolver; write the audit + reconciliation
//     workflow task if unallocated; always 200 on persist (Safaricom
//     would otherwise retry into a busy loop).
//
// URL note: Daraja's portal refuses callback URLs that contain the
// substring "mpesa", so these public routes live OUTSIDE the
// /v1/mpesa/* admin namespace. Internal/staff routes (paybills CRUD,
// events list, b2c requests enqueue) keep the /v1/mpesa prefix.
//
// Idempotency: phase 1's (tenant_id, transaction_id) UNIQUE + an
// ON CONFLICT DO NOTHING upsert absorb Safaricom retries.

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/distribution"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/metrics"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

// WebhookHandler bundles the dependencies. It's separate from
// PaybillHandler because the auth surface (none for webhooks; JWT
// for admin) is wildly different.
type WebhookHandler struct {
	DB             *db.Pool
	Paybills       *store.PaybillStore
	InboundEvents  *store.InboundEventStore
	Resolver       distribution.Lookups
	Audit          *store.AuditStore
	WorkflowClient *workflowclient.Client
	Logger         *slog.Logger
}

// Daraja's wire shape for both validation + confirmation. The keys
// are TitleCased per Safaricom's docs; we tolerate odd casing but
// the canonical fixtures use this layout.
type c2bWebhookPayload struct {
	TransactionType   string `json:"TransactionType"`
	TransID           string `json:"TransID"`
	TransTime         string `json:"TransTime"` // "YYYYMMDDHHMMSS"
	TransAmount       string `json:"TransAmount"`
	BusinessShortCode string `json:"BusinessShortCode"`
	BillRefNumber     string `json:"BillRefNumber"`
	InvoiceNumber     string `json:"InvoiceNumber"`
	OrgAccountBalance string `json:"OrgAccountBalance"`
	ThirdPartyTransID string `json:"ThirdPartyTransID"`
	MSISDN            string `json:"MSISDN"`
	FirstName         string `json:"FirstName"`
	MiddleName        string `json:"MiddleName"`
	LastName          string `json:"LastName"`
}

// daraja result envelope. {0, "Accepted"} for success; non-zero
// codes (C2B00011 / C2B00012 / etc) tell Safaricom to reject the
// transaction on the customer's handset.
type darajaResult struct {
	ResultCode int    `json:"ResultCode"`
	ResultDesc string `json:"ResultDesc"`
}

// ─────────── /v1/c2b/{paybill_id}/validation ───────────

func (h *WebhookHandler) Validation(w http.ResponseWriter, r *http.Request) {
	paybillID, ok := paybillFromPath(r)
	if !ok {
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: invalid paybill id"})
		return
	}
	token := r.URL.Query().Get("token")

	paybill, err := h.Paybills.ByIDAndToken(r.Context(), paybillID, token)
	if err != nil {
		// Wrong token / unknown paybill — same answer, no leak.
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: unauthorised"})
		return
	}

	// Default policy: accept everything. Safaricom expects the
	// confirmation hop to actually classify the payment; validation
	// is just "do you recognise this account number?" Most tenants
	// would rather catch the bad reference at confirmation time
	// (where they have the full transaction to log) than reject at
	// validation and lose the visibility.
	if !paybill.StrictValidation {
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Accepted"})
		return
	}

	// Strict mode: require the bill ref to resolve to a member.
	// Phone-number fallback is intentionally NOT consulted here even
	// when allow_msisdn_fallback is on — strict validation is about
	// the explicit account number the payer typed, not a phone
	// guess.
	var body c2bWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Malformed body → Safaricom shouldn't have sent it; surface
		// as a reject so they don't proceed to confirmation.
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: malformed payload"})
		return
	}
	var decision distribution.Decision
	err = h.DB.WithTenantTx(r.Context(), paybill.TenantID, func(tx pgx.Tx) error {
		d, err := distribution.Resolve(r.Context(), tx, h.Resolver, distribution.Input{
			BillRef:             body.BillRefNumber,
			MSISDN:              body.MSISDN,
			AllowMSISDNFallback: false,
		})
		if err != nil {
			return err
		}
		decision = d
		return nil
	})
	if err != nil {
		// Tx error — let Safaricom retry by sending a generic reject.
		h.Logger.Error("validation resolver tx", "err", err)
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: temporary error"})
		return
	}
	if decision.Via == domain.ViaUnallocated {
		// C2B00012 is the documented "Invalid Account Number" code.
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "C2B00012: Invalid account number"})
		return
	}
	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Accepted"})
}

// ─────────── /v1/c2b/{paybill_id}/confirmation ───────────

func (h *WebhookHandler) Confirmation(w http.ResponseWriter, r *http.Request) {
	paybillID, ok := paybillFromPath(r)
	if !ok {
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: invalid paybill id"})
		return
	}
	token := r.URL.Query().Get("token")

	paybill, err := h.Paybills.ByIDAndToken(r.Context(), paybillID, token)
	if err != nil {
		// Token mismatch: refuse with 401 so audit logs distinguish
		// "Safaricom sent us a body" from "someone is probing".
		http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: unauthorised"}`, http.StatusUnauthorized)
		return
	}

	rawBody, err := readBoundedBody(r)
	if err != nil {
		http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: body too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	var body c2bWebhookPayload
	if err := json.Unmarshal(rawBody, &body); err != nil {
		// Don't 500 — Safaricom would retry. Treat as "we received it"
		// but log the parse failure so operators can repair the row.
		h.Logger.Error("confirmation: malformed body", "err", err, "paybill_id", paybillID)
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
		return
	}

	txTime := parseDarajaTime(body.TransTime)

	// 1. Persist verbatim. ON CONFLICT DO NOTHING absorbs retries.
	var event *domain.InboundEvent
	var inserted bool
	err = h.DB.WithTenantTx(r.Context(), paybill.TenantID, func(tx pgx.Tx) error {
		e, ins, err := h.InboundEvents.RecordTx(r.Context(), tx, store.RecordInboundInput{
			TenantID:        paybill.TenantID,
			PaybillID:       paybill.ID,
			Shortcode:       paybill.Shortcode,
			TransactionID:   body.TransID,
			TransactionTime: txTime,
			Amount:          body.TransAmount,
			MSISDN:          body.MSISDN,
			BillRef:         body.BillRefNumber,
			RawPayload:      rawBody,
		})
		if err != nil {
			return err
		}
		event, inserted = e, ins
		return nil
	})
	if err != nil {
		// Persistence itself failed — surface to Safaricom as a non-
		// success so they retry. This is the ONE branch where we
		// don't 200 (the spec: "Always 200 on success of persistence").
		h.Logger.Error("confirmation persist", "err", err, "paybill_id", paybillID, "trans_id", body.TransID)
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: persistence error"})
		return
	}
	if !inserted {
		// Duplicate. We have already resolved + audited it; just ack.
		h.Logger.Info("confirmation duplicate ignored",
			"trans_id", body.TransID, "paybill_id", paybillID,
			"tenant_id", paybill.TenantID)
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
		return
	}
	metrics.InboundTotal.Inc("received", paybill.TenantID.String())

	// 2. Run the resolver + write resolution + workflow task + audit.
	// Downstream failures are logged but never block the Safaricom ack.
	if err := h.resolveAndWire(r, paybill, event, &body); err != nil {
		h.Logger.Error("post-persist work failed", "err", err, "event_id", event.ID)
		// fallthrough — Safaricom still gets a 200.
	}
	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
}

func (h *WebhookHandler) resolveAndWire(
	r *http.Request,
	paybill *domain.Paybill,
	event *domain.InboundEvent,
	body *c2bWebhookPayload,
) error {
	var decision distribution.Decision
	var wfInstanceID *uuid.UUID
	err := h.DB.WithTenantTx(r.Context(), paybill.TenantID, func(tx pgx.Tx) error {
		d, err := distribution.Resolve(r.Context(), tx, h.Resolver, distribution.Input{
			BillRef:             body.BillRefNumber,
			MSISDN:              body.MSISDN,
			AllowMSISDNFallback: paybill.AllowMSISDNFallback,
		})
		if err != nil {
			return err
		}
		decision = d

		// Update inbound_event with the verdict.
		var memberID *uuid.UUID
		if decision.MemberID != uuid.Nil {
			id := decision.MemberID
			memberID = &id
		}
		// If unallocated, create the reconciliation workflow task
		// inside the same tx so the task and the event are atomic.
		if decision.Via == domain.ViaUnallocated {
			metrics.UnallocatedTotal.Inc(paybill.TenantID.String())
		}
		if decision.Via == domain.ViaUnallocated && h.WorkflowClient != nil {
			id, err := h.WorkflowClient.CreateInstanceTx(r.Context(), tx,
				workflowclient.CreateInstanceInput{
					TenantID:    paybill.TenantID,
					ProcessKind: "mpesa_unallocated_reconciliation",
					SubjectKind: "mpesa_inbound_event",
					SubjectID:   event.ID,
					Summary: fmt.Sprintf(
						"Unallocated M-PESA payment KES %s from %s (paybill %s)",
						body.TransAmount, body.MSISDN, paybill.Label,
					),
					SourceURL: "/accounting/mpesa-reconciliation?event=" + event.ID.String(),
					Context: map[string]any{
						"event_id":      event.ID,
						"transaction":   body.TransID,
						"amount":        body.TransAmount,
						"bill_ref":      body.BillRefNumber,
						"msisdn":        body.MSISDN,
						"paybill_label": paybill.Label,
					},
				})
			if err != nil {
				// A missing definition is a known soft-fail: we
				// still record the event as unallocated, just
				// without a task. Operators can re-resolve from the
				// staff UI.
				if !errors.Is(err, workflowclient.ErrDefinitionNotFound) {
					return err
				}
				h.Logger.Warn("no mpesa_unallocated_reconciliation definition seeded for tenant",
					"tenant_id", paybill.TenantID)
			} else {
				wfInstanceID = &id
			}
		}
		return h.InboundEvents.UpdateResolutionTx(r.Context(), tx, store.UpdateResolutionInput{
			ID:                 event.ID,
			ResolvedMemberID:   memberID,
			ResolvedVia:        decision.Via,
			WorkflowInstanceID: wfInstanceID,
		})
	})
	if err != nil {
		return err
	}

	// Audit AFTER the tx commits — the audit_log table has no RLS
	// and the action is observational, not transactional.
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &paybill.TenantID,
		Action:     "mpesa.inbound_received",
		TargetKind: "mpesa_inbound_event",
		TargetID:   event.ID.String(),
		Metadata: map[string]any{
			"paybill_id":   paybill.ID,
			"trans_id":     body.TransID,
			"amount":       body.TransAmount,
			"resolved_via": string(decision.Via),
			"resolved_id":  decision.MemberID,
			"workflow_id":  wfInstanceID,
		},
	})
	return nil
}

// ─────────── helpers ───────────

func paybillFromPath(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "paybill_id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func writeDaraja(w http.ResponseWriter, body darajaResult) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// readBoundedBody guards against an attacker (or a buggy Daraja
// build) shovelling megabytes. Daraja confirmations are ~1KB in
// practice; we cap at 64KB which leaves room for jumbo
// reasons / strings without enabling anything weaponisable.
func readBoundedBody(r *http.Request) ([]byte, error) {
	const max = 64 << 10
	r.Body = http.MaxBytesReader(nil, r.Body, max)
	return io.ReadAll(r.Body)
}

// parseDarajaTime parses "YYYYMMDDHHMMSS" into a *time.Time. Returns
// nil when the input is empty/malformed — we still persist the row;
// the staff UI handles a missing timestamp by falling back to
// received_at.
func parseDarajaTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if len(s) < 14 {
		return nil
	}
	t, err := time.Parse("20060102150405", s[:14])
	if err != nil {
		return nil
	}
	return &t
}
