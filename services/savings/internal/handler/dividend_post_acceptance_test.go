// End-to-end acceptance test for the Dividend Run → batched GL post.
//
// Property under test: a 3-line dividend run with mixed payouts
// produces exactly one batched APPROPRIATION journal entry on the
// outbox — DR 3010 Retained Earnings (equity transfer, NOT a P&L
// expense) — with balanced DR/CR. Re-posting an already-posted run is
// a no-op at the GL level (handler refuses the second call; outbox
// row count stays at 1).

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

type dividendScenario struct {
	RunID         uuid.UUID
	CPIDs         [3]uuid.UUID
	ShareAcctIDs  [3]uuid.UUID
	DepAcctIDs    [3]uuid.UUID
	ProductID     uuid.UUID
	LiabilityCode string // 2000 for ordinary FOSA
}

// seedDividendRun stages an approved dividend_run with three lines:
//   Member 0 (credit_savings): gross 2000, wht 100, net 1900 → CR 2000 = 1900
//   Member 1 (buy_shares):     gross 3000, wht 150, net 2850 → 28 sh × 100 = 2800 (CR 3000) + residual 50 (CR 2000)
//   Member 2 (external):       gross 1000, wht  50, net  950 → CR 2230 = 950
// Totals: DR 3010 = 6000; CR 2200 = 300; CR 3000 = 2800; CR 2230 = 950; CR 2000 = 1950 → balances at 6000.
func seedDividendRun(t *testing.T, env *testEnv) dividendScenario {
	t.Helper()
	ctx := context.Background()
	sc := dividendScenario{LiabilityCode: "2000"}

	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// Pick 3 active counterparties.
		rows, err := tx.Query(ctx, `
			SELECT id FROM counterparties
			 WHERE tenant_id=$1 AND status='active' ORDER BY id LIMIT 3
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

		// FOSA + ordinary product → liability code 2000 (per
		// depositLiabilityCode).
		if err := tx.QueryRow(ctx, `
			SELECT id FROM deposit_products
			 WHERE tenant_id=$1 AND is_active AND segment='fosa' AND product_type='ordinary'
			 LIMIT 1
		`, env.TenantID).Scan(&sc.ProductID); err != nil {
			return fmt.Errorf("find ordinary product: %w", err)
		}

		// Per-CP fresh deposit accounts (target for credit_savings;
		// fallback for buy_shares residual). Marker-suffixed so the
		// testenv cleanup catches them.
		for idx, cp := range sc.CPIDs {
			if err := tx.QueryRow(ctx, `
				INSERT INTO deposit_accounts (
				  tenant_id, counterparty_id, product_id, account_no,
				  status, current_balance, available_balance, opened_at, created_by
				) VALUES ($1, $2, $3, $4, 'active', 0, 0, now(), $5)
				RETURNING id
			`, env.TenantID, cp, sc.ProductID,
				fmt.Sprintf("DPA-DIV-%s-%d", env.MarkerSuffix, idx),
				env.UserID).Scan(&sc.DepAcctIDs[idx]); err != nil {
				return fmt.Errorf("seed deposit account %d: %w", idx, err)
			}
		}

		// Per-CP share accounts (required FK for dividend_run_lines;
		// par_value_at_open = 100 to match the tenant_operations
		// default share_par_value). Use INSERT … ON CONFLICT DO NOTHING
		// + a follow-up SELECT to handle CPs that already have a share
		// account from prior seed/tests.
		for idx, cp := range sc.CPIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO share_accounts (
				  tenant_id, counterparty_id, account_no, par_value_at_open
				) VALUES ($1, $2, $3, 100)
				ON CONFLICT (tenant_id, counterparty_id) DO NOTHING
			`, env.TenantID, cp,
				fmt.Sprintf("SHA-DIV-%s-%d", env.MarkerSuffix, idx)); err != nil {
				return fmt.Errorf("ensure share account %d: %w", idx, err)
			}
			if err := tx.QueryRow(ctx,
				`SELECT id FROM share_accounts WHERE counterparty_id=$1`, cp,
			).Scan(&sc.ShareAcctIDs[idx]); err != nil {
				return fmt.Errorf("read share account %d: %w", idx, err)
			}
		}

		// Approved-state dividend_run.
		if err := tx.QueryRow(ctx, `
			INSERT INTO dividend_runs (
			  tenant_id, run_no, financial_year_label, fy_start, fy_end,
			  status, calc_method, agm_rate_pct, agm_resolution_ref, agm_resolution_date,
			  wht_rate_pct, created_by, approved_at, approved_by
			) VALUES ($1, $2, 'FY-DIV-'||$3,
			  DATE '2025-01-01', DATE '2025-12-31',
			  'approved', 'closing_balance', 6.0,
			  'AGM-DIV-'||$3, DATE '2026-01-15', 5.0, $4, now(), $4)
			RETURNING id
		`, env.TenantID, "DR-"+env.MarkerSuffix, env.MarkerSuffix, env.UserID).
			Scan(&sc.RunID); err != nil {
			return fmt.Errorf("seed dividend_run: %w", err)
		}

		insertLine := func(idx int, gross, wht, net decimal.Decimal,
			method string, targetAcct *uuid.UUID, extCh *string,
		) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO dividend_run_lines (
				  tenant_id, run_id, share_account_id, counterparty_id,
				  calc_method, shares_basis, par_value_at_run, capital_basis,
				  days_in_fy, rate_applied_pct, wht_rate_pct,
				  gross_dividend, wht_amount, net_dividend,
				  payout_method, payout_target_account_id, payout_external_channel
				) VALUES (
				  $1, $2, $3, $4,
				  'closing_balance', 100, 100, 10000,
				  365, 6.0, 5.0,
				  $5, $6, $7,
				  $8::interest_payout_method, $9, $10
				)
			`,
				env.TenantID, sc.RunID, sc.ShareAcctIDs[idx], sc.CPIDs[idx],
				gross, wht, net,
				method, targetAcct, extCh)
			return err
		}

		extCh := "bank_transfer"
		if err := insertLine(0,
			decimal.NewFromInt(2000), decimal.NewFromInt(100), decimal.NewFromInt(1900),
			"credit_savings", &sc.DepAcctIDs[0], nil); err != nil {
			return fmt.Errorf("seed line 0 (credit_savings): %w", err)
		}
		if err := insertLine(1,
			decimal.NewFromInt(3000), decimal.NewFromInt(150), decimal.NewFromInt(2850),
			"buy_shares", nil, nil); err != nil {
			return fmt.Errorf("seed line 1 (buy_shares): %w", err)
		}
		if err := insertLine(2,
			decimal.NewFromInt(1000), decimal.NewFromInt(50), decimal.NewFromInt(950),
			"external", nil, &extCh); err != nil {
			return fmt.Errorf("seed line 2 (external): %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seedDividendRun: %v", err)
	}
	return sc
}

func cleanupDividendRun(t *testing.T, env *testEnv, sc dividendScenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Isolated tx per delete — a single FK miss otherwise aborts the
	// rest of the cleanup. Same pattern as the loan-disburse acceptance
	// test established for FK-heavy teardown sequences.
	exec := func(label, sql string, args ...any) {
		if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Logf("cleanup %s: %v", label, err)
		}
	}
	exec("posting_outbox",
		`DELETE FROM posting_outbox WHERE payload->>'source_module' = 'savings.dividend' AND payload->>'source_ref' = $1`, sc.RunID.String())
	exec("tax_payable_ledger",
		`DELETE FROM tax_payable_ledger WHERE source_kind='dividend_run' AND source_id = $1`, sc.RunID)
	exec("share_certificates",
		`DELETE FROM share_certificates WHERE issued_by = $1`, env.UserID)
	// dividend_run_lines CASCADE-deletes via dividend_runs FK.
	exec("dividend_runs",
		`DELETE FROM dividend_runs WHERE id = $1`, sc.RunID)
}

func buildDividendHandlerForTest(env *testEnv) *DividendHandler {
	pool := env.Pool
	return &DividendHandler{
		DB:             pool,
		Tenants:        store.NewTenantStore(pool.Pool),
		Members:        store.NewMemberStore(pool.Pool),
		Counterparties: store.NewCounterpartyStore(pool.Pool),
		Products:       store.NewDepositProductStore(pool.Pool),
		Deposits:       store.NewDepositStore(pool.Pool),
		Shares:         store.NewShareStore(pool.Pool),
		Dividends:      store.NewDividendStore(pool.Pool),
		// Live posting client — PostTx writes to posting_outbox.
		Posting: &posting.Client{DryRun: false},
	}
}

func TestDividendRun_PostsBatchedAppropriationToOutbox(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	sc := seedDividendRun(t, env)
	// Defer LIFO: cleanupDividendRun (registered after env.close) runs
	// FIRST so dividend_run_lines + share_certificates are gone before
	// env.close's blanket deposit_accounts/share_transactions delete.
	defer cleanupDividendRun(t, env, sc)

	h := buildDividendHandlerForTest(env)

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
	r.Post("/v1/dividend-runs/{run_id}/post", h.Post)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// ─── 1. POST /post ─────────────────────────────────────────────
	status, raw := httpJSON(t, "POST", srv.URL+"/v1/dividend-runs/"+sc.RunID.String()+"/post", nil)
	if status != http.StatusOK {
		t.Fatalf("POST /post: want 200, got %d. body=%s", status, raw)
	}

	// ─── 2. Run state + journal_entry_id stamp ─────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var runStatus string
	var stampedJE *uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, journal_entry_id FROM dividend_runs WHERE id=$1`, sc.RunID).
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

	// ─── 3. Outbox payload structure ───────────────────────────────
	type lineExpect struct{ Debit, Credit decimal.Decimal }
	want := map[string]lineExpect{
		"3010": {Debit: decimal.NewFromInt(6000)},  // DR Retained Earnings (equity transfer)
		"2200": {Credit: decimal.NewFromInt(300)},  // CR WHT Payable
		"3000": {Credit: decimal.NewFromInt(2800)}, // CR Share Capital (buy_shares portion)
		"2230": {Credit: decimal.NewFromInt(950)},  // CR Other Payables (external)
		"2000": {Credit: decimal.NewFromInt(1950)}, // CR Member Savings (credit_savings net + buy_shares residual)
	}

	var (
		rowCount int
		payload  []byte
	)
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.dividend'
			   AND payload->>'source_ref'    = $1
		`, sc.RunID.String()).Scan(&rowCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.dividend'
			   AND payload->>'source_ref'    = $1
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
	if got.SourceModule != "savings.dividend" || got.SourceRef != sc.RunID.String() {
		t.Errorf("outbox source: want savings.dividend/%s, got %s/%s",
			sc.RunID, got.SourceModule, got.SourceRef)
	}

	totalDR, totalCR := decimal.Zero, decimal.Zero
	gotByCode := map[string]lineExpect{}
	for _, l := range got.Lines {
		d, _ := decimal.NewFromString(l.Debit)
		c, _ := decimal.NewFromString(l.Credit)
		gotByCode[l.AccountCode] = lineExpect{Debit: d, Credit: c}
		totalDR = totalDR.Add(d)
		totalCR = totalCR.Add(c)
	}
	for code, exp := range want {
		g, ok := gotByCode[code]
		if !ok {
			t.Errorf("missing outbox line for account %s", code)
			continue
		}
		if !exp.Debit.Equal(g.Debit) {
			t.Errorf("acct %s debit: want %s, got %s", code, exp.Debit.StringFixed(2), g.Debit.StringFixed(2))
		}
		if !exp.Credit.Equal(g.Credit) {
			t.Errorf("acct %s credit: want %s, got %s", code, exp.Credit.StringFixed(2), g.Credit.StringFixed(2))
		}
	}
	if !totalDR.Equal(totalCR) {
		t.Errorf("DR/CR balance: DR=%s CR=%s", totalDR.StringFixed(2), totalCR.StringFixed(2))
	}
	if !totalDR.Equal(decimal.NewFromInt(6000)) {
		t.Errorf("total DR (Σ gross): want 6000, got %s", totalDR.StringFixed(2))
	}

	// ─── 4. Idempotency — second POST is a no-op at the GL level ──
	status2, raw2 := httpJSON(t, "POST", srv.URL+"/v1/dividend-runs/"+sc.RunID.String()+"/post", nil)
	if status2 == http.StatusOK {
		t.Errorf("second POST /post: want non-2xx (status check should fire on posted run), got %d. body=%s", status2, raw2)
	}
	var rowsAfter int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.dividend'
			   AND payload->>'source_ref'    = $1
		`, sc.RunID.String()).Scan(&rowsAfter)
	}); err != nil {
		t.Fatalf("read outbox post-second-call: %v", err)
	}
	if rowsAfter != 1 {
		t.Errorf("outbox rows after second POST: want 1 (idempotent), got %d", rowsAfter)
	}
}
