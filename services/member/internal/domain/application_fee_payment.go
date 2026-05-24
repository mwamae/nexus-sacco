// Late-fee-capture domain types.
//
// One row per (idempotent) payment attempt against an application's
// registration fee. The denormalised fee_* columns on the parent
// membership_application are aggregates of the rows defined here —
// the handler recomputes them inside the same tx as every
// insert / void.

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Channels accepted on a late-fee payment. Mirrors the Collection
// Desk's ReceiptChannel union; kept as a free-form string here
// because the table doesn't pin it to an enum (see migration 0017
// for the rationale).
const (
	FeeChannelCash          = "cash"
	FeeChannelMpesa         = "mpesa"
	FeeChannelAirtelMoney   = "airtel_money"
	FeeChannelBankTransfer  = "bank_transfer"
	FeeChannelCheque        = "cheque"
	FeeChannelStandingOrder = "standing_order"
)

// ValidFeePaymentChannel reports whether the channel string is one
// we'd accept on a POST. Cash is the only channel allowed without a
// channel_reference.
func ValidFeePaymentChannel(s string) bool {
	switch s {
	case FeeChannelCash, FeeChannelMpesa, FeeChannelAirtelMoney,
		FeeChannelBankTransfer, FeeChannelCheque, FeeChannelStandingOrder:
		return true
	}
	return false
}

// FeeChannelRequiresReference returns true for every non-cash
// channel — the regulator wants a paper trail (M-Pesa code, cheque
// no, bank slip ref, etc.) for anything that isn't physical cash
// across the counter.
func FeeChannelRequiresReference(s string) bool {
	return s != "" && s != FeeChannelCash
}

type ApplicationFeePayment struct {
	ID               uuid.UUID        `json:"id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	ApplicationID    uuid.UUID        `json:"application_id"`
	Amount           decimal.Decimal  `json:"amount"`
	Channel          string           `json:"channel"`
	ChannelReference *string          `json:"channel_reference,omitempty"`
	ValueDate        time.Time        `json:"value_date"`
	ProofDocPath     *string          `json:"proof_doc_path,omitempty"`
	Note             *string          `json:"note,omitempty"`
	JournalEntryID   *uuid.UUID       `json:"journal_entry_id,omitempty"`
	PostedAt         *time.Time       `json:"posted_at,omitempty"`
	VoidedAt         *time.Time       `json:"voided_at,omitempty"`
	VoidReason       *string          `json:"void_reason,omitempty"`
	VoidedBy         *uuid.UUID       `json:"voided_by,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	CreatedBy        uuid.UUID        `json:"created_by"`
}

// Sentinel errors the handler maps to HTTP statuses.
var (
	ErrFeePaymentForbiddenStatus = errors.New("application status does not allow fee payments")
	ErrFeePaymentDuplicate       = errors.New("duplicate fee payment for the same channel + reference")
	ErrFeePaymentOverpay         = errors.New("payment would exceed 150% of fee due; void earlier rows or add an OVERPAY note")
	ErrFeePaymentAlreadyVoided   = errors.New("fee payment is already voided")
	ErrFeePaymentNotFound        = errors.New("fee payment not found")
)
