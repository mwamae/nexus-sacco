// Centralized OTP service — Stage 6.
//
// Generate / verify codes. Delivery uses the existing notification
// pipeline: we create a notifications + notification_deliveries row
// for the OTP_REQUESTED event in the SAME tx as the otp_requests row,
// and the SMS / email worker drains the queue normally.
//
// Codes are HMAC-SHA256-hashed with the service crypto key before
// storage. Verification recomputes the hash and constant-time compares.

package otp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

type Service struct {
	DB            *db.Pool
	OTPs          *store.OTPStore
	Settings      *store.OTPSettingsStore
	Notifications *store.NotificationStore
	Templates     *store.TemplateStore
	HashKey       []byte // first 32 bytes used as HMAC key
}

// ─────────── Errors ───────────

var (
	ErrCooldown      = errors.New("otp: resend cooldown not yet elapsed")
	ErrExpired       = errors.New("otp: code has expired")
	ErrExhausted     = errors.New("otp: max verification attempts reached")
	ErrInvalidCode   = errors.New("otp: code does not match")
	ErrAlreadyClosed = errors.New("otp: code is not in pending state")
)

// ─────────── Inputs / outputs ───────────

type RequestInput struct {
	TenantID          uuid.UUID
	Purpose           domain.OTPPurpose
	Channel           domain.Channel  // optional override; defaults to tenant policy
	SubjectUserID     *uuid.UUID
	SubjectMemberID   *uuid.UUID
	SubjectIdentifier *string
	Destination       string         // phone (sms) or email (email) to send to
	RecipientName     string
	IPAddress         *string
	DeviceFingerprint *string
	CreatedBy         *uuid.UUID
}

type RequestResult struct {
	OTPID              uuid.UUID  `json:"otp_id"`
	Channel            domain.Channel `json:"channel"`
	DestinationMasked  string     `json:"destination_masked"`
	ExpiresAt          time.Time  `json:"expires_at"`
	MaxAttempts        int        `json:"max_attempts"`
	ResendAfterSeconds int        `json:"resend_after_seconds"`
}

type VerifyInput struct {
	TenantID          uuid.UUID
	OTPID             uuid.UUID
	Code              string
	IPAddress         *string
	DeviceFingerprint *string
}

type VerifyResult struct {
	OTPID              uuid.UUID `json:"otp_id"`
	Verified           bool      `json:"verified"`
	Status             domain.OTPStatus `json:"status"`
	AttemptsUsed       int       `json:"attempts_used"`
	AttemptsRemaining  int       `json:"attempts_remaining"`
}

// ─────────── Request ───────────

func (s *Service) Request(ctx context.Context, in RequestInput) (*RequestResult, error) {
	if in.TenantID == uuid.Nil {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if !in.Purpose.Valid() {
		return nil, fmt.Errorf("invalid purpose")
	}
	if in.Destination == "" {
		return nil, fmt.Errorf("destination is required")
	}
	var out *RequestResult
	err := s.DB.WithTenantTx(ctx, in.TenantID, func(tx pgx.Tx) error {
		settings, err := s.Settings.GetTx(ctx, tx)
		if err != nil {
			return err
		}

		// Resolve channel — explicit override or tenant default.
		ch := in.Channel
		if ch == "" {
			ch = settings.DefaultChannel
		}
		if !ch.Valid() {
			return fmt.Errorf("invalid channel %q", ch)
		}

		// Cooldown — most recent request for the same (purpose, subject).
		mostRecent, err := s.OTPs.MostRecentForCooldownTx(
			ctx, tx, in.Purpose,
			in.SubjectUserID, in.SubjectMemberID, in.SubjectIdentifier,
		)
		if err != nil {
			return err
		}
		if mostRecent != nil {
			cooldown := time.Duration(settings.ResendCooldownSeconds) * time.Second
			if time.Since(*mostRecent) < cooldown {
				return ErrCooldown
			}
		}

		// Generate + hash + insert.
		code, err := generateCode(settings.CodeLength)
		if err != nil {
			return err
		}
		codeHash := hashCode(s.HashKey, code)
		expires := time.Now().Add(time.Duration(settings.ExpiryMinutes) * time.Minute)

		otp, err := s.OTPs.CreateTx(ctx, tx, store.CreateOTPInput{
			Purpose:           in.Purpose,
			SubjectUserID:     in.SubjectUserID,
			SubjectMemberID:   in.SubjectMemberID,
			SubjectIdentifier: in.SubjectIdentifier,
			Channel:           ch,
			Destination:       in.Destination,
			CodeHash:          codeHash,
			CodeLength:        settings.CodeLength,
			MaxAttempts:       settings.MaxAttempts,
			ExpiresAt:         expires,
			IPAddress:         in.IPAddress,
			DeviceFingerprint: in.DeviceFingerprint,
			CreatedBy:         in.CreatedBy,
		})
		if err != nil {
			return err
		}

		// Dispatch via the notification pipeline. Resolve the OTP_REQUESTED
		// template for this channel and render with the plaintext code +
		// expiry — the body goes into notification_deliveries.body so the
		// SMS/email worker can pick it up.
		tpl, err := s.Templates.ActiveByEventChannelTx(ctx, tx, "OTP_REQUESTED", ch)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"otp":             code,
			"expiry_minutes":  settings.ExpiryMinutes,
			"recipient_name":  in.RecipientName,
		}
		body := "Your code is " + code + " (expires in " + itoa(settings.ExpiryMinutes) + " minutes)."
		var subject *string
		var templateID *uuid.UUID
		if tpl != nil {
			body = store.RenderTemplate(tpl.Body, payload)
			if tpl.Subject != nil {
				rendered := store.RenderTemplate(*tpl.Subject, payload)
				subject = &rendered
			}
			id := tpl.ID
			templateID = &id
		}
		var phonePtr, emailPtr *string
		switch ch {
		case domain.ChannelSMS:
			phonePtr = &in.Destination
		case domain.ChannelEmail:
			emailPtr = &in.Destination
		}
		sourceModule := "notification.otp"
		recordID := otp.ID
		notif, err := s.Notifications.CreateTx(ctx, tx, store.CreateInput{
			EventCode:       "OTP_REQUESTED",
			Priority:        domain.PriorityInfo,
			RecipientUserID: in.SubjectUserID,
			RecipientMemberID: in.SubjectMemberID,
			RecipientName:   in.RecipientName,
			RecipientPhone:  phonePtr,
			RecipientEmail:  emailPtr,
			SourceModule:    &sourceModule,
			SourceRecordID:  &recordID,
			Payload:         map[string]any{"purpose": string(in.Purpose), "expiry_minutes": settings.ExpiryMinutes},
			InitiatedBy:     in.CreatedBy,
		})
		if err != nil {
			return err
		}
		if _, err := s.Notifications.CreateDeliveryTx(ctx, tx, store.CreateDeliveryInput{
			NotificationID: notif.ID,
			Channel:        ch,
			TemplateID:     templateID,
			Subject:        subject,
			Body:           body,
		}); err != nil {
			return err
		}
		if err := s.OTPs.AttachNotificationIDTx(ctx, tx, otp.ID, notif.ID); err != nil {
			return err
		}

		out = &RequestResult{
			OTPID:              otp.ID,
			Channel:            ch,
			DestinationMasked:  maskDestination(ch, in.Destination),
			ExpiresAt:          expires,
			MaxAttempts:        settings.MaxAttempts,
			ResendAfterSeconds: settings.ResendCooldownSeconds,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ─────────── Verify ───────────

func (s *Service) Verify(ctx context.Context, in VerifyInput) (*VerifyResult, error) {
	if in.TenantID == uuid.Nil || in.OTPID == uuid.Nil {
		return nil, fmt.Errorf("tenant_id and otp_id are required")
	}
	if in.Code == "" {
		return nil, fmt.Errorf("code is required")
	}
	// Business-level verify outcomes (invalid / expired / exhausted /
	// already-closed) MUST commit the attempt-count + status changes —
	// returning an error from the closure would roll those back and
	// stale state would leak into the next attempt. Capture the
	// outcome in outcomeErr, return nil from the closure, then bubble
	// the error to the HTTP layer outside the tx.
	var out *VerifyResult
	var outcomeErr error
	err := s.DB.WithTenantTx(ctx, in.TenantID, func(tx pgx.Tx) error {
		otp, err := s.OTPs.GetTx(ctx, tx, in.OTPID)
		if err != nil {
			return err
		}
		if otp.Status != domain.OTPStatusPending {
			outcomeErr = ErrAlreadyClosed
			out = &VerifyResult{
				OTPID: otp.ID, Verified: false, Status: otp.Status,
				AttemptsUsed: otp.AttemptsUsed, AttemptsRemaining: 0,
			}
			return nil
		}
		if time.Now().After(otp.ExpiresAt) {
			if err := s.OTPs.MarkStatusTx(ctx, tx, otp.ID, domain.OTPStatusExpired); err != nil {
				return err
			}
			outcomeErr = ErrExpired
			out = &VerifyResult{
				OTPID: otp.ID, Verified: false, Status: domain.OTPStatusExpired,
				AttemptsUsed: otp.AttemptsUsed, AttemptsRemaining: 0,
			}
			return nil
		}
		attempts, err := s.OTPs.IncrementAttemptsTx(ctx, tx, otp.ID)
		if err != nil {
			return err
		}
		submitted := hashCode(s.HashKey, in.Code)
		if subtle.ConstantTimeCompare([]byte(submitted), []byte(otp.CodeHash)) == 1 {
			if err := s.OTPs.MarkVerifiedTx(ctx, tx, otp.ID); err != nil {
				return err
			}
			out = &VerifyResult{
				OTPID: otp.ID, Verified: true, Status: domain.OTPStatusVerified,
				AttemptsUsed: attempts, AttemptsRemaining: 0,
			}
			return nil
		}
		// Wrong code.
		remaining := otp.MaxAttempts - attempts
		if remaining <= 0 {
			if err := s.OTPs.MarkStatusTx(ctx, tx, otp.ID, domain.OTPStatusExhausted); err != nil {
				return err
			}
			outcomeErr = ErrExhausted
			out = &VerifyResult{
				OTPID: otp.ID, Verified: false, Status: domain.OTPStatusExhausted,
				AttemptsUsed: attempts, AttemptsRemaining: 0,
			}
			return nil
		}
		outcomeErr = ErrInvalidCode
		out = &VerifyResult{
			OTPID: otp.ID, Verified: false, Status: domain.OTPStatusPending,
			AttemptsUsed: attempts, AttemptsRemaining: remaining,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, outcomeErr
}

// ─────────── Helpers ───────────

// generateCode returns a uniformly-random N-digit numeric string.
// Uses crypto/rand to avoid biased modulo distribution.
func generateCode(length int) (string, error) {
	if length < 4 || length > 8 {
		length = 6
	}
	max := big.NewInt(10)
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + n.Int64())
	}
	return string(out), nil
}

func hashCode(key []byte, code string) string {
	if len(key) > 32 {
		key = key[:32]
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(code))
	return hex.EncodeToString(h.Sum(nil))
}

// maskDestination obfuscates the address we just sent the code to —
// the API echoes this back so the UI can show "code sent to ***1234".
func maskDestination(ch domain.Channel, d string) string {
	if ch == domain.ChannelEmail {
		at := -1
		for i, r := range d {
			if r == '@' {
				at = i
				break
			}
		}
		if at <= 1 {
			return d
		}
		// keep first char + last char of local-part, plus domain
		return string(d[0]) + "***" + string(d[at-1:])
	}
	// SMS — keep last 4 digits.
	if len(d) <= 4 {
		return d
	}
	return "***" + d[len(d)-4:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
