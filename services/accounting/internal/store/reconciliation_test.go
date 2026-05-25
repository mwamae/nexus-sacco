// Acceptance test for the subledger reconciliation report.
//
// The spec's "artificially break a deposit by deleting the JE row"
// scenario, modeled as: pick an account that's currently in 'ok'
// (GL == subledger), delete one of its journal_lines, re-run the
// report → assert status flips to 'error' with the expected delta.
// Re-insert the line → status flips back to 'ok'.
//
// Runs inside a rolled-back tx so the surgery never persists.

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestReconciliation_BreakAndRestoreFlipsStatus(t *testing.T) {
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

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // rolls back everything below

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	rs := NewReconciliationStore(pool)
	asOf := time.Now().UTC()

	// ─── Baseline: pick an account currently in 'ok' that has at
	// least one journal_line we can delete + recreate.
	rep, err := rs.ReconciliationTx(ctx, tx, asOf)
	if err != nil {
		t.Fatalf("baseline reconciliation: %v", err)
	}
	var target *ReconciliationRow
	for i := range rep.Rows {
		if rep.Rows[i].Status == "ok" && rep.Rows[i].GLBalance.GreaterThan(decimal.NewFromInt(100)) {
			target = &rep.Rows[i]
			break
		}
	}
	if target == nil {
		t.Skip("no 'ok' account with material GL balance available to break — tenant likely empty")
	}
	t.Logf("target account: %s (%s) — GL=%s subledger=%s",
		target.Code, target.Name,
		target.GLBalance.StringFixed(2), target.SubledgerBalance.StringFixed(2))

	// ─── Find a journal_line on this account big enough to make the
	// delta exceed the 0.1% threshold. Use the largest one we can
	// find on the target account.
	var lineID, entryID, accountID, lineTenantID uuid.UUID
	var lineNo int
	var lineDebit, lineCredit decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT l.id, l.tenant_id, l.entry_id, l.line_no, l.account_id, l.debit, l.credit
		  FROM journal_lines l
		  JOIN journal_entries je ON je.id = l.entry_id
		  JOIN chart_of_accounts a ON a.id = l.account_id
		 WHERE a.code = $1
		   AND je.status = 'posted'
		 ORDER BY GREATEST(l.debit, l.credit) DESC
		 LIMIT 1
	`, target.Code).Scan(&lineID, &lineTenantID, &entryID, &lineNo, &accountID, &lineDebit, &lineCredit); err != nil {
		t.Fatalf("pick journal_line on %s: %v", target.Code, err)
	}
	lineAmount := lineDebit
	if lineDebit.IsZero() {
		lineAmount = lineCredit
	}
	t.Logf("breaking journal_line %s (amount %s)", lineID, lineAmount.StringFixed(2))

	// ─── Break: delete the line. Re-run; assert status flipped to
	// error (or warn if the amount happens to be small relative to GL,
	// but for "material" we picked > 100 so it should be error on any
	// realistic tenant).
	if _, err := tx.Exec(ctx, `DELETE FROM journal_lines WHERE id = $1`, lineID); err != nil {
		t.Fatalf("delete line: %v", err)
	}
	rep2, err := rs.ReconciliationTx(ctx, tx, asOf)
	if err != nil {
		t.Fatalf("post-break reconciliation: %v", err)
	}
	var broken *ReconciliationRow
	for i := range rep2.Rows {
		if rep2.Rows[i].Code == target.Code {
			broken = &rep2.Rows[i]
			break
		}
	}
	if broken == nil {
		t.Fatalf("account %s missing from post-break report", target.Code)
	}
	if broken.Status == "ok" {
		t.Errorf("after deleting journal_line %s amount=%s on %s: want status != ok, got ok (delta=%s)",
			lineID, lineAmount.StringFixed(2), target.Code, broken.Delta.StringFixed(2))
	}
	// Delta must equal (or be very close to) the deleted line amount.
	expectedDelta := lineAmount.Round(2)
	gotDelta := broken.Delta.Round(2)
	if !expectedDelta.Equal(gotDelta) {
		t.Errorf("post-break delta: want %s, got %s",
			expectedDelta.StringFixed(2), gotDelta.StringFixed(2))
	}
	t.Logf("post-break status=%s delta=%s", broken.Status, broken.Delta.StringFixed(2))

	// ─── Restore: re-insert the line. Re-run; assert ok.
	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_lines (id, tenant_id, entry_id, line_no, account_id, debit, credit)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, lineID, lineTenantID, entryID, lineNo, accountID, lineDebit, lineCredit); err != nil {
		t.Fatalf("re-insert line: %v", err)
	}
	rep3, err := rs.ReconciliationTx(ctx, tx, asOf)
	if err != nil {
		t.Fatalf("post-restore reconciliation: %v", err)
	}
	var restored *ReconciliationRow
	for i := range rep3.Rows {
		if rep3.Rows[i].Code == target.Code {
			restored = &rep3.Rows[i]
			break
		}
	}
	if restored == nil {
		t.Fatalf("account %s missing from post-restore report", target.Code)
	}
	if restored.Status != "ok" {
		t.Errorf("after restoring journal_line on %s: want status=ok, got %s (delta=%s)",
			target.Code, restored.Status, restored.Delta.StringFixed(2))
	}
}

// Silence unused-import warning when the test is skipped at the top.
var _ = fmt.Sprintf
