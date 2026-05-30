// STK Push (Lipa Na M-PESA Online) — Phase 2.2.
//
// Two endpoints:
//
//   POST /v1/mpesa/stk/push                 (internal-token gated)
//     Service-to-service. The standing-order processor (and admin
//     "test pull" tooling) calls this to push a payment prompt to a
//     member's phone. Body carries paybill_id + msisdn + amount +
//     account_reference + source_module + source_ref. Returns the
//     stk_request row id + checkout_request_id immediately; the
//     async callback updates status.
//
//   POST /v1/c2b/{paybill_id}/stk-callback  (public, paybill token)
//     Daraja's callback. Updates the matching stk_request row by
//     checkout_request_id; on ResultCode=0 also inserts a synthetic
//     mpesa_inbound_events row so the existing distribution waterfall
//     posts the deposit — keeps a single posting path.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/daraja"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/httpx"
	"github.com/nexussacco/mpesa/internal/metrics"
	"github.com/nexussacco/mpesa/internal/store"
)

type STKHandler struct {
	DB             *db.Pool
	Paybills       *store.PaybillStore
	Credentials    *store.CredentialStore
	InboundEvents  *store.InboundEventStore
	STKRequests    *store.STKRequestStore
	Audit          *store.AuditStore
	Daraja         *daraja.Client
	Sealer         sealerIface
	InternalToken  string
	CallbackBase   string // e.g. "https://mpesa.acme.com" — used to build CallBackURL
	Logger         *slog.Logger
}

// sealerIface mirrors the small surface the handler needs from the
// crypto sealer. Mocked in tests.
type sealerIface interface {
	Decrypt([]byte) ([]byte, error)
}

// ─────────── POST /v1/mpesa/stk/push (internal) ───────────

type stkPushReq struct {
	PaybillID        uuid.UUID       `json:"paybill_id"`
	MSISDN           string          `json:"msisdn"`
	Amount           decimal.Decimal `json:"amount"`
	AccountReference string          `json:"account_reference"`
	TransactionDesc  string          `json:"transaction_desc"`
	SourceModule     string          `json:"source_module"`
	SourceRef        string          `json:"source_ref"`
}

type stkPushResp struct {
	ID                  uuid.UUID `json:"id"`
	Status              string    `json:"status"`
	CheckoutRequestID   string    `json:"checkout_request_id,omitempty"`
	MerchantRequestID   string    `json:"merchant_request_id,omitempty"`
	ResponseCode        string    `json:"response_code,omitempty"`
	ResponseDescription string    `json:"response_description,omitempty"`
}

func (h *STKHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken == "" || r.Header.Get("X-Internal-Token") != h.InternalToken {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	var in stkPushReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.PaybillID == uuid.Nil || in.MSISDN == "" ||
		in.Amount.LessThanOrEqual(decimal.Zero) ||
		in.AccountReference == "" ||
		in.SourceModule == "" || in.SourceRef == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(
			"paybill_id, msisdn, amount, account_reference, source_module, source_ref required"))
		return
	}
	if in.TransactionDesc == "" {
		in.TransactionDesc = "Standing order"
	}

	// 1. Resolve paybill tenant + load credentials.
	var paybillTenant uuid.UUID
	if err := h.DB.Pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM mpesa_paybills WHERE id = $1`, in.PaybillID,
	).Scan(&paybillTenant); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("paybill not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	var paybill *domain.Paybill
	var consumerKey, consumerSecret, passkey []byte
	var keyID string
	err := h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		p, err := h.Paybills.ByIDTx(r.Context(), tx, in.PaybillID)
		if err != nil {
			return err
		}
		paybill = p
		kid, ck, err := h.Credentials.ReadTx(r.Context(), tx, in.PaybillID, domain.CredConsumerKey)
		if err != nil {
			return fmt.Errorf("consumer_key: %w", err)
		}
		keyID = kid
		_, cs, err := h.Credentials.ReadTx(r.Context(), tx, in.PaybillID, domain.CredConsumerSecret)
		if err != nil {
			return fmt.Errorf("consumer_secret: %w", err)
		}
		_, pk, err := h.Credentials.ReadTx(r.Context(), tx, in.PaybillID, domain.CredPasskey)
		if err != nil {
			return fmt.Errorf("passkey: %w", err)
		}
		if consumerKey, err = h.Sealer.Decrypt(ck); err != nil {
			return fmt.Errorf("decrypt consumer_key: %w", err)
		}
		if consumerSecret, err = h.Sealer.Decrypt(cs); err != nil {
			return fmt.Errorf("decrypt consumer_secret: %w", err)
		}
		if passkey, err = h.Sealer.Decrypt(pk); err != nil {
			return fmt.Errorf("decrypt passkey: %w", err)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	defer func() {
		for i := range consumerKey {
			consumerKey[i] = 0
		}
		for i := range consumerSecret {
			consumerSecret[i] = 0
		}
		for i := range passkey {
			passkey[i] = 0
		}
	}()

	// 2. Insert the pending stk_request row.
	var pending *store.STKRequest
	err = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		p, err := h.STKRequests.InsertPendingTx(r.Context(), tx, store.InsertSTKRequestInput{
			TenantID:         paybillTenant,
			PaybillID:        in.PaybillID,
			MSISDN:           in.MSISDN,
			Amount:           in.Amount,
			AccountReference: in.AccountReference,
			TransactionDesc:  in.TransactionDesc,
			SourceModule:     in.SourceModule,
			SourceRef:        in.SourceRef,
		})
		if err != nil {
			return err
		}
		pending = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// 3. OAuth + STK initiate.
	tok, err := h.Daraja.AuthenticateForPaybill(r.Context(),
		daraja.CacheKey{PaybillID: in.PaybillID, KeyID: keyID},
		string(consumerKey), string(consumerSecret))
	if err != nil {
		h.markFailed(r.Context(), paybillTenant, pending.ID, "", "", "oauth", err.Error(), nil)
		httpx.WriteErr(w, r, fmt.Errorf("oauth: %w", err))
		return
	}

	ts := daraja.DarajaTimestamp(time.Now())
	password := daraja.PasswordForSTKPush(paybill.Shortcode, string(passkey), ts)
	callbackURL := h.CallbackBase + "/v1/c2b/" + in.PaybillID.String() + "/stk-callback?token=" + paybill.WebhookToken

	stkReq := daraja.STKPushRequest{
		BusinessShortCode: paybill.Shortcode,
		Password:          password,
		Timestamp:         ts,
		TransactionType:   daraja.STKPaybillOnline,
		Amount:            in.Amount.StringFixed(0),
		PartyA:            in.MSISDN,
		PartyB:            paybill.Shortcode,
		PhoneNumber:       in.MSISDN,
		CallBackURL:       callbackURL,
		AccountReference:  truncate(in.AccountReference, 12),
		TransactionDesc:   truncate(in.TransactionDesc, 13),
	}
	resp, raw, sErr := h.Daraja.SubmitSTKPush(r.Context(), tok.AccessToken, stkReq)
	if sErr != nil {
		h.markFailed(r.Context(), paybillTenant, pending.ID, "", "", "submit", sErr.Error(), raw)
		httpx.WriteErr(w, r, fmt.Errorf("stk submit: %w", sErr))
		return
	}

	// 4. Persist Daraja's IDs + status.
	finalStatus := "sent"
	if resp.ResponseCode != "0" {
		finalStatus = "failed"
	}
	_ = h.DB.WithTenantTx(r.Context(), paybillTenant, func(tx pgx.Tx) error {
		return h.STKRequests.MarkInitiatedTx(r.Context(), tx, store.MarkInitiatedInput{
			ID:                       pending.ID,
			OriginatorConversationID: "",
			MerchantRequestID:        resp.MerchantRequestID,
			CheckoutRequestID:        resp.CheckoutRequestID,
			ResponseCode:             resp.ResponseCode,
			ResponseDescription:      resp.ResponseDescription,
			Status:                   finalStatus,
			RawInitiateResponse:      raw,
		})
	})

	metrics.OutboundTotal.Inc("stk_"+finalStatus, paybillTenant.String())
	if h.Audit != nil {
		t := paybillTenant
		_ = h.Audit.Write(r.Context(), store.AuditEntry{
			TenantID:   &t,
			Action:     "mpesa.stk.initiated",
			TargetKind: "mpesa_stk_request",
			TargetID:   pending.ID.String(),
			Metadata: map[string]any{
				"paybill_id":          in.PaybillID.String(),
				"msisdn":              in.MSISDN,
				"amount":              in.Amount.StringFixed(2),
				"account_reference":   in.AccountReference,
				"checkout_request_id": resp.CheckoutRequestID,
				"response_code":       resp.ResponseCode,
				"source_module":       in.SourceModule,
				"source_ref":          in.SourceRef,
			},
		})
	}

	httpx.OK(w, stkPushResp{
		ID:                  pending.ID,
		Status:              finalStatus,
		CheckoutRequestID:   resp.CheckoutRequestID,
		MerchantRequestID:   resp.MerchantRequestID,
		ResponseCode:        resp.ResponseCode,
		ResponseDescription: resp.ResponseDescription,
	})
}

// ─────────── POST /v1/c2b/{paybill_id}/stk-callback (public) ───────────

func (h *STKHandler) Callback(w http.ResponseWriter, r *http.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "paybill_id"))
	if err != nil {
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: invalid paybill id"})
		return
	}
	tok := r.URL.Query().Get("token")
	paybill, err := h.Paybills.ByIDAndToken(r.Context(), pid, tok)
	if err != nil || paybill == nil {
		http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: unauthorised"}`, http.StatusUnauthorized)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, `{"ResultCode":1,"ResultDesc":"Rejected: body"}`, http.StatusBadRequest)
		return
	}
	var env daraja.STKCallbackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		h.Logger.Error("stk callback: malformed body", "err", err, "paybill_id", pid, "body", string(raw))
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (malformed)"})
		return
	}
	cb := env.Body.StkCallback
	if cb.CheckoutRequestID == "" {
		writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received (missing checkout id)"})
		return
	}

	receipt := daraja.PickCallbackItem(env, "MpesaReceiptNumber")
	amountStr := daraja.PickCallbackItem(env, "Amount")
	msisdn := daraja.PickCallbackItem(env, "PhoneNumber")

	status := "failed"
	if cb.ResultCode == 0 {
		status = "completed"
	} else if cb.ResultCode == 1032 {
		status = "cancelled" // user cancelled on phone
	}

	var inboundID *uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), paybill.TenantID, func(tx pgx.Tx) error {
		// On success, insert a synthetic inbound_event so the existing
		// distribution waterfall posts the deposit. We look up the
		// pending row first to recover the AccountReference (it's not
		// echoed back in the callback metadata).
		var ref string
		if status == "completed" {
			pending, perr := h.STKRequests.ByCheckoutTx(r.Context(), tx, cb.CheckoutRequestID)
			if perr == nil && pending != nil {
				ref = pending.AccountReference
				if msisdn == "" {
					msisdn = pending.MSISDN
				}
				if amountStr == "" {
					amountStr = pending.Amount.StringFixed(0)
				}
				ev, _, ierr := h.InboundEvents.RecordTx(r.Context(), tx, store.RecordInboundInput{
					TenantID:      paybill.TenantID,
					PaybillID:     paybill.ID,
					Shortcode:     paybill.Shortcode,
					TransactionID: nonEmpty(receipt, "STK-"+cb.CheckoutRequestID),
					Amount:        amountStr,
					MSISDN:        msisdn,
					BillRef:       ref,
					RawPayload:    raw,
				})
				if ierr != nil {
					return ierr
				}
				id := ev.ID
				inboundID = &id
			}
		}
		_, err := h.STKRequests.ApplyCallbackTx(r.Context(), tx, store.ApplyCallbackInput{
			CheckoutRequestID:  cb.CheckoutRequestID,
			ResultCode:         fmt.Sprintf("%d", cb.ResultCode),
			ResultDesc:         cb.ResultDesc,
			MpesaReceiptNumber: receipt,
			Status:             status,
			RawCallback:        raw,
			InboundEventID:     inboundID,
		})
		return err
	})
	if err != nil {
		h.Logger.Error("stk callback persist", "err", err, "checkout_id", cb.CheckoutRequestID)
		writeDaraja(w, darajaResult{ResultCode: 1, ResultDesc: "Rejected: persistence"})
		return
	}

	metrics.OutboundTotal.Inc("stk_callback_"+status, paybill.TenantID.String())
	writeDaraja(w, darajaResult{ResultCode: 0, ResultDesc: "Received"})
}

// ─────────── helpers ───────────

func (h *STKHandler) markFailed(ctx context.Context, tenantID, id uuid.UUID, merchID, checkoutID, code, desc string, raw []byte) {
	_ = h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return h.STKRequests.MarkInitiatedTx(ctx, tx, store.MarkInitiatedInput{
			ID:                  id,
			MerchantRequestID:   merchID,
			CheckoutRequestID:   checkoutID,
			ResponseCode:        code,
			ResponseDescription: desc,
			Status:              "failed",
			RawInitiateResponse: raw,
		})
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
