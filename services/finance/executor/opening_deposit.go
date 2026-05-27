// Opening-deposit executor. Posts the deposit_transactions row +
// queues the GL outbox for an opening contribution on a
// freshly-created deposit_accounts row.
//
// Two paths route through here:
//
//   1. savings's Open handler — when the operator opens an account
//      with OpeningDeposit > 0 and the toggle is OFF. (Toggle-ON
//      flows through savings's executeDepositInlineTx with the full
//      approval/receipt/GL path; this executor is the GL-only
//      under-the-hood for that path too once the approval fires.)
//
//   2. member's application_store.activateIndividualTx — BOSA
//      opening at member-onboarding. The application approval IS
//      the deposit approval; no per-deposit approval, no receipt
//      (per the BOSA-opening choice in the spec — the application
//      fee process already issued its receipt; this is the
//      accounting movement).
//
// What this function does NOT do:
//   - Create the account. Caller wrote the account row first
//     (savings.DepositStore.CreateAccountTx).
//   - Queue an approval. Caller (savings Open handler) decides.
//   - Write a receipt. Same — caller decides per source rules.
//
// What it DOES:
//   - Validate AccountID points at an active deposit_accounts row.
//   - Write the deposit_transactions row (txn_type='opening_balance').
//   - Bump the account's cached current_balance / available_balance.
//   - Queue the GL outbox post: DR <channel cash> / CR <liability>.
//
// Idempotency: the outbox post keys on
//   source_module = "savings.deposits.opening"
//   source_ref    = <deposit_transactions.id>
// Accounting's dedup on (source_module, source_ref) makes re-running
// the executor on the same outbox row a no-op.

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/finance/posting"
	"github.com/nexussacco/finance/types"
)

// PostOpeningDepositTx writes the opening_balance txn + GL outbox.
// Returns the txn id + outbox id. Caller commits the surrounding tx.
func PostOpeningDepositTx(ctx context.Context, tx pgx.Tx, in types.PostOpeningDepositInput) (*types.Result, error) {
	if in.AccountID == uuid.Nil || in.TenantID == uuid.Nil || in.CounterpartyID == uuid.Nil {
		return nil, errors.New("finance/executor: tenant_id, account_id, counterparty_id required")
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("finance/executor: opening deposit amount must be > 0")
	}

	// 1. Lock the account; assert it's active + zero-balance (the
	//    "opening" invariant — a follow-up deposit on a non-zero
	//    account is the standard Deposit path, not this one).
	var (
		productID      uuid.UUID
		currentBalance decimal.Decimal
		status         string
	)
	if err := tx.QueryRow(ctx, `
		SELECT product_id, status::text, current_balance
		  FROM deposit_accounts
		 WHERE id = $1
		   FOR UPDATE
	`, in.AccountID).Scan(&productID, &status, &currentBalance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("finance/executor: deposit account %s not found", in.AccountID)
		}
		return nil, fmt.Errorf("read account: %w", err)
	}
	if status != "active" {
		return nil, fmt.Errorf("finance/executor: account status %q does not accept openings", status)
	}
	if !currentBalance.IsZero() {
		return nil, fmt.Errorf("finance/executor: account %s has non-zero balance (%s); use the standard Deposit path",
			in.AccountID, currentBalance.String())
	}

	// 2. Cache balance forward + apply with CAS.
	newBalance := in.Amount
	tag, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET current_balance   = $2,
		       available_balance = $2,
		       last_deposit_at   = now(),
		       last_activity_at  = now()
		 WHERE id = $1 AND current_balance = 0
	`, in.AccountID, newBalance)
	if err != nil {
		return nil, fmt.Errorf("update balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("finance/executor: concurrent balance update on %s", in.AccountID)
	}

	// 3. Write the deposit_transactions row.
	narr := in.Narration
	if narr == "" {
		narr = "Opening deposit"
	}
	valueDate := in.ValueDate
	if valueDate.IsZero() {
		valueDate = time.Now().UTC()
	}
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
			balance_after, initiated_by
		) VALUES (
			$1, $2, $3, $4, 'opening_balance', $5,
			$6, NULLIF($7,'')::deposit_channel, NULLIF($8,''), $9,
			$10, $11
		)
		RETURNING id
	`, in.TenantID, in.AccountID, in.CounterpartyID, txnNo, in.Amount,
		valueDate, in.Channel, in.ChannelRef, narr,
		newBalance, in.InitiatedBy,
	).Scan(&txnID); err != nil {
		return nil, fmt.Errorf("insert deposit_transactions: %w", err)
	}

	// 4. Resolve the liability code (caller override wins) + the
	//    channel-cash code (empty channel → 1099 internal suspense
	//    for application-activation BOSA openings that don't have a
	//    teller event).
	liab := in.LiabilityAccountCode
	if liab == "" {
		liab = openingLiabilityCodeForProduct(ctx, tx, productID)
	}
	cashAcct := openingChannelCashAccount(in.Channel)

	if _, err := posting.PostTx(ctx, tx, posting.Input{
		TenantID:     in.TenantID,
		EntryDate:    valueDate,
		ValueDate:    valueDate,
		SourceModule: "savings.deposits.opening",
		SourceRef:    txnID.String(),
		Narration:    narr,
		Lines: []posting.Line{
			{AccountCode: cashAcct, Debit: in.Amount, Narration: "Opening cash leg"},
			{AccountCode: liab, Credit: in.Amount, Narration: "Member savings credited (opening)"},
		},
	}); err != nil {
		return nil, fmt.Errorf("posting outbox: %w", err)
	}
	return &types.Result{TxnID: txnID}, nil
}

// openingChannelCashAccount maps the channel string to the debit-side
// CoA code. "" → 1099 (Internal Transfer Suspense) for openings
// without a teller event (application-activation BOSA). Matches
// services/savings/internal/postingops's per-channel map.
func openingChannelCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "":
		return "1099"
	case "mpesa":
		return "1030"
	case "airtel_money":
		return "1040"
	case "bank_transfer", "standing_order", "direct_debit", "payroll":
		return "1020"
	case "internal":
		return "2000"
	}
	return "1000"
}

// openingLiabilityCodeForProduct reads deposit_products.{segment,
// product_type} and applies the same default ladder savings uses.
// Falls back to 2000 (ordinary savings) on any read error so the GL
// post still commits — operators eyeball the journal and route the
// suspense balance via a manual JE if needed.
func openingLiabilityCodeForProduct(ctx context.Context, tx pgx.Tx, productID uuid.UUID) string {
	var (
		segment     string
		productType string
	)
	if err := tx.QueryRow(ctx, `
		SELECT segment::text, product_type::text
		  FROM deposit_products WHERE id = $1
	`, productID).Scan(&segment, &productType); err != nil {
		return "2000"
	}
	if segment == "bosa" {
		return "2050"
	}
	switch productType {
	case "ordinary":
		return "2000"
	case "holiday":
		return "2010"
	case "emergency":
		return "2020"
	case "goal":
		return "2030"
	case "junior":
		return "2040"
	case "fixed":
		return "2100"
	case "member_deposit":
		return "2050"
	}
	return "2000"
}
