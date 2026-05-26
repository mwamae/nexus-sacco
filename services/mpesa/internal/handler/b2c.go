// B2C handlers — phase 4.
//
// Three routes:
//
//   POST /v1/mpesa/b2c/requests               (internal-token gated)
//     Loan disbursement / refund flows call this to enqueue an
//     outbound payment. Body carries paybill_id + msisdn + amount +
//     idempotency keys. Returns the row (created or pre-existing).
//
//   POST /v1/mpesa/b2c/{paybill_id}/result    (public, paybill token)
//     Daraja's success/failure callback. We persist the raw body and
//     update the outbound row's final status. On success we ALSO
//     trigger the savings-side finalize HTTP via the savingsclient.
//
//   POST /v1/mpesa/b2c/{paybill_id}/timeout   (public, paybill token)
//     Daraja's queue-timeout callback. Marks the row 'failed' with
//     a retry hint; the dispatcher's normal retry path handles it.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/httpx"
	"github.com/nexussacco/mpesa/internal/metrics"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

// FinalizeClient is the surface the result handler uses to hand off
// to savings (or whichever module owns the source_module of the
// outbound row). Kept as an interface so tests can mock the
// completion call.
type FinalizeClient interface {
	FinalizeDisbursement(ctx context.Context, loanID uuid.UUID, mpesaReceipt string) error
}

type B2CHandler struct {
	DB             *db.Pool
	Paybills       *store.PaybillStore
	Outbound       *store.OutboundRequestStore
	Audit          *store.AuditStore
	Workflow       *workflowclient.Client
	Finalize       FinalizeClient // nil-safe (logged + retried by reconciler)
	InternalToken  string         // gates /v1/mpesa/b2c/requests
	Logger         *slog.Logger
}

// ─────────── POST /v1/mpesa/b2c/requests (internal) ───────────

type enqueueReq struct {
	PaybillID    uuid.UUID       `json:"paybill_id"`
	MSISDN       string          `json:"msisdn"`
	Amount       decimal.Decimal `json:"amount"`
	CommandID    string          `json:"command_id"`
	SourceModule string          `json:"source_module"`
	SourceRef    string          `json:"source_ref"`
	Remarks      string          `json:"remarks"`
	Kind         string          `json:"kind"` // "b2c_disbursement" | "refund"
}

func (h *B2CHandler) Enqueue(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken == "" || r.Header.Get("X-Internal-Token") != h.InternalToken {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	var req enqueueReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.PaybillID == uuid.Nil || req.MSISDN == "" ||
		req.Amount.LessThanOrEqual(decimal.Zero) ||
		req.SourceModule == "" || req.SourceRef == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("paybill_id, msisdn, amount, source_module, source_ref required"))
		return
	}
	kind := domain.OutboundB2CDisbursement
	if req.Kind == string(domain.OutboundRefund) {
		kind = domain.OutboundRefund
	}

	var paybillTenant uuid.UUID
	var out *store.OutboundRequest
	var inserted bool
	err := h.DB.Pool.QueryRow(r.Context(), `SELECT tenant_id FROM mpesa_paybills WHERE id = $1`, req.PaybillID).Scan(&paybillTenant)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("paybill not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	err = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		o, ins, err := h.Outbound.InsertTx(r.Context(), tx, store.InsertOutboundInput{
			TenantID:     paybillTenant,
			PaybillID:    req.PaybillID,
			Kind:         kind,
			MSISDN:       req.MSISDN,
			Amount:       req.Amount,
			CommandID:    req.CommandID,
			Remarks:      req.Remarks,
			SourceModule: req.SourceModule,
			SourceRef:    req.SourceRef,
		})
		if err != nil {
			return err
		}
		out = o
		inserted = ins
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if inserted {
		metrics.OutboundTotal.Inc("queued", paybillTenant.String())
		h.audit(r, paybillTenant, out.ID, "mpesa.b2c.enqueued", map[string]any{
			"source_module": req.SourceModule,
			"source_ref":    req.SourceRef,
			"amount":        req.Amount.StringFixed(2),
			"msisdn":        req.MSISDN,
		})
	}
	httpx.OK(w, map[string]any{
		"id":             out.ID,
		"status":         out.Status,
		"inserted":       inserted,
		"source_ref":     out.SourceRef,
		"source_module":  out.SourceModule,
	})
}

// ─────────── POST /v1/mpesa/b2c/{paybill_id}/result (public) ───────────

// Daraja's result envelope. The actual payload has a deep nesting
// (Result.ResultParameters.ResultParameter[]) that carries the
// receipt number + recipient info; we capture the raw body and the
// commonly-used top-level fields.
type b2cResultEnvelope struct {
	Result struct {
		ResultType                  int    `json:"ResultType"`
		ResultCode                  int    `json:"ResultCode"`
		ResultDesc                  string `json:"ResultDesc"`
		OriginatorConversationID    string `json:"OriginatorConversationID"`
		ConversationID              string `json:"ConversationID"`
		TransactionID               string `json:"TransactionID"`
		ResultParameters            struct {
			ResultParameter []struct {
				Key   string      `json:"Key"`
				Value interface{} `json:"Value"`
			} `json:"ResultParameter"`
		} `json:"ResultParameters"`
	} `json:"Result"`
}

func (h *B2CHandler) Result(w http.ResponseWriter, r *http.Request) {
	paybillID, paybillToken, ok := h.authWebhook(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid paybill token"))
		return
	}
	_ = paybillToken // tagged for audit downstream
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "body read", http.StatusBadRequest)
		return
	}
	var env b2cResultEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		// Per-spec: log + 200 so Safaricom doesn't retry forever.
		h.Logger.Error("b2c result: malformed body", "paybill_id", paybillID, "err", err, "body", string(rawBody))
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (malformed)"})
		return
	}

	// Look the row up by conversation id; if we don't recognise it,
	// log + ack (Safaricom would otherwise retry).
	var paybillTenant uuid.UUID
	if err := h.DB.Pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM mpesa_paybills WHERE id = $1`, paybillID,
	).Scan(&paybillTenant); err != nil {
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (unknown paybill)"})
		return
	}

	var outboundID uuid.UUID
	var loanID uuid.UUID
	var resultCode string
	err = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		out, err := h.Outbound.ByConversationIDTx(r.Context(), tx, env.Result.ConversationID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // logged below; ack to Daraja
			}
			return err
		}
		outboundID = out.ID
		resultCode = fmt.Sprintf("%d", env.Result.ResultCode)
		// Extract MpesaReceiptNumber from ResultParameter list when
		// present; phase 4 also persists the raw envelope for replay.
		mpesaReceipt := pickResultParam(env, "TransactionReceipt")

		if err := h.Outbound.MarkResultTx(r.Context(), tx, out.ID,
			resultCode, env.Result.ResultDesc, mpesaReceipt, rawBody); err != nil {
			return err
		}
		if resultCode == "0" {
			metrics.OutboundTotal.Inc("completed", paybillTenant.String())
		} else {
			metrics.OutboundTotal.Inc("failed", paybillTenant.String())
		}

		// Look up the loan id for the finalize call. The source_ref
		// carries it for loan disbursement; refund flows don't go
		// through savings finalize.
		if out.SourceModule == "loan.disbursement" {
			if lid, perr := uuid.Parse(out.SourceRef); perr == nil {
				loanID = lid
			}
		}
		return nil
	})
	if err != nil {
		h.Logger.Error("b2c result persist", "paybill_id", paybillID, "err", err)
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: persistence"})
		return
	}
	if outboundID == uuid.Nil {
		h.Logger.Info("b2c result for unknown conversation_id", "conversation_id", env.Result.ConversationID)
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (unknown conversation)"})
		return
	}
	h.audit(r, paybillTenant, outboundID, "mpesa.b2c.result", map[string]any{
		"result_code":     resultCode,
		"result_desc":     env.Result.ResultDesc,
		"conversation_id": env.Result.ConversationID,
	})

	// On success, finalize the savings-side disbursement. Best-effort:
	// failure is recorded so the reconciler can retry without
	// re-asking Daraja.
	if resultCode == "0" && loanID != uuid.Nil && h.Finalize != nil {
		mpesaReceipt := pickResultParam(env, "TransactionReceipt")
		ferr := h.Finalize.FinalizeDisbursement(r.Context(), loanID, mpesaReceipt)
		_ = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
			if ferr != nil {
				return h.Outbound.RecordFinalizationAttemptTx(r.Context(), tx, outboundID, false, ferr.Error())
			}
			return h.Outbound.RecordFinalizationAttemptTx(r.Context(), tx, outboundID, true, "")
		})
		if ferr != nil {
			h.Logger.Warn("b2c finalize attempt failed",
				"outbound_id", outboundID, "loan_id", loanID, "err", ferr)
		}
	}
	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
}

// ─────────── POST /v1/mpesa/b2c/{paybill_id}/timeout (public) ───────────

func (h *B2CHandler) Timeout(w http.ResponseWriter, r *http.Request) {
	paybillID, _, ok := h.authWebhook(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid paybill token"))
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var env b2cResultEnvelope
	_ = json.Unmarshal(body, &env)

	var paybillTenant uuid.UUID
	if err := h.DB.Pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM mpesa_paybills WHERE id = $1`, paybillID,
	).Scan(&paybillTenant); err != nil {
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (unknown paybill)"})
		return
	}
	_ = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		out, err := h.Outbound.ByConversationIDTx(r.Context(), tx, env.Result.ConversationID)
		if err != nil {
			return nil // ack to Daraja
		}
		return h.Outbound.MarkFailedTx(r.Context(), tx, out.ID, "Daraja queue timeout")
	})
	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
}

// ─────────── helpers ───────────

func (h *B2CHandler) authWebhook(r *http.Request) (paybillID uuid.UUID, token string, ok bool) {
	pid, err := uuid.Parse(chi.URLParam(r, "paybill_id"))
	if err != nil {
		return uuid.Nil, "", false
	}
	tok := r.URL.Query().Get("token")
	paybill, err := h.Paybills.ByIDAndToken(r.Context(), pid, tok)
	if err != nil || paybill == nil {
		return uuid.Nil, "", false
	}
	return pid, tok, true
}

// pickResultParam pulls a single key out of Daraja's ResultParameter
// list. The list is array-of-{Key,Value} where Value may be a string
// or a number depending on the field. Returns "" when missing.
func pickResultParam(env b2cResultEnvelope, key string) string {
	for _, p := range env.Result.ResultParameters.ResultParameter {
		if !strings.EqualFold(p.Key, key) {
			continue
		}
		switch v := p.Value.(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%v", v)
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func (h *B2CHandler) audit(r *http.Request, tenantID uuid.UUID, outboundID uuid.UUID, action string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	t := tenantID
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &t,
		Action:     action,
		TargetKind: "mpesa_outbound_request",
		TargetID:   outboundID.String(),
		Metadata:   meta,
	})
}
