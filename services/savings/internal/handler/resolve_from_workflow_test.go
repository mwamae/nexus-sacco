// Acceptance test for the Unified Inbox cutover (PR #3):
//
//   POST /internal/v1/pending-approvals/{id}/resolve
//
// Property under test — IDEMPOTENT EXECUTION. The workflow service
// may redeliver its terminal-status webhook on transient transport
// failures; the resolve handler must:
//   • execute the underlying transaction exactly once on the
//     first  event=approved call;
//   • be a no-op on the second event=approved call (same
//     pending_approval id, already terminal).
//
// Both expectations are verified by counting the deposit_transactions
// rows the executor creates: must equal 1 after both calls, not 2.
//
// Skipped when DATABASE_URL is unset (matches the other integration
// tests). Cleanup keys off env.UserID + env.MarkerSuffix so the run's
// rows are removed without disturbing surrounding seed data.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

func TestResolveFromWorkflow_IdempotentApprove(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	scenario := seedCollectionScenario(t, env)

	ctx := context.Background()

	// Build a synthetic pending_approval of kind=deposit pointing at
	// the seeded deposit account. The dispatcher's executePayloadTx
	// for ApprovalKindDeposit calls DepositHandler.ExecuteDepositTx
	// which inserts a deposit_transactions row + bumps balances.
	payload := map[string]any{
		"account_id":             scenario.DepositAcctID,
		"amount":                 "1000.00",
		"channel":                "mpesa",
		"channel_ref":            "MPS-RES-" + env.MarkerSuffix,
		"narration":              "resolve idempotency test",
		"value_date":             time.Now().UTC().Format("2006-01-02"),
		"bypass_duplicate_check": true,
	}
	payloadBytes, _ := json.Marshal(payload)

	var paID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO pending_approvals (
			  tenant_id, kind, status, title, amount,
			  subject_member_id, subject_account_id,
			  payload, maker_user_id
			) VALUES (
			  $1, 'deposit', 'pending', $2, $3,
			  $4, $5, $6::jsonb, $7
			)
			RETURNING id
		`, env.TenantID, "resolve test deposit", decimal.NewFromInt(1000),
			scenario.CounterpartyID, scenario.DepositAcctID, payloadBytes, env.UserID,
		).Scan(&paID)
	}); err != nil {
		t.Fatalf("seed pending_approval: %v", err)
	}

	// Count deposit_transactions on the target account BEFORE.
	depositTxnCount := func(label string) int {
		var n int
		if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT count(*) FROM deposit_transactions WHERE account_id = $1`,
				scenario.DepositAcctID).Scan(&n)
		}); err != nil {
			t.Fatalf("count txns (%s): %v", label, err)
		}
		return n
	}
	before := depositTxnCount("before")

	// Build the workflow callback envelope the resolve handler expects.
	envelope := map[string]any{
		"tenant_id": env.TenantID,
		"event":     "approved",
		"instance": map[string]any{
			"id": uuid.New(),
			"context": map[string]any{
				"legacy_pending_approval_id": paID.String(),
			},
		},
	}

	url := env.Server.URL + "/internal/v1/pending-approvals/" + paID.String() + "/resolve"

	// First call — must execute the deposit + mark pending_approval approved.
	s1, b1 := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s1 != http.StatusOK {
		t.Fatalf("first resolve: want 200, got %d. body=%s", s1, b1)
	}

	after1 := depositTxnCount("after-first")
	if after1 != before+1 {
		t.Errorf("first resolve should add exactly 1 deposit_txn (before=%d, after=%d)", before, after1)
	}

	// Second call (redelivered webhook) — must be a no-op.
	s2, b2 := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s2 != http.StatusOK {
		t.Fatalf("second resolve: want 200 (idempotent), got %d. body=%s", s2, b2)
	}

	after2 := depositTxnCount("after-second")
	if after2 != after1 {
		t.Errorf("second resolve should NOT add another deposit_txn (after-first=%d, after-second=%d)", after1, after2)
	}

	// And the pending_approval row should reflect terminal status with
	// the result_txn_id populated.
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		var status string
		var resultTxnID *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT status, result_txn_id FROM pending_approvals WHERE id = $1`,
			paID).Scan(&status, &resultTxnID); err != nil {
			return err
		}
		if status != "approved" {
			t.Errorf("pending_approval.status: want approved, got %s", status)
		}
		if resultTxnID == nil {
			t.Errorf("pending_approval.result_txn_id should be populated after approve")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify pa terminal state: %v", err)
	}
}

func TestResolveFromWorkflow_RejectMarksDeclined(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	ctx := context.Background()
	// Minimal pending_approval — we don't need a working executor
	// for the decline path since it skips executePayloadTx.
	var paID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO pending_approvals (
			  tenant_id, kind, status, title, payload, maker_user_id
			) VALUES ($1, 'deposit', 'pending', 'decline test', '{}'::jsonb, $2)
			RETURNING id
		`, env.TenantID, env.UserID).Scan(&paID)
	}); err != nil {
		t.Fatalf("seed pa: %v", err)
	}

	envelope := map[string]any{
		"tenant_id": env.TenantID,
		"event":     "rejected",
		"instance":  map[string]any{"id": uuid.New(), "context": map[string]any{}},
	}
	url := env.Server.URL + "/internal/v1/pending-approvals/" + paID.String() + "/resolve"
	s, b := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s != http.StatusOK {
		t.Fatalf("reject resolve: want 200, got %d. body=%s", s, b)
	}
	var status string
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM pending_approvals WHERE id=$1`, paID).Scan(&status)
	}); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "declined" {
		t.Errorf("status after reject: want declined, got %s", status)
	}
}

// httpJSONWithHeaders is the local helper that lets the test set
// custom request headers. The existing httpJSON in
// collection_desk_acceptance_test.go doesn't take headers; rather
// than widen its signature for one caller, we add a thin wrapper.
func httpJSONWithHeaders(t *testing.T, method, url string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}
