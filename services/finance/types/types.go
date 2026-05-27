// Shared input + output types for finance executors.
//
// Kept deliberately minimal: each executor reads what it needs from
// the DB via SELECT inside the caller's tx, so callers only need to
// pass primitive identifiers (uuid, decimal, string) + an optional
// external_validation_ref to mark Safaricom-validated postings.

package types

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Allocation is the engine-precomputed split of a loan repayment
// across the four repayment components. Sum should equal the
// inbound amount; the executor will reject inputs where it doesn't.
type Allocation struct {
	Penalty   decimal.Decimal `json:"penalty"`
	Interest  decimal.Decimal `json:"interest"`
	Principal decimal.Decimal `json:"principal"`
	Fees      decimal.Decimal `json:"fees"`
}

// Total returns the sum across components.
func (a Allocation) Total() decimal.Decimal {
	return a.Penalty.Add(a.Interest).Add(a.Principal).Add(a.Fees)
}

// RepayLoanInput is what executor.RepayLoanTx takes. The executor
// reads the loan inside the tx by ID, applies the allocation, posts
// the GL outbox row, and returns the loan_transactions id it wrote.
type RepayLoanInput struct {
	TenantID    uuid.UUID
	LoanID      uuid.UUID
	Allocation  Allocation
	Channel     string
	ChannelRef  string
	Narration   string
	ValueDate   time.Time
	InitiatedBy uuid.UUID
	// ExternalValidationRef carries the upstream rail's authoritative
	// receipt id (e.g. Safaricom MpesaReceiptNumber). When non-empty
	// the executor stamps it on the loan_transactions row + on the
	// posting_outbox source_ref, which is also what the
	// collection-desk approval router checks to skip the approval
	// gate downstream.
	ExternalValidationRef string
}

// DepositInput parameters for executor.DepositTx.
type DepositInput struct {
	TenantID    uuid.UUID
	AccountID   uuid.UUID
	Amount      decimal.Decimal
	Channel     string
	ChannelRef  string
	Narration   string
	ValueDate   time.Time
	InitiatedBy uuid.UUID
	// ExternalValidationRef — see RepayLoanInput.
	ExternalValidationRef string
}

// PostOpeningDepositInput parameters for executor.PostOpeningDepositTx.
//
// Distinct from DepositInput because the opening flow has different
// invariants: the account row was just created (zero balance), the
// channel is optional (BOSA openings from application activation
// don't carry one — Channel="" routes to the internal-transfer GL
// shape), and a fixed liability code can be passed by the caller to
// override the per-product default (the application path knows the
// segment without re-loading the product).
type PostOpeningDepositInput struct {
	TenantID    uuid.UUID
	AccountID   uuid.UUID
	// CounterpartyID + ProductID stamped on the deposit_transactions
	// row. CounterpartyID is required; ProductID drives the default
	// liability-code lookup when LiabilityAccountCode is empty.
	CounterpartyID uuid.UUID
	ProductID      uuid.UUID
	Amount         decimal.Decimal
	// Channel is optional. Empty means "no external cash leg" — the
	// JE debits the internal-transfer suspense (1099) instead of a
	// channel cash account; the receipt skip is the caller's
	// responsibility.
	Channel     string
	ChannelRef  string
	Narration   string
	ValueDate   time.Time
	InitiatedBy uuid.UUID
	// LiabilityAccountCode lets the application path pass the
	// already-resolved BOSA code (2050) without a product lookup.
	// When empty, the executor reads deposit_products.code → liability
	// via the same map savings handlers use.
	LiabilityAccountCode string
}

// PostFeeInput parameters for executor.PostFeeTx.
type PostFeeInput struct {
	TenantID     uuid.UUID
	FeeCode      string
	Amount       decimal.Decimal
	Channel      string
	ChannelRef   string
	Narration    string
	ValueDate    time.Time
	InitiatedBy  uuid.UUID
	// Receipt context — the fee post needs a "from where" GL debit
	// account; the executor maps this via the till / virtual-till
	// row the caller identifies.
	TillSessionID    *uuid.UUID
	VirtualTillID    *uuid.UUID
	// ExternalValidationRef — see RepayLoanInput.
	ExternalValidationRef string
}

// Result is what every executor returns: the id of whatever
// transaction-record they wrote (loan_transactions.id,
// deposit_transactions.id, or the synthetic posting source_ref for
// fee posts), plus the GL outbox id when one was written.
type Result struct {
	TxnID    uuid.UUID
	OutboxID uuid.UUID
}
