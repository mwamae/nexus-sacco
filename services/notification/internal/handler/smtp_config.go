// Admin endpoints for per-tenant SMTP configuration:
//   GET  /v1/notification-config/smtp        current config (password masked)
//   PUT  /v1/notification-config/smtp        upsert config
//   POST /v1/notification-config/smtp/test   send a one-off test email

package handler

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/smtp"
	"github.com/nexussacco/notification/internal/store"
)

type SMTPHandler struct {
	DB     *db.Pool
	SMTP   *store.SMTPConfigStore
	Logger *slog.Logger
}

type smtpConfigResp struct {
	domain.SMTPConfig
	PasswordSet bool `json:"password_set"`
}

func (h *SMTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var resp *smtpConfigResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMTP.GetTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if c == nil {
			return nil
		}
		// Strip password — only signal whether one is set.
		passwordSet := c.Password != ""
		c.Password = ""
		resp = &smtpConfigResp{SMTPConfig: *c, PasswordSet: passwordSet}
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

type upsertSMTPReq struct {
	Host        string                `json:"host"`
	Port        int                   `json:"port"`
	Username    string                `json:"username"`
	Password    string                `json:"password,omitempty"` // empty = keep existing
	Encryption  domain.SMTPEncryption `json:"encryption"`
	FromAddress string                `json:"from_address"`
	FromName    string                `json:"from_name"`
	ReplyTo     *string               `json:"reply_to,omitempty"`
	IsActive    bool                  `json:"is_active"`
}

func (h *SMTPHandler) Update(w http.ResponseWriter, r *http.Request) {
	var in upsertSMTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Host == "" || in.Port == 0 || in.FromAddress == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("host, port, and from_address are required"))
		return
	}
	if in.Encryption == "" {
		in.Encryption = domain.SMTPStartTLS
	}
	switch in.Encryption {
	case domain.SMTPNone, domain.SMTPStartTLS, domain.SMTPTLS:
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("encryption must be one of: none | starttls | tls"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *smtpConfigResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMTP.UpsertTx(r.Context(), tx, store.UpsertSMTPInput{
			Host:        in.Host,
			Port:        in.Port,
			Username:    in.Username,
			Password:    in.Password,
			Encryption:  in.Encryption,
			FromAddress: in.FromAddress,
			FromName:    in.FromName,
			ReplyTo:     in.ReplyTo,
			IsActive:    in.IsActive,
		})
		if err != nil {
			return err
		}
		passwordSet := c.Password != ""
		c.Password = ""
		out = &smtpConfigResp{SMTPConfig: *c, PasswordSet: passwordSet}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

type testSMTPReq struct {
	ToAddress string `json:"to_address"`
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body,omitempty"`
}

func (h *SMTPHandler) Test(w http.ResponseWriter, r *http.Request) {
	var in testSMTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ToAddress == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to_address is required"))
		return
	}
	if in.Subject == "" {
		in.Subject = "SMTP test from your SACCO platform"
	}
	if in.Body == "" {
		in.Body = "This is a test email. If you're reading it, your SMTP configuration is working."
	}
	tid, _ := middleware.TenantIDFrom(r)
	var cfg *domain.SMTPConfig
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.SMTP.GetTx(r.Context(), tx)
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
		httpx.WriteErr(w, r, httpx.ErrConflict("no active SMTP config — save one first"))
		return
	}
	from := cfg.FromAddress
	if cfg.FromName != "" {
		from = cfg.FromName + " <" + cfg.FromAddress + ">"
	}
	msgID, sendErr := smtp.Send(cfg, smtp.Message{
		From:      from,
		To:        in.ToAddress,
		Subject:   in.Subject,
		PlainBody: in.Body,
	})
	if sendErr != nil {
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"data": map[string]any{
				"ok":      false,
				"error":   sendErr.Error(),
			},
		})
		return
	}
	httpx.OK(w, map[string]any{
		"ok":                  true,
		"provider_message_id": msgID,
		"to":                  in.ToAddress,
	})
}
