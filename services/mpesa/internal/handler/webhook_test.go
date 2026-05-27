// Integration tests for the phase-2 webhook handlers.
//
// Covers:
//   - Confirmation round-trip: a sandbox C2B payload persists to
//     mpesa_inbound_events with status='received', resolver attempt
//     stamped on the row, and 200 OK to Safaricom.
//   - Idempotency: replaying the same payload writes one row and
//     returns 200 both times.
//   - Strict validation: when the paybill flips strict_validation on,
//     the validation endpoint returns C2B00012 for an unknown
//     account.
//   - Cross-tenant: a paybill_id from tenant A presented with
//     tenant B's token (or just a wrong token) returns 401 on the
//     confirmation endpoint AND does NOT persist a row.
//   - Resolver wiring: when the bill_ref matches a member_no, the
//     row's resolved_via is 'member_no'; when nothing matches, it's
//     'unallocated' and a wf_instance has been created for the
//     mpesa_unallocated_reconciliation process.

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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

func TestConfirmation_RoundTrip_Unallocated(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	// Seed a paybill with a known token.
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	// Build a router that mounts ONLY the webhook routes — saves us
	// from JWT shimming, which the webhook handlers don't use.
	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()

	body := readFixture(t)
	resp := postC2B(t, srv.URL, "confirmation", paybill.ID, paybill.WebhookToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirmation: want 200, got %d body=%s", resp.StatusCode, readBody(resp))
	}
	var res darajaResultBody
	mustDecode(t, resp, &res)
	if res.ResultCode != 0 {
		t.Errorf("ResultCode=%d body=%v", res.ResultCode, res)
	}

	// Row landed under the right tenant + paybill.
	var row eventRow
	if err := pool.QueryRow(context.Background(), `
		SELECT id, paybill_id, transaction_id, status::text, COALESCE(resolved_via::text,''), workflow_instance_id
		  FROM mpesa_inbound_events WHERE tenant_id = $1 AND transaction_id = 'RKTQDM7W6S'
	`, tenantID).Scan(&row.ID, &row.PaybillID, &row.TxID, &row.Status, &row.ResolvedVia, &row.WorkflowID); err != nil {
		t.Fatalf("read row: %v", err)
	}
	t.Cleanup(func() {
		// wf_actions cascade off wf_instances; wf_levels belong to
		// the shared definition row and MUST NOT be touched by the
		// test or other tenants' instances of the same process_kind
		// will lose their level snapshot.
		_, _ = pool.Exec(context.Background(), `DELETE FROM wf_instances WHERE id = $1`, row.WorkflowID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM mpesa_inbound_events WHERE id = $1`, row.ID)
	})
	if row.Status != "received" {
		t.Errorf("status: want received, got %q", row.Status)
	}
	// The fixture's bill_ref "M-2025-00001" almost certainly doesn't
	// match a real member in the dev DB (we seed via cp_number /
	// tenant fixtures); resolver should land on unallocated AND
	// the handler should have created the mpesa_unallocated wf
	// instance.
	if row.ResolvedVia != "unallocated" {
		t.Errorf("resolved_via: want unallocated, got %q", row.ResolvedVia)
	}
	if row.WorkflowID == nil {
		t.Error("workflow_instance_id should be populated on unallocated rows")
	} else {
		var procKind string
		_ = pool.QueryRow(context.Background(),
			`SELECT process_kind FROM wf_instances WHERE id = $1`, row.WorkflowID).Scan(&procKind)
		if procKind != "mpesa_unallocated_reconciliation" {
			t.Errorf("workflow process_kind: want mpesa_unallocated_reconciliation, got %q", procKind)
		}
	}
}

func TestConfirmation_IdempotentReplay(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()
	body := readFixture(t)

	for i := 0; i < 2; i++ {
		resp := postC2B(t, srv.URL, "confirmation", paybill.ID, paybill.WebhookToken, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: status %d", i, resp.StatusCode)
		}
	}
	var n int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mpesa_inbound_events WHERE tenant_id=$1 AND transaction_id='RKTQDM7W6S'`,
		tenantID,
	).Scan(&n)
	if n != 1 {
		t.Errorf("idempotency: want exactly 1 row, got %d", n)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM wf_instances WHERE subject_kind='mpesa_inbound_event'
				AND subject_id IN (SELECT id FROM mpesa_inbound_events WHERE tenant_id=$1 AND transaction_id='RKTQDM7W6S')`, tenantID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_inbound_events WHERE tenant_id=$1 AND transaction_id='RKTQDM7W6S'`, tenantID)
	})
}

func TestValidation_StrictMode_C2B00012(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	// strict_validation = true. The fixture's bill_ref won't match
	// anything in the dev DB, so the resolver lands unallocated and
	// the validation endpoint MUST return C2B00012.
	paybill := seedTestPaybill(t, dbPool, tenantID, true, false)

	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()
	body := readFixture(t)
	resp := postC2B(t, srv.URL, "validation", paybill.ID, paybill.WebhookToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validation: %d", resp.StatusCode)
	}
	var res darajaResultBody
	mustDecode(t, resp, &res)
	if res.ResultCode == 0 {
		t.Errorf("strict-validation unknown account: want non-zero ResultCode, got 0 (%+v)", res)
	}
	if !strings.Contains(res.ResultDesc, "C2B00012") {
		t.Errorf("ResultDesc: want C2B00012, got %q", res.ResultDesc)
	}
}

func TestValidation_DefaultMode_Accepts(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()
	resp := postC2B(t, srv.URL, "validation", paybill.ID, paybill.WebhookToken, readFixture(t))
	var res darajaResultBody
	mustDecode(t, resp, &res)
	if res.ResultCode != 0 {
		t.Errorf("default validation: want ResultCode=0, got %d", res.ResultCode)
	}
}

func TestConfirmation_WrongToken_401_NoRow(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}
	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)

	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()
	resp := postC2B(t, srv.URL, "confirmation", paybill.ID, "wrong-token", readFixture(t))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: want 401, got %d", resp.StatusCode)
	}
	var n int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mpesa_inbound_events WHERE tenant_id=$1 AND transaction_id='RKTQDM7W6S'`,
		tenantID,
	).Scan(&n)
	if n != 0 {
		t.Errorf("wrong token must not persist any row, got %d", n)
	}
}

func TestConfirmation_CrossTenant_PaybillFromB_TokenFromA(t *testing.T) {
	pool, tenantA, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	var tenantB uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE id <> $1 LIMIT 1`, tenantA,
	).Scan(&tenantB); err != nil {
		t.Skipf("only one tenant; cannot exercise cross-tenant: %v", err)
	}
	pbA := seedTestPaybill(t, dbPool, tenantA, false, false)
	pbB := seedTestPaybill(t, dbPool, tenantB, false, false)

	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()

	// Hit B's paybill with A's token → 401, no row landed.
	resp := postC2B(t, srv.URL, "confirmation", pbB.ID, pbA.WebhookToken, readFixture(t))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("paybill from B + token from A: want 401, got %d", resp.StatusCode)
	}
	var n int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mpesa_inbound_events WHERE transaction_id='RKTQDM7W6S' AND tenant_id IN ($1,$2)`,
		tenantA, tenantB,
	).Scan(&n)
	if n != 0 {
		t.Errorf("no row should be persisted on cross-tenant token mismatch, got %d", n)
	}
}

func TestConfirmation_ResolvedViaMemberNo(t *testing.T) {
	pool, _, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	// Pick whatever tenant has at least one member — the default
	// openTestPool tenant may be empty in dev DBs that only seeded
	// fixtures into one of several tenants. Skip if no tenant has
	// any member at all (totally fresh DB).
	var tenantID uuid.UUID
	var memberNo string
	if err := pool.QueryRow(context.Background(), `
		SELECT tenant_id, member_no FROM members LIMIT 1
	`).Scan(&tenantID, &memberNo); err != nil {
		t.Skipf("no member in any tenant; skip: %v", err)
	}

	paybill := seedTestPaybill(t, dbPool, tenantID, false, false)
	srv := newWebhookSrv(t, dbPool)
	defer srv.Close()

	rawFixture := readFixture(t)
	// Swap the bill_ref + give the row a fresh transaction id so we
	// don't collide with the other tests in this file.
	freshTx := "RKTRM" + fmt.Sprint(time.Now().UnixNano()%10000000)
	body := bytes.NewBuffer(nil)
	rep := strings.NewReplacer(
		`"BillRefNumber": "M-2025-00001"`, `"BillRefNumber": "`+memberNo+`"`,
		`"TransID": "RKTQDM7W6S"`, `"TransID": "`+freshTx+`"`,
	)
	body.WriteString(rep.Replace(string(rawFixture)))

	resp := postC2B(t, srv.URL, "confirmation", paybill.ID, paybill.WebhookToken, body.Bytes())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirmation: %d %s", resp.StatusCode, readBody(resp))
	}
	var via string
	if err := pool.QueryRow(context.Background(),
		`SELECT resolved_via::text FROM mpesa_inbound_events WHERE transaction_id = $1`, freshTx,
	).Scan(&via); err != nil {
		t.Fatalf("read resolved_via: %v", err)
	}
	if via != "member_no" {
		t.Errorf("resolved_via: want member_no, got %q", via)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM mpesa_inbound_events WHERE transaction_id = $1`, freshTx)
	})
}

// ─── helpers ───

type darajaResultBody struct {
	ResultCode int    `json:"ResultCode"`
	ResultDesc string `json:"ResultDesc"`
}

type eventRow struct {
	ID          uuid.UUID
	PaybillID   *uuid.UUID
	TxID        string
	Status      string
	ResolvedVia string
	WorkflowID  *uuid.UUID
}

type seededPaybill struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	WebhookToken string
}

// seedTestPaybill creates a paybill with a unique shortcode + token.
// strict + msisdnFallback flags wire the corresponding columns.
func seedTestPaybill(t *testing.T, dbPool *db.Pool, tenantID uuid.UUID, strict, msisdnFallback bool) seededPaybill {
	t.Helper()
	uniq := fmt.Sprintf("PB%07d", time.Now().UnixNano()%10000000)
	var id uuid.UUID
	var token string
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_paybills
				(tenant_id, label, shortcode, purpose, scope, environment,
				 strict_validation, allow_msisdn_fallback, webhook_token)
			VALUES ($1, 'phase2-test', $2, 'collection', '{member_deposits}', 'sandbox',
			        $3, $4, encode(gen_random_bytes(24),'hex'))
			RETURNING id, webhook_token
		`, tenantID, uniq, strict, msisdnFallback).Scan(&id, &token)
	})
	if err != nil {
		t.Fatalf("seed paybill: %v", err)
	}
	t.Cleanup(func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybill_credentials WHERE paybill_id = $1`, id)
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_inbound_events WHERE paybill_id = $1`, id)
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybills WHERE id = $1`, id)
			return nil
		})
	})
	return seededPaybill{ID: id, TenantID: tenantID, WebhookToken: token}
}

// newWebhookSrv mounts JUST the webhook routes — no JWT middleware,
// matching the production route group. The IP allow-list is
// configured permissive (empty list → accept all).
func newWebhookSrv(t *testing.T, dbPool *db.Pool) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	allow, err := middleware.NewIPAllowList("", logger)
	if err != nil {
		t.Fatalf("allow list: %v", err)
	}
	h := &WebhookHandler{
		DB:             dbPool,
		Paybills:       store.NewPaybillStore(dbPool.Pool),
		InboundEvents:  store.NewInboundEventStore(dbPool.Pool),
		Resolver:       store.NewResolverLookups(dbPool.Pool),
		Audit:          store.NewAuditStore(dbPool.Pool),
		WorkflowClient: workflowclient.New(),
		Logger:         logger,
	}
	r := chi.NewRouter()
	r.Route("/v1/c2b", func(r chi.Router) {
		r.Use(allow.Middleware)
		r.Post("/{paybill_id}/validation", h.Validation)
		r.Post("/{paybill_id}/confirmation", h.Confirmation)
	})
	return httptest.NewServer(r)
}

func postC2B(t *testing.T, base, hook string, paybillID uuid.UUID, token string, body []byte) *http.Response {
	t.Helper()
	url := fmt.Sprintf("%s/v1/c2b/%s/%s?token=%s", base, paybillID, hook, token)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/safaricom_c2b_confirmation.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func mustDecode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}
