// Integration test for the soak harness. Requires:
//   - MPESA_RUN_SOAK_TEST=1  (opt-in; CI sets this, local `go test
//     ./...` skips so the orchestrator suite isn't poisoned by
//     half-drained soak events)
//   - DATABASE_URL with at least one active paybill seeded
//   - a running cmd/distributor against the same DB (the test does
//     not spin one up — that's the caller's job, matching how
//     production runs the binary)
// CI invokes with N=20 against the dev DB; the 24-hour acceptance
// soak runs the binary directly with N=1000.

package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nexussacco/mpesa/internal/db"
)

func TestSoakHarness_SmallN(t *testing.T) {
	if os.Getenv("MPESA_RUN_SOAK_TEST") != "1" {
		t.Skip("MPESA_RUN_SOAK_TEST != 1 — opt-in soak test, skipping")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.NewPrivileged(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	// Sanity check: at least one active paybill must exist or the
	// harness has nothing to soak against. Skip rather than fail —
	// CI on a freshly-migrated DB hasn't seeded paybills.
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mpesa_paybills WHERE status = 'active'`).Scan(&count); err != nil {
		t.Fatalf("count active paybills: %v", err)
	}
	if count == 0 {
		t.Skip("no active paybills in mpesa_paybills — seed one before running soak")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	const n = 20
	if err := runSoak(ctx, pool, logger, n, 60*time.Second); err != nil {
		t.Fatalf("soak N=%d failed: %v", n, err)
	}
}
