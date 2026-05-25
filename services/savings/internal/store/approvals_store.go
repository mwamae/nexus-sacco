// Pending approvals store — insert / list / transition.
//
// Toggles live on tenant_operations and are read via GetApprovalToggleTx.
// Handlers call MaybeQueueApprovalTx after validation: if the per-kind
// toggle is on, a pending_approvals row is created and the handler
// returns 202 to the caller. Otherwise the handler proceeds with its
// original ledger-posting path.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

// ErrToggleChangeReasonRequired is returned by AppendToggleChangeTx
// when a flip-to-false (or any "opt-out" / relaxation) is attempted
// without a non-empty reason. The handler maps this to a typed 400
// so the UI can render an inline error on the modal's reason field.
var ErrToggleChangeReasonRequired = errors.New("a non-empty reason is required when relaxing an approval toggle")

type ApprovalsStore struct {
	pool *pgxpool.Pool
}

func NewApprovalsStore(pool *pgxpool.Pool) *ApprovalsStore {
	return &ApprovalsStore{pool: pool}
}

const approvalCols = `
	id, tenant_id, kind, status, title,
	subject_member_id, subject_account_id, subject_loan_id, amount,
	payload, maker_user_id, maker_at, maker_note,
	checker_user_id, checker_at, checker_note,
	result_txn_id, result_error, created_at, workflow_instance_id
`

func scanApproval(row pgx.Row) (*domain.PendingApproval, error) {
	var p domain.PendingApproval
	var kind, status string
	err := row.Scan(
		&p.ID, &p.TenantID, &kind, &status, &p.Title,
		&p.SubjectMemberID, &p.SubjectAccountID, &p.SubjectLoanID, &p.Amount,
		&p.Payload, &p.MakerUserID, &p.MakerAt, &p.MakerNote,
		&p.CheckerUserID, &p.CheckerAt, &p.CheckerNote,
		&p.ResultTxnID, &p.ResultError, &p.CreatedAt, &p.WorkflowInstanceID,
	)
	if err != nil {
		return nil, err
	}
	p.Kind = domain.ApprovalKind(kind)
	p.Status = domain.ApprovalStatus(status)
	return &p, nil
}

// QueueInput captures everything needed to insert a pending approval.
type QueueInput struct {
	Kind             domain.ApprovalKind
	Title            string
	SubjectMemberID  *uuid.UUID
	SubjectAccountID *uuid.UUID
	SubjectLoanID    *uuid.UUID
	Amount           *decimal.Decimal
	Payload          any
	MakerUserID      uuid.UUID
	MakerNote        *string
}

func (s *ApprovalsStore) QueueTx(ctx context.Context, tx pgx.Tx, in QueueInput) (*domain.PendingApproval, error) {
	if !in.Kind.Valid() {
		return nil, domain.ErrApprovalKindUnknown
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO pending_approvals (
			tenant_id, kind, status, title,
			subject_member_id, subject_account_id, subject_loan_id, amount,
			payload, maker_user_id, maker_note
		) VALUES (
			current_tenant_id(), $1, 'pending', $2,
			$3, $4, $5, $6,
			$7::jsonb, $8, $9
		)
		RETURNING `+approvalCols,
		string(in.Kind), in.Title,
		in.SubjectMemberID, in.SubjectAccountID, in.SubjectLoanID, in.Amount,
		payload, in.MakerUserID, in.MakerNote,
	)
	return scanApproval(row)
}

func (s *ApprovalsStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.PendingApproval, error) {
	row := tx.QueryRow(ctx, `SELECT `+approvalCols+` FROM pending_approvals WHERE id = $1`, id)
	p, err := scanApproval(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

type ApprovalListFilter struct {
	Status        string
	Kind          string
	CounterpartyID      *uuid.UUID
	MakerUserID   *uuid.UUID
	IncludeClosed bool
	Limit         int
	Offset        int
}

func (s *ApprovalsStore) ListTx(ctx context.Context, tx pgx.Tx, f ApprovalListFilter) ([]domain.PendingApproval, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + itoa(idx)
		args = append(args, f.Status)
		idx++
	} else if !f.IncludeClosed {
		where += " AND status = 'pending'"
	}
	if f.Kind != "" {
		where += " AND kind = $" + itoa(idx)
		args = append(args, f.Kind)
		idx++
	}
	if f.CounterpartyID != nil {
		where += " AND subject_member_id = $" + itoa(idx)
		args = append(args, *f.CounterpartyID)
		idx++
	}
	if f.MakerUserID != nil {
		where += " AND maker_user_id = $" + itoa(idx)
		args = append(args, *f.MakerUserID)
		idx++
	}

	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM pending_approvals "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	q := "SELECT " + approvalCols + " FROM pending_approvals " + where +
		" ORDER BY created_at DESC LIMIT $" + itoa(idx) + " OFFSET $" + itoa(idx+1)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.PendingApproval{}
	for rows.Next() {
		p, err := scanApproval(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}

// MarkApprovedTx records the checker decision + the result txn id (if any).
func (s *ApprovalsStore) MarkApprovedTx(
	ctx context.Context, tx pgx.Tx,
	id uuid.UUID, checker uuid.UUID, note *string, resultTxnID *uuid.UUID,
) (*domain.PendingApproval, error) {
	row := tx.QueryRow(ctx, `
		UPDATE pending_approvals SET
			status = 'approved',
			checker_user_id = $2,
			checker_at = now(),
			checker_note = $3,
			result_txn_id = $4
		WHERE id = $1 AND status = 'pending'
		RETURNING `+approvalCols,
		id, checker, note, resultTxnID)
	return scanApproval(row)
}

func (s *ApprovalsStore) MarkDeclinedTx(
	ctx context.Context, tx pgx.Tx,
	id uuid.UUID, checker uuid.UUID, note *string,
) (*domain.PendingApproval, error) {
	row := tx.QueryRow(ctx, `
		UPDATE pending_approvals SET
			status = 'declined',
			checker_user_id = $2,
			checker_at = now(),
			checker_note = $3
		WHERE id = $1 AND status = 'pending'
		RETURNING `+approvalCols,
		id, checker, note)
	return scanApproval(row)
}

func (s *ApprovalsStore) MarkCancelledTx(
	ctx context.Context, tx pgx.Tx,
	id uuid.UUID, byUser uuid.UUID, note *string,
) (*domain.PendingApproval, error) {
	row := tx.QueryRow(ctx, `
		UPDATE pending_approvals SET
			status = 'cancelled',
			checker_user_id = $2,
			checker_at = now(),
			checker_note = $3
		WHERE id = $1 AND status = 'pending'
		RETURNING `+approvalCols,
		id, byUser, note)
	return scanApproval(row)
}

func (s *ApprovalsStore) MarkExecErrorTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, errMsg string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE pending_approvals SET
			status = 'execution_error',
			result_error = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

// ApprovalToggles caches the per-kind on/off settings for a tenant.
type ApprovalToggles struct {
	Deposit                 bool `json:"deposit"`
	Withdrawal              bool `json:"withdrawal"`
	DepositTransfer         bool `json:"deposit_transfer"`
	SharePurchase           bool `json:"share_purchase"`
	ShareTransfer           bool `json:"share_transfer"`
	ShareBonus              bool `json:"share_bonus"`
	ShareLien               bool `json:"share_lien"`
	LoanDisbursement        bool `json:"loan_disbursement"`
	LoanRepayment           bool `json:"loan_repayment"`
	LoanSettle              bool `json:"loan_settle"`
	LoanReverse             bool `json:"loan_reverse"`
	LoanWriteoff            bool `json:"loan_writeoff"`
	LoanReschedule          bool `json:"loan_reschedule"`
	LoanMoratorium          bool `json:"loan_moratorium"`
	LoanSettlementDiscount  bool `json:"loan_settlement_discount"`
	// PR — approval coverage completion. Three previously-uncovered
	// posting paths now consult dedicated toggles. Defaults are TRUE
	// so a fresh tenant is safe-by-default; existing tenants were
	// flipped on by migration 0030 with an audit row per flip.
	FeeCollection     bool `json:"fee_collection"`
	WelfareCollection bool `json:"welfare_collection"`
	ApplicationFee    bool `json:"application_fee"`
	AllowSelf         bool `json:"allow_self"`
}

func (s *ApprovalsStore) GetTogglesTx(ctx context.Context, tx pgx.Tx) (*ApprovalToggles, error) {
	var t ApprovalToggles
	err := tx.QueryRow(ctx, `
		SELECT
			approval_deposit, approval_withdrawal, approval_deposit_transfer,
			approval_share_purchase, approval_share_transfer,
			approval_share_bonus, approval_share_lien,
			approval_loan_disbursement, approval_loan_repayment, approval_loan_settle,
			approval_loan_reverse, approval_loan_writeoff,
			approval_loan_reschedule, approval_loan_moratorium, approval_loan_settlement_discount,
			approval_fee_collection, approval_welfare_collection, approval_application_fee,
			approval_allow_self
		FROM tenant_operations
	`).Scan(
		&t.Deposit, &t.Withdrawal, &t.DepositTransfer,
		&t.SharePurchase, &t.ShareTransfer,
		&t.ShareBonus, &t.ShareLien,
		&t.LoanDisbursement, &t.LoanRepayment, &t.LoanSettle,
		&t.LoanReverse, &t.LoanWriteoff,
		&t.LoanReschedule, &t.LoanMoratorium, &t.LoanSettlementDiscount,
		&t.FeeCollection, &t.WelfareCollection, &t.ApplicationFee,
		&t.AllowSelf,
	)
	return &t, err
}

type UpdateTogglesInput struct {
	Deposit                *bool
	Withdrawal             *bool
	DepositTransfer        *bool
	SharePurchase          *bool
	ShareTransfer          *bool
	ShareBonus             *bool
	ShareLien              *bool
	LoanDisbursement       *bool
	LoanRepayment          *bool
	LoanSettle             *bool
	LoanReverse            *bool
	LoanWriteoff           *bool
	LoanReschedule         *bool
	LoanMoratorium         *bool
	LoanSettlementDiscount *bool
	FeeCollection          *bool
	WelfareCollection      *bool
	ApplicationFee         *bool
	AllowSelf              *bool
}

func (s *ApprovalsStore) UpdateTogglesTx(ctx context.Context, tx pgx.Tx, in UpdateTogglesInput) (*ApprovalToggles, error) {
	_, err := tx.Exec(ctx, `
		UPDATE tenant_operations SET
			approval_deposit                  = COALESCE($1,  approval_deposit),
			approval_withdrawal               = COALESCE($2,  approval_withdrawal),
			approval_deposit_transfer         = COALESCE($3,  approval_deposit_transfer),
			approval_share_purchase           = COALESCE($4,  approval_share_purchase),
			approval_share_transfer           = COALESCE($5,  approval_share_transfer),
			approval_share_bonus              = COALESCE($6,  approval_share_bonus),
			approval_share_lien               = COALESCE($7,  approval_share_lien),
			approval_loan_disbursement        = COALESCE($8,  approval_loan_disbursement),
			approval_loan_repayment           = COALESCE($9,  approval_loan_repayment),
			approval_loan_settle              = COALESCE($10, approval_loan_settle),
			approval_loan_reverse             = COALESCE($11, approval_loan_reverse),
			approval_loan_writeoff            = COALESCE($12, approval_loan_writeoff),
			approval_loan_reschedule          = COALESCE($13, approval_loan_reschedule),
			approval_loan_moratorium          = COALESCE($14, approval_loan_moratorium),
			approval_loan_settlement_discount = COALESCE($15, approval_loan_settlement_discount),
			approval_fee_collection           = COALESCE($16, approval_fee_collection),
			approval_welfare_collection       = COALESCE($17, approval_welfare_collection),
			approval_application_fee          = COALESCE($18, approval_application_fee),
			approval_allow_self               = COALESCE($19, approval_allow_self)
	`,
		in.Deposit, in.Withdrawal, in.DepositTransfer,
		in.SharePurchase, in.ShareTransfer,
		in.ShareBonus, in.ShareLien,
		in.LoanDisbursement, in.LoanRepayment, in.LoanSettle,
		in.LoanReverse, in.LoanWriteoff,
		in.LoanReschedule, in.LoanMoratorium, in.LoanSettlementDiscount,
		in.FeeCollection, in.WelfareCollection, in.ApplicationFee,
		in.AllowSelf,
	)
	if err != nil {
		return nil, err
	}
	return s.GetTogglesTx(ctx, tx)
}

// IsKindGated returns the toggle value for a specific kind.
func (t *ApprovalToggles) IsKindGated(k domain.ApprovalKind) bool {
	switch k {
	case domain.ApprovalKindDeposit:
		return t.Deposit
	case domain.ApprovalKindWithdrawal:
		return t.Withdrawal
	case domain.ApprovalKindDepositTransfer:
		return t.DepositTransfer
	case domain.ApprovalKindSharePurchase:
		return t.SharePurchase
	case domain.ApprovalKindShareTransfer:
		return t.ShareTransfer
	case domain.ApprovalKindShareBonus:
		return t.ShareBonus
	case domain.ApprovalKindShareLien:
		return t.ShareLien
	case domain.ApprovalKindLoanDisbursement:
		return t.LoanDisbursement
	case domain.ApprovalKindLoanRepayment:
		return t.LoanRepayment
	case domain.ApprovalKindLoanSettle:
		return t.LoanSettle
	case domain.ApprovalKindLoanReverse:
		return t.LoanReverse
	case domain.ApprovalKindLoanWriteoff:
		return t.LoanWriteoff
	case domain.ApprovalKindLoanReschedule:
		return t.LoanReschedule
	case domain.ApprovalKindLoanMoratorium:
		return t.LoanMoratorium
	case domain.ApprovalKindLoanSettlementDiscount:
		return t.LoanSettlementDiscount
	}
	return false
}

// UnmarshalPayload is a convenience generic helper used by executors.
func UnmarshalPayload[T any](raw []byte) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	return v, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// ─────────── Toggle-change audit (tenant_approval_changes) ───────────

// ToggleChange — one row of the audit history surfaced to the
// Settings → Recent changes panel. ChangedByUserID may be NULL when
// the row was written by a migration / system path; the UI renders
// "system" in that case.
type ToggleChange struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	ChangedByUserID  *uuid.UUID `json:"changed_by_user_id,omitempty"`
	Field            string     `json:"field"`
	OldValue         *string    `json:"old_value,omitempty"`
	NewValue         string     `json:"new_value"`
	Reason           *string    `json:"reason,omitempty"`
	ChangedAt        time.Time  `json:"changed_at"`
}

// AppendToggleChangeInput — one (field, old, new) tuple to audit.
// The handler builds one of these per field changed and calls
// AppendToggleChangeTx once per tuple in the same tx as
// UpdateTogglesTx.
type AppendToggleChangeInput struct {
	ChangedByUserID uuid.UUID
	Field           string // 'approval_deposit', etc.
	OldValue        bool
	NewValue        bool
	Reason          string // empty = no reason supplied; required when relaxing
}

// AppendToggleChangeTx inserts one audit row. Refuses (without
// inserting) when the change relaxes a gate (new=false) and no
// reason was supplied — the handler maps the typed error to a 400.
//
// Idempotency note: callers must skip the call entirely when old ==
// new (no actual flip happened). This method does NOT filter
// no-op changes; that's by design — the only caller is the
// handler's PATCH path which already iterates the diff.
func (s *ApprovalsStore) AppendToggleChangeTx(ctx context.Context, tx pgx.Tx, in AppendToggleChangeInput) error {
	if !in.NewValue && in.Reason == "" {
		return ErrToggleChangeReasonRequired
	}
	var changedBy *uuid.UUID
	if in.ChangedByUserID != uuid.Nil {
		id := in.ChangedByUserID
		changedBy = &id
	}
	old := boolStr(in.OldValue)
	newv := boolStr(in.NewValue)
	var reason *string
	if in.Reason != "" {
		r := in.Reason
		reason = &r
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO tenant_approval_changes (
		  tenant_id, changed_by_user_id, field, old_value, new_value, reason
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4, $5
		)
	`, changedBy, in.Field, old, newv, reason)
	return err
}

// ListToggleChangesTx returns the most-recent N audit rows for the
// current tenant. Backs the Settings → Recent changes panel.
func (s *ApprovalsStore) ListToggleChangesTx(ctx context.Context, tx pgx.Tx, limit int) ([]ToggleChange, error) {
	if limit <= 0 || limit > 200 {
		limit = 10
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, changed_by_user_id, field, old_value, new_value, reason, changed_at
		  FROM tenant_approval_changes
		 ORDER BY changed_at DESC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToggleChange{}
	for rows.Next() {
		var c ToggleChange
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ChangedByUserID,
			&c.Field, &c.OldValue, &c.NewValue, &c.Reason,
			&c.ChangedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
