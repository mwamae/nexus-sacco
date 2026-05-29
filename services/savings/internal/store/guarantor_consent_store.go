// Guarantor consent token + OTP storage.
//
// One row per (guarantee, attempt). The plaintext token only ever
// exists in the SMS body + the visitor's URL bar — the DB stores
// SHA-256 of it. The visitor's submit-OTP step picks up an OTP code
// stored hashed on the same row, with a counter that's poisoned
// after the tenant-configured max-attempts.
//
// Public-endpoint lookups use the SECURITY DEFINER function
// find_guarantor_token_tenant() to discover the row's tenant before
// switching app.tenant_id. All other reads + writes go through normal
// tenant RLS.

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type GuarantorConsentStore struct {
	pool *pgxpool.Pool
}

func NewGuarantorConsentStore(pool *pgxpool.Pool) *GuarantorConsentStore {
	return &GuarantorConsentStore{pool: pool}
}

// ─────────── Token + OTP value helpers ───────────

// NewToken returns a URL-safe random token of ~32 chars (24 random
// bytes base64-encoded). Returns both the plaintext (for the SMS
// body) and the hash (for storage).
func NewToken() (plaintext string, hash []byte, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

// HashToken returns the storage hash for a plaintext token. Same
// algorithm as NewToken so a visitor's URL-bar token can be matched
// to a stored row.
func HashToken(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}

// NewOTP returns a 6-digit numeric code (plaintext + hash). Numeric
// keypads are universal on Kenyan feature phones; alpha codes would
// trip elderly users.
func NewOTP() (plaintext string, hash []byte, err error) {
	const digits = "0123456789"
	out := make([]byte, 6)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", nil, err
		}
		out[i] = digits[n.Int64()]
	}
	plaintext = string(out)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

func hashOTP(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}

// ShortRef returns a 6-char human-quotable prefix of the token (for
// the SMS "Ref: ABC123" field). Read off the hash so the SMS sender
// doesn't need the plaintext lying around after dispatch.
func ShortRef(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:3])
}

// ─────────── Public types ───────────

type ConsentToken struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	GuaranteeID           uuid.UUID
	AttemptNumber         int
	CreatedAt             time.Time
	ExpiresAt             time.Time
	UsedAt                *time.Time
	Decision              *string
	DecisionReason        *string
	DecisionSignaturePath *string
	OTPSentTo             *string
	OTPExpiresAt          *time.Time
	OTPAttempts           int
	OTPVerifiedAt         *time.Time
}

// ConsentTokenContext bundles the row + the guarantee + the
// application context that the public page needs to render (no
// secrets included).
type ConsentTokenContext struct {
	Token                ConsentToken
	GuarantorName        string
	GuarantorMemberNo    string
	GuarantorIDDocNumber string // National ID — never sent to the public page; only used for verify
	GuarantorPhone       string // phone the OTP goes to; only sent partially-masked to UI
	ApplicantName        string
	ApplicationNo        string
	ProductName          string
	RequestedAmount      decimal.Decimal
	AmountGuaranteed     decimal.Decimal
	GuaranteeStatus      string // current loan_guarantees.status
	TenantName           string
	TenantSlug           string
}

// Errors callers may check.
var (
	ErrConsentTokenNotFound = errors.New("guarantor consent token not found")
	ErrConsentTokenExpired  = errors.New("guarantor consent token has expired")
	ErrConsentTokenUsed     = errors.New("guarantor consent token has already been used")
	ErrConsentOTPBadCode    = errors.New("OTP did not match")
	ErrConsentOTPExpired    = errors.New("OTP has expired")
	ErrConsentOTPExceeded   = errors.New("max OTP attempts exceeded; token invalidated")
	ErrConsentOTPNotIssued  = errors.New("no OTP has been issued for this token")
)

// ─────────── Find-by-hash (uses SECURITY DEFINER for the tenant lookup) ───────────

// FindTenantByHash looks up the tenant_id for a token hash without
// requiring tenant context. Returns (uuid.Nil, ErrConsentTokenNotFound)
// when no row matches. Public-endpoint callers use this to discover
// the tenant + set app.tenant_id, then run normal tenant-scoped reads.
func (s *GuarantorConsentStore) FindTenantByHash(ctx context.Context, hash []byte) (tokenID, tenantID uuid.UUID, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT token_id, tenant_id FROM find_guarantor_token_tenant($1)
	`, hash).Scan(&tokenID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrConsentTokenNotFound
	}
	return tokenID, tenantID, err
}

// ─────────── Token CRUD (tenant-scoped, run inside WithTenantTx) ───────────

// CreateTx creates a token row. Caller supplies the hash + expiry.
// Returns the new token id.
func (s *GuarantorConsentStore) CreateTx(
	ctx context.Context, tx pgx.Tx,
	guaranteeID uuid.UUID, hash []byte, expiresAt time.Time,
	attemptNumber int, createdBy uuid.UUID,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO guarantor_consent_tokens (
		  tenant_id, guarantee_id, token_hash, attempt_number,
		  expires_at, created_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		RETURNING id
	`, guaranteeID, hash, attemptNumber, expiresAt, createdBy).Scan(&id)
	return id, err
}

// LoadContextTx hydrates the row + the surrounding guarantee +
// application + tenant info the public page renders. Returns the
// usable errors above so the handler can map them to clean HTTP
// responses.
func (s *GuarantorConsentStore) LoadContextTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID,
) (*ConsentTokenContext, error) {
	var out ConsentTokenContext
	err := tx.QueryRow(ctx, `
		SELECT
		  t.id, t.tenant_id, t.guarantee_id, t.attempt_number,
		  t.created_at, t.expires_at, t.used_at, t.decision,
		  t.decision_reason, t.decision_signature_path,
		  t.otp_sent_to, t.otp_expires_at, t.otp_attempts, t.otp_verified_at,
		  COALESCE(cd_g.full_name, ''), COALESCE(cd_g.member_no, ''),
		  COALESCE(m_g.id_doc_number, ''), COALESCE(m_g.phone, ''),
		  COALESCE(cd_a.full_name, ''), a.application_no, p.name,
		  a.requested_amount, g.amount_guaranteed, g.status::text,
		  ten.name, ten.slug
		  FROM guarantor_consent_tokens t
		  JOIN loan_guarantees g ON g.id = t.guarantee_id
		  JOIN loan_applications a ON a.id = g.application_id
		  JOIN loan_products p ON p.id = a.product_id
		  JOIN tenants ten ON ten.id = t.tenant_id
		  LEFT JOIN counterparty_directory cd_g ON cd_g.counterparty_id = g.guarantor_counterparty_id
		  LEFT JOIN members m_g ON m_g.id = cd_g.member_id
		  LEFT JOIN counterparty_directory cd_a ON cd_a.counterparty_id = a.counterparty_id
		 WHERE t.id = $1
	`, tokenID).Scan(
		&out.Token.ID, &out.Token.TenantID, &out.Token.GuaranteeID, &out.Token.AttemptNumber,
		&out.Token.CreatedAt, &out.Token.ExpiresAt, &out.Token.UsedAt, &out.Token.Decision,
		&out.Token.DecisionReason, &out.Token.DecisionSignaturePath,
		&out.Token.OTPSentTo, &out.Token.OTPExpiresAt, &out.Token.OTPAttempts, &out.Token.OTPVerifiedAt,
		&out.GuarantorName, &out.GuarantorMemberNo,
		&out.GuarantorIDDocNumber, &out.GuarantorPhone,
		&out.ApplicantName, &out.ApplicationNo, &out.ProductName,
		&out.RequestedAmount, &out.AmountGuaranteed, &out.GuaranteeStatus,
		&out.TenantName, &out.TenantSlug,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConsentTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	if out.Token.UsedAt != nil {
		return &out, ErrConsentTokenUsed
	}
	if time.Now().UTC().After(out.Token.ExpiresAt) {
		return &out, ErrConsentTokenExpired
	}
	return &out, nil
}

// IssueOTPTx generates + stores a fresh OTP (replacing any prior),
// extends the expiry, returns the plaintext so the caller can send
// it via SMS.
func (s *GuarantorConsentStore) IssueOTPTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID, phone string,
	validFor time.Duration,
) (otpPlaintext string, err error) {
	plaintext, hash, err := NewOTP()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE guarantor_consent_tokens
		   SET otp_code_hash    = $2,
		       otp_sent_to      = $3,
		       otp_expires_at   = $4,
		       otp_attempts     = 0,
		       otp_verified_at  = NULL
		 WHERE id = $1
	`, tokenID, hash, phone, time.Now().UTC().Add(validFor)); err != nil {
		return "", err
	}
	return plaintext, nil
}

// VerifyOTPTx checks the submitted code, increments the attempts
// counter, and either marks otp_verified_at OR poisons the token
// when the max-attempts cap is hit.
func (s *GuarantorConsentStore) VerifyOTPTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID, submitted string, maxAttempts int,
) error {
	var (
		hash    []byte
		expires *time.Time
		attempts int
	)
	if err := tx.QueryRow(ctx, `
		SELECT otp_code_hash, otp_expires_at, otp_attempts
		  FROM guarantor_consent_tokens
		 WHERE id = $1
	`, tokenID).Scan(&hash, &expires, &attempts); err != nil {
		return fmt.Errorf("verify otp: %w", err)
	}
	if hash == nil {
		return ErrConsentOTPNotIssued
	}
	if expires != nil && time.Now().UTC().After(*expires) {
		return ErrConsentOTPExpired
	}

	submittedHash := hashOTP(submitted)
	matches := constantTimeEqual(hash, submittedHash)

	if matches {
		_, err := tx.Exec(ctx, `
			UPDATE guarantor_consent_tokens
			   SET otp_verified_at = now()
			 WHERE id = $1
		`, tokenID)
		return err
	}
	// Wrong code — bump counter, possibly poison.
	attempts++
	if attempts >= maxAttempts {
		if _, err := tx.Exec(ctx, `
			UPDATE guarantor_consent_tokens
			   SET otp_attempts = $2,
			       used_at      = now(),
			       decision     = 'abandoned',
			       decision_reason = 'OTP attempts exceeded'
			 WHERE id = $1
		`, tokenID, attempts); err != nil {
			return err
		}
		return ErrConsentOTPExceeded
	}
	if _, err := tx.Exec(ctx, `
		UPDATE guarantor_consent_tokens SET otp_attempts = $2 WHERE id = $1
	`, tokenID, attempts); err != nil {
		return err
	}
	return ErrConsentOTPBadCode
}

// RecordDecisionTx marks the token used with a final decision.
// Returns the latest row state. Callers run a follow-up update on
// loan_guarantees.status separately (so the same RecordDecisionTx
// works for accept / decline / opt_offline).
func (s *GuarantorConsentStore) RecordDecisionTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID,
	decision string, reason *string, signaturePath *string,
	ip, userAgent *string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE guarantor_consent_tokens
		   SET used_at                 = now(),
		       decision                = $2,
		       decision_reason         = $3,
		       decision_signature_path = $4,
		       ip_address              = $5::inet,
		       user_agent              = $6
		 WHERE id = $1
		   AND used_at IS NULL
	`, tokenID, decision, reason, signaturePath, ip, userAgent)
	return err
}

// ─────────── Reminder worker support ───────────

// DueReminders lists tokens whose attempt #1 was sent N hours ago
// (and no reminder yet) OR whose attempt #2 was sent M hours ago
// (and no decision yet). Caller decides which list to send.
type ReminderTarget struct {
	TokenID    uuid.UUID
	TenantID   uuid.UUID
	NextAttempt int // 2 or 3
}

func (s *GuarantorConsentStore) FindDueRemindersTx(
	ctx context.Context, tx pgx.Tx,
	firstHours, secondHours int,
) ([]ReminderTarget, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, attempt_number
		  FROM guarantor_consent_tokens t
		 WHERE decision IS NULL AND used_at IS NULL
		   AND expires_at > now()
		   AND (
		     (attempt_number = 1 AND created_at <= now() - make_interval(hours => $1)
		         AND NOT EXISTS (
		           SELECT 1 FROM guarantor_consent_tokens t2
		            WHERE t2.guarantee_id = t.guarantee_id AND t2.attempt_number = 2
		         ))
		     OR
		     (attempt_number = 2 AND created_at <= now() - make_interval(hours => $2)
		         AND NOT EXISTS (
		           SELECT 1 FROM guarantor_consent_tokens t2
		            WHERE t2.guarantee_id = t.guarantee_id AND t2.attempt_number = 3
		         ))
		   )
	`, firstHours, secondHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReminderTarget
	for rows.Next() {
		var t ReminderTarget
		var prev int
		if err := rows.Scan(&t.TokenID, &t.TenantID, &prev); err != nil {
			return nil, err
		}
		t.NextAttempt = prev + 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateGuaranteeFromTokenTx flips the underlying loan_guarantees
// row's status from the public-page decision. Used by the public
// /respond endpoint after RecordDecisionTx runs.
//
// IMPORTANT: this path doesn't pass responded_by because the public
// flow has no JWT user. The audit trail lives on
// guarantor_consent_tokens (decision + ip_address + user_agent).
// Admin actions go through LoanGuaranteeStore.RespondTx which does
// require responded_by.
func (s *GuarantorConsentStore) UpdateGuaranteeFromTokenTx(
	ctx context.Context, tx pgx.Tx, guaranteeID uuid.UUID,
	decision string, declineReason *string,
) (string, error) {
	if decision != "accepted" && decision != "declined" {
		return "", fmt.Errorf("UpdateGuaranteeFromTokenTx: invalid decision %q", decision)
	}
	var newStatus string
	err := tx.QueryRow(ctx, `
		UPDATE loan_guarantees
		   SET status         = $2,
		       responded_at   = now(),
		       decline_reason = $3
		 WHERE id = $1
		   AND status = 'pending_consent'
		 RETURNING status::text
	`, guaranteeID, decision, declineReason).Scan(&newStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already responded by another path — fetch current status.
		_ = tx.QueryRow(ctx,
			`SELECT status::text FROM loan_guarantees WHERE id = $1`, guaranteeID,
		).Scan(&newStatus)
		return newStatus, nil
	}
	return newStatus, err
}

// constantTimeEqual avoids leaking byte-by-byte hash comparison
// timing — overkill for SHA-256 but cheap.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
