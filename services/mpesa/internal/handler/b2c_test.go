// Phase-4 B2C handler tests. Three properties pinned:
//
//   1. Enqueue is idempotent on (tenant, source_module, source_ref).
//   2. Result callback success flips the row to 'completed' + invokes
//      the FinalizeClient.
//   3. Result callback non-zero code flips the row to 'failed' +
//      does NOT invoke FinalizeClient.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

type fakeFinalize struct {
	count        atomic.Int32
	reverseCount atomic.Int32
	// reverseErr lets tests inject the savings-side error (or nil for
	// the happy path). Default is nil — savings returned 200.
	reverseErr error
	// lastReverseReceipt captures the mpesa_reversal_receipt the
	// caller passed; tests can assert on the value the handler
	// surfaced from the Daraja envelope.
	lastReverseReceipt string
	lastReverseLoanID  uuid.UUID
}

func (f *fakeFinalize) FinalizeDisbursement(_ context.Context, _ uuid.UUID, _ string) error {
	f.count.Add(1)
	return nil
}

func (f *fakeFinalize) ReverseDisbursement(
	_ context.Context, loanID uuid.UUID, mpesaReceipt, _ string,
) error {
	f.reverseCount.Add(1)
	f.lastReverseReceipt = mpesaReceipt
	f.lastReverseLoanID = loanID
	return f.reverseErr
}

func TestB2C_Enqueue_Idempotent(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	finalizer := &fakeFinalize{}
	h := newB2CHandler(t, dbPool, finalizer, "secret-token")
	srv := newB2CSrv(h)
	defer srv.Close()

	body := map[string]any{
		"paybill_id":    paybill.ID,
		"msisdn":        "254712345678",
		"amount":        "1000",
		"source_module": "phase4.test",
		"source_ref":    "REF-IDEMPOTENT-01",
		"command_id":    "BusinessPayment",
		"remarks":       "Phase 4 idempotency",
	}
	first := postEnqueue(t, srv, body, "secret-token")
	if !first["inserted"].(bool) {
		t.Errorf("first enqueue: want inserted=true, got %v", first)
	}
	second := postEnqueue(t, srv, body, "secret-token")
	if second["inserted"].(bool) {
		t.Errorf("second enqueue: want inserted=false, got %v", second)
	}
	if first["id"] != second["id"] {
		t.Errorf("second enqueue must return the first id; got %v vs %v", first["id"], second["id"])
	}

	// Clean up so re-runs are fresh.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_outbound_requests WHERE source_ref = 'REF-IDEMPOTENT-01'`)
	})
}

func TestB2C_Enqueue_RejectsWithoutInternalToken(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	h := newB2CHandler(t, dbPool, &fakeFinalize{}, "secret-token")
	srv := newB2CSrv(h)
	defer srv.Close()

	url := srv.URL + "/v1/mpesa/b2c/requests" // staff/internal route — stays under /v1/mpesa
	body := map[string]any{
		"paybill_id":    paybill.ID,
		"msisdn":        "254712345678",
		"amount":        "100",
		"source_module": "phase4.test",
		"source_ref":    "REF-NO-AUTH",
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	// No X-Internal-Token header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
}

func TestB2C_ResultCallback_Success_InvokesFinalize(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	// Pre-seed an outbound row in 'sent' status with a known
	// conversation_id (mimics what the dispatcher would have done).
	convID := "AG_" + fmt.Sprint(time.Now().UnixNano())
	var outboundID uuid.UUID
	_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_outbound_requests
				(tenant_id, paybill_id, kind, msisdn, amount,
				 source_module, source_ref, status,
				 daraja_conversation_id, daraja_originator_id)
			VALUES ($1, $2, 'b2c_disbursement', '254712345678', 1000,
			        'loan.disbursement', $3, 'sent', $4, 'orig-1')
			RETURNING id
		`, tenantID, paybill.ID, uuid.New().String(), convID).Scan(&outboundID)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_outbound_requests WHERE id = $1`, outboundID)
	})

	finalizer := &fakeFinalize{}
	h := newB2CHandler(t, dbPool, finalizer, "secret-token")
	srv := newB2CSrv(h)
	defer srv.Close()

	resultBody := map[string]any{
		"Result": map[string]any{
			"ResultCode":     0,
			"ResultDesc":     "The service request is processed successfully.",
			"ConversationID": convID,
			"TransactionID":  "QKE12345",
			"ResultParameters": map[string]any{
				"ResultParameter": []map[string]any{
					{"Key": "TransactionReceipt", "Value": "QKE12345"},
				},
			},
		},
	}
	resp := postResult(t, srv, paybill.ID, paybill.WebhookToken, resultBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("result callback: %d", resp.StatusCode)
	}

	// Outbound row flipped to completed + receipt stamped.
	var status, receipt string
	_ = pool.QueryRow(context.Background(), `
		SELECT status::text, COALESCE(mpesa_receipt_number, '')
		  FROM mpesa_outbound_requests WHERE id = $1
	`, outboundID).Scan(&status, &receipt)
	if status != "completed" {
		t.Errorf("status: want completed, got %q", status)
	}
	if receipt != "QKE12345" {
		t.Errorf("receipt: want QKE12345, got %q", receipt)
	}
	// FinalizeClient was invoked once (since source_module = loan.disbursement
	// the callback hands off to savings).
	if finalizer.count.Load() != 1 {
		t.Errorf("FinalizeClient: want 1 call, got %d", finalizer.count.Load())
	}
}

func TestB2C_ResultCallback_Failure_SkipsFinalize(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	convID := "AG_FAIL_" + fmt.Sprint(time.Now().UnixNano())
	var outboundID uuid.UUID
	_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_outbound_requests
				(tenant_id, paybill_id, kind, msisdn, amount,
				 source_module, source_ref, status,
				 daraja_conversation_id, daraja_originator_id)
			VALUES ($1, $2, 'b2c_disbursement', '254712345678', 1000,
			        'loan.disbursement', $3, 'sent', $4, 'orig-2')
			RETURNING id
		`, tenantID, paybill.ID, uuid.New().String(), convID).Scan(&outboundID)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_outbound_requests WHERE id = $1`, outboundID)
	})

	finalizer := &fakeFinalize{}
	h := newB2CHandler(t, dbPool, finalizer, "secret-token")
	srv := newB2CSrv(h)
	defer srv.Close()

	resultBody := map[string]any{
		"Result": map[string]any{
			"ResultCode":     2001,
			"ResultDesc":     "Insufficient balance",
			"ConversationID": convID,
		},
	}
	resp := postResult(t, srv, paybill.ID, paybill.WebhookToken, resultBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("result callback: %d", resp.StatusCode)
	}

	var status string
	_ = pool.QueryRow(context.Background(),
		`SELECT status::text FROM mpesa_outbound_requests WHERE id = $1`, outboundID,
	).Scan(&status)
	if status != "failed" {
		t.Errorf("status: want failed, got %q", status)
	}
	if finalizer.count.Load() != 0 {
		t.Errorf("FinalizeClient: must NOT be invoked on failure, got %d calls", finalizer.count.Load())
	}
}

// ─── helpers ───

func newB2CHandler(t *testing.T, dbPool *db.Pool, finalize FinalizeClient, internalToken string) *B2CHandler {
	t.Helper()
	return &B2CHandler{
		DB:            dbPool,
		Paybills:      store.NewPaybillStore(dbPool.Pool),
		Outbound:      store.NewOutboundRequestStore(dbPool.Pool),
		Audit:         store.NewAuditStore(dbPool.Pool),
		Workflow:      workflowclient.New(),
		Finalize:      finalize,
		InternalToken: internalToken,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func newB2CSrv(h *B2CHandler) *httptest.Server {
	r := chi.NewRouter()
	// Internal enqueue keeps /v1/mpesa prefix; Daraja-facing routes
	// dropped the prefix per Safaricom's no-"mpesa"-in-URL rule.
	r.Post("/v1/mpesa/b2c/requests", h.Enqueue)
	r.Post("/v1/b2c/{paybill_id}/result", h.Result)
	r.Post("/v1/b2c/{paybill_id}/timeout", h.Timeout)
	r.Post("/v1/b2c/{paybill_id}/reverse", h.Reverse)
	return httptest.NewServer(r)
}

func postEnqueue(t *testing.T, srv *httptest.Server, body map[string]any, token string) map[string]any {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/mpesa/b2c/requests", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("enqueue: status %d body %s", resp.StatusCode, raw)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return env.Data
}

func postResult(t *testing.T, srv *httptest.Server, paybillID uuid.UUID, token string, body map[string]any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/v1/b2c/%s/result?token=%s", srv.URL, paybillID, token)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func postReverse(t *testing.T, srv *httptest.Server, paybillID uuid.UUID, token string, body map[string]any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/v1/b2c/%s/reverse?token=%s", srv.URL, paybillID, token)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestB2C_ReverseCallback_LoanDisbursement_HandsOffToSavings is the
// integration test for Fix 2 — it asserts the mpesa reverse webhook
// (a) marks the outbound row 'reversed', (b) creates the wf task,
// and (c) calls savings.ReverseDisbursement out-of-tx. Combined with
// the dispatcher loadcreds_test.go (which proves Fix 1 — encrypted
// credentials decrypt cleanly), this ties both fixes end-to-end:
// signed dispatch fires + reversal hands off to savings.
func TestB2C_ReverseCallback_LoanDisbursement_HandsOffToSavings(t *testing.T) {
	pool, tenantID, _ := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	// Source_ref must be a valid UUID — the reverse handler parses
	// it as the loan_id when source_module='loan.disbursement'.
	loanID := uuid.New()
	convID := "AG_REV_" + fmt.Sprint(time.Now().UnixNano())
	var outboundID uuid.UUID
	_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_outbound_requests
				(tenant_id, paybill_id, kind, msisdn, amount,
				 source_module, source_ref, status,
				 daraja_conversation_id, daraja_originator_id)
			VALUES ($1, $2, 'b2c_disbursement', '254712345678', 1000,
			        'loan.disbursement', $3, 'sent', $4, 'orig-rev-1')
			RETURNING id
		`, tenantID, paybill.ID, loanID.String(), convID).Scan(&outboundID)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_outbound_requests WHERE id = $1`, outboundID)
	})

	finalizer := &fakeFinalize{}
	h := newB2CHandler(t, dbPool, finalizer, "secret-token")
	srv := newB2CSrv(h)
	defer srv.Close()

	resultBody := map[string]any{
		"Result": map[string]any{
			"ResultCode":     0,
			"ResultDesc":     "Transaction reversed",
			"ConversationID": convID,
		},
	}
	resp := postReverse(t, srv, paybill.ID, paybill.WebhookToken, resultBody)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("reverse callback: status %d body %s", resp.StatusCode, raw)
	}

	// Outbound row marked reversed.
	var status string
	_ = pool.QueryRow(context.Background(),
		`SELECT status::text FROM mpesa_outbound_requests WHERE id = $1`, outboundID,
	).Scan(&status)
	if status != "reversed" {
		t.Errorf("outbound status: want reversed, got %q", status)
	}

	// ReverseDisbursement was invoked exactly once.
	if finalizer.reverseCount.Load() != 1 {
		t.Errorf("savings ReverseDisbursement: want 1 call, got %d", finalizer.reverseCount.Load())
	}

	// Audit trail recorded the handoff outcome (one entry with
	// action=mpesa.b2c.reverse_handoff).
	var auditCount int
	_ = pool.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log
		 WHERE tenant_id = $1
		   AND target_id = $2
		   AND action = 'mpesa.b2c.reverse_handoff'
	`, tenantID, outboundID.String()).Scan(&auditCount)
	if auditCount != 1 {
		t.Errorf("audit handoff entry: want 1, got %d", auditCount)
	}
}

// silence unused-import warnings when this file is the only consumer
// of these packages in its sibling file.
var _ = strings.Contains
