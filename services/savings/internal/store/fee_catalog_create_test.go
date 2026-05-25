// PR fee-coa — exercises the new gl_credit_code existence guard on
// FeeCatalogStore.CreateTx. Two cases:
//
//   1. A code that exists in chart_of_accounts (4080 — Registration
//      Fee Income, seeded by accounting 0008) inserts cleanly.
//   2. A code that doesn't exist (9999) returns ErrUnknownGLCode.
//
// Both run inside a single rolled-back transaction so we never
// pollute the live tenant. Skipped when DATABASE_URL is unset,
// matching every other store-level integration test.

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
)

func TestFeeCatalogCreate_GuardsAgainstUnknownGLCode(t *testing.T) {
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
	if err := pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`,
	).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge tenant: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // always roll back; this test never commits

	// RLS scope.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`,
		tenantID.String(),
	); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	s := NewFeeCatalogStore(pool)
	// Per-run unique codes so re-running the test doesn't trip the
	// (tenant, code) UNIQUE constraint inside the same transaction
	// window.
	uniq := fmt.Sprintf("fee_coa_test_%d", time.Now().UnixNano())

	// ─── 1. Known-good GL code (4080) inserts cleanly. ─────────────
	good, err := s.CreateTx(ctx, tx, tenantID, CreateFeeCatalogInput{
		Code:           uniq + "_ok",
		Label:          "Fee-coa test (good)",
		AmountDefault:  decimal.NewFromInt(100),
		AmountEditable: false,
		GLCreditCode:   "4080",
		SortOrder:      999,
	})
	if err != nil {
		t.Fatalf("create with known GL code: %v", err)
	}
	if good == nil || good.GLCreditCode != "4080" {
		t.Errorf("returned entry: want gl_credit_code=4080, got %+v", good)
	}

	// ─── 2. Unknown GL code returns ErrUnknownGLCode. ──────────────
	bad, err := s.CreateTx(ctx, tx, tenantID, CreateFeeCatalogInput{
		Code:           uniq + "_bad",
		Label:          "Fee-coa test (bad)",
		AmountDefault:  decimal.NewFromInt(50),
		AmountEditable: true,
		GLCreditCode:   "9999",
		SortOrder:      999,
	})
	if err == nil {
		t.Fatalf("expected ErrUnknownGLCode, got nil error + entry %+v", bad)
	}
	if !errors.Is(err, ErrUnknownGLCode) {
		t.Errorf("expected ErrUnknownGLCode, got %T: %v", err, err)
	}
	if bad != nil {
		t.Errorf("expected no entry on failure, got %+v", bad)
	}
}

// Silence the unused-import warning on pgx when the test is skipped
// at the top.
var _ = pgx.ErrNoRows
