// Integration test for the Collection Desk → approvals dispatcher
// hookup (Phase G). Verifies the two contracts that the dispatcher
// relies on:
//
//   1. GetLineByApprovalIDTx — receipt line round-trips correctly
//      from an approval_id (the dispatcher's reverse lookup).
//   2. MarkLinePostedTx + RecomputeStatusForLineTx — header rolls up
//      to 'posted' once every line is terminal, even when one line
//      was declined.
//
// Runs against the live DATABASE_URL (skipped when unset). All writes
// happen inside a rolled-back transaction.

package store

import (
	"context"
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

func TestReceiptApprovalPropagation(t *testing.T) {
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

	// Pick a tenant + a counterparty that exists. Tujenge is the dev
	// fixture across the rest of the test suite.
	var tenantID, cpID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT t.id, c.id FROM tenants t
		  JOIN counterparties c ON c.tenant_id = t.id
		 WHERE t.slug = 'tujenge'
		 LIMIT 1
	`).Scan(&tenantID, &cpID); err != nil {
		t.Skipf("no tujenge tenant with counterparties: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	vts := NewVirtualTillStore(pool)
	rs := NewReceiptStore(pool)

	// Provision the mpesa virtual till.
	vt, err := vts.EnsureForChannelTx(ctx, tx, tenantID, domain.RCMpesa)
	if err != nil {
		t.Fatalf("ensure virtual till: %v", err)
	}

	// Build a 3-line receipt: deposit + share + loan (matches the
	// acceptance criterion shape in the original spec).
	uniq := time.Now().UnixNano()
	ref := fmt.Sprintf("MPS-TEST-%d", uniq)
	depAmt := decimal.NewFromInt(2000)
	shrAmt := decimal.NewFromInt(500)
	lnAmt := decimal.NewFromInt(8500)
	total := depAmt.Add(shrAmt).Add(lnAmt)

	receipt, err := rs.CreateTx(ctx, tx, CreateReceiptInput{
		TenantID:       tenantID,
		CounterpartyID: cpID,
		Channel:        domain.RCMpesa,
		ChannelRef:     &ref,
		ChannelAmount:  total,
		ValueDate:      time.Now().UTC(),
		CashierUserID:  uuid.New(),
		VirtualTillID:  &vt.ID,
		TillCode:       "mpesa",
		Lines: []CreateReceiptLineInput{
			{LineNo: 1, Kind: domain.LineSavingsDeposit, Amount: depAmt, TargetAccountID: ptrUUID(uuid.New())},
			{LineNo: 2, Kind: domain.LineSharePurchase, Amount: shrAmt},
			{LineNo: 3, Kind: domain.LineLoanRepayment, Amount: lnAmt, TargetAccountID: ptrUUID(uuid.New())},
		},
	})
	if err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	if got, want := len(receipt.Lines), 3; got != want {
		t.Fatalf("lines: want %d, got %d", want, got)
	}
	if receipt.Status != domain.ReceiptDraft {
		t.Errorf("status: want draft, got %s", receipt.Status)
	}

	// Synthesise per-line approval ids (the real dispatcher writes
	// these on Queue; we just need a non-nil UUID for the round-trip).
	for i := range receipt.Lines {
		appID := uuid.New()
		if err := rs.AttachApprovalTx(ctx, tx, receipt.Lines[i].ID, appID); err != nil {
			t.Fatalf("attach approval to line %d: %v", i, err)
		}
		// Round-trip: GetLineByApprovalIDTx finds the line we just
		// attached.
		line, err := rs.GetLineByApprovalIDTx(ctx, tx, appID)
		if err != nil {
			t.Fatalf("get line by approval: %v", err)
		}
		if line.ID != receipt.Lines[i].ID {
			t.Errorf("round-trip mismatch on line %d: want %s, got %s", i, receipt.Lines[i].ID, line.ID)
		}
	}

	// Approve line 1 + 2; decline line 3. Header should roll up to
	// 'posted' because every line is terminal.
	if err := rs.MarkLinePostedTx(ctx, tx, receipt.Lines[0].ID, uuid.New()); err != nil {
		t.Fatalf("mark line 0 posted: %v", err)
	}
	// Status should still be draft (line 2 + 3 not terminal).
	r1, err := rs.GetByIDTx(ctx, tx, receipt.ID)
	if err != nil {
		t.Fatalf("get receipt: %v", err)
	}
	if r1.Status != domain.ReceiptDraft {
		t.Errorf("after 1 line posted, header status: want draft, got %s", r1.Status)
	}
	if err := rs.MarkLinePostedTx(ctx, tx, receipt.Lines[1].ID, uuid.New()); err != nil {
		t.Fatalf("mark line 1 posted: %v", err)
	}
	if err := rs.MarkLineDeclinedTx(ctx, tx, receipt.Lines[2].ID); err != nil {
		t.Fatalf("mark line 2 declined: %v", err)
	}

	r2, err := rs.GetByIDTx(ctx, tx, receipt.ID)
	if err != nil {
		t.Fatalf("get receipt after all terminal: %v", err)
	}
	if r2.Status != domain.ReceiptPosted {
		t.Errorf("after every line terminal, header status: want posted, got %s", r2.Status)
	}
	if r2.PostedAt == nil {
		t.Errorf("posted_at not stamped")
	}
	// Per-line status sanity.
	if r2.Lines[0].Status != domain.LinePosted {
		t.Errorf("line 0 status: want posted, got %s", r2.Lines[0].Status)
	}
	if r2.Lines[2].Status != domain.LineDeclined {
		t.Errorf("line 2 status: want declined, got %s", r2.Lines[2].Status)
	}
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

// silence linters when one or more deps aren't referenced in trimmed
// builds.
var _ = pgx.ErrNoRows
