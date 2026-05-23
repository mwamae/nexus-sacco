// Member ledger — unified read-model that joins the three per-module
// transaction tables (deposit / loan / share) into one timeline for a
// single member.
//
// Why a store (not a view): the savings service already owns all three
// tables; building this as a Go-level UNION ALL keeps the cardinality
// + cursor pagination explicit and side-steps a view migration on
// what's still an evolving schema. A materialised view (or a snapshot
// table) is the next-step option if this query becomes hot.
//
// Cash-flow semantics — debit / credit are from the MEMBER's pocket:
//   credit  = money flowing IN  to the member
//   debit   = money flowing OUT of the member
//
// Per-source mappings:
//   deposit_transactions
//     credit:  deposit, transfer_in, interest_credit, opening_balance
//     debit:   withdrawal, transfer_out, fee_debit, goal_payout
//   loan_transactions
//     credit:  disbursement                       (money received)
//     debit:   repayment                          (money paid back)
//     (info-only — no debit/credit):
//             fee_charge, interest_accrual, penalty_charge, penalty_waiver
//     (sign-flipped via reverses_txn_id):
//             reversal rows mirror their parent
//   share_transactions
//     credit:  transfer_in
//     debit:   purchase
//     info-only: adjustment, redemption, bonus_issue
//
// balance_after is the source-account's balance immediately after the
// row — not a cross-module running total (no such thing exists for a
// member spanning savings + loans + shares).

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type MemberLedgerStore struct {
	pool *pgxpool.Pool
}

func NewMemberLedgerStore(pool *pgxpool.Pool) *MemberLedgerStore {
	return &MemberLedgerStore{pool: pool}
}

type LedgerSource string

const (
	LedgerSourceDeposit LedgerSource = "deposit"
	LedgerSourceLoan    LedgerSource = "loan"
	LedgerSourceShare   LedgerSource = "share"
)

// LedgerRow is the unified shape returned over the wire. Fields that
// are source-specific (e.g. account_no vs loan_no) collapse into the
// generic `account_label`; consumers can branch on `source` if they
// need to display source-specific affordances.
type LedgerRow struct {
	Source        LedgerSource    `json:"source"`
	TxnID         uuid.UUID       `json:"txn_id"`
	TxnNo         string          `json:"txn_no"`
	PostedAt      time.Time       `json:"posted_at"`
	ValueDate     *time.Time      `json:"value_date,omitempty"`
	TxnType       string          `json:"txn_type"`
	AccountID     uuid.UUID       `json:"account_id"`
	AccountLabel  string          `json:"account_label"`
	Narration     *string         `json:"narration,omitempty"`
	Debit         decimal.Decimal `json:"debit"`
	Credit        decimal.Decimal `json:"credit"`
	BalanceAfter  decimal.Decimal `json:"balance_after"`
}

type LedgerPage struct {
	Rows []LedgerRow `json:"rows"`
	// NextCursor is the posted_at of the last row, encoded as RFC3339Nano.
	// Pass it back as `before` to fetch the next page. Empty when the
	// page is the final one.
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// ListMemberLedgerTx returns the next `limit` ledger rows for the
// member, ordered by posted_at DESC. `before` is the cursor — pass
// time.Time{} for the first page.
func (s *MemberLedgerStore) ListMemberLedgerTx(
	ctx context.Context,
	tx pgx.Tx,
	memberID uuid.UUID,
	before time.Time,
	limit int,
) (*LedgerPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Fetch limit+1 so we can detect "has more" without a second count
	// query. The extra row is dropped before returning.
	fetch := limit + 1

	// Sentinel cursor — if `before` is zero, accept everything (the
	// query uses `posted_at < $cursor` to keep cursoring uniform).
	cursor := before
	if cursor.IsZero() {
		cursor = time.Now().Add(24 * time.Hour) // safely in the future
	}

	q := `
WITH deposits AS (
  SELECT
    'deposit'::text                              AS source,
    t.id                                         AS txn_id,
    t.txn_no,
    t.posted_at,
    t.value_date,
    t.txn_type::text                             AS txn_type,
    t.account_id,
    a.account_no                                 AS account_label,
    t.narration,
    CASE WHEN t.txn_type IN ('withdrawal','transfer_out','fee_debit','goal_payout')
         THEN ABS(t.amount) ELSE 0 END           AS debit,
    CASE WHEN t.txn_type IN ('deposit','transfer_in','interest_credit','opening_balance')
         THEN ABS(t.amount) ELSE 0 END           AS credit,
    t.balance_after                              AS balance_after
    FROM deposit_transactions t
    JOIN deposit_accounts a ON a.id = t.account_id
   WHERE t.counterparty_id = $1
     AND t.posted_at < $2
),
loans AS (
  SELECT
    'loan'::text                                 AS source,
    t.id                                         AS txn_id,
    t.txn_no,
    t.posted_at,
    t.value_date,
    t.txn_type::text                             AS txn_type,
    t.loan_id                                    AS account_id,
    l.loan_no                                    AS account_label,
    t.narration,
    -- loan_transactions.amount is SIGNED (+ adds to outstanding, − reduces).
    -- Take ABS so the user-facing column always carries magnitude.
    CASE WHEN t.txn_type = 'repayment'
         THEN ABS(t.amount) ELSE 0 END           AS debit,
    CASE WHEN t.txn_type = 'disbursement'
         THEN ABS(t.amount) ELSE 0 END           AS credit,
    -- principal_balance is the running outstanding for the loan.
    l.principal_balance                          AS balance_after
    FROM loan_transactions t
    JOIN loans l ON l.id = t.loan_id
   WHERE t.counterparty_id = $1
     AND t.posted_at < $2
),
shares AS (
  SELECT
    'share'::text                                AS source,
    t.id                                         AS txn_id,
    t.txn_no,
    t.posted_at,
    NULL::date                                   AS value_date,
    t.txn_type::text                             AS txn_type,
    t.account_id,
    a.account_no                                 AS account_label,
    t.narration,
    CASE WHEN t.txn_type = 'purchase'
         THEN ABS(t.amount) ELSE 0 END           AS debit,
    CASE WHEN t.txn_type = 'transfer_in'
         THEN ABS(t.amount) ELSE 0 END           AS credit,
    t.balance_after_amount                       AS balance_after
    FROM share_transactions t
    JOIN share_accounts a ON a.id = t.account_id
   WHERE t.counterparty_id = $1
     AND t.posted_at < $2
)
SELECT * FROM deposits
UNION ALL SELECT * FROM loans
UNION ALL SELECT * FROM shares
ORDER BY posted_at DESC, txn_id DESC
LIMIT $3
`
	rows, err := tx.Query(ctx, q, memberID, cursor, fetch)
	if err != nil {
		return nil, fmt.Errorf("ledger query: %w", err)
	}
	defer rows.Close()

	page := &LedgerPage{Rows: []LedgerRow{}}
	for rows.Next() {
		var r LedgerRow
		var source string
		var narration *string
		var valueDate *time.Time
		if err := rows.Scan(
			&source, &r.TxnID, &r.TxnNo, &r.PostedAt, &valueDate, &r.TxnType,
			&r.AccountID, &r.AccountLabel, &narration,
			&r.Debit, &r.Credit, &r.BalanceAfter,
		); err != nil {
			return nil, err
		}
		r.Source = LedgerSource(source)
		r.Narration = narration
		r.ValueDate = valueDate
		page.Rows = append(page.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// If we got more than requested, the surplus is the "has-more" signal.
	// Strip it and emit a cursor based on the last KEPT row.
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		page.HasMore = true
		page.NextCursor = page.Rows[len(page.Rows)-1].PostedAt.UTC().Format(time.RFC3339Nano)
	}
	return page, nil
}
