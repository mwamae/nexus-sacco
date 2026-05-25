// End-to-end HTTP acceptance test for the Collection Desk.
//
// Property under test: a single mpesa payment with four mixed
// lines (deposit + share-purchase + loan-repayment + fee) for the
// SAME counterparty produces exactly one receipt, fires every
// per-line side-effect when the approvals go through, and rolls
// the header up to 'posted' with a tenant-scoped per-till per-day
// serial.
//
// This is the only HTTP-level test in the service (the other
// integration tests are store-layer). It uses the fixture in
// testenv_test.go and skips when DATABASE_URL is not set.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ─────────── Scenario seed ───────────

type scenarioIDs struct {
	CounterpartyID uuid.UUID
	DepositAcctID  uuid.UUID
	LoanID         uuid.UUID
	ApplicationID  uuid.UUID
	ShareAcctID    uuid.UUID
	FeeCode        string
}

// seedCollectionScenario picks an existing tujenge counterparty that
// already has a share account, then layers on a fresh deposit
// account + a fresh overdue loan for it. Everything created here
// carries the env's MarkerSuffix in its natural-key column so
// teardown can target only this run's rows.
func seedCollectionScenario(t *testing.T, e *testEnv) scenarioIDs {
	t.Helper()
	ctx := context.Background()

	var ids scenarioIDs
	err := e.Pool.WithTenantTx(ctx, e.TenantID, func(tx pgx.Tx) error {
		// Pick a CP that has a share_account. share_accounts is the
		// load-bearing constraint because the share-purchase line
		// won't have anywhere to credit otherwise.
		if err := tx.QueryRow(ctx, `
			SELECT c.id, sa.id
			  FROM counterparties c
			  JOIN share_accounts sa ON sa.counterparty_id = c.id
			 WHERE c.tenant_id = $1 AND c.status = 'active'
			 LIMIT 1
		`, e.TenantID).Scan(&ids.CounterpartyID, &ids.ShareAcctID); err != nil {
			return fmt.Errorf("find seeded CP with share account: %w", err)
		}

		// Pick a deposit_product + loan_product for the tenant.
		var depositProductID, loanProductID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM deposit_products WHERE tenant_id=$1 LIMIT 1`, e.TenantID).Scan(&depositProductID); err != nil {
			return fmt.Errorf("find deposit product: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM loan_products WHERE tenant_id=$1 LIMIT 1`, e.TenantID).Scan(&loanProductID); err != nil {
			return fmt.Errorf("find loan product: %w", err)
		}

		// Fresh deposit account — opens at zero balance.
		if err := tx.QueryRow(ctx, `
			INSERT INTO deposit_accounts (
			  tenant_id, counterparty_id, product_id, account_no,
			  status, current_balance, available_balance, opened_at, created_by
			) VALUES ($1, $2, $3, $4, 'active', 0, 0, now(), $5)
			RETURNING id
		`,
			e.TenantID, ids.CounterpartyID, depositProductID,
			"DPA-"+e.MarkerSuffix, e.UserID,
		).Scan(&ids.DepositAcctID); err != nil {
			return fmt.Errorf("open deposit account: %w", err)
		}

		// Loan application + active loan + one overdue installment.
		if err := tx.QueryRow(ctx, `
			INSERT INTO loan_applications (
			  tenant_id, application_no, counterparty_id, product_id, status,
			  requested_amount, requested_term_months, monthly_net_income, created_by
			) VALUES ($1, $2, $3, $4, 'disbursed', 50000, 12, 30000, $5)
			RETURNING id
		`,
			e.TenantID, "LA-"+e.MarkerSuffix, ids.CounterpartyID, loanProductID, e.UserID,
		).Scan(&ids.ApplicationID); err != nil {
			return fmt.Errorf("insert loan application: %w", err)
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO loans (
			  tenant_id, loan_no, application_id, counterparty_id, product_id, status,
			  principal, interest_rate_pct, interest_method, repayment_method,
			  term_months, installment_count, first_due_date,
			  principal_disbursed, principal_balance,
			  disbursed_at, disbursed_by
			) VALUES (
			  $1, $2, $3, $4, $5, 'active',
			  50000, 12.0, 'reducing_balance', 'reducing_balance',
			  12, 12, CURRENT_DATE - INTERVAL '40 days',
			  50000, 50000,
			  now(), $6
			)
			RETURNING id
		`,
			e.TenantID, "L-"+e.MarkerSuffix, ids.ApplicationID, ids.CounterpartyID, loanProductID, e.UserID,
		).Scan(&ids.LoanID); err != nil {
			return fmt.Errorf("insert loan: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO loan_repayment_schedule (
			  tenant_id, loan_id, installment_no, due_date,
			  principal_due, interest_due, total_due, outstanding_after, status
			) VALUES ($1, $2, 1, CURRENT_DATE - INTERVAL '40 days',
			          5000, 500, 5500, 45000, 'pending')
		`, e.TenantID, ids.LoanID); err != nil {
			return fmt.Errorf("insert schedule: %w", err)
		}

		// Fee code: the migration seeds 'ad_hoc' as the editable
		// catch-all on every tenant. Trust the seed; if missing we
		// surface a clean test error rather than papering over it.
		if err := tx.QueryRow(ctx, `SELECT code FROM fee_catalog WHERE tenant_id=$1 AND code='ad_hoc'`, e.TenantID).Scan(&ids.FeeCode); err != nil {
			return fmt.Errorf("seed fee_catalog missing 'ad_hoc' for tenant: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return ids
}

// ─────────── HTTP helpers ───────────

func httpJSON(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// ─────────── Test ───────────

func TestCollectionDeskAcceptance_MixedReceiptHappyPath(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	ids := seedCollectionScenario(t, env)

	// ─── 1. POST 4-line receipt ───────────────────────────────────
	depAmt := decimal.NewFromInt(2000)
	shareAmt := decimal.NewFromInt(500)
	loanAmt := decimal.NewFromInt(5500)
	feeAmt := decimal.NewFromInt(150)
	total := depAmt.Add(shareAmt).Add(loanAmt).Add(feeAmt)

	body := map[string]any{
		"counterparty_id": ids.CounterpartyID,
		"channel":         "mpesa",
		"channel_ref":     "MPS-" + env.MarkerSuffix,
		"channel_amount":  total.String(),
		"narration":       "acceptance test mixed receipt",
		"lines": []map[string]any{
			{"kind": "savings_deposit", "amount": depAmt.String(), "target_account_id": ids.DepositAcctID},
			{"kind": "share_purchase", "amount": shareAmt.String()},
			{"kind": "loan_repayment", "amount": loanAmt.String(), "target_account_id": ids.LoanID},
			{"kind": "fee", "amount": feeAmt.String(), "fee_code": ids.FeeCode},
		},
	}
	status, raw := httpJSON(t, "POST", env.Server.URL+"/v1/receipts", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /v1/receipts: want 201, got %d. body=%s", status, raw)
	}

	var createdWrap struct {
		Data struct {
			ID     uuid.UUID `json:"id"`
			Serial string    `json:"serial"`
			Status string    `json:"status"`
			Lines  []struct {
				ID         uuid.UUID  `json:"id"`
				LineNo     int        `json:"line_no"`
				Kind       string     `json:"kind"`
				Status     string     `json:"status"`
				ApprovalID *uuid.UUID `json:"approval_id"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &createdWrap); err != nil {
		t.Fatalf("decode create response: %v. body=%s", err, raw)
	}
	created := createdWrap.Data
	if len(created.Lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(created.Lines))
	}
	// Serial shape: R-<till_code>-YYYYMMDD-NNNN
	if !strings.HasPrefix(created.Serial, "R-mpesa-") {
		t.Errorf("serial prefix: want R-mpesa-..., got %q", created.Serial)
	}
	// Wave 2 of the approvals-coverage rollout: every line kind
	// (including fee + welfare) now consults its matching
	// approval_* toggle. The dev fixture flipped all toggles to
	// TRUE during migration 0030, so every line is pending +
	// approval_id-attached now — including fees.
	for _, l := range created.Lines {
		if l.Status != "pending" {
			t.Errorf("line kind=%s: want status=pending, got %s", l.Kind, l.Status)
		}
		if l.ApprovalID == nil {
			t.Errorf("line kind=%s: missing approval_id", l.Kind)
		}
	}

	// ─── 2. Approve every non-fee line ────────────────────────────
	for _, l := range created.Lines {
		if l.ApprovalID == nil {
			continue
		}
		url := env.Server.URL + "/v1/pending-approvals/" + l.ApprovalID.String() + "/approve"
		s, b := httpJSON(t, "POST", url, nil)
		if s != http.StatusOK {
			t.Fatalf("approve line %d (%s): want 200, got %d. body=%s", l.LineNo, l.Kind, s, b)
		}
	}

	// ─── 3. GET the receipt back, assert posted rollup ────────────
	s, b := httpJSON(t, "GET", env.Server.URL+"/v1/receipts/"+created.ID.String(), nil)
	if s != http.StatusOK {
		t.Fatalf("GET receipt: want 200, got %d. body=%s", s, b)
	}
	var fetchedWrap struct {
		Data struct {
			Status   string  `json:"status"`
			PostedAt *string `json:"posted_at"`
			Lines    []struct {
				Kind   string `json:"kind"`
				Status string `json:"status"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &fetchedWrap); err != nil {
		t.Fatalf("decode fetched receipt: %v. body=%s", err, b)
	}
	fetched := fetchedWrap.Data
	if fetched.Status != "posted" {
		t.Errorf("rolled-up status: want posted, got %s", fetched.Status)
	}
	if fetched.PostedAt == nil {
		t.Errorf("posted_at not stamped")
	}
	for _, l := range fetched.Lines {
		if l.Status != "posted" {
			t.Errorf("post-approval line kind=%s: want status=posted, got %s", l.Kind, l.Status)
		}
	}

	// ─── 4. Verify side-effects on the underlying accounts ────────
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type balances struct {
		Deposit         decimal.Decimal
		LoanPrincipalBal decimal.Decimal
		ShareCount      int
	}
	var got balances
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT current_balance FROM deposit_accounts WHERE id=$1`, ids.DepositAcctID).Scan(&got.Deposit); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT principal_balance FROM loans WHERE id=$1`, ids.LoanID).Scan(&got.LoanPrincipalBal); err != nil {
			return err
		}
		// Count share txns produced by THIS run's user (cleanup pinned
		// to initiated_by, so this read uses the same key).
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM share_transactions WHERE account_id=$1 AND initiated_by=$2`,
			ids.ShareAcctID, env.UserID).Scan(&got.ShareCount); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("balance read: %v", err)
	}

	if !got.Deposit.Equal(depAmt) {
		t.Errorf("deposit balance: want %s, got %s", depAmt.StringFixed(2), got.Deposit.StringFixed(2))
	}
	// Loan started at principal_balance=50000; repayment splits across
	// principal/interest/fees. We don't pin the exact split (the
	// allocation engine owns that) — just assert it moved DOWN.
	if got.LoanPrincipalBal.GreaterThanOrEqual(decimal.NewFromInt(50000)) {
		t.Errorf("loan principal_balance: want < 50000, got %s", got.LoanPrincipalBal.StringFixed(2))
	}
	if got.ShareCount < 1 {
		t.Errorf("share_transactions for this run: want ≥ 1, got %d", got.ShareCount)
	}
}

// Regression: two cash receipts in the same tenant must both succeed.
//
// The original 0022 migration created receipts_channel_ref_unique
// with NULLS NOT DISTINCT — so every cash receipt (channel_ref NULL)
// collided with the previous one. Migration 0024 made the index
// partial (WHERE channel_ref IS NOT NULL), and the store stopped
// dereferencing nil ChannelRef on the (now non-applicable) unique-
// violation path. Both regressions are pinned together here.
func TestCollectionDeskAcceptance_TwoCashReceiptsSucceed(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	ids := seedCollectionScenario(t, env)

	// Cash requires an open till_session for env.UserID. Provision a
	// fresh till + open session for this run; cleanup is deferred so
	// the session goes away when env.close() runs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var tillID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO tills (tenant_id, code, name)
			VALUES ($1, $2, $3) RETURNING id
		`, env.TenantID, "TILL-"+env.MarkerSuffix, "acceptance test till").Scan(&tillID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO till_sessions (tenant_id, till_id, teller_user_id, opening_float, opened_by)
			VALUES ($1, $2, $3, 0, $3)
		`, env.TenantID, tillID, env.UserID)
		return err
	}); err != nil {
		t.Fatalf("provision till session: %v", err)
	}
	// Cleanup hook — env.close() doesn't know about tills yet, so
	// fold the deletes in directly.
	defer func() {
		_ = env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(context.Background(), `DELETE FROM till_sessions WHERE till_id = $1`, tillID); err != nil {
				return err
			}
			_, err := tx.Exec(context.Background(), `DELETE FROM tills WHERE id = $1`, tillID)
			return err
		})
	}()

	mkBody := func() map[string]any {
		return map[string]any{
			"counterparty_id": ids.CounterpartyID,
			"channel":         "cash",
			"channel_amount":  "1000",
			"narration":       "cash dup-allowed test",
			"lines": []map[string]any{
				{"kind": "savings_deposit", "amount": "1000", "target_account_id": ids.DepositAcctID},
			},
		}
	}
	s1, b1 := httpJSON(t, "POST", env.Server.URL+"/v1/receipts", mkBody())
	if s1 != http.StatusCreated {
		t.Fatalf("first cash POST: want 201, got %d. body=%s", s1, b1)
	}
	s2, b2 := httpJSON(t, "POST", env.Server.URL+"/v1/receipts", mkBody())
	if s2 != http.StatusCreated {
		t.Fatalf("second cash POST: want 201 (cash should not collide), got %d. body=%s", s2, b2)
	}
}

// Regression: posting a second receipt with the same (channel,
// channel_ref) on the same tenant must surface a clean 409, NOT a
// 500 with a raw pg unique-violation message.
//
// Earlier bug: isUniqueViolation tried to satisfy the
// pgconn.PgError fields (ConstraintName) via an interface method
// assertion that pgconn.PgError doesn't implement — so the error
// fell through to the generic 500 path and the UI saw "an
// unexpected error occurred" instead of "duplicate M-Pesa code".
func TestCollectionDeskAcceptance_DuplicateChannelRefReturns409(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	ids := seedCollectionScenario(t, env)

	// Pick a CP that has a SEPARATE share-account-less line set —
	// keep this test independent of the share-purchase / loan paths
	// so it's a focused dup-test.
	ref := "MPS-DUP-" + env.MarkerSuffix
	body := map[string]any{
		"counterparty_id": ids.CounterpartyID,
		"channel":         "mpesa",
		"channel_ref":     ref,
		"channel_amount":  "1000",
		"narration":       "dup-receipt test",
		"lines": []map[string]any{
			{"kind": "savings_deposit", "amount": "1000", "target_account_id": ids.DepositAcctID},
		},
	}
	// First POST — should succeed.
	s1, b1 := httpJSON(t, "POST", env.Server.URL+"/v1/receipts", body)
	if s1 != http.StatusCreated {
		t.Fatalf("first POST: want 201, got %d. body=%s", s1, b1)
	}
	// Second POST with same channel_ref — should be 409, not 500.
	s2, b2 := httpJSON(t, "POST", env.Server.URL+"/v1/receipts", body)
	if s2 != http.StatusConflict {
		t.Fatalf("second POST (dup): want 409, got %d. body=%s", s2, b2)
	}
	// Body should mention 'duplicate' so the UI can render a clear
	// hint instead of a generic 'unexpected error'.
	if !strings.Contains(strings.ToLower(string(b2)), "duplicate") {
		t.Errorf("dup error body should mention 'duplicate', got: %s", b2)
	}
}
