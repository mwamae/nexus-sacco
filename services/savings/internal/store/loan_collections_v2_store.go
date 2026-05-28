// Loans Phase 4 — collection-events / assignments-history / PTP-cancel
// / escalation-rules / message-templates / queue / PTP-summary.
//
// Methods hang off the existing LoanCollectionsStore (same pool, same
// pgx tx pattern). The legacy methods on that store keep working
// unchanged.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

// ─────────── Events ───────────

// LogEventTx appends one row to loan_collection_events. case_id is
// optional (system events with no enclosing case pass nil); loan_id
// is required.
func (s *LoanCollectionsStore) LogEventTx(
	ctx context.Context, tx pgx.Tx,
	loanID uuid.UUID, caseID *uuid.UUID,
	kind domain.CollectionEventKind, createdBy *uuid.UUID,
	details json.RawMessage,
	letterKind *domain.CollectionLetterKind,
	amount *decimal.Decimal,
	promisedDate *time.Time,
) (*domain.CollectionEvent, error) {
	if !kind.Valid() {
		return nil, domain.ErrInvalidEventKind
	}
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	var letterKindArg any
	if letterKind != nil {
		letterKindArg = string(*letterKind)
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_collection_events (
		  tenant_id, case_id, loan_id, kind, created_by, details,
		  letter_kind, amount, promised_date
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, tenant_id, case_id, loan_id, kind, occurred_at,
		          created_by, details, letter_kind, amount, promised_date
	`, caseID, loanID, string(kind), createdBy, details,
		letterKindArg, amount, promisedDate)

	return scanEvent(row)
}

func scanEvent(row pgx.Row) (*domain.CollectionEvent, error) {
	var e domain.CollectionEvent
	var letterKindStr *string
	if err := row.Scan(
		&e.ID, &e.TenantID, &e.CaseID, &e.LoanID, &e.Kind, &e.OccurredAt,
		&e.CreatedBy, &e.Details, &letterKindStr, &e.Amount, &e.PromisedDate,
	); err != nil {
		return nil, err
	}
	if letterKindStr != nil {
		lk := domain.CollectionLetterKind(*letterKindStr)
		e.LetterKind = &lk
	}
	return &e, nil
}

// EventsByLoanTx returns events ordered most-recent-first, limited.
func (s *LoanCollectionsStore) EventsByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, limit int) ([]domain.CollectionEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, case_id, loan_id, kind, occurred_at,
		       created_by, details, letter_kind, amount, promised_date
		  FROM loan_collection_events
		 WHERE loan_id = $1
		 ORDER BY occurred_at DESC
		 LIMIT $2
	`, loanID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollectionEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// HasRecentEventTx is the idempotency check for the auto-SMS + letter
// worker. Returns true if any event of `kind` (optionally with
// matching letter_kind) was recorded for this loan within `since`.
func (s *LoanCollectionsStore) HasRecentEventTx(
	ctx context.Context, tx pgx.Tx,
	loanID uuid.UUID, kind domain.CollectionEventKind,
	letterKind *domain.CollectionLetterKind, since time.Duration,
) (bool, error) {
	var exists bool
	args := []any{loanID, string(kind), since.String()}
	query := `
		SELECT EXISTS (
		  SELECT 1 FROM loan_collection_events
		   WHERE loan_id = $1
		     AND kind = $2
		     AND occurred_at >= now() - ($3::interval)
	`
	if letterKind != nil {
		query += ` AND letter_kind = $4`
		args = append(args, string(*letterKind))
	}
	query += `)`
	if err := tx.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ─────────── PTP cancel + auto-resolve ───────────

// CancelPTPTx flips an open PTP to 'cancelled' with a reason.
// The handler emits a ptp_cancelled event after this call.
func (s *LoanCollectionsStore) CancelPTPTx(
	ctx context.Context, tx pgx.Tx,
	ptpID uuid.UUID, reason string, byUser uuid.UUID,
) (*domain.PromiseToPay, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_promises_to_pay
		   SET status = 'cancelled',
		       resolved_at = now(),
		       resolved_by = $2,
		       cancel_reason = $3
		 WHERE id = $1 AND status = 'open'
		 RETURNING `+ptpCols+`, cancel_reason
	`, ptpID, byUser, reason)
	// Manual scan — we appended cancel_reason which scanPTP doesn't read.
	var p domain.PromiseToPay
	var cancelReason *string
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.CaseID, &p.LoanID, &p.PromisedAmount,
		&p.PromisedDate, &p.PromisedChannel, &p.Status, &p.PaidAmount,
		&p.PaidTxnID, &p.ResolvedAt, &p.ResolvedBy, &p.Notes,
		&p.CreatedAt, &p.CreatedBy, &cancelReason,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPTPNotOpen
		}
		return nil, err
	}
	return &p, nil
}

// AutoResolveOpenPTPOnRepaymentTx is called from PostRepaymentTx
// after the repayment is allocated. If the loan has an open PTP and
// the cumulative payments since the PTP was created cover the
// promised amount on/before promised_date, it's marked 'kept' and a
// ptp_kept event is emitted. Returns the resolved PTP (or nil if no
// open PTP or not yet fulfilled).
//
// "Cumulative payments since PTP creation" = sum of loan_transactions
// of type repayment with posted_at >= ptp.created_at. The legacy
// ResolvePTPTx writes paid_amount; we update that with the running
// total each repayment.
func (s *LoanCollectionsStore) AutoResolveOpenPTPOnRepaymentTx(
	ctx context.Context, tx pgx.Tx,
	loanID uuid.UUID, repaymentTxnID uuid.UUID, repaymentAmount decimal.Decimal,
	asOf time.Time,
) (*domain.PromiseToPay, error) {
	// Find the (single) open PTP for this loan, if any.
	var ptpID uuid.UUID
	var promised decimal.Decimal
	var promisedDate time.Time
	var caseID uuid.UUID
	var paidSoFar decimal.Decimal
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, promised_amount, promised_date, case_id, paid_amount, created_at
		  FROM loan_promises_to_pay
		 WHERE loan_id = $1 AND status = 'open'
		 ORDER BY created_at DESC LIMIT 1
	`, loanID).Scan(&ptpID, &promised, &promisedDate, &caseID, &paidSoFar, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	newPaid := paidSoFar.Add(repaymentAmount)
	covered := newPaid.GreaterThanOrEqual(promised)
	onTime := !asOf.After(promisedDate.AddDate(0, 0, 1)) // grace = next-day

	newStatus := "open"
	if covered && onTime {
		newStatus = "kept"
	} else if covered && !onTime {
		newStatus = "partial" // late but full — legacy status
	}

	// Always update paid_amount + paid_txn_id (most-recent fulfilling txn).
	row := tx.QueryRow(ctx, `
		UPDATE loan_promises_to_pay
		   SET paid_amount = $2,
		       paid_txn_id = COALESCE(paid_txn_id, $3),
		       status      = $4,
		       resolved_at = CASE WHEN $4 <> 'open' THEN now() ELSE resolved_at END
		 WHERE id = $1
		 RETURNING `+ptpCols,
		ptpID, newPaid, repaymentTxnID, newStatus)
	p, err := scanPTP(row)
	if err != nil {
		return nil, err
	}
	// Caller (handler/store) emits the event row after this call. The
	// returned PTP carries the new status — caller checks p.Status == 'kept'.
	return p, nil
}

// ─────────── Assignment history ───────────

// ReassignTx atomically:
//   - closes any existing open loan_assignment_history row for the case
//   - inserts a new open row for officerID
//   - updates loan_collection_cases.assigned_to / assigned_at
//
// Idempotent against re-assigning to the SAME officer: no-op except
// for emitting the assigned event (caller decides).
func (s *LoanCollectionsStore) ReassignTx(
	ctx context.Context, tx pgx.Tx,
	caseID, loanID, officerID, byUser uuid.UUID,
) (*domain.LoanAssignment, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE loan_assignment_history
		   SET ended_at = now(), ended_by = $2, end_reason = 'reassigned'
		 WHERE case_id = $1 AND ended_at IS NULL
	`, caseID, byUser); err != nil {
		return nil, fmt.Errorf("close prior assignment: %w", err)
	}

	var asgn domain.LoanAssignment
	if err := tx.QueryRow(ctx, `
		INSERT INTO loan_assignment_history (
		  tenant_id, case_id, loan_id, officer_id, assigned_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4)
		RETURNING id, tenant_id, case_id, loan_id, officer_id,
		          assigned_at, assigned_by, ended_at, ended_by, end_reason
	`, caseID, loanID, officerID, byUser).Scan(
		&asgn.ID, &asgn.TenantID, &asgn.CaseID, &asgn.LoanID, &asgn.OfficerID,
		&asgn.AssignedAt, &asgn.AssignedBy, &asgn.EndedAt, &asgn.EndedBy, &asgn.EndReason,
	); err != nil {
		return nil, fmt.Errorf("insert new assignment: %w", err)
	}

	// Mirror the current assignment onto the case row (legacy field).
	if _, err := tx.Exec(ctx, `
		UPDATE loan_collection_cases
		   SET assigned_to = $2, assigned_at = now(),
		       status = CASE WHEN status = 'open' THEN 'in_progress'::loan_collection_case_status ELSE status END
		 WHERE id = $1
	`, caseID, officerID); err != nil {
		return nil, err
	}

	return &asgn, nil
}

// UnassignTx clears the current assignment on a case.
func (s *LoanCollectionsStore) UnassignTx(
	ctx context.Context, tx pgx.Tx,
	caseID, byUser uuid.UUID, reason string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE loan_assignment_history
		   SET ended_at = now(), ended_by = $2, end_reason = $3
		 WHERE case_id = $1 AND ended_at IS NULL
	`, caseID, byUser, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE loan_collection_cases
		   SET assigned_to = NULL, assigned_at = NULL
		 WHERE id = $1
	`, caseID)
	return err
}

// ─────────── Escalation rules + message templates ───────────

func (s *LoanCollectionsStore) EscalationRulesTx(ctx context.Context, tx pgx.Tx) ([]domain.EscalationRule, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, dpd_min, dpd_max, required_role, letter_kind, auto_sms, description
		  FROM collections_escalation_rules
		 ORDER BY dpd_min
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.EscalationRule
	for rows.Next() {
		var r domain.EscalationRule
		var letterKindStr *string
		if err := rows.Scan(&r.TenantID, &r.DPDMin, &r.DPDMax, &r.RequiredRole, &letterKindStr, &r.AutoSMS, &r.Description); err != nil {
			return nil, err
		}
		if letterKindStr != nil {
			lk := domain.CollectionLetterKind(*letterKindStr)
			r.LetterKind = &lk
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *LoanCollectionsStore) MessageTemplateForTx(
	ctx context.Context, tx pgx.Tx, channel string, dpdDays int,
) (*domain.CollectionMessageTemplate, error) {
	row := tx.QueryRow(ctx, `
		SELECT tenant_id, channel, dpd_min, body_template, subject, updated_at
		  FROM collections_message_templates
		 WHERE channel = $1 AND dpd_min <= $2
		 ORDER BY dpd_min DESC
		 LIMIT 1
	`, channel, dpdDays)
	var t domain.CollectionMessageTemplate
	if err := row.Scan(&t.TenantID, &t.Channel, &t.DPDMin, &t.BodyTemplate, &t.Subject, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// ─────────── PTP summary ───────────

type PTPSummary struct {
	Open       int `json:"open"`
	Kept       int `json:"kept"`
	Broken     int `json:"broken"`
	Cancelled  int `json:"cancelled"`
	DueThisWeek int `json:"due_this_week"`
	Overdue    int `json:"overdue"`
}

func (s *LoanCollectionsStore) PTPSummaryTx(ctx context.Context, tx pgx.Tx) (*PTPSummary, error) {
	var sum PTPSummary
	err := tx.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE status = 'open'),
		  count(*) FILTER (WHERE status = 'kept'),
		  count(*) FILTER (WHERE status = 'broken'),
		  count(*) FILTER (WHERE status = 'cancelled'),
		  count(*) FILTER (WHERE status = 'open' AND promised_date <= CURRENT_DATE + INTERVAL '7 days' AND promised_date >= CURRENT_DATE),
		  count(*) FILTER (WHERE status = 'open' AND promised_date < CURRENT_DATE)
		  FROM loan_promises_to_pay
	`).Scan(&sum.Open, &sum.Kept, &sum.Broken, &sum.Cancelled, &sum.DueThisWeek, &sum.Overdue)
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

// ─────────── Collections queue (Phase 4 spec) ───────────

type QueueFilter struct {
	OfficerID   *uuid.UUID
	Unassigned  bool
	DPDMin      *int
	DPDMax      *int
	PTPStatus   string // open | kept | broken | "" (any)
	ProductID   *uuid.UUID
	Limit, Offset int
}

type QueueRow struct {
	LoanID            uuid.UUID  `json:"loan_id"`
	LoanNo            string     `json:"loan_no"`
	MemberName        string     `json:"member_name"`
	OutstandingPrincipal string  `json:"outstanding_principal"`
	OutstandingTotal  string     `json:"outstanding_total"`
	DPDDays           int        `json:"dpd_days"`
	Classification    string     `json:"classification"`
	AssignedOfficer   *uuid.UUID `json:"assigned_officer,omitempty"`
	OpenPTPStatus     *string    `json:"open_ptp_status,omitempty"`
	OpenPTPDate       *time.Time `json:"open_ptp_date,omitempty"`
	LastEventKind     *string    `json:"last_event_kind,omitempty"`
	LastEventAt       *time.Time `json:"last_event_at,omitempty"`
	CaseID            *uuid.UUID `json:"case_id,omitempty"`
	CasePriority      int        `json:"case_priority"`
}

func (s *LoanCollectionsStore) QueueTx(ctx context.Context, tx pgx.Tx, f QueueFilter) ([]QueueRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := []string{"l.status IN ('active','in_arrears','restructured')"}
	args := []any{}
	idx := 1
	if f.DPDMin != nil {
		where = append(where, fmt.Sprintf("COALESCE(ls.dpd_days, GREATEST(0, (CURRENT_DATE - l.next_installment_due_at))::int) >= $%d", idx))
		args = append(args, *f.DPDMin)
		idx++
	}
	if f.DPDMax != nil {
		where = append(where, fmt.Sprintf("COALESCE(ls.dpd_days, GREATEST(0, (CURRENT_DATE - l.next_installment_due_at))::int) <= $%d", idx))
		args = append(args, *f.DPDMax)
		idx++
	}
	if f.OfficerID != nil {
		where = append(where, fmt.Sprintf("c.assigned_to = $%d", idx))
		args = append(args, *f.OfficerID)
		idx++
	} else if f.Unassigned {
		where = append(where, "c.assigned_to IS NULL")
	}
	if f.ProductID != nil {
		where = append(where, fmt.Sprintf("l.product_id = $%d", idx))
		args = append(args, *f.ProductID)
		idx++
	}
	if f.PTPStatus != "" {
		where = append(where, fmt.Sprintf(`EXISTS (
		  SELECT 1 FROM loan_promises_to_pay p
		   WHERE p.loan_id = l.id AND p.status = $%d)`, idx))
		args = append(args, f.PTPStatus)
		idx++
	}

	whereSQL := "WHERE " + joinAnd(where)
	args = append(args, f.Limit, f.Offset)
	query := fmt.Sprintf(`
		WITH latest_snap AS (
		  SELECT DISTINCT ON (loan_id) loan_id, dpd_days, classification_sasra
		    FROM loan_dpd_snapshots
		   ORDER BY loan_id, snapshot_date DESC
		), last_ev AS (
		  SELECT DISTINCT ON (loan_id) loan_id, kind, occurred_at
		    FROM loan_collection_events
		   ORDER BY loan_id, occurred_at DESC
		)
		SELECT l.id, l.loan_no, cd.full_name,
		       l.principal_balance::text,
		       (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance)::text,
		       COALESCE(ls.dpd_days, GREATEST(0, (CURRENT_DATE - l.next_installment_due_at))::int) AS dpd,
		       COALESCE(ls.classification_sasra, l.arrears_classification, 'performing'),
		       c.assigned_to,
		       (SELECT status::text FROM loan_promises_to_pay WHERE loan_id = l.id AND status = 'open' LIMIT 1),
		       (SELECT promised_date FROM loan_promises_to_pay WHERE loan_id = l.id AND status = 'open' LIMIT 1),
		       last_ev.kind::text, last_ev.occurred_at,
		       c.id, COALESCE(c.priority, 0)
		  FROM loans l
		  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
		  LEFT JOIN latest_snap ls ON ls.loan_id = l.id
		  LEFT JOIN loan_collection_cases c ON c.loan_id = l.id
		  LEFT JOIN last_ev ON last_ev.loan_id = l.id
		 %s
		 ORDER BY dpd DESC, c.priority DESC NULLS LAST, l.loan_no
		 LIMIT $%d OFFSET $%d
	`, whereSQL, idx, idx+1)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueRow
	for rows.Next() {
		var q QueueRow
		if err := rows.Scan(
			&q.LoanID, &q.LoanNo, &q.MemberName,
			&q.OutstandingPrincipal, &q.OutstandingTotal,
			&q.DPDDays, &q.Classification,
			&q.AssignedOfficer,
			&q.OpenPTPStatus, &q.OpenPTPDate,
			&q.LastEventKind, &q.LastEventAt,
			&q.CaseID, &q.CasePriority,
		); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func joinAnd(parts []string) string {
	if len(parts) == 0 {
		return "1=1"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " AND " + p
	}
	return out
}
