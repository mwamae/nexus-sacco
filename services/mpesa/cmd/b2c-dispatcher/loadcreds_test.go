// Integration tests for the dispatcher's credential load + decrypt
// path. Hits the real DB so the SECURITY DEFINER read function is
// exercised end-to-end; skipped when DATABASE_URL is unset.
//
// Two cases:
//   - happy path: credentials sealed under the dispatcher's active
//     key decrypt cleanly + come back as plaintext
//   - unknown key: credentials sealed under a DIFFERENT key surface
//     a "credential sealed with unknown key" error so an operator
//     knows to re-seal via Settings → Rotate creds

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
)

func TestLoadCreds_HappyPath_DecryptsAllFour(t *testing.T) {
	dbPool, tenantID := openDispatcherTestPool(t)
	sealer := newTestSealer(t, "kms-test-active")

	paybillID, cleanup := seedDispatcherPaybill(t, dbPool, tenantID)
	t.Cleanup(cleanup)

	want := map[domain.CredentialKind]string{
		domain.CredConsumerKey:       "ck-plaintext-12345",
		domain.CredConsumerSecret:    "cs-plaintext-supersecret",
		domain.CredInitiatorName:     "B2CInitiator",
		domain.CredInitiatorPassword: "initiator-password-plaintext",
	}
	seedCredentials(t, dbPool, sealer, tenantID, paybillID, want)

	d := &dispatcher{
		pool:      dbPool,
		credStore: store.NewCredentialStore(dbPool.Pool),
		sealer:    sealer,
		logger:    slog.New(slog.NewTextHandler(io_discard{}, nil)),
	}

	var ck, cs, in, ip string
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		var loadErr error
		ck, cs, in, ip, loadErr = d.loadCreds(context.Background(), tx, paybillID)
		return loadErr
	})
	if err != nil {
		t.Fatalf("loadCreds: %v", err)
	}
	if ck != want[domain.CredConsumerKey] {
		t.Errorf("consumerKey: want %q got %q", want[domain.CredConsumerKey], ck)
	}
	if cs != want[domain.CredConsumerSecret] {
		t.Errorf("consumerSecret: want %q got %q", want[domain.CredConsumerSecret], cs)
	}
	if in != want[domain.CredInitiatorName] {
		t.Errorf("initiatorName: want %q got %q", want[domain.CredInitiatorName], in)
	}
	if ip != want[domain.CredInitiatorPassword] {
		t.Errorf("initiatorPassword: want %q got %q", want[domain.CredInitiatorPassword], ip)
	}
}

func TestLoadCreds_UnknownKeyID_ReturnsClearError(t *testing.T) {
	dbPool, tenantID := openDispatcherTestPool(t)
	// Sealer used to ENCRYPT the rows — call its key "kms-old-001".
	oldSealer := newTestSealer(t, "kms-old-001")
	// Dispatcher's sealer carries a DIFFERENT key id. Decrypt should
	// refuse before it ever looks at the payload.
	currentSealer := newTestSealer(t, "kms-new-002")

	paybillID, cleanup := seedDispatcherPaybill(t, dbPool, tenantID)
	t.Cleanup(cleanup)

	seedCredentials(t, dbPool, oldSealer, tenantID, paybillID, map[domain.CredentialKind]string{
		domain.CredConsumerKey:       "ck",
		domain.CredConsumerSecret:    "cs",
		domain.CredInitiatorName:     "init",
		domain.CredInitiatorPassword: "pw",
	})

	d := &dispatcher{
		pool:      dbPool,
		credStore: store.NewCredentialStore(dbPool.Pool),
		sealer:    currentSealer,
		logger:    slog.New(slog.NewTextHandler(io_discard{}, nil)),
	}

	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		_, _, _, _, e := d.loadCreds(context.Background(), tx, paybillID)
		return e
	})
	if err == nil {
		t.Fatal("loadCreds: want error for unknown key, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown key") {
		t.Errorf("error should mention 'unknown key': %v", err)
	}
	if !strings.Contains(msg, "kms-old-001") {
		t.Errorf("error should name the stamped key id %q: %v", "kms-old-001", err)
	}
	if !strings.Contains(msg, "Rotate creds") {
		t.Errorf("error should hint at the re-seal action: %v", err)
	}
}

// ─── helpers ───

func openDispatcherTestPool(t *testing.T) (*db.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	// Bypass nexus_app role so the test can write to mpesa_paybill_credentials
	// without the production handler's wrapping logic.
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	pool, err := db.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM tenants LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tenant available: %v", err)
	}
	return pool, tenantID
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

func seedDispatcherPaybill(t *testing.T, dbPool *db.Pool, tenantID uuid.UUID) (uuid.UUID, func()) {
	t.Helper()
	uniq := hex.EncodeToString([]byte{byte(time.Now().UnixNano() & 0xff)})
	var id uuid.UUID
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO mpesa_paybills (tenant_id, label, shortcode, purpose, scope, environment, webhook_token)
			VALUES ($1, $2, $3, 'disbursement', '{loan_disbursement}', 'sandbox',
			        encode(gen_random_bytes(24),'hex'))
			RETURNING id
		`, tenantID, "Dispatcher Test "+uniq, "DISP-"+uniq).Scan(&id)
	})
	if err != nil {
		t.Fatalf("seed paybill: %v", err)
	}
	return id, func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybill_credentials WHERE paybill_id = $1`, id)
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM mpesa_paybills WHERE id = $1`, id)
			return nil
		})
	}
}

func seedCredentials(
	t *testing.T,
	dbPool *db.Pool,
	sealer *crypto.Sealer,
	tenantID, paybillID uuid.UUID,
	values map[domain.CredentialKind]string,
) {
	t.Helper()
	credStore := store.NewCredentialStore(dbPool.Pool)
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		for kind, plain := range values {
			ct, err := sealer.Encrypt([]byte(plain))
			if err != nil {
				return err
			}
			if _, err := credStore.PutTx(context.Background(), tx, store.PutCredentialInput{
				TenantID:   tenantID,
				PaybillID:  paybillID,
				Kind:       kind,
				KeyID:      sealer.ActiveID,
				Ciphertext: ct,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
}

// io_discard avoids dragging io/ioutil + io into the test imports
// just for the slog handler.
type io_discard struct{}

func (io_discard) Write(p []byte) (int, error) { return len(p), nil }

// Suppress unused-import warnings; errors is used inside loadCreds
// (transitively) but vet complains when the test body doesn't refer
// to it directly.
var _ = errors.New
var _ sync.Mutex
