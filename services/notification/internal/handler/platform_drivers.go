// Platform-admin endpoints for the shared SMTP + SMS drivers. These
// supersede the per-tenant `notification-config/{smtp,sms}` endpoints
// that lived in stages 2-3. Tenant admins no longer have visibility
// into provider configuration; they only see their credit balance.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/sms"
	"github.com/nexussacco/notification/internal/smtp"
	"github.com/nexussacco/notification/internal/store"
)

type PlatformDriversHandler struct {
	DB           *db.Pool
	PlatformSMTP *store.PlatformSMTPStore
	PlatformSMS  *store.PlatformSMSStore
	Logger       *slog.Logger
}

// ─────────── SMTP ───────────

func (h *PlatformDriversHandler) GetSMTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.PlatformSMTP.Get(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, scrubPassword(cfg))
}

type updateSMTPReq struct {
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Encryption  string  `json:"encryption"`
	Username    string  `json:"username"`
	Password    *string `json:"password,omitempty"` // omit to leave unchanged
	FromAddress string  `json:"from_address"`
	FromName    string  `json:"from_name"`
	IsEnabled   bool    `json:"is_enabled"`
}

func (h *PlatformDriversHandler) UpdateSMTP(w http.ResponseWriter, r *http.Request) {
	var in updateSMTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Host == "" || in.FromAddress == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("host and from_address are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	cfg, err := h.PlatformSMTP.Update(r.Context(), store.UpdatePlatformSMTPInput{
		Host: in.Host, Port: in.Port, Encryption: in.Encryption,
		Username: in.Username, Password: in.Password,
		FromAddress: in.FromAddress, FromName: in.FromName,
		IsEnabled: in.IsEnabled, UpdatedBy: userID,
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, scrubPassword(cfg))
}

type testSMTPReq struct {
	To      string `json:"to"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
}

func (h *PlatformDriversHandler) TestSMTP(w http.ResponseWriter, r *http.Request) {
	var in testSMTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.To == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to is required"))
		return
	}
	cfg, err := h.PlatformSMTP.Get(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if cfg == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("SMTP not configured"))
		return
	}
	subject := in.Subject
	if subject == "" {
		subject = "nexusSacco SMTP test"
	}
	body := in.Body
	if body == "" {
		body = "This is a test email from the nexusSacco platform SMTP driver."
	}
	from := cfg.FromAddress
	if cfg.FromName != "" {
		from = cfg.FromName + " <" + cfg.FromAddress + ">"
	}
	tenantCfg := &domain.SMTPConfig{
		Host: cfg.Host, Port: cfg.Port,
		Username: cfg.Username, Password: cfg.Password,
		Encryption:  domain.SMTPEncryption(cfg.Encryption),
		FromAddress: cfg.FromAddress, FromName: cfg.FromName,
		IsActive: cfg.IsEnabled,
	}
	msgID, err := smtp.Send(tenantCfg, smtp.Message{
		From: from, To: in.To, Subject: subject, PlainBody: body,
	})
	if err != nil {
		httpx.OK(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	httpx.OK(w, map[string]any{
		"ok":                  true,
		"to":                  in.To,
		"provider_message_id": msgID,
	})
}

// ─────────── SMS ───────────

func (h *PlatformDriversHandler) GetSMS(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.PlatformSMS.Get(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, scrubSMSSecrets(cfg))
}

type updateSMSReq struct {
	Provider      string  `json:"provider"`
	Username      string  `json:"username"`
	APIKey        *string `json:"api_key,omitempty"`
	SenderID      string  `json:"sender_id"`
	RatePerMinute int     `json:"rate_per_minute"`
	WebhookSecret *string `json:"webhook_secret,omitempty"`
	IsEnabled     bool    `json:"is_enabled"`
}

func (h *PlatformDriversHandler) UpdateSMS(w http.ResponseWriter, r *http.Request) {
	var in updateSMSReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	cfg, err := h.PlatformSMS.Update(r.Context(), store.UpdatePlatformSMSInput{
		Provider: in.Provider, Username: in.Username,
		APIKey: in.APIKey, SenderID: in.SenderID,
		RatePerMinute: in.RatePerMinute, WebhookSecret: in.WebhookSecret,
		IsEnabled: in.IsEnabled, UpdatedBy: userID,
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, scrubSMSSecrets(cfg))
}

type testSMSReq struct {
	To   string `json:"to"`
	Body string `json:"body,omitempty"`
}

func (h *PlatformDriversHandler) TestSMS(w http.ResponseWriter, r *http.Request) {
	var in testSMSReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.To == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to is required"))
		return
	}
	cfg, err := h.PlatformSMS.Get(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	body := in.Body
	if body == "" {
		body = "nexusSacco platform SMS driver test."
	}
	tenantCfg := platformConfigToTenantShape(cfg)
	res, sendErr := sms.Send(sms.DefaultClient(), tenantCfg, sms.Message{
		To: in.To, From: cfg.SenderID, Body: body,
	})
	if sendErr != nil {
		httpx.OK(w, map[string]any{"ok": false, "error": sendErr.Error()})
		return
	}
	httpx.OK(w, map[string]any{
		"ok":                  true,
		"to":                  in.To,
		"provider_message_id": res.ProviderMessageID,
	})
}

// ─────────── Helpers ───────────

func scrubPassword(c *domain.PlatformSMTPConfig) any {
	return map[string]any{
		"host":         c.Host,
		"port":         c.Port,
		"encryption":   c.Encryption,
		"username":     c.Username,
		"has_password": c.Password != "",
		"from_address": c.FromAddress,
		"from_name":    c.FromName,
		"is_enabled":   c.IsEnabled,
		"updated_at":   c.UpdatedAt,
		"updated_by":   c.UpdatedBy,
	}
}

func scrubSMSSecrets(c *domain.PlatformSMSConfig) any {
	return map[string]any{
		"provider":             c.Provider,
		"username":             c.Username,
		"has_api_key":          c.APIKey != "",
		"sender_id":            c.SenderID,
		"rate_per_minute":      c.RatePerMinute,
		"has_webhook_secret":   c.WebhookSecret != "",
		"is_enabled":           c.IsEnabled,
		"updated_at":           c.UpdatedAt,
		"updated_by":           c.UpdatedBy,
	}
}

// platformConfigToTenantShape mirrors the worker-side helper to feed
// the SMTP/SMS senders. Local copy to avoid importing worker package
// from handler.
func platformConfigToTenantShape(p *domain.PlatformSMSConfig) *domain.SMSConfig {
	return &domain.SMSConfig{
		Provider:      p.Provider,
		Username:      p.Username,
		APIKey:        p.APIKey,
		SenderID:      p.SenderID,
		RatePerMinute: p.RatePerMinute,
		WebhookSecret: p.WebhookSecret,
		IsActive:      p.IsEnabled,
	}
}
