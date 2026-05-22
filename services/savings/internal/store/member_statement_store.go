// Member 360° statement aggregator.
//
// Builds a single consolidated DTO of every financial relationship a
// member has with the SACCO: shares, deposit accounts, loans, plus a
// cross-module activity feed of recent transactions. Pure read — the
// data already lives in dedicated stores; this just stitches it
// together so the UI can serve a printable statement from one fetch.

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type MemberStatementStore struct {
	pool *pgxpool.Pool
}

func NewMemberStatementStore(pool *pgxpool.Pool) *MemberStatementStore {
	return &MemberStatementStore{pool: pool}
}

// ─────────── DTOs ───────────

type MemberStatement struct {
	MemberID    uuid.UUID                `json:"member_id"`
	GeneratedAt time.Time                `json:"generated_at"`
	Member      MemberIdentity           `json:"member"`
	Shares      *SharesSummary           `json:"shares,omitempty"`
	Deposits    MemberDepositsSummary          `json:"deposits"`
	Loans       MemberLoanHistory        `json:"loans"`
	RecentActivity []ActivityEntry       `json:"recent_activity"`
	TotalFinancialPosition decimal.Decimal `json:"total_financial_position"` // member's net stake (savings + shares − loan balances)
}

type MemberIdentity struct {
	ID            uuid.UUID `json:"id"`
	MemberNo      string    `json:"member_no"`
	FullName      string    `json:"full_name"`
	Phone         *string   `json:"phone,omitempty"`
	Email         *string   `json:"email,omitempty"`
	Status        string    `json:"status"`
	JoinedAt      *time.Time `json:"joined_at,omitempty"`
}

type SharesSummary struct {
	AccountID         uuid.UUID       `json:"account_id"`
	SharesHeld        int             `json:"shares_held"`
	ParValue          decimal.Decimal `json:"par_value"`
	BookValue         decimal.Decimal `json:"book_value"`
	CertificateNo     *string         `json:"certificate_no,omitempty"`
	CertificateIssuedAt *time.Time    `json:"certificate_issued_at,omitempty"`
}

type MemberDepositsSummary struct {
	TotalBalance decimal.Decimal       `json:"total_balance"`
	AccountCount int                   `json:"account_count"`
	Accounts     []MemberDepositAcctRow `json:"accounts"`
}

type MemberDepositAcctRow struct {
	AccountID    uuid.UUID       `json:"account_id"`
	AccountNo    string          `json:"account_no"`
	ProductCode  string          `json:"product_code"`
	ProductName  string          `json:"product_name"`
	Status       string          `json:"status"`
	Balance      decimal.Decimal `json:"balance"`
	AvailableBalance decimal.Decimal `json:"available_balance"`
	OpenedAt     time.Time       `json:"opened_at"`
}

type ActivityEntry struct {
	PostedAt    time.Time       `json:"posted_at"`
	Module      string          `json:"module"`     // 'shares' | 'deposits' | 'loans'
	Type        string          `json:"type"`       // txn_type from source table
	TxnNo       string          `json:"txn_no"`
	Reference   string          `json:"reference,omitempty"`
	Description string          `json:"description"`
	Amount      decimal.Decimal `json:"amount"`     // signed; + = inflow to member, − = outflow
	Narration   *string         `json:"narration,omitempty"`
}

// ─────────── Build ───────────

// BuildTx assembles the statement. Each section is best-effort — a
// member with no shares simply gets a null Shares; no deposits means
// an empty accounts list. The activity feed pulls the 50 most-recent
// transactions across all three modules.
func (s *MemberStatementStore) BuildTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*MemberStatement, error) {
	stmt := &MemberStatement{
		MemberID:    memberID,
		GeneratedAt: time.Now(),
		Deposits:    MemberDepositsSummary{Accounts: []MemberDepositAcctRow{}},
		Loans:       MemberLoanHistory{MemberID: memberID, Loans: []MemberLoanRow{}},
		RecentActivity: []ActivityEntry{},
	}

	// Member identity (tenant-scoped via RLS). joined_at falls back to
	// created_at since the schema doesn't have an explicit join date.
	var ident MemberIdentity
	err := tx.QueryRow(ctx, `
		SELECT id, member_no, full_name, phone, email::text, status::text, created_at
		  FROM members WHERE id = $1
	`, memberID).Scan(&ident.ID, &ident.MemberNo, &ident.FullName, &ident.Phone, &ident.Email, &ident.Status, &ident.JoinedAt)
	if err != nil {
		return nil, err
	}
	stmt.Member = ident

	// Shares.
	var (
		shareAcctID  uuid.UUID
		sharesHeld   int
		parAtOpen    decimal.Decimal
	)
	err = tx.QueryRow(ctx, `
		SELECT sa.id, sa.shares_held, sa.par_value_at_open
		  FROM share_accounts sa
		 WHERE sa.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		 LIMIT 1
	`, memberID).Scan(&shareAcctID, &sharesHeld, &parAtOpen)
	switch {
	case err == nil:
		bookValue := parAtOpen.Mul(decimal.NewFromInt(int64(sharesHeld)))
		var certNo *string
		var certIssued *time.Time
		// Active certificate = most recent without a retired_at.
		_ = tx.QueryRow(ctx, `
			SELECT certificate_no, issued_at FROM share_certificates
			 WHERE account_id = $1 AND retired_at IS NULL
			 ORDER BY issued_at DESC LIMIT 1
		`, shareAcctID).Scan(&certNo, &certIssued)
		stmt.Shares = &SharesSummary{
			AccountID: shareAcctID, SharesHeld: sharesHeld,
			ParValue: parAtOpen, BookValue: bookValue,
			CertificateNo: certNo, CertificateIssuedAt: certIssued,
		}
	case err == pgx.ErrNoRows:
		// no share account is fine
	default:
		return nil, err
	}

	// Deposit accounts.
	rows, err := tx.Query(ctx, `
		SELECT da.id, da.account_no, p.code, p.name, da.status::text,
		       da.current_balance, da.available_balance,
		       COALESCE(da.opened_at, da.created_at)
		  FROM deposit_accounts da
		  JOIN deposit_products p ON p.id = da.product_id
		 WHERE da.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		 ORDER BY COALESCE(da.opened_at, da.created_at) DESC
	`, memberID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r MemberDepositAcctRow
		if err := rows.Scan(&r.AccountID, &r.AccountNo, &r.ProductCode, &r.ProductName,
			&r.Status, &r.Balance, &r.AvailableBalance, &r.OpenedAt); err != nil {
			rows.Close()
			return nil, err
		}
		stmt.Deposits.Accounts = append(stmt.Deposits.Accounts, r)
		stmt.Deposits.TotalBalance = stmt.Deposits.TotalBalance.Add(r.Balance)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stmt.Deposits.AccountCount = len(stmt.Deposits.Accounts)

	// Loans — header summary.
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(principal_disbursed), 0),
		       COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured')
		                    THEN principal_balance + interest_balance + fees_balance + penalty_balance
		                    ELSE 0 END), 0)
		FROM loans WHERE counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
	`, memberID).Scan(&stmt.Loans.TotalLoansEverTaken, &stmt.Loans.ActiveLoans,
		&stmt.Loans.TotalDisbursed, &stmt.Loans.TotalOutstanding)
	if err != nil {
		return nil, err
	}
	// Loan list — keep this compact (top 20).
	lrows, err := tx.Query(ctx, `
		SELECT l.id, l.loan_no, l.status, l.principal, l.principal_balance,
		       l.interest_balance, l.days_past_due, l.arrears_classification,
		       l.next_installment_due_at, l.disbursed_at, l.closed_at,
		       p.code, p.name
		  FROM loans l
		  JOIN loan_products p ON p.id = l.product_id
		 WHERE l.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		 ORDER BY l.created_at DESC
		 LIMIT 20
	`, memberID)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var r MemberLoanRow
		if err := lrows.Scan(
			&r.Loan.ID, &r.Loan.LoanNo, &r.Loan.Status,
			&r.Loan.Principal, &r.Loan.PrincipalBalance,
			&r.Loan.InterestBalance, &r.Loan.DaysPastDue, &r.Loan.ArrearsClassification,
			&r.Loan.NextInstallmentDueAt, &r.Loan.DisbursedAt, &r.Loan.ClosedAt,
			&r.ProductCode, &r.ProductName,
		); err != nil {
			lrows.Close()
			return nil, err
		}
		stmt.Loans.Loans = append(stmt.Loans.Loans, r)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return nil, err
	}

	// Recent activity — pull last 50 across modules via UNION ALL.
	// Sign convention for `amount`: positive = inflow to member
	// (deposit received, share purchase increases holding, loan
	// disbursed). Negative = outflow (withdrawal, share redemption,
	// loan repayment).
	arows, err := tx.Query(ctx, `
		SELECT * FROM (
		  -- Shares
		  SELECT
		    st.posted_at AS posted_at,
		    'shares'::text AS module,
		    st.txn_type::text AS type,
		    st.txn_no AS txn_no,
		    COALESCE(st.payment_ref, '') AS reference,
		    'Share txn ' || st.txn_type::text AS description,
		    CASE WHEN st.shares_delta > 0 THEN st.amount ELSE -st.amount END AS amount,
		    st.narration AS narration
		  FROM share_transactions st
		  WHERE st.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		  UNION ALL
		  -- Deposits
		  SELECT
		    dt.posted_at AS posted_at,
		    'deposits'::text AS module,
		    dt.txn_type::text AS type,
		    dt.txn_no AS txn_no,
		    COALESCE(dt.channel_ref, '') AS reference,
		    'Deposit ' || dt.txn_type::text AS description,
		    dt.amount AS amount,
		    dt.narration AS narration
		  FROM deposit_transactions dt
		  WHERE dt.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		  UNION ALL
		  -- Loans (positive amount = increase in obligation = outflow to member when repaying;
		  --       negative amount = decrease in obligation = repayment received from member)
		  -- For the activity feed, we flip the sign so disbursement (a member inflow) shows positive
		  -- and repayment shows negative.
		  SELECT
		    lt.posted_at AS posted_at,
		    'loans'::text AS module,
		    lt.txn_type::text AS type,
		    lt.txn_no AS txn_no,
		    COALESCE(lt.channel_ref, '') AS reference,
		    'Loan ' || lt.txn_type::text AS description,
		    -lt.amount AS amount,
		    lt.narration AS narration
		  FROM loan_transactions lt
		  WHERE lt.counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1)
		) t
		ORDER BY posted_at DESC
		LIMIT 50
	`, memberID)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var a ActivityEntry
		if err := arows.Scan(&a.PostedAt, &a.Module, &a.Type, &a.TxnNo, &a.Reference, &a.Description, &a.Amount, &a.Narration); err != nil {
			arows.Close()
			return nil, err
		}
		stmt.RecentActivity = append(stmt.RecentActivity, a)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return nil, err
	}

	// Total financial position: deposits + share book value − active loan outstanding.
	pos := stmt.Deposits.TotalBalance
	if stmt.Shares != nil {
		pos = pos.Add(stmt.Shares.BookValue)
	}
	pos = pos.Sub(stmt.Loans.TotalOutstanding)
	stmt.TotalFinancialPosition = pos

	return stmt, nil
}
