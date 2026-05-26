// Read-only balance lookups the engine uses to build a Plan.
//
// Phase 3 (defer-apply): only the loan-component lookups are wired
// against real tables. fees_due / bosa_top_up / fosa_top_up return
// zero balances — they need fees-due + deposit-target tables that
// don't exist yet. The engine still builds a correct Plan because
// the last waterfall leg picks up any leftover.
//
// Phase 3.5 wires these against the extracted finance package and
// flips the zero stubs to real lookups.

package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type DistributionBalances struct {
	pool *pgxpool.Pool
}

func NewDistributionBalances(pool *pgxpool.Pool) *DistributionBalances {
	return &DistributionBalances{pool: pool}
}

// LoanComponents is the per-component outstanding balance for one
// loan. Each component is non-negative; the sum is the loan's total
// outstanding (excluding closed installments — those don't appear
// here because their balances are zero).
type LoanComponents struct {
	LoanID    uuid.UUID
	LoanNo    string
	Penalty   decimal.Decimal
	Interest  decimal.Decimal
	Principal decimal.Decimal
	Fees      decimal.Decimal
}

// AnyOutstanding returns true when at least one component is > 0.
// Used by the engine to short-circuit the loan legs when the
// matched member has no live loans.
func (lc LoanComponents) AnyOutstanding() bool {
	return !lc.Penalty.IsZero() || !lc.Interest.IsZero() ||
		!lc.Principal.IsZero() || !lc.Fees.IsZero()
}

// PrimaryActiveLoanTx returns the single oldest active/in_arrears
// loan for a counterparty. When a member has multiple live loans
// the waterfall targets the oldest first; phase 3.5 will accept a
// loan id from the engine to support targeted repayment via
// resolved_via='loan_no'.
//
// Returns ErrNotFound when the counterparty has no active loan —
// the engine handles that by zeroing the loan legs.
func (b *DistributionBalances) PrimaryActiveLoanTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (*LoanComponents, error) {
	var lc LoanComponents
	err := tx.QueryRow(ctx, `
		SELECT id, loan_no,
		       COALESCE(penalty_balance,   0),
		       COALESCE(interest_balance,  0),
		       COALESCE(principal_balance, 0),
		       COALESCE(fees_balance,      0)
		  FROM loans
		 WHERE counterparty_id = $1
		   AND status = ANY (ARRAY['active','in_arrears']::loan_status[])
		 ORDER BY created_at ASC
		 LIMIT 1
	`, cpID).Scan(&lc.LoanID, &lc.LoanNo, &lc.Penalty, &lc.Interest, &lc.Principal, &lc.Fees)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lc, nil
}

// LoanByNoTx — direct loan lookup for resolved_via='loan_no'. The
// engine sends the whole inbound amount to this loan's components in
// waterfall order (penalty → interest → principal → fees), skipping
// the BOSA/FOSA fallback entirely.
func (b *DistributionBalances) LoanByNoTx(ctx context.Context, tx pgx.Tx, loanNo string) (*LoanComponents, error) {
	if strings.TrimSpace(loanNo) == "" {
		return nil, ErrNotFound
	}
	var lc LoanComponents
	err := tx.QueryRow(ctx, `
		SELECT id, loan_no,
		       COALESCE(penalty_balance,   0),
		       COALESCE(interest_balance,  0),
		       COALESCE(principal_balance, 0),
		       COALESCE(fees_balance,      0)
		  FROM loans WHERE loan_no = $1
	`, loanNo).Scan(&lc.LoanID, &lc.LoanNo, &lc.Penalty, &lc.Interest, &lc.Principal, &lc.Fees)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lc, nil
}

// DepositAccount is the minimal shape the engine needs to direct a
// leftover deposit at.
type DepositAccount struct {
	ID        uuid.UUID
	AccountNo string
	// ProductCode is what distinguishes BOSA from FOSA in the
	// engine. The convention is product_code starting with "BOSA"
	// or "FOSA"; phase 3.5 will flip this to a typed product-
	// classification column.
	ProductCode string
}

// DepositAccountsTx returns all active deposit accounts for a
// counterparty, ordered by created_at. The engine picks the first
// BOSA-classified account for the bosa_top_up leg and the first
// FOSA for fosa_top_up; absent accounts cause that leg to
// contribute zero (the leftover flows to the next leg).
func (b *DistributionBalances) DepositAccountsTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) ([]DepositAccount, error) {
	rs, err := tx.Query(ctx, `
		SELECT d.id, d.account_no, COALESCE(p.product_code, '')
		  FROM deposit_accounts d
		  JOIN deposit_products p ON p.id = d.product_id
		 WHERE d.counterparty_id = $1
		   AND d.status = 'active'
		 ORDER BY d.created_at ASC
	`, cpID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []DepositAccount
	for rs.Next() {
		var d DepositAccount
		if err := rs.Scan(&d.ID, &d.AccountNo, &d.ProductCode); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rs.Err()
}

// DepositAccountByNoTx — direct lookup for
// resolved_via='deposit_account_no'. The engine sends the whole
// inbound amount to this one account, skipping the waterfall.
func (b *DistributionBalances) DepositAccountByNoTx(ctx context.Context, tx pgx.Tx, accountNo string) (*DepositAccount, error) {
	if strings.TrimSpace(accountNo) == "" {
		return nil, ErrNotFound
	}
	var d DepositAccount
	err := tx.QueryRow(ctx, `
		SELECT d.id, d.account_no, COALESCE(p.product_code, '')
		  FROM deposit_accounts d
		  JOIN deposit_products p ON p.id = d.product_id
		 WHERE d.account_no = $1
	`, accountNo).Scan(&d.ID, &d.AccountNo, &d.ProductCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// FeesDueTx returns the amount of fees this counterparty owes. Phase
// 3 stubs this to zero — fees aren't tracked in a per-member dues
// table today, only as fee_catalog entries that fire on specific
// events (loan disbursement, etc). Phase 3.5 wires the real dues
// query once the fee-dues table lands. The engine's contract treats
// any non-recognised target as "zero", so the fees_due leg
// contributes nothing and the leftover flows.
//
// Kept as a method so phase 3.5 doesn't have to chase a static
// helper through the engine's call sites.
func (b *DistributionBalances) FeesDueTx(_ context.Context, _ pgx.Tx, _ uuid.UUID) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
