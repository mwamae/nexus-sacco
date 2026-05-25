// End-to-end acceptance tests for the share-handler GL wiring.
//
// Three orthogonal properties under test:
//   1. BonusIssue posts one batched DR 3010 / CR 3000 appropriation JE
//      and stamps every per-member share_transactions row with the
//      same synthetic journal_entry_id.
//   2. Adjust posts the correct polarity (increase → DR offset / CR 3000,
//      decrease → DR 3000 / CR offset) and rejects bad offsetting
//      accounts (missing, unknown, wrong class).
//   3. Transfer leaves journal_entry_id NULL on both legs and posts
//      no outbox row (equity-class-internal move, by design).
//
// Tujenge's dev seed has share_accounts for all counterparties, so
// these tests operate on existing accounts. Snapshot + restore
// shares_held in cleanup so balances aren't permanently shifted.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

func buildShareHandlerForTest(env *testEnv) *ShareHandler {
	pool := env.Pool
	return &ShareHandler{
		DB:             pool,
		Tenants:        store.NewTenantStore(pool.Pool),
		Members:        store.NewMemberStore(pool.Pool),
		Counterparties: store.NewCounterpartyStore(pool.Pool),
		Shares:         store.NewShareStore(pool.Pool),
		Approvals:      store.NewApprovalsStore(pool.Pool),
		// Notifier nil — handlers nil-check it; we don't exercise the
		// notification side-effects in the GL contract tests.
		Notifier: nil,
		// Live posting client — PostTx writes to posting_outbox.
		Posting: &posting.Client{Disabled: false},
	}
}

func mountShareRoutes(env *testEnv, h *ShareHandler) *httptest.Server {
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
	r.Post("/v1/share-accounts/{counterparty_id}/adjust", h.Adjust)
	r.Post("/v1/share-accounts/{counterparty_id}/transfer", h.Transfer)
	r.Post("/v1/share-accounts/bonus-issue", h.BonusIssue)
	return httptest.NewServer(r)
}

// flipShareApprovalToggles disables share-related approvals for the
// test so the handler runs the executor path directly. Returns a
// restorer to defer-call from the test.
func flipShareApprovalToggles(t *testing.T, env *testEnv) func() {
	t.Helper()
	ctx := context.Background()
	var origTransfer, origBonus bool
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT approval_share_transfer, approval_share_bonus FROM tenant_operations`).
			Scan(&origTransfer, &origBonus); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE tenant_operations
			   SET approval_share_transfer = false,
			       approval_share_bonus = false
		`)
		return err
	}); err != nil {
		t.Fatalf("flip share approvals: %v", err)
	}
	return func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `
				UPDATE tenant_operations
				   SET approval_share_transfer = $1,
				       approval_share_bonus = $2
			`, origTransfer, origBonus)
			return err
		})
	}
}

// snapshotAndRestoreShares snapshots shares_held + shares_pledged for
// every active share_account with shares > 0, and returns a closer
// that restores them. Used by tests that run /bonus or other
// tenant-wide mutations to keep dev seed data clean.
func snapshotAndRestoreShares(t *testing.T, env *testEnv) func() {
	t.Helper()
	ctx := context.Background()
	type acctSnap struct{ ID uuid.UUID; Shares, Pledged int }
	var pre []acctSnap
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, shares_held, shares_pledged FROM share_accounts
			 WHERE tenant_id=$1 AND status='active'
			 ORDER BY id
		`, env.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s acctSnap
			if err := rows.Scan(&s.ID, &s.Shares, &s.Pledged); err != nil {
				return err
			}
			pre = append(pre, s)
		}
		return nil
	}); err != nil {
		t.Fatalf("snapshot shares: %v", err)
	}
	return func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			for _, s := range pre {
				if _, err := tx.Exec(context.Background(),
					`UPDATE share_accounts SET shares_held=$2, shares_pledged=$3 WHERE id=$1`,
					s.ID, s.Shares, s.Pledged); err != nil {
					t.Logf("restore shares for %s: %v", s.ID, err)
				}
			}
			return nil
		})
	}
}

// decodeDataInto unwraps the {"data": …} envelope httpx.Created/OK
// emits and decodes the inner object into v.
func decodeDataInto(t *testing.T, raw []byte, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v. raw=%s", err, raw)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode .data: %v. raw=%s", err, env.Data)
	}
}

// ─────────── Transfer — no GL, no outbox ───────────

func TestShareTransfer_LeavesJournalEntryIDNull(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	defer flipShareApprovalToggles(t, env)()
	defer snapshotAndRestoreShares(t, env)()

	ctx := context.Background()

	// Pick the holder with the largest shares_held as the sender,
	// and a different active holder as the receiver. Tujenge has
	// share_accounts for every CP, so we operate on existing rows.
	var fromCP, toCP, fromAcct, toAcct uuid.UUID
	var fromShares int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT counterparty_id, id, shares_held FROM share_accounts
			 WHERE tenant_id=$1 AND status='active' AND shares_held > 20
			 ORDER BY shares_held DESC LIMIT 1
		`, env.TenantID).Scan(&fromCP, &fromAcct, &fromShares); err != nil {
			return fmt.Errorf("pick sender: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT counterparty_id, id FROM share_accounts
			 WHERE tenant_id=$1 AND status='active' AND counterparty_id <> $2
			 ORDER BY counterparty_id LIMIT 1
		`, env.TenantID, fromCP).Scan(&toCP, &toAcct); err != nil {
			return fmt.Errorf("pick receiver: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Logf("transfer test: from=%s (acct=%s shares=%d) → to=%s", fromCP, fromAcct, fromShares, toCP)

	h := buildShareHandlerForTest(env)
	srv := mountShareRoutes(env, h)
	defer srv.Close()

	body := map[string]any{
		"shares":       5,
		"to_member_id": toCP,
		"narration":    "acceptance test transfer",
		"reason":       "acceptance test",
	}
	status, raw := httpJSON(t, "POST",
		srv.URL+"/v1/share-accounts/"+fromCP.String()+"/transfer", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /transfer: want 201, got %d. body=%s", status, raw)
	}

	// Both share_transactions rows for THIS transfer must have
	// journal_entry_id IS NULL. Scope by initiated_by (this run's user)
	// to avoid colliding with dev-seed historical transfers.
	var nullCount int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM share_transactions
			 WHERE initiated_by = $1
			   AND txn_type IN ('transfer_out','transfer_in')
			   AND journal_entry_id IS NULL
		`, env.UserID).Scan(&nullCount)
	}); err != nil {
		t.Fatalf("read share_transactions: %v", err)
	}
	if nullCount != 2 {
		t.Errorf("transfer share_transactions with journal_entry_id IS NULL: want 2 (both legs), got %d", nullCount)
	}

	// No outbox row for transfer source_module.
	var outboxCount int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' LIKE 'savings.shares.transfer%'
		`).Scan(&outboxCount)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if outboxCount != 0 {
		t.Errorf("transfer must produce no outbox row, got %d", outboxCount)
	}
}

// ─────────── Adjust — JE posted + validation rejections ───────────

func TestShareAdjust_PostsToOutbox_AndRejectsBadOffsetAccount(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	defer snapshotAndRestoreShares(t, env)()

	ctx := context.Background()

	// Pick an existing active share_account with enough headroom for
	// +10 and -5 (the happy-path calls). Skip if no holder has >= 20.
	var cpID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT counterparty_id FROM share_accounts
			 WHERE tenant_id=$1 AND status='active' AND shares_held >= 20
			 ORDER BY shares_held DESC LIMIT 1
		`, env.TenantID).Scan(&cpID)
	}); err != nil {
		t.Skipf("no active share_account with >= 20 shares on tujenge: %v", err)
	}
	defer func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(),
				`DELETE FROM posting_outbox WHERE payload->>'source_module' = 'savings.shares.adjust'
				   AND payload->>'narration' LIKE '%acceptance test%'`); err != nil {
				t.Logf("cleanup outbox: %v", err)
			}
			return nil
		})
	}()

	h := buildShareHandlerForTest(env)
	srv := mountShareRoutes(env, h)
	defer srv.Close()

	url := srv.URL + "/v1/share-accounts/" + cpID.String() + "/adjust"

	// ─── Rejection #1: missing offsetting_account_code ────────────
	status, raw := httpJSON(t, "POST", url, map[string]any{
		"shares_delta": 5, "reason": "missing offset acceptance test",
	})
	if status != http.StatusBadRequest {
		t.Errorf("missing offset: want 400, got %d. body=%s", status, raw)
	}

	// ─── Rejection #2: unknown code ────────────────────────────────
	status, raw = httpJSON(t, "POST", url, map[string]any{
		"shares_delta":            5,
		"reason":                  "unknown offset acceptance test",
		"offsetting_account_code": "9999",
	})
	if status != http.StatusBadRequest {
		t.Errorf("unknown offset: want 400, got %d. body=%s", status, raw)
	}

	// ─── Rejection #3: asset class (1000 Cash on Hand) ─────────────
	status, raw = httpJSON(t, "POST", url, map[string]any{
		"shares_delta":            5,
		"reason":                  "asset offset acceptance test",
		"offsetting_account_code": "1000",
	})
	if status != http.StatusBadRequest {
		t.Errorf("asset-class offset: want 400, got %d. body=%s", status, raw)
	}

	// ─── Happy path: increase (+10 shares, offset 3010) ───────────
	status, raw = httpJSON(t, "POST", url, map[string]any{
		"shares_delta":            10,
		"reason":                  "year-end true-up acceptance test",
		"offsetting_account_code": "3010",
	})
	if status != http.StatusCreated {
		t.Fatalf("increase: want 201, got %d. body=%s", status, raw)
	}
	var incResp struct {
		Transaction struct {
			ID             uuid.UUID  `json:"id"`
			JournalEntryID *uuid.UUID `json:"journal_entry_id"`
		} `json:"transaction"`
	}
	decodeDataInto(t, raw, &incResp)
	if incResp.Transaction.JournalEntryID == nil || *incResp.Transaction.JournalEntryID != incResp.Transaction.ID {
		t.Errorf("journal_entry_id must equal txn.ID for single-txn JE; got %v vs %s",
			incResp.Transaction.JournalEntryID, incResp.Transaction.ID)
	}
	checkOutboxLines(t, ctx, env, incResp.Transaction.ID,
		map[string]struct{ Debit, Credit decimal.Decimal }{
			"3010": {Debit: decimal.NewFromInt(1000)},
			"3000": {Credit: decimal.NewFromInt(1000)},
		})

	// ─── Happy path: decrease (-5 shares, offset 3010) ────────────
	status, raw = httpJSON(t, "POST", url, map[string]any{
		"shares_delta":            -5,
		"reason":                  "duplicate entry correction acceptance test",
		"offsetting_account_code": "3010",
	})
	if status != http.StatusCreated {
		t.Fatalf("decrease: want 201, got %d. body=%s", status, raw)
	}
	var decResp struct {
		Transaction struct {
			ID uuid.UUID `json:"id"`
		} `json:"transaction"`
	}
	decodeDataInto(t, raw, &decResp)
	checkOutboxLines(t, ctx, env, decResp.Transaction.ID,
		map[string]struct{ Debit, Credit decimal.Decimal }{
			"3000": {Debit: decimal.NewFromInt(500)},
			"3010": {Credit: decimal.NewFromInt(500)},
		})
}

func checkOutboxLines(t *testing.T, ctx context.Context, env *testEnv,
	txnID uuid.UUID, want map[string]struct{ Debit, Credit decimal.Decimal },
) {
	t.Helper()
	var payload []byte
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_ref' = $1
		`, txnID.String()).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox for %s: %v", txnID, err)
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
	gotByCode := map[string]struct{ Debit, Credit decimal.Decimal }{}
	for _, l := range got.Lines {
		d, _ := decimal.NewFromString(l.Debit)
		c, _ := decimal.NewFromString(l.Credit)
		gotByCode[l.AccountCode] = struct{ Debit, Credit decimal.Decimal }{d, c}
	}
	for code, exp := range want {
		g, ok := gotByCode[code]
		if !ok {
			t.Errorf("missing outbox line for %s", code)
			continue
		}
		if !exp.Debit.Equal(g.Debit) {
			t.Errorf("acct %s debit: want %s, got %s", code, exp.Debit.StringFixed(2), g.Debit.StringFixed(2))
		}
		if !exp.Credit.Equal(g.Credit) {
			t.Errorf("acct %s credit: want %s, got %s", code, exp.Credit.StringFixed(2), g.Credit.StringFixed(2))
		}
	}
}

// ─────────── BonusIssue — batched DR 3010 / CR 3000 ───────────

func TestShareBonus_PostsBatchedAppropriationToOutbox(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	defer flipShareApprovalToggles(t, env)()
	defer snapshotAndRestoreShares(t, env)()

	ctx := context.Background()

	// Snapshot active holders pre-bonus to compute the expected total
	// dynamically (executor iterates the full register). Use the SAME
	// query the executor uses — ActiveAccountsTx joins members and
	// excludes blacklisted/exited/deceased/rejected. A naive
	// "shares_held > 0" snapshot over-counts those excluded holders.
	type acctSnap struct{ ID uuid.UUID; Shares int }
	var preState []acctSnap
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.shares_held FROM share_accounts a
			  JOIN members m ON m.counterparty_id = a.counterparty_id
			 WHERE a.tenant_id=$1 AND a.status='active' AND a.shares_held > 0
			   AND m.status NOT IN ('blacklisted','exited','deceased','rejected')
			 ORDER BY a.id
		`, env.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s acctSnap
			if err := rows.Scan(&s.ID, &s.Shares); err != nil {
				return err
			}
			preState = append(preState, s)
		}
		return nil
	}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(preState) == 0 {
		t.Skip("tujenge has no active share-holders eligible for bonus")
	}

	pct := decimal.NewFromInt(50) // 50% — generous enough that every holder
	par := decimal.NewFromInt(100)
	expectedBonusShares := 0
	for _, s := range preState {
		bonus := pct.Div(decimal.NewFromInt(100)).
			Mul(decimal.NewFromInt(int64(s.Shares))).
			Floor()
		expectedBonusShares += int(bonus.IntPart())
	}
	if expectedBonusShares == 0 {
		t.Skip("snapshot-based expected bonus rounded to zero")
	}

	defer func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(),
				`DELETE FROM posting_outbox WHERE payload->>'source_module' = 'savings.shares.bonus' AND payload->>'narration' LIKE '%acceptance test bonus%'`); err != nil {
				t.Logf("cleanup outbox: %v", err)
			}
			if _, err := tx.Exec(context.Background(),
				`DELETE FROM share_certificates WHERE issued_by = $1`, env.UserID); err != nil {
				t.Logf("cleanup share_certificates: %v", err)
			}
			return nil
		})
	}()

	h := buildShareHandlerForTest(env)
	srv := mountShareRoutes(env, h)
	defer srv.Close()

	body := map[string]any{
		"pct_of_holding": pct.String(),
		"reason":         "acceptance test bonus",
	}
	status, raw := httpJSON(t, "POST", srv.URL+"/v1/share-accounts/bonus-issue", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /bonus-issue: want 201, got %d. body=%s", status, raw)
	}
	var resp struct {
		IssuedToCount    int    `json:"issued_to_count"`
		TotalBonusShares int    `json:"total_bonus_shares"`
		PctApplied       string `json:"pct_applied"`
	}
	decodeDataInto(t, raw, &resp)
	if resp.TotalBonusShares != expectedBonusShares {
		t.Errorf("total_bonus_shares: want %d (computed from pre-state), got %d",
			expectedBonusShares, resp.TotalBonusShares)
	}

	expectedAmount := par.Mul(decimal.NewFromInt(int64(resp.TotalBonusShares)))

	var rowCount int
	var payload []byte
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.shares.bonus'
			   AND payload->>'narration' LIKE '%acceptance test bonus%'
		`).Scan(&rowCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.shares.bonus'
			   AND payload->>'narration' LIKE '%acceptance test bonus%'
			 ORDER BY enqueued_at DESC LIMIT 1
		`).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("outbox rows: want 1, got %d", rowCount)
	}

	var got struct {
		SourceRef string `json:"source_ref"`
		Lines     []struct {
			AccountCode string `json:"account_code"`
			Debit       string `json:"debit"`
			Credit      string `json:"credit"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	dr3010, cr3000 := decimal.Zero, decimal.Zero
	for _, l := range got.Lines {
		switch l.AccountCode {
		case "3010":
			dr3010, _ = decimal.NewFromString(l.Debit)
		case "3000":
			cr3000, _ = decimal.NewFromString(l.Credit)
		}
	}
	if !dr3010.Equal(expectedAmount) {
		t.Errorf("DR 3010: want %s, got %s", expectedAmount.StringFixed(2), dr3010.StringFixed(2))
	}
	if !cr3000.Equal(expectedAmount) {
		t.Errorf("CR 3000: want %s, got %s", expectedAmount.StringFixed(2), cr3000.StringFixed(2))
	}

	// Every share_transactions row produced by this bonus must carry
	// the SAME journal_entry_id (= outbox source_ref).
	jeID, err := uuid.Parse(got.SourceRef)
	if err != nil {
		t.Fatalf("source_ref not uuid: %v", err)
	}
	var stampedCount int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM share_transactions
			 WHERE journal_entry_id = $1 AND txn_type = 'bonus_issue'
		`, jeID).Scan(&stampedCount)
	}); err != nil {
		t.Fatalf("read stamped count: %v", err)
	}
	if stampedCount != resp.IssuedToCount {
		t.Errorf("share_transactions stamped with this JE: want %d (issued_to_count), got %d",
			resp.IssuedToCount, stampedCount)
	}
}
