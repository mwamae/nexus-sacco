// Admin endpoints for per-tenant SMS configuration:
//   GET  /v1/notification-config/sms              current config (api key masked)
//   PUT  /v1/notification-config/sms              upsert
//   POST /v1/notification-config/sms/test         direct send via saved config
//
// AT delivery-report webhook (per-tenant URL so we don't need to
// bypass RLS to discover the tenant):
//   POST /webhooks/at/delivery/{tenant_id}        form-encoded payload

package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/sms"
	"github.com/nexussacco/notification/internal/store"
)

type SMSHandler struct {
	DB         *db.Pool
	SMS        *store.SMSConfigStore
	Notifs     *store.NotificationStore
	HTTPClient *http.Client
	Logger     *slog.Logger
}

type smsConfigResp struct {
	domain.SMSConfig
	APIKeySet        bool `json:"api_key_set"`
	WebhookSecretSet bool `json:"webhook_secret_set"`
}

func (h *SMSHandler) Get(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var resp *smsConfigResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMS.GetTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if c == nil {
			return nil
		}
		apiSet := c.APIKey != ""
		hookSet := c.WebhookSecret != ""
		c.APIKey = ""
		c.WebhookSecret = ""
		resp = &smsConfigResp{SMSConfig: *c, APIKeySet: apiSet, WebhookSecretSet: hookSet}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if resp == nil {
		httpx.OK(w, nil)
		return
	}
	httpx.OK(w, resp)
}

type upsertSMSReq struct {
	Provider      domain.SMSProvider `json:"provider"`
	Username      string             `json:"username"`
	APIKey        string             `json:"api_key,omitempty"`
	SenderID      string             `json:"sender_id"`
	RatePerMinute int                `json:"rate_per_minute"`
	WebhookSecret string             `json:"webhook_secret,omitempty"`
	IsActive      bool               `json:"is_active"`
}

func (h *SMSHandler) Update(w http.ResponseWriter, r *http.Request) {
	var in upsertSMSReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Provider == "" {
		in.Provider = domain.SMSProviderMock
	}
	switch in.Provider {
	case domain.SMSProviderMock, domain.SMSProviderSandbox, domain.SMSProviderProduction:
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("provider must be one of: mock | sandbox | production"))
		return
	}
	if in.Provider != domain.SMSProviderMock && in.Username == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("username is required for sandbox/production"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *smsConfigResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMS.UpsertTx(r.Context(), tx, store.UpsertSMSInput{
			Provider:      in.Provider,
			Username:      in.Username,
			APIKey:        in.APIKey,
			SenderID:      in.SenderID,
			RatePerMinute: in.RatePerMinute,
			WebhookSecret: in.WebhookSecret,
			IsActive:      in.IsActive,
		})
		if err != nil {
			return err
		}
		apiSet := c.APIKey != ""
		hookSet := c.WebhookSecret != ""
		c.APIKey = ""
		c.WebhookSecret = ""
		out = &smsConfigResp{SMSConfig: *c, APIKeySet: apiSet, WebhookSecretSet: hookSet}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

type testSMSReq struct {
	To   string `json:"to"`
	Body string `json:"body,omitempty"`
}

func (h *SMSHandler) Test(w http.ResponseWriter, r *http.Request) {
	var in testSMSReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.To == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to is required"))
		return
	}
	if in.Body == "" {
		in.Body = "Test SMS from your SACCO notification platform."
	}
	tid, _ := middleware.TenantIDFrom(r)
	var cfg *domain.SMSConfig
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMS.GetTx(r.Context(), tx)
		if err != nil {
			return err
		}
		cfg = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if cfg == nil || !cfg.IsActive {
		httpx.WriteErr(w, r, httpx.ErrConflict("no active SMS config — save one first"))
		return
	}
	client := h.HTTPClient
	if client == nil {
		client = sms.DefaultClient()
	}
	res, sendErr := sms.Send(client, cfg, sms.Message{
		To:   in.To,
		From: cfg.SenderID,
		Body: in.Body,
	})
	if sendErr != nil {
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"data": map[string]any{"ok": false, "error": sendErr.Error()},
		})
		return
	}
	httpx.OK(w, map[string]any{
		"ok":                  true,
		"provider":            cfg.Provider,
		"provider_message_id": res.ProviderMessageID,
		"cost":                res.Cost,
		"to":                  in.To,
	})
}

// ─────────── AT delivery report webhook ───────────
//
// Tenant is in the URL path so we never need to bypass RLS. Operators
// configure their AT callback URL as:
//   https://<your-host>/webhooks/at/delivery/<tenant_id>
//
// AT POSTs form-encoded:
//   id           = messageId from the send response
//   status       = Success | Sent | Buffered | Rejected | Failed | Delivered
//   phoneNumber  = recipient
//   networkCode  = MNO code
//   failureReason= (when Failed/Rejected)

func (h *SMSHandler) ATDeliveryReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	if err := r.ParseForm(); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("parse form: "+err.Error()))
		return
	}
	msgID := r.PostFormValue("id")
	status := r.PostFormValue("status")
	failureReason := r.PostFormValue("failureReason")
	if msgID == "" || status == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("id and status are required"))
		return
	}

	applyErr := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		deliveryID, _, err := h.Notifs.FindByProviderMessageIDTx(r.Context(), tx, domain.ChannelSMS, msgID)
		if err == store.ErrNotFound {
			h.Logger.Warn("at delivery report: unknown message id",
				"tenant", tenantID, "id", msgID, "status", status)
			return nil
		}
		if err != nil {
			return err
		}
		switch status {
		case "Success", "Sent", "Delivered":
			return h.Notifs.MarkDeliveredTx(r.Context(), tx, deliveryID)
		case "Buffered":
			// Carrier accepted; final state pending. Leave 'sent'.
			return nil
		case "Rejected", "Failed":
			reason := failureReason
			if reason == "" {
				reason = "AT reported " + status
			}
			return h.Notifs.MarkFailedFinalTx(r.Context(), tx, deliveryID, reason)
		default:
			h.Logger.Warn("at delivery report: unknown status",
				"tenant", tenantID, "id", msgID, "status", status)
			return nil
		}
	})
	if applyErr != nil {
		h.Logger.Warn("at delivery report: apply failed", "tenant", tenantID, "id", msgID, "err", applyErr)
	}
	// Always 200 — AT will retry on non-2xx, which we don't want.
	w.WriteHeader(http.StatusOK)
}
