// Loans Phase 5 — per-tenant guarantor-SMS-consent settings.
//
// Mounted at /v1/loans/policy/guarantor-sms (GET + PUT), reusing the
// same loans:policy:write permission that gates DPD thresholds.
//
//   GET  /v1/loans/policy/guarantor-sms
//        Returns the seven knobs from tenant_operations that drive
//        the SMS-based consent flow (enabled toggle, template body,
//        token expiry, reminder hours, max-OTP attempts, public URL).
//
//   PUT  /v1/loans/policy/guarantor-sms
//        Updates the same seven columns. Values that are valid but
//        unusual (e.g. expiry 30 days) are accepted — operators can
//        tune to their callcenter capacity.
//
// The settings affect the next IssueConsentForGuarantee call (on
// application Create / topup copy / admin Resend) and the next
// guarantor-reminder worker tick.

package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type GuarantorSMSPolicyHandler struct {
	Pool   *db.Pool
	Logger *slog.Logger
}

type guarantorSMSPolicy struct {
	Enabled              bool   `json:"enabled"`
	Template             string `json:"template"`
	TokenExpiryDays      int    `json:"token_expiry_days"`
	ReminderHoursFirst   int    `json:"reminder_hours_first"`
	ReminderHoursSecond  int    `json:"reminder_hours_second"`
	MaxOTPAttempts       int    `json:"max_otp_attempts"`
	PublicBaseURL        string `json:"public_base_url"`
}

func (h *GuarantorSMSPolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out guarantorSMSPolicy
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT
			  COALESCE(guarantor_sms_enabled, true),
			  COALESCE(guarantor_sms_template, ''),
			  COALESCE(guarantor_token_expiry_days, 7),
			  COALESCE(guarantor_reminder_hours_first, 48),
			  COALESCE(guarantor_reminder_hours_second, 144),
			  COALESCE(guarantor_max_otp_attempts, 3),
			  COALESCE(guarantor_public_base_url, '')
			  FROM tenant_operations
			 LIMIT 1
		`).Scan(
			&out.Enabled, &out.Template, &out.TokenExpiryDays,
			&out.ReminderHoursFirst, &out.ReminderHoursSecond,
			&out.MaxOTPAttempts, &out.PublicBaseURL,
		)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *GuarantorSMSPolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	var in guarantorSMSPolicy
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TokenExpiryDays < 1 || in.TokenExpiryDays > 90 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("token_expiry_days must be 1..90"))
		return
	}
	if in.ReminderHoursFirst < 1 || in.ReminderHoursFirst >= in.ReminderHoursSecond {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reminder_hours_first must be < reminder_hours_second"))
		return
	}
	if in.MaxOTPAttempts < 1 || in.MaxOTPAttempts > 10 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("max_otp_attempts must be 1..10"))
		return
	}
	tpl := strings.TrimSpace(in.Template)
	if tpl != "" && !strings.Contains(tpl, "{{link}}") {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("template must include {{link}} so the SMS contains the consent URL"))
		return
	}
	url := strings.TrimSpace(in.PublicBaseURL)
	if url != "" && !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("public_base_url must start with http:// or https://"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE tenant_operations SET
			  guarantor_sms_enabled            = $1,
			  guarantor_sms_template           = NULLIF($2, ''),
			  guarantor_token_expiry_days      = $3,
			  guarantor_reminder_hours_first   = $4,
			  guarantor_reminder_hours_second  = $5,
			  guarantor_max_otp_attempts       = $6,
			  guarantor_public_base_url        = NULLIF($7, ''),
			  updated_at                       = now()
		`, in.Enabled, tpl, in.TokenExpiryDays,
			in.ReminderHoursFirst, in.ReminderHoursSecond,
			in.MaxOTPAttempts, url)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
