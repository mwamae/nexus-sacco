// Bank reconciliation persistence — accounts, statements, lines, and
// the match transitions. RLS-scoped via current_tenant_id() like the
// rest of the accounting service.

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

	"github.com/nexussacco/accounting/internal/domain"
)

type BankStore struct {
	pool *pgxpool.Pool
}

func NewBankStore(pool *pgxpool.Pool) *BankStore {
	return &BankStore{pool: pool}
}

var (
	ErrBankAccountNotFound  = errors.New("bank account not found")
	ErrBankStatementNotFound = errors.New("bank statement not found")
	ErrBankLineNotFound      = errors.New("bank statement line not found")
	ErrLineAlreadyMatched    = errors.New("bank statement line is already matched")
	ErrJournalAlreadyMatched = errors.New("journal line is already matched to a different bank line")
)

// ─────────── Bank accounts ───────────

const bankAcctCols = `
	id, tenant_id, gl_account_code, bank_name, account_number, branch,
	currency_code, is_active, notes, created_at, updated_at
`

func scanBankAcct(row pgx.Row) (*domain.BankAccount, error) {
	var b domain.BankAccount
	err := row.Scan(
		&b.ID, &b.TenantID, &b.GLAccountCode, &b.BankName, &b.AccountNumber, &b.Branch,
		&b.CurrencyCode, &b.IsActive, &b.Notes, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *BankStore) ListAccountsTx(ctx context.Context, tx pgx.Tx, activeOnly bool) ([]domain.BankAccount, error) {
	q := `SELECT ` + bankAcctCols + ` FROM bank_accounts`
	if activeOnly {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY bank_name, account_number`
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BankAccount{}
	for rows.Next() {
		b, err := scanBankAcct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *BankStore) GetAccountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.BankAccount, error) {
	row := tx.QueryRow(ctx, `SELECT `+bankAcctCols+` FROM bank_accounts WHERE id = $1`, id)
	b, err := scanBankAcct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBankAccountNotFound
	}
	return b, err
}

type CreateBankAccountInput struct {
	GLAccountCode string
	BankName      string
	AccountNumber string
	Branch        *string
	CurrencyCode  string
	Notes         *string
}

func (s *BankStore) CreateAccountTx(ctx context.Context, tx pgx.Tx, in CreateBankAccountInput) (*domain.BankAccount, error) {
	if in.CurrencyCode == "" {
		in.CurrencyCode = "KES"
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO bank_accounts (
		  tenant_id, gl_account_code, bank_name, account_number,
		  branch, currency_code, notes
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6)
		RETURNING `+bankAcctCols+`
	`, in.GLAccountCode, in.BankName, in.AccountNumber, in.Branch, in.CurrencyCode, in.Notes)
	return scanBankAcct(row)
}

type UpdateBankAccountInput struct {
	BankName     *string
	AccountNumber *string
	Branch       *string
	CurrencyCode *string
	IsActive     *bool
	Notes        *string
}

func (s *BankStore) UpdateAccountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, in UpdateBankAccountInput) (*domain.BankAccount, error) {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	pos := 2
	if in.BankName != nil { sets = append(sets, fmt.Sprintf("bank_name = $%d", pos)); args = append(args, *in.BankName); pos++ }
	if in.AccountNumber != nil { sets = append(sets, fmt.Sprintf("account_number = $%d", pos)); args = append(args, *in.AccountNumber); pos++ }
	if in.Branch != nil { sets = append(sets, fmt.Sprintf("branch = $%d", pos)); args = append(args, *in.Branch); pos++ }
	if in.CurrencyCode != nil { sets = append(sets, fmt.Sprintf("currency_code = $%d", pos)); args = append(args, *in.CurrencyCode); pos++ }
	if in.IsActive != nil { sets = append(sets, fmt.Sprintf("is_active = $%d", pos)); args = append(args, *in.IsActive); pos++ }
	if in.Notes != nil { sets = append(sets, fmt.Sprintf("notes = $%d", pos)); args = append(args, *in.Notes); pos++ }
	if len(sets) == 1 {
		return s.GetAccountTx(ctx, tx, id)
	}
	q := fmt.Sprintf(`UPDATE bank_accounts SET %s WHERE id = $1 RETURNING %s`, strings.Join(sets, ", "), bankAcctCols)
	row := tx.QueryRow(ctx, q, args...)
	b, err := scanBankAcct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBankAccountNotFound
	}
	return b, err
}

// ─────────── Statements ───────────

const bankStmtCols = `
	id, tenant_id, bank_account_id, statement_date, period_start, period_end,
	opening_balance, closing_balance, total_debits, total_credits,
	line_count, source_format, source_filename, uploaded_at, uploaded_by
`

func scanBankStmt(row pgx.Row) (*domain.BankStatement, error) {
	var st domain.BankStatement
	err := row.Scan(
		&st.ID, &st.TenantID, &st.BankAccountID, &st.StatementDate, &st.PeriodStart, &st.PeriodEnd,
		&st.OpeningBalance, &st.ClosingBalance, &st.TotalDebits, &st.TotalCredits,
		&st.LineCount, &st.SourceFormat, &st.SourceFilename, &st.UploadedAt, &st.UploadedBy,
	)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

type CreateStatementInput struct {
	BankAccountID  uuid.UUID
	StatementDate  time.Time
	PeriodStart    *time.Time
	PeriodEnd      *time.Time
	OpeningBalance *decimal.Decimal
	ClosingBalance *decimal.Decimal
	SourceFormat   string
	SourceFilename *string
	UploadedBy     uuid.UUID
	Lines          []ParsedStatementLine
}

// ParsedStatementLine — the upload's lines pre-persistence. The store
// computes totals + assigns line_no.
type ParsedStatementLine struct {
	TxnDate        time.Time
	ValueDate      *time.Time
	Description    string
	Reference      string
	Debit          decimal.Decimal
	Credit         decimal.Decimal
	RunningBalance *decimal.Decimal
}

func (s *BankStore) CreateStatementWithLinesTx(
	ctx context.Context, tx pgx.Tx, in CreateStatementInput,
) (*domain.BankStatement, error) {
	var totalDebits, totalCredits decimal.Decimal
	for _, l := range in.Lines {
		totalDebits = totalDebits.Add(l.Debit)
		totalCredits = totalCredits.Add(l.Credit)
	}
	stmtID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO bank_statements (
		  id, tenant_id, bank_account_id, statement_date,
		  period_start, period_end, opening_balance, closing_balance,
		  total_debits, total_credits, line_count,
		  source_format, source_filename, uploaded_by
		) VALUES (
		  $1, current_tenant_id(), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		)
	`,
		stmtID, in.BankAccountID, in.StatementDate,
		in.PeriodStart, in.PeriodEnd, in.OpeningBalance, in.ClosingBalance,
		totalDebits, totalCredits, len(in.Lines),
		in.SourceFormat, in.SourceFilename, in.UploadedBy,
	); err != nil {
		return nil, err
	}
	for i, ln := range in.Lines {
		var desc, ref *string
		if ln.Description != "" { d := ln.Description; desc = &d }
		if ln.Reference != ""   { r := ln.Reference;   ref = &r }
		if _, err := tx.Exec(ctx, `
			INSERT INTO bank_statement_lines (
			  tenant_id, statement_id, bank_account_id, line_no,
			  txn_date, value_date, description, reference,
			  debit, credit, running_balance
			) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`,
			stmtID, in.BankAccountID, i+1,
			ln.TxnDate, ln.ValueDate, desc, ref,
			ln.Debit, ln.Credit, ln.RunningBalance,
		); err != nil {
			return nil, fmt.Errorf("insert line %d: %w", i+1, err)
		}
	}
	row := tx.QueryRow(ctx, `SELECT `+bankStmtCols+` FROM bank_statements WHERE id = $1`, stmtID)
	return scanBankStmt(row)
}

func (s *BankStore) ListStatementsTx(ctx context.Context, tx pgx.Tx, bankAccountID uuid.UUID) ([]domain.BankStatement, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+bankStmtCols+` FROM bank_statements WHERE bank_account_id = $1 ORDER BY statement_date DESC, uploaded_at DESC`,
		bankAccountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BankStatement{}
	for rows.Next() {
		st, err := scanBankStmt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

func (s *BankStore) GetStatementTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.BankStatement, error) {
	row := tx.QueryRow(ctx, `SELECT `+bankStmtCols+` FROM bank_statements WHERE id = $1`, id)
	st, err := scanBankStmt(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBankStatementNotFound
	}
	return st, err
}

// ─────────── Lines + matching ───────────

const bankLineCols = `
	id, tenant_id, statement_id, bank_account_id, line_no,
	txn_date, value_date, description, reference,
	debit, credit, running_balance,
	match_status, matched_journal_line_id, matched_at, matched_by, match_notes
`

func scanBankLine(row pgx.Row) (*domain.BankStatementLine, error) {
	var l domain.BankStatementLine
	var status string
	err := row.Scan(
		&l.ID, &l.TenantID, &l.StatementID, &l.BankAccountID, &l.LineNo,
		&l.TxnDate, &l.ValueDate, &l.Description, &l.Reference,
		&l.Debit, &l.Credit, &l.RunningBalance,
		&status, &l.MatchedJournalLineID, &l.MatchedAt, &l.MatchedBy, &l.MatchNotes,
	)
	if err != nil {
		return nil, err
	}
	l.MatchStatus = domain.BankStatementMatchStatus(status)
	return &l, nil
}

func (s *BankStore) ListLinesByStatementTx(ctx context.Context, tx pgx.Tx, statementID uuid.UUID) ([]domain.BankStatementLine, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+bankLineCols+` FROM bank_statement_lines WHERE statement_id = $1 ORDER BY line_no`,
		statementID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BankStatementLine{}
	for rows.Next() {
		l, err := scanBankLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *BankStore) GetLineTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.BankStatementLine, error) {
	row := tx.QueryRow(ctx, `SELECT `+bankLineCols+` FROM bank_statement_lines WHERE id = $1`, id)
	l, err := scanBankLine(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBankLineNotFound
	}
	return l, err
}

// MatchCandidate — a GL journal_line that could be the counterparty
// for an unmatched bank statement line. Built from the same bank GL
// account, with the side flipped (statement credit ↔ GL debit, since
// the bank's statement shows money from its perspective).
type MatchCandidate struct {
	JournalLineID uuid.UUID       `json:"journal_line_id"`
	EntryID       uuid.UUID       `json:"entry_id"`
	EntryNo       string          `json:"entry_no"`
	EntryDate     time.Time       `json:"entry_date"`
	Debit         decimal.Decimal `json:"debit"`
	Credit        decimal.Decimal `json:"credit"`
	Narration     string          `json:"narration"`
	SourceModule  *string         `json:"source_module,omitempty"`
	SourceRef     *string         `json:"source_ref,omitempty"`
}

// SuggestMatchesTx finds GL journal_line candidates for a bank
// statement line. Rules:
//   • Account: same GL account as the bank_account.gl_account_code
//   • Direction: bank credit ⇒ GL debit (cash came in to bank → debited
//     to 1020 in the GL); bank debit ⇒ GL credit (cash left bank →
//     credited from 1020).
//   • Amount: exact match on the relevant side.
//   • Date window: ± dayTolerance from the statement line's txn_date.
//   • Not already matched to a different bank line.
func (s *BankStore) SuggestMatchesTx(
	ctx context.Context, tx pgx.Tx,
	line domain.BankStatementLine,
	dayTolerance int,
) ([]MatchCandidate, error) {
	// Look up the bank account's GL code.
	var glCode string
	if err := tx.QueryRow(ctx,
		`SELECT gl_account_code FROM bank_accounts WHERE id = $1`,
		line.BankAccountID,
	).Scan(&glCode); err != nil {
		return nil, err
	}

	// Decide which side we're matching by looking at the bank line.
	var amount decimal.Decimal
	var glSide string // "debit" or "credit"
	if !line.Credit.IsZero() {
		// Bank credit (money in) → GL debit on cash account
		amount = line.Credit
		glSide = "debit"
	} else if !line.Debit.IsZero() {
		// Bank debit (money out) → GL credit on cash account
		amount = line.Debit
		glSide = "credit"
	} else {
		return []MatchCandidate{}, nil
	}

	q := `
		SELECT l.id, l.entry_id, je.entry_no, je.entry_date,
		       l.debit, l.credit, COALESCE(je.narration, ''),
		       je.source_module, je.source_ref
		  FROM journal_lines l
		  JOIN chart_of_accounts a ON a.id = l.account_id
		  JOIN journal_entries je  ON je.id = l.entry_id
		 WHERE a.code = $1
		   AND je.status = 'posted'
		   AND je.entry_date BETWEEN $2 AND $3
		   AND ` + glSide + ` = $4
		   AND NOT EXISTS (
		     SELECT 1 FROM bank_statement_lines bsl
		      WHERE bsl.matched_journal_line_id = l.id
		        AND bsl.id <> $5
		   )
		 ORDER BY je.entry_date, je.entry_no
		 LIMIT 20
	`
	from := line.TxnDate.AddDate(0, 0, -dayTolerance)
	to := line.TxnDate.AddDate(0, 0, dayTolerance)
	rows, err := tx.Query(ctx, q, glCode, from, to, amount, line.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MatchCandidate{}
	for rows.Next() {
		var c MatchCandidate
		if err := rows.Scan(
			&c.JournalLineID, &c.EntryID, &c.EntryNo, &c.EntryDate,
			&c.Debit, &c.Credit, &c.Narration,
			&c.SourceModule, &c.SourceRef,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MatchTx commits the link between a bank line and a GL journal_line.
// Verifies both that the line isn't already matched and that the
// journal_line isn't already matched to a different bank line.
func (s *BankStore) MatchTx(
	ctx context.Context, tx pgx.Tx,
	lineID, journalLineID uuid.UUID,
	matchKind domain.BankStatementMatchStatus,
	userID uuid.UUID,
	notes *string,
) (*domain.BankStatementLine, error) {
	line, err := s.GetLineTx(ctx, tx, lineID)
	if err != nil {
		return nil, err
	}
	if line.MatchStatus == domain.BankLineMatched || line.MatchStatus == domain.BankLineManualMatch {
		return nil, ErrLineAlreadyMatched
	}

	// Is this journal_line already matched to a different bank line?
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM bank_statement_lines WHERE matched_journal_line_id = $1 AND id <> $2
	`, journalLineID, lineID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if existing != uuid.Nil {
		return nil, ErrJournalAlreadyMatched
	}

	_, err = tx.Exec(ctx, `
		UPDATE bank_statement_lines
		   SET match_status = $2,
		       matched_journal_line_id = $3,
		       matched_at = now(),
		       matched_by = $4,
		       match_notes = NULLIF($5, '')
		 WHERE id = $1
	`, lineID, string(matchKind), journalLineID, userID, derefStrSafe(notes))
	if err != nil {
		return nil, err
	}
	return s.GetLineTx(ctx, tx, lineID)
}

// UnmatchTx reverts a bank line back to unmatched. The journal entry
// itself is not touched — only the link.
func (s *BankStore) UnmatchTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) (*domain.BankStatementLine, error) {
	cmd, err := tx.Exec(ctx, `
		UPDATE bank_statement_lines
		   SET match_status = 'unmatched',
		       matched_journal_line_id = NULL,
		       matched_at = NULL,
		       matched_by = NULL,
		       match_notes = NULL
		 WHERE id = $1 AND match_status IN ('matched','manual_match','adjusted')
	`, lineID)
	if err != nil {
		return nil, err
	}
	if cmd.RowsAffected() == 0 {
		return nil, ErrLineNotInMatchableState()
	}
	return s.GetLineTx(ctx, tx, lineID)
}

// ExcludeTx marks a line as excluded (e.g. a transfer between own
// accounts that was already recorded differently). No GL impact.
func (s *BankStore) ExcludeTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, userID uuid.UUID, reason string) (*domain.BankStatementLine, error) {
	_, err := tx.Exec(ctx, `
		UPDATE bank_statement_lines
		   SET match_status = 'excluded',
		       matched_at = now(),
		       matched_by = $2,
		       match_notes = NULLIF($3, '')
		 WHERE id = $1 AND match_status = 'unmatched'
	`, lineID, userID, reason)
	if err != nil {
		return nil, err
	}
	return s.GetLineTx(ctx, tx, lineID)
}

// MarkAdjustedTx links a bank line to a journal_line created by an
// adjustment post (bank charge, interest credit) — the kind of GL
// entry that didn't exist before reconciliation.
func (s *BankStore) MarkAdjustedTx(
	ctx context.Context, tx pgx.Tx,
	lineID, journalLineID uuid.UUID, userID uuid.UUID, notes string,
) (*domain.BankStatementLine, error) {
	_, err := tx.Exec(ctx, `
		UPDATE bank_statement_lines
		   SET match_status = 'adjusted',
		       matched_journal_line_id = $2,
		       matched_at = now(),
		       matched_by = $3,
		       match_notes = NULLIF($4, '')
		 WHERE id = $1
	`, lineID, journalLineID, userID, notes)
	if err != nil {
		return nil, err
	}
	return s.GetLineTx(ctx, tx, lineID)
}

func ErrLineNotInMatchableState() error {
	return errors.New("line is not in a matched/adjusted state to revert")
}

// ─────────── Reconciliation report ───────────

type ReconciliationReport struct {
	BankAccountID         uuid.UUID                   `json:"bank_account_id"`
	AsOf                  time.Time                   `json:"as_of"`
	GLAccountCode         string                      `json:"gl_account_code"`
	GLBalance             decimal.Decimal             `json:"gl_balance"`
	StatementBalance      *decimal.Decimal            `json:"statement_balance,omitempty"`
	StatementDate         *time.Time                  `json:"statement_date,omitempty"`
	OutstandingBankLines  []domain.BankStatementLine  `json:"outstanding_bank_lines"`
	OutstandingGLLines    []OutstandingGLLine         `json:"outstanding_gl_lines"`
	OutstandingBankCredit decimal.Decimal             `json:"outstanding_bank_credit"`
	OutstandingBankDebit  decimal.Decimal             `json:"outstanding_bank_debit"`
	OutstandingGLDebit    decimal.Decimal             `json:"outstanding_gl_debit"`
	OutstandingGLCredit   decimal.Decimal             `json:"outstanding_gl_credit"`
	AdjustedGLBalance     decimal.Decimal             `json:"adjusted_gl_balance"`
	Variance              decimal.Decimal             `json:"variance"`
	Reconciled            bool                        `json:"reconciled"`
}

type OutstandingGLLine struct {
	JournalLineID uuid.UUID       `json:"journal_line_id"`
	EntryNo       string          `json:"entry_no"`
	EntryDate     time.Time       `json:"entry_date"`
	Debit         decimal.Decimal `json:"debit"`
	Credit        decimal.Decimal `json:"credit"`
	Narration     string          `json:"narration"`
}

// ReconciliationTx computes the report at `asOf`. Outstanding items are:
//   • bank lines not yet matched/excluded/adjusted
//   • GL journal_lines on this bank's GL account, posted ≤ asOf, that
//     no bank_statement_line has matched (i.e. timing differences:
//     payments in transit, cheques unpresented)
func (s *BankStore) ReconciliationTx(
	ctx context.Context, tx pgx.Tx,
	bankAccountID uuid.UUID, asOf time.Time,
) (*ReconciliationReport, error) {
	acct, err := s.GetAccountTx(ctx, tx, bankAccountID)
	if err != nil {
		return nil, err
	}

	// GL balance for this account as-of.
	var glDebit, glCredit decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN je.id IS NOT NULL THEN l.debit  ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN je.id IS NOT NULL THEN l.credit ELSE 0 END), 0)
		FROM chart_of_accounts a
		LEFT JOIN journal_lines l   ON l.account_id = a.id
		LEFT JOIN journal_entries je ON je.id = l.entry_id
		                            AND je.status = 'posted'
		                            AND je.entry_date <= $2
		WHERE a.code = $1
	`, acct.GLAccountCode, asOf).Scan(&glDebit, &glCredit); err != nil {
		return nil, fmt.Errorf("read GL balance: %w", err)
	}
	glBalance := glDebit.Sub(glCredit)

	// Most recent statement ≤ asOf — its closing balance is the
	// statement-side reference.
	var stmtDate *time.Time
	var stmtClosing *decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT statement_date, closing_balance
		  FROM bank_statements
		 WHERE bank_account_id = $1 AND statement_date <= $2
		 ORDER BY statement_date DESC
		 LIMIT 1
	`, bankAccountID, asOf).Scan(&stmtDate, &stmtClosing); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Outstanding bank lines (unmatched ≤ asOf).
	outBankRows, err := tx.Query(ctx,
		`SELECT `+bankLineCols+` FROM bank_statement_lines
		  WHERE bank_account_id = $1 AND txn_date <= $2 AND match_status = 'unmatched'
		  ORDER BY txn_date, line_no`,
		bankAccountID, asOf,
	)
	if err != nil {
		return nil, err
	}
	var outBankCredit, outBankDebit decimal.Decimal
	outBank := []domain.BankStatementLine{}
	for outBankRows.Next() {
		l, err := scanBankLine(outBankRows)
		if err != nil {
			outBankRows.Close()
			return nil, err
		}
		outBankCredit = outBankCredit.Add(l.Credit)
		outBankDebit = outBankDebit.Add(l.Debit)
		outBank = append(outBank, *l)
	}
	outBankRows.Close()
	if err := outBankRows.Err(); err != nil {
		return nil, err
	}

	// Outstanding GL lines — posted to the bank GL account, not yet
	// matched by any bank statement line.
	outGLRows, err := tx.Query(ctx, `
		SELECT l.id, je.entry_no, je.entry_date, l.debit, l.credit, COALESCE(je.narration, '')
		  FROM journal_lines l
		  JOIN chart_of_accounts a ON a.id = l.account_id
		  JOIN journal_entries je  ON je.id = l.entry_id
		 WHERE a.code = $1
		   AND je.status = 'posted'
		   AND je.entry_date <= $2
		   AND NOT EXISTS (SELECT 1 FROM bank_statement_lines bsl WHERE bsl.matched_journal_line_id = l.id)
		 ORDER BY je.entry_date, je.entry_no
	`, acct.GLAccountCode, asOf)
	if err != nil {
		return nil, err
	}
	var outGLDebit, outGLCredit decimal.Decimal
	outGL := []OutstandingGLLine{}
	for outGLRows.Next() {
		var g OutstandingGLLine
		if err := outGLRows.Scan(&g.JournalLineID, &g.EntryNo, &g.EntryDate, &g.Debit, &g.Credit, &g.Narration); err != nil {
			outGLRows.Close()
			return nil, err
		}
		outGLDebit = outGLDebit.Add(g.Debit)
		outGLCredit = outGLCredit.Add(g.Credit)
		outGL = append(outGL, g)
	}
	outGLRows.Close()
	if err := outGLRows.Err(); err != nil {
		return nil, err
	}

	// Reconciliation math:
	//   Statement balance
	// + outstanding bank credits (money in per bank but not in GL yet)
	// - outstanding bank debits   (money out per bank but not in GL yet)
	// + outstanding GL debits     (money in per GL but not on statement yet)
	// - outstanding GL credits    (money out per GL but not on statement yet)
	// should equal the GL balance.
	//
	// We compute "adjusted GL balance" = statement balance + the four
	// outstanding totals (signed) and compare against GL balance.
	var adjusted decimal.Decimal
	if stmtClosing != nil {
		adjusted = *stmtClosing
	}
	// From bank's POV: credit = money into bank account.
	// From SACCO's GL POV: cash account is debited when money comes in.
	// "Outstanding bank credit" = bank says money came in, GL doesn't know yet
	//   → adding it bridges statement→GL since GL will catch up by debiting.
	adjusted = adjusted.Add(outBankCredit).Sub(outBankDebit)
	// "Outstanding GL debit" = GL has it but statement doesn't yet.
	//   Statement will catch up by crediting (i.e. money into bank) →
	//   we ADD outstanding GL debit to the statement side.
	adjusted = adjusted.Add(outGLDebit).Sub(outGLCredit)

	variance := glBalance.Sub(adjusted)

	return &ReconciliationReport{
		BankAccountID:         bankAccountID,
		AsOf:                  asOf,
		GLAccountCode:         acct.GLAccountCode,
		GLBalance:             glBalance,
		StatementBalance:      stmtClosing,
		StatementDate:         stmtDate,
		OutstandingBankLines:  outBank,
		OutstandingGLLines:    outGL,
		OutstandingBankCredit: outBankCredit,
		OutstandingBankDebit:  outBankDebit,
		OutstandingGLDebit:    outGLDebit,
		OutstandingGLCredit:   outGLCredit,
		AdjustedGLBalance:     adjusted,
		Variance:              variance,
		Reconciled:            variance.IsZero(),
	}, nil
}

func derefStrSafe(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
