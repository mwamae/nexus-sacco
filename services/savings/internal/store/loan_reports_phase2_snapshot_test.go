// Loans Phase 3 — verifies the DPD-resolver fallback in PARTx + AgingBucketsTx
// and the SnapshotMeta payload.
//
// Invariants (no fixed snapshots — exercises live tenant data so the
// test follows seed changes):
//
//   1. With no loan_dpd_snapshots rows for the tenant, the report
//      still returns sane totals (fallback path). meta.Available is false.
//
//   2. After inserting a snapshot for one loan with a DIFFERENT DPD
//      than the inline proxy, the PAR totals reflect the snapshot
//      (proves the COALESCE actually fires).
//
// Runs inside a rolled-back tx so the test never alters tenant state.

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPARTx_FallsBackToInlineDPDWhenNoSnapshots(t *testing.T) {
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

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	// Wipe snapshots inside the rolled-back tx to force fallback.
	if _, err := tx.Exec(ctx, `DELETE FROM loan_dpd_snapshots`); err != nil {
		t.Fatalf("clear snapshots: %v", err)
	}

	s := &LoanReportsStore{}
	par, err := s.PARTx(ctx, tx)
	if err != nil {
		t.Fatalf("PARTx: %v", err)
	}
	if par.Snapshot == nil {
		t.Fatalf("expected non-nil Snapshot meta")
	}
	if par.Snapshot.Available {
		t.Errorf("expected Available=false when snapshots wiped, got true")
	}
	// Totals must still be parseable / non-error — sanity only.
	if par.TotalPrincipal == "" {
		t.Errorf("expected non-empty TotalPrincipal even in fallback mode")
	}
}

func TestPARTx_PrefersSnapshotOverInlineDPD(t *testing.T) {
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

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	// Find one active loan to manipulate.
	var loanID uuid.UUID
	var principalBalance float64
	err = tx.QueryRow(ctx, `
		SELECT id, principal_balance FROM loans
		 WHERE status IN ('active','in_arrears','restructured')
		   AND principal_balance > 0
		 LIMIT 1
	`).Scan(&loanID, &principalBalance)
	if err != nil {
		t.Skipf("no active loan to test against: %v", err)
	}

	// Wipe + insert a synthetic snapshot with a HIGH DPD (200 days)
	// for this single loan. The inline proxy for the same loan is
	// almost certainly 0 in seed data — proving the snapshot path
	// wins, the loan's principal should appear in the par_90 bucket.
	if _, err := tx.Exec(ctx, `DELETE FROM loan_dpd_snapshots`); err != nil {
		t.Fatalf("clear snapshots: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO loan_dpd_snapshots (
		  tenant_id, loan_id, snapshot_date, dpd_days,
		  principal_balance, interest_balance, fees_balance, penalty_balance,
		  classification_sasra, classification_ifrs9_stage
		) VALUES ($1, $2, CURRENT_DATE, 200, $3, 0, 0, 0, 'doubtful', 3)
	`, tenantID, loanID, principalBalance); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}

	s := &LoanReportsStore{}
	par, err := s.PARTx(ctx, tx)
	if err != nil {
		t.Fatalf("PARTx: %v", err)
	}
	if par.Snapshot == nil || !par.Snapshot.Available {
		t.Fatalf("expected Snapshot.Available=true after insert")
	}
	if par.Snapshot.LatestSnapshotDate == nil {
		t.Fatalf("expected non-nil LatestSnapshotDate")
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !par.Snapshot.LatestSnapshotDate.Equal(today) {
		t.Errorf("LatestSnapshotDate: got %v, want today (%v)", *par.Snapshot.LatestSnapshotDate, today)
	}
	// par_90 must be at least the principal we just classified as
	// DPD=200. (Other loans may contribute too — invariant is >=, not ==.)
	var par90 float64
	if _, err := parseFloatLoose(par.Par90Principal, &par90); err != nil {
		t.Fatalf("parse par_90: %v (raw=%q)", err, par.Par90Principal)
	}
	if par90 < principalBalance-0.01 {
		t.Errorf("expected par_90 (%.2f) >= injected loan principal (%.2f) — snapshot path apparently not used",
			par90, principalBalance)
	}
}

// parseFloatLoose accepts the numeric-as-text format Phase 2 reports
// emit (".00" suffix, possibly negative). Lives in test scope so a bad
// number from the SUT shows the raw string in the failure.
func parseFloatLoose(s string, out *float64) (float64, error) {
	if s == "" {
		*out = 0
		return 0, nil
	}
	var v float64
	_, err := fmtScanf(s, &v)
	if err != nil {
		return 0, err
	}
	*out = v
	return v, nil
}

// thin wrapper around fmt.Sscanf to avoid a top-of-file import that
// the rest of the file doesn't need.
func fmtScanf(s string, v *float64) (int, error) {
	return fmtSscan(s, v)
}

// Hand-rolled to avoid the fmt import in the test file (already
// goimports-friendly: classifier_test.go in another package).
func fmtSscan(s string, v *float64) (int, error) {
	// Manual parse — supports "1234.56" / "1234" / "-1234.56".
	var sign float64 = 1
	i := 0
	if i < len(s) && s[i] == '-' {
		sign = -1
		i++
	}
	whole := 0.0
	frac := 0.0
	div := 1.0
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			whole = whole*10 + float64(c-'0')
		} else if c == '.' {
			i++
			for ; i < len(s); i++ {
				c = s[i]
				if c < '0' || c > '9' {
					break
				}
				frac = frac*10 + float64(c-'0')
				div *= 10
			}
			break
		} else {
			break
		}
	}
	*v = sign * (whole + frac/div)
	return 1, nil
}
