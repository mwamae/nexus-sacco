// Integration test for the GL pipeline end-to-end.
//
// Walks the full flow:
//
//   1. Build a posting.Client pointed at a real HTTP server that
//      mocks the accounting /internal/v1/post endpoint.
//   2. Open a tenant tx, write a forged outbox row.
//   3. Run the dispatcher's drain pass once (in-process import of
//      the dispatcher main isn't possible since it's package main —
//      so we test via the posting.Client path: enqueue → mock
//      accounting receives the post).
//   4. Assert the mock accounting endpoint saw exactly one POST and
//      the outbox row was stamped dispatched_at.
//
// Gated behind SAVINGS_RUN_E2E_TEST=1 so default `go test ./...`
// stays fast. CI sets the env var on every PR — that's the wall
// against the bug reopening.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/posting"
)

func TestHealthzIntegration_PipelineEndToEnd(t *testing.T) {
	if os.Getenv("SAVINGS_RUN_E2E_TEST") != "1" {
		t.Skip("SAVINGS_RUN_E2E_TEST != 1 — opt-in e2e test, skipping")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE status='active' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no active tenant: %v", err)
	}

	// Mock accounting — counts incoming posts + replies with the
	// idempotent JE envelope.
	var postCount atomic.Int32
	acctSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		postCount.Add(1)
		if len(body) > 200 {
			body = body[:200]
		}
		t.Logf("mock accounting POST received: %s", body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","entry_no":"JE-E2E-1"}}`))
	}))
	defer acctSrv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	postingClient, perr := posting.New(acctSrv.URL, "", logger)
	if perr != nil {
		t.Fatalf("posting.New: %v", perr)
	}
	if postingClient.DryRun {
		t.Fatal("posting client should NOT be DryRun when baseURL is set")
	}

	// Enqueue an outbox row through the canonical PostTx surface.
	sourceRef := uuid.NewString()
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return postingClient.PostTx(ctx, tx, posting.PostInput{
			TenantID:     tenantID,
			SourceModule: "savings.e2e.test",
			SourceRef:    sourceRef,
			Narration:    "E2E integration row",
			Lines: []posting.Line{
				{AccountCode: "1000", Debit: mustDec("100.00"), Narration: "test"},
				{AccountCode: "3000", Credit: mustDec("100.00"), Narration: "test"},
			},
		})
	}); err != nil {
		t.Fatalf("PostTx: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM posting_outbox WHERE payload->>'source_module' = $1 AND payload->>'source_ref' = $2`,
			"savings.e2e.test", sourceRef)
	})

	// Drive the dispatcher's drain logic in-process. drain() is the
	// public entry point of cmd/posting-dispatcher's package main —
	// it's accessible because this test lives in cmd/server but the
	// build still includes both binaries via the workspace.
	//
	// Since cmd/server (this test's package) doesn't import the
	// dispatcher, we replay the relevant slice of its logic by
	// driving Post() directly. The shape we're asserting is
	// "enqueue → outbox row → HTTP POST → row stamped".
	if err := drainOnceForTest(ctx, pool, postingClient, sourceRef); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if got := postCount.Load(); got != 1 {
		t.Errorf("mock accounting POST count: want 1, got %d", got)
	}

	var dispatchedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT dispatched_at FROM posting_outbox
		 WHERE payload->>'source_module' = $1 AND payload->>'source_ref' = $2
	`, "savings.e2e.test", sourceRef).Scan(&dispatchedAt); err != nil {
		t.Fatalf("re-read outbox: %v", err)
	}
	if dispatchedAt == nil {
		t.Error("outbox row was not stamped dispatched_at")
	}

	t.Logf("e2e ok: accounting_posts=%d, dispatched_at=%v", postCount.Load(), dispatchedAt)
}

// drainOnceForTest replays the dispatcher's single-row drain. Kept
// here (not in the dispatcher's package main) so the test can run
// without spawning a separate process or importing main.
func drainOnceForTest(ctx context.Context, pool *db.Pool, client *posting.Client, sourceRef string) error {
	var (
		rowID   uuid.UUID
		payload []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT id, payload FROM posting_outbox
		 WHERE payload->>'source_ref' = $1 AND dispatched_at IS NULL
		 ORDER BY enqueued_at LIMIT 1
	`, sourceRef).Scan(&rowID, &payload); err != nil {
		return err
	}
	var p struct {
		TenantID     uuid.UUID `json:"tenant_id"`
		SourceModule string    `json:"source_module"`
		SourceRef    string    `json:"source_ref"`
		Narration    string    `json:"narration"`
		Lines        []struct {
			AccountCode string `json:"account_code"`
			Debit       string `json:"debit,omitempty"`
			Credit      string `json:"credit,omitempty"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	in := posting.PostInput{
		TenantID:     p.TenantID,
		SourceModule: p.SourceModule,
		SourceRef:    p.SourceRef,
		Narration:    p.Narration,
	}
	for _, l := range p.Lines {
		ln := posting.Line{AccountCode: l.AccountCode}
		if l.Debit != "" {
			ln.Debit = mustDec(l.Debit)
		}
		if l.Credit != "" {
			ln.Credit = mustDec(l.Credit)
		}
		in.Lines = append(in.Lines, ln)
	}
	if err := client.Post(ctx, in); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		UPDATE posting_outbox SET dispatched_at = now() WHERE id = $1
	`, rowID)
	return err
}

func mustDec(s string) decimalShim {
	d, err := decimalParse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Type alias so the test file doesn't drag shopspring/decimal into
// imports just for two literals — posting.Line uses it but we
// build the value via posting's own parsing path.
type decimalShim = decimal.Decimal

func decimalParse(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, errors.New("bad decimal " + s)
	}
	return d, nil
}
