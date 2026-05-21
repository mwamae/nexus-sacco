// Bank reconciliation domain types.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Bank account ───────────

type BankAccount struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	GLAccountCode  string    `json:"gl_account_code"`
	BankName       string    `json:"bank_name"`
	AccountNumber  string    `json:"account_number"`
	Branch         *string   `json:"branch,omitempty"`
	CurrencyCode   string    `json:"currency_code"`
	IsActive       bool      `json:"is_active"`
	Notes          *string   `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ─────────── Bank statement (header) ───────────

type BankStatement struct {
	ID              uuid.UUID        `json:"id"`
	TenantID        uuid.UUID        `json:"tenant_id"`
	BankAccountID   uuid.UUID        `json:"bank_account_id"`
	StatementDate   time.Time        `json:"statement_date"`
	PeriodStart     *time.Time       `json:"period_start,omitempty"`
	PeriodEnd       *time.Time       `json:"period_end,omitempty"`
	OpeningBalance  *decimal.Decimal `json:"opening_balance,omitempty"`
	ClosingBalance  *decimal.Decimal `json:"closing_balance,omitempty"`
	TotalDebits     decimal.Decimal  `json:"total_debits"`
	TotalCredits    decimal.Decimal  `json:"total_credits"`
	LineCount       int              `json:"line_count"`
	SourceFormat    string           `json:"source_format"`
	SourceFilename  *string          `json:"source_filename,omitempty"`
	UploadedAt      time.Time        `json:"uploaded_at"`
	UploadedBy      *uuid.UUID       `json:"uploaded_by,omitempty"`
}

// ─────────── Statement line ───────────

type BankStatementMatchStatus string

const (
	BankLineUnmatched   BankStatementMatchStatus = "unmatched"
	BankLineMatched     BankStatementMatchStatus = "matched"
	BankLineManualMatch BankStatementMatchStatus = "manual_match"
	BankLineExcluded    BankStatementMatchStatus = "excluded"
	BankLineAdjusted    BankStatementMatchStatus = "adjusted"
)

type BankStatementLine struct {
	ID                    uuid.UUID                `json:"id"`
	TenantID              uuid.UUID                `json:"tenant_id"`
	StatementID           uuid.UUID                `json:"statement_id"`
	BankAccountID         uuid.UUID                `json:"bank_account_id"`
	LineNo                int                      `json:"line_no"`
	TxnDate               time.Time                `json:"txn_date"`
	ValueDate             *time.Time               `json:"value_date,omitempty"`
	Description           *string                  `json:"description,omitempty"`
	Reference             *string                  `json:"reference,omitempty"`
	Debit                 decimal.Decimal          `json:"debit"`
	Credit                decimal.Decimal          `json:"credit"`
	RunningBalance        *decimal.Decimal         `json:"running_balance,omitempty"`
	MatchStatus           BankStatementMatchStatus `json:"match_status"`
	MatchedJournalLineID  *uuid.UUID               `json:"matched_journal_line_id,omitempty"`
	MatchedAt             *time.Time               `json:"matched_at,omitempty"`
	MatchedBy             *uuid.UUID               `json:"matched_by,omitempty"`
	MatchNotes            *string                  `json:"match_notes,omitempty"`
}
