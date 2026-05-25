// End-to-end acceptance test for the Interest Run posting → GL wiring.
//
// Property under test: a 3-member interest run with mixed payout
// methods produces exactly one batched journal entry on the outbox,
// with a balanced DR/CR aggregation and a stamp back onto
// interest_runs.journal_entry_id.
//
// The test uses a non-Disabled posting.Client so PostTx writes a
// real posting_outbox row (PostTx is a no-op when Disabled — that's
// the dev-mode short-circuit). The dispatcher is NOT exercised
// here; whether the outbox row reaches journal_entries is the
// dispatcher's + accounting service's contract, with its own tests.

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

type interestScenario struct {
	RunID         uuid.UUID
	CPIDs         [3]uuid.UUID
	DepAcctIDs    [3]uuid.UUID // one per CP — interest_run_lines unique on (run_id, account_id)
	ProductID     uuid.UUID
	LiabilityCode string
}

// seedInterestRun stages an approved interest_run with three lines:
//   member 0: credit_savings — gross 1000, wht 50, net 950 → CR 2000
//   member 1: buy_shares     — gross 2000, wht 100, net 1900 → CR 3000 (par=100, 19 shares, residual 0)
//   member 2: external       — gross 500, wht 25, net 475 → CR 2230
// Totals: DR 5000 = 3500, CR 2200 = 175, CR 2000 = 950, CR 3000 = 1900, CR 2230 = 475 → balances.
func seedInterestRun(t *testing.T, env *testEnv) interestScenario {
	t.Helper()
	ctx := context.Background()

	var sc interestScenario
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// Pick 3 active counterparties.
		rows, err := tx.Query(ctx, `
			SELECT id FROM counterparties
			 WHERE tenant_id = $1 AND status = 'active'
			 ORDER BY id LIMIT 3
		`, env.TenantID)
		if err != nil {
			return fmt.Errorf("pick counterparties: %w", err)
		}
		i := 0
		for rows.Next() {
			if err := rows.Scan(&sc.CPIDs[i]); err != nil {
				rows.Close()
				return err
			}
			i++
		}
		rows.Close()
		if i < 3 {
			return fmt.Errorf("need 3 counterparties, got %d", i)
		}

		// Pick an interest-eligible FOSA ordinary product. Liability
		// code for fosa+ordinary is 2000 (depositLiabilityCode).
		if err := tx.QueryRow(ctx, `
			SELECT id FROM deposit_products
			 WHERE tenant_id = $1 AND is_active AND interest_eligible
			   AND segment = 'fosa' AND product_type = 'ordinary'
			 LIMIT 1
		`, env.TenantID).Scan(&sc.ProductID); err != nil {
			return fmt.Errorf("find ordinary product: %w", err)
		}
		sc.LiabilityCode = "2000"

		// Fresh deposit account per CP. interest_run_lines is unique on
		// (run_id, account_id), so even the buy_shares + external
		// lines (which don't credit the account) need a distinct
		// account_id. Marker-suffixed → testenv cleanup catches them.
		for idx, cp := range sc.CPIDs {
			if err := tx.QueryRow(ctx, `
				INSERT INTO deposit_accounts (
				  tenant_id, counterparty_id, product_id, account_no,
				  status, current_balance, available_balance, opened_at, created_by
				) VALUES ($1, $2, $3, $4, 'active', 0, 0, now(), $5)
				RETURNING id
			`, env.TenantID, cp, sc.ProductID,
				fmt.Sprintf("DPA-IR-%s-%d", env.MarkerSuffix, idx),
				env.UserID).Scan(&sc.DepAcctIDs[idx]); err != nil {
				return fmt.Errorf("seed deposit account %d: %w", idx, err)
			}
		}

		// Create the run in 'approved' state — we don't walk the full
		// draft → compute → submit → approve lifecycle here.
		if err := tx.QueryRow(ctx, `
			INSERT INTO interest_runs (
			  tenant_id, run_no, financial_year_label,
			  fy_start, fy_end, status,
			  agm_rate_pct, agm_resolution_ref, agm_resolution_date,
			  wht_rate_pct, product_ids, created_by, approved_at, approved_by
			) VALUES (
			  $1, $2, 'FY-IR-'||$3,
			  DATE '2025-01-01', DATE '2025-12-31', 'approved',
			  10.0, 'AGM-IR-'||$3, DATE '2026-01-15',
			  5.0, ARRAY[$4::uuid], $5, now(), $5
			) RETURNING id
		`, env.TenantID, "IR-"+env.MarkerSuffix, env.MarkerSuffix,
			sc.ProductID, env.UserID).Scan(&sc.RunID); err != nil {
			return fmt.Errorf("seed interest_run: %w", err)
		}

		// Three lines, mixed payouts.
		insertLine := func(idx int, gross, wht, net decimal.Decimal,
			method string, targetAcct *uuid.UUID, extCh *string,
		) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO interest_run_lines (
				  tenant_id, run_id, account_id, counterparty_id, product_id,
				  days_in_fy, days_with_snapshots,
				  sum_of_daily_balances, weighted_avg_balance,
				  rate_applied_pct, wht_rate_pct,
				  gross_interest, wht_amount, net_interest,
				  payout_method, payout_target_account_id, payout_external_channel
				) VALUES (
				  $1, $2, $3, $4, $5, 365, 365,
				  100000, 100000,
				  10.0, 5.0,
				  $6, $7, $8,
				  $9::interest_payout_method, $10, $11
				)
			`,
				env.TenantID, sc.RunID, sc.DepAcctIDs[idx], sc.CPIDs[idx], sc.ProductID,
				gross, wht, net,
				method, targetAcct, extCh)
			return err
		}

		extCh := "bank_transfer"
		if err := insertLine(0,
			decimal.NewFromInt(1000), decimal.NewFromInt(50), decimal.NewFromInt(950),
			"credit_savings", &sc.DepAcctIDs[0], nil); err != nil {
			return fmt.Errorf("seed line 0 (credit_savings): %w", err)
		}
		if err := insertLine(1,
			decimal.NewFromInt(2000), decimal.NewFromInt(100), decimal.NewFromInt(1900),
			"buy_shares", nil, nil); err != nil {
			return fmt.Errorf("seed line 1 (buy_shares): %w", err)
		}
		if err := insertLine(2,
			decimal.NewFromInt(500), decimal.NewFromInt(25), decimal.NewFromInt(475),
			"external", nil, &extCh); err != nil {
			return fmt.Errorf("seed line 2 (external): %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seedInterestRun: %v", err)
	}
	return sc
}

// cleanupInterestRun nukes the seeded run + cascading rows. Called via
// t.Cleanup so it runs even on test failure. Best-effort — failures
// only log.
func cleanupInterestRun(t *testing.T, env *testEnv, sc interestScenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		exec := func(label, sql string, args ...any) {
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				t.Logf("cleanup %s: %v", label, err)
			}
		}
		// Outbox rows + tax ledger + share certificates + share txns
		// produced by the run. Share accounts created by
		// EnsureAccountTx are left in place — they're benign and the
		// counterparties may reuse them in other tests.
		exec("posting_outbox", `DELETE FROM posting_outbox WHERE payload->>'source_module' = 'savings.interest' AND payload->>'source_ref' = $1`, sc.RunID.String())
		exec("tax_payable_ledger", `DELETE FROM tax_payable_ledger WHERE source_kind = 'interest_run' AND source_id = $1`, sc.RunID)
		exec("share_certificates", `DELETE FROM share_certificates WHERE issued_by = $1`, env.UserID)
		// interest_run_lines CASCADE-deletes via interest_runs FK.
		exec("interest_runs", `DELETE FROM interest_runs WHERE id = $1`, sc.RunID)
		return nil
	})
}

// buildInterestHandlerForTest constructs an InterestHandler with a
// LIVE (non-Disabled) posting client so PostTx actually writes the
// outbox row. The rest of the testenv wiring builds a router around
// a Disabled client for the receipt-flow tests; this helper hands us
// the same wiring with one knob flipped.
func buildInterestHandlerForTest(env *testEnv) *InterestHandler {
	pool := env.Pool
	return &InterestHandler{
		DB:             pool,
		Tenants:        store.NewTenantStore(pool.Pool),
		Members:        store.NewMemberStore(pool.Pool),
		Counterparties: store.NewCounterpartyStore(pool.Pool),
		Products:       store.NewDepositProductStore(pool.Pool),
		Deposits:       store.NewDepositStore(pool.Pool),
		Shares:         store.NewShareStore(pool.Pool),
		Interest:       store.NewInterestStore(pool.Pool),
		// Live posting client: writes to posting_outbox via PostTx.
		// No HTTP base URL is needed — PostTx never calls accounting,
		// it only inserts the outbox row.
		Posting: &posting.Client{Disabled: false},
	}
}

func TestInterestRun_PostsBatchedJEToOutbox(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	sc := seedInterestRun(t, env)
	// Defer LIFO: cleanupInterestRun (registered after env.close) runs
	// FIRST so interest_run_lines are gone before env.close's
	// blanket deposit_accounts delete (which would FK-collide).
	defer cleanupInterestRun(t, env, sc)

	h := buildInterestHandlerForTest(env)

	// Mount only the Post route — chi gives us URL-param parsing.
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
	r.Post("/v1/interest-runs/{run_id}/post", h.Post)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// ─── 1. POST /post ─────────────────────────────────────────────
	status, body := httpJSON(t, "POST", srv.URL+"/v1/interest-runs/"+sc.RunID.String()+"/post", nil)
	if status != http.StatusOK {
		t.Fatalf("POST /post: want 200, got %d. body=%s", status, body)
	}

	// ─── 2. Verify the run row flipped + journal_entry_id stamp ────
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var runStatus string
	var stampedJE *uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, journal_entry_id FROM interest_runs WHERE id=$1`, sc.RunID).
			Scan(&runStatus, &stampedJE)
	}); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if runStatus != "posted" {
		t.Errorf("run.status: want posted, got %s", runStatus)
	}
	if stampedJE == nil || *stampedJE != sc.RunID {
		t.Errorf("run.journal_entry_id: want %s, got %v", sc.RunID, stampedJE)
	}

	// ─── 3. Verify exactly ONE outbox row + payload structure ──────
	type lineExpect struct {
		Account string
		Debit   string
		Credit  string
	}
	want := map[string]lineExpect{
		"5000": {Account: "5000", Debit: "3500"},   // DR Σ gross
		"2200": {Account: "2200", Credit: "175"},   // CR Σ wht
		"3000": {Account: "3000", Credit: "1900"},  // CR shares portion
		"2230": {Account: "2230", Credit: "475"},   // CR external payable
		"2000": {Account: "2000", Credit: "950"},   // CR per-product liability (credit_savings line)
	}

	var (
		rowCount int
		payload  []byte
	)
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.interest'
			   AND payload->>'source_ref' = $1
		`, sc.RunID.String()).Scan(&rowCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.interest'
			   AND payload->>'source_ref' = $1
		`, sc.RunID.String()).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("outbox rows for run: want 1, got %d", rowCount)
	}

	var got struct {
		SourceModule string `json:"source_module"`
		SourceRef    string `json:"source_ref"`
		Lines        []struct {
			AccountCode string `json:"account_code"`
			Debit       string `json:"debit"`
			Credit      string `json:"credit"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v. raw=%s", err, payload)
	}
	if got.SourceModule != "savings.interest" || got.SourceRef != sc.RunID.String() {
		t.Errorf("outbox source: want savings.interest/%s, got %s/%s",
			sc.RunID, got.SourceModule, got.SourceRef)
	}

	gotByCode := map[string]lineExpect{}
	totalDR, totalCR := decimal.Zero, decimal.Zero
	for _, l := range got.Lines {
		gotByCode[l.AccountCode] = lineExpect{Account: l.AccountCode, Debit: l.Debit, Credit: l.Credit}
		if l.Debit != "" {
			d, _ := decimal.NewFromString(l.Debit)
			totalDR = totalDR.Add(d)
		}
		if l.Credit != "" {
			c, _ := decimal.NewFromString(l.Credit)
			totalCR = totalCR.Add(c)
		}
	}
	for code, exp := range want {
		got, ok := gotByCode[code]
		if !ok {
			t.Errorf("missing outbox line for account %s", code)
			continue
		}
		// Strings emitted via StringFixed(2) — normalise via decimal for
		// the comparison (so "3500" matches "3500.00").
		expDR, _ := decimal.NewFromString(exp.Debit)
		expCR, _ := decimal.NewFromString(exp.Credit)
		gotDR, _ := decimal.NewFromString(got.Debit)
		gotCR, _ := decimal.NewFromString(got.Credit)
		if !expDR.Equal(gotDR) {
			t.Errorf("account %s debit: want %s, got %s", code, exp.Debit, got.Debit)
		}
		if !expCR.Equal(gotCR) {
			t.Errorf("account %s credit: want %s, got %s", code, exp.Credit, got.Credit)
		}
	}
	if !totalDR.Equal(totalCR) {
		t.Errorf("DR/CR balance: DR=%s CR=%s", totalDR.StringFixed(2), totalCR.StringFixed(2))
	}
	if !totalDR.Equal(decimal.NewFromInt(3500)) {
		t.Errorf("total DR: want 3500, got %s", totalDR.StringFixed(2))
	}
}
