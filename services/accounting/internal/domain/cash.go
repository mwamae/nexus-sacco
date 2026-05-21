// Cash & Float Management domain types.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Till struct {
	ID                    uuid.UUID        `json:"id"`
	TenantID              uuid.UUID        `json:"tenant_id"`
	Code                  string           `json:"code"`
	Name                  string           `json:"name"`
	Branch                *string          `json:"branch,omitempty"`
	GLAccountCode         string           `json:"gl_account_code"`
	VaultAccountCode      string           `json:"vault_account_code"`
	VarianceAccountCode   string           `json:"variance_account_code"`
	MaxFloat              *decimal.Decimal `json:"max_float,omitempty"`
	IsActive              bool             `json:"is_active"`
	Notes                 *string          `json:"notes,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type TillSessionStatus string

const (
	SessionOpen   TillSessionStatus = "open"
	SessionClosed TillSessionStatus = "closed"
)

type TillSession struct {
	ID                       uuid.UUID         `json:"id"`
	TenantID                 uuid.UUID         `json:"tenant_id"`
	TillID                   uuid.UUID         `json:"till_id"`
	TellerUserID             uuid.UUID         `json:"teller_user_id"`
	Status                   TillSessionStatus `json:"status"`
	OpeningFloat             decimal.Decimal   `json:"opening_float"`
	ExpectedClose            decimal.Decimal   `json:"expected_close"`
	ActualClose              *decimal.Decimal  `json:"actual_close,omitempty"`
	Variance                 decimal.Decimal   `json:"variance"`
	VarianceJournalEntryID   *uuid.UUID        `json:"variance_journal_entry_id,omitempty"`
	OpenedAt                 time.Time         `json:"opened_at"`
	OpenedBy                 uuid.UUID         `json:"opened_by"`
	ClosedAt                 *time.Time        `json:"closed_at,omitempty"`
	ClosedBy                 *uuid.UUID        `json:"closed_by,omitempty"`
	Notes                    *string           `json:"notes,omitempty"`
}

type CashTransferType string

const (
	TransferVaultToTill        CashTransferType = "vault_to_till"
	TransferTillToVault        CashTransferType = "till_to_vault"
	TransferTillToTill         CashTransferType = "till_to_till"
	TransferOpeningFloat       CashTransferType = "opening_float"
	TransferClosingReturn      CashTransferType = "closing_return"
	TransferVarianceAdjustment CashTransferType = "variance_adjustment"
)

type CashTransfer struct {
	ID                uuid.UUID         `json:"id"`
	TenantID          uuid.UUID         `json:"tenant_id"`
	TransferType      CashTransferType  `json:"transfer_type"`
	FromTillID        *uuid.UUID        `json:"from_till_id,omitempty"`
	ToTillID          *uuid.UUID        `json:"to_till_id,omitempty"`
	SessionID         *uuid.UUID        `json:"session_id,omitempty"`
	Amount            decimal.Decimal   `json:"amount"`
	Reference         *string           `json:"reference,omitempty"`
	Narration         *string           `json:"narration,omitempty"`
	JournalEntryID    *uuid.UUID        `json:"journal_entry_id,omitempty"`
	TransferredAt     time.Time         `json:"transferred_at"`
	TransferredBy     uuid.UUID         `json:"transferred_by"`
}
