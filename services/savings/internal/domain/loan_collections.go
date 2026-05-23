// Collections + restructuring domain types (Phase 6e).

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Collections ───────────

type CollectionCaseStatus string

const (
	CaseOpen                CollectionCaseStatus = "open"
	CaseInProgress          CollectionCaseStatus = "in_progress"
	CasePaused              CollectionCaseStatus = "paused"
	CaseEscalatedLegal      CollectionCaseStatus = "escalated_legal"
	CaseClosedRecovered     CollectionCaseStatus = "closed_recovered"
	CaseClosedUncollectable CollectionCaseStatus = "closed_uncollectable"
)

type ContactKind string

const (
	ContactCall    ContactKind = "call"
	ContactSMS     ContactKind = "sms"
	ContactWhatsApp ContactKind = "whatsapp"
	ContactEmail   ContactKind = "email"
	ContactVisit   ContactKind = "in_person_visit"
	ContactLetter  ContactKind = "letter"
)

func (k ContactKind) Valid() bool {
	switch k {
	case ContactCall, ContactSMS, ContactWhatsApp, ContactEmail, ContactVisit, ContactLetter:
		return true
	}
	return false
}

type ContactOutcome string

const (
	OutcomeReached       ContactOutcome = "reached"
	OutcomeNoAnswer      ContactOutcome = "no_answer"
	OutcomeWrongNumber   ContactOutcome = "wrong_number"
	OutcomeBusy          ContactOutcome = "busy"
	OutcomeLeftMessage   ContactOutcome = "left_message"
	OutcomePromiseMade   ContactOutcome = "promise_made"
	OutcomeDispute       ContactOutcome = "dispute"
	OutcomeRefused       ContactOutcome = "refused"
	OutcomeVisitedNotHome ContactOutcome = "visited_not_home"
)

func (o ContactOutcome) Valid() bool {
	switch o {
	case OutcomeReached, OutcomeNoAnswer, OutcomeWrongNumber, OutcomeBusy,
		OutcomeLeftMessage, OutcomePromiseMade, OutcomeDispute, OutcomeRefused, OutcomeVisitedNotHome:
		return true
	}
	return false
}

type PTPStatus string

const (
	PTPOpen     PTPStatus = "open"
	PTPKept     PTPStatus = "kept"
	PTPPartial  PTPStatus = "partial"
	PTPBroken   PTPStatus = "broken"
	PTPCancelled PTPStatus = "cancelled"
)

type CollectionCase struct {
	ID                    uuid.UUID            `json:"id"`
	TenantID              uuid.UUID            `json:"tenant_id"`
	LoanID                uuid.UUID            `json:"loan_id"`
	CounterpartyID              uuid.UUID            `json:"counterparty_id"`
	Status                CollectionCaseStatus `json:"status"`
	ClassificationAtOpen  *string              `json:"classification_at_open,omitempty"`
	AssignedTo            *uuid.UUID           `json:"assigned_to,omitempty"`
	AssignedAt            *time.Time           `json:"assigned_at,omitempty"`
	Priority              int                  `json:"priority"`
	TotalContacts         int                  `json:"total_contacts"`
	LastContactAt         *time.Time           `json:"last_contact_at,omitempty"`
	LastAction            *string              `json:"last_action,omitempty"`
	Notes                 *string              `json:"notes,omitempty"`
	OpenedAt              time.Time            `json:"opened_at"`
	ClosedAt              *time.Time           `json:"closed_at,omitempty"`
	ClosedBy              *uuid.UUID           `json:"closed_by,omitempty"`
	ClosureReason         *string              `json:"closure_reason,omitempty"`
}

type CollectionContact struct {
	ID           uuid.UUID       `json:"id"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	CaseID       uuid.UUID       `json:"case_id"`
	Kind         ContactKind     `json:"kind"`
	Outcome      ContactOutcome  `json:"outcome"`
	Note         *string         `json:"note,omitempty"`
	GPSLat       *decimal.Decimal `json:"gps_lat,omitempty"`
	GPSLng       *decimal.Decimal `json:"gps_lng,omitempty"`
	ContactedAt  time.Time       `json:"contacted_at"`
	ContactedBy  uuid.UUID       `json:"contacted_by"`
}

type PromiseToPay struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	CaseID          uuid.UUID       `json:"case_id"`
	LoanID          uuid.UUID       `json:"loan_id"`
	PromisedAmount  decimal.Decimal `json:"promised_amount"`
	PromisedDate    time.Time       `json:"promised_date"`
	PromisedChannel *string         `json:"promised_channel,omitempty"`
	Status          PTPStatus       `json:"status"`
	PaidAmount      decimal.Decimal `json:"paid_amount"`
	PaidTxnID       *uuid.UUID      `json:"paid_txn_id,omitempty"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
	ResolvedBy      *uuid.UUID      `json:"resolved_by,omitempty"`
	Notes           *string         `json:"notes,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	CreatedBy       uuid.UUID       `json:"created_by"`
}

// ─────────── Restructuring ───────────

type RestructuringKind string

const (
	RestructureReschedule         RestructuringKind = "reschedule"
	RestructureTopup              RestructuringKind = "topup"
	RestructureRefinance          RestructuringKind = "refinance"
	RestructureMoratorium         RestructuringKind = "moratorium"
	RestructureSettlementDiscount RestructuringKind = "settlement_discount"
)

func (k RestructuringKind) Valid() bool {
	switch k {
	case RestructureReschedule, RestructureTopup, RestructureRefinance,
		RestructureMoratorium, RestructureSettlementDiscount:
		return true
	}
	return false
}

type LoanRestructuring struct {
	ID                       uuid.UUID            `json:"id"`
	TenantID                 uuid.UUID            `json:"tenant_id"`
	LoanID                   uuid.UUID            `json:"loan_id"`
	Kind                     RestructuringKind    `json:"kind"`
	Reason                   string               `json:"reason"`
	PreviousPrincipalBalance *decimal.Decimal     `json:"previous_principal_balance,omitempty"`
	PreviousInterestBalance  *decimal.Decimal     `json:"previous_interest_balance,omitempty"`
	PreviousTermMonths       *int                 `json:"previous_term_months,omitempty"`
	PreviousInterestRatePct  *decimal.Decimal     `json:"previous_interest_rate_pct,omitempty"`
	PreviousRepaymentMethod  *LoanRepaymentMethod `json:"previous_repayment_method,omitempty"`
	PreviousStatus           *LoanStatus          `json:"previous_status,omitempty"`
	NewTermMonths            *int                 `json:"new_term_months,omitempty"`
	NewInterestRatePct       *decimal.Decimal     `json:"new_interest_rate_pct,omitempty"`
	TopupAmount              *decimal.Decimal     `json:"topup_amount,omitempty"`
	RefinanceNewLoanID       *uuid.UUID           `json:"refinance_new_loan_id,omitempty"`
	MoratoriumMonths         *int                 `json:"moratorium_months,omitempty"`
	MoratoriumSuspendInterest *bool               `json:"moratorium_suspend_interest,omitempty"`
	DiscountAmount           *decimal.Decimal     `json:"discount_amount,omitempty"`
	DiscountWriteoffTxnID    *uuid.UUID           `json:"discount_writeoff_txn_id,omitempty"`
	WorkflowInstanceID       *uuid.UUID           `json:"workflow_instance_id,omitempty"`
	AuthorizedAt             *time.Time           `json:"authorized_at,omitempty"`
	AuthorizedBy             *uuid.UUID           `json:"authorized_by,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	CreatedBy                uuid.UUID            `json:"created_by"`
}

// ─────────── Errors ───────────

var (
	ErrCaseNotOpen          = errors.New("collection case is not in an open state")
	ErrPTPNotOpen           = errors.New("promise-to-pay is not in 'open' state")
	ErrRestructureNotAllowed = errors.New("loan is not in a state that permits restructuring")
	ErrInvalidRestructuringKind = errors.New("invalid restructuring kind")
	ErrMoratoriumMonthsInvalid = errors.New("moratorium_months must be > 0")
	ErrRescheduleTermInvalid = errors.New("new_term_months must be > 0")
)
