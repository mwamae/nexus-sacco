// Loan reports — portfolio + aging-with-provisioning + member history
// + maturing + restructured + written-off + CRB submission.
//
// Each report is a single read-side query (no writes). The handlers
// just wrap these into JSON responses.

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

type LoanReportsStore struct {
	pool *pgxpool.Pool
}

func NewLoanReportsStore(pool *pgxpool.Pool) *LoanReportsStore {
	return &LoanReportsStore{pool: pool}
}

// ─────────── Portfolio summary ───────────

type PortfolioSummary struct {
	TotalLoansLifetime    int             `json:"total_loans_lifetime"`
	TotalDisbursedLifetime decimal.Decimal `json:"total_disbursed_lifetime"`
	TotalOutstanding      decimal.Decimal `json:"total_outstanding"`
	PrincipalOutstanding  decimal.Decimal `json:"principal_outstanding"`
	InterestReceivable    decimal.Decimal `json:"interest_receivable"`
	FeesReceivable        decimal.Decimal `json:"fees_receivable"`
	PenaltyReceivable     decimal.Decimal `json:"penalty_receivable"`

	ActiveLoans       int `json:"active_loans"`
	InArrearsLoans    int `json:"in_arrears_loans"`
	RestructuredLoans int `json:"restructured_loans"`
	SettledLoans      int `json:"settled_loans"`
	WrittenOffLoans   int `json:"written_off_loans"`

	ByProduct []ProductPortfolioRow `json:"by_product"`
	ByStatus  []StatusPortfolioRow  `json:"by_status"`
}

type ProductPortfolioRow struct {
	ProductID            uuid.UUID       `json:"product_id"`
	ProductCode          string          `json:"product_code"`
	ProductName          string          `json:"product_name"`
	ActiveLoans          int             `json:"active_loans"`
	TotalOutstanding     decimal.Decimal `json:"total_outstanding"`
	PrincipalOutstanding decimal.Decimal `json:"principal_outstanding"`
}

type StatusPortfolioRow struct {
	Status      string          `json:"status"`
	LoanCount   int             `json:"loan_count"`
	Outstanding decimal.Decimal `json:"outstanding"`
}

func (s *LoanReportsStore) PortfolioSummaryTx(ctx context.Context, tx pgx.Tx) (*PortfolioSummary, error) {
	out := &PortfolioSummary{
		ByProduct: []ProductPortfolioRow{},
		ByStatus:  []StatusPortfolioRow{},
	}

	// Lifetime + outstanding totals.
	err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(principal_disbursed), 0),
			COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN principal_balance + interest_balance + fees_balance + penalty_balance ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN principal_balance ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN interest_balance ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN fees_balance ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN penalty_balance ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'in_arrears' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'restructured' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'settled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'written_off' THEN 1 ELSE 0 END), 0)
		FROM loans
	`).Scan(
		&out.TotalLoansLifetime, &out.TotalDisbursedLifetime,
		&out.TotalOutstanding, &out.PrincipalOutstanding,
		&out.InterestReceivable, &out.FeesReceivable, &out.PenaltyReceivable,
		&out.ActiveLoans, &out.InArrearsLoans, &out.RestructuredLoans,
		&out.SettledLoans, &out.WrittenOffLoans,
	)
	if err != nil {
		return nil, err
	}

	// By product.
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.code, p.name,
		       COALESCE(SUM(CASE WHEN l.status IN ('active','in_arrears','restructured') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN l.status IN ('active','in_arrears','restructured') THEN l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN l.status IN ('active','in_arrears','restructured') THEN l.principal_balance ELSE 0 END), 0)
		FROM loan_products p
		LEFT JOIN loans l ON l.product_id = p.id
		GROUP BY p.id, p.code, p.name
		ORDER BY p.category, p.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ProductPortfolioRow
		if err := rows.Scan(&r.ProductID, &r.ProductCode, &r.ProductName, &r.ActiveLoans, &r.TotalOutstanding, &r.PrincipalOutstanding); err != nil {
			return nil, err
		}
		out.ByProduct = append(out.ByProduct, r)
	}

	// By status.
	rows2, err := tx.Query(ctx, `
		SELECT status::text,
		       COUNT(*),
		       COALESCE(SUM(principal_balance + interest_balance + fees_balance + penalty_balance), 0)
		FROM loans
		GROUP BY status
		ORDER BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var r StatusPortfolioRow
		if err := rows2.Scan(&r.Status, &r.LoanCount, &r.Outstanding); err != nil {
			return nil, err
		}
		out.ByStatus = append(out.ByStatus, r)
	}
	return out, nil
}

// ─────────── Aging with provisioning ───────────

type AgingRow struct {
	Classification     string          `json:"classification"`
	LoanCount          int             `json:"loan_count"`
	PrincipalBalance   decimal.Decimal `json:"principal_balance"`
	InterestBalance    decimal.Decimal `json:"interest_balance"`
	TotalOutstanding   decimal.Decimal `json:"total_outstanding"`
	ProvisioningPct    decimal.Decimal `json:"provisioning_pct"`
	ProvisioningAmount decimal.Decimal `json:"provisioning_amount"`
}

type AgingReport struct {
	Bands              []AgingRow      `json:"bands"`
	TotalLoans         int             `json:"total_loans"`
	TotalOutstanding   decimal.Decimal `json:"total_outstanding"`
	TotalProvisioning  decimal.Decimal `json:"total_provisioning"`
	NPLLoanCount       int             `json:"npl_loan_count"`
	NPLOutstanding     decimal.Decimal `json:"npl_outstanding"`
	NPLRatioPct        decimal.Decimal `json:"npl_ratio_pct"`
}

func (s *LoanReportsStore) AgingReportTx(ctx context.Context, tx pgx.Tx) (*AgingReport, error) {
	// Provisioning percentages.
	var watchPct, subPct, doubtPct, lossPct decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT provisioning_watch_pct, provisioning_substandard_pct,
		       provisioning_doubtful_pct, provisioning_loss_pct
		FROM tenant_operations
	`).Scan(&watchPct, &subPct, &doubtPct, &lossPct); err != nil {
		return nil, err
	}
	pctFor := func(class string) decimal.Decimal {
		switch class {
		case "watch":
			return watchPct
		case "substandard":
			return subPct
		case "doubtful":
			return doubtPct
		case "loss":
			return lossPct
		}
		return decimal.Zero
	}
	rows, err := tx.Query(ctx, `
		SELECT arrears_classification,
		       COUNT(*),
		       COALESCE(SUM(principal_balance), 0),
		       COALESCE(SUM(interest_balance), 0),
		       COALESCE(SUM(principal_balance + interest_balance + fees_balance + penalty_balance), 0)
		FROM loans
		WHERE status IN ('active', 'in_arrears', 'restructured')
		GROUP BY arrears_classification
		ORDER BY
		  CASE arrears_classification
		    WHEN 'performing'  THEN 1
		    WHEN 'watch'       THEN 2
		    WHEN 'substandard' THEN 3
		    WHEN 'doubtful'    THEN 4
		    WHEN 'loss'        THEN 5
		    ELSE 6
		  END
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &AgingReport{Bands: []AgingRow{}}
	for rows.Next() {
		var r AgingRow
		if err := rows.Scan(&r.Classification, &r.LoanCount, &r.PrincipalBalance, &r.InterestBalance, &r.TotalOutstanding); err != nil {
			return nil, err
		}
		r.ProvisioningPct = pctFor(r.Classification)
		r.ProvisioningAmount = r.PrincipalBalance.Mul(r.ProvisioningPct).Div(decimal.NewFromInt(100)).Round(2)
		out.Bands = append(out.Bands, r)
		out.TotalLoans += r.LoanCount
		out.TotalOutstanding = out.TotalOutstanding.Add(r.TotalOutstanding)
		out.TotalProvisioning = out.TotalProvisioning.Add(r.ProvisioningAmount)
		switch r.Classification {
		case "substandard", "doubtful", "loss":
			out.NPLLoanCount += r.LoanCount
			out.NPLOutstanding = out.NPLOutstanding.Add(r.TotalOutstanding)
		}
	}
	if out.TotalOutstanding.GreaterThan(decimal.Zero) {
		out.NPLRatioPct = out.NPLOutstanding.Mul(decimal.NewFromInt(100)).Div(out.TotalOutstanding).Round(2)
	}
	return out, nil
}

// ─────────── Per-member loan history ───────────

type MemberLoanRow struct {
	Loan        domain.Loan `json:"loan"`
	ProductCode string      `json:"product_code"`
	ProductName string      `json:"product_name"`
}

type MemberLoanHistory struct {
	CounterpartyID            uuid.UUID       `json:"counterparty_id"`
	TotalLoansEverTaken int             `json:"total_loans_ever_taken"`
	ActiveLoans         int             `json:"active_loans"`
	TotalDisbursed      decimal.Decimal `json:"total_disbursed"`
	TotalOutstanding    decimal.Decimal `json:"total_outstanding"`
	Loans               []MemberLoanRow `json:"loans"`
}

func (s *LoanReportsStore) MemberLoanHistoryTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*MemberLoanHistory, error) {
	out := &MemberLoanHistory{CounterpartyID: memberID, Loans: []MemberLoanRow{}}
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(principal_disbursed), 0),
		       COALESCE(SUM(CASE WHEN status IN ('active','in_arrears','restructured') THEN principal_balance + interest_balance + fees_balance + penalty_balance ELSE 0 END), 0)
		FROM loans WHERE counterparty_id = $1
	`, memberID).Scan(&out.TotalLoansEverTaken, &out.ActiveLoans, &out.TotalDisbursed, &out.TotalOutstanding)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+prefixCols(loanCols, "l")+`, p.code, p.name
		FROM loans l
		JOIN loan_products p ON p.id = l.product_id
		WHERE l.counterparty_id = $1
		ORDER BY l.created_at DESC
	`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r MemberLoanRow
		dest := []any{
			&r.Loan.ID, &r.Loan.TenantID, &r.Loan.LoanNo, &r.Loan.ApplicationID, &r.Loan.CounterpartyID, &r.Loan.ProductID, &r.Loan.Status,
			&r.Loan.Principal, &r.Loan.InterestRatePct, &r.Loan.InterestMethod, &r.Loan.RepaymentMethod,
			&r.Loan.TermMonths, &r.Loan.GracePeriodMonths, &r.Loan.InstallmentCount, &r.Loan.FirstDueDate,
			&r.Loan.DisbursementChannel, &r.Loan.DisbursementTargetAccountID, &r.Loan.DisbursementRef,
			&r.Loan.TotalFeesDeducted, &r.Loan.NetDisbursed, &r.Loan.DisbursedAt, &r.Loan.DisbursedBy,
			&r.Loan.PrincipalDisbursed, &r.Loan.PrincipalRepaid, &r.Loan.PrincipalBalance,
			&r.Loan.InterestCharged, &r.Loan.InterestPaid, &r.Loan.InterestBalance,
			&r.Loan.FeesCharged, &r.Loan.FeesPaid, &r.Loan.FeesBalance,
			&r.Loan.PenaltyAccrued, &r.Loan.PenaltyPaid, &r.Loan.PenaltyBalance,
			&r.Loan.InstallmentsPaid, &r.Loan.NextInstallmentDueAt, &r.Loan.NextInstallmentAmount,
			&r.Loan.DaysPastDue, &r.Loan.ArrearsClassification, &r.Loan.LastRepaymentAt, &r.Loan.LastArrearsCalcAt,
			&r.Loan.CreatedAt, &r.Loan.UpdatedAt, &r.Loan.SettledAt, &r.Loan.WrittenOffAt, &r.Loan.ClosedAt,
			&r.ProductCode, &r.ProductName,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out.Loans = append(out.Loans, r)
	}
	return out, rows.Err()
}

// ─────────── Maturing loans ───────────

type MaturingLoanRow struct {
	Loan            domain.Loan `json:"loan"`
	MemberNo        string      `json:"member_no"`
	MemberName      string      `json:"member_name"`
	ProductName     string      `json:"product_name"`
	FinalDueDate    time.Time   `json:"final_due_date"`
	DaysUntilFinal  int         `json:"days_until_final"`
}

func (s *LoanReportsStore) MaturingLoansTx(ctx context.Context, tx pgx.Tx, withinDays int) ([]MaturingLoanRow, error) {
	if withinDays <= 0 {
		withinDays = 30
	}
	rows, err := tx.Query(ctx, `
		WITH final_dues AS (
		  SELECT loan_id, MAX(due_date) AS final_due
		  FROM loan_repayment_schedule
		  WHERE status NOT IN ('paid', 'cancelled')
		  GROUP BY loan_id
		)
		SELECT `+prefixCols(loanCols, "l")+`,
		       m.member_no, m.full_name, p.name, f.final_due
		FROM loans l
		JOIN members m ON m.counterparty_id = l.counterparty_id
		JOIN loan_products p ON p.id = l.product_id
		JOIN final_dues f ON f.loan_id = l.id
		WHERE l.status IN ('active', 'in_arrears', 'restructured')
		  AND f.final_due <= CURRENT_DATE + ($1 || ' days')::interval
		ORDER BY f.final_due ASC
	`, fmt.Sprintf("%d", withinDays))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MaturingLoanRow{}
	for rows.Next() {
		var r MaturingLoanRow
		var finalDue *time.Time
		dest := []any{
			&r.Loan.ID, &r.Loan.TenantID, &r.Loan.LoanNo, &r.Loan.ApplicationID, &r.Loan.CounterpartyID, &r.Loan.ProductID, &r.Loan.Status,
			&r.Loan.Principal, &r.Loan.InterestRatePct, &r.Loan.InterestMethod, &r.Loan.RepaymentMethod,
			&r.Loan.TermMonths, &r.Loan.GracePeriodMonths, &r.Loan.InstallmentCount, &r.Loan.FirstDueDate,
			&r.Loan.DisbursementChannel, &r.Loan.DisbursementTargetAccountID, &r.Loan.DisbursementRef,
			&r.Loan.TotalFeesDeducted, &r.Loan.NetDisbursed, &r.Loan.DisbursedAt, &r.Loan.DisbursedBy,
			&r.Loan.PrincipalDisbursed, &r.Loan.PrincipalRepaid, &r.Loan.PrincipalBalance,
			&r.Loan.InterestCharged, &r.Loan.InterestPaid, &r.Loan.InterestBalance,
			&r.Loan.FeesCharged, &r.Loan.FeesPaid, &r.Loan.FeesBalance,
			&r.Loan.PenaltyAccrued, &r.Loan.PenaltyPaid, &r.Loan.PenaltyBalance,
			&r.Loan.InstallmentsPaid, &r.Loan.NextInstallmentDueAt, &r.Loan.NextInstallmentAmount,
			&r.Loan.DaysPastDue, &r.Loan.ArrearsClassification, &r.Loan.LastRepaymentAt, &r.Loan.LastArrearsCalcAt,
			&r.Loan.CreatedAt, &r.Loan.UpdatedAt, &r.Loan.SettledAt, &r.Loan.WrittenOffAt, &r.Loan.ClosedAt,
			&r.MemberNo, &r.MemberName, &r.ProductName, &finalDue,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if finalDue != nil {
			r.FinalDueDate = *finalDue
			r.DaysUntilFinal = int(time.Until(*finalDue).Hours() / 24)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Restructured loan register ───────────

type RestructuringRegisterRow struct {
	Restructuring domain.LoanRestructuring `json:"restructuring"`
	LoanNo        string                   `json:"loan_no"`
	MemberNo      string                   `json:"member_no"`
	MemberName    string                   `json:"member_name"`
	ProductName   string                   `json:"product_name"`
}

func (s *LoanReportsStore) RestructuringRegisterTx(ctx context.Context, tx pgx.Tx, kind string) ([]RestructuringRegisterRow, error) {
	where := "WHERE 1=1"
	args := []any{}
	if kind != "" {
		where = "WHERE r.kind = $1"
		args = append(args, kind)
	}
	rows, err := tx.Query(ctx, `
		SELECT `+prefixCols(restructureCols, "r")+`,
		       l.loan_no, m.member_no, m.full_name, p.name
		FROM loan_restructurings r
		JOIN loans l ON l.id = r.loan_id
		JOIN members m ON m.counterparty_id = l.counterparty_id
		JOIN loan_products p ON p.id = l.product_id
		`+where+`
		ORDER BY r.created_at DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RestructuringRegisterRow{}
	for rows.Next() {
		var r RestructuringRegisterRow
		dest := []any{
			&r.Restructuring.ID, &r.Restructuring.TenantID, &r.Restructuring.LoanID, &r.Restructuring.Kind, &r.Restructuring.Reason,
			&r.Restructuring.PreviousPrincipalBalance, &r.Restructuring.PreviousInterestBalance,
			&r.Restructuring.PreviousTermMonths, &r.Restructuring.PreviousInterestRatePct, &r.Restructuring.PreviousRepaymentMethod, &r.Restructuring.PreviousStatus,
			&r.Restructuring.NewTermMonths, &r.Restructuring.NewInterestRatePct,
			&r.Restructuring.TopupAmount, &r.Restructuring.RefinanceNewLoanID,
			&r.Restructuring.MoratoriumMonths, &r.Restructuring.MoratoriumSuspendInterest,
			&r.Restructuring.DiscountAmount, &r.Restructuring.DiscountWriteoffTxnID,
			&r.Restructuring.WorkflowInstanceID, &r.Restructuring.AuthorizedAt, &r.Restructuring.AuthorizedBy,
			&r.Restructuring.CreatedAt, &r.Restructuring.CreatedBy,
			&r.LoanNo, &r.MemberNo, &r.MemberName, &r.ProductName,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Write-offs ───────────

type LoanWriteoff struct {
	ID                  uuid.UUID       `json:"id"`
	LoanID              uuid.UUID       `json:"loan_id"`
	CounterpartyID            uuid.UUID       `json:"counterparty_id"`
	PrincipalWrittenOff decimal.Decimal `json:"principal_written_off"`
	InterestWrittenOff  decimal.Decimal `json:"interest_written_off"`
	FeesWrittenOff      decimal.Decimal `json:"fees_written_off"`
	PenaltyWrittenOff   decimal.Decimal `json:"penalty_written_off"`
	TotalWrittenOff     decimal.Decimal `json:"total_written_off"`
	Reason              string          `json:"reason"`
	AuthorizedAt        time.Time       `json:"authorized_at"`
	AuthorizedBy        uuid.UUID       `json:"authorized_by"`
	WriteoffTxnID       *uuid.UUID      `json:"writeoff_txn_id,omitempty"`
}

type WriteoffRegisterRow struct {
	Writeoff       LoanWriteoff    `json:"writeoff"`
	LoanNo         string          `json:"loan_no"`
	MemberNo       string          `json:"member_no"`
	MemberName     string          `json:"member_name"`
	RecoveredAmount decimal.Decimal `json:"recovered_amount"`
}

// WriteOffLoanTx posts a write_off ledger transaction, zeros all loan
// balances, marks the loan written_off, and records the audit row.
type WriteOffInput struct {
	LoanID uuid.UUID
	Reason string
	By     uuid.UUID
}

func (s *LoanReportsStore) WriteOffLoanTx(
	ctx context.Context, tx pgx.Tx,
	loans *LoanStore,
	in WriteOffInput,
) (*LoanWriteoff, *domain.Loan, error) {
	loan, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	if loan.Status == domain.LoanWrittenOff || loan.Status == domain.LoanSettled || loan.Status == domain.LoanClosed {
		return nil, nil, fmt.Errorf("loan is %s — cannot write off", loan.Status)
	}
	total := loan.PrincipalBalance.Add(loan.InterestBalance).Add(loan.FeesBalance).Add(loan.PenaltyBalance)
	if total.LessThanOrEqual(decimal.Zero) {
		return nil, nil, fmt.Errorf("loan has no outstanding balance to write off")
	}
	// Post the write_off ledger row.
	ch := "internal"
	narration := "Write-off · " + in.Reason
	woTxn, err := loans.PostTxnTx(ctx, tx, PostLoanInput{
		Loan:               loan,
		TxnType:            domain.LoanTxnWriteOff,
		Amount:             total.Neg(),
		PrincipalComponent: loan.PrincipalBalance,
		InterestComponent:  loan.InterestBalance,
		FeeComponent:       loan.FeesBalance,
		PenaltyComponent:   loan.PenaltyBalance,
		Channel:            &ch,
		Narration:          &narration,
		InitiatedBy:        in.By,
		AuthorizedBy:       &in.By,
	})
	if err != nil {
		return nil, nil, err
	}
	// Snapshot balances + zero them.
	wo := &LoanWriteoff{
		LoanID:              in.LoanID,
		CounterpartyID:            loan.CounterpartyID,
		PrincipalWrittenOff: loan.PrincipalBalance,
		InterestWrittenOff:  loan.InterestBalance,
		FeesWrittenOff:      loan.FeesBalance,
		PenaltyWrittenOff:   loan.PenaltyBalance,
		TotalWrittenOff:     total,
		Reason:              in.Reason,
		AuthorizedBy:        in.By,
		WriteoffTxnID:       &woTxn.ID,
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_writeoffs (
			tenant_id, loan_id, counterparty_id,
			principal_written_off, interest_written_off, fees_written_off, penalty_written_off, total_written_off,
			reason, authorized_by, writeoff_txn_id
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		RETURNING id, authorized_at
	`, wo.LoanID, wo.CounterpartyID,
		wo.PrincipalWrittenOff, wo.InterestWrittenOff, wo.FeesWrittenOff, wo.PenaltyWrittenOff, wo.TotalWrittenOff,
		wo.Reason, wo.AuthorizedBy, wo.WriteoffTxnID)
	if err := row.Scan(&wo.ID, &wo.AuthorizedAt); err != nil {
		return nil, nil, err
	}
	// Flip loan state.
	if _, err := tx.Exec(ctx, `
		UPDATE loans SET
			status = 'written_off',
			principal_balance = 0, interest_balance = 0, fees_balance = 0, penalty_balance = 0,
			written_off_at = now()
		WHERE id = $1
	`, in.LoanID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE loan_repayment_schedule SET status = 'cancelled'
		WHERE loan_id = $1 AND status NOT IN ('paid', 'cancelled')
	`, in.LoanID); err != nil {
		return nil, nil, err
	}
	updated, err := loans.GetTx(ctx, tx, in.LoanID)
	if err != nil {
		return nil, nil, err
	}
	return wo, updated, nil
}

func (s *LoanReportsStore) WriteoffRegisterTx(ctx context.Context, tx pgx.Tx) ([]WriteoffRegisterRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT w.id, w.loan_id, w.counterparty_id,
		       w.principal_written_off, w.interest_written_off, w.fees_written_off, w.penalty_written_off, w.total_written_off,
		       w.reason, w.authorized_at, w.authorized_by, w.writeoff_txn_id,
		       l.loan_no, m.member_no, m.full_name,
		       COALESCE((SELECT SUM(amount) FROM loan_recoveries WHERE writeoff_id = w.id), 0)
		FROM loan_writeoffs w
		JOIN loans l ON l.id = w.loan_id
		JOIN members m ON m.counterparty_id = w.counterparty_id
		ORDER BY w.authorized_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WriteoffRegisterRow{}
	for rows.Next() {
		var r WriteoffRegisterRow
		var w = &r.Writeoff
		if err := rows.Scan(
			&w.ID, &w.LoanID, &w.CounterpartyID,
			&w.PrincipalWrittenOff, &w.InterestWrittenOff, &w.FeesWrittenOff, &w.PenaltyWrittenOff, &w.TotalWrittenOff,
			&w.Reason, &w.AuthorizedAt, &w.AuthorizedBy, &w.WriteoffTxnID,
			&r.LoanNo, &r.MemberNo, &r.MemberName,
			&r.RecoveredAmount,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── CRB submission (skeleton) ───────────

type CRBLoanRecord struct {
	LoanNo               string          `json:"loan_no"`
	CounterpartyID             uuid.UUID       `json:"counterparty_id"`
	MemberName           string          `json:"member_name"`
	IDDocNumber          string          `json:"id_doc_number"`
	DisbursedAt          *time.Time      `json:"disbursed_at,omitempty"`
	PrincipalDisbursed   decimal.Decimal `json:"principal_disbursed"`
	OutstandingBalance   decimal.Decimal `json:"outstanding_balance"`
	DaysPastDue          int             `json:"days_past_due"`
	Classification       string          `json:"classification"`
	IsNPL                bool            `json:"is_npl"`
}

func (s *LoanReportsStore) CRBSubmissionTx(ctx context.Context, tx pgx.Tx) ([]CRBLoanRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.loan_no, l.counterparty_id, m.full_name, m.id_doc_number,
		       l.disbursed_at, l.principal_disbursed,
		       (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance),
		       l.days_past_due, l.arrears_classification
		FROM loans l
		JOIN members m ON m.counterparty_id = l.counterparty_id
		WHERE l.status IN ('active', 'in_arrears', 'restructured', 'written_off')
		ORDER BY l.disbursed_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CRBLoanRecord{}
	for rows.Next() {
		var r CRBLoanRecord
		if err := rows.Scan(
			&r.LoanNo, &r.CounterpartyID, &r.MemberName, &r.IDDocNumber,
			&r.DisbursedAt, &r.PrincipalDisbursed, &r.OutstandingBalance,
			&r.DaysPastDue, &r.Classification,
		); err != nil {
			return nil, err
		}
		switch r.Classification {
		case "substandard", "doubtful", "loss":
			r.IsNPL = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// silence unused
var _ = errors.New
