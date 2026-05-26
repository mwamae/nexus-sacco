// Institutional share-purchase GL parity test.
//
// Property under test: an institutional counterparty buys shares and
// the GL post is shape-identical to the individual case — DR <channel
// cash>, CR 3000 Member Share Capital. The R5 share-purchase wiring
// keys on counterparty_id, not member kind; this test pins that.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

func TestInstitutionalSharePurchase_PostsToOutboxLikeIndividual(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	cpID := seedInstitutionalCounterparty(t, env)
	defer cleanupInstitutionalCounterparty(t, env, cpID)

	ctx := context.Background()

	// Flip share-purchase approval toggle off so the handler runs the
	// executor directly + GL post fires inline. Restore on cleanup.
	var origToggle bool
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT approval_share_purchase FROM tenant_operations`).Scan(&origToggle); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tenant_operations SET approval_share_purchase = false`)
		return err
	}); err != nil {
		t.Fatalf("flip share approval: %v", err)
	}
	defer func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE tenant_operations SET approval_share_purchase = $1`, origToggle)
			return err
		})
	}()

	pool := env.Pool
	h := &ShareHandler{
		DB:             pool,
		Tenants:        store.NewTenantStore(pool.Pool),
		Members:        store.NewMemberStore(pool.Pool),
		Counterparties: store.NewCounterpartyStore(pool.Pool),
		Shares:         store.NewShareStore(pool.Pool),
		Approvals:      store.NewApprovalsStore(pool.Pool),
		Notifier:       nil,
		Posting:        &posting.Client{Disabled: false},
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
	r.Post("/v1/share-accounts/by-counterparty/{counterparty_id}/purchase", h.Purchase)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// 10 shares × par 100 = 1000 KES. Channel mpesa → DR 1030.
	body := map[string]any{
		"shares":          10,
		"payment_channel": "mpesa",
		"payment_ref":     "MPS-INST-SHARE-" + env.MarkerSuffix,
	}
	status, raw := httpJSON(t, "POST",
		srv.URL+"/v1/share-accounts/by-counterparty/"+cpID.String()+"/purchase", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /purchase: want 201, got %d. body=%s", status, raw)
	}

	var payload []byte
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.shares.purchase'
			   AND tenant_id = $1
			   AND enqueued_at > now() - interval '1 minute'
			 ORDER BY enqueued_at DESC LIMIT 1
		`, env.TenantID).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
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
		"1030": {Debit: decimal.NewFromInt(1000)},  // M-Pesa
		"3000": {Credit: decimal.NewFromInt(1000)}, // Member Share Capital
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
			t.Errorf("missing outbox line for account %s — institutional purchase may be bypassing GL", code)
		}
	}

	// Use the unused pgx import.
	_ = pgx.ErrNoRows
}
