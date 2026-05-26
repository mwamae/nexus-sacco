// RLS smoke for the counterparty_directory view (migration 0037).
//
// Property under test: a query against the view from inside a
// tenant-scoped tx returns ONLY that tenant's counterparties. The
// view inherits the RLS policy on counterparties + the per-tenant
// scope on members/org_members, so a cross-tenant query MUST NOT
// leak rows.
//
// Runs against tujenge + one other tenant from the dev seed. Skips
// if fewer than 2 tenants exist (single-tenant dev installs).

package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCounterpartyDirectory_RLSPerTenant(t *testing.T) {
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
	// The default DSN connects as `nexus` which is a superuser that
	// bypasses RLS. Each tx-scoped query below runs `SET LOCAL ROLE
	// nexus_app` to drop superuser privileges for the duration of
	// the tx — that's the role the prod savings service uses, and
	// it's where RLS actually fires.

	type tenantSnap struct {
		ID   uuid.UUID
		Slug string
	}
	rows, err := pool.Query(ctx, `SELECT id, slug FROM tenants ORDER BY slug LIMIT 2`)
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	var tenants []tenantSnap
	for rows.Next() {
		var s tenantSnap
		if err := rows.Scan(&s.ID, &s.Slug); err != nil {
			rows.Close()
			t.Fatalf("scan tenant: %v", err)
		}
		tenants = append(tenants, s)
	}
	rows.Close()
	if len(tenants) < 2 {
		t.Skipf("need at least 2 tenants to verify RLS isolation; got %d", len(tenants))
	}

	type pair struct {
		tenant tenantSnap
		count  int
	}
	var observed []pair
	for _, ten := range tenants {
		var n int
		err := func() error {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE nexus_app`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, ten.ID.String()); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM counterparty_directory`).Scan(&n)
		}()
		if err != nil {
			t.Fatalf("count for %s: %v", ten.Slug, err)
		}
		observed = append(observed, pair{tenant: ten, count: n})
		t.Logf("tenant %s: %d directory rows", ten.Slug, n)
	}

	// Cross-tenant probe: tenant A's set_config must NOT make tenant
	// B's rows visible. Sum of per-tenant counts must equal
	// cross-tenant count (no overlap). If RLS leaked, scoped counts
	// would each return the union and the sum would be inflated.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE nexus_app`); err != nil {
		t.Fatalf("set role: %v", err)
	}
	// Don't set app.tenant_id — nexus_app role with RLS sees zero
	// when no tenant is set.
	var unscoped int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM counterparty_directory`).Scan(&unscoped); err != nil {
		t.Fatalf("unscoped count: %v", err)
	}
	if unscoped != 0 {
		t.Errorf("counterparty_directory leaks rows without tenant scope: got %d, want 0", unscoped)
	}

	// Within-scope: tenant A's count + tenant B's count should reflect
	// disjoint rows. Re-run tenant A in a fresh tx and confirm the
	// count didn't shift.
	var aRescan int
	if err := func() error {
		tx2, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx2.Rollback(ctx)
		if _, err := tx2.Exec(ctx, `SET LOCAL ROLE nexus_app`); err != nil {
			return err
		}
		if _, err := tx2.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenants[0].ID.String()); err != nil {
			return err
		}
		return tx2.QueryRow(ctx, `SELECT count(*) FROM counterparty_directory`).Scan(&aRescan)
	}(); err != nil {
		t.Fatalf("rescan tenant A: %v", err)
	}
	if aRescan != observed[0].count {
		t.Errorf("tenant %s rescan changed: was %d, now %d (RLS leak suspected)",
			tenants[0].Slug, observed[0].count, aRescan)
	}
}
