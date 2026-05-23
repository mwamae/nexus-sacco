// Unified membership application: domain types + state machine.

package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type ApplicationKind string

const (
	ApplicationKindIndividual    ApplicationKind = "individual"
	ApplicationKindInstitutional ApplicationKind = "institutional"
)

func (k ApplicationKind) Valid() bool {
	return k == ApplicationKindIndividual || k == ApplicationKindInstitutional
}

type ApplicationStatus string

const (
	AppStatusSubmitted             ApplicationStatus = "submitted"
	AppStatusUnderReview           ApplicationStatus = "under_review"
	AppStatusReturnedForCorrection ApplicationStatus = "returned_for_correction"
	AppStatusReviewedPendingApp    ApplicationStatus = "reviewed_pending_approval"
	AppStatusApprovedActive        ApplicationStatus = "approved_active"
	AppStatusDeclined              ApplicationStatus = "declined"
	AppStatusWithdrawn             ApplicationStatus = "withdrawn"
)

func (s ApplicationStatus) Valid() bool {
	switch s {
	case AppStatusSubmitted, AppStatusUnderReview, AppStatusReturnedForCorrection,
		AppStatusReviewedPendingApp, AppStatusApprovedActive, AppStatusDeclined, AppStatusWithdrawn:
		return true
	}
	return false
}

// ApplicationTransition encodes the legal moves through the
// pipeline. Anything not listed here is rejected by ValidateAppTx.
//
//	submitted                 → under_review | withdrawn
//	under_review              → returned_for_correction | reviewed_pending_approval | withdrawn
//	returned_for_correction   → submitted (officer re-submits) | withdrawn
//	reviewed_pending_approval → approved_active | declined | under_review (approver bounce-back) | withdrawn
//	approved_active           — terminal (Phase D activation runs)
//	declined                  — terminal (with optional refund prompt)
//	withdrawn                 — terminal
type ApplicationTransition struct {
	From, To ApplicationStatus
}

var appTransitions = []ApplicationTransition{
	{AppStatusSubmitted, AppStatusUnderReview},
	{AppStatusSubmitted, AppStatusWithdrawn},

	{AppStatusUnderReview, AppStatusReturnedForCorrection},
	{AppStatusUnderReview, AppStatusReviewedPendingApp},
	{AppStatusUnderReview, AppStatusWithdrawn},

	{AppStatusReturnedForCorrection, AppStatusSubmitted}, // officer re-submits
	{AppStatusReturnedForCorrection, AppStatusWithdrawn},

	{AppStatusReviewedPendingApp, AppStatusApprovedActive},
	{AppStatusReviewedPendingApp, AppStatusDeclined},
	{AppStatusReviewedPendingApp, AppStatusUnderReview}, // approver returns to reviewer
	{AppStatusReviewedPendingApp, AppStatusWithdrawn},
}

func CanTransitionApp(from, to ApplicationStatus) bool {
	for _, t := range appTransitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}

var ErrIllegalAppTransition = errors.New("application: illegal status transition")

// ApplicantPayload — flexible JSON shape stored on the application
// row. Individual + institutional variants share the same field set
// at the application stage; consumers branch on `Kind`.
type ApplicantPayload struct {
	// Individual-specific
	DateOfBirth        *time.Time `json:"date_of_birth,omitempty"`
	Gender             string     `json:"gender,omitempty"`
	Nationality        string     `json:"nationality,omitempty"`
	IDDocKind          string     `json:"id_doc_kind,omitempty"` // national_id | passport
	IDDocNumber        string     `json:"id_doc_number,omitempty"`
	KRAPIN             string     `json:"kra_pin,omitempty"`
	Occupation         string     `json:"occupation,omitempty"`
	Employer           string     `json:"employer,omitempty"`
	MonthlyIncome      *decimal.Decimal `json:"monthly_income,omitempty"`
	NextOfKinName      string     `json:"next_of_kin_name,omitempty"`
	NextOfKinRelation  string     `json:"next_of_kin_relation,omitempty"`
	NextOfKinPhone     string     `json:"next_of_kin_phone,omitempty"`
	NextOfKinIDNumber  string     `json:"next_of_kin_id_number,omitempty"`

	// Institutional-specific
	RegisteredName     string `json:"registered_name,omitempty"`
	TradingName        string `json:"trading_name,omitempty"`
	RegistrationNumber string `json:"registration_number,omitempty"`
	DateOfRegistration *time.Time `json:"date_of_registration,omitempty"`
	Industry           string `json:"industry,omitempty"`
	NatureOfBusiness   string `json:"nature_of_business,omitempty"`
	BoardResolutionRef string `json:"board_resolution_ref,omitempty"`
	BeneficialOwners   string `json:"beneficial_owners,omitempty"` // free-text declaration

	// Shared
	PhysicalAddress    string `json:"physical_address,omitempty"`
	PostalAddress      string `json:"postal_address,omitempty"`
	County             string `json:"county,omitempty"`
	SubCounty          string `json:"sub_county,omitempty"`
	Ward               string `json:"ward,omitempty"`

	// Free-form notes captured by the officer at submission time.
	Notes string `json:"notes,omitempty"`
}

// EncodePayload returns a JSON byte slice for DB storage.
func EncodePayload(p ApplicantPayload) ([]byte, error) {
	return json.Marshal(p)
}

// MembershipApplication — the row + helpers projection.
type MembershipApplication struct {
	ID                   uuid.UUID         `json:"id"`
	TenantID             uuid.UUID         `json:"tenant_id"`
	ApplicationNo        string            `json:"application_no"`
	Kind                 ApplicationKind   `json:"kind"`
	Status               ApplicationStatus `json:"status"`

	ApplicantName        string            `json:"applicant_name"`
	EntityType           *string           `json:"entity_type,omitempty"`
	PrimaryPhone         *string           `json:"primary_phone,omitempty"`
	PrimaryEmail         *string           `json:"primary_email,omitempty"`
	BranchID             *uuid.UUID        `json:"branch_id,omitempty"`

	ApplicantPayload     json.RawMessage   `json:"applicant_payload"`

	FeeRequired          bool              `json:"fee_required"`
	FeeAmountDue         decimal.Decimal   `json:"fee_amount_due"`
	FeeAmountPaid        decimal.Decimal   `json:"fee_amount_paid"`
	FeePaymentChannel    *string           `json:"fee_payment_channel,omitempty"`
	FeePaymentReference  *string           `json:"fee_payment_reference,omitempty"`
	FeePaymentDate       *time.Time        `json:"fee_payment_date,omitempty"`
	FeeProofDocPath      *string           `json:"fee_proof_doc_path,omitempty"`
	FeeShortfallNote     *string           `json:"fee_shortfall_note,omitempty"`
	FeeStatus            string            `json:"fee_status"`

	SubmittedAt          time.Time         `json:"submitted_at"`
	SubmittedBy          uuid.UUID         `json:"submitted_by"`

	ReviewerUserID       *uuid.UUID        `json:"reviewer_user_id,omitempty"`
	ReviewStartedAt      *time.Time        `json:"review_started_at,omitempty"`
	ReviewCompletedAt    *time.Time        `json:"review_completed_at,omitempty"`
	ReviewSummaryNote    *string           `json:"review_summary_note,omitempty"`

	ApproverUserID       *uuid.UUID        `json:"approver_user_id,omitempty"`
	ApprovedAt           *time.Time        `json:"approved_at,omitempty"`
	DeclineReason        *string           `json:"decline_reason,omitempty"`
	ApprovalConditions   *string           `json:"approval_conditions,omitempty"`
	WorkflowInstanceID   *uuid.UUID        `json:"workflow_instance_id,omitempty"`

	WithdrawnAt          *time.Time        `json:"withdrawn_at,omitempty"`
	WithdrawnBy          *uuid.UUID        `json:"withdrawn_by,omitempty"`
	WithdrawReason       *string           `json:"withdraw_reason,omitempty"`

	// Activation linkage (Phase D, simplified in Phase E C). Populated
	// when the application is approved and the auto-activation pipeline
	// materialises the counterparty + share + savings + GL post.
	// Frontend follows the deep-link via /counterparties/<id>.
	MaterializedCounterpartyID *uuid.UUID `json:"materialized_counterparty_id,omitempty"`
	MaterializedAt             *time.Time `json:"materialized_at,omitempty"`
	FeeJournalEntryID          *uuid.UUID `json:"fee_journal_entry_id,omitempty"`
	FeeRefundJournalEntryID    *uuid.UUID `json:"fee_refund_journal_entry_id,omitempty"`

	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`

	// DaysInQueue is computed server-side for the queue display.
	DaysInQueue          int               `json:"days_in_queue"`
}

type ChecklistItem struct {
	ID           uuid.UUID       `json:"id"`
	Kind         ApplicationKind `json:"kind"`
	Code         string          `json:"code"`
	Label        string          `json:"label"`
	Description  *string         `json:"description,omitempty"`
	Mandatory    bool            `json:"mandatory"`
	DisplayOrder int             `json:"display_order"`
	IsActive     bool            `json:"is_active"`
}

type ChecklistResponse struct {
	ID             uuid.UUID `json:"id"`
	ApplicationID  uuid.UUID `json:"application_id"`
	ChecklistCode  string    `json:"checklist_code"`
	Response       string    `json:"response"` // confirmed | flagged | n/a
	Note           *string   `json:"note,omitempty"`
	RespondedBy    uuid.UUID `json:"responded_by"`
	RespondedAt    time.Time `json:"responded_at"`
}

type ApplicationDocument struct {
	ID            uuid.UUID `json:"id"`
	ApplicationID uuid.UUID `json:"application_id"`
	Kind          string    `json:"kind"`
	Filename      string    `json:"filename"`
	MIMEType      string    `json:"mime_type"`
	SizeBytes     int64     `json:"size_bytes"`
	UploadedAt    time.Time `json:"uploaded_at"`
	UploadedBy    uuid.UUID `json:"uploaded_by"`
}

type CorrectionEvent struct {
	ID            uuid.UUID `json:"id"`
	ApplicationID uuid.UUID `json:"application_id"`
	EventKind     string    `json:"event_kind"` // returned | resubmitted
	ActorUserID   uuid.UUID `json:"actor_user_id"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"created_at"`
}
