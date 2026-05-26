// Integration tests for the paybill admin HTTP surface.
//
// Covers:
//   - POST /v1/mpesa/paybills creates a row scoped to the calling
//     tenant (RLS visible to the caller's WithTenantTx context).
//   - POST /v1/mpesa/paybills/{id}/credentials encrypts before write
//     — `SELECT * FROM mpesa_paybill_credentials` from a privileged
//     connection finds bytes, not the plaintext.
//   - GET /v1/mpesa/paybills/{id}/test-auth happy path with a stubbed
//     Daraja authenticator (returns {ok:true}).
//   - test-auth with bogus credentials (mock Daraja returns 401-ish
//     error) cleanly returns {ok:false, error: "..."}.
//   - RLS regression: a paybill seeded for tenant B is invisible to
//     the test-auth handler running under tenant A.
//
// All writes happen against the dev DATABASE_URL; rows are cleaned
// up with t.Cleanup. The handler stack uses an in-process httptest
// server and the same chi/middleware shim the production binary
// would build.

package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/mpesa/internal/auth"
	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/daraja"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
)

// fakeAuthenticator returns whatever the test asked for; lets us
// exercise the success + failure branches without standing up an
// httptest server for every case.
type fakeAuthenticator struct {
	token daraja.Token
	err   error
	seen  int
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, _, _ string) (daraja.Token, error) {
	f.seen++
	return f.token, f.err
}

func TestPaybill_CreateAndCredentialsAndTestAuth(t *testing.T) {
	pool, tenantA, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}
	actor := uuid.New()

	sealer := newTestSealer(t, "kms-test-001")
	fake := &fakeAuthenticator{token: daraja.Token{
		AccessToken: "tok-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
	}}

	h := buildHandler(dbPool, sealer, fake)
	srv := httptest.NewServer(buildRouter(tenantA, actor, h))
	defer srv.Close()

	// 1. Create paybill. Shortcode is uniquified per run so a test
	// that gets aborted mid-flight (e.g. by a CI cancellation)
	// doesn't leave an orphan row that the (tenant,shortcode,env)
	// unique constraint would then trip on next time.
	shortcode := fmt.Sprintf("99%05d", time.Now().UnixNano()%100000)
	createBody := fmt.Sprintf(`{
		"label": "Sandbox Test Paybill",
		"shortcode": %q,
		"purpose": "collection",
		"scope": ["member_deposits","loan_repayments"],
		"environment": "sandbox"
	}`, shortcode)
	resp := postJSON(t, srv.URL+"/v1/mpesa/paybills", createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create paybill: status %d body=%s", resp.StatusCode, readBody(resp))
	}
	created := decodeData(t, resp)
	paybillID := uuid.MustParse(created["id"].(string))
	t.Cleanup(func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybill_credentials WHERE paybill_id = $1`, paybillID)
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybills WHERE id = $1`, paybillID)
			return nil
		})
	})

	// 2. Put credentials (consumer_key + consumer_secret).
	for _, kv := range []struct{ Kind, Plain string }{
		{"consumer_key", "sandbox-key-abc"},
		{"consumer_secret", "sandbox-secret-xyz"},
	} {
		body := fmt.Sprintf(`{"kind":"%s","plaintext":"%s"}`, kv.Kind, kv.Plain)
		r := postJSON(t, fmt.Sprintf("%s/v1/mpesa/paybills/%s/credentials", srv.URL, paybillID), body)
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("put %s: %d body=%s", kv.Kind, r.StatusCode, readBody(r))
		}
	}

	// Ciphertext on disk is opaque — direct privileged read must NOT
	// expose the plaintext. We bypass RLS by using a fresh
	// superuser-pool connection (the test runs as the same role that
	// applied the migration), but the credential row's grant model
	// will refuse a SELECT for nexus_app — that's exercised
	// separately in TestCredentialsTable_NoSelectForAppRole.
	var rawCT []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT ciphertext FROM mpesa_paybill_credentials
		WHERE paybill_id = $1 AND kind = 'consumer_key' AND tenant_id = $2
	`, paybillID, tenantA).Scan(&rawCT); err != nil {
		t.Fatalf("read raw ciphertext: %v", err)
	}
	if bytes.Contains(rawCT, []byte("sandbox-key-abc")) {
		t.Errorf("plaintext leaked into ciphertext bytes")
	}
	// Sanity: round-trip via the sealer.
	pt, err := sealer.Decrypt(rawCT)
	if err != nil {
		t.Fatalf("sealer cannot decrypt its own output: %v", err)
	}
	if string(pt) != "sandbox-key-abc" {
		t.Errorf("round-trip mismatch: got %q", pt)
	}

	// 3. test-auth happy path.
	r := httpGet(t, srv.URL+"/v1/mpesa/paybills/"+paybillID.String()+"/test-auth")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("test-auth: %d", r.StatusCode)
	}
	out := decodeData(t, r)
	if ok, _ := out["ok"].(bool); !ok {
		t.Errorf("test-auth happy: want ok=true, got %v (err=%v)", out["ok"], out["error"])
	}
	if fake.seen != 1 {
		t.Errorf("Daraja should have been hit exactly once, was %d", fake.seen)
	}

	// 4. test-auth with bogus creds — switch the fake to error mode.
	fake.token = daraja.Token{}
	fake.err = errors.New("daraja oauth: status 401 body bad credentials")
	r = httpGet(t, srv.URL+"/v1/mpesa/paybills/"+paybillID.String()+"/test-auth")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("bogus test-auth status: want 200, got %d", r.StatusCode)
	}
	out = decodeData(t, r)
	if ok, _ := out["ok"].(bool); ok {
		t.Errorf("bogus test-auth: want ok=false")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "401") {
		t.Errorf("bogus test-auth error: want 401 mention, got %q", msg)
	}
}

func TestCredentialsTable_NoSelectForAppRole(t *testing.T) {
	pool, tenantA, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	// Seed a paybill + credential as the privileged role.
	paybillID, cleanupPB := seedPaybill(t, dbPool, tenantA)
	defer cleanupPB()
	sealer := newTestSealer(t, "kms-test-001")
	ct, err := sealer.Encrypt([]byte("nope"))
	if err != nil {
		t.Fatal(err)
	}
	_ = dbPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO mpesa_paybill_credentials
				(tenant_id, paybill_id, kind, key_id, ciphertext)
			VALUES ($1, $2, 'consumer_key', $3, $4)
		`, tenantA, paybillID, "kms-test-001", ct)
		return err
	})
	t.Cleanup(func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybill_credentials WHERE paybill_id = $1`, paybillID)
			return nil
		})
	})

	// Open a pool that does SET ROLE nexus_app on every connection
	// (DB_SKIP_SET_ROLE unset). That role must NOT be able to
	// SELECT from mpesa_paybill_credentials directly.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	_ = os.Unsetenv("DB_SKIP_SET_ROLE")
	appPool, err := db.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	defer appPool.Close()

	err = appPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
		var c int
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM mpesa_paybill_credentials`).Scan(&c)
	})
	if err == nil {
		t.Fatal("expected permission denied on SELECT for nexus_app, but query succeeded")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("want permission-denied error, got: %v", err)
	}

	// The SECURITY DEFINER function MUST work for the same role.
	err = appPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
		var keyID string
		var ciphertext []byte
		return tx.QueryRow(context.Background(),
			`SELECT key_id, ciphertext FROM mpesa_credentials_read($1, 'consumer_key')`,
			paybillID).Scan(&keyID, &ciphertext)
	})
	if err != nil {
		t.Errorf("mpesa_credentials_read should be callable by nexus_app: %v", err)
	}
}

func TestPaybill_RLSCrossTenant(t *testing.T) {
	pool, tenantA, cleanup := openTestPool(t)
	defer cleanup()
	dbPool := &db.Pool{Pool: pool}

	var tenantB uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE id <> $1 LIMIT 1`, tenantA,
	).Scan(&tenantB); err != nil {
		t.Skipf("only one tenant; cannot exercise cross-tenant RLS: %v", err)
	}

	// Seed a paybill for tenant B.
	paybillID, cleanupPB := seedPaybill(t, dbPool, tenantB)
	defer cleanupPB()

	// Hit /test-auth as tenant A using B's paybill id — should be 404
	// (RLS hides the row) NOT 200 with credential decryption errors.
	sealer := newTestSealer(t, "kms-test-001")
	fake := &fakeAuthenticator{}
	h := buildHandler(dbPool, sealer, fake)
	srv := httptest.NewServer(buildRouter(tenantA, uuid.New(), h))
	defer srv.Close()

	r := httpGet(t, srv.URL+"/v1/mpesa/paybills/"+paybillID.String()+"/test-auth")
	// Soft-fail path: handler returns 200 with ok=false when the
	// paybill is invisible (so the UI can render "Configure
	// credentials first" rather than a hard 404). Either shape is
	// acceptable as long as `ok` is false — the regression to guard
	// against is "ok:true" for the wrong tenant.
	if r.StatusCode == http.StatusOK {
		out := decodeData(t, r)
		if ok, _ := out["ok"].(bool); ok {
			t.Errorf("RLS leak: tenant A reported ok=true for tenant B's paybill")
		}
	} else if r.StatusCode != http.StatusNotFound {
		t.Errorf("unexpected status for cross-tenant lookup: %d", r.StatusCode)
	}

	if fake.seen != 0 {
		t.Errorf("Daraja must NOT be hit for an invisible paybill (called %d times)", fake.seen)
	}
}

// ─── helpers ───

func openTestPool(t *testing.T) (*pgxpool.Pool, uuid.UUID, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	// Migration runner role; we need to be able to enumerate the
	// credentials table for the leak assertion in test #1.
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants LIMIT 1`).Scan(&tenantID); err != nil {
		pool.Close()
		t.Skipf("no tenant available: %v", err)
	}
	return pool, tenantID, func() { pool.Close() }
}

func newTestSealer(t *testing.T, id string) *crypto.Sealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	s, err := crypto.NewSealer(id, key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seedPaybill(t *testing.T, dbPool *db.Pool, tenantID uuid.UUID) (uuid.UUID, func()) {
	t.Helper()
	var id uuid.UUID
	uniq := hex.EncodeToString([]byte{byte(time.Now().UnixNano() & 0xff)})
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_paybills (tenant_id, label, shortcode, purpose, scope, environment)
			VALUES ($1, $2, $3, 'collection', '{member_deposits}', 'sandbox')
			RETURNING id
		`, tenantID, "Seed Paybill "+uniq, "TEST-"+uniq).Scan(&id)
	})
	if err != nil {
		t.Fatalf("seed paybill: %v", err)
	}
	cleanup := func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybill_credentials WHERE paybill_id = $1`, id)
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybills WHERE id = $1`, id)
			return nil
		})
	}
	return id, cleanup
}

func buildHandler(pool *db.Pool, sealer *crypto.Sealer, fake *fakeAuthenticator) *PaybillHandler {
	return &PaybillHandler{
		DB:          pool,
		Paybills:    store.NewPaybillStore(pool.Pool),
		Credentials: store.NewCredentialStore(pool.Pool),
		Sealer:      sealer,
		Daraja:      fake,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func buildRouter(tenantID, actorID uuid.UUID, h *PaybillHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := middleware.WithTenant(r.Context(), tenantID, "test")
			ctx = middleware.WithClaims(ctx, &auth.AccessClaims{UserID: actorID.String()})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Post("/v1/mpesa/paybills", h.Create)
	r.Post("/v1/mpesa/paybills/{id}/credentials", h.PutCredential)
	r.Get("/v1/mpesa/paybills/{id}/test-auth", h.TestAuth)
	return r
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func httpGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeData(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return env.Data
}

func readBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
