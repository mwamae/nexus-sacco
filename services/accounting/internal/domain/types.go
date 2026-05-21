// Accounting domain types. Mirrors the schema in 0001_init.up.sql.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Chart of Accounts ───────────

type AccountClass string

const (
	ClassAsset     AccountClass = "asset"
	ClassLiability AccountClass = "liability"
	ClassEquity    AccountClass = "equity"
	ClassIncome    AccountClass = "income"
	ClassExpense   AccountClass = "expense"
)

func (c AccountClass) Valid() bool {
	switch c {
	case ClassAsset, ClassLiability, ClassEquity, ClassIncome, ClassExpense:
		return true
	}
	return false
}

type NormalBalance string

const (
	NormalDebit  NormalBalance = "debit"
	NormalCredit NormalBalance = "credit"
)

type Account struct {
	ID             uuid.UUID     `json:"id"`
	TenantID       uuid.UUID     `json:"tenant_id"`
	Code           string        `json:"code"`
	Name           string        `json:"name"`
	Class          AccountClass  `json:"class"`
	Type           string        `json:"type"`
	ParentID       *uuid.UUID    `json:"parent_id,omitempty"`
	NormalBalance  NormalBalance `json:"normal_balance"`
	CurrencyCode   string        `json:"currency_code"`
	IsActive       bool          `json:"is_active"`
	IsSystemLocked bool          `json:"is_system_locked"`
	Description    *string       `json:"description,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// ─────────── Accounting periods ───────────

type PeriodStatus string

const (
	PeriodOpen   PeriodStatus = "open"
	PeriodClosed PeriodStatus = "closed"
)

type Period struct {
	ID        uuid.UUID    `json:"id"`
	TenantID  uuid.UUID    `json:"tenant_id"`
	Year      int          `json:"year"`
	Month     int          `json:"month"` // 1..13 (13 is the year-end adjustment period)
	Status    PeriodStatus `json:"status"`
	OpenedAt  *time.Time   `json:"opened_at,omitempty"`
	OpenedBy  *uuid.UUID   `json:"opened_by,omitempty"`
	ClosedAt  *time.Time   `json:"closed_at,omitempty"`
	ClosedBy  *uuid.UUID   `json:"closed_by,omitempty"`
	Notes     *string      `json:"notes,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// ─────────── Journal entries ───────────

type JournalEntryStatus string

const (
	EntryDraft           JournalEntryStatus = "draft"
	EntryPendingApproval JournalEntryStatus = "pending_approval"
	EntryPosted          JournalEntryStatus = "posted"
	EntryRejected        JournalEntryStatus = "rejected"
)

type JournalEntryType string

const (
	TypeAuto           JournalEntryType = "auto"
	TypeManual         JournalEntryType = "manual"
	TypeAdjustment     JournalEntryType = "adjustment"
	TypeReversal       JournalEntryType = "reversal"
	TypeOpeningBalance JournalEntryType = "opening_balance"
)

type JournalEntry struct {
	ID              uuid.UUID          `json:"id"`
	TenantID        uuid.UUID          `json:"tenant_id"`
	EntryNo         *string            `json:"entry_no,omitempty"`
	EntryDate       time.Time          `json:"entry_date"`
	ValueDate       time.Time          `json:"value_date"`
	PeriodYear      int                `json:"period_year"`
	PeriodMonth     int                `json:"period_month"`
	EntryType       JournalEntryType   `json:"entry_type"`
	SourceModule    *string            `json:"source_module,omitempty"`
	SourceRef       *string            `json:"source_ref,omitempty"`
	Narration       string             `json:"narration"`
	Status          JournalEntryStatus `json:"status"`
	TotalDebits     decimal.Decimal    `json:"total_debits"`
	TotalCredits    decimal.Decimal    `json:"total_credits"`
	ReversalOf      *uuid.UUID         `json:"reversal_of,omitempty"`
	CreatedBy       *uuid.UUID         `json:"created_by,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	PostedBy        *uuid.UUID         `json:"posted_by,omitempty"`
	PostedAt        *time.Time         `json:"posted_at,omitempty"`
	RejectedBy      *uuid.UUID         `json:"rejected_by,omitempty"`
	RejectedAt      *time.Time         `json:"rejected_at,omitempty"`
	RejectionReason *string            `json:"rejection_reason,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
	Lines           []JournalLine      `json:"lines,omitempty"`
}

type JournalLine struct {
	ID         uuid.UUID       `json:"id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	EntryID    uuid.UUID       `json:"entry_id"`
	LineNo     int             `json:"line_no"`
	AccountID  uuid.UUID       `json:"account_id"`
	// AccountCode/Name are populated via JOIN on read paths so the UI
	// doesn't need a second round trip per line.
	AccountCode string         `json:"account_code,omitempty"`
	AccountName string         `json:"account_name,omitempty"`
	Debit      decimal.Decimal `json:"debit"`
	Credit     decimal.Decimal `json:"credit"`
	Narration  *string         `json:"narration,omitempty"`
}

// ─────────── Posting rules ───────────

type PostingRule struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	EventCode   string     `json:"event_code"`
	ProductID   *uuid.UUID `json:"product_id,omitempty"`
	Name        string     `json:"name"`
	Description *string    `json:"description,omitempty"`
	Lines       []byte     `json:"lines"` // raw jsonb; clients parse as needed
	IsActive    bool       `json:"is_active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ─────────── Trial Balance row (computed; no underlying table) ───────────

type TrialBalanceRow struct {
	AccountID     uuid.UUID       `json:"account_id"`
	AccountCode   string          `json:"account_code"`
	AccountName   string          `json:"account_name"`
	Class         AccountClass    `json:"class"`
	NormalBalance NormalBalance   `json:"normal_balance"`
	OpeningDebit  decimal.Decimal `json:"opening_debit"`
	OpeningCredit decimal.Decimal `json:"opening_credit"`
	PeriodDebits  decimal.Decimal `json:"period_debits"`
	PeriodCredits decimal.Decimal `json:"period_credits"`
	ClosingDebit  decimal.Decimal `json:"closing_debit"`
	ClosingCredit decimal.Decimal `json:"closing_credit"`
}
