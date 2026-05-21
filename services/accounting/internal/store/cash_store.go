// Cash & Float Management persistence. Pure DB layer — the handler
// orchestrates GL posting via posting.Engine; this store only writes
// the operational records (tills, sessions, transfers) and computes
// the per-till cash position from the cash_transfers stream.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
)

type CashStore struct {
	pool *pgxpool.Pool
}

func NewCashStore(pool *pgxpool.Pool) *CashStore {
	return &CashStore{pool: pool}
}

var (
	ErrTillNotFound       = errors.New("till not found")
	ErrSessionNotFound    = errors.New("till session not found")
	ErrSessionAlreadyOpen = errors.New("till already has an open session")
	ErrSessionNotOpen     = errors.New("till session is not open")
)

// ─────────── Tills ───────────

const tillCols = `
	id, tenant_id, code, name, branch, gl_account_code, vault_account_code,
	variance_account_code, max_float, is_active, notes, created_at, updated_at
`

func scanTill(row pgx.Row) (*domain.Till, error) {
	var t domain.Till
	err := row.Scan(
		&t.ID, &t.TenantID, &t.Code, &t.Name, &t.Branch, &t.GLAccountCode, &t.VaultAccountCode,
		&t.VarianceAccountCode, &t.MaxFloat, &t.IsActive, &t.Notes, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *CashStore) ListTillsTx(ctx context.Context, tx pgx.Tx, activeOnly bool) ([]domain.Till, error) {
	q := `SELECT ` + tillCols + ` FROM tills`
	if activeOnly {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY code`
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Till{}
	for rows.Next() {
		t, err := scanTill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *CashStore) GetTillTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Till, error) {
	row := tx.QueryRow(ctx, `SELECT `+tillCols+` FROM tills WHERE id = $1`, id)
	t, err := scanTill(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTillNotFound
	}
	return t, err
}

type CreateTillInput struct {
	Code     string
	Name     string
	Branch   *string
	MaxFloat *decimal.Decimal
	Notes    *string
}

func (s *CashStore) CreateTillTx(ctx context.Context, tx pgx.Tx, in CreateTillInput) (*domain.Till, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO tills (tenant_id, code, name, branch, max_float, notes)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		RETURNING `+tillCols, in.Code, in.Name, in.Branch, in.MaxFloat, in.Notes)
	return scanTill(row)
}

// ─────────── Sessions ───────────

const sessionCols = `
	id, tenant_id, till_id, teller_user_id, status,
	opening_float, expected_close, actual_close, variance,
	variance_journal_entry_id,
	opened_at, opened_by, closed_at, closed_by, notes
`

func scanSession(row pgx.Row) (*domain.TillSession, error) {
	var s domain.TillSession
	var status string
	err := row.Scan(
		&s.ID, &s.TenantID, &s.TillID, &s.TellerUserID, &status,
		&s.OpeningFloat, &s.ExpectedClose, &s.ActualClose, &s.Variance,
		&s.VarianceJournalEntryID,
		&s.OpenedAt, &s.OpenedBy, &s.ClosedAt, &s.ClosedBy, &s.Notes,
	)
	if err != nil {
		return nil, err
	}
	s.Status = domain.TillSessionStatus(status)
	return &s, nil
}

func (st *CashStore) GetSessionTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.TillSession, error) {
	row := tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM till_sessions WHERE id = $1`, id)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	return s, err
}

func (st *CashStore) CurrentSessionTx(ctx context.Context, tx pgx.Tx, tillID uuid.UUID) (*domain.TillSession, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM till_sessions WHERE till_id = $1 AND status = 'open' LIMIT 1`,
		tillID,
	)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // no open session is a normal state
	}
	return s, err
}

func (st *CashStore) ListSessionsByTillTx(ctx context.Context, tx pgx.Tx, tillID uuid.UUID, limit int) ([]domain.TillSession, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := tx.Query(ctx,
		`SELECT `+sessionCols+` FROM till_sessions WHERE till_id = $1 ORDER BY opened_at DESC LIMIT $2`,
		tillID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TillSession{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

type OpenSessionInput struct {
	TillID       uuid.UUID
	TellerUserID uuid.UUID
	OpeningFloat decimal.Decimal
	Notes        *string
	OpenedBy     uuid.UUID
}

// OpenSessionTx inserts a new session row in 'open' state. The unique
// index `till_sessions_one_open_per_till` enforces that only one open
// session exists per till — if there's a conflict the caller gets
// ErrSessionAlreadyOpen back.
func (st *CashStore) OpenSessionTx(ctx context.Context, tx pgx.Tx, in OpenSessionInput) (*domain.TillSession, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO till_sessions (
		  tenant_id, till_id, teller_user_id, status,
		  opening_float, expected_close, opened_by, notes
		) VALUES (current_tenant_id(), $1, $2, 'open', $3, $3, $4, $5)
		RETURNING `+sessionCols,
		in.TillID, in.TellerUserID, in.OpeningFloat, in.OpenedBy, in.Notes,
	)
	s, err := scanSession(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSessionAlreadyOpen
		}
		return nil, err
	}
	return s, nil
}

type CloseSessionInput struct {
	SessionID              uuid.UUID
	ActualClose            decimal.Decimal
	Variance               decimal.Decimal
	VarianceJournalEntryID *uuid.UUID
	ClosedBy               uuid.UUID
	Notes                  string
}

func (st *CashStore) CloseSessionTx(ctx context.Context, tx pgx.Tx, in CloseSessionInput) (*domain.TillSession, error) {
	notes := ""
	if in.Notes != "" {
		notes = in.Notes
	}
	cmd, err := tx.Exec(ctx, `
		UPDATE till_sessions
		   SET status = 'closed',
		       actual_close = $2,
		       variance = $3,
		       variance_journal_entry_id = $4,
		       closed_at = now(),
		       closed_by = $5,
		       notes = CASE WHEN $6 = '' THEN notes ELSE COALESCE(notes,'') || E'\n[CLOSE] ' || $6 END
		 WHERE id = $1 AND status = 'open'
	`, in.SessionID, in.ActualClose, in.Variance, in.VarianceJournalEntryID, in.ClosedBy, notes)
	if err != nil {
		return nil, err
	}
	if cmd.RowsAffected() == 0 {
		return nil, ErrSessionNotOpen
	}
	return st.GetSessionTx(ctx, tx, in.SessionID)
}

// AdjustSessionExpectedTx bumps the expected_close by `delta` when a
// transfer in/out happens during the session. Positive delta = cash in,
// negative = cash out.
func (st *CashStore) AdjustSessionExpectedTx(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, delta decimal.Decimal) error {
	_, err := tx.Exec(ctx,
		`UPDATE till_sessions SET expected_close = expected_close + $2 WHERE id = $1 AND status = 'open'`,
		sessionID, delta,
	)
	return err
}

// ─────────── Cash transfers ───────────

const transferCols = `
	id, tenant_id, transfer_type, from_till_id, to_till_id, session_id,
	amount, reference, narration, journal_entry_id, transferred_at, transferred_by
`

func scanTransfer(row pgx.Row) (*domain.CashTransfer, error) {
	var t domain.CashTransfer
	var typ string
	err := row.Scan(
		&t.ID, &t.TenantID, &typ, &t.FromTillID, &t.ToTillID, &t.SessionID,
		&t.Amount, &t.Reference, &t.Narration, &t.JournalEntryID,
		&t.TransferredAt, &t.TransferredBy,
	)
	if err != nil {
		return nil, err
	}
	t.TransferType = domain.CashTransferType(typ)
	return &t, nil
}

type CreateTransferInput struct {
	TransferType   domain.CashTransferType
	FromTillID     *uuid.UUID
	ToTillID       *uuid.UUID
	SessionID      *uuid.UUID
	Amount         decimal.Decimal
	Reference      *string
	Narration      *string
	JournalEntryID *uuid.UUID
	TransferredBy  uuid.UUID
}

func (st *CashStore) CreateTransferTx(ctx context.Context, tx pgx.Tx, in CreateTransferInput) (*domain.CashTransfer, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO cash_transfers (
		  tenant_id, transfer_type, from_till_id, to_till_id, session_id,
		  amount, reference, narration, journal_entry_id, transferred_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+transferCols,
		string(in.TransferType), in.FromTillID, in.ToTillID, in.SessionID,
		in.Amount, in.Reference, in.Narration, in.JournalEntryID, in.TransferredBy,
	)
	return scanTransfer(row)
}

func (st *CashStore) ListTransfersTx(ctx context.Context, tx pgx.Tx, sessionID *uuid.UUID, limit int) ([]domain.CashTransfer, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if sessionID != nil {
		rows, err = tx.Query(ctx,
			`SELECT `+transferCols+` FROM cash_transfers WHERE session_id = $1 ORDER BY transferred_at DESC LIMIT $2`,
			*sessionID, limit,
		)
	} else {
		rows, err = tx.Query(ctx,
			`SELECT `+transferCols+` FROM cash_transfers ORDER BY transferred_at DESC LIMIT $1`,
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CashTransfer{}
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ─────────── Cash position ───────────

type CashPosition struct {
	VaultBalance   decimal.Decimal `json:"vault_balance"`
	TillBalance    decimal.Decimal `json:"till_balance"`
	VarianceBalance decimal.Decimal `json:"variance_balance"`
	GrandTotal     decimal.Decimal `json:"grand_total"`
	TillBreakdown  []TillBalance   `json:"till_breakdown"`
}

type TillBalance struct {
	TillID         uuid.UUID       `json:"till_id"`
	TillCode       string          `json:"till_code"`
	TillName       string          `json:"till_name"`
	HasOpenSession bool            `json:"has_open_session"`
	SessionID      *uuid.UUID      `json:"session_id,omitempty"`
	TellerUserID   *uuid.UUID      `json:"teller_user_id,omitempty"`
	ExpectedBalance decimal.Decimal `json:"expected_balance"`
}

// CashPositionTx — snapshot of the SACCO's physical cash:
//   • Vault balance from GL 1000
//   • Till aggregate from GL 1010
//   • Variance balance from 2250
//   • Per-till expected balance from open sessions (session's
//     expected_close, which is opening_float + transfers in/out)
//
// We DO NOT try to reconcile per-till balances against the aggregate
// GL 1010 here — that's done in the operational close. This is purely
// a viewable position.
func (st *CashStore) CashPositionTx(ctx context.Context, tx pgx.Tx) (*CashPosition, error) {
	// Read the four cash account balances.
	cashAccts := []string{"1000", "1010", "2250"}
	bal := map[string]decimal.Decimal{}
	for _, code := range cashAccts {
		var net decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(l.debit - l.credit), 0)
			  FROM chart_of_accounts a
			  JOIN journal_lines l ON l.account_id = a.id
			  JOIN journal_entries je ON je.id = l.entry_id
			 WHERE a.code = $1 AND je.status = 'posted'
		`, code).Scan(&net); err != nil {
			return nil, fmt.Errorf("balance for %s: %w", code, err)
		}
		bal[code] = net
	}

	// Per-till expected balance from open sessions.
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.code, t.name,
		       (SELECT id FROM till_sessions WHERE till_id = t.id AND status = 'open' LIMIT 1),
		       (SELECT teller_user_id FROM till_sessions WHERE till_id = t.id AND status = 'open' LIMIT 1),
		       (SELECT expected_close FROM till_sessions WHERE till_id = t.id AND status = 'open' LIMIT 1)
		  FROM tills t
		 WHERE t.is_active = true
		 ORDER BY t.code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	breakdown := []TillBalance{}
	for rows.Next() {
		var (
			b               TillBalance
			openSessionID   *uuid.UUID
			openTeller      *uuid.UUID
			expectedClose   *decimal.Decimal
		)
		if err := rows.Scan(&b.TillID, &b.TillCode, &b.TillName, &openSessionID, &openTeller, &expectedClose); err != nil {
			return nil, err
		}
		if openSessionID != nil {
			b.HasOpenSession = true
			b.SessionID = openSessionID
			b.TellerUserID = openTeller
			if expectedClose != nil {
				b.ExpectedBalance = *expectedClose
			}
		}
		breakdown = append(breakdown, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	vault := bal["1000"]
	till := bal["1010"]
	variance := bal["2250"].Neg() // 2250 is liability (credit normal); display as positive when there's a short
	return &CashPosition{
		VaultBalance:    vault,
		TillBalance:     till,
		VarianceBalance: variance,
		GrandTotal:      vault.Add(till),
		TillBreakdown:   breakdown,
	}, nil
}

// isUniqueViolation detects PG's 23505 unique_violation error code.
func isUniqueViolation(err error) bool {
	type pgErr interface{ SQLState() string }
	if e, ok := err.(pgErr); ok {
		return e.SQLState() == "23505"
	}
	return false
}
