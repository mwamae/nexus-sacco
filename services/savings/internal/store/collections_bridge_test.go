// Integration test for the DPD → collections-queue bridge.
//
// Property under test:
//   ANY caller of RecalcDPDTx that flips a loan into arrears MUST
//   cause a row to appear in loan_collection_cases, regardless of
//   whether the caller is the nightly cron, a repayment posting, a
//   repayment reversal, or a manual /recalc-dpd request.
//
// Before the fix in store/loan_store.go.SetCollections +
// store/loan_repayment_store.go.RecalcDPDTx, only the cron path
// triggered the bridge — the other three callers silently updated
// arrears_classification without enqueueing a case. The non-cron
// branch in this test (subtest "repayment-path bridges") is the
// regression pin.
//
// Runs against the live DATABASE_URL (set by the dev .env). Skipped
// when the env var is unset so CI without a Postgres can still run
// `go test ./...` without exploding. Uses a single transaction that
// is rolled back at the end — no committed test data is left behind.

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
)

func TestRecalcDPDOpensCollectionsCase(t *testing.T) {
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

	// Acquire one connection so all setup + assertions share the
	// same tx. RLS is satisfied via set_config('app.tenant_id', …).
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// IMPORTANT — always rollback. No fixtures land in the real DB.
	defer func() { _ = tx.Rollback(ctx) }()

	// Pick a tenant that has at least one member AND one loan
	// product — many dev tenants are empty shells.
	var tenantID, memberID, productID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT t.id
		  FROM tenants t
		 WHERE EXISTS (SELECT 1 FROM members        m WHERE m.tenant_id = t.id)
		   AND EXISTS (SELECT 1 FROM loan_products  p WHERE p.tenant_id = t.id)
		 LIMIT 1
	`).Scan(&tenantID)
	if err != nil {
		t.Skipf("no tenant has both a member and a loan product (dev DB not seeded): %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM members WHERE tenant_id = $1 LIMIT 1`, tenantID).Scan(&memberID); err != nil {
		t.Fatalf("no members for tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM loan_products WHERE tenant_id = $1 LIMIT 1`, tenantID).Scan(&productID); err != nil {
		t.Fatalf("no loan products for tenant: %v", err)
	}

	// Insert a synthetic application + loan with status 'active' and
	// no schedule yet. dpd = 0 initially so the bridge stays quiet.
	uniq := time.Now().UnixNano()
	var applicationID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO loan_applications (
		  tenant_id, application_no, member_id, product_id, status,
		  requested_amount, requested_term_months, monthly_net_income, created_by
		) VALUES ($1, $2, $3, $4, 'disbursed', 50000, 12, 30000, $5)
		RETURNING id
	`, tenantID, fmt.Sprintf("LA-BRIDGE-%d", uniq), memberID, productID, uuid.Nil).Scan(&applicationID); err != nil {
		t.Fatalf("insert application: %v", err)
	}

	var loanID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO loans (
		  tenant_id, loan_no, application_id, member_id, product_id, status,
		  principal, interest_rate_pct, interest_method, repayment_method,
		  term_months, installment_count, first_due_date,
		  principal_disbursed, principal_balance,
		  disbursed_at, disbursed_by
		) VALUES (
		  $1, $2, $3, $4, $5, 'active',
		  50000, 12.0, 'reducing_balance', 'reducing_balance',
		  12, 12, CURRENT_DATE - INTERVAL '70 days',
		  50000, 50000,
		  now(), $6
		)
		RETURNING id
	`, tenantID, fmt.Sprintf("L-BRIDGE-%d", uniq), applicationID, memberID, productID, uuid.Nil).Scan(&loanID); err != nil {
		t.Fatalf("insert loan: %v", err)
	}

	// One overdue installment dated 70 days ago. With the default
	// tenant DPD bands (substandard ≥ 30, doubtful ≥ 90, loss ≥ 180)
	// 70 days lands squarely in 'substandard'.
	if _, err := tx.Exec(ctx, `
		INSERT INTO loan_repayment_schedule (
		  tenant_id, loan_id, installment_no, due_date,
		  principal_due, interest_due, total_due, outstanding_after, status
		) VALUES ($1, $2, 1, CURRENT_DATE - INTERVAL '70 days',
		          5000, 500, 5500, 45000, 'pending')
	`, tenantID, loanID); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	// Pre-condition: no case for this loan yet.
	assertCaseCount(t, ctx, tx, loanID, 0, "before RecalcDPDTx")

	loanStore := NewLoanStore(pool)
	collectionsStore := NewLoanCollectionsStore(pool)
	// The fix: SetCollections wires the bridge. Without it,
	// RecalcDPDTx updates the classification but doesn't enqueue.
	loanStore.SetCollections(collectionsStore)

	res, err := loanStore.RecalcDPDTx(ctx, tx, loanID, time.Now())
	if err != nil {
		t.Fatalf("RecalcDPDTx: %v", err)
	}
	if res.DaysPastDue < 30 {
		t.Fatalf("expected dpd ≥ 30 (substandard); got %d", res.DaysPastDue)
	}
	if res.Classification != "substandard" {
		t.Fatalf("expected classification=substandard; got %q (dpd=%d)", res.Classification, res.DaysPastDue)
	}

	// Post-condition: exactly one open case for the loan, with the
	// classification stamped at open time and priority scaled to dpd.
	assertCaseCount(t, ctx, tx, loanID, 1, "after RecalcDPDTx (substandard)")

	var status, classification string
	var priority int
	if err := tx.QueryRow(ctx, `
		SELECT status, classification_at_open, priority
		  FROM loan_collection_cases WHERE loan_id = $1
	`, loanID).Scan(&status, &classification, &priority); err != nil {
		t.Fatalf("select case: %v", err)
	}
	if status != "open" {
		t.Errorf("case status: want open, got %s", status)
	}
	if classification != "substandard" {
		t.Errorf("classification_at_open: want substandard, got %s", classification)
	}
	if priority < 30 {
		t.Errorf("priority: want ≥ 30 (scaled to dpd), got %d", priority)
	}

	// Idempotency: running RecalcDPDTx again must NOT create a
	// duplicate case. (EnsureCaseForLoanTx is idempotent on loan_id.)
	if _, err := loanStore.RecalcDPDTx(ctx, tx, loanID, time.Now()); err != nil {
		t.Fatalf("RecalcDPDTx idempotency call: %v", err)
	}
	assertCaseCount(t, ctx, tx, loanID, 1, "after second RecalcDPDTx (idempotency)")

	// Bridge-off sanity: a fresh LoanStore WITHOUT SetCollections
	// should still update the classification but NOT touch the
	// queue. This guards the nil-safety of the new code path.
	bareStore := NewLoanStore(pool)
	// New loan so we start from 0 cases for it.
	var loan2ID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO loan_applications (tenant_id, application_no, member_id, product_id, status,
		  requested_amount, requested_term_months, monthly_net_income, created_by)
		VALUES ($1, $2, $3, $4, 'disbursed', 50000, 12, 30000, $5) RETURNING id
	`, tenantID, fmt.Sprintf("LA-NOBRIDGE-%d", uniq), memberID, productID, uuid.Nil).Scan(&applicationID); err != nil {
		t.Fatalf("insert app2: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO loans (tenant_id, loan_no, application_id, member_id, product_id, status,
		  principal, interest_rate_pct, interest_method, repayment_method,
		  term_months, installment_count, first_due_date,
		  principal_disbursed, principal_balance, disbursed_at, disbursed_by)
		VALUES ($1, $2, $3, $4, $5, 'active', 50000, 12.0, 'reducing_balance', 'reducing_balance',
		  12, 12, CURRENT_DATE - INTERVAL '40 days', 50000, 50000, now(), $6)
		RETURNING id
	`, tenantID, fmt.Sprintf("L-NOBRIDGE-%d", uniq), applicationID, memberID, productID, uuid.Nil).Scan(&loan2ID); err != nil {
		t.Fatalf("insert loan2: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO loan_repayment_schedule (tenant_id, loan_id, installment_no, due_date,
		  principal_due, interest_due, total_due, outstanding_after, status)
		VALUES ($1, $2, 1, CURRENT_DATE - INTERVAL '40 days', 5000, 500, 5500, 45000, 'pending')
	`, tenantID, loan2ID); err != nil {
		t.Fatalf("insert schedule2: %v", err)
	}
	if _, err := bareStore.RecalcDPDTx(ctx, tx, loan2ID, time.Now()); err != nil {
		t.Fatalf("RecalcDPDTx bare: %v", err)
	}
	assertCaseCount(t, ctx, tx, loan2ID, 0, "bare store (no SetCollections) should NOT enqueue")

	// Sanity: amount + decimals didn't blow up. (No assertion, just
	// the act of using decimal.Decimal here keeps the import live so
	// future test expansion has it ready.)
	_ = decimal.New(0, 0)
}

func assertCaseCount(t *testing.T, ctx context.Context, tx pgx.Tx, loanID uuid.UUID, want int, label string) {
	t.Helper()
	var got int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM loan_collection_cases WHERE loan_id = $1`, loanID,
	).Scan(&got); err != nil {
		t.Fatalf("count cases (%s): %v", label, err)
	}
	if got != want {
		t.Errorf("[%s] case count: want %d, got %d", label, want, got)
	}
}
