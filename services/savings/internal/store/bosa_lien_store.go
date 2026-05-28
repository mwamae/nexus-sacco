// Loans Phase 5 — BOSA lien store.
//
// A lien is placed when a loan is disbursed (against the borrower's
// BOSA account) and released when the loan settles or writes off.
// One row per loan, enforced by UNIQUE (loan_id) — re-running the
// disbursement (e.g. via a finalize callback) is idempotent.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type BOSALienStore struct {
	pool *pgxpool.Pool
}

func NewBOSALienStore(pool *pgxpool.Pool) *BOSALienStore { return &BOSALienStore{pool: pool} }

type BOSALien struct {
	ID            uuid.UUID       `json:"id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	BOSAAccountID uuid.UUID       `json:"bosa_account_id"`
	LoanID        uuid.UUID       `json:"loan_id"`
	MemberID      uuid.UUID       `json:"member_id"`
	Amount        decimal.Decimal `json:"amount"`
	Status        string          `json:"status"`
}

var ErrBOSALienAlreadyExists = errors.New("bosa_liens: a lien already exists for this loan")

// PlaceTx inserts an 'active' lien for a loan. Idempotent on
// UNIQUE(loan_id) — re-running returns nil + sets out to the existing
// row. The amount captured is the BOSA balance AT disburse time, so
// future BOSA deposits + the loan's repayments don't move the lien
// figure unless policy='proportional' (deferred).
func (s *BOSALienStore) PlaceTx(
	ctx context.Context, tx pgx.Tx,
	bosaAcctID, loanID, memberID, placedBy uuid.UUID,
	amount decimal.Decimal,
) (*BOSALien, error) {
	var l BOSALien
	err := tx.QueryRow(ctx, `
		INSERT INTO bosa_liens (
		  tenant_id, bosa_account_id, loan_id, member_id, amount, placed_by
		) VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		ON CONFLICT (loan_id) DO NOTHING
		RETURNING id, tenant_id, bosa_account_id, loan_id, member_id, amount, status
	`, bosaAcctID, loanID, memberID, amount, placedBy).Scan(
		&l.ID, &l.TenantID, &l.BOSAAccountID, &l.LoanID, &l.MemberID, &l.Amount, &l.Status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already exists — return the existing row.
		existing, ferr := s.ByLoanTx(ctx, tx, loanID)
		if ferr != nil {
			return nil, ferr
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("place bosa lien: %w", err)
	}
	return &l, nil
}

// ReleaseTx flips the lien to status='released' for a given loan.
// Idempotent — returns no error if the lien doesn't exist or is
// already released. Called from settle / write-off paths.
func (s *BOSALienStore) ReleaseTx(
	ctx context.Context, tx pgx.Tx,
	loanID, releasedBy uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE bosa_liens
		   SET status = 'released',
		       released_at = COALESCE(released_at, now()),
		       released_by = COALESCE(released_by, $2)
		 WHERE loan_id = $1
		   AND status IN ('active','partially_released')
	`, loanID, releasedBy)
	return err
}

// ActiveByBOSAAccountTx returns every active or partially_released lien
// against a BOSA account. Used by the BOSA exit handler to refuse with
// a useful message ("blocked by loans L-1, L-2").
func (s *BOSALienStore) ActiveByBOSAAccountTx(
	ctx context.Context, tx pgx.Tx, bosaAcctID uuid.UUID,
) ([]BOSALien, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, bosa_account_id, loan_id, member_id, amount, status
		  FROM bosa_liens
		 WHERE bosa_account_id = $1
		   AND status IN ('active','partially_released')
	`, bosaAcctID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BOSALien
	for rows.Next() {
		var l BOSALien
		if err := rows.Scan(&l.ID, &l.TenantID, &l.BOSAAccountID, &l.LoanID, &l.MemberID, &l.Amount, &l.Status); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ByLoanTx fetches the lien for a loan (or nil if none).
func (s *BOSALienStore) ByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) (*BOSALien, error) {
	var l BOSALien
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, bosa_account_id, loan_id, member_id, amount, status
		  FROM bosa_liens
		 WHERE loan_id = $1
	`, loanID).Scan(&l.ID, &l.TenantID, &l.BOSAAccountID, &l.LoanID, &l.MemberID, &l.Amount, &l.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// FindBOSAAccountForMemberTx returns the FIRST BOSA-segment deposit
// account for the member. Most SACCOs run a single BOSA account per
// member; we don't try to be cleverer than that. Returns (nil, nil)
// if the member has no BOSA account (rare but possible for new
// members applying via the disbursement flow without prior savings).
func (s *BOSALienStore) FindBOSAAccountForMemberTx(
	ctx context.Context, tx pgx.Tx, memberID uuid.UUID,
) (*BOSAAccountSummary, error) {
	var sum BOSAAccountSummary
	err := tx.QueryRow(ctx, `
		SELECT da.id, da.current_balance::numeric
		  FROM deposit_accounts da
		  JOIN deposit_products dp ON dp.id = da.product_id
		  JOIN counterparty_directory cd ON cd.counterparty_id = da.counterparty_id
		 WHERE cd.member_id = $1
		   AND dp.segment::text = 'bosa'
		   AND da.status = 'active'
		 ORDER BY da.opened_at
		 LIMIT 1
	`, memberID).Scan(&sum.AccountID, &sum.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

type BOSAAccountSummary struct {
	AccountID uuid.UUID
	Balance   decimal.Decimal
}
