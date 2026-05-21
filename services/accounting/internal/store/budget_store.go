// Budget persistence + variance report.

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

	"github.com/nexussacco/accounting/internal/domain"
)

type BudgetStore struct {
	pool *pgxpool.Pool
}

func NewBudgetStore(pool *pgxpool.Pool) *BudgetStore {
	return &BudgetStore{pool: pool}
}

var (
	ErrBudgetNotFound     = errors.New("budget not found")
	ErrBudgetLocked       = errors.New("budget is approved/archived — lines are immutable")
	ErrAlreadyApprovedYr  = errors.New("an approved budget already exists for this fiscal year")
	ErrMakerEqualsChecker = errors.New("submitter and approver must be different users")
)

const budgetCols = `
	id, tenant_id, name, fiscal_year, period_start, period_end, status,
	total_income_budget, total_expense_budget, net_surplus_budget, notes,
	submitted_at, submitted_by, approved_at, approved_by, archived_at, archived_by,
	created_at, created_by, updated_at
`

func scanBudget(row pgx.Row) (*domain.Budget, error) {
	var b domain.Budget
	var status string
	err := row.Scan(
		&b.ID, &b.TenantID, &b.Name, &b.FiscalYear, &b.PeriodStart, &b.PeriodEnd, &status,
		&b.TotalIncomeBudget, &b.TotalExpenseBudget, &b.NetSurplusBudget, &b.Notes,
		&b.SubmittedAt, &b.SubmittedBy, &b.ApprovedAt, &b.ApprovedBy, &b.ArchivedAt, &b.ArchivedBy,
		&b.CreatedAt, &b.CreatedBy, &b.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	b.Status = domain.BudgetStatus(status)
	return &b, nil
}

type CreateBudgetInput struct {
	Name        string
	FiscalYear  int
	PeriodStart time.Time
	PeriodEnd   time.Time
	Notes       *string
	CreatedBy   uuid.UUID
}

func (s *BudgetStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateBudgetInput) (*domain.Budget, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO budgets (
		  tenant_id, name, fiscal_year, period_start, period_end, notes, created_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6)
		RETURNING `+budgetCols, in.Name, in.FiscalYear, in.PeriodStart, in.PeriodEnd, in.Notes, in.CreatedBy)
	return scanBudget(row)
}

func (s *BudgetStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Budget, error) {
	row := tx.QueryRow(ctx, `SELECT `+budgetCols+` FROM budgets WHERE id = $1`, id)
	b, err := scanBudget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBudgetNotFound
	}
	return b, err
}

func (s *BudgetStore) ListTx(ctx context.Context, tx pgx.Tx, fiscalYear int) ([]domain.Budget, error) {
	var rows pgx.Rows
	var err error
	if fiscalYear > 0 {
		rows, err = tx.Query(ctx,
			`SELECT `+budgetCols+` FROM budgets WHERE fiscal_year = $1 ORDER BY created_at DESC`,
			fiscalYear)
	} else {
		rows, err = tx.Query(ctx,
			`SELECT `+budgetCols+` FROM budgets ORDER BY fiscal_year DESC, created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Budget{}
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ─────────── Status transitions ───────────

func (s *BudgetStore) SubmitTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, userID uuid.UUID) (*domain.Budget, error) {
	b, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !domain.CanTransitionBudget(b.Status, domain.BudgetSubmitted) {
		return nil, domain.ErrIllegalBudgetTransition
	}
	// Refresh totals from lines before submission.
	if err := s.RecomputeTotalsTx(ctx, tx, id); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE budgets
		   SET status = 'submitted', submitted_at = now(), submitted_by = $2, updated_at = now()
		 WHERE id = $1
	`, id, userID)
	if err != nil {
		return nil, err
	}
	return s.GetTx(ctx, tx, id)
}

func (s *BudgetStore) ApproveTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, userID uuid.UUID) (*domain.Budget, error) {
	b, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !domain.CanTransitionBudget(b.Status, domain.BudgetApproved) {
		return nil, domain.ErrIllegalBudgetTransition
	}
	if b.SubmittedBy != nil && *b.SubmittedBy == userID {
		return nil, ErrMakerEqualsChecker
	}
	// Approve. The UNIQUE index `budgets_one_approved_per_year` will
	// bounce this if another approved budget exists for the same year.
	_, err = tx.Exec(ctx, `
		UPDATE budgets
		   SET status = 'approved', approved_at = now(), approved_by = $2, updated_at = now()
		 WHERE id = $1
	`, id, userID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyApprovedYr
		}
		return nil, err
	}
	return s.GetTx(ctx, tx, id)
}

func (s *BudgetStore) ArchiveTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, userID uuid.UUID) (*domain.Budget, error) {
	b, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !domain.CanTransitionBudget(b.Status, domain.BudgetArchived) {
		return nil, domain.ErrIllegalBudgetTransition
	}
	_, err = tx.Exec(ctx, `
		UPDATE budgets
		   SET status = 'archived', archived_at = now(), archived_by = $2, updated_at = now()
		 WHERE id = $1
	`, id, userID)
	if err != nil {
		return nil, err
	}
	return s.GetTx(ctx, tx, id)
}

func (s *BudgetStore) RecomputeTotalsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var income, expense decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN account_class = 'income'  THEN amount ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN account_class = 'expense' THEN amount ELSE 0 END), 0)
		  FROM budget_lines WHERE budget_id = $1
	`, id).Scan(&income, &expense); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE budgets
		   SET total_income_budget = $2,
		       total_expense_budget = $3,
		       net_surplus_budget = $4,
		       updated_at = now()
		 WHERE id = $1
	`, id, income, expense, income.Sub(expense))
	return err
}

// ─────────── Lines ───────────

type BudgetLineUpsert struct {
	AccountCode string
	PeriodMonth int
	Amount      decimal.Decimal
	Notes       *string
}

// UpsertLinesTx replaces a budget's lines for the given accounts.
// Only allowed when budget status is 'draft' — approved/archived
// budgets are immutable.
func (s *BudgetStore) UpsertLinesTx(ctx context.Context, tx pgx.Tx, budgetID uuid.UUID, lines []BudgetLineUpsert) error {
	b, err := s.GetTx(ctx, tx, budgetID)
	if err != nil {
		return err
	}
	if b.Status != domain.BudgetDraft {
		return ErrBudgetLocked
	}
	for _, l := range lines {
		// Resolve account id + class
		var (
			accountID   uuid.UUID
			class       string
		)
		if err := tx.QueryRow(ctx,
			`SELECT id, class FROM chart_of_accounts WHERE code = $1`, l.AccountCode,
		).Scan(&accountID, &class); err != nil {
			return fmt.Errorf("account %s: %w", l.AccountCode, err)
		}
		if class != "income" && class != "expense" {
			return fmt.Errorf("account %s is not income/expense (class=%s)", l.AccountCode, class)
		}
		if l.PeriodMonth < 1 || l.PeriodMonth > 12 {
			return fmt.Errorf("period_month must be 1-12 (got %d)", l.PeriodMonth)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_lines (
			  tenant_id, budget_id, account_id, account_code, account_class,
			  period_month, amount, notes
			) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (budget_id, account_id, period_month) DO UPDATE
			   SET amount = EXCLUDED.amount,
			       notes  = EXCLUDED.notes,
			       updated_at = now()
		`, budgetID, accountID, l.AccountCode, class, l.PeriodMonth, l.Amount, l.Notes); err != nil {
			return err
		}
	}
	return s.RecomputeTotalsTx(ctx, tx, budgetID)
}

func (s *BudgetStore) ListLinesTx(ctx context.Context, tx pgx.Tx, budgetID uuid.UUID) ([]domain.BudgetLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, budget_id, account_id, account_code, account_class,
		       period_month, amount, notes, created_at, updated_at
		  FROM budget_lines WHERE budget_id = $1
		  ORDER BY account_class, account_code, period_month
	`, budgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BudgetLine{}
	for rows.Next() {
		var l domain.BudgetLine
		if err := rows.Scan(
			&l.ID, &l.BudgetID, &l.AccountID, &l.AccountCode, &l.AccountClass,
			&l.PeriodMonth, &l.Amount, &l.Notes, &l.CreatedAt, &l.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteLinesByAccountTx removes every line for a single account
// across all periods. Lets the UI clear a row when the budgeter
// decides not to allocate to that account.
func (s *BudgetStore) DeleteLinesByAccountTx(ctx context.Context, tx pgx.Tx, budgetID, accountID uuid.UUID) error {
	b, err := s.GetTx(ctx, tx, budgetID)
	if err != nil {
		return err
	}
	if b.Status != domain.BudgetDraft {
		return ErrBudgetLocked
	}
	_, err = tx.Exec(ctx, `DELETE FROM budget_lines WHERE budget_id = $1 AND account_id = $2`, budgetID, accountID)
	if err != nil {
		return err
	}
	return s.RecomputeTotalsTx(ctx, tx, budgetID)
}

// ─────────── Variance report ───────────

type VarianceRow struct {
	AccountID    uuid.UUID       `json:"account_id"`
	AccountCode  string          `json:"account_code"`
	AccountName  string          `json:"account_name"`
	AccountClass string          `json:"account_class"`
	Budget       decimal.Decimal `json:"budget"`
	Actual       decimal.Decimal `json:"actual"`
	Variance     decimal.Decimal `json:"variance"`           // actual − budget
	VariancePct  decimal.Decimal `json:"variance_pct"`       // variance / budget × 100
	Favourable   bool            `json:"favourable"`         // income up OR expense down
}

type VarianceReport struct {
	BudgetID         uuid.UUID       `json:"budget_id"`
	From             time.Time       `json:"from"`
	To               time.Time       `json:"to"`
	Rows             []VarianceRow   `json:"rows"`
	TotalIncomeBudget   decimal.Decimal `json:"total_income_budget"`
	TotalIncomeActual   decimal.Decimal `json:"total_income_actual"`
	TotalExpenseBudget  decimal.Decimal `json:"total_expense_budget"`
	TotalExpenseActual  decimal.Decimal `json:"total_expense_actual"`
	NetSurplusBudget    decimal.Decimal `json:"net_surplus_budget"`
	NetSurplusActual    decimal.Decimal `json:"net_surplus_actual"`
	NetSurplusVariance  decimal.Decimal `json:"net_surplus_variance"`
}

// VarianceTx — for every account that appears in the budget OR has
// actual P&L activity in [from, to], compute the budget vs actual.
// Both sides use the same date-window definition (entry_date) so the
// comparison is apples-to-apples.
func (s *BudgetStore) VarianceTx(ctx context.Context, tx pgx.Tx, budgetID uuid.UUID, from, to time.Time) (*VarianceReport, error) {
	b, err := s.GetTx(ctx, tx, budgetID)
	if err != nil {
		return nil, err
	}

	// Budget and actual are computed as independent scalar subqueries.
	// If we joined both budget_lines and journal_lines onto the
	// account in the same FROM, SQL would produce a cartesian product
	// and each side's SUM would be multiplied by the cardinality of
	// the other. The subquery pattern keeps them independent.
	rows, err := tx.Query(ctx, `
		SELECT id, code, name, class, budget, actual FROM (
		  SELECT a.id, a.code, a.name, a.class,
		    COALESCE((
		      SELECT SUM(bl.amount)
		        FROM budget_lines bl
		       WHERE bl.budget_id = $4
		         AND bl.account_id = a.id
		         AND make_date($3::int, bl.period_month, 1)
		             BETWEEN date_trunc('month', $1::timestamptz)::date
		                 AND date_trunc('month', $2::timestamptz)::date
		    ), 0) AS budget,
		    COALESCE((
		      SELECT SUM(
		               CASE WHEN a.class = 'income'  THEN jl.credit - jl.debit
		                    WHEN a.class = 'expense' THEN jl.debit  - jl.credit
		                    ELSE 0 END)
		        FROM journal_lines jl
		        JOIN journal_entries je ON je.id = jl.entry_id
		       WHERE jl.account_id = a.id
		         AND je.status = 'posted'
		         AND je.entry_date BETWEEN $1::date AND $2::date
		    ), 0) AS actual
		  FROM chart_of_accounts a
		  WHERE a.class IN ('income', 'expense') AND a.is_active = true
		) t
		WHERE budget > 0 OR actual <> 0
		ORDER BY class, code
	`, from, to, b.FiscalYear, budgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rep := &VarianceReport{BudgetID: budgetID, From: from, To: to, Rows: []VarianceRow{}}
	for rows.Next() {
		var (
			row    VarianceRow
			budget decimal.Decimal
			actual decimal.Decimal
		)
		if err := rows.Scan(&row.AccountID, &row.AccountCode, &row.AccountName, &row.AccountClass, &budget, &actual); err != nil {
			return nil, err
		}
		row.Budget = budget
		row.Actual = actual
		row.Variance = actual.Sub(budget)
		if !budget.IsZero() {
			row.VariancePct = row.Variance.Div(budget).Mul(decimal.NewFromInt(100)).Round(2)
		}
		// Favourable: actual > budget for income, actual < budget for expense.
		if row.AccountClass == "income" {
			row.Favourable = row.Variance.GreaterThanOrEqual(decimal.Zero)
			rep.TotalIncomeBudget = rep.TotalIncomeBudget.Add(row.Budget)
			rep.TotalIncomeActual = rep.TotalIncomeActual.Add(row.Actual)
		} else {
			row.Favourable = row.Variance.LessThanOrEqual(decimal.Zero)
			rep.TotalExpenseBudget = rep.TotalExpenseBudget.Add(row.Budget)
			rep.TotalExpenseActual = rep.TotalExpenseActual.Add(row.Actual)
		}
		rep.Rows = append(rep.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.NetSurplusBudget = rep.TotalIncomeBudget.Sub(rep.TotalExpenseBudget)
	rep.NetSurplusActual = rep.TotalIncomeActual.Sub(rep.TotalExpenseActual)
	rep.NetSurplusVariance = rep.NetSurplusActual.Sub(rep.NetSurplusBudget)
	return rep, nil
}
