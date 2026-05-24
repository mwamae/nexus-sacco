// Integration test for the Unified Inbox onboarding-decision
// resolve callback (PR #8).
//
// Property under test:
//   - event=rejected   → status transitions to 'declined'.
//   - second call after terminal is a no-op (idempotent redeliver).
//   - missing auth header is rejected (401).
//
// We test the reject path because it exercises the same dispatch +
// auth + idempotency logic as approve without requiring the full
// activation chain (which materialises a member, opens share +
// deposit accounts, posts the registration fee — much heavier
// setup that the existing acceptance test suite already pins).

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

	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/store"
)

func TestResolveApplicationFromWorkflow_RejectAndIdempotent(t *testing.T) {
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

	// Seed a minimal application in reviewed_pending_approval with a
	// workflow_instance_id. The reject path doesn't need a full
	// ApplicantPayload — TransitionTx only touches status + decline
	// fields. Using raw SQL keeps the test independent of the wider
	// CreateApplication validation.
	wfID := uuid.New()
	submitter := uuid.New()
	var appID uuid.UUID
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO membership_applications (
			  tenant_id, application_no, kind, applicant_name,
			  applicant_payload, status, fee_required, fee_amount_due,
			  fee_status, submitted_by, workflow_instance_id
			) VALUES (
			  $1,
			  'APP-PR8-' || substr(gen_random_uuid()::text, 1, 8),
			  'individual', 'PR #8 reject test',
			  '{}'::jsonb, 'reviewed_pending_approval', false, 0,
			  'not_required', $2, $3
			) RETURNING id
		`, tenantID, submitter, wfID).Scan(&appID)
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	// Cleanup so re-runs are clean.
	defer func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(), `DELETE FROM membership_applications WHERE id = $1`, appID)
			return nil
		})
	}()

	// Mount just the resolve route. No JWT shim needed — the
	// internal-token / User-Agent gate is what guards it.
	h := &ApplicationHandler{
		DB:           dbPool,
		Applications: store.NewApplicationStore(pool),
	}
	r := chi.NewRouter()
	r.Post("/internal/v1/applications/{id}/resolve", h.ResolveFromWorkflow)
	srv := httptest.NewServer(r)
	defer srv.Close()

	envelope := map[string]any{
		"tenant_id": tenantID,
		"event":     "rejected",
		"instance": map[string]any{
			"id":      wfID,
			"context": map[string]any{"decline_reason": "PR #8 test reason"},
		},
	}
	url := srv.URL + "/internal/v1/applications/" + appID.String() + "/resolve"

	// Missing-auth check — no X-Internal-Token, no User-Agent prefix
	// → 401.
	req0, _ := http.NewRequest("POST", url, strings.NewReader(`{}`))
	req0.Header.Set("Content-Type", "application/json")
	resp0, _ := http.DefaultClient.Do(req0)
	if resp0 != nil {
		_ = resp0.Body.Close()
	}
	if resp0.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth: want 401, got %d", resp0.StatusCode)
	}

	// Reject call.
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexus-workflow/1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject resolve: want 200, got %d. body=%s", resp.StatusCode, respBody)
	}

	// Verify status flipped to declined + decline_reason captured.
	var status, declineReason string
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status, COALESCE(decline_reason,'') FROM membership_applications WHERE id=$1`,
			appID).Scan(&status, &declineReason)
	}); err != nil {
		t.Fatalf("read app: %v", err)
	}
	if status != "declined" {
		t.Errorf("status: want declined, got %s", status)
	}
	if declineReason != "PR #8 test reason" {
		t.Errorf("decline_reason: want 'PR #8 test reason', got %s", declineReason)
	}

	// Idempotent re-deliver. Change the reason in the envelope to
	// prove the stored reason isn't overwritten.
	envelope["instance"].(map[string]any)["context"].(map[string]any)["decline_reason"] = "SECOND CALL"
	body2, _ := json.Marshal(envelope)
	req2, _ := http.NewRequest("POST", url, strings.NewReader(string(body2)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("User-Agent", "nexus-workflow/1")
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	var stillSame string
	_ = dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(decline_reason,'') FROM membership_applications WHERE id=$1`,
			appID).Scan(&stillSame)
	})
	if stillSame != "PR #8 test reason" {
		t.Errorf("re-deliver overwrote decline_reason: want 'PR #8 test reason', got %s", stillSame)
	}
}
