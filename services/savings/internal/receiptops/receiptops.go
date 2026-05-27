// Package receiptops centralises "write a receipt + N lines, atomic
// with the surrounding tx" so the Collection Desk, the inline money
// panels (share Buy / Deposit / Withdraw / Repay), and future
// programmatic posters (the M-PESA C2B distributor, payroll import)
// share one primitive. Before this seam each path duplicated the
// channel-to-till resolution + the serial allocation; in practice
// only the Collection Desk had been wired, which is why inline-panel
// transactions were invisible to "Today's receipts".
//
// What this package does NOT do:
//
//   - Post to the GL. That's postingops's job.
//   - Queue approvals. The caller decides whether to AttachApprovalTx
//     on each returned line.
//   - Validate counterparty / amount semantics. The caller's domain
//     logic ran first; we just persist the receipt envelope.
//
// Channel rules:
//
//   - cash       → caller MUST pass TillSessionID (a real till the
//                  cashier has opened). receiptops does not synthesise
//                  a till session. The inline money panels reject cash
//                  upstream and deep-link to Collection Desk.
//   - everything else → receiptops calls VirtualTillStore.EnsureForChannelTx
//                  to resolve / lazy-create the per-tenant virtual till.

package receiptops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/store"
)

// ErrCashRequiresTill is returned when the caller hands cash channel
// without a till session id. The inline panels translate this into a
// 412 with a deep-link to Collection Desk.
var ErrCashRequiresTill = errors.New("cash channel requires an open till session — use Collection Desk")

// ErrUnsupportedChannel is returned when the channel isn't representable
// on the receipts table (e.g. 'internal' or 'payroll' — those channels
// don't generate teller-style receipts). Callers should skip the
// receipt write and continue with just the subledger + GL post.
var ErrUnsupportedChannel = errors.New("channel cannot be persisted as a receipt")

// Deps is the stores receiptops needs. Constructed once per service
// and stashed on the handler — see ReceiptDeps wiring in main.go.
type Deps struct {
	Receipts     *store.ReceiptStore
	VirtualTills *store.VirtualTillStore
}

// WriteInput is everything the caller knows about the receipt. The
// caller is responsible for ensuring the Lines sum to ChannelAmount
// — the underlying store enforces this and returns an error if not.
type WriteInput struct {
	TenantID       uuid.UUID
	CounterpartyID uuid.UUID
	CashierUserID  uuid.UUID
	Channel        domain.ReceiptChannel
	ChannelRef     string // required for non-cash; ignored for cash
	ChannelAmount  decimal.Decimal
	ValueDate      time.Time // zero → now()
	Narration      string
	// Source identifies the writer for diagnostics. Examples:
	//   "inline_share_purchase" · "inline_deposit" · "inline_withdrawal"
	//   "inline_loan_repayment" · "collection_desk" · "mpesa_c2b_distributor"
	// Surfaced into the narration suffix when narration is empty so
	// operators reading /collect/receipts can see what wrote the row.
	Source string
	// TillSessionID is required when Channel==cash and ignored
	// otherwise. The caller (Collection Desk) resolves the open till
	// session for the cashier; inline panels reject cash upstream so
	// they never reach this path with cash.
	TillSessionID *uuid.UUID
	// HeaderStatus / LineStatus default to 'draft' / 'pending' (the
	// Collection Desk approval-loop pattern). Inline-money panels
	// set them to 'posted' so the receipt is posted-on-create and
	// the user sees it immediately on /collect/receipts.
	HeaderStatus domain.ReceiptStatus
	Lines        []LineInput
}

// LineInput is one line on the receipt. Kind drives which sub-table
// the line's posted txn id eventually points at — share_transactions,
// deposit_transactions, loan_payments, or a synthetic fee txn.
type LineInput struct {
	Kind            domain.ReceiptLineKind
	Amount          decimal.Decimal
	TargetAccountID *uuid.UUID
	FeeCode         string
	Narration       string
	// Status defaults to 'pending'. Inline panels set 'posted' +
	// populate PostedTxnID at write time; Collection Desk leaves
	// these empty and the approval loop fills them later via
	// ReceiptStore.MarkLinePostedTx.
	Status      domain.ReceiptLineStatus
	PostedTxnID *uuid.UUID
}

// WriteTx persists the receipt header + N lines inside the caller's
// tx + returns the populated Receipt (with its Lines slice
// populated). Failures roll back the surrounding tx, so a receipt
// write that loses to a duplicate channel_ref doesn't leave the
// subledger half-written.
//
// Idempotency: the receipts.channel_ref_unique constraint catches
// duplicate non-cash deliveries by (tenant, channel, channel_ref).
// Callers should pass deterministic ChannelRef values (Daraja
// receipt id, bank transfer reference, etc.) — a random uuid per
// call defeats the dedup.
func WriteTx(ctx context.Context, tx pgx.Tx, deps Deps, in WriteInput) (*domain.Receipt, error) {
	if in.Channel == "" {
		return nil, fmt.Errorf("receiptops: channel is required")
	}
	if in.ChannelAmount.LessThanOrEqual(decimal.Zero) {
		return nil, fmt.Errorf("receiptops: channel_amount must be positive")
	}
	if len(in.Lines) == 0 {
		return nil, fmt.Errorf("receiptops: at least one line is required")
	}

	var (
		tillSessionID *uuid.UUID
		virtualTillID *uuid.UUID
		tillCode      string
	)
	switch in.Channel {
	case domain.RCCash:
		if in.TillSessionID == nil {
			return nil, ErrCashRequiresTill
		}
		tillSessionID = in.TillSessionID
		tillCode = "CASH"
	default:
		vt, err := deps.VirtualTills.EnsureForChannelTx(ctx, tx, in.TenantID, in.Channel)
		if err != nil {
			// Anything not in the per-channel defaults map (internal,
			// payroll, direct_debit) is not representable on the
			// receipts table today; the caller should skip the
			// receipt write entirely for those rails.
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedChannel, err)
		}
		virtualTillID = &vt.ID
		tillCode = string(in.Channel)
	}

	valueDate := in.ValueDate
	if valueDate.IsZero() {
		valueDate = time.Now().UTC()
	}

	var channelRef *string
	if in.ChannelRef != "" {
		s := in.ChannelRef
		channelRef = &s
	}
	narrationStr := in.Narration
	if narrationStr == "" && in.Source != "" {
		narrationStr = "[" + in.Source + "]"
	}
	var narration *string
	if narrationStr != "" {
		narration = &narrationStr
	}

	lines := make([]store.CreateReceiptLineInput, 0, len(in.Lines))
	for i, l := range in.Lines {
		var n *string
		if l.Narration != "" {
			s := l.Narration
			n = &s
		}
		var fee *string
		if l.FeeCode != "" {
			s := l.FeeCode
			fee = &s
		}
		lines = append(lines, store.CreateReceiptLineInput{
			LineNo:          i + 1,
			Kind:            l.Kind,
			Amount:          l.Amount,
			TargetAccountID: l.TargetAccountID,
			FeeCode:         fee,
			Narration:       n,
			Status:          l.Status,
			PostedTxnID:     l.PostedTxnID,
		})
	}

	rec, err := deps.Receipts.CreateTx(ctx, tx, store.CreateReceiptInput{
		TenantID:       in.TenantID,
		CounterpartyID: in.CounterpartyID,
		Channel:        in.Channel,
		ChannelRef:     channelRef,
		ChannelAmount:  in.ChannelAmount,
		ValueDate:      valueDate,
		Narration:      narration,
		CashierUserID:  in.CashierUserID,
		TillSessionID:  tillSessionID,
		VirtualTillID:  virtualTillID,
		TillCode:       tillCode,
		HeaderStatus:   in.HeaderStatus,
		Lines:          lines,
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}
