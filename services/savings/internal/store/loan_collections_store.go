// Collections store — cases, contacts, PTPs. Hooks into the DPD job
// to auto-create a case the first time a loan crosses into arrears.

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

type LoanCollectionsStore struct {
	pool *pgxpool.Pool
}

func NewLoanCollectionsStore(pool *pgxpool.Pool) *LoanCollectionsStore {
	return &LoanCollectionsStore{pool: pool}
}

// ─────────── Cases ───────────

const caseCols = `
	id, tenant_id, loan_id, counterparty_id, status, classification_at_open,
	assigned_to, assigned_at, priority, total_contacts, last_contact_at, last_action, notes,
	opened_at, closed_at, closed_by, closure_reason
`

func scanCase(row pgx.Row) (*domain.CollectionCase, error) {
	var c domain.CollectionCase
	err := row.Scan(
		&c.ID, &c.TenantID, &c.LoanID, &c.CounterpartyID, &c.Status, &c.ClassificationAtOpen,
		&c.AssignedTo, &c.AssignedAt, &c.Priority, &c.TotalContacts, &c.LastContactAt, &c.LastAction, &c.Notes,
		&c.OpenedAt, &c.ClosedAt, &c.ClosedBy, &c.ClosureReason,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// EnsureCaseForLoanTx is idempotent — opens a collection case if one
// doesn't already exist (in any non-closed status) for the loan, given
// the loan is in arrears. Returns the existing or newly-created case.
// Called from the DPD job whenever a loan flips to in_arrears.
func (s *LoanCollectionsStore) EnsureCaseForLoanTx(
	ctx context.Context, tx pgx.Tx,
	loan *domain.Loan,
) (*domain.CollectionCase, error) {
	// Already open?
	row := tx.QueryRow(ctx, `SELECT `+caseCols+` FROM loan_collection_cases WHERE loan_id = $1`, loan.ID)
	c, err := scanCase(row)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Open new case. Priority scaled to DPD (deeper arrears = higher priority).
	priority := loan.DaysPastDue
	if priority > 100 {
		priority = 100
	}
	row = tx.QueryRow(ctx, `
		INSERT INTO loan_collection_cases (
			tenant_id, loan_id, counterparty_id, status, classification_at_open, priority
		) VALUES (
			current_tenant_id(), $1, $2, 'open', $3, $4
		)
		RETURNING `+caseCols,
		loan.ID, loan.CounterpartyID, loan.ArrearsClassification, priority)
	return scanCase(row)
}

func (s *LoanCollectionsStore) GetCaseTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.CollectionCase, error) {
	row := tx.QueryRow(ctx, `SELECT `+caseCols+` FROM loan_collection_cases WHERE id = $1`, id)
	c, err := scanCase(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *LoanCollectionsStore) GetCaseByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) (*domain.CollectionCase, error) {
	row := tx.QueryRow(ctx, `SELECT `+caseCols+` FROM loan_collection_cases WHERE loan_id = $1`, loanID)
	c, err := scanCase(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

type CaseListFilter struct {
	Status     string
	AssignedTo *uuid.UUID
	Unassigned bool
	Limit      int
	Offset     int
}

type CaseListItem struct {
	Case        domain.CollectionCase `json:"case"`
	Loan        domain.Loan           `json:"loan"`
	MemberNo    string                `json:"member_no"`
	MemberName  string                `json:"member_name"`
	ProductCode string                `json:"product_code"`
	OpenPTPs    int                   `json:"open_ptps"`
}

func (s *LoanCollectionsStore) ListCasesTx(ctx context.Context, tx pgx.Tx, f CaseListFilter) ([]CaseListItem, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += fmt.Sprintf(" AND c.status = $%d", idx)
		args = append(args, f.Status); idx++
	}
	if f.Unassigned {
		where += " AND c.assigned_to IS NULL"
	} else if f.AssignedTo != nil {
		where += fmt.Sprintf(" AND c.assigned_to = $%d", idx)
		args = append(args, *f.AssignedTo); idx++
	}
	var total int
	if err := tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM loan_collection_cases c "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s,
		       %s,
		       m.member_no, m.full_name, p.code,
		       (SELECT COUNT(*) FROM loan_promises_to_pay WHERE case_id = c.id AND status = 'open') AS open_ptps
		FROM loan_collection_cases c
		JOIN loans l ON l.id = c.loan_id
		JOIN members m ON m.id = c.counterparty_id
		JOIN loan_products p ON p.id = l.product_id
		%s
		ORDER BY c.priority DESC, c.opened_at
		LIMIT $%d OFFSET $%d
	`, prefixCols(caseCols, "c"), prefixCols(loanCols, "l"), where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []CaseListItem
	for rows.Next() {
		var it CaseListItem
		dest := []any{
			&it.Case.ID, &it.Case.TenantID, &it.Case.LoanID, &it.Case.CounterpartyID, &it.Case.Status, &it.Case.ClassificationAtOpen,
			&it.Case.AssignedTo, &it.Case.AssignedAt, &it.Case.Priority, &it.Case.TotalContacts, &it.Case.LastContactAt, &it.Case.LastAction, &it.Case.Notes,
			&it.Case.OpenedAt, &it.Case.ClosedAt, &it.Case.ClosedBy, &it.Case.ClosureReason,
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
			&it.MemberNo, &it.MemberName, &it.ProductCode, &it.OpenPTPs,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// AssignTx updates the assignee + flips status to in_progress.
func (s *LoanCollectionsStore) AssignTx(ctx context.Context, tx pgx.Tx, caseID, officerID uuid.UUID) (*domain.CollectionCase, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collection_cases
		   SET assigned_to = $2,
		       assigned_at = now(),
		       status = CASE WHEN status = 'open' THEN 'in_progress'::loan_collection_case_status ELSE status END
		 WHERE id = $1
		 RETURNING `+caseCols, caseID, officerID)
	return scanCase(row)
}

// CloseCaseTx marks the case closed with a reason.
func (s *LoanCollectionsStore) CloseCaseTx(ctx context.Context, tx pgx.Tx, caseID, byUser uuid.UUID, recovered bool, reason string) (*domain.CollectionCase, error) {
	finalStatus := domain.CaseClosedUncollectable
	if recovered {
		finalStatus = domain.CaseClosedRecovered
	}
	row := tx.QueryRow(ctx, `
		UPDATE loan_collection_cases
		   SET status = $2, closed_at = now(), closed_by = $3, closure_reason = $4
		 WHERE id = $1
		 RETURNING `+caseCols, caseID, string(finalStatus), byUser, reason)
	return scanCase(row)
}

// AutoCloseIfRecoveredTx — called after a repayment. If the loan no
// longer has overdue installments, close any open case as recovered.
func (s *LoanCollectionsStore) AutoCloseIfRecoveredTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) error {
	// Find the open case (if any) and the loan's current DPD.
	var caseID uuid.UUID
	var dpd int
	err := tx.QueryRow(ctx, `
		SELECT c.id, l.days_past_due
		FROM loan_collection_cases c
		JOIN loans l ON l.id = c.loan_id
		WHERE c.loan_id = $1 AND c.status IN ('open', 'in_progress', 'paused')
	`, loanID).Scan(&caseID, &dpd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if dpd > 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE loan_collection_cases
		   SET status = 'closed_recovered',
		       closed_at = now(),
		       closure_reason = COALESCE(closure_reason, 'Loan back to performing after repayment')
		 WHERE id = $1
	`, caseID)
	return err
}

// ─────────── Contact attempts ───────────

func (s *LoanCollectionsStore) LogContactTx(ctx context.Context, tx pgx.Tx, c *domain.CollectionContact, actionLabel string) (*domain.CollectionContact, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_collection_contacts (
			tenant_id, case_id, kind, outcome, note, gps_lat, gps_lng, contacted_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5, $6, $7
		)
		RETURNING id, tenant_id, case_id, kind, outcome, note, gps_lat, gps_lng, contacted_at, contacted_by
	`, c.CaseID, string(c.Kind), string(c.Outcome), c.Note, c.GPSLat, c.GPSLng, c.ContactedBy)
	var out domain.CollectionContact
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.CaseID, &out.Kind, &out.Outcome, &out.Note,
		&out.GPSLat, &out.GPSLng, &out.ContactedAt, &out.ContactedBy,
	); err != nil {
		return nil, err
	}
	// Bump the case stats.
	if _, err := tx.Exec(ctx, `
		UPDATE loan_collection_cases
		   SET total_contacts = total_contacts + 1,
		       last_contact_at = now(),
		       last_action = $2
		 WHERE id = $1
	`, c.CaseID, actionLabel); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *LoanCollectionsStore) ContactsByCaseTx(ctx context.Context, tx pgx.Tx, caseID uuid.UUID) ([]domain.CollectionContact, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, case_id, kind, outcome, note, gps_lat, gps_lng, contacted_at, contacted_by
		FROM loan_collection_contacts
		WHERE case_id = $1
		ORDER BY contacted_at DESC
	`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollectionContact
	for rows.Next() {
		var c domain.CollectionContact
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.CaseID, &c.Kind, &c.Outcome, &c.Note,
			&c.GPSLat, &c.GPSLng, &c.ContactedAt, &c.ContactedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─────────── Promises to Pay ───────────

const ptpCols = `
	id, tenant_id, case_id, loan_id, promised_amount, promised_date, promised_channel,
	status, paid_amount, paid_txn_id, resolved_at, resolved_by, notes, created_at, created_by
`

func scanPTP(row pgx.Row) (*domain.PromiseToPay, error) {
	var p domain.PromiseToPay
	err := row.Scan(
		&p.ID, &p.TenantID, &p.CaseID, &p.LoanID, &p.PromisedAmount, &p.PromisedDate, &p.PromisedChannel,
		&p.Status, &p.PaidAmount, &p.PaidTxnID, &p.ResolvedAt, &p.ResolvedBy, &p.Notes, &p.CreatedAt, &p.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *LoanCollectionsStore) CreatePTPTx(ctx context.Context, tx pgx.Tx, p *domain.PromiseToPay) (*domain.PromiseToPay, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_promises_to_pay (
			tenant_id, case_id, loan_id, promised_amount, promised_date, promised_channel,
			notes, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5, $6, $7
		)
		RETURNING `+ptpCols,
		p.CaseID, p.LoanID, p.PromisedAmount, p.PromisedDate, p.PromisedChannel,
		p.Notes, p.CreatedBy)
	return scanPTP(row)
}

func (s *LoanCollectionsStore) PTPsByCaseTx(ctx context.Context, tx pgx.Tx, caseID uuid.UUID) ([]domain.PromiseToPay, error) {
	rows, err := tx.Query(ctx, `SELECT `+ptpCols+` FROM loan_promises_to_pay WHERE case_id = $1 ORDER BY promised_date DESC`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PromiseToPay
	for rows.Next() {
		p, err := scanPTP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ResolvePTPTx transitions an open PTP. status = kept | partial | broken | cancelled.
func (s *LoanCollectionsStore) ResolvePTPTx(
	ctx context.Context, tx pgx.Tx,
	ptpID uuid.UUID, status domain.PTPStatus,
	paidAmount decimal.Decimal, paidTxnID *uuid.UUID,
	notes *string, byUser uuid.UUID,
) (*domain.PromiseToPay, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_promises_to_pay
		   SET status = $2, paid_amount = $3, paid_txn_id = $4,
		       resolved_at = now(), resolved_by = $5, notes = COALESCE($6, notes)
		 WHERE id = $1 AND status = 'open'
		 RETURNING `+ptpCols,
		ptpID, string(status), paidAmount, paidTxnID, byUser, notes)
	p, err := scanPTP(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrPTPNotOpen
	}
	return p, err
}

// MarkOverduePTPsBroken finds all open PTPs with promised_date < asOf
// and flips them to 'broken'. Run by the daily DPD job. Returns count.
func (s *LoanCollectionsStore) MarkOverduePTPsBrokenTx(ctx context.Context, tx pgx.Tx, asOf time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_promises_to_pay
		   SET status = 'broken', resolved_at = now()
		 WHERE status = 'open' AND promised_date < $1
	`, asOf)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// EscalatePriorityOnBrokenPTPsTx bumps case priority when a PTP is broken.
// Best-effort signal that the case needs senior attention.
func (s *LoanCollectionsStore) EscalatePriorityOnBrokenPTPsTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		UPDATE loan_collection_cases c
		   SET priority = LEAST(100, priority + 10),
		       last_action = 'PTP broken — escalated priority'
		 WHERE EXISTS (
		    SELECT 1 FROM loan_promises_to_pay p
		    WHERE p.case_id = c.id AND p.status = 'broken'
		      AND p.resolved_at > now() - INTERVAL '1 day'
		 )
	`)
	return err
}
