// Public guarantor-consent endpoints — no JWT required. The URL
// token IS the credential; ID + OTP verification on the public page
// proves the visitor IS the named guarantor.
//
//   GET  /p/guarantor-consent/{token}
//        Loads context for the consent page (loan + applicant info).
//        Returns 404 for unknown/expired/used tokens; the visitor's
//        page renders a friendly error.
//
//   POST /p/guarantor-consent/{token}/verify-id
//        Body: {national_id}
//        Checks the submitted ID against the guarantor's on-file
//        id_doc_number. On match: issues an OTP to the on-file phone
//        and returns {otp_sent_to: "+254****8842"} (masked). Wrong
//        ID counts against OTP attempts (poisoning the token after
//        max attempts).
//
//   POST /p/guarantor-consent/{token}/verify-otp
//        Body: {code}
//        Verifies the OTP. Sets otp_verified_at; subsequent /respond
//        calls require this. Wrong code increments attempts.
//
//   POST /p/guarantor-consent/{token}/respond
//        Body: {decision: 'accepted'|'declined'|'opted_offline',
//               reason: string?, signature_path: string?}
//        Requires prior OTP verification. Marks the token used,
//        records the decision, flips loan_guarantees.status
//        (accepted/declined) — opt_offline leaves status pending
//        and notifies the SACCO admin.
//
// Tenant resolution: the public layer doesn't have an X-Tenant
// header; it discovers the tenant from the token via a
// SECURITY DEFINER function, then sets app.tenant_id for the
// remainder of the request.

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

type PublicGuarantorConsentHandler struct {
	DB       *db.Pool
	Consent  *store.GuarantorConsentStore
	Notifier *notifier.Client
	Logger   *slog.Logger
}

// ─────────── token lookup helper ───────────

// withTokenContext is the workhorse: takes the URL token, hashes it,
// finds the tenant via the SECURITY DEFINER bridge, then runs `fn`
// inside a tenant-scoped tx. The handler returns 404 for unknown
// tokens (deliberately opaque — never confirm token existence
// without correct hash).
func (h *PublicGuarantorConsentHandler) withTokenContext(
	r *http.Request, fn func(ctx context.Context, tx pgx.Tx, tokenID, tenantID uuid.UUID) error,
) error {
	plaintext := chi.URLParam(r, "token")
	if plaintext == "" {
		return httpx.ErrBadRequest("missing token")
	}
	hash := store.HashToken(plaintext)
	tokenID, tenantID, err := h.Consent.FindTenantByHash(r.Context(), hash)
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

type publicTokenResp struct {
	GuarantorName        string `json:"guarantor_name"`
	GuarantorPhoneMasked string `json:"guarantor_phone_masked"`
	ApplicantName        string `json:"applicant_name"`
	ApplicationNo        string `json:"application_no"`
	ProductName          string `json:"product_name"`
	RequestedAmount      string `json:"requested_amount"`
	AmountGuaranteed     string `json:"amount_guaranteed"`
	GuaranteeStatus      string `json:"guarantee_status"`
	TenantName           string `json:"tenant_name"`
	ExpiresAt            string `json:"expires_at"`
	OTPIssued            bool   `json:"otp_issued"`
	OTPVerified          bool   `json:"otp_verified"`
	Decision             string `json:"decision,omitempty"`
}

func (h *PublicGuarantorConsentHandler) Get(w http.ResponseWriter, r *http.Request) {
	var out publicTokenResp
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		ctxBundle, lerr := h.Consent.LoadContextTx(ctx, tx, tokenID)
		// Note: LoadContextTx returns the partial context AND the
		// expired/used sentinel — we hand back what we have so the
		// public page can render "this link was used on 2026-05-29".
		if lerr != nil && !errors.Is(lerr, store.ErrConsentTokenExpired) &&
			!errors.Is(lerr, store.ErrConsentTokenUsed) {
			return lerr
		}
		out.GuarantorName = ctxBundle.GuarantorName
		out.GuarantorPhoneMasked = maskPhone(ctxBundle.GuarantorPhone)
		out.ApplicantName = ctxBundle.ApplicantName
		out.ApplicationNo = ctxBundle.ApplicationNo
		out.ProductName = ctxBundle.ProductName
		out.RequestedAmount = ctxBundle.RequestedAmount.String()
		out.AmountGuaranteed = ctxBundle.AmountGuaranteed.String()
		out.GuaranteeStatus = ctxBundle.GuaranteeStatus
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
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, out)
}

// ─────────── POST verify-id ───────────

type verifyIDReq struct {
	NationalID string `json:"national_id"`
}

func (h *PublicGuarantorConsentHandler) VerifyID(w http.ResponseWriter, r *http.Request) {
	var in verifyIDReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	submitted := strings.TrimSpace(in.NationalID)
	if submitted == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("national_id required")); return
	}
	var maskedPhone string
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		ctxBundle, lerr := h.Consent.LoadContextTx(ctx, tx, tokenID)
		if lerr != nil {
			return lerr
		}
		// Pull max-attempts from tenant settings.
		var maxAttempts int
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(guarantor_max_otp_attempts, 3) FROM tenant_operations
		`).Scan(&maxAttempts)
		if maxAttempts <= 0 {
			maxAttempts = 3
		}

		// Match against the guarantor's id_doc_number. We use the
		// same OTP-attempts counter the OTP step uses — three wrong
		// guesses (across ID + OTP) poisons the token. This makes
		// brute-forcing a guarantor's ID a 3-shot game per token.
		expected := strings.TrimSpace(ctxBundle.GuarantorIDDocNumber)
		if expected == "" || !strings.EqualFold(submitted, expected) {
			// Increment attempts; possibly poison.
			if vErr := h.Consent.VerifyOTPTx(ctx, tx, tokenID, "ID-MISMATCH-MARKER", maxAttempts); vErr != nil {
				// VerifyOTPTx returns ErrConsentOTPBadCode or
				// ErrConsentOTPExceeded; both are the right shape.
				if errors.Is(vErr, store.ErrConsentOTPExceeded) {
					return httpx.ErrForbidden("Too many wrong attempts. This link has been disabled — contact your SACCO.")
				}
				return httpx.ErrBadRequest("National ID does not match the guarantor on file.")
			}
		}

		// ID matched — issue an OTP to the on-file phone.
		if ctxBundle.GuarantorPhone == "" {
			return httpx.ErrConflict("No phone number on file for this guarantor. Contact your SACCO to update before consenting.")
		}
		otpPlain, ierr := h.Consent.IssueOTPTx(ctx, tx, tokenID, ctxBundle.GuarantorPhone, otpValidFor())
		if ierr != nil {
			return ierr
		}
		maskedPhone = maskPhone(ctxBundle.GuarantorPhone)

		// Dispatch OTP SMS — fire-and-forget via the notifier. Body
		// uses an inline template since the OTP send is a control
		// event, not a per-tenant brandable message.
		if h.Notifier != nil {
			body := "Your nexusSacco verification code is " + otpPlain + ". Valid for 10 minutes. Do not share."
			phone := ctxBundle.GuarantorPhone
			h.Notifier.Notify(ctx, notifier.Request{
				TenantID:       tid,
				EventCode:      "GUARANTOR_CONSENT_REQUEST",
				Channels:       []notifier.Channel{notifier.ChannelSMS},
				RecipientName:  ctxBundle.GuarantorName,
				RecipientPhone: &phone,
				Payload:        map[string]any{"body": body},
			})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, map[string]any{
		"otp_sent_to": maskedPhone,
		"otp_valid_minutes": int(otpValidFor().Minutes()),
	})
}

// ─────────── POST verify-otp ───────────

type verifyOTPReq struct {
	Code string `json:"code"`
}

func (h *PublicGuarantorConsentHandler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var in verifyOTPReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	code := strings.TrimSpace(in.Code)
	if code == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code required")); return
	}
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, tokenID, tid uuid.UUID) error {
		var maxAttempts int
		_ = tx.QueryRow(ctx, `SELECT COALESCE(guarantor_max_otp_attempts, 3) FROM tenant_operations`).Scan(&maxAttempts)
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if vErr := h.Consent.VerifyOTPTx(ctx, tx, tokenID, code, maxAttempts); vErr != nil {
			switch {
			case errors.Is(vErr, store.ErrConsentOTPExceeded):
				return httpx.ErrForbidden("Too many wrong attempts. This link has been disabled — contact your SACCO.")
			case errors.Is(vErr, store.ErrConsentOTPExpired):
				return httpx.ErrConflict("OTP expired. Request a fresh code by re-entering your National ID.")
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
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, map[string]any{"verified": true})
}

// ─────────── POST respond ───────────

type respondReq struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (h *PublicGuarantorConsentHandler) Respond(w http.ResponseWriter, r *http.Request) {
	var in respondReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
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
		ctxBundle, lerr := h.Consent.LoadContextTx(ctx, tx, tokenID)
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
		if rerr := h.Consent.RecordDecisionTx(ctx, tx, tokenID, in.Decision, reasonPtr, nil, ipPtr, uaPtr); rerr != nil {
			return rerr
		}

		// Flip the underlying guarantee row for the terminal decisions.
		// opted_offline leaves status pending_consent — the admin
		// will resolve it later via "Record signed consent".
		switch in.Decision {
		case "accepted", "declined":
			declinePtr := reasonPtr
			if in.Decision == "accepted" {
				declinePtr = nil
			}
			// nil responded_by because the public path has no JWT;
			// the audit trail lives on guarantor_consent_tokens
			// (decision + ip_address + user_agent).
			if _, err := h.Consent.UpdateGuaranteeFromTokenTx(ctx, tx,
				ctxBundle.Token.GuaranteeID, in.Decision, declinePtr,
			); err != nil {
				return err
			}
		case "opted_offline":
			// Status stays. The SACCO admin gets notified separately
			// (deferred to a follow-up — for now the admin sees this
			// via the application detail page's Guarantors tab).
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, map[string]any{"decision": in.Decision})
}

// ─────────── helpers ───────────

func otpValidFor() time.Duration { return 10 * time.Minute }

// maskPhone returns a masked form like "+254****8842" so the page
// can confirm "the OTP went to your phone" without echoing the
// full number back to an attacker who's only seen the SMS link.
func maskPhone(p string) string {
	p = strings.TrimSpace(p)
	if len(p) <= 4 {
		return p
	}
	prefix := p[:4]
	suffix := p[len(p)-4:]
	return prefix + strings.Repeat("*", len(p)-8) + suffix
}

// middlewareKeyByIP mirrors middleware.KeyByIP (kept local so the
// handler doesn't import the middleware package — clean separation).
func middlewareKeyByIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx > 0 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}
