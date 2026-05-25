// PR 4 (BOSA / FOSA SASRA split) — integration test that pins the
// new ratio denominators + the deposit-summary field shapes.
//
// Skipped when DATABASE_URL is unset, matching the convention in
// the savings + handler test suites. The test is shape-oriented
// rather than value-oriented: it asserts the *relationships*
// (denominator == shareCap + BOSA) so it tracks live tenant data
// without becoming a brittle snapshot.

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestSASRAReturnBOSADenominators(t *testing.T) {
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
		t.Skipf("no tujenge tenant: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	// Scope into the tenant so RLS picks up the per-tenant CoA.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	rs := NewReportStore(pool)
	rep, err := rs.SASRAReturnTx(ctx, tx, time.Now())
	if err != nil {
		t.Fatalf("SASRAReturnTx: %v", err)
	}

	// ─── Field shape ───
	// Total must equal FOSA + BOSA; the legacy `Total` field is
	// kept for back-compat but should always be the sum.
	want := rep.Deposits.MemberSavingsFOSA.Add(rep.Deposits.MemberDepositsBOSA)
	if !rep.Deposits.Total.Equal(want) {
		t.Errorf("Deposits.Total = %s, want FOSA+BOSA = %s",
			rep.Deposits.Total.String(), want.String())
	}

	// ─── Core capital ÷ total deposits (PR 4 denominator change) ───
	// The ratio's denominator must now be share_capital + BOSA, NOT
	// the combined deposits.Total. Find it by label rather than
	// position so re-ordering the slice doesn't break the test.
	r := findRatio(rep.Ratios, "Core capital to total deposits")
	if r == nil {
		t.Fatal("ratio 'Core capital to total deposits' not present")
	}
	expectDen := rep.Capital.ShareCapital.Add(rep.Deposits.MemberDepositsBOSA)
	if !r.Denominator.Equal(expectDen) {
		t.Errorf("core-capital-to-deposits denominator = %s, want share_capital + BOSA = %s",
			r.Denominator.String(), expectDen.String())
	}

	// ─── Liquidity ratio ───
	// Denominator = FOSA + payables. We don't assert the exact
	// payable codes here (test would couple to GL config) — just
	// that the denominator is at least the FOSA total. If it ever
	// drops below FOSA, the subtype-driven scan from PR 3 got
	// broken.
	r = findRatio(rep.Ratios, "Liquidity ratio")
	if r == nil {
		t.Fatal("ratio 'Liquidity ratio' not present")
	}
	if r.Denominator.LessThan(rep.Deposits.MemberSavingsFOSA) {
		t.Errorf("liquidity denominator = %s, must be ≥ FOSA total %s",
			r.Denominator.String(), rep.Deposits.MemberSavingsFOSA.String())
	}

	// ─── BOSA-empty warning ───
	// Only assert the warning *can* fire — we don't fail if it
	// doesn't, because a tenant with no members or no loans
	// legitimately won't see it.
	if rep.Deposits.MemberDepositsBOSA.IsZero() {
		var memberCount, activeLoanCount int
		if err := tx.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM members),
			  (SELECT count(*) FROM loans WHERE status IN ('active','in_arrears','restructured'))
		`).Scan(&memberCount, &activeLoanCount); err == nil && memberCount > 0 && activeLoanCount > 0 {
			if !hasWarning(rep.Warnings, "bosa_bucket_empty") {
				t.Errorf("expected warning code 'bosa_bucket_empty' on tenant with members=%d, active_loans=%d, BOSA=0; warnings=%+v",
					memberCount, activeLoanCount, rep.Warnings)
			}
		}
	}
}

func findRatio(rs []SASRARatio, label string) *SASRARatio {
	for i := range rs {
		if rs[i].Label == label {
			return &rs[i]
		}
	}
	return nil
}

func hasWarning(ws []SASRAWarning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestSASRAReturnRespectsConfiguredFiscalYearStart asserts that the
// income-statement window honours tenant_operations.fy_start_month /
// fy_start_day instead of hard-coding January 1.
//
// Runs entirely inside a rolled-back tx so the temporary UPDATE on
// tenant_operations never persists — the existing BOSA test uses the
// same pattern so per-test mutations don't leak across runs.
func TestSASRAReturnRespectsConfiguredFiscalYearStart(t *testing.T) {
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
		t.Skipf("no tujenge tenant: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	// Flip the tenant onto a July-start fiscal year for the duration of
	// this tx. Rollback in the defer above restores the original config.
	if _, err := tx.Exec(ctx, `
		UPDATE tenant_operations SET fy_start_month=7, fy_start_day=1
	`); err != nil {
		t.Fatalf("flip fy_start: %v", err)
	}

	// Fixed asOf so the assertion is deterministic. 2026-05-25 falls
	// BEFORE July 1 2026, so the current FY started on July 1 2025.
	asOf := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	wantFYStart := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	rs := NewReportStore(pool)
	rep, err := rs.SASRAReturnTx(ctx, tx, asOf)
	if err != nil {
		t.Fatalf("SASRAReturnTx: %v", err)
	}
	if !rep.FiscalYearStart.Equal(wantFYStart) {
		t.Errorf("SASRAReturn.FiscalYearStart = %s, want %s",
			rep.FiscalYearStart.Format("2006-01-02"),
			wantFYStart.Format("2006-01-02"))
	}
	if !rep.IncomeStatement.FromDate.Equal(wantFYStart) {
		t.Errorf("IncomeStatement.FromDate = %s, want %s (FY start, not Jan 1)",
			rep.IncomeStatement.FromDate.Format("2006-01-02"),
			wantFYStart.Format("2006-01-02"))
	}
	// The window's right edge should match asOf — defensive check that
	// only the left edge moved.
	if !rep.IncomeStatement.ToDate.Equal(asOf) {
		t.Errorf("IncomeStatement.ToDate = %s, want %s",
			rep.IncomeStatement.ToDate.Format("2006-01-02"),
			asOf.Format("2006-01-02"))
	}
}

// Silence unused-import warning when the test is skipped at the top.
var _ = pgx.ErrNoRows
var _ = decimal.Zero
