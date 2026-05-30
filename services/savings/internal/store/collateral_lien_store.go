// Phase 1.5b — internal-account lien store.
//
// Three categories of "lien":
//   1. bosa_liens                  — Phase 5 BOSA-exit-on-active-loan
//                                     liens; one row per loan.
//   2. collateral_deposit_liens    — Phase 1.5b explicit pledges of
//                                     a deposit balance as security.
//   3. collateral_share_pledges    — Phase 1.5b pledges of share count.
//
// The withdraw + share-transfer gates must consider every active row
// from BOTH (deposit liens + bosa) or (share pledges + share_liens) to
// compute the truly-available balance.
//
// The store exposes per-account sum helpers + lifecycle Place/Release/
// Exercise calls. Handlers always run these inside WithTenantTx so RLS
// scopes the rows by tenant.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type CollateralLienStore struct {
	pool *pgxpool.Pool
}

func NewCollateralLienStore(pool *pgxpool.Pool) *CollateralLienStore {
	return &CollateralLienStore{pool: pool}
}

// Errors callers may check.
var (
	ErrLienAlreadyExists = errors.New("a lien already exists for this collateral item")
	ErrLienNotFound      = errors.New("lien not found")
	ErrPledgeNotFound    = errors.New("share pledge not found")
)

// ─────────── Deposit liens ───────────

type PlaceDepositLienInput struct {
	CollateralID     uuid.UUID
	DepositAccountID uuid.UUID
	LienedAmount     decimal.Decimal
	PlacedBy         uuid.UUID
}

// PlaceDepositLienTx writes a row with status='active'. Returns
// ErrLienAlreadyExists if the collateral already has one (the
// UNIQUE(collateral_id) constraint enforces this).
func (s *CollateralLienStore) PlaceDepositLienTx(ctx context.Context, tx pgx.Tx, in PlaceDepositLienInput) (*domain.CollateralDepositLien, error) {
	var l domain.CollateralDepositLien
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_deposit_liens (
		  tenant_id, collateral_id, deposit_account_id, liened_amount,
		  status, placed_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, 'active', $4
		)
		RETURNING id, tenant_id, collateral_id, deposit_account_id, liened_amount,
		          status, placed_at, placed_by,
		          released_at, released_by, released_reason,
		          exercised_at, exercised_by, exercise_reason
	`, in.CollateralID, in.DepositAccountID, in.LienedAmount, in.PlacedBy).Scan(
		&l.ID, &l.TenantID, &l.CollateralID, &l.DepositAccountID, &l.LienedAmount,
		&l.Status, &l.PlacedAt, &l.PlacedBy,
		&l.ReleasedAt, &l.ReleasedBy, &l.ReleasedReason,
		&l.ExercisedAt, &l.ExercisedBy, &l.ExerciseReason,
	)
	if err != nil {
		if isCollateralLienUniqueViolation(err) {
			return nil, ErrLienAlreadyExists
		}
		return nil, err
	}
	return &l, nil
}

// SumActiveDepositLiensTx returns the running KES sum of liened_amount
// across every active (or partially_released) collateral_deposit_lien
// on a deposit account, PLUS the Phase 5 bosa_liens.amount for the
// same account. The withdraw gate subtracts this from the running
// balance to get the truly-available figure.
//
// bosa_liens use bosa_account_id (== deposit_accounts.id when the
// account's product segment is BOSA).
func (s *CollateralLienStore) SumActiveDepositLiensTx(ctx context.Context, tx pgx.Tx, depositAccountID uuid.UUID) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE((
		    SELECT SUM(liened_amount)
		      FROM collateral_deposit_liens
		     WHERE deposit_account_id = $1
		       AND status IN ('active','partially_released')
		  ), 0)
		  +
		  COALESCE((
		    SELECT SUM(amount)
		      FROM bosa_liens
		     WHERE bosa_account_id = $1
		       AND status IN ('active','partially_released')
		  ), 0)
	`, depositAccountID).Scan(&sum)
	return sum, err
}

// ActiveDepositLiensByAccountTx — the per-account list. Used by the
// Member 360 deposit-account view to show "what's locked".
func (s *CollateralLienStore) ActiveDepositLiensByAccountTx(ctx context.Context, tx pgx.Tx, depositAccountID uuid.UUID) ([]domain.CollateralDepositLien, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, deposit_account_id, liened_amount,
		       status, placed_at, placed_by,
		       released_at, released_by, released_reason,
		       exercised_at, exercised_by, exercise_reason
		  FROM collateral_deposit_liens
		 WHERE deposit_account_id = $1
		   AND status IN ('active','partially_released')
		 ORDER BY placed_at
	`, depositAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralDepositLien
	for rows.Next() {
		var l domain.CollateralDepositLien
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.CollateralID, &l.DepositAccountID, &l.LienedAmount,
			&l.Status, &l.PlacedAt, &l.PlacedBy,
			&l.ReleasedAt, &l.ReleasedBy, &l.ReleasedReason,
			&l.ExercisedAt, &l.ExercisedBy, &l.ExerciseReason,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DepositLienByCollateralTx — the row for one collateral item. Returns
// ErrLienNotFound when absent.
func (s *CollateralLienStore) DepositLienByCollateralTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) (*domain.CollateralDepositLien, error) {
	var l domain.CollateralDepositLien
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, collateral_id, deposit_account_id, liened_amount,
		       status, placed_at, placed_by,
		       released_at, released_by, released_reason,
		       exercised_at, exercised_by, exercise_reason
		  FROM collateral_deposit_liens
		 WHERE collateral_id = $1
	`, collateralID).Scan(
		&l.ID, &l.TenantID, &l.CollateralID, &l.DepositAccountID, &l.LienedAmount,
		&l.Status, &l.PlacedAt, &l.PlacedBy,
		&l.ReleasedAt, &l.ReleasedBy, &l.ReleasedReason,
		&l.ExercisedAt, &l.ExercisedBy, &l.ExerciseReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLienNotFound
	}
	return &l, err
}

// ReleaseDepositLienTx — terminal flip, status → 'released'. Used by
// the collateral-release handler.
func (s *CollateralLienStore) ReleaseDepositLienTx(ctx context.Context, tx pgx.Tx, collateralID, actor uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE collateral_deposit_liens SET
		  status          = 'released',
		  released_at     = now(),
		  released_by     = $2,
		  released_reason = $3
		 WHERE collateral_id = $1 AND status IN ('active','partially_released')
	`, collateralID, actor, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLienNotFound
	}
	return nil
}

// ExerciseDepositLienTx — used when the loan write-off exercise path
// applies the lien proceeds against the loan. status → 'exercised'.
func (s *CollateralLienStore) ExerciseDepositLienTx(ctx context.Context, tx pgx.Tx, collateralID, actor uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE collateral_deposit_liens SET
		  status          = 'exercised',
		  exercised_at    = now(),
		  exercised_by    = $2,
		  exercise_reason = $3
		 WHERE collateral_id = $1 AND status IN ('active','partially_released')
	`, collateralID, actor, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLienNotFound
	}
	return nil
}

// ─────────── Share pledges ───────────

type PlaceSharePledgeInput struct {
	CollateralID      uuid.UUID
	ShareAccountID    uuid.UUID
	PledgedShareCount int
	PlacedBy          uuid.UUID
}

func (s *CollateralLienStore) PlaceSharePledgeTx(ctx context.Context, tx pgx.Tx, in PlaceSharePledgeInput) (*domain.CollateralSharePledge, error) {
	var p domain.CollateralSharePledge
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_share_pledges (
		  tenant_id, collateral_id, share_account_id, pledged_share_count,
		  status, placed_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, 'active', $4
		)
		RETURNING id, tenant_id, collateral_id, share_account_id, pledged_share_count,
		          status, placed_at, placed_by,
		          released_at, released_by, released_reason,
		          exercised_at, exercised_by, exercise_reason
	`, in.CollateralID, in.ShareAccountID, in.PledgedShareCount, in.PlacedBy).Scan(
		&p.ID, &p.TenantID, &p.CollateralID, &p.ShareAccountID, &p.PledgedShareCount,
		&p.Status, &p.PlacedAt, &p.PlacedBy,
		&p.ReleasedAt, &p.ReleasedBy, &p.ReleasedReason,
		&p.ExercisedAt, &p.ExercisedBy, &p.ExerciseReason,
	)
	if err != nil {
		if isCollateralLienUniqueViolation(err) {
			return nil, ErrLienAlreadyExists
		}
		return nil, err
	}
	// Keep the denormalised share_accounts.shares_pledged in sync so
	// the existing share-transfer gate (handler/share.go:887) blocks
	// debits against pledged shares without needing a second sum-query.
	// The CHECK constraint shares_pledged <= shares_held enforces the
	// "can't pledge more than you own" invariant at the DB level.
	if _, err := tx.Exec(ctx, `
		UPDATE share_accounts SET shares_pledged = shares_pledged + $2
		 WHERE id = $1
	`, in.ShareAccountID, in.PledgedShareCount); err != nil {
		return nil, fmt.Errorf("bump shares_pledged: %w", err)
	}
	return &p, nil
}

// SumActiveSharePledgesTx — share-count sum across both
// collateral_share_pledges AND the existing share_liens table (Phase 5
// BOSA-exit pledges live there). Transfer gate subtracts this from
// share_accounts.shares_held to validate.
//
// Note: share_accounts.shares_pledged is the running denormalised
// counter the existing transfer gate uses; we don't trust that for the
// extra layer because Phase 1.5b inserts go through collateral_share_pledges
// without updating shares_pledged.
func (s *CollateralLienStore) SumActiveSharePledgesTx(ctx context.Context, tx pgx.Tx, shareAccountID uuid.UUID) (int, error) {
	var sum int
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE((
		    SELECT SUM(pledged_share_count)
		      FROM collateral_share_pledges
		     WHERE share_account_id = $1
		       AND status IN ('active','partially_released')
		  ), 0)
		  +
		  COALESCE((
		    SELECT SUM(shares_pledged)
		      FROM share_liens
		     WHERE share_account_id = $1
		       AND status = 'active'
		  ), 0)
	`, shareAccountID).Scan(&sum)
	return sum, err
}

// ReleaseSharePledgeTx — terminal release. Decrements
// share_accounts.shares_pledged so the existing transfer gate frees
// the shares back for redemption.
func (s *CollateralLienStore) ReleaseSharePledgeTx(ctx context.Context, tx pgx.Tx, collateralID, actor uuid.UUID, reason string) error {
	// Snapshot the row so we know how many shares to free.
	p, err := s.SharePledgeByCollateralTx(ctx, tx, collateralID)
	if err != nil {
		return err
	}
	if p.Status == "released" || p.Status == "exercised" {
		return nil // already terminal — no-op
	}
	tag, err := tx.Exec(ctx, `
		UPDATE collateral_share_pledges SET
		  status          = 'released',
		  released_at     = now(),
		  released_by     = $2,
		  released_reason = $3
		 WHERE collateral_id = $1 AND status IN ('active','partially_released')
	`, collateralID, actor, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPledgeNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE share_accounts SET shares_pledged = GREATEST(0, shares_pledged - $2)
		 WHERE id = $1
	`, p.ShareAccountID, p.PledgedShareCount); err != nil {
		return fmt.Errorf("decrement shares_pledged: %w", err)
	}
	return nil
}

// SharePledgeByCollateralTx — single-row lookup.
func (s *CollateralLienStore) SharePledgeByCollateralTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) (*domain.CollateralSharePledge, error) {
	var p domain.CollateralSharePledge
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, collateral_id, share_account_id, pledged_share_count,
		       status, placed_at, placed_by,
		       released_at, released_by, released_reason,
		       exercised_at, exercised_by, exercise_reason
		  FROM collateral_share_pledges
		 WHERE collateral_id = $1
	`, collateralID).Scan(
		&p.ID, &p.TenantID, &p.CollateralID, &p.ShareAccountID, &p.PledgedShareCount,
		&p.Status, &p.PlacedAt, &p.PlacedBy,
		&p.ReleasedAt, &p.ReleasedBy, &p.ReleasedReason,
		&p.ExercisedAt, &p.ExercisedBy, &p.ExerciseReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPledgeNotFound
	}
	return &p, err
}

// ReleaseLiensForCollateralTx — convenience for the collateral-release
// path: silently does nothing if neither a deposit lien nor a share
// pledge exists. Used by handler.CollateralHandler.Release to keep
// the downstream cleanup simple.
func (s *CollateralLienStore) ReleaseLiensForCollateralTx(ctx context.Context, tx pgx.Tx, collateralID, actor uuid.UUID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_deposit_liens SET
		  status='released', released_at=now(), released_by=$2, released_reason=$3
		 WHERE collateral_id = $1 AND status IN ('active','partially_released')
	`, collateralID, actor, reason); err != nil {
		return fmt.Errorf("release deposit lien: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_share_pledges SET
		  status='released', released_at=now(), released_by=$2, released_reason=$3
		 WHERE collateral_id = $1 AND status IN ('active','partially_released')
	`, collateralID, actor, reason); err != nil {
		return fmt.Errorf("release share pledge: %w", err)
	}
	return nil
}

// isCollateralLienUniqueViolation — pgx returns *pgconn.PgError with
// SQLState '23505' on UNIQUE conflicts. Local helper so we don't import
// pgconn in the store layer.
func isCollateralLienUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsSub(s, "duplicate key value") || containsSub(s, "23505")
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
