// Loan + repayment schedule + loan transaction persistence (Phase 6c).
//
// On offer acceptance we create a `loans` row in 'pending_disbursement'
// status with the approved terms snapshot. On disbursement, the
// schedule is generated, the principal is moved to the target account,
// fee transactions are posted, and the loan flips to 'active'.

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

type LoanStore struct {
	pool *pgxpool.Pool
	// Optional — when set, RecalcDPDTx auto-opens a collection case
	// any time the loan slips into arrears. Wired via SetCollections
	// in main.go so we don't have to take it as a constructor arg
	// (and create a chicken-and-egg with LoanCollectionsStore).
	// Nil-safe: a test or migration that constructs LoanStore
	// directly without setting Collections still works; it just
	// doesn't bridge to the queue.
	collections *LoanCollectionsStore
}

func NewLoanStore(pool *pgxpool.Pool) *LoanStore {
	return &LoanStore{pool: pool}
}

// SetCollections attaches the collections bridge. Call this once at
// startup from main.go after both stores have been constructed.
func (s *LoanStore) SetCollections(c *LoanCollectionsStore) {
	s.collections = c
}

const loanCols = `
	id, tenant_id, loan_no, application_id, counterparty_id, product_id, status,
	principal, interest_rate_pct, interest_method, repayment_method,
	term_months, grace_period_months, installment_count, first_due_date,
	disbursement_channel, disbursement_target_account_id, disbursement_ref,
	total_fees_deducted, net_disbursed, disbursed_at, disbursed_by,
	principal_disbursed, principal_repaid, principal_balance,
	interest_charged, interest_paid, interest_balance,
	fees_charged, fees_paid, fees_balance,
	penalty_accrued, penalty_paid, penalty_balance,
	installments_paid, next_installment_due_at, next_installment_amount,
	days_past_due, arrears_classification, last_repayment_at, last_arrears_calc_at,
	created_at, updated_at, settled_at, written_off_at, closed_at
`

func scanLoan(row pgx.Row) (*domain.Loan, error) {
	var l domain.Loan
	err := row.Scan(
		&l.ID, &l.TenantID, &l.LoanNo, &l.ApplicationID, &l.CounterpartyID, &l.ProductID, &l.Status,
		&l.Principal, &l.InterestRatePct, &l.InterestMethod, &l.RepaymentMethod,
		&l.TermMonths, &l.GracePeriodMonths, &l.InstallmentCount, &l.FirstDueDate,
		&l.DisbursementChannel, &l.DisbursementTargetAccountID, &l.DisbursementRef,
		&l.TotalFeesDeducted, &l.NetDisbursed, &l.DisbursedAt, &l.DisbursedBy,
		&l.PrincipalDisbursed, &l.PrincipalRepaid, &l.PrincipalBalance,
		&l.InterestCharged, &l.InterestPaid, &l.InterestBalance,
		&l.FeesCharged, &l.FeesPaid, &l.FeesBalance,
		&l.PenaltyAccrued, &l.PenaltyPaid, &l.PenaltyBalance,
		&l.InstallmentsPaid, &l.NextInstallmentDueAt, &l.NextInstallmentAmount,
		&l.DaysPastDue, &l.ArrearsClassification, &l.LastRepaymentAt, &l.LastArrearsCalcAt,
		&l.CreatedAt, &l.UpdatedAt, &l.SettledAt, &l.WrittenOffAt, &l.ClosedAt,
	)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// CreateOnAcceptanceTx makes the loan row when the member accepts the
// offer. Status starts as 'pending_disbursement'; balances are all zero
// until DisburseTx is called.
type CreateLoanInput struct {
	ApplicationID         uuid.UUID
	CounterpartyID              uuid.UUID
	ProductID             uuid.UUID
	Principal             decimal.Decimal
	InterestRatePct       decimal.Decimal
	InterestMethod        domain.LoanInterestMethod
	RepaymentMethod       domain.LoanRepaymentMethod
	TermMonths            int
	GracePeriodMonths     int
	InstallmentCount      int
}

func (s *LoanStore) CreateOnAcceptanceTx(ctx context.Context, tx pgx.Tx, in CreateLoanInput) (*domain.Loan, error) {
	loanNo, err := nextSeq(ctx, tx, "loan", "L")
	if err != nil {
		return nil, err
	}
	// Phase D sub-PR 3: in.CounterpartyID is a counterparty.id directly
	// (caller now extracts it from the loan_applications row, which holds
	// the counterparty bridge already).
	row := tx.QueryRow(ctx, `
		INSERT INTO loans (
			tenant_id, loan_no, application_id, counterparty_id, product_id, status,
			principal, interest_rate_pct, interest_method, repayment_method,
			term_months, grace_period_months, installment_count,
			principal_balance, interest_balance, fees_balance, penalty_balance
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, 'pending_disbursement',
			$5, $6, $7, $8,
			$9, $10, $11,
			0, 0, 0, 0
		)
		RETURNING `+loanCols,
		loanNo, in.ApplicationID, in.CounterpartyID, in.ProductID,
		in.Principal, in.InterestRatePct, string(in.InterestMethod), string(in.RepaymentMethod),
		in.TermMonths, in.GracePeriodMonths, in.InstallmentCount,
	)
	return scanLoan(row)
}

func (s *LoanStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Loan, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanCols+` FROM loans WHERE id = $1`, id)
	l, err := scanLoan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

func (s *LoanStore) GetByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) (*domain.Loan, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanCols+` FROM loans WHERE application_id = $1`, appID)
	l, err := scanLoan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

type LoanListFilter struct {
	Status    string
	CounterpartyID  *uuid.UUID
	ProductID *uuid.UUID
	Q         string
	Limit     int
	Offset    int
}

type LoanListItem struct {
	Loan        domain.Loan `json:"loan"`
	MemberNo    string      `json:"member_no"`
	MemberName  string      `json:"member_name"`
	ProductCode string      `json:"product_code"`
	ProductName string      `json:"product_name"`
}

func (s *LoanStore) ListTx(ctx context.Context, tx pgx.Tx, f LoanListFilter) ([]LoanListItem, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += fmt.Sprintf(" AND l.status = $%d", idx)
		args = append(args, f.Status)
		idx++
	}
	if f.CounterpartyID != nil {
		where += fmt.Sprintf(" AND l.counterparty_id = $%d", idx)
		args = append(args, *f.CounterpartyID); idx++
	}
	if f.ProductID != nil {
		where += fmt.Sprintf(" AND l.product_id = $%d", idx)
		args = append(args, *f.ProductID); idx++
	}
	if f.Q != "" {
		where += fmt.Sprintf(" AND (cd.full_name ILIKE $%d OR cd.member_no ILIKE $%d OR cd.cp_number ILIKE $%d OR l.loan_no ILIKE $%d)", idx, idx, idx, idx)
		args = append(args, "%"+f.Q+"%"); idx++
	}
	var total int
	if err := tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM loans l JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id JOIN loan_products p ON p.id = l.product_id "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s, cd.member_no, cd.full_name, p.code, p.name
		FROM loans l
		JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
		JOIN loan_products p ON p.id = l.product_id
		%s
		ORDER BY l.created_at DESC
		LIMIT $%d OFFSET $%d
	`, prefixCols(loanCols, "l"), where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LoanListItem
	for rows.Next() {
		var it LoanListItem
		dest := []any{
			&it.Loan.ID, &it.Loan.TenantID, &it.Loan.LoanNo, &it.Loan.ApplicationID, &it.Loan.CounterpartyID, &it.Loan.ProductID, &it.Loan.Status,
			&it.Loan.Principal, &it.Loan.InterestRatePct, &it.Loan.InterestMethod, &it.Loan.RepaymentMethod,
			&it.Loan.TermMonths, &it.Loan.GracePeriodMonths, &it.Loan.InstallmentCount, &it.Loan.FirstDueDate,
			&it.Loan.DisbursementChannel, &it.Loan.DisbursementTargetAccountID, &it.Loan.DisbursementRef,
			&it.Loan.TotalFeesDeducted, &it.Loan.NetDisbursed, &it.Loan.DisbursedAt, &it.Loan.DisbursedBy,
			&it.Loan.PrincipalDisbursed, &it.Loan.PrincipalRepaid, &it.Loan.PrincipalBalance,
			&it.Loan.InterestCharged, &it.Loan.InterestPaid, &it.Loan.InterestBalance,
			&it.Loan.FeesCharged, &it.Loan.FeesPaid, &it.Loan.FeesBalance,
			&it.Loan.PenaltyAccrued, &it.Loan.PenaltyPaid, &it.Loan.PenaltyBalance,
			&it.Loan.InstallmentsPaid, &it.Loan.NextInstallmentDueAt, &it.Loan.NextInstallmentAmount,
			&it.Loan.DaysPastDue, &it.Loan.ArrearsClassification, &it.Loan.LastRepaymentAt, &it.Loan.LastArrearsCalcAt,
			&it.Loan.CreatedAt, &it.Loan.UpdatedAt, &it.Loan.SettledAt, &it.Loan.WrittenOffAt, &it.Loan.ClosedAt,
			&it.MemberNo, &it.MemberName, &it.ProductCode, &it.ProductName,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// SaveScheduleTx persists the generated amortisation schedule.
func (s *LoanStore) SaveScheduleTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, schedule []domain.ScheduleRow) error {
	for _, row := range schedule {
		_, err := tx.Exec(ctx, `
			INSERT INTO loan_repayment_schedule (
				tenant_id, loan_id, installment_no, due_date,
				principal_due, interest_due, fee_due, total_due, outstanding_after
			) VALUES (
				current_tenant_id(), $1, $2, $3,
				$4, $5, $6, $7, $8
			)
		`,
			loanID, row.InstallmentNo, row.DueDate,
			row.PrincipalDue, row.InterestDue, row.FeeDue, row.TotalDue, row.OutstandingAfter,
		)
		if err != nil {
			return fmt.Errorf("insert schedule row %d: %w", row.InstallmentNo, err)
		}
	}
	return nil
}

func (s *LoanStore) ScheduleByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) ([]domain.LoanInstallment, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, loan_id, installment_no, due_date,
		       principal_due, interest_due, fee_due, total_due,
		       principal_paid, interest_paid, fee_paid,
		       status, paid_at, outstanding_after,
		       accrued_at, accrued_interest_txn_id
		FROM loan_repayment_schedule
		WHERE loan_id = $1
		ORDER BY installment_no
	`, loanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanInstallment
	for rows.Next() {
		var l domain.LoanInstallment
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.LoanID, &l.InstallmentNo, &l.DueDate,
			&l.PrincipalDue, &l.InterestDue, &l.FeeDue, &l.TotalDue,
			&l.PrincipalPaid, &l.InterestPaid, &l.FeePaid,
			&l.Status, &l.PaidAt, &l.OutstandingAfter,
			&l.AccruedAt, &l.AccruedInterestTxnID,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PostTxnTx writes a loan_transactions row. Pure ledger insert; callers
// adjust the cached balances on the loans row themselves.
type PostLoanInput struct {
	Loan               *domain.Loan
	TxnType            domain.LoanTxnType
	Amount             decimal.Decimal // signed; callers compute by convention
	PrincipalComponent decimal.Decimal
	InterestComponent  decimal.Decimal
	FeeComponent       decimal.Decimal
	PenaltyComponent   decimal.Decimal
	Channel            *string
	ChannelRef         *string
	Narration          *string
	InstallmentNo      *int
	InitiatedBy        uuid.UUID
	AuthorizedBy       *uuid.UUID
	ReversesTxnID      *uuid.UUID
}

func (s *LoanStore) PostTxnTx(ctx context.Context, tx pgx.Tx, in PostLoanInput) (*domain.LoanTransaction, error) {
	txnNo, err := nextSeq(ctx, tx, "loan_txn", "LT")
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_transactions (
			tenant_id, loan_id, counterparty_id, txn_no, txn_type,
			amount, principal_component, interest_component, fee_component, penalty_component,
			channel, channel_ref, narration, installment_no,
			reverses_txn_id, initiated_by, authorized_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $16
		)
		RETURNING id, tenant_id, loan_id, counterparty_id, txn_no, txn_type,
		          amount, principal_component, interest_component, fee_component, penalty_component,
		          value_date, channel, channel_ref, narration,
		          reverses_txn_id, reversed_by_txn_id, installment_no,
		          posted_at, initiated_by, authorized_by
	`,
		in.Loan.ID, in.Loan.CounterpartyID, txnNo, string(in.TxnType),
		in.Amount, in.PrincipalComponent, in.InterestComponent, in.FeeComponent, in.PenaltyComponent,
		in.Channel, in.ChannelRef, in.Narration, in.InstallmentNo,
		in.ReversesTxnID, in.InitiatedBy, in.AuthorizedBy,
	)
	var t domain.LoanTransaction
	err = row.Scan(
		&t.ID, &t.TenantID, &t.LoanID, &t.CounterpartyID, &t.TxnNo, &t.TxnType,
		&t.Amount, &t.PrincipalComponent, &t.InterestComponent, &t.FeeComponent, &t.PenaltyComponent,
		&t.ValueDate, &t.Channel, &t.ChannelRef, &t.Narration,
		&t.ReversesTxnID, &t.ReversedByTxnID, &t.InstallmentNo,
		&t.PostedAt, &t.InitiatedBy, &t.AuthorizedBy,
	)
	return &t, err
}

// MarkDisbursedTx atomically flips the loan to 'active', stamps
// disbursement details, populates cached balances, and sets next-due.
func (s *LoanStore) MarkDisbursedTx(
	ctx context.Context, tx pgx.Tx,
	loanID uuid.UUID,
	netDisbursed, feesDeducted decimal.Decimal,
	channel, channelRef string,
	targetAccountID *uuid.UUID,
	firstDueDate time.Time, firstInstallmentAmount decimal.Decimal,
	disbursedBy uuid.UUID,
) (*domain.Loan, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loans SET
			status = 'active',
			disbursement_channel = $2,
			disbursement_target_account_id = $3,
			disbursement_ref = $4,
			net_disbursed = $5,
			total_fees_deducted = $6,
			disbursed_at = now(),
			disbursed_by = $7,
			principal_disbursed = principal,
			principal_balance = principal,
			fees_charged = $6,
			fees_balance = $6,
			next_installment_due_at = $8,
			next_installment_amount = $9,
			first_due_date = $8
		WHERE id = $1
		RETURNING `+loanCols,
		loanID, channel, targetAccountID, channelRef,
		netDisbursed, feesDeducted, disbursedBy,
		firstDueDate, firstInstallmentAmount,
	)
	return scanLoan(row)
}
