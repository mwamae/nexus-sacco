// Share account + transaction + lien + certificate persistence.
//
// All mutating operations live inside a tenant-bound pgx.Tx so RLS
// policies enforce isolation. The append-only ledger
// (share_transactions) is the source of truth; share_accounts caches
// the running balance for fast reads and atomic CAS.

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

type ShareStore struct {
	pool *pgxpool.Pool
}

func NewShareStore(pool *pgxpool.Pool) *ShareStore {
	return &ShareStore{pool: pool}
}

// ─────────── Sequence helpers ───────────

// nextSeq bumps the per-tenant per-year counter and formats a human-
// readable identifier. kind is 'account' | 'txn' | 'certificate'.
func nextSeq(ctx context.Context, tx pgx.Tx, kind, prefix string) (string, error) {
	year := time.Now().UTC().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
		VALUES (current_tenant_id(), $1, $2, 1)
		ON CONFLICT (tenant_id, kind, year)
		DO UPDATE SET last_value = share_number_seq.last_value + 1
		RETURNING last_value
	`, kind, year).Scan(&next)
	if err != nil {
		return "", fmt.Errorf("next %s seq: %w", kind, err)
	}
	return fmt.Sprintf("%s-%d-%05d", prefix, year, next), nil
}

// ─────────── Account CRUD ───────────

func scanAccount(row pgx.Row) (*domain.ShareAccount, error) {
	var a domain.ShareAccount
	err := row.Scan(
		&a.ID, &a.TenantID, &a.MemberID, &a.AccountNo, &a.Status,
		&a.SharesHeld, &a.SharesPledged, &a.ParValueAtOpen,
		&a.FirstPurchaseAt, &a.ClosedAt, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.SharesAvailable = a.SharesHeld - a.SharesPledged
	a.TotalValue = decimal.NewFromInt(int64(a.SharesHeld)).Mul(a.ParValueAtOpen)
	return &a, nil
}

const accountCols = `
	id, tenant_id, member_id, account_no, status,
	shares_held, shares_pledged, par_value_at_open,
	first_purchase_at, closed_at, created_at, updated_at
`

// EnsureAccountTx returns the member's share account, creating it if
// missing. The first-purchase timestamp stays nil until the first
// crediting transaction posts.
func (s *ShareStore) EnsureAccountTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID, parValue decimal.Decimal) (*domain.ShareAccount, error) {
	// Try existing first. Read by counterparty_id (Phase D sub-PR 1);
	// the BEFORE INSERT trigger keeps the bridge column populated, so
	// the row we may have just inserted is found via the same filter.
	row := tx.QueryRow(ctx, `SELECT `+accountCols+` FROM share_accounts WHERE counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)`, memberID)
	acc, err := scanAccount(row)
	if err == nil {
		return acc, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Create.
	accountNo, err := nextSeq(ctx, tx, "account", "SHA")
	if err != nil {
		return nil, err
	}
	row = tx.QueryRow(ctx, `
		INSERT INTO share_accounts (tenant_id, member_id, account_no, par_value_at_open)
		VALUES (current_tenant_id(), $1, $2, $3)
		RETURNING `+accountCols, memberID, accountNo, parValue)
	return scanAccount(row)
}

func (s *ShareStore) GetAccountTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (*domain.ShareAccount, error) {
	row := tx.QueryRow(ctx, `SELECT `+accountCols+` FROM share_accounts WHERE id = $1`, accountID)
	acc, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return acc, err
}

func (s *ShareStore) GetAccountByMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*domain.ShareAccount, error) {
	row := tx.QueryRow(ctx, `SELECT `+accountCols+` FROM share_accounts WHERE counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)`, memberID)
	acc, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return acc, err
}

// ─────────── Transaction posting ───────────

// PostInput is the per-transaction payload. shares_delta MUST be signed
// (positive credit, negative debit); callers compute it from txn_type
// + quantity. par_value_at_txn snapshots the tenant policy at post time.
type PostInput struct {
	Account              *domain.ShareAccount
	TxnType              domain.ShareTxnType
	SharesDelta          int
	ParValueAtTxn        decimal.Decimal
	PaymentChannel       *domain.PaymentChannel
	PaymentRef           *string
	Narration            *string
	CounterpartyAccount  *domain.ShareAccount
	CounterpartyTxnID    *uuid.UUID
	InitiatedBy          uuid.UUID
	AuthorizedBy         *uuid.UUID
	AuthorizationReason  *string
}

// PostTxnTx writes one ledger row, updates the cached running balance,
// and (on the first credit) stamps first_purchase_at. Returns the
// inserted transaction.
func (s *ShareStore) PostTxnTx(ctx context.Context, tx pgx.Tx, in PostInput) (*domain.ShareTransaction, error) {
	if in.Account == nil {
		return nil, fmt.Errorf("post: account is required")
	}
	if in.SharesDelta == 0 {
		return nil, fmt.Errorf("post: shares_delta must be non-zero")
	}
	newBalance := in.Account.SharesHeld + in.SharesDelta
	if newBalance < 0 {
		return nil, domain.ErrInsufficientShares
	}

	amount := decimal.NewFromInt(int64(in.SharesDelta)).Mul(in.ParValueAtTxn)
	balanceAfterAmount := decimal.NewFromInt(int64(newBalance)).Mul(in.ParValueAtTxn)

	// Update cached balance with optimistic concurrency check.
	tag, err := tx.Exec(ctx, `
		UPDATE share_accounts
		   SET shares_held = $2,
		       first_purchase_at = COALESCE(first_purchase_at, CASE WHEN $3 > 0 THEN now() END)
		 WHERE id = $1 AND shares_held = $4
	`, in.Account.ID, newBalance, in.SharesDelta, in.Account.SharesHeld)
	if err != nil {
		return nil, fmt.Errorf("update account balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("concurrent share balance update — please retry")
	}

	// Generate txn_no.
	txnNo, err := nextSeq(ctx, tx, "txn", "SHT")
	if err != nil {
		return nil, err
	}

	var cpAcct *uuid.UUID
	if in.CounterpartyAccount != nil {
		id := in.CounterpartyAccount.ID
		cpAcct = &id
	}

	var pc *string
	if in.PaymentChannel != nil {
		v := string(*in.PaymentChannel)
		pc = &v
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO share_transactions (
			tenant_id, account_id, member_id, txn_no, txn_type, shares_delta,
			par_value_at_txn, amount, payment_channel, payment_ref, narration,
			counterparty_account_id, counterparty_txn_id, balance_after_shares,
			balance_after_amount, initiated_by, authorized_by, authorization_reason
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13,
			$14, $15, $16, $17
		)
		RETURNING id, tenant_id, account_id, member_id, txn_no, txn_type, shares_delta,
		          par_value_at_txn, amount, payment_channel, payment_ref, narration,
		          counterparty_account_id, counterparty_txn_id, balance_after_shares,
		          balance_after_amount, initiated_by, authorized_by, authorization_reason,
		          posted_at, created_at
	`,
		in.Account.ID, in.Account.MemberID, txnNo, string(in.TxnType), in.SharesDelta,
		in.ParValueAtTxn, amount, pc, in.PaymentRef, in.Narration,
		cpAcct, in.CounterpartyTxnID, newBalance,
		balanceAfterAmount, in.InitiatedBy, in.AuthorizedBy, in.AuthorizationReason,
	)
	return scanTxn(row)
}

func scanTxn(row pgx.Row) (*domain.ShareTransaction, error) {
	var t domain.ShareTransaction
	var pc *string
	err := row.Scan(
		&t.ID, &t.TenantID, &t.AccountID, &t.MemberID, &t.TxnNo, &t.TxnType, &t.SharesDelta,
		&t.ParValueAtTxn, &t.Amount, &pc, &t.PaymentRef, &t.Narration,
		&t.CounterpartyAccountID, &t.CounterpartyTxnID, &t.BalanceAfterShares,
		&t.BalanceAfterAmount, &t.InitiatedBy, &t.AuthorizedBy, &t.AuthorizationReason,
		&t.PostedAt, &t.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if pc != nil {
		c := domain.PaymentChannel(*pc)
		t.PaymentChannel = &c
	}
	return &t, nil
}

// LinkCounterpartyTxnTx fills the counterparty_txn_id back-reference on
// the first leg of a transfer, after the second leg has been posted.
func (s *ShareStore) LinkCounterpartyTxnTx(ctx context.Context, tx pgx.Tx, txnID, counterpartyTxnID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE share_transactions SET counterparty_txn_id = $2 WHERE id = $1
	`, txnID, counterpartyTxnID)
	return err
}

// ─────────── History + queries ───────────

func (s *ShareStore) HistoryByAccountTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, limit, offset int) ([]domain.ShareTransaction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, account_id, member_id, txn_no, txn_type, shares_delta,
		       par_value_at_txn, amount, payment_channel, payment_ref, narration,
		       counterparty_account_id, counterparty_txn_id, balance_after_shares,
		       balance_after_amount, initiated_by, authorized_by, authorization_reason,
		       posted_at, created_at
		FROM share_transactions
		WHERE account_id = $1
		ORDER BY posted_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, accountID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ShareTransaction
	for rows.Next() {
		t, err := scanTxn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

type ListFilter struct {
	Status   string
	Q        string // search member_no or full_name
	MinBelow bool   // only accounts below min_shares_required
	Limit    int
	Offset   int
}

type AccountListItem struct {
	Account  domain.ShareAccount `json:"account"`
	MemberNo string              `json:"member_no"`
	FullName string              `json:"full_name"`
	Status   string              `json:"member_status"`
}

func (s *ShareStore) ListAccountsTx(ctx context.Context, tx pgx.Tx, f ListFilter, minRequired int) ([]AccountListItem, int, error) {
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
	if f.Q != "" {
		where += fmt.Sprintf(" AND (m.full_name ILIKE $%d OR m.member_no ILIKE $%d OR a.account_no ILIKE $%d)", idx, idx, idx)
		args = append(args, "%"+f.Q+"%")
		idx++
	}
	if f.MinBelow && minRequired > 0 {
		where += fmt.Sprintf(" AND a.shares_held < $%d", idx)
		args = append(args, minRequired)
		idx++
	}

	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM share_accounts a JOIN members m ON m.id = a.member_id "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s, m.member_no, m.full_name, m.status::text
		FROM share_accounts a
		JOIN members m ON m.id = a.member_id
		%s
		ORDER BY a.shares_held DESC, m.full_name ASC
		LIMIT $%d OFFSET $%d
	`, prefixCols(accountCols, "a"), where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []AccountListItem
	for rows.Next() {
		var it AccountListItem
		err := rows.Scan(
			&it.Account.ID, &it.Account.TenantID, &it.Account.MemberID, &it.Account.AccountNo,
			&it.Account.Status, &it.Account.SharesHeld, &it.Account.SharesPledged,
			&it.Account.ParValueAtOpen, &it.Account.FirstPurchaseAt, &it.Account.ClosedAt,
			&it.Account.CreatedAt, &it.Account.UpdatedAt,
			&it.MemberNo, &it.FullName, &it.Status,
		)
		if err != nil {
			return nil, 0, err
		}
		it.Account.SharesAvailable = it.Account.SharesHeld - it.Account.SharesPledged
		it.Account.TotalValue = decimal.NewFromInt(int64(it.Account.SharesHeld)).Mul(it.Account.ParValueAtOpen)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// prefixCols prepends "a." to each comma-separated column for JOIN selects.
func prefixCols(cols, alias string) string {
	out := ""
	depth := 0
	field := ""
	flush := func() {
		if field == "" {
			return
		}
		if out != "" {
			out += ", "
		}
		out += alias + "." + field
		field = ""
	}
	for _, r := range cols {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				flush()
				continue
			}
		case ' ', '\t', '\n':
			if field == "" {
				continue
			}
		}
		field += string(r)
	}
	flush()
	return out
}

// ─────────── Summary ───────────

type Summary struct {
	TotalAccounts        int             `json:"total_accounts"`
	ActiveAccounts       int             `json:"active_accounts"`
	TotalSharesIssued    int             `json:"total_shares_issued"`
	TotalShareCapital    decimal.Decimal `json:"total_share_capital"`
	MembersBelowMinimum  int             `json:"members_below_minimum"`
	AccountsWithLien     int             `json:"accounts_with_lien"`
	TotalPledgedShares   int             `json:"total_pledged_shares"`
	ParValue             decimal.Decimal `json:"par_value"`
	MinSharesRequired    int             `json:"min_shares_required"`
}

func (s *ShareStore) SummaryTx(ctx context.Context, tx pgx.Tx, policy *SharePolicy) (*Summary, error) {
	sum := &Summary{
		ParValue:          policy.ParValue,
		MinSharesRequired: policy.MinSharesRequired,
	}
	err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(shares_held), 0),
			COALESCE(SUM(CASE WHEN shares_held < $1 AND status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN shares_pledged > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(shares_pledged), 0)
		FROM share_accounts
	`, policy.MinSharesRequired).Scan(
		&sum.TotalAccounts, &sum.ActiveAccounts,
		&sum.TotalSharesIssued, &sum.MembersBelowMinimum,
		&sum.AccountsWithLien, &sum.TotalPledgedShares,
	)
	if err != nil {
		return nil, err
	}
	sum.TotalShareCapital = decimal.NewFromInt(int64(sum.TotalSharesIssued)).Mul(policy.ParValue)
	return sum, nil
}

// TotalSharesIssuedTx returns the system-wide tenant share count, used
// to evaluate the per-member max-holding cap (% of total capital).
func (s *ShareStore) TotalSharesIssuedTx(ctx context.Context, tx pgx.Tx) (int, error) {
	var total int
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(shares_held), 0) FROM share_accounts`).Scan(&total)
	return total, err
}

// ActiveAccountsTx returns every active account with at least one share
// whose owning member is in a status that permits crediting (pending,
// active, dormant, suspended). Blacklisted, exited, deceased, and
// rejected members are excluded — they should not receive bonus shares
// or dividend top-ups.
//
// Used by bonus-issue runs that iterate the whole tenant register.
func (s *ShareStore) ActiveAccountsTx(ctx context.Context, tx pgx.Tx) ([]domain.ShareAccount, error) {
	rows, err := tx.Query(ctx, `SELECT `+prefixCols(accountCols, "a")+`
		FROM share_accounts a
		JOIN members m ON m.id = a.member_id
		WHERE a.status = 'active'
		  AND a.shares_held > 0
		  AND m.status NOT IN ('blacklisted', 'exited', 'deceased', 'rejected')
		ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ShareAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ─────────── Liens ───────────

func (s *ShareStore) PlaceLienTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, shares int, reason string, refKind, refID *string, placedBy uuid.UUID) (*domain.ShareLien, error) {
	if shares <= 0 {
		return nil, domain.ErrInvalidQuantity
	}
	// Atomically check availability and bump pledged.
	tag, err := tx.Exec(ctx, `
		UPDATE share_accounts
		   SET shares_pledged = shares_pledged + $2
		 WHERE id = $1
		   AND status = 'active'
		   AND shares_held - shares_pledged >= $2
	`, accountID, shares)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, domain.ErrInsufficientShares
	}
	var l domain.ShareLien
	err = tx.QueryRow(ctx, `
		INSERT INTO share_liens (tenant_id, account_id, shares_pledged, reason, reference_kind, reference_id, placed_by)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6)
		RETURNING id, tenant_id, account_id, shares_pledged, reason, reference_kind, reference_id, status, placed_by, placed_at, released_by, released_at, released_reason
	`, accountID, shares, reason, refKind, refID, placedBy).Scan(
		&l.ID, &l.TenantID, &l.AccountID, &l.SharesPledged, &l.Reason,
		&l.ReferenceKind, &l.ReferenceID, &l.Status, &l.PlacedBy, &l.PlacedAt,
		&l.ReleasedBy, &l.ReleasedAt, &l.ReleasedReason,
	)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *ShareStore) ReleaseLienTx(ctx context.Context, tx pgx.Tx, lienID uuid.UUID, releasedBy uuid.UUID, reason string) (*domain.ShareLien, error) {
	// Read current lien.
	var l domain.ShareLien
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, account_id, shares_pledged, reason, reference_kind, reference_id, status, placed_by, placed_at, released_by, released_at, released_reason
		FROM share_liens WHERE id = $1 FOR UPDATE
	`, lienID).Scan(
		&l.ID, &l.TenantID, &l.AccountID, &l.SharesPledged, &l.Reason,
		&l.ReferenceKind, &l.ReferenceID, &l.Status, &l.PlacedBy, &l.PlacedAt,
		&l.ReleasedBy, &l.ReleasedAt, &l.ReleasedReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if l.Status != domain.LienActive {
		return nil, fmt.Errorf("lien is already %s", l.Status)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE share_accounts SET shares_pledged = shares_pledged - $2 WHERE id = $1
	`, l.AccountID, l.SharesPledged); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `
		UPDATE share_liens
		   SET status = 'released',
		       released_by = $2,
		       released_at = now(),
		       released_reason = $3
		 WHERE id = $1
		 RETURNING id, tenant_id, account_id, shares_pledged, reason, reference_kind, reference_id, status, placed_by, placed_at, released_by, released_at, released_reason
	`, lienID, releasedBy, reason).Scan(
		&l.ID, &l.TenantID, &l.AccountID, &l.SharesPledged, &l.Reason,
		&l.ReferenceKind, &l.ReferenceID, &l.Status, &l.PlacedBy, &l.PlacedAt,
		&l.ReleasedBy, &l.ReleasedAt, &l.ReleasedReason,
	)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *ShareStore) LiensForAccountTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, activeOnly bool) ([]domain.ShareLien, error) {
	q := `
		SELECT id, tenant_id, account_id, shares_pledged, reason, reference_kind, reference_id,
		       status, placed_by, placed_at, released_by, released_at, released_reason
		FROM share_liens WHERE account_id = $1
	`
	if activeOnly {
		q += " AND status = 'active'"
	}
	q += " ORDER BY placed_at DESC"
	rows, err := tx.Query(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ShareLien
	for rows.Next() {
		var l domain.ShareLien
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.AccountID, &l.SharesPledged, &l.Reason,
			&l.ReferenceKind, &l.ReferenceID, &l.Status, &l.PlacedBy, &l.PlacedAt,
			&l.ReleasedBy, &l.ReleasedAt, &l.ReleasedReason,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ─────────── Certificates ───────────

// IssueCertificateTx retires the current certificate (if any) and
// issues a new one reflecting the post-txn balance.
func (s *ShareStore) IssueCertificateTx(ctx context.Context, tx pgx.Tx, accountID, memberID, issuedBy uuid.UUID, shares int, parValue decimal.Decimal, prefix string) (*domain.ShareCertificate, error) {
	// Retire prior current cert.
	var priorID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE share_certificates
		   SET retired_at = now()
		 WHERE account_id = $1 AND retired_at IS NULL
		 RETURNING id
	`, accountID).Scan(&priorID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	certNo, err := nextSeq(ctx, tx, "certificate", prefix)
	if err != nil {
		return nil, err
	}
	total := decimal.NewFromInt(int64(shares)).Mul(parValue)
	var c domain.ShareCertificate
	err = tx.QueryRow(ctx, `
		INSERT INTO share_certificates (
			tenant_id, account_id, member_id, certificate_no, shares_covered,
			par_value_at_issue, total_value, supersedes_id, issued_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8
		)
		RETURNING id, tenant_id, account_id, member_id, certificate_no, shares_covered,
		          par_value_at_issue, total_value, issued_at, retired_at, supersedes_id, issued_by
	`, accountID, memberID, certNo, shares, parValue, total, priorID, issuedBy).Scan(
		&c.ID, &c.TenantID, &c.AccountID, &c.MemberID, &c.CertificateNo, &c.SharesCovered,
		&c.ParValueAtIssue, &c.TotalValue, &c.IssuedAt, &c.RetiredAt, &c.SupersedesID, &c.IssuedBy,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *ShareStore) CurrentCertificateTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (*domain.ShareCertificate, error) {
	var c domain.ShareCertificate
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, account_id, member_id, certificate_no, shares_covered,
		       par_value_at_issue, total_value, issued_at, retired_at, supersedes_id, issued_by
		FROM share_certificates
		WHERE account_id = $1 AND retired_at IS NULL
	`, accountID).Scan(
		&c.ID, &c.TenantID, &c.AccountID, &c.MemberID, &c.CertificateNo, &c.SharesCovered,
		&c.ParValueAtIssue, &c.TotalValue, &c.IssuedAt, &c.RetiredAt, &c.SupersedesID, &c.IssuedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
