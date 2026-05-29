// Lending module domain types.
//
// Covers the full lifecycle (Phases 6a + 6b + 6c): product config →
// application → scoring → approval → offer → acceptance → disbursement.
// Repayment, arrears, restructuring add types later (separate file
// boundaries to keep this manageable).

package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Enums ───────────

type LoanCategory string

const (
	CatShortTerm    LoanCategory = "short_term"
	CatMediumTerm   LoanCategory = "medium_term"
	CatLongTerm     LoanCategory = "long_term"
	CatEmergency    LoanCategory = "emergency"
	CatAssetFinance LoanCategory = "asset_finance"
	CatGroup        LoanCategory = "group"
)

func (c LoanCategory) Valid() bool {
	switch c {
	case CatShortTerm, CatMediumTerm, CatLongTerm, CatEmergency, CatAssetFinance, CatGroup:
		return true
	}
	return false
}

type LoanInterestMethod string

const (
	InterestFlat      LoanInterestMethod = "flat_rate"
	InterestReducing  LoanInterestMethod = "reducing_balance"
)

func (m LoanInterestMethod) Valid() bool {
	return m == InterestFlat || m == InterestReducing
}

type LoanRepaymentMethod string

const (
	RepayReducingBalance LoanRepaymentMethod = "reducing_balance"
	RepayFlatRate        LoanRepaymentMethod = "flat_rate"
	RepayBullet          LoanRepaymentMethod = "bullet"
	RepayInterestOnly    LoanRepaymentMethod = "interest_only"
)

func (m LoanRepaymentMethod) Valid() bool {
	switch m {
	case RepayReducingBalance, RepayFlatRate, RepayBullet, RepayInterestOnly:
		return true
	}
	return false
}

type LoanFeeTiming string

const (
	FeeUpfront           LoanFeeTiming = "upfront"
	FeeAddedToLoan       LoanFeeTiming = "added_to_loan"
	FeeAtEachInstallment LoanFeeTiming = "at_each_installment"
)

type LoanCollateralRequirement string

const (
	CollateralRequired      LoanCollateralRequirement = "required"
	CollateralOptional      LoanCollateralRequirement = "optional"
	CollateralNotApplicable LoanCollateralRequirement = "not_applicable"
)

type LoanMultiplierBasis string

const (
	MultiplierNone   LoanMultiplierBasis = "none"
	MultiplierShares LoanMultiplierBasis = "shares"
	// SACCO-prudential bases, introduced with the BOSA/FOSA split.
	// "bosa" multiplies the member's non-withdrawable deposit bond
	// only; "bosa_plus_shares" adds share capital on top. These are
	// the values new loan products should be configured with.
	MultiplierBOSA           LoanMultiplierBasis = "bosa"
	MultiplierBOSAPlusShares LoanMultiplierBasis = "bosa_plus_shares"
	// Deprecated: the legacy "deposits" / "shares_plus_deposits"
	// values pre-date the BOSA/FOSA split — they sum *all* deposit
	// balances (BOSA + FOSA), which is SACCO-unsafe because
	// withdrawable FOSA shouldn't secure a loan. Kept for back-compat
	// only; the scorer silently re-routes them to the BOSA-only
	// equivalent and emits a soft warning when the BOSA_FOSA flag is
	// on. Edit affected products to MultiplierBOSAPlusShares.
	MultiplierDeposits       LoanMultiplierBasis = "deposits"
	MultiplierSharesPlusDeps LoanMultiplierBasis = "shares_plus_deposits"
)

// IsLegacyMultiplierBasis reports whether the value is one of the
// pre-BOSA/FOSA bases. The /loan-products UI uses this to surface a
// "rebase to BOSA + shares" warning on affected rows.
func (b LoanMultiplierBasis) IsLegacyMultiplierBasis() bool {
	return b == MultiplierDeposits || b == MultiplierSharesPlusDeps
}

type LoanAppStatus string

const (
	AppDraft                  LoanAppStatus = "draft"
	AppPendingValidation      LoanAppStatus = "pending_validation"
	AppPendingGuarantor       LoanAppStatus = "pending_guarantor"
	AppPendingScoring         LoanAppStatus = "pending_scoring"
	AppPendingApproval        LoanAppStatus = "pending_approval"
	AppApproved               LoanAppStatus = "approved"
	AppApprovedWithConditions LoanAppStatus = "approved_with_conditions"
	AppDeclined               LoanAppStatus = "declined"
	AppReturnedForInfo        LoanAppStatus = "returned_for_info"
	AppOfferSent              LoanAppStatus = "offer_sent"
	AppOfferAccepted          LoanAppStatus = "offer_accepted"
	AppOfferDeclined          LoanAppStatus = "offer_declined"
	AppExpired                LoanAppStatus = "expired"
	AppCancelled              LoanAppStatus = "cancelled"
	AppDisbursed              LoanAppStatus = "disbursed"
)

type LoanGuaranteeStatus string

const (
	GuarPending  LoanGuaranteeStatus = "pending_consent"
	GuarAccepted LoanGuaranteeStatus = "accepted"
	GuarDeclined LoanGuaranteeStatus = "declined"
	GuarReleased LoanGuaranteeStatus = "released"
	GuarCalled   LoanGuaranteeStatus = "called_upon"
)

type LoanCollateralKind string

const (
	ColTitleDeed         LoanCollateralKind = "title_deed"
	ColVehicleLogbook    LoanCollateralKind = "vehicle_logbook"
	ColEquipment         LoanCollateralKind = "equipment"
	ColListedShares      LoanCollateralKind = "listed_shares"
	ColFixedDepositLien  LoanCollateralKind = "fixed_deposit_lien"
	ColOther             LoanCollateralKind = "other"
)

type LoanDocKind string

const (
	DocPayslip            LoanDocKind = "payslip"
	DocBankStatement      LoanDocKind = "bank_statement"
	DocMpesaStatement     LoanDocKind = "mpesa_statement"
	DocBusinessFinancials LoanDocKind = "business_financials"
	DocIDCopy             LoanDocKind = "id_copy"
	DocOfferLetterSigned  LoanDocKind = "offer_letter_signed"
	DocAgreement          LoanDocKind = "agreement"
	DocOther              LoanDocKind = "other"
)

type LoanStatus string

const (
	LoanPendingDisbursement LoanStatus = "pending_disbursement"
	LoanActive              LoanStatus = "active"
	LoanInArrears           LoanStatus = "in_arrears"
	LoanDefaulted           LoanStatus = "defaulted"
	LoanRestructured        LoanStatus = "restructured"
	LoanSettled             LoanStatus = "settled"
	LoanWrittenOff          LoanStatus = "written_off"
	LoanClosed              LoanStatus = "closed"
)

type LoanTxnType string

const (
	LoanTxnDisbursement       LoanTxnType = "disbursement"
	LoanTxnFeeCharge          LoanTxnType = "fee_charge"
	LoanTxnInterestAccrual    LoanTxnType = "interest_accrual"
	LoanTxnPenaltyCharge      LoanTxnType = "penalty_charge"
	LoanTxnPenaltyWaiver      LoanTxnType = "penalty_waiver"
	LoanTxnRepayment          LoanTxnType = "repayment"
	LoanTxnWriteOff           LoanTxnType = "write_off"
	LoanTxnAdjustment         LoanTxnType = "adjustment"
	LoanTxnReversal           LoanTxnType = "reversal"
	LoanTxnSettlementDiscount LoanTxnType = "settlement_discount"
)

type LoanEmploymentType string

const (
	EmpSalaried     LoanEmploymentType = "salaried"
	EmpSelfEmployed LoanEmploymentType = "self_employed"
	EmpBusinessOwn  LoanEmploymentType = "business_owner"
	EmpRetired      LoanEmploymentType = "retired"
	EmpStudent      LoanEmploymentType = "student"
	EmpOther        LoanEmploymentType = "other"
)

// ─────────── Entities ───────────

// LoanProductFee is one line on a product's fee schedule. A product may
// have zero, one, or many of these. Tenant can name them anything (e.g.
// "Disbursement fee", "CRB filing fee", "Stamp duty") and toggle each
// between %-of-principal and flat-amount, with one of three timings:
//   upfront            — deducted from the disbursement
//   added_to_loan      — added to principal so it amortises with the loan
//   at_each_installment — charged on every installment row
type LoanProductFee struct {
	ID           uuid.UUID       `json:"id,omitempty"`
	ProductID    uuid.UUID       `json:"product_id,omitempty"`
	Name         string          `json:"name"`
	Amount       decimal.Decimal `json:"amount"`
	IsPct        bool            `json:"is_pct"`
	Timing       LoanFeeTiming   `json:"timing"`
	DisplayOrder int             `json:"display_order"`
	// GLCreditCode is the CoA code credited when this fee is recognised
	// as income at disbursement (default 4010 — Loan Processing Fee
	// Income). Insurance-style fees set 4020; ad-hoc set 4190.
	GLCreditCode string          `json:"gl_credit_code"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at,omitempty"`
}

type LoanProduct struct {
	ID                       uuid.UUID                 `json:"id"`
	TenantID                 uuid.UUID                 `json:"tenant_id"`
	Code                     string                    `json:"code"`
	Name                     string                    `json:"name"`
	Category                 LoanCategory              `json:"category"`
	Description              *string                   `json:"description,omitempty"`
	IsActive                 bool                      `json:"is_active"`
	MinAmount                decimal.Decimal           `json:"min_amount"`
	MaxAmount                decimal.Decimal           `json:"max_amount"`
	MultiplierBasis          LoanMultiplierBasis       `json:"multiplier_basis"`
	MultiplierValue          *decimal.Decimal          `json:"multiplier_value,omitempty"`
	MinTermMonths            int                       `json:"min_term_months"`
	MaxTermMonths            int                       `json:"max_term_months"`
	DefaultTermMonths        *int                      `json:"default_term_months,omitempty"`
	GracePeriodMonths        int                       `json:"grace_period_months"`
	InterestRatePct          decimal.Decimal           `json:"interest_rate_pct"`
	InterestMethod           LoanInterestMethod        `json:"interest_method"`
	RepaymentMethod          LoanRepaymentMethod       `json:"repayment_method"`
	// Fees: free-form per-product list. May be empty if the product
	// charges no fees. Loaded into the struct by the store; never
	// populated by direct SELECTs against loan_products alone.
	Fees                     []LoanProductFee          `json:"fees"`
	PenaltyRatePct           decimal.Decimal           `json:"penalty_rate_pct"`
	MinGuarantors            int                       `json:"min_guarantors"`
	MaxGuarantorExposurePct  decimal.Decimal           `json:"max_guarantor_exposure_pct"`
	GuarantorMustBeMember    bool                      `json:"guarantor_must_be_member"`
	CollateralRequirement    LoanCollateralRequirement `json:"collateral_requirement"`
	MinMembershipMonths      int                       `json:"min_membership_months"`
	MinSharesRequired        int                       `json:"min_shares_required"`
	AllowConcurrent          bool                      `json:"allow_concurrent"`
	WorkflowDefinitionCode   *string                   `json:"workflow_definition_code,omitempty"`
	AutoApprovalThreshold    *decimal.Decimal          `json:"auto_approval_threshold,omitempty"`
	AutoApprovalMinScore     *int                      `json:"auto_approval_min_score,omitempty"`
	AllowTopup               bool                      `json:"allow_topup"`
	AllowRefinance           bool                      `json:"allow_refinance"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
	CreatedBy                *uuid.UUID                `json:"created_by,omitempty"`
}

type LoanPurposeCategory struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

type LoanApplication struct {
	ID                          uuid.UUID            `json:"id"`
	TenantID                    uuid.UUID            `json:"tenant_id"`
	ApplicationNo               string               `json:"application_no"`
	CounterpartyID                    uuid.UUID            `json:"counterparty_id"`
	ProductID                   uuid.UUID            `json:"product_id"`
	Status                      LoanAppStatus        `json:"status"`
	RequestedAmount             decimal.Decimal      `json:"requested_amount"`
	RequestedTermMonths         int                  `json:"requested_term_months"`
	PurposeCategoryID           *uuid.UUID           `json:"purpose_category_id,omitempty"`
	PurposeNote                 *string              `json:"purpose_note,omitempty"`
	PreferredDisbursementChannel *string             `json:"preferred_disbursement_channel,omitempty"`
	EmploymentType              *LoanEmploymentType  `json:"employment_type,omitempty"`
	EmployerName                *string              `json:"employer_name,omitempty"`
	EmployerPayrollContact      *string              `json:"employer_payroll_contact,omitempty"`
	MonthlyNetIncome            decimal.Decimal      `json:"monthly_net_income"`
	OtherIncome                 decimal.Decimal      `json:"other_income"`
	MonthlyExpenses             decimal.Decimal      `json:"monthly_expenses"`
	MonthlyExistingObligations  decimal.Decimal      `json:"monthly_existing_obligations"`
	CreditScore                 *int                 `json:"credit_score,omitempty"`
	RiskBand                    *string              `json:"risk_band,omitempty"`
	AffordabilityPass           *bool                `json:"affordability_pass,omitempty"`
	DTIRatio                    *decimal.Decimal     `json:"dti_ratio,omitempty"`
	NetDisposableIncome         *decimal.Decimal     `json:"net_disposable_income,omitempty"`
	ComputedMaxAmount           *decimal.Decimal     `json:"computed_max_amount,omitempty"`
	ComputedMaxInstallment      *decimal.Decimal     `json:"computed_max_installment,omitempty"`
	RecommendedAmount           *decimal.Decimal     `json:"recommended_amount,omitempty"`
	RecommendedTermMonths       *int                 `json:"recommended_term_months,omitempty"`
	ScoringDetails              []byte               `json:"scoring_details,omitempty"`  // raw JSON
	ScoringFlags                []byte               `json:"scoring_flags,omitempty"`
	ScoredAt                    *time.Time           `json:"scored_at,omitempty"`
	WorkflowInstanceID          *uuid.UUID           `json:"workflow_instance_id,omitempty"`
	ApprovedAmount              *decimal.Decimal     `json:"approved_amount,omitempty"`
	ApprovedTermMonths          *int                 `json:"approved_term_months,omitempty"`
	ApprovedInterestRatePct     *decimal.Decimal     `json:"approved_interest_rate_pct,omitempty"`
	ApprovedAt                  *time.Time           `json:"approved_at,omitempty"`
	ApprovedBy                  *uuid.UUID           `json:"approved_by,omitempty"`
	ApprovalConditions          *string              `json:"approval_conditions,omitempty"`
	DeclineCategory             *string              `json:"decline_category,omitempty"`
	DeclineReason               *string              `json:"decline_reason,omitempty"`
	OfferLetterPath             *string              `json:"offer_letter_path,omitempty"`
	OfferSentAt                 *time.Time           `json:"offer_sent_at,omitempty"`
	OfferExpiresAt              *time.Time           `json:"offer_expires_at,omitempty"`
	OfferAcceptedAt             *time.Time           `json:"offer_accepted_at,omitempty"`
	Notes                       *string              `json:"notes,omitempty"`
	CreatedAt                   time.Time            `json:"created_at"`
	UpdatedAt                   time.Time            `json:"updated_at"`
	CreatedBy                   uuid.UUID            `json:"created_by"`

	// Phase 5 fields. All nullable for backwards compat with apps
	// predating the migration.
	ApplicationType         string          `json:"application_type"`               // new | topup | refinance
	ParentLoanID            *uuid.UUID      `json:"parent_loan_id,omitempty"`
	RefinanceSourceLoanIDs  []byte          `json:"refinance_source_loan_ids,omitempty"` // jsonb [uuid...]
	ApplicantKind           string          `json:"applicant_kind"`                 // individual | group
	BorrowerCounterpartyID  *uuid.UUID      `json:"borrower_counterparty_id,omitempty"`
	GroupIncomeSource       *string         `json:"group_income_source,omitempty"`
	IsInsider               bool            `json:"is_insider"`
	InsiderCategory         *string         `json:"insider_category,omitempty"`
}

type LoanGuarantee struct {
	ID                uuid.UUID           `json:"id"`
	TenantID          uuid.UUID           `json:"tenant_id"`
	ApplicationID     uuid.UUID           `json:"application_id"`
	LoanID            *uuid.UUID          `json:"loan_id,omitempty"`
	GuarantorMemberID uuid.UUID           `json:"guarantor_member_id"`
	AmountGuaranteed  decimal.Decimal     `json:"amount_guaranteed"`
	Status            LoanGuaranteeStatus `json:"status"`
	RequestedAt       time.Time           `json:"requested_at"`
	RequestedBy       uuid.UUID           `json:"requested_by"`
	RespondedAt       *time.Time          `json:"responded_at,omitempty"`
	ReleasedAt        *time.Time          `json:"released_at,omitempty"`
	CalledUponAt      *time.Time          `json:"called_upon_at,omitempty"`
	DeclineReason     *string             `json:"decline_reason,omitempty"`
	Notes             *string             `json:"notes,omitempty"`

	// Display-only fields populated by ByApplicationTx via a join on
	// counterparty_directory. Empty when the row is loaded by a code
	// path that doesn't join (e.g. CreateTx returns the raw row).
	// Nullable so older callers that don't populate them still
	// marshal cleanly.
	GuarantorName     string `json:"guarantor_name,omitempty"`
	GuarantorMemberNo string `json:"guarantor_member_no,omitempty"`
}

type LoanCollateralItem struct {
	ID                uuid.UUID          `json:"id"`
	TenantID          uuid.UUID          `json:"tenant_id"`
	ApplicationID     uuid.UUID          `json:"application_id"`
	LoanID            *uuid.UUID         `json:"loan_id,omitempty"`
	Kind              LoanCollateralKind `json:"kind"`
	Description       string             `json:"description"`
	EstimatedValue    decimal.Decimal    `json:"estimated_value"`
	ForcedSaleValue   *decimal.Decimal   `json:"forced_sale_value,omitempty"`
	ValuationDate     *time.Time         `json:"valuation_date,omitempty"`
	ValuationPath     *string            `json:"valuation_path,omitempty"`
	OwnershipPath     *string            `json:"ownership_path,omitempty"`
	Status            string             `json:"status"`
	Notes             *string            `json:"notes,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
}

type LoanDocument struct {
	ID            uuid.UUID    `json:"id"`
	TenantID      uuid.UUID    `json:"tenant_id"`
	ApplicationID *uuid.UUID   `json:"application_id,omitempty"`
	LoanID        *uuid.UUID   `json:"loan_id,omitempty"`
	Kind          LoanDocKind  `json:"kind"`
	Description   *string      `json:"description,omitempty"`
	StoragePath   string       `json:"storage_path"`
	Mime          string       `json:"mime"`
	SizeBytes     int64        `json:"size_bytes"`
	UploadedAt    time.Time    `json:"uploaded_at"`
	UploadedBy    *uuid.UUID   `json:"uploaded_by,omitempty"`
}

type Loan struct {
	ID                          uuid.UUID            `json:"id"`
	TenantID                    uuid.UUID            `json:"tenant_id"`
	LoanNo                      string               `json:"loan_no"`
	ApplicationID               uuid.UUID            `json:"application_id"`
	CounterpartyID                    uuid.UUID            `json:"counterparty_id"`
	ProductID                   uuid.UUID            `json:"product_id"`
	Status                      LoanStatus           `json:"status"`
	Principal                   decimal.Decimal      `json:"principal"`
	InterestRatePct             decimal.Decimal      `json:"interest_rate_pct"`
	InterestMethod              LoanInterestMethod   `json:"interest_method"`
	RepaymentMethod             LoanRepaymentMethod  `json:"repayment_method"`
	TermMonths                  int                  `json:"term_months"`
	GracePeriodMonths           int                  `json:"grace_period_months"`
	InstallmentCount            int                  `json:"installment_count"`
	FirstDueDate                *time.Time           `json:"first_due_date,omitempty"`
	DisbursementChannel         *string              `json:"disbursement_channel,omitempty"`
	DisbursementTargetAccountID *uuid.UUID           `json:"disbursement_target_account_id,omitempty"`
	DisbursementRef             *string              `json:"disbursement_ref,omitempty"`
	TotalFeesDeducted           decimal.Decimal      `json:"total_fees_deducted"`
	NetDisbursed                *decimal.Decimal     `json:"net_disbursed,omitempty"`
	DisbursedAt                 *time.Time           `json:"disbursed_at,omitempty"`
	DisbursedBy                 *uuid.UUID           `json:"disbursed_by,omitempty"`
	PrincipalDisbursed          decimal.Decimal      `json:"principal_disbursed"`
	PrincipalRepaid             decimal.Decimal      `json:"principal_repaid"`
	PrincipalBalance            decimal.Decimal      `json:"principal_balance"`
	InterestCharged             decimal.Decimal      `json:"interest_charged"`
	InterestPaid                decimal.Decimal      `json:"interest_paid"`
	InterestBalance             decimal.Decimal      `json:"interest_balance"`
	FeesCharged                 decimal.Decimal      `json:"fees_charged"`
	FeesPaid                    decimal.Decimal      `json:"fees_paid"`
	FeesBalance                 decimal.Decimal      `json:"fees_balance"`
	PenaltyAccrued              decimal.Decimal      `json:"penalty_accrued"`
	PenaltyPaid                 decimal.Decimal      `json:"penalty_paid"`
	PenaltyBalance              decimal.Decimal      `json:"penalty_balance"`
	InstallmentsPaid            int                  `json:"installments_paid"`
	NextInstallmentDueAt        *time.Time           `json:"next_installment_due_at,omitempty"`
	NextInstallmentAmount       *decimal.Decimal     `json:"next_installment_amount,omitempty"`
	DaysPastDue                 int                  `json:"days_past_due"`
	ArrearsClassification       string               `json:"arrears_classification"`
	LastRepaymentAt             *time.Time           `json:"last_repayment_at,omitempty"`
	LastArrearsCalcAt           *time.Time           `json:"last_arrears_calc_at,omitempty"`
	CreatedAt                   time.Time            `json:"created_at"`
	UpdatedAt                   time.Time            `json:"updated_at"`
	SettledAt                   *time.Time           `json:"settled_at,omitempty"`
	WrittenOffAt                *time.Time           `json:"written_off_at,omitempty"`
	ClosedAt                    *time.Time           `json:"closed_at,omitempty"`
}

type LoanInstallment struct {
	ID                    uuid.UUID       `json:"id"`
	TenantID              uuid.UUID       `json:"tenant_id"`
	LoanID                uuid.UUID       `json:"loan_id"`
	InstallmentNo         int             `json:"installment_no"`
	DueDate               time.Time       `json:"due_date"`
	PrincipalDue          decimal.Decimal `json:"principal_due"`
	InterestDue           decimal.Decimal `json:"interest_due"`
	FeeDue                decimal.Decimal `json:"fee_due"`
	TotalDue              decimal.Decimal `json:"total_due"`
	PrincipalPaid         decimal.Decimal `json:"principal_paid"`
	InterestPaid          decimal.Decimal `json:"interest_paid"`
	FeePaid               decimal.Decimal `json:"fee_paid"`
	Status                string          `json:"status"`
	PaidAt                *time.Time      `json:"paid_at,omitempty"`
	OutstandingAfter      decimal.Decimal `json:"outstanding_after"`
	AccruedAt             *time.Time      `json:"accrued_at,omitempty"`
	AccruedInterestTxnID  *uuid.UUID      `json:"accrued_interest_txn_id,omitempty"`
}

type LoanTransaction struct {
	ID                  uuid.UUID       `json:"id"`
	TenantID            uuid.UUID       `json:"tenant_id"`
	LoanID              uuid.UUID       `json:"loan_id"`
	CounterpartyID            uuid.UUID       `json:"counterparty_id"`
	TxnNo               string          `json:"txn_no"`
	TxnType             LoanTxnType     `json:"txn_type"`
	Amount              decimal.Decimal `json:"amount"`
	PrincipalComponent  decimal.Decimal `json:"principal_component"`
	InterestComponent   decimal.Decimal `json:"interest_component"`
	FeeComponent        decimal.Decimal `json:"fee_component"`
	PenaltyComponent    decimal.Decimal `json:"penalty_component"`
	ValueDate           time.Time       `json:"value_date"`
	Channel             *string         `json:"channel,omitempty"`
	ChannelRef          *string         `json:"channel_ref,omitempty"`
	Narration           *string         `json:"narration,omitempty"`
	ReversesTxnID       *uuid.UUID      `json:"reverses_txn_id,omitempty"`
	ReversedByTxnID     *uuid.UUID      `json:"reversed_by_txn_id,omitempty"`
	InstallmentNo       *int            `json:"installment_no,omitempty"`
	PostedAt            time.Time       `json:"posted_at"`
	InitiatedBy         uuid.UUID       `json:"initiated_by"`
	AuthorizedBy        *uuid.UUID      `json:"authorized_by,omitempty"`
}

// ─────────── Errors ───────────

var (
	ErrLoanProductInactive    = errors.New("loan product is not active")
	ErrLoanAmountOutsideRange = errors.New("requested loan amount is outside the product's min/max range")
	ErrLoanTermOutsideRange   = errors.New("requested term is outside the product's min/max range")
	ErrMemberIneligibleForLoan = errors.New("member is not eligible under the loan product's rules")
	ErrInsufficientSharesForLoan = errors.New("member does not meet the minimum share-holding requirement for this loan product")
	ErrMembershipTooShort     = errors.New("member does not meet the minimum membership duration")
	ErrConcurrentLoanForbidden = errors.New("member already has an active loan of this product type and the product forbids concurrent loans")
	ErrMultiplierExceeded     = errors.New("loan amount exceeds the configured multiplier ceiling on member's shares/deposits")
	ErrInsufficientGuarantors = errors.New("application has fewer guarantors than the product requires")
	ErrGuarantorsNotConsented = errors.New("not all guarantors have accepted the guarantee request")
	ErrCollateralMissing      = errors.New("product requires collateral but none was attached")
	ErrAffordabilityFailed    = errors.New("proposed installment exceeds the affordability ceiling")
	ErrAppNotApprovable       = errors.New("application is not in a state that allows approval")
	ErrAppNotOfferable        = errors.New("application is not in a state that allows offer generation")
	ErrAppNotAcceptable       = errors.New("application is not in a state that allows offer acceptance")
	ErrAppNotDisbursable      = errors.New("application is not in 'offer_accepted' state")
	ErrLoanNotRepayable       = errors.New("loan is not in a state that permits repayments")
)

// ─────────── Helpers ───────────

func NormalizeLoanCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ApplyFee resolves a configured fee against the principal. When
// isPct=true the value is treated as a percentage; otherwise a flat
// amount. Returns the resulting fee amount, rounded to 2dp.
func ApplyFee(principal, value decimal.Decimal, isPct bool) decimal.Decimal {
	if !isPct {
		return value.Round(2)
	}
	return principal.Mul(value).Div(decimal.NewFromInt(100)).Round(2)
}
