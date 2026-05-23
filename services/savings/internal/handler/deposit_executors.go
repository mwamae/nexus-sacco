// Executors for the three cash-touching deposit actions (deposit,
// withdrawal, transfer). The original Deposit/Withdraw/Transfer handlers
// are thin wrappers that either run these directly (toggle off) or queue
// a pending_approvals row (toggle on).
//
// Each executor takes a typed payload + maker user-id, performs the
// same validations + ledger writes as the original inline path, and
// returns the resulting transaction id so it can be recorded on the
// pending row when this is being executed during an approval.

package handler

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

// ─────────── Payloads ───────────

type DepositPayload struct {
	AccountID            uuid.UUID             `json:"account_id"`
	Amount               decimal.Decimal       `json:"amount"`
	Channel              domain.DepositChannel `json:"channel"`
	ChannelRef           string                `json:"channel_ref"`
	Narration            string                `json:"narration"`
	ValueDate            string                `json:"value_date"`
	BypassDuplicateCheck bool                  `json:"bypass_duplicate_check"`
}

type WithdrawalPayload struct {
	AccountID  uuid.UUID             `json:"account_id"`
	Amount     decimal.Decimal       `json:"amount"`
	Channel    domain.DepositChannel `json:"channel"`
	ChannelRef string                `json:"channel_ref"`
	Narration  string                `json:"narration"`
	Reason     string                `json:"reason"`
}

type DepTransferPayload struct {
	FromAccountID uuid.UUID       `json:"from_account_id"`
	ToAccountID   uuid.UUID       `json:"to_account_id"`
	Amount        decimal.Decimal `json:"amount"`
	Narration     string          `json:"narration"`
}

// ─────────── Result shapes ───────────

// DepositReversePayload mirrors LoanReversePayload — used by the
// Collection Desk's per-line void to reverse a posted savings_deposit
// line. The original deposit txn id is what's on receipt_lines.posted_txn_id.
type DepositReversePayload struct {
	TxnID  uuid.UUID `json:"txn_id"`
	Reason string    `json:"reason"`
}

type DepositReverseResult struct {
	Reversal domain.DepositTransaction `json:"reversal"`
	Account  domain.DepositAccount     `json:"account"`
}

// ExecuteDepositReverseTx posts the inverse withdrawal + back-links
// the original. Surfaces store.ErrAlreadyReversed when the original
// was already reversed (the handler maps it to 409).
func (h *DepositHandler) ExecuteDepositReverseTx(
	ctx context.Context, tx pgx.Tx,
	p DepositReversePayload, makerID uuid.UUID,
) (*DepositReverseResult, error) {
	rev, err := h.Deposits.ReverseDepositTx(ctx, tx, p.TxnID, p.Reason, makerID)
	if err != nil {
		return nil, err
	}
	acct, err := h.Deposits.GetAccountTx(ctx, tx, rev.AccountID)
	if err != nil {
		return nil, err
	}
	return &DepositReverseResult{Reversal: *rev, Account: *acct}, nil
}

type DepositResult struct {
	Transaction domain.DepositTransaction `json:"transaction"`
	Account     domain.DepositAccount     `json:"account"`
}

type WithdrawalResult struct {
	Transaction      domain.DepositTransaction `json:"transaction"`
	Account          domain.DepositAccount     `json:"account"`
	RequiresApproval bool                      `json:"requires_approval"`
}

type DepTransferResult struct {
	From DepositResult `json:"from"`
	To   DepositResult `json:"to"`
}

// ─────────── Executors ───────────

// ExecuteDepositTx posts a deposit transaction. Caller guarantees:
//   - amount is positive
//   - channel is valid
// (i.e. handler-side validation already passed)
func (h *DepositHandler) ExecuteDepositTx(
	ctx context.Context, tx pgx.Tx,
	p DepositPayload, makerID uuid.UUID,
) (*DepositResult, error) {
	var vd *time.Time
	if p.ValueDate != "" {
		d, err := time.Parse("2006-01-02", p.ValueDate)
		if err != nil {
			return nil, fmt.Errorf("value_date must be YYYY-MM-DD")
		}
		vd = &d
	}
	product, acct, _, err := h.loadProductAccount(ctx, tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	if err := domain.EvaluateDeposit(product, acct, p.Amount); err != nil {
		return nil, err
	}
	if p.ChannelRef != "" && !p.BypassDuplicateCheck {
		dup, err := h.Deposits.DuplicateExistsTx(ctx, tx, p.AccountID, p.Amount, p.ChannelRef, h.lookback())
		if err != nil {
			return nil, err
		}
		if dup {
			return nil, domain.ErrDuplicateTransaction
		}
	}
	ch := p.Channel
	ref := strNilIfEmpty(p.ChannelRef)
	narr := strNilIfEmpty(p.Narration)
	txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:     acct,
		TxnType:     domain.TxnDeposit,
		Amount:      p.Amount,
		ValueDate:   vd,
		Channel:     &ch,
		ChannelRef:  ref,
		Narration:   narr,
		InitiatedBy: makerID,
	})
	if err != nil {
		return nil, err
	}
	updated, err := h.Deposits.GetAccountTx(ctx, tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	_ = h.Counterparties.TouchActivityTx(ctx, tx, acct.CounterpartyID)
	return &DepositResult{Transaction: *txn, Account: *updated}, nil
}

func (h *DepositHandler) ExecuteWithdrawalTx(
	ctx context.Context, tx pgx.Tx,
	p WithdrawalPayload, makerID uuid.UUID,
) (*WithdrawalResult, error) {
	product, acct, _, err := h.loadProductAccount(ctx, tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	monthly, err := h.Deposits.WithdrawalCountThisMonthTx(ctx, tx, p.AccountID, time.Now())
	if err != nil {
		return nil, err
	}
	if err := domain.EvaluateWithdrawal(product, acct, p.Amount, time.Now(), monthly); err != nil {
		return nil, err
	}
	requires := false
	if domain.IsLargeWithdrawal(product, p.Amount) {
		if p.Reason == "" {
			return nil, errors.New("withdrawal exceeds the product's large-withdrawal threshold; reason is required")
		}
		requires = true
	}
	ch := p.Channel
	ref := strNilIfEmpty(p.ChannelRef)
	narr := strNilIfEmpty(p.Narration)
	reason := strNilIfEmpty(p.Reason)
	txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:             acct,
		TxnType:             domain.TxnWithdrawal,
		Amount:              p.Amount,
		Channel:             &ch,
		ChannelRef:          ref,
		Narration:           narr,
		InitiatedBy:         makerID,
		AuthorizedBy:        &makerID,
		AuthorizationReason: reason,
	})
	if err != nil {
		return nil, err
	}
	_ = h.Deposits.ClearWithdrawalNoticeTx(ctx, tx, p.AccountID)
	updated, err := h.Deposits.GetAccountTx(ctx, tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	_ = h.Counterparties.TouchActivityTx(ctx, tx, acct.CounterpartyID)
	return &WithdrawalResult{Transaction: *txn, Account: *updated, RequiresApproval: requires}, nil
}

func (h *DepositHandler) ExecuteDepTransferTx(
	ctx context.Context, tx pgx.Tx,
	p DepTransferPayload, makerID uuid.UUID,
) (*DepTransferResult, error) {
	fromProduct, fromAcct, fromMember, err := h.loadProductAccount(ctx, tx, p.FromAccountID)
	if err != nil {
		return nil, err
	}
	toProduct, toAcct, toMember, err := h.loadProductAccount(ctx, tx, p.ToAccountID)
	if err != nil {
		return nil, err
	}
	if fromMember.ID != toMember.ID {
		return nil, errors.New("transfer endpoints are restricted to the same member's accounts")
	}
	monthly, err := h.Deposits.WithdrawalCountThisMonthTx(ctx, tx, p.FromAccountID, time.Now())
	if err != nil {
		return nil, err
	}
	if err := domain.EvaluateWithdrawal(fromProduct, fromAcct, p.Amount, time.Now(), monthly); err != nil {
		return nil, err
	}
	if err := domain.EvaluateDeposit(toProduct, toAcct, p.Amount); err != nil {
		return nil, err
	}
	internal := domain.DepChannelInternal
	narration := p.Narration
	if narration == "" {
		narration = "Transfer between own deposit accounts"
	}
	narrPtr := &narration
	outTxn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:             fromAcct,
		TxnType:             domain.TxnDepTransferOut,
		Amount:              p.Amount,
		Channel:             &internal,
		Narration:           narrPtr,
		CounterpartyAccount: toAcct,
		InitiatedBy:         makerID,
	})
	if err != nil {
		return nil, err
	}
	fromAcct, err = h.Deposits.GetAccountTx(ctx, tx, p.FromAccountID)
	if err != nil {
		return nil, err
	}
	inTxn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:             toAcct,
		TxnType:             domain.TxnDepTransferIn,
		Amount:              p.Amount,
		Channel:             &internal,
		Narration:           narrPtr,
		CounterpartyAccount: fromAcct,
		CounterpartyTxnID:   &outTxn.ID,
		InitiatedBy:         makerID,
	})
	if err != nil {
		return nil, err
	}
	if err := h.Deposits.LinkCounterpartyTxnTx(ctx, tx, outTxn.ID, inTxn.ID); err != nil {
		return nil, err
	}
	toAcct, err = h.Deposits.GetAccountTx(ctx, tx, p.ToAccountID)
	if err != nil {
		return nil, err
	}
	return &DepTransferResult{
		From: DepositResult{Transaction: *outTxn, Account: *fromAcct},
		To:   DepositResult{Transaction: *inTxn, Account: *toAcct},
	}, nil
}
