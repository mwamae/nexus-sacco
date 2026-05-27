// B2C reversal flow.
//
// Two trigger paths land here:
//   1. Safaricom's Result callback returns ResultCode that maps to
//      a reversal (e.g. SUCCESS but later a separate reversal
//      callback fires — Daraja's reversal envelope is a different
//      shape that the Result handler can route here when detected).
//   2. Staff initiates a reversal via the workflow inbox (not in
//      this PR's UI scope; the wf instance just exists and the
//      reconciler runs the same code path).
//
// For phase 4 we expose one public callback URL the Daraja portal
// can be configured with: POST /v1/b2c/{paybill_id}/reverse.
// (No /mpesa segment — Safaricom's portal refuses URLs containing
// that substring.)
// The handler:
//   • Looks up the outbound row by conversation_id
//   • Flips status to 'reversed', stamps the raw payload
//   • Enqueues an mpesa_b2c_reversal wf_instance for staff to retry
//     or cancel the disbursement.
//
// Loan rollback (flipping the loan back to pending_disbursement)
// happens via the savings /internal/v1/loans/{id}/reverse-disbursement
// endpoint. The call fires OUTSIDE the persistence tx (savings owns
// its own tx + posting outbox) and is best-effort — the wf task
// remains the durable handle so an operator can finish the job
// manually if the HTTP call fails. Daraja ALWAYS sees a 200, even
// when the savings call errors, because telling Safaricom to retry
// would compound the bounce.

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

// ReverseHandler can live on the existing B2CHandler — it has the
// same dependency surface (DB + outbound store + audit + workflow).
func (h *B2CHandler) Reverse(w http.ResponseWriter, r *http.Request) {
	paybillID, _, ok := h.authWebhook(r)
	if !ok {
		http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: unauthorised"}`, http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "body read", http.StatusBadRequest)
		return
	}
	var env b2cResultEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		h.Logger.Error("b2c reverse: malformed body", "paybill_id", paybillID, "err", err)
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (malformed)"})
		return
	}

	var paybillTenant uuid.UUID
	if err := h.DB.Pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM mpesa_paybills WHERE id = $1`, paybillID,
	).Scan(&paybillTenant); err != nil {
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (unknown paybill)"})
		return
	}

	var (
		outboundID   uuid.UUID
		amount       string
		msisdn       string
		sourceRef    string
		sourceModule string
	)
	err = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		out, err := h.Outbound.ByConversationIDTx(r.Context(), tx, env.Result.ConversationID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // unknown — ack to Safaricom
			}
			return err
		}
		outboundID = out.ID
		amount = out.Amount.StringFixed(2)
		msisdn = out.MSISDN
		sourceRef = out.SourceRef
		sourceModule = out.SourceModule
		if err := h.Outbound.MarkReversedTx(r.Context(), tx, out.ID, body); err != nil {
			return err
		}
		// Enqueue the staff workflow task. Soft-fail when the
		// definition isn't seeded (the wf def comes from migration
		// 0007; if a tenant predates the migration the task just
		// doesn't get queued — log + continue).
		if h.Workflow != nil {
			ctxMap := map[string]any{
				"outbound_id":   out.ID,
				"source_module": out.SourceModule,
				"source_ref":    out.SourceRef,
				"amount":        out.Amount.StringFixed(2),
				"msisdn":        out.MSISDN,
				"reversal_desc": env.Result.ResultDesc,
			}
			// loan_id when source_ref is a parseable UUID + module is
			// the loan-disbursement source. Lets the staff inbox UI
			// deep-link to the loan profile from the reversal task.
			if out.SourceModule == "loan.disbursement" {
				if lid, perr := uuid.Parse(out.SourceRef); perr == nil {
					ctxMap["loan_id"] = lid.String()
				}
			}
			_, err := h.Workflow.CreateInstanceTx(r.Context(), tx, workflowclient.CreateInstanceInput{
				TenantID:    paybillTenant,
				ProcessKind: "mpesa_b2c_reversal",
				SubjectKind: "mpesa_outbound_request",
				SubjectID:   out.ID,
				Summary: fmt.Sprintf("M-PESA B2C reversal · KES %s to %s",
					out.Amount.StringFixed(2), out.MSISDN),
				SourceURL: "/accounting/mpesa-reversal?outbound=" + out.ID.String(),
				Context:   ctxMap,
			})
			if err != nil && !errors.Is(err, workflowclient.ErrDefinitionNotFound) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		h.Logger.Error("b2c reverse persist", "paybill_id", paybillID, "err", err)
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: persistence"})
		return
	}
	if outboundID == uuid.Nil {
		h.Logger.Info("b2c reverse for unknown conversation_id", "conversation_id", env.Result.ConversationID)
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (unknown conversation)"})
		return
	}
	h.audit(r, paybillTenant, outboundID, "mpesa.b2c.reversed", map[string]any{
		"amount":     amount,
		"msisdn":     msisdn,
		"source_ref": sourceRef,
	})

	// Hand off to savings (loan rollback). Only attempt for the
	// loan-disbursement source_module; refund flows don't have a
	// savings-side counterpart. Failure is logged + audited but does
	// NOT change the Daraja-facing response — the wf task is the
	// durable handle and the reconciler retries on its own schedule.
	if h.Finalize != nil && sourceModule == "loan.disbursement" && sourceRef != "" {
		if loanID, perr := uuid.Parse(sourceRef); perr == nil {
			reason := env.Result.ResultDesc
			if reason == "" {
				reason = "Safaricom reversal"
			}
			// Daraja's reversal envelope carries the original
			// TransactionID in Result.TransactionID — surface it to
			// savings as the mpesa_reversal_receipt for audit
			// correlation with mpesa_outbound_requests.
			mpesaReceipt := env.Result.TransactionID
			if mpesaReceipt == "" {
				mpesaReceipt = env.Result.ConversationID
			}
			if rerr := h.Finalize.ReverseDisbursement(
				r.Context(), loanID, mpesaReceipt, reason,
			); rerr != nil {
				h.Logger.Error("loan reverse-disbursement",
					"loan_id", loanID, "outbound_id", outboundID, "err", rerr)
				h.audit(r, paybillTenant, outboundID, "mpesa.b2c.reverse_handoff", map[string]any{
					"loan_id":                loanID.String(),
					"mpesa_reversal_receipt": mpesaReceipt,
					"error":                  rerr.Error(),
				})
				// Best-effort: the wf task is the human safety net.
			} else {
				h.Logger.Info("loan reverse-disbursement",
					"loan_id", loanID, "outbound_id", outboundID,
					"mpesa_reversal_receipt", mpesaReceipt)
				h.audit(r, paybillTenant, outboundID, "mpesa.b2c.reverse_handoff", map[string]any{
					"loan_id":                loanID.String(),
					"mpesa_reversal_receipt": mpesaReceipt,
				})
			}
		}
	}

	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})

	// Silence an "import unused" warning for chi when no other
	// reverse-side route in this file uses it directly.
	_ = chi.URLParam
}
