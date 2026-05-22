// Deposit account + transaction persistence.
//
// PostTxnTx is the atomic posting routine. It:
//   1. Locks the account row (`FOR UPDATE`)
//   2. Updates cached current_balance + available_balance
//   3. Inserts the ledger row with balance_after = new_balance
//   4. Bumps last_activity_at / last_deposit_at / last_withdrawal_at
//   5. Upserts the daily balance snapshot for today
//
// Every mutation must be inside the same tenant-bound pgx.Tx (RLS).

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type DepositStore struct {
	pool *pgxpool.Pool
}

func NewDepositStore(pool *pgxpool.Pool) *DepositStore {
	return &DepositStore{pool: pool}
}

// ─────────── Account scan / cols ───────────

const acctCols = `
	id, tenant_id, member_id, product_id, account_no, status,
	current_balance, available_balance,
	opened_at, matures_at, closed_at,
	last_activity_at, last_deposit_at, last_withdrawal_at,
	fixed_term_months, fixed_interest_rate_pct,
	goal_target_amount, goal_target_date, goal_description,
	guardian_member_id, group_org_id,
	withdrawal_notice_given_at, withdrawal_notice_amount,
	created_at, updated_at, created_by
`

func scanAcct(row pgx.Row) (*domain.DepositAccount, error) {
	var a domain.DepositAccount
	err := row.Scan(
		&a.ID, &a.TenantID, &a.MemberID, &a.ProductID, &a.AccountNo, &a.Status,
		&a.CurrentBalance, &a.AvailableBalance,
		&a.OpenedAt, &a.MaturesAt, &a.ClosedAt,
		&a.LastActivityAt, &a.LastDepositAt, &a.LastWithdrawalAt,
		&a.FixedTermMonths, &a.FixedInterestRatePct,
		&a.GoalTargetAmount, &a.GoalTargetDate, &a.GoalDescription,
		&a.GuardianMemberID, &a.GroupOrgID,
		&a.WithdrawalNoticeGivenAt, &a.WithdrawalNoticeAmount,
		&a.CreatedAt, &a.UpdatedAt, &a.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ─────────── Account open / get / list ───────────

type OpenInput struct {
	MemberID             uuid.UUID
	ProductID            uuid.UUID
	OpeningDeposit       decimal.Decimal // 0 to skip the opening deposit
	OpeningChannel       *domain.DepositChannel
	OpeningChannelRef    *string
	FixedTermMonths      *int
	FixedInterestRatePct *decimal.Decimal
	GoalTargetAmount     *decimal.Decimal
	GoalTargetDate       *time.Time
	GoalDescription      *string
	GuardianMemberID     *uuid.UUID
	GroupOrgID           *uuid.UUID
	CreatedBy            uuid.UUID
}

// OpenAccountTx creates an account row and, if OpeningDeposit > 0,
// posts the opening_balance transaction.
func (s *DepositStore) OpenAccountTx(ctx context.Context, tx pgx.Tx, in OpenInput, accountNo string) (*domain.DepositAccount, *domain.DepositTransaction, error) {
	// Fixed deposits compute maturity at open time.
	var matures *time.Time
	if in.FixedTermMonths != nil && *in.FixedTermMonths > 0 {
		t := time.Now().AddDate(0, *in.FixedTermMonths, 0)
		matures = &t
	}
	now := time.Now()

	row := tx.QueryRow(ctx, `
		INSERT INTO deposit_accounts (
			tenant_id, member_id, product_id, account_no, status,
			current_balance, available_balance,
			opened_at, matures_at,
			fixed_term_months, fixed_interest_rate_pct,
			goal_target_amount, goal_target_date, goal_description,
			guardian_member_id, group_org_id,
			created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, 'active',
			0, 0,
			$4, $5,
			$6, $7,
			$8, $9, $10,
			$11, $12,
			$13
		)
		RETURNING `+acctCols,
		in.MemberID, in.ProductID, accountNo,
		now, matures,
		in.FixedTermMonths, in.FixedInterestRatePct,
		in.GoalTargetAmount, in.GoalTargetDate, in.GoalDescription,
		in.GuardianMemberID, in.GroupOrgID,
		in.CreatedBy,
	)
	acct, err := scanAcct(row)
	if err != nil {
		return nil, nil, err
	}

	// Opening deposit (optional).
	if in.OpeningDeposit.GreaterThan(decimal.Zero) {
		txn, err := s.PostTxnTx(ctx, tx, PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnOpeningBalance,
			Amount:      in.OpeningDeposit,
			Channel:     in.OpeningChannel,
			ChannelRef:  in.OpeningChannelRef,
			Narration:   ptrStr("Opening balance"),
			InitiatedBy: in.CreatedBy,
		})
		if err != nil {
			return nil, nil, err
		}
		// Re-read the account so balances reflect the opening txn.
		acct, err = s.GetAccountTx(ctx, tx, acct.ID)
		if err != nil {
			return nil, nil, err
		}
		return acct, txn, nil
	}
	return acct, nil, nil
}

func (s *DepositStore) GetAccountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DepositAccount, error) {
	row := tx.QueryRow(ctx, `SELECT `+acctCols+` FROM deposit_accounts WHERE id = $1`, id)
	a, err := scanAcct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *DepositStore) AccountsByMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]domain.DepositAccount, error) {
	rows, err := tx.Query(ctx, `SELECT `+acctCols+` FROM deposit_accounts WHERE counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1) ORDER BY opened_at DESC NULLS LAST`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DepositAccount
	for rows.Next() {
		a, err := scanAcct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type AcctListFilter struct {
	Status    string
	ProductID *uuid.UUID
	Q         string
	Limit     int
	Offset    int
}

type AcctListItem struct {
	Account  domain.DepositAccount `json:"account"`
	MemberNo string                `json:"member_no"`
	FullName string                `json:"full_name"`
	Status   string                `json:"member_status"`
	Product  struct {
		Code        string                    `json:"code"`
		Name        string                    `json:"name"`
		ProductType domain.DepositProductType `json:"product_type"`
	} `json:"product"`
}

func (s *DepositStore) ListAccountsTx(ctx context.Context, tx pgx.Tx, f AcctListFilter) ([]AcctListItem, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += fmt.Sprintf(" AND a.status = $%d", idx)
		args = append(args, f.Status)
		idx++
	}
	if f.ProductID != nil {
		where += fmt.Sprintf(" AND a.product_id = $%d", idx)
		args = append(args, *f.ProductID)
		idx++
	}
	if f.Q != "" {
		where += fmt.Sprintf(" AND (m.full_name ILIKE $%d OR m.member_no ILIKE $%d OR a.account_no ILIKE $%d)", idx, idx, idx)
		args = append(args, "%"+f.Q+"%")
		idx++
	}

	var total int
	if err := tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM deposit_accounts a JOIN members m ON m.id = a.member_id JOIN deposit_products p ON p.id = a.product_id "+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s,
		       m.member_no, m.full_name, m.status::text,
		       p.code, p.name, p.product_type
		FROM deposit_accounts a
		JOIN members m ON m.id = a.member_id
		JOIN deposit_products p ON p.id = a.product_id
		%s
		ORDER BY a.current_balance DESC, m.full_name ASC
		LIMIT $%d OFFSET $%d
	`, prefixCols(acctCols, "a"), where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AcctListItem
	for rows.Next() {
		var it AcctListItem
		err := rows.Scan(
			&it.Account.ID, &it.Account.TenantID, &it.Account.MemberID, &it.Account.ProductID,
			&it.Account.AccountNo, &it.Account.Status,
			&it.Account.CurrentBalance, &it.Account.AvailableBalance,
			&it.Account.OpenedAt, &it.Account.MaturesAt, &it.Account.ClosedAt,
			&it.Account.LastActivityAt, &it.Account.LastDepositAt, &it.Account.LastWithdrawalAt,
			&it.Account.FixedTermMonths, &it.Account.FixedInterestRatePct,
			&it.Account.GoalTargetAmount, &it.Account.GoalTargetDate, &it.Account.GoalDescription,
			&it.Account.GuardianMemberID, &it.Account.GroupOrgID,
			&it.Account.WithdrawalNoticeGivenAt, &it.Account.WithdrawalNoticeAmount,
			&it.Account.CreatedAt, &it.Account.UpdatedAt, &it.Account.CreatedBy,
			&it.MemberNo, &it.FullName, &it.Status,
			&it.Product.Code, &it.Product.Name, &it.Product.ProductType,
		)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// ─────────── Withdrawal-notice management ───────────

func (s *DepositStore) SetWithdrawalNoticeTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, amount decimal.Decimal) error {
	_, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET withdrawal_notice_given_at = now(),
		       withdrawal_notice_amount   = $2
		 WHERE id = $1
	`, accountID, amount)
	return err
}

func (s *DepositStore) ClearWithdrawalNoticeTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET withdrawal_notice_given_at = NULL,
		       withdrawal_notice_amount   = NULL
		 WHERE id = $1
	`, accountID)
	return err
}

// ─────────── Status transitions ───────────

func (s *DepositStore) SetStatusTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, status domain.DepositAccountStatus, closed bool) error {
	var q string
	if closed {
		q = `UPDATE deposit_accounts SET status = $2, closed_at = COALESCE(closed_at, now()) WHERE id = $1`
	} else {
		q = `UPDATE deposit_accounts SET status = $2 WHERE id = $1`
	}
	_, err := tx.Exec(ctx, q, accountID, string(status))
	return err
}

// ─────────── Transaction posting ───────────

type PostDepInput struct {
	Account              *domain.DepositAccount
	TxnType              domain.DepositTxnType
	Amount               decimal.Decimal // signed: + credit, − debit. Callers may pass positive + TxnType drives sign.
	ValueDate            *time.Time
	Channel              *domain.DepositChannel
	ChannelRef           *string
	Narration            *string
	CounterpartyAccount  *domain.DepositAccount
	CounterpartyTxnID    *uuid.UUID
	ReversesTxnID        *uuid.UUID
	ReversalReason       *string
	InitiatedBy          uuid.UUID
	AuthorizedBy         *uuid.UUID
	AuthorizationReason  *string
	WorkflowInstanceID   *uuid.UUID
}

// signFor returns the canonical sign multiplier for a txn type.
// Callers may pass positive amounts; we apply the sign here so the
// ledger always stores signed amounts.
func signFor(t domain.DepositTxnType) decimal.Decimal {
	switch t {
	case domain.TxnDeposit, domain.TxnDepTransferIn, domain.TxnInterestCredit,
		domain.TxnOpeningBalance:
		return decimal.NewFromInt(1)
	case domain.TxnWithdrawal, domain.TxnDepTransferOut, domain.TxnFeeDebit,
		domain.TxnGoalPayout:
		return decimal.NewFromInt(-1)
	default:
		// adjustment & reversal: caller passes signed amount, we keep it.
		return decimal.Zero
	}
}

func (s *DepositStore) PostTxnTx(ctx context.Context, tx pgx.Tx, in PostDepInput) (*domain.DepositTransaction, error) {
	if in.Account == nil {
		return nil, fmt.Errorf("post: account is required")
	}
	if in.Amount.IsZero() {
		return nil, fmt.Errorf("post: amount must be non-zero")
	}
	if !in.TxnType.Valid() {
		return nil, fmt.Errorf("post: invalid txn_type %q", in.TxnType)
	}

	// Apply canonical sign for fixed-direction types; trust caller for adjustment / reversal.
	signed := in.Amount
	if mult := signFor(in.TxnType); !mult.IsZero() {
		signed = in.Amount.Abs().Mul(mult)
	}
	newBalance := in.Account.CurrentBalance.Add(signed)
	if newBalance.LessThan(decimal.Zero) {
		return nil, domain.ErrInsufficientBalance
	}

	// Update cached balances with CAS.
	tag, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET current_balance   = $2,
		       available_balance = $2,
		       last_activity_at  = now(),
		       last_deposit_at   = CASE WHEN $3::numeric > 0 THEN now() ELSE last_deposit_at END,
		       last_withdrawal_at = CASE WHEN $3::numeric < 0 THEN now() ELSE last_withdrawal_at END
		 WHERE id = $1 AND current_balance = $4
	`, in.Account.ID, newBalance, signed, in.Account.CurrentBalance)
	if err != nil {
		return nil, fmt.Errorf("update account balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("concurrent balance update — please retry")
	}

	txnNo, err := nextSeq(ctx, tx, "deposit_txn", "DPT")
	if err != nil {
		return nil, err
	}

	var ch *string
	if in.Channel != nil {
		v := string(*in.Channel)
		ch = &v
	}
	var cpAcctID *uuid.UUID
	if in.CounterpartyAccount != nil {
		id := in.CounterpartyAccount.ID
		cpAcctID = &id
	}
	valueDate := time.Now().UTC()
	if in.ValueDate != nil {
		valueDate = *in.ValueDate
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO deposit_transactions (
			tenant_id, account_id, member_id, txn_no, txn_type,
			amount, value_date,
			channel, channel_ref, narration,
			counterparty_account_id, counterparty_txn_id,
			reverses_txn_id, reversal_reason,
			balance_after,
			initiated_by, authorized_by, authorization_reason,
			workflow_instance_id
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5, $6,
			$7, $8, $9,
			$10, $11,
			$12, $13,
			$14,
			$15, $16, $17,
			$18
		)
		RETURNING id, tenant_id, account_id, member_id, txn_no, txn_type,
		          amount, value_date, channel, channel_ref, narration,
		          counterparty_account_id, counterparty_txn_id,
		          reverses_txn_id, reversed_by_txn_id, reversal_reason,
		          balance_after, initiated_by, authorized_by, authorization_reason,
		          workflow_instance_id, posted_at, created_at
	`,
		in.Account.ID, in.Account.MemberID, txnNo, string(in.TxnType),
		signed, valueDate,
		ch, in.ChannelRef, in.Narration,
		cpAcctID, in.CounterpartyTxnID,
		in.ReversesTxnID, in.ReversalReason,
		newBalance,
		in.InitiatedBy, in.AuthorizedBy, in.AuthorizationReason,
		in.WorkflowInstanceID,
	)
	txn, err := scanDepTxn(row)
	if err != nil {
		return nil, err
	}

	// Mark the reversed row, if any, as "reversed_by".
	if in.ReversesTxnID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE deposit_transactions SET reversed_by_txn_id = $2 WHERE id = $1
		`, *in.ReversesTxnID, txn.ID); err != nil {
			return nil, err
		}
	}

	// Upsert today's daily snapshot for this account.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if _, err := tx.Exec(ctx, `
		INSERT INTO deposit_daily_balances (tenant_id, account_id, snapshot_date, balance, product_id, member_id)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		ON CONFLICT (account_id, snapshot_date)
		DO UPDATE SET balance = EXCLUDED.balance
	`, in.Account.ID, today, newBalance, in.Account.ProductID, in.Account.MemberID); err != nil {
		return nil, err
	}

	return txn, nil
}

// LinkCounterpartyTxnTx back-fills the counterparty_txn_id on the first
// leg of a transfer, after the second leg posts.
func (s *DepositStore) LinkCounterpartyTxnTx(ctx context.Context, tx pgx.Tx, txnID, counterpartyTxnID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE deposit_transactions SET counterparty_txn_id = $2 WHERE id = $1`, txnID, counterpartyTxnID)
	return err
}

// GetTxnTx loads a single transaction by id.
func (s *DepositStore) GetTxnTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DepositTransaction, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, account_id, member_id, txn_no, txn_type,
		       amount, value_date, channel, channel_ref, narration,
		       counterparty_account_id, counterparty_txn_id,
		       reverses_txn_id, reversed_by_txn_id, reversal_reason,
		       balance_after, initiated_by, authorized_by, authorization_reason,
		       workflow_instance_id, posted_at, created_at
		FROM deposit_transactions WHERE id = $1`, id)
	t, err := scanDepTxn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func scanDepTxn(row pgx.Row) (*domain.DepositTransaction, error) {
	var t domain.DepositTransaction
	var ch *string
	err := row.Scan(
		&t.ID, &t.TenantID, &t.AccountID, &t.MemberID, &t.TxnNo, &t.TxnType,
		&t.Amount, &t.ValueDate, &ch, &t.ChannelRef, &t.Narration,
		&t.CounterpartyAccountID, &t.CounterpartyTxnID,
		&t.ReversesTxnID, &t.ReversedByTxnID, &t.ReversalReason,
		&t.BalanceAfter, &t.InitiatedBy, &t.AuthorizedBy, &t.AuthorizationReason,
		&t.WorkflowInstanceID, &t.PostedAt, &t.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if ch != nil {
		c := domain.DepositChannel(*ch)
		t.Channel = &c
	}
	return &t, nil
}

// ─────────── Queries used by rule evaluation ───────────

// WithdrawalCountThisMonth returns how many withdrawals (not reversed)
// have been posted in the current calendar month for an account.
func (s *DepositStore) WithdrawalCountThisMonthTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, now time.Time) (int, error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM deposit_transactions
		 WHERE account_id = $1
		   AND txn_type = 'withdrawal'
		   AND reversed_by_txn_id IS NULL
		   AND posted_at >= $2
	`, accountID, monthStart).Scan(&n)
	return n, err
}

// DuplicateExistsTx checks for a same-amount same-channel-ref txn posted
// within the lookback window. Used as a soft duplicate-detection signal.
// Returns true if a likely duplicate exists.
func (s *DepositStore) DuplicateExistsTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, amount decimal.Decimal, channelRef string, lookback time.Duration) (bool, error) {
	if channelRef == "" {
		return false, nil
	}
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM deposit_transactions
		 WHERE account_id = $1
		   AND channel_ref = $2
		   AND ABS(amount) = $3
		   AND posted_at > now() - $4::interval
	`, accountID, channelRef, amount.Abs(), fmt.Sprintf("%d seconds", int(lookback.Seconds()))).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ─────────── Statement query ───────────

type StatementRow struct {
	domain.DepositTransaction
}

func (s *DepositStore) StatementTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, from, to time.Time, limit, offset int) ([]domain.DepositTransaction, decimal.Decimal, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Opening balance: the balance_after of the last txn before `from`,
	// or 0 if no prior txn exists.
	var opening decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT balance_after FROM deposit_transactions
			 WHERE account_id = $1 AND posted_at < $2
			 ORDER BY posted_at DESC, id DESC LIMIT 1
		), 0)
	`, accountID, from).Scan(&opening)
	if err != nil {
		return nil, decimal.Zero, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, account_id, member_id, txn_no, txn_type,
		       amount, value_date, channel, channel_ref, narration,
		       counterparty_account_id, counterparty_txn_id,
		       reverses_txn_id, reversed_by_txn_id, reversal_reason,
		       balance_after, initiated_by, authorized_by, authorization_reason,
		       workflow_instance_id, posted_at, created_at
		FROM deposit_transactions
		WHERE account_id = $1 AND posted_at >= $2 AND posted_at < $3
		ORDER BY posted_at ASC, id ASC
		LIMIT $4 OFFSET $5
	`, accountID, from, to, limit, offset)
	if err != nil {
		return nil, decimal.Zero, err
	}
	defer rows.Close()
	var out []domain.DepositTransaction
	for rows.Next() {
		t, err := scanDepTxn(rows)
		if err != nil {
			return nil, decimal.Zero, err
		}
		out = append(out, *t)
	}
	return out, opening, rows.Err()
}

// ─────────── Tenant-wide rollups ───────────

type ProductSummary struct {
	ProductID      uuid.UUID                 `json:"product_id"`
	Code           string                    `json:"code"`
	Name           string                    `json:"name"`
	ProductType    domain.DepositProductType `json:"product_type"`
	ActiveAccounts int                       `json:"active_accounts"`
	TotalBalance   decimal.Decimal           `json:"total_balance"`
}

type DepositsSummary struct {
	TotalAccounts  int              `json:"total_accounts"`
	ActiveAccounts int              `json:"active_accounts"`
	DormantAccounts int             `json:"dormant_accounts"`
	TotalBalance   decimal.Decimal  `json:"total_balance"`
	ByProduct      []ProductSummary `json:"by_product"`
}

func (s *DepositStore) SummaryTx(ctx context.Context, tx pgx.Tx) (*DepositsSummary, error) {
	sum := &DepositsSummary{}
	err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(CASE WHEN status = 'active'  THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'dormant' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(current_balance), 0)
		FROM deposit_accounts
	`).Scan(&sum.TotalAccounts, &sum.ActiveAccounts, &sum.DormantAccounts, &sum.TotalBalance)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.code, p.name, p.product_type,
		       COALESCE(SUM(CASE WHEN a.status = 'active' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(a.current_balance), 0)
		FROM deposit_products p
		LEFT JOIN deposit_accounts a ON a.product_id = p.id
		WHERE p.is_active = true
		GROUP BY p.id, p.code, p.name, p.product_type
		ORDER BY p.product_type, p.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ps ProductSummary
		if err := rows.Scan(&ps.ProductID, &ps.Code, &ps.Name, &ps.ProductType, &ps.ActiveAccounts, &ps.TotalBalance); err != nil {
			return nil, err
		}
		sum.ByProduct = append(sum.ByProduct, ps)
	}
	return sum, rows.Err()
}

// ─────────── Daily snapshot job ───────────

// SnapshotForDateTx upserts the end-of-day balance for every non-closed
// account into deposit_daily_balances. Called from the -run-snapshot CLI
// or a cron. Idempotent — re-running for the same date overwrites.
func (s *DepositStore) SnapshotForDateTx(ctx context.Context, tx pgx.Tx, date time.Time) (int, error) {
	d := date.UTC().Truncate(24 * time.Hour)
	tag, err := tx.Exec(ctx, `
		INSERT INTO deposit_daily_balances (tenant_id, account_id, snapshot_date, balance, product_id, member_id)
		SELECT current_tenant_id(), a.id, $1, a.current_balance, a.product_id, a.member_id
		FROM deposit_accounts a
		WHERE a.status <> 'closed'
		ON CONFLICT (account_id, snapshot_date)
		DO UPDATE SET balance = EXCLUDED.balance
	`, d)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func ptrStr(s string) *string { return &s }
