// OTP HTTP endpoints — Stage 6.
//
// Internal (service-to-service):
//   POST /internal/v1/otp/request
//   POST /internal/v1/otp/verify
//
// Admin (JWT):
//   GET  /v1/otp-settings
//   PUT  /v1/otp-settings
//   GET  /v1/otp-requests    (audit listing)

package handler

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/otp"
	"github.com/nexussacco/notification/internal/store"
)

type OTPHandler struct {
	DB            *db.Pool
	OTP           *otp.Service
	OTPs          *store.OTPStore
	Settings      *store.OTPSettingsStore
	InternalToken string
	Logger        *slog.Logger
}

// ─────────── Internal: request / verify ───────────

type otpRequestReq struct {
	TenantID          uuid.UUID         `json:"tenant_id"`
	Purpose           domain.OTPPurpose `json:"purpose"`
	Channel           domain.Channel    `json:"channel,omitempty"`
	SubjectUserID     *uuid.UUID        `json:"subject_user_id,omitempty"`
	SubjectMemberID   *uuid.UUID        `json:"subject_member_id,omitempty"`
	SubjectIdentifier *string           `json:"subject_identifier,omitempty"`
	Destination       string            `json:"destination"`
	RecipientName     string            `json:"recipient_name,omitempty"`
	DeviceFingerprint *string           `json:"device_fingerprint,omitempty"`
	CreatedBy         *uuid.UUID        `json:"created_by,omitempty"`
}

func (h *OTPHandler) RequestInternal(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken != "" && r.Header.Get("X-Internal-Token") != h.InternalToken {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	var in otpRequestReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	ip := clientIP(r)
	res, err := h.OTP.Request(r.Context(), otp.RequestInput{
		TenantID:          in.TenantID,
		Purpose:           in.Purpose,
		Channel:           in.Channel,
		SubjectUserID:     in.SubjectUserID,
		SubjectMemberID:   in.SubjectMemberID,
		SubjectIdentifier: in.SubjectIdentifier,
		Destination:       in.Destination,
		RecipientName:     in.RecipientName,
		IPAddress:         &ip,
		DeviceFingerprint: in.DeviceFingerprint,
		CreatedBy:         in.CreatedBy,
	})
	if err != nil {
		writeOTPErr(w, r, err)
		return
	}
	httpx.Created(w, res)
}

type otpVerifyReq struct {
	TenantID          uuid.UUID `json:"tenant_id"`
	OTPID             uuid.UUID `json:"otp_id"`
	Code              string    `json:"code"`
	DeviceFingerprint *string   `json:"device_fingerprint,omitempty"`
}

func (h *OTPHandler) VerifyInternal(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken != "" && r.Header.Get("X-Internal-Token") != h.InternalToken {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	var in otpVerifyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	ip := clientIP(r)
	res, err := h.OTP.Verify(r.Context(), otp.VerifyInput{
		TenantID:          in.TenantID,
		OTPID:             in.OTPID,
		Code:              in.Code,
		IPAddress:         &ip,
		DeviceFingerprint: in.DeviceFingerprint,
	})
	// Business outcomes (wrong / expired / exhausted / already-closed)
	// return BOTH a result AND an error. Always return the result so
	// the caller sees attempts_remaining; surface the outcome via a
	// 200 OK with verified=false. Real errors (DB / IO) still 5xx.
	if err != nil && res == nil {
		writeOTPErr(w, r, err)
		return
	}
	httpx.OK(w, res)
}

func writeOTPErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, otp.ErrCooldown):
		httpx.WriteErr(w, r, httpx.ErrConflict("resend cooldown not yet elapsed"))
	case errors.Is(err, otp.ErrExpired):
		httpx.WriteErr(w, r, httpx.ErrConflict("OTP code has expired"))
	case errors.Is(err, otp.ErrExhausted):
		httpx.WriteErr(w, r, httpx.ErrConflict("OTP attempts exhausted"))
	case errors.Is(err, otp.ErrInvalidCode):
		httpx.WriteErr(w, r, httpx.ErrConflict("OTP code does not match"))
	case errors.Is(err, otp.ErrAlreadyClosed):
		httpx.WriteErr(w, r, httpx.ErrConflict("OTP is not in a pending state"))
	default:
		httpx.WriteErr(w, r, err)
	}
}

// ─────────── Admin: settings + audit ───────────

func (h *OTPHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var s *domain.OTPSettings
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		s, err = h.Settings.GetTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, s)
}

type updateOTPSettingsReq struct {
	CodeLength            int            `json:"code_length"`
	ExpiryMinutes         int            `json:"expiry_minutes"`
	MaxAttempts           int            `json:"max_attempts"`
	ResendCooldownSeconds int            `json:"resend_cooldown_seconds"`
	DefaultChannel        domain.Channel `json:"default_channel"`
}

func (h *OTPHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in updateOTPSettingsReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CodeLength != 0 && (in.CodeLength < 4 || in.CodeLength > 8) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code_length must be between 4 and 8"))
		return
	}
	if in.MaxAttempts != 0 && (in.MaxAttempts < 3 || in.MaxAttempts > 5) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("max_attempts must be between 3 and 5"))
		return
	}
	if in.ResendCooldownSeconds != 0 && (in.ResendCooldownSeconds < 15 || in.ResendCooldownSeconds > 600) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("resend_cooldown_seconds must be between 15 and 600"))
		return
	}
	if in.ExpiryMinutes != 0 && (in.ExpiryMinutes < 1 || in.ExpiryMinutes > 60) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("expiry_minutes must be between 1 and 60"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.OTPSettings
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Settings.UpsertTx(r.Context(), tx, store.UpsertOTPSettingsInput{
			CodeLength:            in.CodeLength,
			ExpiryMinutes:         in.ExpiryMinutes,
			MaxAttempts:           in.MaxAttempts,
			ResendCooldownSeconds: in.ResendCooldownSeconds,
			DefaultChannel:        in.DefaultChannel,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *OTPHandler) ListRequests(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.OTPListFilter{
		Status:  q.Get("status"),
		Purpose: q.Get("purpose"),
		Limit:   limit,
		Offset:  offset,
	}
	var items []domain.OTPRequest
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.OTPs.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// ─────────── Helpers ───────────

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		return xf
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
