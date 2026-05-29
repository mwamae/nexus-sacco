// Tenant settings store: branding, region, operations.
//
// Each of the three sub-tables is 1:1 with tenants. We use upsert
// patterns (INSERT … ON CONFLICT) so callers can always "save" without
// caring whether the row exists yet. All access goes through
// WithTenantTx so RLS applies.

package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type SettingsStore struct {
	pool *pgxpool.Pool
}

func NewSettingsStore(pool *pgxpool.Pool) *SettingsStore {
	return &SettingsStore{pool: pool}
}

// ─────────── Branding ───────────

func (s *SettingsStore) GetOrInitBrandingTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.TenantBranding, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_branding (tenant_id)
		VALUES ($1) ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return nil, err
	}
	var b domain.TenantBranding
	var path *string
	err := tx.QueryRow(ctx, `
		SELECT tenant_id,
		       logo_storage_path,
		       COALESCE(logo_mime,''),
		       COALESCE(logo_size_bytes, 0),
		       logo_updated_at,
		       primary_color, accent_color, font_family,
		       COALESCE(email_from_name,''),
		       COALESCE(sms_sender_id,''),
		       COALESCE(custom_domain,''),
		       updated_at
		FROM tenant_branding WHERE tenant_id = $1
	`, tenantID).Scan(
		&b.TenantID,
		&path,
		&b.LogoMIME,
		&b.LogoSizeBytes,
		&b.LogoUpdatedAt,
		&b.PrimaryColor, &b.AccentColor, &b.FontFamily,
		&b.EmailFromName, &b.SMSSenderID, &b.CustomDomain,
		&b.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	b.HasLogo = path != nil && *path != ""
	return &b, nil
}

type BrandingPatch struct {
	PrimaryColor  *string
	AccentColor   *string
	FontFamily    *string
	EmailFromName *string
	SMSSenderID   *string
	CustomDomain  *string
}

func (s *SettingsStore) UpdateBrandingTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p BrandingPatch) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_branding (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE tenant_branding
		SET primary_color    = COALESCE($2, primary_color),
		    accent_color     = COALESCE($3, accent_color),
		    font_family      = COALESCE($4, font_family),
		    email_from_name  = CASE WHEN $5::text IS NULL THEN email_from_name ELSE NULLIF($5,'') END,
		    sms_sender_id    = CASE WHEN $6::text IS NULL THEN sms_sender_id   ELSE NULLIF($6,'') END,
		    custom_domain    = CASE WHEN $7::text IS NULL THEN custom_domain   ELSE NULLIF($7,'') END
		WHERE tenant_id = $1
	`, tenantID,
		p.PrimaryColor, p.AccentColor, p.FontFamily,
		p.EmailFromName, p.SMSSenderID, p.CustomDomain,
	)
	return err
}

type LogoMeta struct {
	StoragePath string
	MIME        string
	SizeBytes   int64
}

func (s *SettingsStore) SetLogoTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, m LogoMeta) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_branding (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE tenant_branding
		SET logo_storage_path = $2,
		    logo_mime         = $3,
		    logo_size_bytes   = $4,
		    logo_updated_at   = now()
		WHERE tenant_id = $1
	`, tenantID, m.StoragePath, m.MIME, m.SizeBytes)
	return err
}

// LogoPathTx returns the storage path (or "") and mime for serving the
// current logo bytes.
func (s *SettingsStore) LogoPathTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (path, mime string, size int64, err error) {
	var p, m *string
	var sz *int64
	err = tx.QueryRow(ctx, `
		SELECT logo_storage_path, logo_mime, logo_size_bytes
		FROM tenant_branding WHERE tenant_id = $1
	`, tenantID).Scan(&p, &m, &sz)
	if err == pgx.ErrNoRows {
		return "", "", 0, nil
	}
	if err != nil {
		return "", "", 0, err
	}
	if p == nil {
		return "", "", 0, nil
	}
	out := ""
	if m != nil {
		out = *m
	}
	var s64 int64
	if sz != nil {
		s64 = *sz
	}
	return *p, out, s64, nil
}

func (s *SettingsStore) ClearLogoTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE tenant_branding
		SET logo_storage_path = NULL,
		    logo_mime         = NULL,
		    logo_size_bytes   = NULL,
		    logo_updated_at   = NULL
		WHERE tenant_id = $1
	`, tenantID)
	return err
}

// ─────────── Region ───────────

func (s *SettingsStore) GetOrInitRegionTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.TenantRegion, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_region (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return nil, err
	}
	var r domain.TenantRegion
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, timezone, language, date_format,
		       COALESCE(regulator,''), COALESCE(jurisdiction,''),
		       vat_rate, withholding_tax_rate, updated_at
		FROM tenant_region WHERE tenant_id = $1
	`, tenantID).Scan(
		&r.TenantID, &r.Timezone, &r.Language, &r.DateFormat,
		&r.Regulator, &r.Jurisdiction,
		&r.VATRate, &r.WithholdingTaxRate, &r.UpdatedAt,
	)
	return &r, err
}

type RegionPatch struct {
	Timezone           *string
	Language           *string
	DateFormat         *string
	Regulator          *string
	Jurisdiction       *string
	VATRate            *float64
	WithholdingTaxRate *float64
}

func (s *SettingsStore) UpdateRegionTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p RegionPatch) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_region (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE tenant_region
		SET timezone             = COALESCE($2, timezone),
		    language             = COALESCE($3, language),
		    date_format          = COALESCE($4, date_format),
		    regulator            = CASE WHEN $5::text IS NULL THEN regulator    ELSE NULLIF($5,'') END,
		    jurisdiction         = CASE WHEN $6::text IS NULL THEN jurisdiction ELSE NULLIF($6,'') END,
		    vat_rate             = COALESCE($7, vat_rate),
		    withholding_tax_rate = COALESCE($8, withholding_tax_rate)
		WHERE tenant_id = $1
	`, tenantID, p.Timezone, p.Language, p.DateFormat, p.Regulator, p.Jurisdiction, p.VATRate, p.WithholdingTaxRate)
	return err
}

// ─────────── Operations ───────────

func (s *SettingsStore) GetOrInitOperationsTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.TenantOperations, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_operations (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return nil, err
	}
	var o domain.TenantOperations
	err := tx.QueryRow(ctx, `
		SELECT tenant_id,
		       loan_min_amount, loan_max_amount, loan_max_term_months,
		       default_interest_method, default_interest_rate,
		       savings_min_opening_bal, savings_min_running_bal, savings_withdrawal_fee,
		       dividend_rate, dividend_frequency,
		       penalty_late_fee_rate, penalty_grace_period_days,
		       guarantor_min_count, guarantor_self_max_amount,
		       approval_branch_limit, approval_credit_limit, approval_board_limit,
		       COALESCE(default_security_model, 'guarantor_only'),
		       COALESCE(default_min_guarantor_cover_pct, 100),
		       COALESCE(default_min_collateral_cover_pct, 125),
		       COALESCE(collateral_revaluation_months, 24),
		       updated_at
		FROM tenant_operations WHERE tenant_id = $1
	`, tenantID).Scan(
		&o.TenantID,
		&o.LoanMinAmount, &o.LoanMaxAmount, &o.LoanMaxTermMonths,
		&o.DefaultInterestMethod, &o.DefaultInterestRate,
		&o.SavingsMinOpeningBal, &o.SavingsMinRunningBal, &o.SavingsWithdrawalFee,
		&o.DividendRate, &o.DividendFrequency,
		&o.PenaltyLateFeeRate, &o.PenaltyGracePeriodDays,
		&o.GuarantorMinCount, &o.GuarantorSelfMaxAmount,
		&o.ApprovalBranchLimit, &o.ApprovalCreditLimit, &o.ApprovalBoardLimit,
		&o.DefaultSecurityModel, &o.DefaultMinGuarantorCoverPct,
		&o.DefaultMinCollateralCoverPct, &o.CollateralRevaluationMonths,
		&o.UpdatedAt,
	)
	return &o, err
}

type OperationsPatch struct {
	LoanMinAmount         *float64
	LoanMaxAmount         *float64
	LoanMaxTermMonths     *int
	DefaultInterestMethod *string
	DefaultInterestRate   *float64

	SavingsMinOpeningBal *float64
	SavingsMinRunningBal *float64
	SavingsWithdrawalFee *float64

	DividendRate      *float64
	DividendFrequency *string

	PenaltyLateFeeRate     *float64
	PenaltyGracePeriodDays *int

	GuarantorMinCount      *int
	GuarantorSelfMaxAmount *float64

	ApprovalBranchLimit *float64
	ApprovalCreditLimit *float64
	ApprovalBoardLimit  *float64

	// Phase 1.5a — collateral defaults editor.
	DefaultSecurityModel         *string
	DefaultMinGuarantorCoverPct  *float64
	DefaultMinCollateralCoverPct *float64
	CollateralRevaluationMonths  *int
}

func (s *SettingsStore) UpdateOperationsTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p OperationsPatch) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_operations (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE tenant_operations SET
		  loan_min_amount             = COALESCE($2, loan_min_amount),
		  loan_max_amount             = COALESCE($3, loan_max_amount),
		  loan_max_term_months        = COALESCE($4, loan_max_term_months),
		  default_interest_method     = COALESCE($5::interest_method, default_interest_method),
		  default_interest_rate       = COALESCE($6, default_interest_rate),

		  savings_min_opening_bal     = COALESCE($7, savings_min_opening_bal),
		  savings_min_running_bal     = COALESCE($8, savings_min_running_bal),
		  savings_withdrawal_fee      = COALESCE($9, savings_withdrawal_fee),

		  dividend_rate               = COALESCE($10, dividend_rate),
		  dividend_frequency          = COALESCE($11, dividend_frequency),

		  penalty_late_fee_rate       = COALESCE($12, penalty_late_fee_rate),
		  penalty_grace_period_days   = COALESCE($13, penalty_grace_period_days),

		  guarantor_min_count         = COALESCE($14, guarantor_min_count),
		  guarantor_self_max_amount   = COALESCE($15, guarantor_self_max_amount),

		  approval_branch_limit       = COALESCE($16, approval_branch_limit),
		  approval_credit_limit       = COALESCE($17, approval_credit_limit),
		  approval_board_limit        = COALESCE($18, approval_board_limit),

		  default_security_model           = COALESCE($19, default_security_model),
		  default_min_guarantor_cover_pct  = COALESCE($20, default_min_guarantor_cover_pct),
		  default_min_collateral_cover_pct = COALESCE($21, default_min_collateral_cover_pct),
		  collateral_revaluation_months    = COALESCE($22, collateral_revaluation_months)
		WHERE tenant_id = $1
	`, tenantID,
		p.LoanMinAmount, p.LoanMaxAmount, p.LoanMaxTermMonths,
		p.DefaultInterestMethod, p.DefaultInterestRate,

		p.SavingsMinOpeningBal, p.SavingsMinRunningBal, p.SavingsWithdrawalFee,

		p.DividendRate, p.DividendFrequency,

		p.PenaltyLateFeeRate, p.PenaltyGracePeriodDays,

		p.GuarantorMinCount, p.GuarantorSelfMaxAmount,

		p.ApprovalBranchLimit, p.ApprovalCreditLimit, p.ApprovalBoardLimit,

		p.DefaultSecurityModel, p.DefaultMinGuarantorCoverPct,
		p.DefaultMinCollateralCoverPct, p.CollateralRevaluationMonths,
	)
	return err
}

// ─────────── Membership ───────────

func (s *SettingsStore) GetOrInitMembershipTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.TenantMembership, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_membership (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return nil, err
	}
	var m domain.TenantMembership
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, collect_registration_fee,
		       registration_fee_individual, registration_fee_institutional,
		       accepted_payment_channels, fee_refundable_on_rejection,
		       default_deposit_product_id, updated_at
		  FROM tenant_membership WHERE tenant_id = $1
	`, tenantID).Scan(
		&m.TenantID, &m.CollectRegistrationFee,
		&m.RegistrationFeeIndividual, &m.RegistrationFeeInstitutional,
		&m.AcceptedPaymentChannels, &m.FeeRefundableOnRejection,
		&m.DefaultDepositProductID, &m.UpdatedAt,
	)
	return &m, err
}

type MembershipPatch struct {
	CollectRegistrationFee       *bool
	RegistrationFeeIndividual    *float64
	RegistrationFeeInstitutional *float64
	AcceptedPaymentChannels      *[]string
	FeeRefundableOnRejection     *bool
	DefaultDepositProductID      *uuid.UUID
}

func (s *SettingsStore) UpdateMembershipTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p MembershipPatch) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_membership (tenant_id) VALUES ($1)
		ON CONFLICT DO NOTHING
	`, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE tenant_membership SET
		  collect_registration_fee        = COALESCE($2, collect_registration_fee),
		  registration_fee_individual     = COALESCE($3, registration_fee_individual),
		  registration_fee_institutional  = COALESCE($4, registration_fee_institutional),
		  accepted_payment_channels       = COALESCE($5::text[], accepted_payment_channels),
		  fee_refundable_on_rejection     = COALESCE($6, fee_refundable_on_rejection),
		  default_deposit_product_id      = COALESCE($7, default_deposit_product_id)
		 WHERE tenant_id = $1
	`, tenantID,
		p.CollectRegistrationFee,
		p.RegistrationFeeIndividual, p.RegistrationFeeInstitutional,
		p.AcceptedPaymentChannels, p.FeeRefundableOnRejection,
		p.DefaultDepositProductID,
	)
	return err
}
