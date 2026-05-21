// notification_credit_balances + notification_credit_ledger access.
//
// All mutating methods take a pgx.Tx — callers are expected to have
// opened a tenant-scoped transaction via db.Pool.WithTenantTx so RLS
// applies. Every credit movement writes to BOTH the balance row and
// the ledger atomically; that's the contract that prevents "phantom"
// credits (balance moved but no audit row, or vice versa).

package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type CreditStore struct {
	pool *pgxpool.Pool
}

func NewCreditStore(pool *pgxpool.Pool) *CreditStore {
	return &CreditStore{pool: pool}
}

// ErrInsufficientCredits is returned by DeductTx when the balance is
// below the requested debit amount. Callers handle it by marking the
// delivery `blocked` with reason "insufficient_credits" rather than
// retrying.
var ErrInsufficientCredits = errors.New("credit: insufficient balance")

// ─────────── Read ───────────

func (s *CreditStore) BalanceTx(ctx context.Context, tx pgx.Tx, channel domain.Channel) (*domain.CreditBalance, error) {
	row := tx.QueryRow(ctx, `
		SELECT tenant_id, channel, balance, low_balance_threshold,
		       low_balance_alerted_at, zero_balance_alerted_at,
		       last_topup_at, last_topup_credits, updated_at
		FROM notification_credit_balances
		WHERE channel = $1
	`, string(channel))
	return scanBalance(row)
}

func (s *CreditStore) AllBalancesTx(ctx context.Context, tx pgx.Tx) ([]domain.CreditBalance, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, channel, balance, low_balance_threshold,
		       low_balance_alerted_at, zero_balance_alerted_at,
		       last_topup_at, last_topup_credits, updated_at
		FROM notification_credit_balances
		ORDER BY channel
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CreditBalance{}
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// AllBalancesAcrossTenantsTx — for the platform-admin overview. Caller
// must be running with RLS bypassed (BYPASSRLS role) or with no tenant
// context set; the query intentionally omits the RLS filter via a
// SECURITY-DEFINER-style approach we don't have, so we rely on the
// caller already being unscoped.
func (s *CreditStore) AllBalancesAcrossTenantsTx(ctx context.Context, tx pgx.Tx) ([]domain.CreditBalance, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, channel, balance, low_balance_threshold,
		       low_balance_alerted_at, zero_balance_alerted_at,
		       last_topup_at, last_topup_credits, updated_at
		FROM notification_credit_balances
		ORDER BY tenant_id, channel
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CreditBalance{}
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBalance(row pgx.Row) (*domain.CreditBalance, error) {
	var b domain.CreditBalance
	var channel string
	err := row.Scan(
		&b.TenantID, &channel, &b.Balance, &b.LowBalanceThreshold,
		&b.LowBalanceAlertedAt, &b.ZeroBalanceAlertedAt,
		&b.LastTopupAt, &b.LastTopupCredits, &b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.Channel = domain.Channel(channel)
	return &b, nil
}

// ─────────── Atomic deduct (workers) ───────────

// DeductTx locks the balance row, verifies sufficient credits, debits
// by `amount`, writes a `consumption` ledger entry — all in one round
// trip. The whole thing must run inside a tenant-scoped tx; the worker
// commits that tx only if the actual provider send succeeded, so a
// transient provider error rolls everything back and no credit is
// burned.
//
// Returns the new balance (post-deduction). Returns ErrInsufficientCredits
// if balance < amount; the caller should mark the delivery blocked.
type DeductInput struct {
	Channel        domain.Channel
	Amount         int            // always 1 today, parameterised in case multi-credit messages land
	NotificationID *uuid.UUID
	DeliveryID     *uuid.UUID
	Notes          string
}

func (s *CreditStore) DeductTx(ctx context.Context, tx pgx.Tx, in DeductInput) (int, error) {
	if in.Amount <= 0 {
		return 0, fmt.Errorf("credit: deduct amount must be positive, got %d", in.Amount)
	}
	// SELECT ... FOR UPDATE to serialise concurrent deductions for the
	// same (tenant, channel). The next worker thread will wait until
	// the tx commits, then re-read the new balance.
	var balance int
	err := tx.QueryRow(ctx, `
		SELECT balance FROM notification_credit_balances
		WHERE channel = $1 FOR UPDATE
	`, string(in.Channel)).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInsufficientCredits
	}
	if err != nil {
		return 0, err
	}
	if balance < in.Amount {
		return balance, ErrInsufficientCredits
	}
	newBalance := balance - in.Amount
	if _, err := tx.Exec(ctx, `
		UPDATE notification_credit_balances
		SET balance = $2, updated_at = now()
		WHERE channel = $1
	`, string(in.Channel), newBalance); err != nil {
		return balance, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_credit_ledger
		    (tenant_id, channel, movement_type, credits, balance_after,
		     notification_id, delivery_id, notes)
		VALUES (current_tenant_id(), $1, 'consumption', $2, $3, $4, $5, $6)
	`,
		string(in.Channel), -in.Amount, newBalance,
		in.NotificationID, in.DeliveryID, nullIfEmpty(in.Notes),
	); err != nil {
		return balance, err
	}
	return newBalance, nil
}

// ─────────── Top-up (platform admin) ───────────

type TopupInput struct {
	Channel    domain.Channel
	Credits    int          // must be > 0
	Reference  string       // PO / invoice
	ActionedBy uuid.UUID    // platform admin user id
	Notes      string
}

func (s *CreditStore) TopupTx(ctx context.Context, tx pgx.Tx, in TopupInput) (int, uuid.UUID, error) {
	if in.Credits <= 0 {
		return 0, uuid.Nil, fmt.Errorf("credit: topup amount must be positive")
	}
	var balance int
	err := tx.QueryRow(ctx, `
		SELECT balance FROM notification_credit_balances
		WHERE channel = $1 FOR UPDATE
	`, string(in.Channel)).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return 0, uuid.Nil, err
	}
	newBalance := balance + in.Credits
	if _, err := tx.Exec(ctx, `
		UPDATE notification_credit_balances
		SET balance = $2,
		    last_topup_at = now(),
		    last_topup_credits = $3,
		    low_balance_alerted_at = NULL,
		    zero_balance_alerted_at = NULL,
		    updated_at = now()
		WHERE channel = $1
	`, string(in.Channel), newBalance, in.Credits); err != nil {
		return balance, uuid.Nil, err
	}
	var ledgerID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO notification_credit_ledger
		    (tenant_id, channel, movement_type, credits, balance_after,
		     reference, actioned_by, notes)
		VALUES (current_tenant_id(), $1, 'topup', $2, $3, $4, $5, $6)
		RETURNING id
	`,
		string(in.Channel), in.Credits, newBalance,
		nullIfEmpty(in.Reference), in.ActionedBy, nullIfEmpty(in.Notes),
	).Scan(&ledgerID)
	if err != nil {
		return balance, uuid.Nil, err
	}
	return newBalance, ledgerID, nil
}

// ─────────── Adjustment (post-approval) ───────────

// ApplyAdjustmentTx posts the credits delta from an already-approved
// adjustment row to the balance + ledger. Caller is the
// platform-admin checker; we trust that the upstream maker/checker
// machinery already validated the actor split.
type ApplyAdjustmentInput struct {
	Channel    domain.Channel
	Credits    int       // positive or negative; non-zero
	Reason     string
	ActionedBy uuid.UUID // the approver
}

func (s *CreditStore) ApplyAdjustmentTx(ctx context.Context, tx pgx.Tx, in ApplyAdjustmentInput) (int, uuid.UUID, error) {
	if in.Credits == 0 {
		return 0, uuid.Nil, fmt.Errorf("credit: adjustment cannot be zero")
	}
	var balance int
	err := tx.QueryRow(ctx, `
		SELECT balance FROM notification_credit_balances
		WHERE channel = $1 FOR UPDATE
	`, string(in.Channel)).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return 0, uuid.Nil, err
	}
	newBalance := balance + in.Credits
	if newBalance < 0 {
		return balance, uuid.Nil, fmt.Errorf("credit: adjustment would push balance below zero (current=%d, delta=%d)", balance, in.Credits)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notification_credit_balances
		SET balance = $2, updated_at = now()
		WHERE channel = $1
	`, string(in.Channel), newBalance); err != nil {
		return balance, uuid.Nil, err
	}
	var ledgerID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO notification_credit_ledger
		    (tenant_id, channel, movement_type, credits, balance_after,
		     actioned_by, notes)
		VALUES (current_tenant_id(), $1, 'adjustment', $2, $3, $4, $5)
		RETURNING id
	`,
		string(in.Channel), in.Credits, newBalance,
		in.ActionedBy, nullIfEmpty(in.Reason),
	).Scan(&ledgerID)
	if err != nil {
		return balance, uuid.Nil, err
	}
	return newBalance, ledgerID, nil
}

// ─────────── Settings (low-balance threshold) ───────────

func (s *CreditStore) SetLowBalanceThresholdTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, threshold int) error {
	if threshold < 0 {
		return fmt.Errorf("threshold must be >= 0")
	}
	_, err := tx.Exec(ctx, `
		UPDATE notification_credit_balances
		SET low_balance_threshold = $2,
		    low_balance_alerted_at = NULL,
		    updated_at = now()
		WHERE channel = $1
	`, string(channel), threshold)
	return err
}

// MarkAlertedTx flips the alert-sent timestamps so the alerter doesn't
// re-fire on every send. `which` is "low" or "zero".
func (s *CreditStore) MarkAlertedTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, which string) error {
	var col string
	switch which {
	case "low":
		col = "low_balance_alerted_at"
	case "zero":
		col = "zero_balance_alerted_at"
	default:
		return fmt.Errorf("unknown alert kind %q", which)
	}
	_, err := tx.Exec(ctx,
		`UPDATE notification_credit_balances SET `+col+` = now(), updated_at = now() WHERE channel = $1`,
		string(channel),
	)
	return err
}

// ─────────── Ledger reads ───────────

type LedgerFilter struct {
	Channel      string
	MovementType string
	Limit        int
	Offset       int
}

func (s *CreditStore) ListLedgerTx(ctx context.Context, tx pgx.Tx, f LedgerFilter) ([]domain.CreditLedgerEntry, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Channel != "" {
		where += " AND channel = $" + strconv.Itoa(idx)
		args = append(args, f.Channel)
		idx++
	}
	if f.MovementType != "" {
		where += " AND movement_type = $" + strconv.Itoa(idx)
		args = append(args, f.MovementType)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notification_credit_ledger `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit, f.Offset)
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, channel, movement_type, credits, balance_after,
		       notification_id, delivery_id, reference, actioned_by, notes, created_at
		FROM notification_credit_ledger
		`+where+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.CreditLedgerEntry{}
	for rows.Next() {
		var e domain.CreditLedgerEntry
		var channel, movement string
		if err := rows.Scan(
			&e.ID, &e.TenantID, &channel, &movement, &e.Credits, &e.BalanceAfter,
			&e.NotificationID, &e.DeliveryID, &e.Reference, &e.ActionedBy, &e.Notes, &e.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		e.Channel = domain.Channel(channel)
		e.MovementType = domain.CreditMovementType(movement)
		out = append(out, e)
	}
	return out, total, rows.Err()
}
