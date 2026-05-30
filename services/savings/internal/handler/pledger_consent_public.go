// Phase 1.5b — public pledger consent endpoints.
//
//   GET  /p/pledger-consent/{token}                 — load context
//   POST /p/pledger-consent/{token}/verify-id       — National ID + OTP issue
//   POST /p/pledger-consent/{token}/verify-otp      — submit 6-digit OTP
//   POST /p/pledger-consent/{token}/respond         — accept | declined | opted_offline
//
// Tenant resolution: the public layer has no X-Tenant header; the
// SECURITY DEFINER bridge find_pledger_token_tenant() looks up the
// tenant from the token hash before we open the tenant-scoped tx.
//
// Mirrors the guarantor-consent public handler 1:1. The two flows
// share storage helpers (NewOTP / hashOTP / constantTimeEqual / error
// sentinels) so error UX is identical across them.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type PublicPledgerConsentHandler struct {
	DB       *db.Pool
	Tokens   *store.PledgerConsentStore
	Notifier *notifier.Client
	Logger   *slog.Logger
}

func (h *PublicPledgerConsentHandler) withTokenContext(
	r *http.Request, fn func(ctx context.Context, tx pgx.Tx, tokenID, tenantID uuid.UUID) error,
) error {
	plaintext := chi.URLParam(r, "token")
	if plaintext == "" {
		return httpx.ErrBadRequest("missing token")
	}
	hash := store.HashToken(plaintext)
	tokenID, tenantID, err := h.Tokens.FindTenantByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrConsentTokenNotFound) {
			return httpx.ErrNotFound("consent link not found or expired")
		}
		return err
	}
	return h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return fn(r.Context(), tx, tokenID, tenantID)
	})
}

// ─────────── GET context ───────────

type pledgerTokenResp struct {
	PledgerName           string `json:"pledger_name"`
	PledgerPhoneMasked    string `json:"pledger_phone_masked"`
	ApplicantName         string `json:"applicant_name"`
	ApplicationNo         string `json:"application_no"`
	ProductName           string `json:"product_name"`
	RequestedAmount       string `json:"requested_amount"`
	CollateralKind        string `json:"collateral_kind"`
	CollateralDescription string `json:"collateral_description"`
	EstimatedValue        string `json:"estimated_value"`
	TenantName            string `json:"tenant_name"`
	ExpiresAt             string `json:"expires_at"`
	OTPIssued             bool   `json:"otp_issued"`
	OTPVerified           bool   `json:"otp_verified"`
	Decision              string `json:"decision,omitempty"`
}

func (h *PublicPledgerConsentHandler) Get(w http.ResponseWriter, r *http.Request) {
	var out pledgerTokenResp
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		ctxBundle, lerr := h.Tokens.LoadContextTx(ctx, tx, tokenID)
		if lerr != nil &&
			!errors.Is(lerr, store.ErrConsentTokenExpired) &&
			!errors.Is(lerr, store.ErrConsentTokenUsed) {
			return lerr
		}
		out.PledgerName = ctxBundle.PledgerName
		out.PledgerPhoneMasked = maskPhone(ctxBundle.PledgerPhone)
		out.ApplicantName = ctxBundle.ApplicantName
		out.ApplicationNo = ctxBundle.ApplicationNo
		out.ProductName = ctxBundle.ProductName
		out.RequestedAmount = ctxBundle.RequestedAmount.String()
		out.CollateralKind = ctxBundle.CollateralKind
		out.CollateralDescription = ctxBundle.CollateralDescription
		out.EstimatedValue = ctxBundle.EstimatedValue.String()
		out.TenantName = ctxBundle.TenantName
		out.ExpiresAt = ctxBundle.Token.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		out.OTPIssued = ctxBundle.Token.OTPSentTo != nil && *ctxBundle.Token.OTPSentTo != ""
		out.OTPVerified = ctxBundle.Token.OTPVerifiedAt != nil
		if ctxBundle.Token.Decision != nil {
			out.Decision = *ctxBundle.Token.Decision
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── POST verify-id ───────────

type pledgerVerifyIDReq struct {
	NationalID string `json:"national_id"`
}

func (h *PublicPledgerConsentHandler) VerifyID(w http.ResponseWriter, r *http.Request) {
	var in pledgerVerifyIDReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	submitted := strings.TrimSpace(in.NationalID)
	if submitted == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("national_id required"))
		return
	}
	var maskedPhone string
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		ctxBundle, lerr := h.Tokens.LoadContextTx(ctx, tx, tokenID)
		if lerr != nil {
			return lerr
		}
		var maxAttempts int
		_ = tx.QueryRow(ctx, `SELECT COALESCE(guarantor_max_otp_attempts, 3) FROM tenant_operations`).Scan(&maxAttempts)
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		expected := strings.TrimSpace(ctxBundle.PledgerIDDocNumber)
		if expected == "" || !strings.EqualFold(submitted, expected) {
			if vErr := h.Tokens.VerifyOTPTx(ctx, tx, tokenID, "ID-MISMATCH-MARKER", maxAttempts); vErr != nil {
				if errors.Is(vErr, store.ErrConsentOTPExceeded) {
					return httpx.ErrForbidden("Too many wrong attempts. This link has been disabled — contact the SACCO.")
				}
				return httpx.ErrBadRequest("National ID does not match the pledger on file.")
			}
		}
		if ctxBundle.PledgerPhone == "" {
			return httpx.ErrConflict("No phone on file for this pledger; contact the SACCO to update before consenting.")
		}
		otpPlain, ierr := h.Tokens.IssueOTPTx(ctx, tx, tokenID, ctxBundle.PledgerPhone, 10*time.Minute)
		if ierr != nil {
			return ierr
		}
		maskedPhone = maskPhone(ctxBundle.PledgerPhone)

		if h.Notifier != nil {
			body := "Your nexusSacco verification code is " + otpPlain + ". Valid for 10 minutes. Do not share."
			phone := ctxBundle.PledgerPhone
			h.Notifier.Notify(ctx, notifier.Request{
				TenantID:       tid,
				EventCode:      "GUARANTOR_CONSENT_REQUEST", // reuse passthrough event
				Channels:       []notifier.Channel{notifier.ChannelSMS},
				RecipientName:  ctxBundle.PledgerName,
				RecipientPhone: &phone,
				Payload:        map[string]any{"body": body},
			})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"otp_sent_to":       maskedPhone,
		"otp_valid_minutes": 10,
	})
}

// ─────────── POST verify-otp ───────────

type pledgerVerifyOTPReq struct {
	Code string `json:"code"`
}

func (h *PublicPledgerConsentHandler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var in pledgerVerifyOTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	code := strings.TrimSpace(in.Code)
	if code == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code required"))
		return
	}
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		var maxAttempts int
		_ = tx.QueryRow(ctx, `SELECT COALESCE(guarantor_max_otp_attempts, 3) FROM tenant_operations`).Scan(&maxAttempts)
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if vErr := h.Tokens.VerifyOTPTx(ctx, tx, tokenID, code, maxAttempts); vErr != nil {
			switch {
			case errors.Is(vErr, store.ErrConsentOTPExceeded):
				return httpx.ErrForbidden("Too many wrong attempts. This link has been disabled.")
			case errors.Is(vErr, store.ErrConsentOTPExpired):
				return httpx.ErrConflict("OTP expired. Re-enter your National ID to receive a new code.")
			case errors.Is(vErr, store.ErrConsentOTPBadCode):
				return httpx.ErrBadRequest("OTP did not match. Check the code and try again.")
			case errors.Is(vErr, store.ErrConsentOTPNotIssued):
				return httpx.ErrConflict("Verify your National ID first.")
			default:
				return vErr
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"verified": true})
}

// ─────────── POST respond ───────────

type pledgerRespondReq struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (h *PublicPledgerConsentHandler) Respond(w http.ResponseWriter, r *http.Request) {
	var in pledgerRespondReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	switch in.Decision {
	case "accepted", "declined", "opted_offline":
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decision must be accepted | declined | opted_offline"))
		return
	}
	if in.Decision == "declined" && strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required when declining"))
		return
	}
	clientIP := middlewareKeyByIP(r)
	userAgent := r.Header.Get("User-Agent")

	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		ctxBundle, lerr := h.Tokens.LoadContextTx(ctx, tx, tokenID)
		if lerr != nil {
			return lerr
		}
		if ctxBundle.Token.OTPVerifiedAt == nil {
			return httpx.ErrForbidden("Verify your phone via OTP before responding.")
		}
		var reasonPtr *string
		if r := strings.TrimSpace(in.Reason); r != "" {
			reasonPtr = &r
		}
		var ipPtr, uaPtr *string
		if clientIP != "" {
			ipPtr = &clientIP
		}
		if userAgent != "" {
			uaPtr = &userAgent
		}
		if rerr := h.Tokens.RecordDecisionTx(ctx, tx, tokenID, in.Decision, reasonPtr, nil, ipPtr, uaPtr); rerr != nil {
			return rerr
		}
		if uerr := h.Tokens.UpdateCollateralFromTokenTx(ctx, tx, ctxBundle.Token.CollateralID, in.Decision); uerr != nil {
			return uerr
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"decision": in.Decision})
}
