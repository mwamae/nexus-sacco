// Loans Phase 4 domain types — workflow events + escalation rules +
// dividend offset records.
//
// Lives in a separate file from loan_collections.go (the Phase 6e
// types) so the legacy code stays untouched. The Phase 4 backend
// shares the legacy CollectionCase + PromiseToPay types; only NEW
// concepts (events table, escalation rule rows, dividend-offset
// audit rows) need new domain types.

package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Collection events (Phase 4) ───────────

type CollectionEventKind string

const (
	EventNote            CollectionEventKind = "note"
	EventAutoSMS         CollectionEventKind = "auto_sms"
	EventAutoEmail       CollectionEventKind = "auto_email"
	EventPTPCreated      CollectionEventKind = "ptp_created"
	EventPTPKept         CollectionEventKind = "ptp_kept"
	EventPTPBroken       CollectionEventKind = "ptp_broken"
	EventPTPCancelled    CollectionEventKind = "ptp_cancelled"
	EventEscalation      CollectionEventKind = "escalation"
	EventLegalHandover   CollectionEventKind = "legal_handover"
	EventAssigned        CollectionEventKind = "assigned"
	EventUnassigned      CollectionEventKind = "unassigned"
	EventLetterGenerated CollectionEventKind = "letter_generated"
)

func (k CollectionEventKind) Valid() bool {
	switch k {
	case EventNote, EventAutoSMS, EventAutoEmail,
		EventPTPCreated, EventPTPKept, EventPTPBroken, EventPTPCancelled,
		EventEscalation, EventLegalHandover, EventAssigned, EventUnassigned,
		EventLetterGenerated:
		return true
	}
	return false
}

type CollectionLetterKind string

const (
	LetterPreCollection CollectionLetterKind = "pre_collection"
	LetterDemand        CollectionLetterKind = "demand"
	LetterFinalDemand   CollectionLetterKind = "final_demand"
	LetterLegalNotice   CollectionLetterKind = "legal_notice"
)

func (k CollectionLetterKind) Valid() bool {
	switch k {
	case LetterPreCollection, LetterDemand, LetterFinalDemand, LetterLegalNotice:
		return true
	}
	return false
}

// LoanDocKindForLetter maps the workflow letter kind to the
// loan_documents.kind enum value that the generated PDF lands under.
func (k CollectionLetterKind) LoanDocKind() string {
	switch k {
	case LetterPreCollection:
		return "pre_collection_letter"
	case LetterDemand:
		return "demand_letter"
	case LetterFinalDemand:
		return "final_demand_letter"
	case LetterLegalNotice:
		return "legal_notice_letter"
	}
	return "other"
}

// CollectionEvent — one workflow event against a loan/case. Distinct
// from CollectionContact (the legacy human-logged contact record).
// The Phase 4 timeline endpoint UNIONs both.
type CollectionEvent struct {
	ID            uuid.UUID            `json:"id"`
	TenantID      uuid.UUID            `json:"tenant_id"`
	CaseID        *uuid.UUID           `json:"case_id,omitempty"`
	LoanID        uuid.UUID            `json:"loan_id"`
	Kind          CollectionEventKind  `json:"kind"`
	OccurredAt    time.Time            `json:"occurred_at"`
	CreatedBy     *uuid.UUID           `json:"created_by,omitempty"`
	Details       json.RawMessage      `json:"details"`
	LetterKind    *CollectionLetterKind `json:"letter_kind,omitempty"`
	Amount        *decimal.Decimal     `json:"amount,omitempty"`
	PromisedDate  *time.Time           `json:"promised_date,omitempty"`
}

// ─────────── Assignment history (Phase 4) ───────────

type LoanAssignment struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	CaseID     uuid.UUID  `json:"case_id"`
	LoanID     uuid.UUID  `json:"loan_id"`
	OfficerID  uuid.UUID  `json:"officer_id"`
	AssignedAt time.Time  `json:"assigned_at"`
	AssignedBy uuid.UUID  `json:"assigned_by"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	EndedBy    *uuid.UUID `json:"ended_by,omitempty"`
	EndReason  *string    `json:"end_reason,omitempty"`
}

// ─────────── Escalation rule ───────────

type EscalationRule struct {
	TenantID     uuid.UUID             `json:"tenant_id"`
	DPDMin       int                   `json:"dpd_min"`
	DPDMax       *int                  `json:"dpd_max,omitempty"`
	RequiredRole string                `json:"required_role"`
	LetterKind   *CollectionLetterKind `json:"letter_kind,omitempty"`
	AutoSMS      bool                  `json:"auto_sms"`
	Description  *string               `json:"description,omitempty"`
}

// MessageTemplate — per-tenant SMS/email body templates.
type CollectionMessageTemplate struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	Channel      string    `json:"channel"` // sms | email
	DPDMin       int       `json:"dpd_min"`
	BodyTemplate string    `json:"body_template"`
	Subject      *string   `json:"subject,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ─────────── Dividend offset ───────────

type DividendOffsetPosting struct {
	ID             uuid.UUID       `json:"id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	DividendRunID  uuid.UUID       `json:"dividend_run_id"`
	MemberID       uuid.UUID       `json:"member_id"`
	LoanID         uuid.UUID       `json:"loan_id"`
	Amount         decimal.Decimal `json:"amount"`
	Allocation     json.RawMessage `json:"allocation"`
	PostedAt       time.Time       `json:"posted_at"`
	PostedBy       uuid.UUID       `json:"posted_by"`
	JournalEntryID *uuid.UUID      `json:"journal_entry_id,omitempty"`
	SourceRef      string          `json:"source_ref"`
}

// ─────────── Errors ───────────

var (
	ErrInvalidEventKind  = errors.New("invalid collection event kind")
	ErrInvalidLetterKind = errors.New("invalid letter kind")
	ErrPTPAlreadyClosed  = errors.New("promise-to-pay is already closed")
	ErrCaseRequired      = errors.New("loan has no open collection case — cannot perform action")
)
