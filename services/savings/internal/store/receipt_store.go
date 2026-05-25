// Receipt store — the persistence layer for the Collection Desk.
//
// The store handles three concerns the handler shouldn't have to:
//   1. Per-(tenant, till, date) receipt serial generation via an
//      upsert on receipt_serial_seq. Serial format:
//      R-<till_code>-YYYYMMDD-NNNN.
//   2. Cross-table integrity at create-time: header + N lines in one
//      tx, with the channel + till FK shape validated by the table
//      CHECK constraint (cash → till_session_id, else virtual_till_id).
//   3. Status transitions: receipt status is *derived* from the lines.
//      RecomputeStatusTx scans the lines and flips the header to
//      'posted' once every line is terminal (posted | declined | voided).

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type ReceiptStore struct {
	pool *pgxpool.Pool
}

func NewReceiptStore(pool *pgxpool.Pool) *ReceiptStore {
	return &ReceiptStore{pool: pool}
}

// ─────────── Errors ───────────

// ErrDuplicateReceipt surfaces when (tenant, channel, channel_ref)
// already exists for a non-cash channel. The handler maps this to a
// 409 soft block with the existing receipt id in the body so the UI
// can offer "continue anyway" semantics.
var ErrDuplicateReceipt = errors.New("duplicate receipt for channel ref")

// ─────────── Serial generation ───────────

// nextSerialTx atomically advances the per-(tenant, till_code, date)
// counter and returns the formatted serial. Uses an upsert so the
// first call of the day for a till seeds the counter.
func nextSerialTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, tillCode string, date time.Time) (string, error) {
	dateKey := date.UTC().Format("2006-01-02")
	var nextNo int
	err := tx.QueryRow(ctx, `
		INSERT INTO receipt_serial_seq (tenant_id, till_code, date_key, last_no)
		VALUES ($1, $2, $3::date, 1)
		ON CONFLICT (tenant_id, till_code, date_key)
		DO UPDATE SET last_no = receipt_serial_seq.last_no + 1
		RETURNING last_no
	`, tenantID, tillCode, dateKey).Scan(&nextNo)
	if err != nil {
		return "", fmt.Errorf("next receipt serial: %w", err)
	}
	return fmt.Sprintf("R-%s-%s-%04d",
		tillCode,
		date.UTC().Format("20060102"),
		nextNo,
	), nil
}

// ─────────── Create ───────────

// CreateInput is what the handler hands to the store. The handler
// has already resolved the till_session vs virtual_till split based
// on the channel.
type CreateReceiptInput struct {
	TenantID        uuid.UUID
	CounterpartyID  uuid.UUID
	Channel         domain.ReceiptChannel
	ChannelRef      *string
	ChannelAmount   decimal.Decimal
	ValueDate       time.Time
	Narration       *string
	CashierUserID   uuid.UUID
	TillSessionID   *uuid.UUID // set when Channel==cash
	VirtualTillID   *uuid.UUID // set when Channel!=cash
	TillCode        string     // for the serial; comes from the till_session.till.code or virtual_till.display
	Lines           []CreateReceiptLineInput
}

type CreateReceiptLineInput struct {
	LineNo          int
	Kind            domain.ReceiptLineKind
	Amount          decimal.Decimal
	TargetAccountID *uuid.UUID
	FeeCode         *string
	Narration       *string
}

// CreateTx inserts the header + all lines in one tx, with serial
// allocation inside the same tx so a partial failure doesn't leak a
// serial number. Returns the persisted Receipt with Lines populated.
//
// On a (channel, channel_ref) UNIQUE collision returns ErrDuplicateReceipt
// AFTER fetching the colliding receipt id so the handler can include it
// in the 409 body.
func (s *ReceiptStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateReceiptInput) (*domain.Receipt, error) {
	// Validate the till/channel pairing up front — DB will also enforce
	// via CHECK constraint, but the Go-side message is clearer.
	if in.Channel == domain.RCCash {
		if in.TillSessionID == nil {
			return nil, fmt.Errorf("cash receipt requires till_session_id")
		}
		if in.VirtualTillID != nil {
			return nil, fmt.Errorf("cash receipt must not set virtual_till_id")
		}
		if in.ChannelRef != nil && *in.ChannelRef != "" {
			return nil, fmt.Errorf("cash receipt must not set channel_ref")
		}
	} else {
		if in.VirtualTillID == nil {
			return nil, fmt.Errorf("non-cash receipt requires virtual_till_id")
		}
		if in.TillSessionID != nil {
			return nil, fmt.Errorf("non-cash receipt must not set till_session_id")
		}
		if in.ChannelRef == nil || *in.ChannelRef == "" {
			return nil, fmt.Errorf("non-cash receipt requires channel_ref")
		}
	}
	if len(in.Lines) == 0 {
		return nil, fmt.Errorf("receipt must have at least one line")
	}
	// Subtotal == channel_amount invariant. The handler can offer a
	// "deposit residual to ordinary savings" auto-line but it must be
	// expressed as a real line, never as slack.
	var sum decimal.Decimal
	for _, l := range in.Lines {
		sum = sum.Add(l.Amount)
	}
	if !sum.Equal(in.ChannelAmount) {
		return nil, fmt.Errorf("line subtotal %s != channel_amount %s", sum.String(), in.ChannelAmount.String())
	}

	serial, err := nextSerialTx(ctx, tx, in.TenantID, in.TillCode, in.ValueDate)
	if err != nil {
		return nil, err
	}

	r := &domain.Receipt{
		TenantID:       in.TenantID,
		Serial:         serial,
		CounterpartyID: in.CounterpartyID,
		Channel:        in.Channel,
		ChannelRef:     in.ChannelRef,
		ChannelAmount:  in.ChannelAmount,
		ValueDate:      in.ValueDate,
		Narration:      in.Narration,
		CashierUserID:  in.CashierUserID,
		TillSessionID:  in.TillSessionID,
		VirtualTillID:  in.VirtualTillID,
		Status:         domain.ReceiptDraft,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO receipts (
			tenant_id, serial, counterparty_id, channel, channel_ref, channel_amount,
			value_date, narration, cashier_user_id, till_session_id, virtual_till_id, status
		) VALUES (
			$1, $2, $3, $4::receipt_channel, $5, $6,
			$7, $8, $9, $10, $11, 'draft'
		)
		RETURNING id, created_at, updated_at
	`,
		r.TenantID, r.Serial, r.CounterpartyID, string(r.Channel), r.ChannelRef, r.ChannelAmount,
		r.ValueDate, r.Narration, r.CashierUserID, r.TillSessionID, r.VirtualTillID,
	).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err, "receipts_channel_ref_unique") && in.ChannelRef != nil {
			return nil, wrapDuplicateReceipt(ctx, tx, in.TenantID, in.Channel, *in.ChannelRef)
		}
		return nil, fmt.Errorf("insert receipt: %w", err)
	}

	for _, li := range in.Lines {
		var line domain.ReceiptLine
		err := tx.QueryRow(ctx, `
			INSERT INTO receipt_lines (
				receipt_id, line_no, kind, amount, target_account_id, fee_code, narration, status
			) VALUES ($1, $2, $3::receipt_line_kind, $4, $5, $6, $7, 'pending')
			RETURNING id, receipt_id, line_no, kind::text, amount,
			          target_account_id, fee_code, narration, status::text, created_at
		`, r.ID, li.LineNo, string(li.Kind), li.Amount, li.TargetAccountID, li.FeeCode, li.Narration).
			Scan(&line.ID, &line.ReceiptID, &line.LineNo, &line.Kind, &line.Amount,
				&line.TargetAccountID, &line.FeeCode, &line.Narration, &line.Status, &line.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("insert receipt line %d: %w", li.LineNo, err)
		}
		r.Lines = append(r.Lines, line)
	}
	return r, nil
}

func isUniqueViolation(err error, constraint string) bool {
	// pgconn.PgError exposes ConstraintName as a struct field, not a
	// method — so an interface assertion that requires both
	// SQLState() and ConstraintName() as methods would always fail.
	// Use the concrete type directly.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505" && pgErr.ConstraintName == constraint
	}
	return false
}

func wrapDuplicateReceipt(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, channel domain.ReceiptChannel, ref string) error {
	var existingID uuid.UUID
	_ = tx.QueryRow(ctx, `
		SELECT id FROM receipts
		 WHERE tenant_id = $1 AND channel = $2::receipt_channel AND channel_ref = $3
		 LIMIT 1
	`, tenantID, string(channel), ref).Scan(&existingID)
	return fmt.Errorf("%w (existing=%s)", ErrDuplicateReceipt, existingID)
}

// ─────────── Read ───────────

const receiptCols = `
	id, tenant_id, serial, counterparty_id, channel::text, channel_ref, channel_amount,
	value_date, narration, cashier_user_id, till_session_id, virtual_till_id,
	status::text, pdf_document_id, voided_at, voided_by, void_reason,
	created_at, posted_at, updated_at
`

func scanReceipt(row pgx.Row) (*domain.Receipt, error) {
	var r domain.Receipt
	var channelStr, statusStr string
	err := row.Scan(
		&r.ID, &r.TenantID, &r.Serial, &r.CounterpartyID, &channelStr, &r.ChannelRef, &r.ChannelAmount,
		&r.ValueDate, &r.Narration, &r.CashierUserID, &r.TillSessionID, &r.VirtualTillID,
		&statusStr, &r.PDFDocumentID, &r.VoidedAt, &r.VoidedBy, &r.VoidReason,
		&r.CreatedAt, &r.PostedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.Channel = domain.ReceiptChannel(channelStr)
	r.Status = domain.ReceiptStatus(statusStr)
	return &r, nil
}

// GetByIDTx returns the header + all lines, RLS-scoped to the
// caller's tenant. Returns ErrNotFound for cross-tenant or missing.
func (s *ReceiptStore) GetByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Receipt, error) {
	r, err := scanReceipt(tx.QueryRow(ctx, `SELECT `+receiptCols+` FROM receipts WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	lines, err := s.linesForReceiptTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	r.Lines = lines
	return r, nil
}

func (s *ReceiptStore) linesForReceiptTx(ctx context.Context, tx pgx.Tx, receiptID uuid.UUID) ([]domain.ReceiptLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, receipt_id, line_no, kind::text, amount, target_account_id, fee_code, narration,
		       approval_id, posted_txn_id, status::text, voided_at, voided_by, void_reason,
		       created_at, posted_at
		  FROM receipt_lines
		 WHERE receipt_id = $1
		 ORDER BY line_no
	`, receiptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReceiptLine
	for rows.Next() {
		var l domain.ReceiptLine
		var kindStr, statusStr string
		if err := rows.Scan(&l.ID, &l.ReceiptID, &l.LineNo, &kindStr, &l.Amount,
			&l.TargetAccountID, &l.FeeCode, &l.Narration,
			&l.ApprovalID, &l.PostedTxnID, &statusStr, &l.VoidedAt, &l.VoidedBy, &l.VoidReason,
			&l.CreatedAt, &l.PostedAt); err != nil {
			return nil, err
		}
		l.Kind = domain.ReceiptLineKind(kindStr)
		l.Status = domain.ReceiptLineStatus(statusStr)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListFilter scopes /collect/receipts. tenant-RLS is implicit.
type ReceiptListFilter struct {
	TillSessionID *uuid.UUID
	VirtualTillID *uuid.UUID
	CashierUserID *uuid.UUID
	ValueDate     *time.Time // YYYY-MM-DD; matches receipts.value_date
	Status        *domain.ReceiptStatus
	Limit, Offset int
}

// ReceiptListItem wraps domain.Receipt with two summary fields the
// list endpoint computes via a LATERAL aggregate on receipt_lines.
// Keeping these off domain.Receipt so the detail endpoint's wire
// shape (which carries Lines []ReceiptLine) doesn't grow a pair of
// zero-valued fields. Bug 3.1 fix: the old list response showed
// LINES=0 for every row because the per-line array was never
// populated by ListTx.
type ReceiptListItem struct {
	domain.Receipt
	LineCount   int    `json:"line_count"`
	LineSummary string `json:"line_summary"`
}

func (s *ReceiptStore) ListTx(ctx context.Context, tx pgx.Tx, f ReceiptListFilter) ([]ReceiptListItem, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.TillSessionID != nil {
		where += fmt.Sprintf(" AND r.till_session_id = $%d", idx)
		args = append(args, *f.TillSessionID)
		idx++
	}
	if f.VirtualTillID != nil {
		where += fmt.Sprintf(" AND r.virtual_till_id = $%d", idx)
		args = append(args, *f.VirtualTillID)
		idx++
	}
	if f.CashierUserID != nil {
		where += fmt.Sprintf(" AND r.cashier_user_id = $%d", idx)
		args = append(args, *f.CashierUserID)
		idx++
	}
	if f.ValueDate != nil {
		where += fmt.Sprintf(" AND r.value_date = $%d::date", idx)
		args = append(args, f.ValueDate.Format("2006-01-02"))
		idx++
	}
	if f.Status != nil {
		where += fmt.Sprintf(" AND r.status = $%d::receipt_status", idx)
		args = append(args, string(*f.Status))
		idx++
	}
	args = append(args, f.Limit, f.Offset)
	// Single LATERAL join — voided lines are excluded so the count
	// matches what the cashier sees in the detail view. The kinds
	// array stays ordered by line_no so the formatted summary is
	// stable across renders.
	q := fmt.Sprintf(`
		SELECT %s,
		       COALESCE(lc.line_count, 0)   AS line_count,
		       COALESCE(lc.line_kinds, '{}')::text[] AS line_kinds
		  FROM receipts r
		  LEFT JOIN LATERAL (
		    SELECT
		      count(*) AS line_count,
		      array_agg(rl.kind::text ORDER BY rl.line_no) AS line_kinds
		      FROM receipt_lines rl
		     WHERE rl.receipt_id = r.id
		       AND rl.voided_at IS NULL
		  ) lc ON true
		  %s
		 ORDER BY r.created_at DESC
		 LIMIT $%d OFFSET $%d
	`, prefixReceiptCols("r"), where, idx, idx+1)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReceiptListItem
	for rows.Next() {
		item, err := scanReceiptListItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// prefixReceiptCols aliases every column in receiptCols with the
// given table alias so the LATERAL join doesn't ambiguate on
// status / created_at / etc. Cheap string transform — receiptCols
// is a compile-time constant.
func prefixReceiptCols(alias string) string {
	// Each column name is on its own logical line + comma; just
	// prefix every bare identifier that starts each comma-separated
	// item. Simpler: hand-roll the prefixed list here so we don't
	// risk a regex misfire on the cast in `channel::text` /
	// `status::text`.
	return alias + `.id, ` + alias + `.tenant_id, ` + alias + `.serial, ` +
		alias + `.counterparty_id, ` + alias + `.channel::text, ` +
		alias + `.channel_ref, ` + alias + `.channel_amount, ` +
		alias + `.value_date, ` + alias + `.narration, ` +
		alias + `.cashier_user_id, ` + alias + `.till_session_id, ` +
		alias + `.virtual_till_id, ` + alias + `.status::text, ` +
		alias + `.pdf_document_id, ` + alias + `.voided_at, ` +
		alias + `.voided_by, ` + alias + `.void_reason, ` +
		alias + `.created_at, ` + alias + `.posted_at, ` +
		alias + `.updated_at`
}

// scanReceiptListItem reads the same 20 receipt cols + 2 extra
// aggregate cols (count + kinds array) and formats line_summary.
func scanReceiptListItem(row pgx.Row) (*ReceiptListItem, error) {
	var item ReceiptListItem
	var channelStr, statusStr string
	var kinds []string
	if err := row.Scan(
		&item.ID, &item.TenantID, &item.Serial, &item.CounterpartyID, &channelStr,
		&item.ChannelRef, &item.ChannelAmount, &item.ValueDate, &item.Narration,
		&item.CashierUserID, &item.TillSessionID, &item.VirtualTillID, &statusStr,
		&item.PDFDocumentID, &item.VoidedAt, &item.VoidedBy, &item.VoidReason,
		&item.CreatedAt, &item.PostedAt, &item.UpdatedAt,
		&item.LineCount, &kinds,
	); err != nil {
		return nil, err
	}
	item.Channel = domain.ReceiptChannel(channelStr)
	item.Status = domain.ReceiptStatus(statusStr)
	item.LineSummary = formatLineSummary(kinds)
	return &item, nil
}

// formatLineSummary turns ['savings_deposit','share_purchase','loan_repayment']
// into "savings + share + loan", or ['fee','fee'] into "2 fees".
// Truncates to 60 chars + ellipsis. Bug 3.1 plan rule:
//   - empty:             ""
//   - all same kind:     "<n> <label>" (singular for n=1, plural for >1)
//   - mixed kinds:       distinct labels joined with " + ", with a
//                        count prefix for any kind that repeats
//                        (e.g. ['fee','fee','savings_deposit'] →
//                        "savings + 2 fees")
func formatLineSummary(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	counts := map[string]int{}
	order := []string{}
	for _, k := range kinds {
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		label := lineKindShortLabel(k)
		if counts[k] > 1 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], pluralise(label, counts[k])))
		} else {
			parts = append(parts, label)
		}
	}
	out := strings.Join(parts, " + ")
	if len(out) > 60 {
		out = out[:57] + "…"
	}
	return out
}

// lineKindShortLabel maps the canonical kind enum to the short form
// the user sees in summaries. "savings_deposit" → "savings",
// "share_purchase" → "share", "loan_repayment" → "loan",
// "fee" / "welfare" → unchanged.
func lineKindShortLabel(k string) string {
	switch k {
	case "savings_deposit":
		return "savings"
	case "share_purchase":
		return "share"
	case "loan_repayment":
		return "loan"
	default:
		return k
	}
}

// pluralise — single-rule english pluraliser scoped to the short
// kind labels above. Anything that isn't already plural gets an "s".
func pluralise(label string, n int) string {
	if n <= 1 {
		return label
	}
	switch label {
	case "fees", "shares", "loans", "savings":
		return label // already plural / mass noun
	}
	return label + "s"
}

// ─────────── Line updates (called from approvals execution + voids) ───────────

// GetLineByApprovalIDTx is the reverse lookup the pending_approvals
// dispatcher uses to figure out whether an approval row was spawned
// by the Collection Desk (so it should propagate the post/decline
// back onto the receipt line) or by a per-panel direct button (no
// receipt linkage, so the dispatcher leaves the receipts side
// untouched). Returns ErrNotFound when the approval has no backing
// receipt line.
func (s *ReceiptStore) GetLineByApprovalIDTx(ctx context.Context, tx pgx.Tx, approvalID uuid.UUID) (*domain.ReceiptLine, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, receipt_id, line_no, kind::text, amount, target_account_id, fee_code, narration,
		       approval_id, posted_txn_id, status::text, voided_at, voided_by, void_reason,
		       created_at, posted_at
		  FROM receipt_lines
		 WHERE approval_id = $1
	`, approvalID)
	var l domain.ReceiptLine
	var kindStr, statusStr string
	if err := row.Scan(&l.ID, &l.ReceiptID, &l.LineNo, &kindStr, &l.Amount,
		&l.TargetAccountID, &l.FeeCode, &l.Narration,
		&l.ApprovalID, &l.PostedTxnID, &statusStr, &l.VoidedAt, &l.VoidedBy, &l.VoidReason,
		&l.CreatedAt, &l.PostedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	l.Kind = domain.ReceiptLineKind(kindStr)
	l.Status = domain.ReceiptLineStatus(statusStr)
	return &l, nil
}

// MarkLinePostedTx is invoked after the per-line approval's underlying
// posting handler returns success. It writes the posted_txn_id back
// onto the line and flips its status to 'posted', then triggers a
// header-status recompute.
func (s *ReceiptStore) MarkLinePostedTx(ctx context.Context, tx pgx.Tx, lineID, txnID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE receipt_lines
		   SET posted_txn_id = $2, status = 'posted', posted_at = now()
		 WHERE id = $1
	`, lineID, txnID); err != nil {
		return fmt.Errorf("mark line posted: %w", err)
	}
	return s.RecomputeStatusForLineTx(ctx, tx, lineID)
}

// UnpostedFeeLine is one row returned by ListUnpostedFeeLinesTx —
// the replay endpoint loops over these to re-attempt postFeeLineTx
// for receipts that previously crashed (typically because the fee
// catalog pointed at a non-existent GL code; see migration
// accounting 0012 + savings 0031 for the underlying fix).
type UnpostedFeeLine struct {
	ReceiptID uuid.UUID
	LineID    uuid.UUID
	Kind      string
}

// ListUnpostedFeeLinesTx returns every receipt_line where:
//
//	kind IN ('fee', 'welfare')
//	posted_txn_id IS NULL
//	voided_at IS NULL
//
// Caller is responsible for loading the receipt header + the
// matching line before calling postFeeLineTx. RLS scopes by tenant.
func (s *ReceiptStore) ListUnpostedFeeLinesTx(ctx context.Context, tx pgx.Tx) ([]UnpostedFeeLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT receipt_id, id, kind::text
		  FROM receipt_lines
		 WHERE kind IN ('fee', 'welfare')
		   AND posted_txn_id IS NULL
		   AND voided_at IS NULL
		 ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnpostedFeeLine{}
	for rows.Next() {
		var r UnpostedFeeLine
		if err := rows.Scan(&r.ReceiptID, &r.LineID, &r.Kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkLineDeclinedTx mirrors MarkLinePostedTx for the rejected path.
func (s *ReceiptStore) MarkLineDeclinedTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE receipt_lines SET status = 'declined' WHERE id = $1
	`, lineID); err != nil {
		return fmt.Errorf("mark line declined: %w", err)
	}
	return s.RecomputeStatusForLineTx(ctx, tx, lineID)
}

// AttachApprovalTx links a freshly-created pending_approvals row to
// the receipt_line that spawned it. Lets the dispatcher resolve the
// reverse direction (approval → line) cheaply.
func (s *ReceiptStore) AttachApprovalTx(ctx context.Context, tx pgx.Tx, lineID, approvalID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE receipt_lines SET approval_id = $2 WHERE id = $1`, lineID, approvalID)
	return err
}

// GetLineForVoidTx is the lightweight lookup the VoidLine handler
// uses before it dispatches to the per-kind reverse executor. Same
// shape as the dispatcher's reverse lookup; reused here to keep the
// void path from cross-querying the wider receipt with all its
// other lines.
func (s *ReceiptStore) GetLineForVoidTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) (*domain.ReceiptLine, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, receipt_id, line_no, kind::text, amount, target_account_id, fee_code, narration,
		       approval_id, posted_txn_id, status::text, voided_at, voided_by, void_reason,
		       created_at, posted_at
		  FROM receipt_lines
		 WHERE id = $1
	`, lineID)
	var l domain.ReceiptLine
	var kindStr, statusStr string
	if err := row.Scan(&l.ID, &l.ReceiptID, &l.LineNo, &kindStr, &l.Amount,
		&l.TargetAccountID, &l.FeeCode, &l.Narration,
		&l.ApprovalID, &l.PostedTxnID, &statusStr, &l.VoidedAt, &l.VoidedBy, &l.VoidReason,
		&l.CreatedAt, &l.PostedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	l.Kind = domain.ReceiptLineKind(kindStr)
	l.Status = domain.ReceiptLineStatus(statusStr)
	return &l, nil
}

// SetPDFDocumentIDTx stamps the pdf_documents.id onto the receipt
// header so the frontend can render a download link. Called after
// POST /v1/receipts/{id}/pdf finishes its synchronous render.
func (s *ReceiptStore) SetPDFDocumentIDTx(ctx context.Context, tx pgx.Tx, receiptID, pdfDocID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE receipts SET pdf_document_id = $2, updated_at = now() WHERE id = $1`,
		receiptID, pdfDocID)
	return err
}

// VoidLineTx is the per-line void from the plan. The line's underlying
// posted txn must be reversed by the caller through the appropriate
// handler (deposit reverse, share reverse, loan reverse) — this method
// only updates the receipt-side bookkeeping.
type VoidLineInput struct {
	LineID     uuid.UUID
	VoidedBy   uuid.UUID
	Reason     string
}

func (s *ReceiptStore) VoidLineTx(ctx context.Context, tx pgx.Tx, in VoidLineInput) error {
	if _, err := tx.Exec(ctx, `
		UPDATE receipt_lines
		   SET status = 'voided', voided_at = now(), voided_by = $2, void_reason = $3
		 WHERE id = $1 AND status IN ('pending','posted')
	`, in.LineID, in.VoidedBy, in.Reason); err != nil {
		return fmt.Errorf("void line: %w", err)
	}
	return s.RecomputeStatusForLineTx(ctx, tx, in.LineID)
}

// RecomputeStatusForLineTx walks the receipt's lines and flips the
// header status to 'posted' once every line is terminal. Idempotent.
func (s *ReceiptStore) RecomputeStatusForLineTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) error {
	var receiptID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT receipt_id FROM receipt_lines WHERE id = $1`, lineID,
	).Scan(&receiptID); err != nil {
		return err
	}
	return s.RecomputeStatusTx(ctx, tx, receiptID)
}

// RecomputeStatusTx — header is 'posted' iff every line is terminal
// (posted | declined | voided). The voided-header state is reached
// via VoidReceiptTx, not via this recompute path.
func (s *ReceiptStore) RecomputeStatusTx(ctx context.Context, tx pgx.Tx, receiptID uuid.UUID) error {
	var allTerminal bool
	if err := tx.QueryRow(ctx, `
		SELECT bool_and(status IN ('posted','declined','voided'))
		  FROM receipt_lines WHERE receipt_id = $1
	`, receiptID).Scan(&allTerminal); err != nil {
		return err
	}
	if !allTerminal {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE receipts
		   SET status = 'posted', posted_at = COALESCE(posted_at, now()), updated_at = now()
		 WHERE id = $1 AND status = 'draft'
	`, receiptID)
	return err
}
