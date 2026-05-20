// Share-ledger domain types + validation helpers.

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Transaction kinds + channels ───────────

type ShareTxnType string

const (
	TxnPurchase    ShareTxnType = "purchase"
	TxnTransferIn  ShareTxnType = "transfer_in"
	TxnTransferOut ShareTxnType = "transfer_out"
	TxnRedemption  ShareTxnType = "redemption"
	TxnAdjustment  ShareTxnType = "adjustment"
	TxnBonusIssue  ShareTxnType = "bonus_issue"
)

func (t ShareTxnType) Valid() bool {
	switch t {
	case TxnPurchase, TxnTransferIn, TxnTransferOut, TxnRedemption, TxnAdjustment, TxnBonusIssue:
		return true
	}
	return false
}

// SignedDelta returns +1 for credit transactions, -1 for debit.
func (t ShareTxnType) Sign() int {
	switch t {
	case TxnPurchase, TxnTransferIn, TxnBonusIssue:
		return +1
	case TxnTransferOut, TxnRedemption:
		return -1
	default:
		return 0 // adjustment: caller supplies signed shares_delta
	}
}

type PaymentChannel string

const (
	ChannelCash          PaymentChannel = "cash"
	ChannelMpesa         PaymentChannel = "mpesa"
	ChannelAirtelMoney   PaymentChannel = "airtel_money"
	ChannelBankTransfer  PaymentChannel = "bank_transfer"
	ChannelPayroll       PaymentChannel = "payroll"
	ChannelStandingOrder PaymentChannel = "standing_order"
	ChannelInternal      PaymentChannel = "internal"
)

func (c PaymentChannel) Valid() bool {
	switch c {
	case ChannelCash, ChannelMpesa, ChannelAirtelMoney, ChannelBankTransfer,
		ChannelPayroll, ChannelStandingOrder, ChannelInternal:
		return true
	}
	return false
}

// ─────────── Entities ───────────

type AccountStatus string

const (
	AccountActive AccountStatus = "active"
	AccountClosed AccountStatus = "closed"
)

type ShareAccount struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	MemberID        uuid.UUID       `json:"member_id"`
	AccountNo       string          `json:"account_no"`
	Status          AccountStatus   `json:"status"`
	SharesHeld      int             `json:"shares_held"`
	SharesPledged   int             `json:"shares_pledged"`
	SharesAvailable int             `json:"shares_available"` // derived: held - pledged
	ParValueAtOpen  decimal.Decimal `json:"par_value_at_open"`
	TotalValue      decimal.Decimal `json:"total_value"` // shares_held * par_value (current policy)
	FirstPurchaseAt *time.Time      `json:"first_purchase_at,omitempty"`
	ClosedAt        *time.Time      `json:"closed_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type ShareTransaction struct {
	ID                    uuid.UUID       `json:"id"`
	TenantID              uuid.UUID       `json:"tenant_id"`
	AccountID             uuid.UUID       `json:"account_id"`
	MemberID              uuid.UUID       `json:"member_id"`
	TxnNo                 string          `json:"txn_no"`
	TxnType               ShareTxnType    `json:"txn_type"`
	SharesDelta           int             `json:"shares_delta"`
	ParValueAtTxn         decimal.Decimal `json:"par_value_at_txn"`
	Amount                decimal.Decimal `json:"amount"`
	PaymentChannel        *PaymentChannel `json:"payment_channel,omitempty"`
	PaymentRef            *string         `json:"payment_ref,omitempty"`
	Narration             *string         `json:"narration,omitempty"`
	CounterpartyAccountID *uuid.UUID      `json:"counterparty_account_id,omitempty"`
	CounterpartyTxnID     *uuid.UUID      `json:"counterparty_txn_id,omitempty"`
	BalanceAfterShares    int             `json:"balance_after_shares"`
	BalanceAfterAmount    decimal.Decimal `json:"balance_after_amount"`
	InitiatedBy           uuid.UUID       `json:"initiated_by"`
	AuthorizedBy          *uuid.UUID      `json:"authorized_by,omitempty"`
	AuthorizationReason   *string         `json:"authorization_reason,omitempty"`
	PostedAt              time.Time       `json:"posted_at"`
	CreatedAt             time.Time       `json:"created_at"`
}

type LienStatus string

const (
	LienActive   LienStatus = "active"
	LienReleased LienStatus = "released"
)

type ShareLien struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	AccountID      uuid.UUID  `json:"account_id"`
	SharesPledged  int        `json:"shares_pledged"`
	Reason         string     `json:"reason"`
	ReferenceKind  *string    `json:"reference_kind,omitempty"`
	ReferenceID    *string    `json:"reference_id,omitempty"`
	Status         LienStatus `json:"status"`
	PlacedBy       uuid.UUID  `json:"placed_by"`
	PlacedAt       time.Time  `json:"placed_at"`
	ReleasedBy     *uuid.UUID `json:"released_by,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleasedReason *string    `json:"released_reason,omitempty"`
}

type ShareCertificate struct {
	ID               uuid.UUID       `json:"id"`
	TenantID         uuid.UUID       `json:"tenant_id"`
	AccountID        uuid.UUID       `json:"account_id"`
	MemberID         uuid.UUID       `json:"member_id"`
	CertificateNo    string          `json:"certificate_no"`
	SharesCovered    int             `json:"shares_covered"`
	ParValueAtIssue  decimal.Decimal `json:"par_value_at_issue"`
	TotalValue       decimal.Decimal `json:"total_value"`
	IssuedAt         time.Time       `json:"issued_at"`
	RetiredAt        *time.Time      `json:"retired_at,omitempty"`
	SupersedesID     *uuid.UUID      `json:"supersedes_id,omitempty"`
	IssuedBy         uuid.UUID       `json:"issued_by"`
}

// ─────────── Validation ───────────

var (
	ErrInvalidQuantity    = errors.New("share quantity must be a positive integer")
	ErrInsufficientShares = errors.New("insufficient shares available (some may be pledged)")
	ErrLienBlocksAction   = errors.New("operation blocked by an active lien on these shares")
	ErrBelowMinHolding    = errors.New("operation would drop member below the minimum-shares-required threshold")
	ErrExceedsMaxHolding  = errors.New("operation would exceed the configured maximum share holding cap")
	ErrAccountClosed      = errors.New("share account is closed")
	ErrSameMemberTransfer = errors.New("cannot transfer shares to the same member")
	ErrMemberIneligible   = errors.New("member is not eligible for share operations (status must be active or pending)")
)

// AvailableShares returns held - pledged (the portion the member can move).
func AvailableShares(a *ShareAccount) int {
	if a == nil {
		return 0
	}
	return a.SharesHeld - a.SharesPledged
}
