// Integration tests for the Unified Inbox additions to the instance
// store: ClaimTx (lock contention + expiry takeover + idempotent
// re-claim), ReleaseTx, the sla_breach_at mirror in UpdateProgressTx,
// and the new 'majority' wf_quorum enum value.
//
// Pattern matches the other store-level tests: skips when
// DATABASE_URL is unset; every write happens inside a transaction
// that's rolled back at the end so no fixtures land in the live DB.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/workflow/internal/domain"
)

// ─── shared scaffolding ────────────────────────────────────────────

// withTestEnv connects to the dev DB, picks the tujenge tenant, sets
// the RLS GUC, and returns a *pgxpool.Pool + a tx that the test
// MUST roll back on cleanup. All sub-tests inside one test share one
// tx so cleanup is automatic.
func withTestEnv(t *testing.T) (*pgxpool.Pool, pgx.Tx, uuid.UUID, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("begin: %v", err)
	}
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		pool.Close()
		t.Skipf("no tujenge tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		pool.Close()
		t.Fatalf("set tenant: %v", err)
	}
	cleanup := func() {
		_ = tx.Rollback(ctx)
		conn.Release()
		pool.Close()
	}
	return pool, tx, tenantID, cleanup
}

// seedInstance inserts a minimal definition + one in-progress
// instance and returns the instance id. The test can then mutate
// its claim state. Uses uuid.New() for every key so multiple
// sub-tests in the same tx don't collide.
func seedInstance(t *testing.T, ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, slaHours *int) (defID, instID uuid.UUID) {
	t.Helper()
	// Definition with one level. ApproverRoles is required-text[];
	// pass an empty array so we don't depend on identity roles.
	if err := tx.QueryRow(ctx, `
		INSERT INTO wf_definitions (tenant_id, process_kind, name, version, active)
		VALUES ($1, $2, 'test', 1, false)  -- active=false to avoid colliding with the per-(tenant,kind) unique index
		RETURNING id
	`, tenantID, fmt.Sprintf("test_kind_%d", time.Now().UnixNano())).Scan(&defID); err != nil {
		t.Fatalf("insert def: %v", err)
	}
	// Level snapshot inside the instance row (the engine normally
	// builds this from wf_levels; for tests we set it directly).
	level := domain.LevelState{
		Order: 0, Name: "Maker", Status: domain.LvlInProgress,
		ApproverRoles: []string{}, Quorum: domain.QuorumAnyOne,
		SLAHours: slaHours,
	}
	if slaHours != nil {
		due := time.Now().UTC().Add(time.Duration(*slaHours) * time.Hour)
		level.SLADueAt = &due
	}
	lvlsBytes, _ := json.Marshal([]domain.LevelState{level})
	var breach any
	if level.SLADueAt != nil {
		breach = *level.SLADueAt
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO wf_instances (
		  tenant_id, definition_id, process_kind, subject_kind, subject_id,
		  status, current_level, context, levels, sla_breach_at
		) VALUES (
		  $1, $2, 'test_kind', 'test_subject', $3,
		  'in_progress', 0, '{}'::jsonb, $4, $5
		) RETURNING id
	`, tenantID, defID, uuid.New(), lvlsBytes, breach).Scan(&instID); err != nil {
		t.Fatalf("insert instance: %v", err)
	}
	return defID, instID
}

// ─── ClaimTx ────────────────────────────────────────────────────────

func TestClaimTx_ContentionReturns409Sentinel(t *testing.T) {
	pool, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	_, instID := seedInstance(t, ctx, tx, tenantID, nil)
	store := NewInstanceStore(pool)

	userA := uuid.New()
	userB := uuid.New()

	// User A claims successfully.
	got, err := store.ClaimTx(ctx, tx, instID, userA, 30*time.Minute)
	if err != nil {
		t.Fatalf("first claim by userA: %v", err)
	}
	if got.ClaimedBy == nil || *got.ClaimedBy != userA {
		t.Fatalf("claimed_by: want %s, got %v", userA, got.ClaimedBy)
	}
	if got.ClaimExpires == nil {
		t.Fatalf("claim_expires not set")
	}

	// User B tries to claim — must hit the sentinel.
	if _, err := store.ClaimTx(ctx, tx, instID, userB, 30*time.Minute); !errors.Is(err, ErrClaimContested) {
		t.Errorf("contested claim: want ErrClaimContested, got %v", err)
	}
}

func TestClaimTx_SameUserRefreshesExpiry(t *testing.T) {
	pool, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	_, instID := seedInstance(t, ctx, tx, tenantID, nil)
	store := NewInstanceStore(pool)
	user := uuid.New()

	first, err := store.ClaimTx(ctx, tx, instID, user, 5*time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Re-claim by the SAME user with a longer TTL — should succeed
	// and bump claim_expires forward.
	time.Sleep(10 * time.Millisecond) // ensure a measurable delta
	second, err := store.ClaimTx(ctx, tx, instID, user, 60*time.Minute)
	if err != nil {
		t.Fatalf("re-claim by same user: %v", err)
	}
	if !second.ClaimExpires.After(*first.ClaimExpires) {
		t.Errorf("re-claim should extend claim_expires: first=%v second=%v", first.ClaimExpires, second.ClaimExpires)
	}
}

func TestClaimTx_ExpiredLockAllowsTakeover(t *testing.T) {
	pool, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	_, instID := seedInstance(t, ctx, tx, tenantID, nil)
	store := NewInstanceStore(pool)
	userA := uuid.New()
	userB := uuid.New()

	// User A claims with a tiny TTL.
	if _, err := store.ClaimTx(ctx, tx, instID, userA, 50*time.Millisecond); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Force the lock past its expiry by waiting.
	time.Sleep(80 * time.Millisecond)
	// User B should now be able to take over (claim_expires < now()).
	got, err := store.ClaimTx(ctx, tx, instID, userB, 30*time.Minute)
	if err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	if got.ClaimedBy == nil || *got.ClaimedBy != userB {
		t.Errorf("after takeover, claimed_by: want %s, got %v", userB, got.ClaimedBy)
	}
}

// ─── ReleaseTx ──────────────────────────────────────────────────────

func TestReleaseTx_ClearsLock(t *testing.T) {
	pool, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	_, instID := seedInstance(t, ctx, tx, tenantID, nil)
	store := NewInstanceStore(pool)
	user := uuid.New()

	if _, err := store.ClaimTx(ctx, tx, instID, user, 30*time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.ReleaseTx(ctx, tx, instID); err != nil {
		t.Fatalf("release: %v", err)
	}
	after, err := store.ByIDTx(ctx, tx, instID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.ClaimedBy != nil || after.ClaimedAt != nil || after.ClaimExpires != nil {
		t.Errorf("post-release: claim fields should be null; got by=%v at=%v exp=%v",
			after.ClaimedBy, after.ClaimedAt, after.ClaimExpires)
	}
	// Now anyone can take it.
	if _, err := store.ClaimTx(ctx, tx, instID, uuid.New(), 30*time.Minute); err != nil {
		t.Errorf("post-release claim by new user: %v", err)
	}
}

// ─── UpdateProgressTx mirrors sla_breach_at + clears claims ─────────

func TestUpdateProgressTx_MirrorsSLAAndClearsClaimOnTerminal(t *testing.T) {
	pool, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	hours := 24
	_, instID := seedInstance(t, ctx, tx, tenantID, &hours)
	store := NewInstanceStore(pool)
	user := uuid.New()

	// Claim it first so we can verify terminal-status clears the lock.
	if _, err := store.ClaimTx(ctx, tx, instID, user, 30*time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Sanity: sla_breach_at was populated at seed time.
	before, _ := store.ByIDTx(ctx, tx, instID)
	if before.SLABreachAt == nil {
		t.Fatalf("seed should have stamped sla_breach_at")
	}

	// Approve to terminal. UpdateProgressTx should null sla_breach_at
	// and clear the claim.
	before.Status = domain.StatusApproved
	now := time.Now().UTC()
	before.CompletedAt = &now
	if err := store.UpdateProgressTx(ctx, tx, before); err != nil {
		t.Fatalf("update to approved: %v", err)
	}
	after, _ := store.ByIDTx(ctx, tx, instID)
	if after.SLABreachAt != nil {
		t.Errorf("terminal: sla_breach_at should be null, got %v", after.SLABreachAt)
	}
	if after.ClaimedBy != nil {
		t.Errorf("terminal: claimed_by should be null, got %v", after.ClaimedBy)
	}
}

// ─── majority quorum is accepted by the enum ────────────────────────

func TestMajorityQuorumAccepted(t *testing.T) {
	_, tx, tenantID, cleanup := withTestEnv(t)
	defer cleanup()
	ctx := context.Background()

	// Insert a definition + level with quorum='majority' to prove
	// the enum extension landed. The test asserts the round-trip
	// returns the literal string back.
	var defID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO wf_definitions (tenant_id, process_kind, name, version, active)
		VALUES ($1, $2, 'majority test', 1, false)
		RETURNING id
	`, tenantID, fmt.Sprintf("majority_kind_%d", time.Now().UnixNano())).Scan(&defID); err != nil {
		t.Fatalf("insert def: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wf_levels (definition_id, tenant_id, level_order, name, approver_roles, quorum)
		VALUES ($1, $2, 0, 'Board', '{}'::text[], 'majority')
	`, defID, tenantID); err != nil {
		t.Fatalf("insert level with majority quorum: %v", err)
	}
	var got string
	if err := tx.QueryRow(ctx, `SELECT quorum::text FROM wf_levels WHERE definition_id=$1`, defID).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "majority" {
		t.Errorf("quorum: want majority, got %q", got)
	}
}
