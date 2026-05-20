// Domain entities surfaced across packages. Keep these dumb — no DB, no HTTP.

package domain

import (
	"time"

	"github.com/google/uuid"
)

type MemberStatus string

const (
	StatusPending     MemberStatus = "pending"
	StatusActive      MemberStatus = "active"
	StatusDormant     MemberStatus = "dormant"
	StatusSuspended   MemberStatus = "suspended"
	StatusBlacklisted MemberStatus = "blacklisted"
	StatusExited      MemberStatus = "exited"
	StatusDeceased    MemberStatus = "deceased"
	StatusRejected    MemberStatus = "rejected"

	// Deprecated aliases kept so old call sites keep compiling — they
	// point at the new states they collapsed into.
	StatusClosed = StatusExited
)

type StatusReason string

const (
	ReasonOnboardingApproval   StatusReason = "onboarding_approval"
	ReasonOnboardingRejection  StatusReason = "onboarding_rejection"
	ReasonDormancyInactivity   StatusReason = "dormancy_inactivity"
	ReasonReactivationRequest  StatusReason = "reactivation_request"
	ReasonLoanDefault          StatusReason = "loan_default"
	ReasonComplianceHold       StatusReason = "compliance_hold"
	ReasonDisciplinaryAction   StatusReason = "disciplinary_action"
	ReasonFraudInvestigation   StatusReason = "fraud_investigation"
	ReasonRegulatoryDirective  StatusReason = "regulatory_directive"
	ReasonMemberRequest        StatusReason = "member_request"
	ReasonAdminAction          StatusReason = "admin_action"
	ReasonDeceasedNotification StatusReason = "deceased_notification"
	ReasonSystemCorrection     StatusReason = "system_correction"
	ReasonOther                StatusReason = "other"
)

type MemberStatusChange struct {
	ID                 uuid.UUID    `json:"id"`
	MemberID           uuid.UUID    `json:"member_id"`
	FromStatus         MemberStatus `json:"from_status,omitempty"`
	ToStatus           MemberStatus `json:"to_status"`
	ReasonCategory     StatusReason `json:"reason_category"`
	ReasonNote         string       `json:"reason_note,omitempty"`
	SupportingDocPath  string       `json:"-"`
	SupportingDocMIME  string       `json:"supporting_doc_mime,omitempty"`
	HasSupportingDoc   bool         `json:"has_supporting_doc"`
	ChangedBy          *uuid.UUID   `json:"changed_by,omitempty"`
	ChangedAt          time.Time    `json:"changed_at"`
	WorkflowInstanceID *uuid.UUID   `json:"workflow_instance_id,omitempty"`
	ReviewDate         *time.Time   `json:"review_date,omitempty"`
}

type MemberStatusProposal struct {
	ID                 uuid.UUID    `json:"id"`
	MemberID           uuid.UUID    `json:"member_id"`
	WorkflowInstanceID uuid.UUID    `json:"workflow_instance_id"`
	ProposedStatus     MemberStatus `json:"proposed_status"`
	ReasonCategory     StatusReason `json:"reason_category"`
	ReasonNote         string       `json:"reason_note,omitempty"`
	HasSupportingDoc   bool         `json:"has_supporting_doc"`
	ReviewDate         *time.Time   `json:"review_date,omitempty"`
	ProposedBy         *uuid.UUID   `json:"proposed_by,omitempty"`
	ProposedAt         time.Time    `json:"proposed_at"`
	ResolvedAt         *time.Time   `json:"resolved_at,omitempty"`
	Resolution         string       `json:"resolution,omitempty"`
}

type IDDocKind string

const (
	IDNationalID IDDocKind = "national_id"
	IDPassport   IDDocKind = "passport"
	IDAlienID    IDDocKind = "alien_id"
)

type Gender string

const (
	GenderMale         Gender = "male"
	GenderFemale       Gender = "female"
	GenderOther        Gender = "other"
	GenderUndisclosed  Gender = "undisclosed"
)

type RelationKind string

const (
	RelNextOfKin    RelationKind = "next_of_kin"
	RelBeneficiary  RelationKind = "beneficiary"
)

type DocumentKind string

const (
	DocSignature     DocumentKind = "signature"
	DocPassportPhoto DocumentKind = "passport_photo"
	DocIDFront       DocumentKind = "id_front"
	DocIDBack        DocumentKind = "id_back"
)

type Member struct {
	ID       uuid.UUID    `json:"id"`
	TenantID uuid.UUID    `json:"tenant_id"`
	MemberNo string       `json:"member_no"`
	Status   MemberStatus `json:"status"`

	FullName    string    `json:"full_name"`
	IDDocKind   IDDocKind `json:"id_doc_kind"`
	IDDocNumber string    `json:"id_doc_number"`
	KraPIN      string    `json:"kra_pin,omitempty"`
	Gender      Gender    `json:"gender"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`

	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`

	County          string `json:"county,omitempty"`
	SubCounty       string `json:"sub_county,omitempty"`
	PhysicalAddress string `json:"physical_address,omitempty"`

	EmploymentStatus string `json:"employment_status,omitempty"`
	Employer         string `json:"employer,omitempty"`
	PayrollNo        string `json:"payroll_no,omitempty"`
	JobTitle         string `json:"job_title,omitempty"`

	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	ApprovedBy      *uuid.UUID `json:"approved_by,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
}

type Relation struct {
	ID            uuid.UUID    `json:"id"`
	MemberID      uuid.UUID    `json:"member_id"`
	Kind          RelationKind `json:"kind"`
	FullName      string       `json:"full_name"`
	Relationship  string       `json:"relationship"`
	Phone         string       `json:"phone,omitempty"`
	Email         string       `json:"email,omitempty"`
	IDDocNumber   string       `json:"id_doc_number,omitempty"`
	SharePercent  *float64     `json:"share_percent,omitempty"`
	Position      int          `json:"position"`
}

type Document struct {
	ID          uuid.UUID    `json:"id"`
	MemberID    uuid.UUID    `json:"member_id"`
	Kind        DocumentKind `json:"kind"`
	StoragePath string       `json:"-"` // never expose raw storage path
	MIME        string       `json:"mime"`
	SizeBytes   int64        `json:"size_bytes"`
	UploadedAt  time.Time    `json:"uploaded_at"`
}

// ───────── Organisations ─────────

type OrgKind string

const (
	OrgGroup       OrgKind = "group"
	OrgChama       OrgKind = "chama"
	OrgLtd         OrgKind = "ltd"
	OrgSoleProp    OrgKind = "sole_prop"
	OrgNGO         OrgKind = "ngo"
	OrgChurch      OrgKind = "church"
	OrgSacco       OrgKind = "sacco"
	OrgCooperative OrgKind = "cooperative"
	OrgSchool      OrgKind = "school"
)

type OrgStatus string

const (
	OrgPending   OrgStatus = "pending"
	OrgActive    OrgStatus = "active"
	OrgSuspended OrgStatus = "suspended"
	OrgClosed    OrgStatus = "closed"
	OrgRejected  OrgStatus = "rejected"
	OrgDormant   OrgStatus = "dormant"
)

type RiskCategory string

const (
	RiskLow    RiskCategory = "low"
	RiskMedium RiskCategory = "medium"
	RiskHigh   RiskCategory = "high"
)

type KYCReviewStatus string

const (
	KYCNotStarted KYCReviewStatus = "not_started"
	KYCInReview   KYCReviewStatus = "in_review"
	KYCVerified   KYCReviewStatus = "verified"
	KYCRejected   KYCReviewStatus = "rejected"
)

type SignatoryClass string

const (
	SigMandatory SignatoryClass = "mandatory"
	SigOptional  SignatoryClass = "optional"
	SigAlternate SignatoryClass = "alternate"
)

type OrgDocKind string

const (
	DocRegistrationCertificate     OrgDocKind = "registration_certificate"
	DocCR12                        OrgDocKind = "cr12"
	DocKRAPINCertificate           OrgDocKind = "kra_pin_certificate"
	DocMemorandumArticles          OrgDocKind = "memorandum_articles"
	DocConstitutionBylaws          OrgDocKind = "constitution_bylaws"
	DocBusinessPermit              OrgDocKind = "business_permit"
	DocTaxComplianceCertificate    OrgDocKind = "tax_compliance_certificate"
	DocVATCertificate              OrgDocKind = "vat_certificate"
	DocNGOCertificate              OrgDocKind = "ngo_certificate"
	DocCooperativeCertificate      OrgDocKind = "cooperative_certificate"
	DocProofOfAddress              OrgDocKind = "proof_of_address"
	DocAuditedFinancials           OrgDocKind = "audited_financials"
	DocBankStatement               OrgDocKind = "bank_statement"
	DocBoardResolution             OrgDocKind = "board_resolution"
	DocSignatoryAppointmentResol   OrgDocKind = "signatory_appointment_resolution"
	DocBeneficialOwnershipDecl     OrgDocKind = "beneficial_ownership_declaration"
)

type DocVerification string

const (
	VerifyPending  DocVerification = "pending"
	VerifyVerified DocVerification = "verified"
	VerifyRejected DocVerification = "rejected"
)

type ContactKind string

const (
	ContactPrimary    ContactKind = "primary"
	ContactFinance    ContactKind = "finance"
	ContactHRPayroll  ContactKind = "hr_payroll"
	ContactCompliance ContactKind = "compliance"
)

type OfficialPosition string

const (
	PosChairperson     OfficialPosition = "chairperson"
	PosViceChairperson OfficialPosition = "vice_chairperson"
	PosTreasurer       OfficialPosition = "treasurer"
	PosSecretary       OfficialPosition = "secretary"
	PosDirector        OfficialPosition = "director"
	PosTrustee         OfficialPosition = "trustee"
	PosPrincipal       OfficialPosition = "principal"
	PosPastor          OfficialPosition = "pastor"
	PosOther           OfficialPosition = "other"
)

type Org struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`
	OrgNo    string    `json:"org_no"`
	Status   OrgStatus `json:"status"`

	RegisteredName     string     `json:"registered_name"`
	TradingName        string     `json:"trading_name,omitempty"`
	Kind               OrgKind    `json:"kind"`
	RegistrationNo     string     `json:"registration_no,omitempty"`
	DateOfRegistration *time.Time `json:"date_of_registration,omitempty"`
	DateOfOperation    *time.Time `json:"date_of_operation,omitempty"`
	Industry           string     `json:"industry,omitempty"`
	NatureOfBusiness   string     `json:"nature_of_business,omitempty"`
	MemberCount        *int       `json:"member_count,omitempty"`
	EmployeeCount      *int       `json:"employee_count,omitempty"`

	PhysicalAddress string     `json:"physical_address,omitempty"`
	PostalAddress   string     `json:"postal_address,omitempty"`
	County          string     `json:"county,omitempty"`
	SubCounty       string     `json:"sub_county,omitempty"`
	Ward            string     `json:"ward,omitempty"`
	GPSLat          *float64   `json:"gps_lat,omitempty"`
	GPSLng          *float64   `json:"gps_lng,omitempty"`
	BranchID        *uuid.UUID `json:"branch_id,omitempty"`

	RiskCategory    RiskCategory    `json:"risk_category"`
	KYCStatus       KYCReviewStatus `json:"kyc_status"`
	Blacklisted     bool            `json:"blacklisted"`
	BlacklistReason string          `json:"blacklist_reason,omitempty"`
	DormantSince    *time.Time      `json:"dormant_since,omitempty"`

	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	ApprovedBy      *uuid.UUID `json:"approved_by,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
}

type OrgDocument struct {
	ID               uuid.UUID       `json:"id"`
	OrgID            uuid.UUID       `json:"org_id"`
	Kind             OrgDocKind      `json:"kind"`
	StoragePath      string          `json:"-"`
	MIME             string          `json:"mime"`
	SizeBytes        int64           `json:"size_bytes"`
	IssueDate        *time.Time      `json:"issue_date,omitempty"`
	ExpiryDate       *time.Time      `json:"expiry_date,omitempty"`
	Verification     DocVerification `json:"verification"`
	VerifiedBy       *uuid.UUID      `json:"verified_by,omitempty"`
	VerifiedAt       *time.Time      `json:"verified_at,omitempty"`
	VerificationNote string          `json:"verification_note,omitempty"`
	UploadedAt       time.Time       `json:"uploaded_at"`
}

type OfficialFiles map[string]struct {
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Official struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"org_id"`
	TenantID uuid.UUID `json:"tenant_id"`

	FullName        string     `json:"full_name"`
	IDDocKind       IDDocKind  `json:"id_doc_kind"`
	IDDocNumber     string     `json:"id_doc_number"`
	KraPIN          string     `json:"kra_pin,omitempty"`
	DateOfBirth     *time.Time `json:"date_of_birth,omitempty"`
	Gender          Gender     `json:"gender"`
	Nationality     string     `json:"nationality,omitempty"`
	Phone           string     `json:"phone,omitempty"`
	Email           string     `json:"email,omitempty"`
	PhysicalAddress string     `json:"physical_address,omitempty"`
	Occupation      string     `json:"occupation,omitempty"`

	Position      OfficialPosition `json:"position"`
	PositionLabel string           `json:"position_label,omitempty"`
	AppointedOn   *time.Time       `json:"appointed_on,omitempty"`

	IsPEP               bool       `json:"is_pep"`
	PEPNote             string     `json:"pep_note,omitempty"`
	SanctionsScreenedAt *time.Time `json:"sanctions_screened_at,omitempty"`
	SanctionsScreenedBy *uuid.UUID `json:"sanctions_screened_by,omitempty"`
	SanctionsHit        bool       `json:"sanctions_hit"`
	SanctionsNote       string     `json:"sanctions_note,omitempty"`

	IsBeneficialOwner bool     `json:"is_beneficial_owner"`
	OwnershipPercent  *float64 `json:"ownership_percent,omitempty"`

	Files         OfficialFiles `json:"files"`
	PositionOrder int           `json:"position_order"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Signatory struct {
	ID            uuid.UUID      `json:"id"`
	OrgID         uuid.UUID      `json:"org_id"`
	OfficialID    uuid.UUID      `json:"official_id"`
	Class         SignatoryClass `json:"class"`
	SigningOrder  int            `json:"signing_order"`
	TxnLimit      *float64       `json:"txn_limit,omitempty"`
	EffectiveFrom time.Time      `json:"effective_from"`
}

type Mandate struct {
	OrgID     uuid.UUID              `json:"org_id"`
	Rules     map[string]any         `json:"rules"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type Banking struct {
	OrgID                   uuid.UUID `json:"org_id"`
	BankName                string    `json:"bank_name,omitempty"`
	BankBranch              string    `json:"bank_branch,omitempty"`
	BankCode                string    `json:"bank_code,omitempty"`
	SwiftCode               string    `json:"swift_code,omitempty"`
	AccountName             string    `json:"account_name,omitempty"`
	AccountNumber           string    `json:"account_number,omitempty"`
	Paybill                 string    `json:"paybill,omitempty"`
	TillNumber              string    `json:"till_number,omitempty"`
	MobileMoneyPhones       string    `json:"mobile_money_phones,omitempty"`
	MobileSettlementAccount string    `json:"mobile_settlement_account,omitempty"`
	PreferredDisbursement   string    `json:"preferred_disbursement,omitempty"`
	PreferredRepayment      string    `json:"preferred_repayment,omitempty"`
	StandingOrderDetails    string    `json:"standing_order_details,omitempty"`
	CheckoffArrangement     string    `json:"checkoff_arrangement,omitempty"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type Contact struct {
	ID       uuid.UUID   `json:"id"`
	OrgID    uuid.UUID   `json:"org_id"`
	Kind     ContactKind `json:"kind"`
	FullName string      `json:"full_name"`
	Role     string      `json:"role,omitempty"`
	Phone    string      `json:"phone,omitempty"`
	Email    string      `json:"email,omitempty"`
	Position int         `json:"position"`
}
