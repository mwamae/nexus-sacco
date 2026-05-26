// Collection Desk domain types — receipts, receipt lines, and the
// per-tenant virtual tills that anchor non-cash channel reconciliation.
//
// A receipt is the cashier-facing unit of work: one counterparty, one
// payment, one printed slip. The N lines on a receipt fan out to the
// existing per-kind handlers (deposit, share purchase, loan repayment,
// fee) and are coordinated by status — the receipt is "posted" only
// when every line has reached a terminal state.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Enums ───────────

type ReceiptStatus string

const (
	ReceiptDraft  ReceiptStatus = "draft"
	ReceiptPosted ReceiptStatus = "posted"
	ReceiptVoided ReceiptStatus = "voided"
)

type ReceiptLineKind string

const (
	LineSavingsDeposit ReceiptLineKind = "savings_deposit"
	LineSharePurchase  ReceiptLineKind = "share_purchase"
	LineLoanRepayment  ReceiptLineKind = "loan_repayment"
	LineFee            ReceiptLineKind = "fee"
	LineWelfare        ReceiptLineKind = "welfare"
)

type ReceiptLineStatus string

const (
	LinePending  ReceiptLineStatus = "pending"
	LinePosted   ReceiptLineStatus = "posted"
	LineDeclined ReceiptLineStatus = "declined"
	LineVoided   ReceiptLineStatus = "voided"
)

type ReceiptChannel string

// Distinct from domain.PaymentChannel (the share/legacy enum); kept
// separate because the receipt channel set is the canonical
// Collection-Desk one and may diverge over time. Constants are
// prefixed RC* to avoid clashing with the existing ChannelXxx
// PaymentChannel constants.
const (
	RCCash          ReceiptChannel = "cash"
	RCMpesa         ReceiptChannel = "mpesa"
	RCAirtelMoney   ReceiptChannel = "airtel_money"
	RCBankTransfer  ReceiptChannel = "bank_transfer"
	RCCheque        ReceiptChannel = "cheque"
	RCStandingOrder ReceiptChannel = "standing_order"
)

// IsCash reports whether this channel uses a physical till session
// (true) versus a per-channel virtual till (false).
func (c ReceiptChannel) IsCash() bool { return c == RCCash }

// ─────────── Virtual till ───────────

// VirtualTill is the per-(tenant, non-cash channel) reconciliation
// anchor. Auto-provisioned on first use; reconciles to 0 against the
// channel's external statement at EOD.
type VirtualTill struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	Channel       ReceiptChannel `json:"channel"`
	GLAccountCode string    `json:"gl_account_code"`
	DisplayName   string    `json:"display_name"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ─────────── Receipt ───────────

type Receipt struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	Serial          string          `json:"serial"`
	CounterpartyID  uuid.UUID       `json:"counterparty_id"`
	Channel         ReceiptChannel  `json:"channel"`
	ChannelRef      *string         `json:"channel_ref,omitempty"`
	ChannelAmount   decimal.Decimal `json:"channel_amount"`
	ValueDate       time.Time       `json:"value_date"`
	Narration       *string         `json:"narration,omitempty"`
	CashierUserID   uuid.UUID       `json:"cashier_user_id"`
	TillSessionID   *uuid.UUID      `json:"till_session_id,omitempty"`
	VirtualTillID   *uuid.UUID      `json:"virtual_till_id,omitempty"`
	Status          ReceiptStatus   `json:"status"`
	PDFDocumentID   *uuid.UUID      `json:"pdf_document_id,omitempty"`
	VoidedAt        *time.Time      `json:"voided_at,omitempty"`
	VoidedBy        *uuid.UUID      `json:"voided_by,omitempty"`
	VoidReason      *string         `json:"void_reason,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	PostedAt        *time.Time      `json:"posted_at,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// Convenience — populated by the handler when serving a full
	// receipt view. Not persisted.
	Lines []ReceiptLine `json:"lines,omitempty"`
}

// ─────────── Receipt line ───────────

type ReceiptLine struct {
	ID              uuid.UUID         `json:"id"`
	ReceiptID       uuid.UUID         `json:"receipt_id"`
	LineNo          int               `json:"line_no"`
	Kind            ReceiptLineKind   `json:"kind"`
	Amount          decimal.Decimal   `json:"amount"`
	TargetAccountID *uuid.UUID        `json:"target_account_id,omitempty"`
	FeeCode         *string           `json:"fee_code,omitempty"`
	Narration       *string           `json:"narration,omitempty"`
	ApprovalID      *uuid.UUID        `json:"approval_id,omitempty"`
	PostedTxnID     *uuid.UUID        `json:"posted_txn_id,omitempty"`
	Status          ReceiptLineStatus `json:"status"`
	VoidedAt        *time.Time        `json:"voided_at,omitempty"`
	VoidedBy        *uuid.UUID        `json:"voided_by,omitempty"`
	VoidReason      *string           `json:"void_reason,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	PostedAt        *time.Time        `json:"posted_at,omitempty"`
	// Phase-3.5 addition. Carries the upstream rail's authoritative
	// receipt id (Safaricom MpesaReceiptNumber, future bank-rail /
	// USSD ids). Set when the line was created by an externally
	// validated rail; nil for tellercreated rows. The collection-
	// desk approval router uses this to skip the maker-checker
	// queue for already-validated lines.
	ExternalValidationRef *string `json:"external_validation_ref,omitempty"`
}

// ─────────── Outstanding (the "what does this CP owe?" bag) ───────────

// CounterpartyOutstanding is the aggregate the Desk's
// "Suggest from outstanding" feature reads. Each section is a list
// the teller can convert to a single receipt-line click.
type CounterpartyOutstanding struct {
	LoanArrears    []LoanArrearSummary    `json:"loan_arrears"`
	UnpaidFees     []FeeDue               `json:"unpaid_fees"`
	ShareShortfall *ShareShortfallSummary `json:"share_shortfall,omitempty"`
	// TotalSuggested is what the "Pay all" CTA pre-fills.
	TotalSuggested decimal.Decimal `json:"total_suggested"`
}

type LoanArrearSummary struct {
	LoanID         uuid.UUID       `json:"loan_id"`
	LoanNo         string          `json:"loan_no"`
	ProductCode    string          `json:"product_code"`
	ArrearsAmount  decimal.Decimal `json:"arrears_amount"`
	DaysPastDue    int             `json:"days_past_due"`
	Classification string          `json:"classification"`
}

type FeeDue struct {
	FeeCode     string          `json:"fee_code"`
	Description string          `json:"description"`
	Amount      decimal.Decimal `json:"amount"`
	SourceRef   *string         `json:"source_ref,omitempty"` // application no, etc.
}

type ShareShortfallSummary struct {
	ShareAccountID  uuid.UUID       `json:"share_account_id"`
	SharesHeld      int             `json:"shares_held"`
	MinSharesPolicy int             `json:"min_shares_policy"`
	ShortfallShares int             `json:"shortfall_shares"`
	ParValue        decimal.Decimal `json:"par_value"`
	ShortfallKES    decimal.Decimal `json:"shortfall_kes"`
}
