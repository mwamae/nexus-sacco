// journal_entries + journal_lines persistence.
//
// The store deliberately separates "draft" workflows (manual entries
// going through maker/checker) from the canonical "post" path that
// the posting engine library uses. Either path eventually calls
// InsertPostedTx, which is the single point that writes a balanced
// double-entry to the GL.

package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
)

type JournalStore struct {
	pool *pgxpool.Pool
}

func NewJournalStore(pool *pgxpool.Pool) *JournalStore {
	return &JournalStore{pool: pool}
}

var (
	ErrUnbalanced  = errors.New("journal entry is unbalanced (total debits != total credits)")
	ErrEmptyEntry  = errors.New("journal entry must have at least two lines")
	ErrBadLine     = errors.New("journal line must have exactly one of debit/credit > 0")
	ErrNotEditable = errors.New("journal entry is not in an editable state")
)

const entryCols = `
	id, tenant_id, entry_no, entry_date, value_date, period_year, period_month,
	entry_type, source_module, source_ref, narration, status,
	total_debits, total_credits, reversal_of,
	created_by, created_at, posted_by, posted_at,
	rejected_by, rejected_at, rejection_reason, updated_at,
	workflow_instance_id
`

func scanEntry(row pgx.Row) (*domain.JournalEntry, error) {
	var e domain.JournalEntry
	var entryType, status string
	err := row.Scan(
		&e.ID, &e.TenantID, &e.EntryNo, &e.EntryDate, &e.ValueDate,
		&e.PeriodYear, &e.PeriodMonth, &entryType,
		&e.SourceModule, &e.SourceRef, &e.Narration, &status,
		&e.TotalDebits, &e.TotalCredits, &e.ReversalOf,
		&e.CreatedBy, &e.CreatedAt, &e.PostedBy, &e.PostedAt,
		&e.RejectedBy, &e.RejectedAt, &e.RejectionReason, &e.UpdatedAt,
		&e.WorkflowInstanceID,
	)
	if err != nil {
		return nil, err
	}
	e.EntryType = domain.JournalEntryType(entryType)
	e.Status = domain.JournalEntryStatus(status)
	return &e, nil
}

// ─────────── List + Get ───────────

type EntryListFilter struct {
	Status       string
	EntryType    string
	SourceModule string
	FromDate     *time.Time
	ToDate       *time.Time
	Limit        int
	Offset       int
}

func (s *JournalStore) ListTx(ctx context.Context, tx pgx.Tx, f EntryListFilter) ([]domain.JournalEntry, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + strconv.Itoa(idx)
		args = append(args, f.Status)
		idx++
	}
	if f.EntryType != "" {
		where += " AND entry_type = $" + strconv.Itoa(idx)
		args = append(args, f.EntryType)
		idx++
	}
	if f.SourceModule != "" {
		where += " AND source_module = $" + strconv.Itoa(idx)
		args = append(args, f.SourceModule)
		idx++
	}
	if f.FromDate != nil {
		where += " AND entry_date >= $" + strconv.Itoa(idx)
		args = append(args, *f.FromDate)
		idx++
	}
	if f.ToDate != nil {
		where += " AND entry_date <= $" + strconv.Itoa(idx)
		args = append(args, *f.ToDate)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM journal_entries `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	rows, err := tx.Query(ctx, `
		SELECT `+entryCols+` FROM journal_entries `+where+`
		ORDER BY entry_date DESC, created_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.JournalEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

func (s *JournalStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.JournalEntry, error) {
	row := tx.QueryRow(ctx, `SELECT `+entryCols+` FROM journal_entries WHERE id = $1`, id)
	e, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (s *JournalStore) LinesForTx(ctx context.Context, tx pgx.Tx, entryID uuid.UUID) ([]domain.JournalLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.id, l.tenant_id, l.entry_id, l.line_no, l.account_id,
		       a.code, a.name, l.debit, l.credit, l.narration
		FROM journal_lines l
		JOIN chart_of_accounts a ON a.id = l.account_id
		WHERE l.entry_id = $1
		ORDER BY l.line_no
	`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.JournalLine{}
	for rows.Next() {
		var l domain.JournalLine
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.EntryID, &l.LineNo, &l.AccountID,
			&l.AccountCode, &l.AccountName, &l.Debit, &l.Credit, &l.Narration,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ─────────── Draft + workflow ───────────

type EntryLineInput struct {
	AccountID uuid.UUID
	Debit     decimal.Decimal
	Credit    decimal.Decimal
	Narration string
}

type CreateEntryInput struct {
	EntryDate    time.Time
	ValueDate    time.Time
	EntryType    domain.JournalEntryType
	SourceModule *string
	SourceRef    *string
	Narration    string
	Lines        []EntryLineInput
	CreatedBy    *uuid.UUID
}

// validateLines enforces: ≥2 lines, each line single-sided, total
// debits = total credits, and no zero-value lines.
func validateLines(lines []EntryLineInput) (totalD, totalC decimal.Decimal, err error) {
	if len(lines) < 2 {
		return decimal.Zero, decimal.Zero, ErrEmptyEntry
	}
	for i, ln := range lines {
		dPos := ln.Debit.IsPositive()
		cPos := ln.Credit.IsPositive()
		if dPos == cPos {
			return decimal.Zero, decimal.Zero, fmt.Errorf("%w (line %d)", ErrBadLine, i+1)
		}
		totalD = totalD.Add(ln.Debit)
		totalC = totalC.Add(ln.Credit)
	}
	if !totalD.Equal(totalC) {
		return decimal.Zero, decimal.Zero, fmt.Errorf("%w (debits=%s credits=%s)", ErrUnbalanced, totalD, totalC)
	}
	return totalD, totalC, nil
}

// CreateDraftTx writes a draft manual journal entry that goes through
// maker/checker. No entry_no is allocated yet — that happens on post.
func (s *JournalStore) CreateDraftTx(ctx context.Context, tx pgx.Tx, in CreateEntryInput) (*domain.JournalEntry, error) {
	totalD, totalC, err := validateLines(in.Lines)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO journal_entries
		    (tenant_id, entry_date, value_date, period_year, period_month,
		     entry_type, source_module, source_ref, narration, status,
		     total_debits, total_credits, created_by)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8,
		        'pending_approval', $9, $10, $11)
		RETURNING `+entryCols,
		in.EntryDate, in.ValueDate, in.EntryDate.Year(), int(in.EntryDate.Month()),
		string(in.EntryType), in.SourceModule, in.SourceRef, in.Narration,
		totalD, totalC, in.CreatedBy,
	)
	entry, err := scanEntry(row)
	if err != nil {
		return nil, err
	}
	for i, ln := range in.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_lines (tenant_id, entry_id, line_no, account_id, debit, credit, narration)
			VALUES (current_tenant_id(), $1, $2, $3, $4, $5, NULLIF($6,''))
		`, entry.ID, i+1, ln.AccountID, ln.Debit, ln.Credit, ln.Narration); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// ApproveAndPostTx promotes a pending_approval entry → posted,
// allocating the next sequential entry_no for the tenant. Maker/checker:
// the approver must differ from the creator (enforced at the handler
// layer; the store trusts the caller).
func (s *JournalStore) ApproveAndPostTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, approverID uuid.UUID,
) (*domain.JournalEntry, error) {
	entryNo, err := s.nextEntryNoTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journal_entries
		SET status = 'posted', entry_no = $2,
		    posted_by = $3, posted_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'pending_approval'
	`, id, entryNo, approverID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotEditable
	}
	return s.GetTx(ctx, tx, id)
}

func (s *JournalStore) RejectTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, rejectorID uuid.UUID, reason string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE journal_entries
		SET status = 'rejected', rejected_by = $2, rejected_at = now(),
		    rejection_reason = NULLIF($3,''), updated_at = now()
		WHERE id = $1 AND status = 'pending_approval'
	`, id, rejectorID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotEditable
	}
	return nil
}

// ─────────── Auto-post (no workflow) ───────────

type InsertPostedInput struct {
	EntryDate    time.Time
	ValueDate    time.Time
	EntryType    domain.JournalEntryType
	SourceModule string
	SourceRef    string
	Narration    string
	Lines        []EntryLineInput
	PostedBy     *uuid.UUID // nil = system
}

// InsertPostedTx writes a fully-posted entry in a single shot. Used by
// the posting engine library. The caller is responsible for confirming
// the period is open (PeriodStore.EnsureOpenForDateTx).
func (s *JournalStore) InsertPostedTx(ctx context.Context, tx pgx.Tx, in InsertPostedInput) (*domain.JournalEntry, error) {
	totalD, totalC, err := validateLines(in.Lines)
	if err != nil {
		return nil, err
	}
	entryNo, err := s.nextEntryNoTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO journal_entries
		    (tenant_id, entry_no, entry_date, value_date, period_year, period_month,
		     entry_type, source_module, source_ref, narration, status,
		     total_debits, total_credits, posted_by, posted_at)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9,
		        'posted', $10, $11, $12, now())
		RETURNING `+entryCols,
		entryNo, in.EntryDate, in.ValueDate,
		in.EntryDate.Year(), int(in.EntryDate.Month()),
		string(in.EntryType), nullIfEmpty(in.SourceModule), nullIfEmpty(in.SourceRef),
		in.Narration, totalD, totalC, in.PostedBy,
	)
	entry, err := scanEntry(row)
	if err != nil {
		return nil, err
	}
	for i, ln := range in.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_lines (tenant_id, entry_id, line_no, account_id, debit, credit, narration)
			VALUES (current_tenant_id(), $1, $2, $3, $4, $5, NULLIF($6,''))
		`, entry.ID, i+1, ln.AccountID, ln.Debit, ln.Credit, ln.Narration); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// nextEntryNoTx allocates the next JE-XXXX number for the tenant. Uses
// a row-locked MAX-and-increment which is fine at SACCO volumes; for
// higher scale we'd switch to per-tenant sequences.
func (s *JournalStore) nextEntryNoTx(ctx context.Context, tx pgx.Tx) (string, error) {
	var next int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(
		    CASE WHEN entry_no ~ '^JE-[0-9]+$'
		         THEN (regexp_replace(entry_no, '^JE-', ''))::int
		    END
		), 0) + 1
		FROM journal_entries
	`).Scan(&next)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("JE-%06d", next), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
