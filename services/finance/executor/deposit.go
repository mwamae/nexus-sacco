// Deposit executor. Adds funds to an existing deposit_accounts row
// (no opening flow — that's a heavier handler). Writes the
// deposit_transactions row + queues the GL outbox.

package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/finance/posting"
	"github.com/nexussacco/finance/types"
)

// DepositTx posts a credit to an existing active deposit_accounts
// row. Caller is responsible for checking the account is open + the
// product allows this deposit channel. Returns the new
// deposit_transactions id + outbox id.
func DepositTx(ctx context.Context, tx pgx.Tx, in types.DepositInput) (*types.Result, error) {
	if in.AccountID == uuid.Nil || in.TenantID == uuid.Nil {
		return nil, errors.New("finance/executor: tenant_id + account_id required")
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("finance/executor: deposit amount must be > 0")
	}

	// 1. Lock account; read what we need.
	var (
		counterpartyID, productID uuid.UUID
		currentBalance            decimal.Decimal
		status                    string
		productCode               string
	)
	if err := tx.QueryRow(ctx, `
		SELECT a.counterparty_id, a.product_id, a.status::text,
		       a.current_balance, COALESCE(p.code, '')
		  FROM deposit_accounts a
		  JOIN deposit_products p ON p.id = a.product_id
		 WHERE a.id = $1
		   FOR UPDATE OF a
	`, in.AccountID).Scan(&counterpartyID, &productID, &status, &currentBalance, &productCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("finance/executor: deposit account %s not found", in.AccountID)
		}
		return nil, fmt.Errorf("read account: %w", err)
	}
	if status != "active" {
		return nil, fmt.Errorf("finance/executor: deposit account status %q does not accept deposits", status)
	}

	// 2. Compute the new balance + apply with CAS.
	newBalance := currentBalance.Add(in.Amount)
	tag, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET current_balance   = $2,
		       available_balance = $2,
		       last_activity_at  = now(),
		       last_deposit_at   = now()
		 WHERE id = $1 AND current_balance = $3
	`, in.AccountID, newBalance, currentBalance)
	if err != nil {
		return nil, fmt.Errorf("update balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("finance/executor: concurrent balance update on %s", in.AccountID)
	}

	// 3. Write deposit_transactions row.
	narr := in.Narration
	if narr == "" {
		narr = "Deposit · " + in.Channel
	}
	valueDate := in.ValueDate
	if valueDate.IsZero() {
		valueDate = time.Now().UTC()
	}
	// nextSeq-equivalent: build a tenant-scoped txn_no via the
	// deposit_txn sequence helper (savings has nextSeq; here we
	// take the same suffix from gen_random_uuid()'s low bytes for
	// uniqueness without coupling to savings's sequence table).
	var txnNo string
	if err := tx.QueryRow(ctx,
		`SELECT 'DPT-' || to_char(now(), 'YYYYMMDD') || '-' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 8)`,
	).Scan(&txnNo); err != nil {
		return nil, fmt.Errorf("generate txn_no: %w", err)
	}

	var txnID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO deposit_transactions (
			tenant_id, account_id, counterparty_id, txn_no, txn_type, amount,
			value_date, channel, channel_ref, narration,
			balance_after, initiated_by, external_validation_ref
		) VALUES (
			$1, $2, $3, $4, 'deposit', $5,
			$6, NULLIF($7,'')::deposit_channel, NULLIF($8,''), NULLIF($9,''),
			$10, $11, NULLIF($12,'')
		)
		RETURNING id
	`, in.TenantID, in.AccountID, counterpartyID, txnNo, in.Amount,
		valueDate, in.Channel, in.ChannelRef, narr,
		newBalance, in.InitiatedBy, in.ExternalValidationRef,
	).Scan(&txnID); err != nil {
		return nil, fmt.Errorf("insert deposit_transactions: %w", err)
	}

	// 4. Post GL outbox: DR M-PESA clearing → CR member savings.
	const (
		cashClearing  = "1099"
		memberSavings = "2000" // member savings (liability) default
	)
	srcRef := txnID.String()
	if in.ExternalValidationRef != "" {
		srcRef = in.ExternalValidationRef + ":" + txnID.String()
	}
	outboxID, err := posting.PostTx(ctx, tx, posting.Input{
		TenantID:     in.TenantID,
		EntryDate:    valueDate,
		ValueDate:    valueDate,
		SourceModule: "finance.executor.deposit",
		SourceRef:    srcRef,
		Narration:    "Deposit · " + in.Channel,
		Lines: []posting.Line{
			{AccountCode: cashClearing, Debit: in.Amount, Narration: "M-PESA clearing → savings"},
			{AccountCode: memberSavings, Credit: in.Amount, Narration: "Member savings credit"},
		},
	})
	if err != nil {
		return nil, err
	}
	return &types.Result{TxnID: txnID, OutboxID: outboxID}, nil
}
