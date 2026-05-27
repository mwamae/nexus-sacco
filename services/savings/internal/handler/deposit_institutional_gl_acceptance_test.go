// Institutional GL parity acceptance test.
//
// Property under test: an institutional counterparty (kind='chama')
// opens a deposit account + posts an opening deposit, and the
// resulting GL post is byte-identical in SHAPE to the individual
// case — DR <channel cash>, CR <product liability code> via the
// posting outbox.
//
// Prevents the regression where someone tightens the deposit handler
// to filter members.kind = 'individual' or similar.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

func seedInstitutionalCounterparty(t *testing.T, env *testEnv) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var cpID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// cp_kind_payload_institution constraint: non-individual rows
		// must populate the institution jsonb (and leave individual NULL).
		if err := tx.QueryRow(ctx, `
			INSERT INTO counterparties (
			  tenant_id, cp_number, kind, display_name, status, kyc_state, risk_band,
			  institution
			) VALUES (
			  $1, $2, 'chama', $3, 'active', 'verified', 'medium',
			  '{"registration_no":"TEST-CHAMA"}'::jsonb
			) RETURNING id
		`, env.TenantID, "CP-INST-"+env.MarkerSuffix, "Test Chama "+env.MarkerSuffix).Scan(&cpID); err != nil {
			return fmt.Errorf("insert counterparty: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO org_members (tenant_id, org_no, kind, registered_name, status, counterparty_id)
			VALUES ($1, $2, 'chama', $3, 'active', $4)
		`, env.TenantID, "ORG-"+env.MarkerSuffix, "Test Chama "+env.MarkerSuffix, cpID); err != nil {
			return fmt.Errorf("insert org_members: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed institutional counterparty: %v", err)
	}
	return cpID
}

func cleanupInstitutionalCounterparty(t *testing.T, env *testEnv, cpID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		exec := func(label, sql string, args ...any) {
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				t.Logf("cleanup %s: %v", label, err)
			}
		}
		exec("posting_outbox",
			`DELETE FROM posting_outbox WHERE payload->>'narration' LIKE '%' || $1 || '%'`,
			env.MarkerSuffix)
		exec("deposit_transactions",
			`DELETE FROM deposit_transactions WHERE account_id IN (SELECT id FROM deposit_accounts WHERE counterparty_id = $1)`, cpID)
		exec("share_certificates",
			`DELETE FROM share_certificates WHERE counterparty_id = $1`, cpID)
		exec("share_transactions",
			`DELETE FROM share_transactions WHERE counterparty_id = $1`, cpID)
		exec("share_accounts",
			`DELETE FROM share_accounts WHERE counterparty_id = $1`, cpID)
		exec("deposit_accounts",
			`DELETE FROM deposit_accounts WHERE counterparty_id = $1`, cpID)
		exec("org_members",
			`DELETE FROM org_members WHERE counterparty_id = $1`, cpID)
		exec("counterparties",
			`DELETE FROM counterparties WHERE id = $1`, cpID)
		return nil
	})
}

func TestInstitutionalDepositOpen_PostsToOutboxLikeIndividual(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	cpID := seedInstitutionalCounterparty(t, env)
	defer cleanupInstitutionalCounterparty(t, env, cpID)

	ctx := context.Background()

	// Flip the deposit-approval toggle off so /deposit runs the
	// executor + GL post inline rather than queueing for approval.
	var origDepToggle bool
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT approval_deposit FROM tenant_operations`).Scan(&origDepToggle); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tenant_operations SET approval_deposit = false`)
		return err
	}); err != nil {
		t.Fatalf("flip deposit approval: %v", err)
	}
	defer func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE tenant_operations SET approval_deposit = $1`, origDepToggle)
			return err
		})
	}()

	var productID uuid.UUID
	var productType string
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// Pick an ordinary FOSA product → CR 2000 on the GL post.
		return tx.QueryRow(ctx, `
			SELECT id, product_type::text FROM deposit_products
			 WHERE tenant_id = $1 AND is_active AND segment='fosa' AND product_type='ordinary'
			 LIMIT 1
		`, env.TenantID).Scan(&productID, &productType)
	}); err != nil {
		t.Skipf("no ordinary FOSA product on tujenge: %v", err)
	}

	// Mount the DepositHandler directly with a non-Disabled posting
	// client so PostTx writes the outbox row.
	pool := env.Pool
	h := &DepositHandler{
		DB:             pool,
		Tenants:        store.NewTenantStore(pool.Pool),
		Members:        store.NewMemberStore(pool.Pool),
		Counterparties: store.NewCounterpartyStore(pool.Pool),
		Products:       store.NewDepositProductStore(pool.Pool),
		Deposits:       store.NewDepositStore(pool.Pool),
		Approvals:      store.NewApprovalsStore(pool.Pool),
		Posting:        &posting.Client{DryRun: false},
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
			c := rq.Context()
			c = middleware.WithTenant(c, env.TenantID, "tujenge")
			c = middleware.WithClaims(c, &auth.AccessClaims{
				TenantID: env.TenantID.String(), TenantSlug: "tujenge",
				UserID: env.UserID.String(), IsPlatformAdmin: true,
			})
			next.ServeHTTP(w, rq.WithContext(c))
		})
	})
	r.Post("/v1/deposit-accounts", h.Open)
	r.Post("/v1/deposit-accounts/{account_id}/deposit", h.Deposit)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Step 1: open the account with a 500 mpesa opening deposit.
	// Open now routes through the shared executeDepositInlineTx,
	// which writes the receipt + queues the GL outbox row — same
	// path the Deposit handler uses. (This test pre-dates that
	// fix; it used to open with cash-channel and then a separate
	// deposit. The cash hard-block introduced later forbids cash
	// inline, and Open now exercises the full pipeline so the
	// "open then separate deposit" two-step is no longer needed
	// here.)
	openBody := map[string]any{
		"counterparty_id": cpID,
		"product_id":      productID,
		"opening_deposit": "500",
		"opening_channel": "mpesa",
	}
	openStatus, openRaw := httpJSON(t, "POST", srv.URL+"/v1/deposit-accounts", openBody)
	if openStatus != http.StatusCreated {
		t.Fatalf("POST /v1/deposit-accounts (open): want 201, got %d. body=%s", openStatus, openRaw)
	}
	var openResp struct {
		Data struct {
			Account struct {
				ID uuid.UUID `json:"id"`
			} `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(openRaw, &openResp); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	acctID := openResp.Data.Account.ID

	// Step 2: post a deposit — this hits the GL outbox.
	depBody := map[string]any{
		"amount":      "50000",
		"channel":     "mpesa",
		"channel_ref": "MPS-INST-" + env.MarkerSuffix,
	}
	status, raw := httpJSON(t, "POST",
		srv.URL+"/v1/deposit-accounts/"+acctID.String()+"/deposit", depBody)
	if status != http.StatusCreated {
		t.Fatalf("POST /deposit: want 201, got %d. body=%s", status, raw)
	}

	// Verify the outbox payload — same shape as the individual case.
	var rowCount int
	var payload []byte
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.deposits'
			   AND (payload->>'narration') LIKE '%MPS-INST-' || $1 || '%'
		`, env.MarkerSuffix).Scan(&rowCount); err != nil {
			return err
		}
		// The opening deposit's narration includes the account_no, not
		// the channel_ref. Fall back to filtering on tenant + counterparty
		// via the recent posting_outbox rows seeded by this test.
		if rowCount == 0 {
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM posting_outbox
				 WHERE payload->>'source_module' = 'savings.deposits'
				   AND tenant_id = $1
				   AND enqueued_at > now() - interval '1 minute'
			`, env.TenantID).Scan(&rowCount); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.deposits'
			   AND tenant_id = $1
			   AND enqueued_at > now() - interval '1 minute'
			 ORDER BY enqueued_at DESC LIMIT 1
		`, env.TenantID).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if rowCount == 0 {
		t.Fatalf("opening deposit did NOT produce a posting_outbox row — institutional accounts may be bypassing the GL post path")
	}

	var got struct {
		Lines []struct {
			AccountCode string `json:"account_code"`
			Debit       string `json:"debit"`
			Credit      string `json:"credit"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	want := map[string]struct{ Debit, Credit decimal.Decimal }{
		"1030": {Debit: decimal.NewFromInt(50000)},  // M-Pesa float
		"2000": {Credit: decimal.NewFromInt(50000)}, // Ordinary Savings Deposits
	}
	for code, exp := range want {
		var found bool
		for _, l := range got.Lines {
			if l.AccountCode == code {
				found = true
				d, _ := decimal.NewFromString(l.Debit)
				c, _ := decimal.NewFromString(l.Credit)
				if !exp.Debit.Equal(d) {
					t.Errorf("acct %s debit: want %s, got %s", code, exp.Debit.StringFixed(2), d.StringFixed(2))
				}
				if !exp.Credit.Equal(c) {
					t.Errorf("acct %s credit: want %s, got %s", code, exp.Credit.StringFixed(2), c.StringFixed(2))
				}
			}
		}
		if !found {
			t.Errorf("missing outbox line for account %s", code)
		}
	}
}
