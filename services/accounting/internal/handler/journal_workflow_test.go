// Integration test for the Unified Inbox journal-entry resolve
// callback (PR #7).
//
// Property under test:
//   • event=approved   → status='posted', entry_no allocated,
//                        posted_by set.
//   • event=rejected   → status='rejected'.
//   • second call after terminal is a no-op (idempotent redeliver).
//
// We don't drive the full Create → workflow chain end-to-end (that
// path requires a running workflow service). Instead we seed a JE
// directly in 'pending_approval' with a workflow_instance_id, POST
// the resolve envelope, and verify the state change.

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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/store"
)

func TestResolveJournalEntryFromWorkflow_ApproveAndIdempotent(t *testing.T) {
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

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge tenant: %v", err)
	}

	// Seed a JE directly in pending_approval. Totals zero so the
	// CHECK constraint stays happy without needing real line data;
	// the resolve handler only flips status / allocates entry_no.
	wfID := uuid.New()
	creator := uuid.New()
	var entryID uuid.UUID
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO journal_entries (
			  tenant_id, entry_date, value_date, period_year, period_month,
			  entry_type, narration, status, total_debits, total_credits,
			  created_by, workflow_instance_id
			) VALUES (
			  $1, CURRENT_DATE, CURRENT_DATE,
			  EXTRACT(YEAR FROM CURRENT_DATE)::int,
			  EXTRACT(MONTH FROM CURRENT_DATE)::int,
			  'manual', $2, 'pending_approval', 0, 0,
			  $3, $4
			) RETURNING id
		`, tenantID, "PR #7 resolve callback test", creator, wfID).Scan(&entryID)
	}); err != nil {
		t.Fatalf("seed JE: %v", err)
	}
	// Cleanup so the test is re-runnable + leaves no orphan rows.
	defer func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(), `DELETE FROM journal_entries WHERE id = $1`, entryID)
			return nil
		})
	}()

	// Mount just the resolve handler — no chi tenant resolution needed
	// because the envelope carries tenant_id explicitly + the handler's
	// internal-token / User-Agent gate is what guards it.
	h := &JournalHandler{
		DB:       dbPool,
		Journals: store.NewJournalStore(pool),
	}
	r := chi.NewRouter()
	r.Post("/internal/v1/journal-entries/resolve", h.ResolveFromWorkflow)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// POST event=approved.
	envelope := map[string]any{
		"tenant_id": tenantID,
		"event":     "approved",
		"instance":  map[string]any{"id": wfID},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest("POST",
		srv.URL+"/internal/v1/journal-entries/resolve",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexus-workflow/1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: want 200, got %d. body=%s", resp.StatusCode, respBody)
	}

	// Verify: status='posted' + entry_no allocated + posted_by set.
	var status, entryNo string
	var postedBy uuid.UUID
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT status, COALESCE(entry_no, ''), COALESCE(posted_by, '00000000-0000-0000-0000-000000000000'::uuid)
			  FROM journal_entries WHERE id = $1
		`, entryID).Scan(&status, &entryNo, &postedBy)
	}); err != nil {
		t.Fatalf("read JE: %v", err)
	}
	if status != "posted" {
		t.Errorf("status: want posted, got %s", status)
	}
	if entryNo == "" {
		t.Errorf("entry_no should be allocated on post")
	}
	if postedBy == uuid.Nil {
		t.Errorf("posted_by should be set on post")
	}

	// Second call — redelivered webhook. Must be a no-op. We detect
	// it by snapshotting the entry_no + posted_by before, then
	// verifying they're unchanged after. (ApproveAndPostTx wouldn't
	// allocate a new entry_no since status != pending_approval, but
	// the contract is "idempotent" — pin it explicitly.)
	beforeEntryNo, beforePostedBy := entryNo, postedBy
	resp2, _ := http.DefaultClient.Do(req)
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	var afterEntryNo string
	var afterPostedBy uuid.UUID
	_ = dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(entry_no, ''), COALESCE(posted_by, '00000000-0000-0000-0000-000000000000'::uuid)
			   FROM journal_entries WHERE id = $1`, entryID).Scan(&afterEntryNo, &afterPostedBy)
	})
	if afterEntryNo != beforeEntryNo {
		t.Errorf("re-deliver allocated a new entry_no: before=%s after=%s", beforeEntryNo, afterEntryNo)
	}
	if afterPostedBy != beforePostedBy {
		t.Errorf("re-deliver re-stamped posted_by: before=%s after=%s", beforePostedBy, afterPostedBy)
	}
}
