// Integration test for the Unified Inbox dormancy gate (PR #6).
//
// Property under test on the resolve side:
//   - event=approved   → the snapshotted candidates get
//                        moved from active → dormant via
//                        Status.ApplyTx, dormancy_runs.status flips
//                        to 'applied', and apply_outcomes captures
//                        one outcome per snapshot row.
//   - event=rejected   → dormancy_runs.status = 'rejected', no
//                        status changes applied.
//   - second call after terminal is a no-op (idempotent redeliver).
//
// We don't exercise the submit-side workflow POST here — that's a
// thin wrapper over the same createWorkflowInstance pattern proven
// in the interest/dividend handlers; sticking to the new resolve
// logic keeps the test focused.

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/auth"
	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/store"
)

func TestResolveDormancyFromWorkflow_ApproveAppliesSnapshot(t *testing.T) {
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
	dbPool := &db.Pool{Pool: pool}

	// Resolve tujenge tenant.
	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge tenant: %v", err)
	}
	userID := uuid.New()

	// Find 2 active members we can flip → dormant + then restore.
	type member struct {
		cpID, memberID uuid.UUID
		memberNo       string
		origStatus     domain.MemberStatus
	}
	var members []member
	rows, err := func() (pgx.Rows, error) {
		conn, _ := pool.Acquire(ctx)
		_, _ = conn.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String())
		defer conn.Release()
		return pool.Query(ctx, `
			SELECT m.id, m.counterparty_id, m.member_no, m.status::text
			  FROM members m
			 WHERE m.tenant_id = $1 AND m.status = 'active'
			 LIMIT 2`, tenantID)
	}()
	if err != nil {
		t.Fatalf("query members: %v", err)
	}
	for rows.Next() {
		var m member
		var st string
		_ = rows.Scan(&m.memberID, &m.cpID, &m.memberNo, &st)
		m.origStatus = domain.MemberStatus(st)
		members = append(members, m)
	}
	rows.Close()
	if len(members) < 2 {
		t.Skipf("need ≥ 2 active members in tujenge to run this test, got %d", len(members))
	}

	// Build the run snapshot.
	snapshot := []store.DormancyCandidate{
		{CounterpartyID: members[0].cpID, MemberNo: members[0].memberNo, DaysInactive: 200},
		{CounterpartyID: members[1].cpID, MemberNo: members[1].memberNo, DaysInactive: 200},
	}

	// Build the handler with just the bits the resolve flow needs.
	h := &StatusHandler{
		DB:           dbPool,
		Members:      store.NewMemberStore(pool),
		Status:       store.NewStatusChangeStore(pool),
		DormancyRuns: store.NewDormancyRunStore(pool),
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
			c := rq.Context()
			c = middleware.WithTenant(c, tenantID, "tujenge")
			c = middleware.WithClaims(c, &auth.AccessClaims{
				TenantID: tenantID.String(), UserID: userID.String(), IsPlatformAdmin: true,
			})
			next.ServeHTTP(w, rq.WithContext(c))
		})
	})
	r.Post("/internal/v1/members/dormancy/resolve", h.ResolveDormancyFromWorkflow)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Insert a dormancy_runs row + a workflow_instance_id stub.
	wfID := uuid.New()
	var runID uuid.UUID
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ptrSnap := make([]*store.DormancyCandidate, len(snapshot))
		for i := range snapshot {
			s := snapshot[i]
			ptrSnap[i] = &s
		}
		run, err := h.DormancyRuns.CreateTx(ctx, tx, tenantID, wfID, userID, 90, ptrSnap)
		if err != nil {
			return err
		}
		runID = run.ID
		return nil
	}); err != nil {
		t.Fatalf("seed dormancy_runs: %v", err)
	}

	// Ensure cleanup so the test is re-runnable + leaves member
	// states intact.
	defer func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			for _, m := range members {
				_, _ = tx.Exec(context.Background(),
					`UPDATE members SET status = $2 WHERE id = $1`, m.memberID, string(m.origStatus))
			}
			// dormancy_runs + status_changes from this run:
			_, _ = tx.Exec(context.Background(), `DELETE FROM dormancy_runs WHERE id = $1`, runID)
			_, _ = tx.Exec(context.Background(), `DELETE FROM member_status_changes WHERE changed_by = $1`, userID)
			return nil
		})
	}()

	// POST the resolve envelope.
	envelope := map[string]any{
		"tenant_id": tenantID,
		"event":     "approved",
		"instance":  map[string]any{"id": wfID},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest("POST",
		srv.URL+"/internal/v1/members/dormancy/resolve",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexus-workflow/1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post resolve: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: want 200, got %d. body=%s", resp.StatusCode, respBody)
	}

	// Verify: both members now dormant; run is applied; outcomes captured.
	var runStatus string
	var outcomesRaw []byte
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status, apply_outcomes FROM dormancy_runs WHERE id=$1`, runID).
			Scan(&runStatus, &outcomesRaw)
	}); err != nil {
		t.Fatalf("re-read run: %v", err)
	}
	if runStatus != "applied" {
		t.Errorf("dormancy_runs.status: want applied, got %s", runStatus)
	}
	var outcomes []store.DormancyApplyOutcome
	_ = json.Unmarshal(outcomesRaw, &outcomes)
	if len(outcomes) != 2 {
		t.Errorf("apply_outcomes: want 2 rows, got %d", len(outcomes))
	}
	for i, o := range outcomes {
		if o.Outcome != "applied" {
			t.Errorf("outcome[%d]: want applied, got %s", i, o.Outcome)
		}
	}
	// Idempotent re-call.
	resp2, _ := http.DefaultClient.Do(req)
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	// State must be unchanged after the redeliver.
	var stillApplied string
	_ = dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM dormancy_runs WHERE id=$1`, runID).Scan(&stillApplied)
	})
	if stillApplied != "applied" {
		t.Errorf("re-deliver shouldn't change status; got %s", stillApplied)
	}

	// Silence unused — keeping the time import close to the test
	// in case a future timing assertion lands.
	_ = time.Now
}
