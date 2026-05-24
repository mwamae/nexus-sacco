// Integration test for the fees CTE added to ListMemberLedgerTx —
// fee + welfare receipt lines must surface as standalone ledger
// rows for the member's Accounts tab.
//
// Property under test:
//   • Two posted fee lines on a single receipt both appear in the
//     ledger result with source='fee', the right txn_type ('fee'),
//     the fee_code as account_label, debit-only.
//   • Pagination cursor still works when a fee row is the last row
//     of the returned page (uses rl.posted_at ordering).
//   • A voided fee line does NOT appear.
//
// Runs against the live DATABASE_URL; skipped when unset. All writes
// happen inside a transaction that is rolled back at the end — no
// committed fixtures land in the dev DB.

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

func TestListMemberLedger_FeeRowsAppear(t *testing.T) {
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

	// Pick tujenge + any counterparty.
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

	// Provision the mpesa virtual till + build a 2-line fee receipt.
	vts := NewVirtualTillStore(pool)
	vt, err := vts.EnsureForChannelTx(ctx, tx, tenantID, domain.RCMpesa)
	if err != nil {
		t.Fatalf("ensure virtual till: %v", err)
	}
	rs := NewReceiptStore(pool)
	uniq := time.Now().UnixNano()
	ref := fmt.Sprintf("MPS-FEELED-%d", uniq)
	stmtFee := decimal.NewFromInt(100)
	regFee := decimal.NewFromInt(1000)
	total := stmtFee.Add(regFee)
	stmtCode := "STMT_FEE"
	regCode := "MEMBERSHIP_REG_FEE"

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
			{LineNo: 1, Kind: domain.LineFee, Amount: stmtFee, FeeCode: &stmtCode},
			{LineNo: 2, Kind: domain.LineFee, Amount: regFee, FeeCode: &regCode},
		},
	})
	if err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	if len(receipt.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(receipt.Lines))
	}

	// Mark both fee lines posted with synthetic txn ids. This mirrors
	// what collection_desk.go::postFeeLineTx does — fee lines don't go
	// through approvals; they post directly with a uuid that the GL
	// callback uses to dedup. posted_at is what the ledger query keys
	// on, so we backdate slightly so the cursor test below has space.
	line1ID := receipt.Lines[0].ID
	line2ID := receipt.Lines[1].ID
	postedAt1 := time.Now().UTC().Add(-2 * time.Minute)
	postedAt2 := time.Now().UTC().Add(-1 * time.Minute) // later → first in DESC ordering
	if err := rs.MarkLinePostedTx(ctx, tx, line1ID, uuid.New()); err != nil {
		t.Fatalf("mark line 1 posted: %v", err)
	}
	if err := rs.MarkLinePostedTx(ctx, tx, line2ID, uuid.New()); err != nil {
		t.Fatalf("mark line 2 posted: %v", err)
	}
	// Override posted_at so we control DESC ordering for the cursor
	// test. MarkLinePostedTx sets it to now() — we tweak after so the
	// two rows have a known gap.
	if _, err := tx.Exec(ctx, `UPDATE receipt_lines SET posted_at = $2 WHERE id = $1`, line1ID, postedAt1); err != nil {
		t.Fatalf("backdate line 1: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE receipt_lines SET posted_at = $2 WHERE id = $1`, line2ID, postedAt2); err != nil {
		t.Fatalf("backdate line 2: %v", err)
	}

	ls := NewMemberLedgerStore(pool)

	// ─── 1. Both fee rows show up in a single page ────────────
	page, err := ls.ListMemberLedgerTx(ctx, tx, cpID, time.Time{}, 200)
	if err != nil {
		t.Fatalf("list ledger: %v", err)
	}

	feeRows := filterFee(page.Rows)
	wantFeeIDs := map[uuid.UUID]struct {
		amount   decimal.Decimal
		code     string
	}{
		line1ID: {stmtFee, stmtCode},
		line2ID: {regFee, regCode},
	}
	if len(feeRows) < 2 {
		t.Fatalf("expected at least 2 fee rows in ledger; got %d (total rows=%d)", len(feeRows), len(page.Rows))
	}
	for _, want := range []uuid.UUID{line1ID, line2ID} {
		row := findRowByTxnID(feeRows, want)
		if row == nil {
			t.Errorf("fee row missing: txn_id=%s", want)
			continue
		}
		w := wantFeeIDs[want]
		if row.Source != LedgerSourceFee {
			t.Errorf("row %s: source = %s, want fee", want, row.Source)
		}
		if row.TxnType != "fee" {
			t.Errorf("row %s: txn_type = %s, want fee", want, row.TxnType)
		}
		if row.AccountLabel != w.code {
			t.Errorf("row %s: account_label = %s, want %s", want, row.AccountLabel, w.code)
		}
		if !row.Debit.Equal(w.amount) {
			t.Errorf("row %s: debit = %s, want %s", want, row.Debit.String(), w.amount.String())
		}
		if !row.Credit.IsZero() {
			t.Errorf("row %s: credit should be 0, got %s", want, row.Credit.String())
		}
		if row.ReceiptID == nil || *row.ReceiptID != receipt.ID {
			t.Errorf("row %s: receipt_id = %v, want %s", want, row.ReceiptID, receipt.ID)
		}
		if row.TxnNo != receipt.Serial {
			t.Errorf("row %s: txn_no = %s, want receipt serial %s", want, row.TxnNo, receipt.Serial)
		}
	}

	// ─── 2. Cursor pagination puts a fee row at the page boundary ───
	// Find the cursor position that sits right after line2 (the
	// later/newer of the two fee rows) and assert paginating from
	// before that timestamp returns line1 but NOT line2.
	pageA, err := ls.ListMemberLedgerTx(ctx, tx, cpID, postedAt2.Add(time.Second), 50)
	if err != nil {
		t.Fatalf("list page A: %v", err)
	}
	if rowOf(pageA.Rows, line2ID) == nil {
		t.Errorf("page A should include line 2 (postedAt2 < cursor)")
	}
	// Cursor past line2's posted_at but BEFORE line1's — should
	// exclude line2 + include line1.
	pageB, err := ls.ListMemberLedgerTx(ctx, tx, cpID, postedAt2, 50)
	if err != nil {
		t.Fatalf("list page B: %v", err)
	}
	if rowOf(pageB.Rows, line2ID) != nil {
		t.Errorf("page B should exclude line 2 (cursor = postedAt2; query uses posted_at < cursor)")
	}
	if rowOf(pageB.Rows, line1ID) == nil {
		t.Errorf("page B should include line 1 (postedAt1 < postedAt2 cursor)")
	}

	// ─── 3. Voided fee line is filtered out ───────────────────
	if _, err := tx.Exec(ctx, `
		UPDATE receipt_lines
		SET status = 'voided', voided_at = now()
		WHERE id = $1`, line1ID); err != nil {
		t.Fatalf("void line 1: %v", err)
	}
	pageAfterVoid, err := ls.ListMemberLedgerTx(ctx, tx, cpID, time.Time{}, 200)
	if err != nil {
		t.Fatalf("list after void: %v", err)
	}
	if rowOf(pageAfterVoid.Rows, line1ID) != nil {
		t.Errorf("voided line should not appear in ledger")
	}
	// The other line is still there.
	if rowOf(pageAfterVoid.Rows, line2ID) == nil {
		t.Errorf("non-voided fee line should still appear after voiding the other")
	}
}

func filterFee(rows []LedgerRow) []LedgerRow {
	out := []LedgerRow{}
	for _, r := range rows {
		if r.Source == LedgerSourceFee {
			out = append(out, r)
		}
	}
	return out
}

func findRowByTxnID(rows []LedgerRow, id uuid.UUID) *LedgerRow {
	for i := range rows {
		if rows[i].TxnID == id {
			return &rows[i]
		}
	}
	return nil
}

func rowOf(rows []LedgerRow, id uuid.UUID) *LedgerRow { return findRowByTxnID(rows, id) }

// silence linters if a dep ends up trimmed in a future refactor.
var _ = pgx.ErrNoRows
var _ = pgxpool.Pool{}
