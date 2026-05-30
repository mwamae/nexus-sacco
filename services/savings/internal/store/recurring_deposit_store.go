// Persistence for recurring_deposits + recurring_deposit_runs.
// (DSID Phase 2.2 — standing orders.)

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type RecurringDepositStore struct {
	pool *pgxpool.Pool
}

func NewRecurringDepositStore(pool *pgxpool.Pool) *RecurringDepositStore {
	return &RecurringDepositStore{pool: pool}
}

type RecurringDeposit struct {
	ID                    uuid.UUID       `json:"id"`
	TenantID              uuid.UUID       `json:"tenant_id"`
	CounterpartyID        uuid.UUID       `json:"counterparty_id"`
	TargetAccountID       uuid.UUID       `json:"target_account_id"`
	Source                string          `json:"source"`
	SourceAccountID       *uuid.UUID      `json:"source_account_id,omitempty"`
	SourceMSISDN          *string         `json:"source_msisdn,omitempty"`
	SourcePayrollEmployer *string         `json:"source_payroll_employer,omitempty"`
	Amount                decimal.Decimal `json:"amount"`
	Frequency             string          `json:"frequency"`
	StartDate             time.Time       `json:"start_date"`
	EndDate               *time.Time      `json:"end_date,omitempty"`
	NextRunAt             time.Time       `json:"next_run_at"`
	LastRunAt             *time.Time      `json:"last_run_at,omitempty"`
	ConsecutiveFailures   int             `json:"consecutive_failures"`
	Status                string          `json:"status"`
	ReasonNotes           *string         `json:"reason_notes,omitempty"`
	LastSuspendedAt       *time.Time      `json:"last_suspended_at,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	CreatedBy             uuid.UUID       `json:"created_by"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

const rdCols = `
	id, tenant_id, counterparty_id, target_account_id, source,
	source_account_id, source_msisdn, source_payroll_employer,
	amount, frequency, start_date, end_date,
	next_run_at, last_run_at, consecutive_failures,
	status, reason_notes, last_suspended_at,
	created_at, created_by, updated_at
`

func scanRD(row pgx.Row) (*RecurringDeposit, error) {
	var r RecurringDeposit
	err := row.Scan(
		&r.ID, &r.TenantID, &r.CounterpartyID, &r.TargetAccountID, &r.Source,
		&r.SourceAccountID, &r.SourceMSISDN, &r.SourcePayrollEmployer,
		&r.Amount, &r.Frequency, &r.StartDate, &r.EndDate,
		&r.NextRunAt, &r.LastRunAt, &r.ConsecutiveFailures,
		&r.Status, &r.ReasonNotes, &r.LastSuspendedAt,
		&r.CreatedAt, &r.CreatedBy, &r.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

type CreateRecurringDepositInput struct {
	TenantID              uuid.UUID
	CounterpartyID        uuid.UUID
	TargetAccountID       uuid.UUID
	Source                string
	SourceAccountID       *uuid.UUID
	SourceMSISDN          *string
	SourcePayrollEmployer *string
	Amount                decimal.Decimal
	Frequency             string
	StartDate             time.Time
	EndDate               *time.Time
	CreatedBy             uuid.UUID
}

func (s *RecurringDepositStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateRecurringDepositInput) (*RecurringDeposit, error) {
	nextRun, err := nextRunFromStart(in.StartDate, in.Frequency)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO recurring_deposits
		    (tenant_id, counterparty_id, target_account_id, source,
		     source_account_id, source_msisdn, source_payroll_employer,
		     amount, frequency, start_date, end_date,
		     next_run_at, created_by)
		VALUES ($1, $2, $3, $4::standing_order_source,
		        $5, $6, $7,
		        $8, $9::standing_order_frequency, $10, $11,
		        $12, $13)
		RETURNING `+rdCols+`
	`,
		in.TenantID, in.CounterpartyID, in.TargetAccountID, in.Source,
		in.SourceAccountID, in.SourceMSISDN, in.SourcePayrollEmployer,
		in.Amount, in.Frequency, in.StartDate, in.EndDate,
		nextRun, in.CreatedBy,
	)
	return scanRD(row)
}

func (s *RecurringDepositStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*RecurringDeposit, error) {
	return scanRD(tx.QueryRow(ctx, `SELECT `+rdCols+` FROM recurring_deposits WHERE id = $1`, id))
}

type ListRecurringDepositsFilter struct {
	CounterpartyID *uuid.UUID
	Status         string
}

func (s *RecurringDepositStore) ListTx(ctx context.Context, tx pgx.Tx, f ListRecurringDepositsFilter) ([]RecurringDeposit, error) {
	q := `SELECT ` + rdCols + ` FROM recurring_deposits WHERE 1=1`
	args := []any{}
	if f.CounterpartyID != nil {
		args = append(args, *f.CounterpartyID)
		q += fmt.Sprintf(" AND counterparty_id = $%d", len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		q += fmt.Sprintf(" AND status = $%d::standing_order_status", len(args))
	}
	q += " ORDER BY created_at DESC"
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecurringDeposit{}
	for rows.Next() {
		r, scanErr := scanRD(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// DueTx returns active standing orders whose next_run_at <= now. Used
// by the processor each tick.
func (s *RecurringDepositStore) DueTx(ctx context.Context, tx pgx.Tx, limit int) ([]RecurringDeposit, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := tx.Query(ctx, `
		SELECT `+rdCols+`
		  FROM recurring_deposits
		 WHERE status = 'active' AND next_run_at <= now()
		 ORDER BY next_run_at
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecurringDeposit{}
	for rows.Next() {
		r, scanErr := scanRD(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

type UpdateRecurringDepositInput struct {
	ID          uuid.UUID
	Amount      *decimal.Decimal
	Frequency   *string
	EndDate     *time.Time
	Status      *string
	ReasonNotes *string
}

func (s *RecurringDepositStore) UpdateTx(ctx context.Context, tx pgx.Tx, in UpdateRecurringDepositInput) (*RecurringDeposit, error) {
	set := []string{"updated_at = now()"}
	args := []any{in.ID}
	if in.Amount != nil {
		args = append(args, *in.Amount)
		set = append(set, fmt.Sprintf("amount = $%d", len(args)))
	}
	if in.Frequency != nil {
		args = append(args, *in.Frequency)
		set = append(set, fmt.Sprintf("frequency = $%d::standing_order_frequency", len(args)))
	}
	if in.EndDate != nil {
		args = append(args, *in.EndDate)
		set = append(set, fmt.Sprintf("end_date = $%d", len(args)))
	}
	if in.Status != nil {
		args = append(args, *in.Status)
		set = append(set, fmt.Sprintf("status = $%d::standing_order_status", len(args)))
		if *in.Status == "suspended" {
			set = append(set, "last_suspended_at = now()")
		}
	}
	if in.ReasonNotes != nil {
		args = append(args, *in.ReasonNotes)
		set = append(set, fmt.Sprintf("reason_notes = $%d", len(args)))
	}
	q := `UPDATE recurring_deposits SET ` + strings.Join(set, ", ") + ` WHERE id = $1 RETURNING ` + rdCols
	return scanRD(tx.QueryRow(ctx, q, args...))
}

// AdvanceNextRunTx — after a successful run, sets next_run_at +
// last_run_at and resets consecutive_failures.
func (s *RecurringDepositStore) AdvanceNextRunTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, frequency string, now time.Time) (*RecurringDeposit, error) {
	cur, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	next, err := nextRunFromAnchor(cur.NextRunAt, frequency)
	if err != nil {
		return nil, err
	}
	// If end_date elapsed, mark completed; otherwise just advance.
	finalStatus := "active"
	if cur.EndDate != nil && !cur.EndDate.After(next) {
		finalStatus = "completed"
	}
	return scanRD(tx.QueryRow(ctx, `
		UPDATE recurring_deposits
		   SET next_run_at         = $2,
		       last_run_at         = $3,
		       consecutive_failures = 0,
		       status              = $4::standing_order_status,
		       updated_at          = now()
		 WHERE id = $1
		 RETURNING `+rdCols, id, next, now, finalStatus))
}

// MarkFailureTx — after a failed run, increments consecutive_failures
// and (if over the suspend threshold) flips status='suspended'.
type FailureOpts struct {
	ID                       uuid.UUID
	NextRetryAt              time.Time
	SuspendAfterFailures     int
	ReasonNotesIfSuspended   string
}

func (s *RecurringDepositStore) MarkFailureTx(ctx context.Context, tx pgx.Tx, in FailureOpts) (*RecurringDeposit, bool, error) {
	row, err := s.GetTx(ctx, tx, in.ID)
	if err != nil {
		return nil, false, err
	}
	newFails := row.ConsecutiveFailures + 1
	if in.SuspendAfterFailures > 0 && newFails >= in.SuspendAfterFailures {
		r, err := scanRD(tx.QueryRow(ctx, `
			UPDATE recurring_deposits
			   SET consecutive_failures = $2,
			       status              = 'suspended',
			       last_suspended_at   = now(),
			       reason_notes        = COALESCE(NULLIF($3, ''), reason_notes),
			       updated_at          = now()
			 WHERE id = $1
			 RETURNING `+rdCols, in.ID, newFails, in.ReasonNotesIfSuspended))
		return r, true, err
	}
	r, err := scanRD(tx.QueryRow(ctx, `
		UPDATE recurring_deposits
		   SET consecutive_failures = $2,
		       next_run_at         = $3,
		       updated_at          = now()
		 WHERE id = $1
		 RETURNING `+rdCols, in.ID, newFails, in.NextRetryAt))
	return r, false, err
}

// ─────────── recurring_deposit_runs ───────────

type RecurringDepositRun struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenant_id"`
	StandingOrderID uuid.UUID       `json:"standing_order_id"`
	AttemptedAt     time.Time       `json:"attempted_at"`
	Amount          decimal.Decimal `json:"amount"`
	AttemptNo       int             `json:"attempt_no"`
	PeriodLabel     string          `json:"period_label"`
	Status          string          `json:"status"`
	ErrorCode       *string         `json:"error_code,omitempty"`
	ErrorMessage    *string         `json:"error_message,omitempty"`
	PostedTxnID     *uuid.UUID      `json:"posted_txn_id,omitempty"`
	NextRetryAt     *time.Time      `json:"next_retry_at,omitempty"`
}

type RecordRunInput struct {
	TenantID        uuid.UUID
	StandingOrderID uuid.UUID
	Amount          decimal.Decimal
	AttemptNo       int
	PeriodLabel     string
	Status          string
	ErrorCode       string
	ErrorMessage    string
	PostedTxnID     *uuid.UUID
	NextRetryAt     *time.Time
}

func (s *RecurringDepositStore) RecordRunTx(ctx context.Context, tx pgx.Tx, in RecordRunInput) (*RecurringDepositRun, error) {
	var r RecurringDepositRun
	err := tx.QueryRow(ctx, `
		INSERT INTO recurring_deposit_runs
		    (tenant_id, standing_order_id, amount, attempt_no, period_label,
		     status, error_code, error_message, posted_txn_id, next_retry_at)
		VALUES ($1, $2, $3, $4, $5, $6,
		        NULLIF($7, ''), NULLIF($8, ''), $9, $10)
		ON CONFLICT (standing_order_id, period_label, attempt_no) DO NOTHING
		RETURNING id, tenant_id, standing_order_id, attempted_at,
		          amount, attempt_no, period_label, status,
		          error_code, error_message, posted_txn_id, next_retry_at
	`, in.TenantID, in.StandingOrderID, in.Amount, in.AttemptNo, in.PeriodLabel,
		in.Status, in.ErrorCode, in.ErrorMessage, in.PostedTxnID, in.NextRetryAt,
	).Scan(
		&r.ID, &r.TenantID, &r.StandingOrderID, &r.AttemptedAt,
		&r.Amount, &r.AttemptNo, &r.PeriodLabel, &r.Status,
		&r.ErrorCode, &r.ErrorMessage, &r.PostedTxnID, &r.NextRetryAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already recorded for this (so_id, period, attempt) — idempotent.
		return nil, nil
	}
	return &r, err
}

func (s *RecurringDepositStore) ListRunsTx(ctx context.Context, tx pgx.Tx, soID uuid.UUID, limit int) ([]RecurringDepositRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, standing_order_id, attempted_at,
		       amount, attempt_no, period_label, status,
		       error_code, error_message, posted_txn_id, next_retry_at
		  FROM recurring_deposit_runs
		 WHERE standing_order_id = $1
		 ORDER BY attempted_at DESC
		 LIMIT $2
	`, soID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecurringDepositRun{}
	for rows.Next() {
		var r RecurringDepositRun
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.StandingOrderID, &r.AttemptedAt,
			&r.Amount, &r.AttemptNo, &r.PeriodLabel, &r.Status,
			&r.ErrorCode, &r.ErrorMessage, &r.PostedTxnID, &r.NextRetryAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastAttemptForPeriodTx returns the max attempt_no already recorded
// for (so_id, period_label). The processor uses it to pick the next
// attempt_no without racing on the UNIQUE constraint.
func (s *RecurringDepositStore) LastAttemptForPeriodTx(ctx context.Context, tx pgx.Tx, soID uuid.UUID, period string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(attempt_no), 0)
		  FROM recurring_deposit_runs
		 WHERE standing_order_id = $1 AND period_label = $2
	`, soID, period).Scan(&n)
	return n, err
}

// ─────────── Frequency arithmetic ───────────

func nextRunFromStart(start time.Time, frequency string) (time.Time, error) {
	// First run lands at the start_date (rounded to 6am UTC so the
	// worker doesn't fire at midnight before payroll is loaded).
	return time.Date(start.Year(), start.Month(), start.Day(), 6, 0, 0, 0, time.UTC), nil
}

func nextRunFromAnchor(anchor time.Time, frequency string) (time.Time, error) {
	switch frequency {
	case "weekly":
		return anchor.AddDate(0, 0, 7), nil
	case "biweekly":
		return anchor.AddDate(0, 0, 14), nil
	case "monthly":
		return anchor.AddDate(0, 1, 0), nil
	case "quarterly":
		return anchor.AddDate(0, 3, 0), nil
	}
	return time.Time{}, fmt.Errorf("unknown frequency %q", frequency)
}
