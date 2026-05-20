// Package domain holds the business entities exposed across packages.
// Keep these dumb — no DB, no HTTP. They're just shapes.

package domain

import (
	"time"

	"github.com/google/uuid"
)

type TenantStatus string

const (
	TenantStatusActive       TenantStatus = "active"
	TenantStatusTrial        TenantStatus = "trial"
	TenantStatusSuspended    TenantStatus = "suspended"
	TenantStatusExpired      TenantStatus = "expired"
	TenantStatusPendingSetup TenantStatus = "pending_setup"
	TenantStatusArchived     TenantStatus = "archived"
)

// TenantRestrictions are independent of TenantStatus: an `active` tenant
// can still have any combination of these flipped on.
type TenantRestrictions struct {
	OperationsFrozen     bool `json:"operations_frozen"`
	UsersLocked          bool `json:"users_locked"`
	TransactionsDisabled bool `json:"transactions_disabled"`
}

type TenantKind string

const (
	TenantKindSACCO         TenantKind = "sacco"
	TenantKindMicrofinance  TenantKind = "microfinance"
	TenantKindDigitalLender TenantKind = "digital_lender"
	TenantKindCooperative   TenantKind = "cooperative"
	TenantKindChama         TenantKind = "chama"
)

type BillingPlan string

const (
	BillingStarter    BillingPlan = "starter"
	BillingStandard   BillingPlan = "standard"
	BillingPremium    BillingPlan = "premium"
	BillingEnterprise BillingPlan = "enterprise"
)

type BranchKind string

const (
	BranchHQ     BranchKind = "hq"
	BranchBranch BranchKind = "branch"
	BranchAgency BranchKind = "agency"
)

type Tenant struct {
	ID             uuid.UUID          `json:"id"`
	Slug           string             `json:"slug"`
	Name           string             `json:"name"`
	LegalName      string             `json:"legal_name,omitempty"`
	Kind           TenantKind         `json:"kind"`
	Status         TenantStatus       `json:"status"`
	CountryCode    string             `json:"country_code"`
	CurrencyCode   string             `json:"currency_code"`
	LicenseNo      string             `json:"license_no,omitempty"`
	RegistrationNo string             `json:"registration_no,omitempty"`
	TaxPIN         string             `json:"tax_pin,omitempty"`
	BillingPlan    BillingPlan        `json:"billing_plan"`
	Restrictions   TenantRestrictions `json:"restrictions"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type TenantBranch struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        uuid.UUID  `json:"tenant_id"`
	Code            string     `json:"code"`
	Name            string     `json:"name"`
	Kind            BranchKind `json:"kind"`
	County          string     `json:"county,omitempty"`
	SubCounty       string     `json:"sub_county,omitempty"`
	PhysicalAddress string     `json:"physical_address,omitempty"`
	Phone           string     `json:"phone,omitempty"`
	Position        int        `json:"position"`
}

type TenantContact struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	FullName  string    `json:"full_name"`
	Title     string    `json:"title,omitempty"`
	Email     string    `json:"email,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Position  int       `json:"position"`
}

// ─────────── Tenant settings ───────────
// Three 1:1 records per tenant. Each surfaces the latest configuration
// that drives the tenant's branding, locale, and operational defaults.

type TenantBranding struct {
	TenantID       uuid.UUID  `json:"tenant_id"`
	HasLogo        bool       `json:"has_logo"`
	LogoMIME       string     `json:"logo_mime,omitempty"`
	LogoSizeBytes  int64      `json:"logo_size_bytes,omitempty"`
	LogoUpdatedAt  *time.Time `json:"logo_updated_at,omitempty"`
	PrimaryColor   string     `json:"primary_color"`
	AccentColor    string     `json:"accent_color"`
	FontFamily     string     `json:"font_family"`
	EmailFromName  string     `json:"email_from_name,omitempty"`
	SMSSenderID    string     `json:"sms_sender_id,omitempty"`
	CustomDomain   string     `json:"custom_domain,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type TenantRegion struct {
	TenantID           uuid.UUID `json:"tenant_id"`
	Timezone           string    `json:"timezone"`
	Language           string    `json:"language"`
	DateFormat         string    `json:"date_format"`
	Regulator          string    `json:"regulator,omitempty"`
	Jurisdiction       string    `json:"jurisdiction,omitempty"`
	VATRate            float64   `json:"vat_rate"`
	WithholdingTaxRate float64   `json:"withholding_tax_rate"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type InterestMethod string

const (
	InterestFlat              InterestMethod = "flat"
	InterestReducingBalance   InterestMethod = "reducing_balance"
	InterestDecliningBalance  InterestMethod = "declining_balance"
)

type TenantOperations struct {
	TenantID uuid.UUID `json:"tenant_id"`

	LoanMinAmount         float64        `json:"loan_min_amount"`
	LoanMaxAmount         float64        `json:"loan_max_amount"`
	LoanMaxTermMonths     int            `json:"loan_max_term_months"`
	DefaultInterestMethod InterestMethod `json:"default_interest_method"`
	DefaultInterestRate   float64        `json:"default_interest_rate"`

	SavingsMinOpeningBal float64 `json:"savings_min_opening_bal"`
	SavingsMinRunningBal float64 `json:"savings_min_running_bal"`
	SavingsWithdrawalFee float64 `json:"savings_withdrawal_fee"`

	DividendRate      float64 `json:"dividend_rate"`
	DividendFrequency string  `json:"dividend_frequency"`

	PenaltyLateFeeRate     float64 `json:"penalty_late_fee_rate"`
	PenaltyGracePeriodDays int     `json:"penalty_grace_period_days"`

	GuarantorMinCount       int     `json:"guarantor_min_count"`
	GuarantorSelfMaxAmount  float64 `json:"guarantor_self_max_amount"`

	ApprovalBranchLimit float64 `json:"approval_branch_limit"`
	ApprovalCreditLimit float64 `json:"approval_credit_limit"`
	ApprovalBoardLimit  float64 `json:"approval_board_limit"`

	UpdatedAt time.Time `json:"updated_at"`
}

type UserStatus string

const (
	UserStatusPending   UserStatus = "pending"
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusLocked    UserStatus = "locked"
	UserStatusClosed    UserStatus = "closed"
)

type User struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	Email            string     `json:"email"`
	Phone            string     `json:"phone,omitempty"`
	FullName         string     `json:"full_name"`
	Status           UserStatus `json:"status"`
	IsPlatformAdmin  bool       `json:"is_platform_admin"`
	EmailVerifiedAt  *time.Time `json:"email_verified_at,omitempty"`
	MFAEnabled       bool       `json:"mfa_enabled"`
	MFAMethod        string     `json:"mfa_method,omitempty"`
	LastLoginAt      *time.Time `json:"last_login_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Permission struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

type Role struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    *uuid.UUID `json:"tenant_id,omitempty"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IsSystem    bool       `json:"is_system"`
	Permissions []string   `json:"permissions,omitempty"`
}

type RefreshToken struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	ParentID   *uuid.UUID
	UserAgent  string
	IP         string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}
