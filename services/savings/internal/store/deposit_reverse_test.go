// Integration test for the Collection Desk line-level reversal
// (Phase G follow-up). Verifies:
//
//   1. ReverseDepositTx posts a matching withdrawal that brings the
//      account balance back to where it started.
//   2. The original deposit_transactions row picks up the
//      reversed_by_txn_id back-link.
//   3. A second ReverseDepositTx call returns ErrAlreadyReversed
//      (idempotency / double-void guard).
//
// Runs against the live DATABASE_URL; skipped without it. All writes
// happen in a transaction that's rolled back at the end.

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

func TestReverseDepositRoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID, cpID, productID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT t.id, c.id, dp.id FROM tenants t
		  JOIN counterparties c ON c.tenant_id = t.id
		  JOIN deposit_products dp ON dp.tenant_id = t.id
		 WHERE t.slug = 'tujenge' LIMIT 1
	`).Scan(&tenantID, &cpID, &productID); err != nil {
		t.Skipf("no tujenge fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	ds := NewDepositStore(pool)
	uniq := time.Now().UnixNano()

	// Open a fresh deposit account so the test doesn't perturb seed data.
	acct, _, err := ds.OpenAccountTx(ctx, tx, OpenInput{
		CounterpartyID: cpID,
		ProductID:      productID,
		OpeningDeposit: decimal.Zero,
		CreatedBy:      uuid.New(),
	}, fmt.Sprintf("DPA-REV-%d", uniq))
	if err != nil {
		t.Fatalf("open account: %v", err)
	}
	if !acct.CurrentBalance.IsZero() {
		t.Fatalf("opened account balance not zero: %s", acct.CurrentBalance.String())
	}

	// Post a 1000 KES mpesa deposit (mirrors a collection-desk line).
	ch := domain.DepChannelMpesa
	ref := fmt.Sprintf("MPS-REV-%d", uniq)
	depTxn, err := ds.PostTxnTx(ctx, tx, PostDepInput{
		Account:     acct,
		TxnType:     domain.TxnDeposit,
		Amount:      decimal.NewFromInt(1000),
		Channel:     &ch,
		ChannelRef:  &ref,
		InitiatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatalf("post deposit: %v", err)
	}
	acctAfter, err := ds.GetAccountTx(ctx, tx, acct.ID)
	if err != nil {
		t.Fatalf("reload acct: %v", err)
	}
	if !acctAfter.CurrentBalance.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("post-deposit balance: want 1000, got %s", acctAfter.CurrentBalance.String())
	}

	// Reverse the deposit; balance must return to zero.
	userID := uuid.New()
	rev, err := ds.ReverseDepositTx(ctx, tx, depTxn.ID, "test reversal", userID)
	if err != nil {
		t.Fatalf("reverse deposit: %v", err)
	}
	if rev.ReversesTxnID == nil || *rev.ReversesTxnID != depTxn.ID {
		t.Errorf("reversal back-link missing: want %s, got %v", depTxn.ID, rev.ReversesTxnID)
	}
	acctReversed, err := ds.GetAccountTx(ctx, tx, acct.ID)
	if err != nil {
		t.Fatalf("reload acct post-reverse: %v", err)
	}
	if !acctReversed.CurrentBalance.IsZero() {
		t.Errorf("post-reverse balance: want 0, got %s", acctReversed.CurrentBalance.String())
	}

	// Original must now carry the forward back-link.
	orig, err := ds.GetTxnTx(ctx, tx, depTxn.ID)
	if err != nil {
		t.Fatalf("reload original: %v", err)
	}
	if orig.ReversedByTxnID == nil || *orig.ReversedByTxnID != rev.ID {
		t.Errorf("original.reversed_by_txn_id not set: want %s, got %v", rev.ID, orig.ReversedByTxnID)
	}

	// Second reverse must be rejected.
	if _, err := ds.ReverseDepositTx(ctx, tx, depTxn.ID, "double reverse", userID); !errors.Is(err, ErrAlreadyReversed) {
		t.Errorf("double-reverse: want ErrAlreadyReversed, got %v", err)
	}
}

// Silence the unused-import linter when one of these deps isn't
// referenced in a trimmed build.
var _ = pgx.ErrNoRows
