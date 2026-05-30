// Phase 1.5b — third-party pledger consent token store.
//
// Mirrors guarantor_consent_store.go: SHA-256 hash-stored single-use
// URL token, OTP on the same row, SECURITY DEFINER bridge for the
// public route's unscoped tenant discovery, and the same error
// sentinels so the public handler maps them with identical UX.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type PledgerConsentStore struct {
	pool *pgxpool.Pool
}

func NewPledgerConsentStore(pool *pgxpool.Pool) *PledgerConsentStore {
	return &PledgerConsentStore{pool: pool}
}

// Re-use the guarantor-side helpers for token + OTP value generation
// so the SMS body, ShortRef format, and OTP length stay consistent
// across both flows.
// NewToken / HashToken / NewOTP / ShortRef live in guarantor_consent_store.go.

// PledgerConsentToken — the on-disk row shape.
type PledgerConsentToken struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	CollateralID          uuid.UUID
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

// PledgerConsentTokenContext — token + pledger + collateral + loan
// surface for the public consent page.
type PledgerConsentTokenContext struct {
	Token                PledgerConsentToken
	PledgerName          string
	PledgerMemberNo      string
	PledgerIDDocNumber   string // National ID — never sent to the page; only used to verify
	PledgerPhone         string // OTP recipient; sent masked to the page
	ApplicantName        string
	ApplicationNo        string
	ProductName          string
	RequestedAmount      decimal.Decimal
	CollateralKind       string
	CollateralDescription string
	EstimatedValue       decimal.Decimal
	TenantName           string
	TenantSlug           string
}

// Re-use the guarantor consent error sentinels so the public handler's
// error mapping is identical. (No new aliases needed — callers import
// the same errors via the shared store package.)

// ─────────── SECURITY DEFINER bridge ───────────

func (s *PledgerConsentStore) FindTenantByHash(ctx context.Context, hash []byte) (tokenID, tenantID uuid.UUID, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT token_id, tenant_id FROM find_pledger_token_tenant($1)
	`, hash).Scan(&tokenID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrConsentTokenNotFound
	}
	return tokenID, tenantID, err
}

// ─────────── CRUD ───────────

func (s *PledgerConsentStore) CreateTx(
	ctx context.Context, tx pgx.Tx,
	collateralID uuid.UUID, hash []byte, expiresAt time.Time,
	attemptNumber int, createdBy uuid.UUID,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_pledger_consent_tokens (
		  tenant_id, collateral_id, token_hash, attempt_number,
		  expires_at, created_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		RETURNING id
	`, collateralID, hash, attemptNumber, expiresAt, createdBy).Scan(&id)
	return id, err
}

func (s *PledgerConsentStore) LoadContextTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID,
) (*PledgerConsentTokenContext, error) {
	var out PledgerConsentTokenContext
	var pledgerName, pledgerMemberNo string
	err := tx.QueryRow(ctx, `
		SELECT
		  t.id, t.tenant_id, t.collateral_id, t.attempt_number,
		  t.created_at, t.expires_at, t.used_at, t.decision,
		  t.decision_reason, t.decision_signature_path,
		  t.otp_sent_to, t.otp_expires_at, t.otp_attempts, t.otp_verified_at,

		  COALESCE(cd_p.full_name, ''), COALESCE(cd_p.member_no, ''),
		  COALESCE(m_p.id_doc_number, ''), COALESCE(m_p.phone, ''),
		  COALESCE(cd_a.full_name, ''), a.application_no, p.name,
		  a.requested_amount,
		  c.kind::text, c.description, c.estimated_value,
		  ten.name, ten.slug
		  FROM collateral_pledger_consent_tokens t
		  JOIN loan_collateral c ON c.id = t.collateral_id
		  JOIN loan_applications a ON a.id = c.application_id
		  JOIN loan_products p ON p.id = a.product_id
		  JOIN tenants ten ON ten.id = t.tenant_id
		  LEFT JOIN counterparty_directory cd_p ON cd_p.counterparty_id = c.pledger_counterparty_id
		  LEFT JOIN members m_p ON m_p.id = cd_p.member_id
		  LEFT JOIN counterparty_directory cd_a ON cd_a.counterparty_id = a.counterparty_id
		 WHERE t.id = $1
	`, tokenID).Scan(
		&out.Token.ID, &out.Token.TenantID, &out.Token.CollateralID, &out.Token.AttemptNumber,
		&out.Token.CreatedAt, &out.Token.ExpiresAt, &out.Token.UsedAt, &out.Token.Decision,
		&out.Token.DecisionReason, &out.Token.DecisionSignaturePath,
		&out.Token.OTPSentTo, &out.Token.OTPExpiresAt, &out.Token.OTPAttempts, &out.Token.OTPVerifiedAt,
		&pledgerName, &pledgerMemberNo,
		&out.PledgerIDDocNumber, &out.PledgerPhone,
		&out.ApplicantName, &out.ApplicationNo, &out.ProductName,
		&out.RequestedAmount,
		&out.CollateralKind, &out.CollateralDescription, &out.EstimatedValue,
		&out.TenantName, &out.TenantSlug,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConsentTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	out.PledgerName = pledgerName
	out.PledgerMemberNo = pledgerMemberNo

	if out.Token.UsedAt != nil {
		return &out, ErrConsentTokenUsed
	}
	if time.Now().UTC().After(out.Token.ExpiresAt) {
		return &out, ErrConsentTokenExpired
	}
	return &out, nil
}

func (s *PledgerConsentStore) IssueOTPTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID, phone string,
	validFor time.Duration,
) (string, error) {
	plaintext, hash, err := NewOTP()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_pledger_consent_tokens SET
		  otp_code_hash    = $2,
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

func (s *PledgerConsentStore) VerifyOTPTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID, submitted string, maxAttempts int,
) error {
	var (
		hash     []byte
		expires  *time.Time
		attempts int
	)
	if err := tx.QueryRow(ctx, `
		SELECT otp_code_hash, otp_expires_at, otp_attempts
		  FROM collateral_pledger_consent_tokens
		 WHERE id = $1
	`, tokenID).Scan(&hash, &expires, &attempts); err != nil {
		return fmt.Errorf("verify pledger otp: %w", err)
	}
	if hash == nil {
		return ErrConsentOTPNotIssued
	}
	if expires != nil && time.Now().UTC().After(*expires) {
		return ErrConsentOTPExpired
	}
	if constantTimeEqual(hash, hashOTP(submitted)) {
		_, err := tx.Exec(ctx, `
			UPDATE collateral_pledger_consent_tokens
			   SET otp_verified_at = now()
			 WHERE id = $1
		`, tokenID)
		return err
	}
	attempts++
	if attempts >= maxAttempts {
		if _, err := tx.Exec(ctx, `
			UPDATE collateral_pledger_consent_tokens SET
			  otp_attempts    = $2,
			  used_at         = now(),
			  decision        = 'abandoned',
			  decision_reason = 'OTP attempts exceeded'
			 WHERE id = $1
		`, tokenID, attempts); err != nil {
			return err
		}
		return ErrConsentOTPExceeded
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_pledger_consent_tokens SET otp_attempts = $2 WHERE id = $1
	`, tokenID, attempts); err != nil {
		return err
	}
	return ErrConsentOTPBadCode
}

// RecordDecisionTx stamps the terminal decision + audit fields.
func (s *PledgerConsentStore) RecordDecisionTx(
	ctx context.Context, tx pgx.Tx, tokenID uuid.UUID,
	decision string, reason *string, signaturePath *string,
	ip, userAgent *string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE collateral_pledger_consent_tokens SET
		  used_at                 = now(),
		  decision                = $2,
		  decision_reason         = $3,
		  decision_signature_path = $4,
		  ip_address              = $5::inet,
		  user_agent              = $6
		 WHERE id = $1 AND used_at IS NULL
	`, tokenID, decision, reason, signaturePath, ip, userAgent)
	return err
}

// UpdateCollateralFromTokenTx flips loan_collateral.pledger_consent_status
// to match the public-page decision. Returns the new status text.
func (s *PledgerConsentStore) UpdateCollateralFromTokenTx(
	ctx context.Context, tx pgx.Tx, collateralID uuid.UUID, decision string,
) error {
	if decision != "accepted" && decision != "declined" && decision != "opted_offline" {
		return fmt.Errorf("UpdateCollateralFromTokenTx: invalid decision %q", decision)
	}
	// 'opted_offline' leaves the door open — admin will record offline
	// consent later via the admin endpoint.
	var consentStatus string
	switch decision {
	case "accepted":
		consentStatus = "accepted"
	case "declined":
		consentStatus = "declined"
	case "opted_offline":
		consentStatus = "pending" // still pending until admin uploads
	}
	_, err := tx.Exec(ctx, `
		UPDATE loan_collateral SET
		  pledger_consent_status = $2,
		  pledger_consent_at     = CASE WHEN $2 = 'accepted' THEN now() ELSE pledger_consent_at END
		 WHERE id = $1
	`, collateralID, consentStatus)
	return err
}

// AdminRecordOfflineConsentTx — admin upload path. Stamps the doc path
// + flips status to offline_consented.
func (s *PledgerConsentStore) AdminRecordOfflineConsentTx(
	ctx context.Context, tx pgx.Tx, collateralID uuid.UUID, docPath string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE loan_collateral SET
		  pledger_consent_status   = 'offline_consented',
		  pledger_consent_at       = now(),
		  pledger_consent_doc_path = $2
		 WHERE id = $1
		   AND pledger_counterparty_id IS NOT NULL
	`, collateralID, docPath)
	return err
}
