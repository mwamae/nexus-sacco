// Integration tests for the Unified Inbox loan-application bridge
// (PR #4) — resolve side.
//
// Property under test:
//   - event=approved   → status moves to 'approved' + approved_amount/
//                        term/rate stamped from the workflow context.
//   - event=rejected   → status moves to 'declined' + decline_category/
//                        decline_reason carried from context.
//   - second call is a no-op (already-terminal short-circuit).
//
// The submit-for-decision path is a thin wrapper around the existing
// createWorkflowInstance pattern already proven by services/savings/
// internal/handler/interest.go. We do NOT duplicate that coverage
// here — the focused risk is the resolve-side state mirror.

package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// seedLoanAppForResolve inserts a minimal loan_applications row in
// status=pending_approval against the run's tenant. Reuses the
// product + counterparty resolved by seedCollectionScenario.
func seedLoanAppForResolve(t *testing.T, env *testEnv, cpID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var loanProductID, appID uuid.UUID
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM loan_products WHERE tenant_id=$1 LIMIT 1`, env.TenantID).Scan(&loanProductID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO loan_applications (
			  tenant_id, application_no, counterparty_id, product_id, status,
			  requested_amount, requested_term_months, monthly_net_income, created_by
			) VALUES ($1, $2, $3, $4, 'pending_approval', 50000, 12, 30000, $5)
			RETURNING id
		`, env.TenantID, "LA-WF-"+env.MarkerSuffix, cpID, loanProductID, env.UserID).Scan(&appID)
	}); err != nil {
		t.Fatalf("seed loan_app: %v", err)
	}
	return appID
}

func TestResolveLoanAppFromWorkflow_ApproveAndIdempotent(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	scenario := seedCollectionScenario(t, env)
	appID := seedLoanAppForResolve(t, env, scenario.CounterpartyID)

	envelope := map[string]any{
		"tenant_id": env.TenantID,
		"event":     "approved",
		"instance": map[string]any{
			"id": uuid.New(),
			"context": map[string]any{
				"approved_amount":            "45000",
				"approved_term_months":       12,
				"approved_interest_rate_pct": "14.5",
			},
		},
	}
	url := env.Server.URL + "/internal/v1/loan-applications/" + appID.String() + "/resolve"

	// First call — must stamp approved_*.
	s1, b1 := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s1 != http.StatusOK {
		t.Fatalf("first resolve: want 200, got %d. body=%s", s1, b1)
	}

	// Read back through pgx.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var status string
	var approvedAmt, approvedRate decimal.Decimal
	var approvedTerm int
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT status, COALESCE(approved_amount, 0), COALESCE(approved_term_months, 0), COALESCE(approved_interest_rate_pct, 0)
			  FROM loan_applications WHERE id=$1
		`, appID).Scan(&status, &approvedAmt, &approvedTerm, &approvedRate)
	}); err != nil {
		t.Fatalf("read loan_app: %v", err)
	}
	if status != "approved" {
		t.Errorf("status after approve: want approved, got %s", status)
	}
	if !approvedAmt.Equal(decimal.NewFromInt(45000)) {
		t.Errorf("approved_amount: want 45000, got %s", approvedAmt)
	}
	if approvedTerm != 12 {
		t.Errorf("approved_term_months: want 12, got %d", approvedTerm)
	}
	if !approvedRate.Equal(decimal.NewFromFloat(14.5)) {
		t.Errorf("approved_interest_rate_pct: want 14.5, got %s", approvedRate)
	}

	// Second call (redelivered webhook) — must be a no-op. We
	// detect it by changing the context's amount and verifying
	// the stored value DOES NOT change.
	envelope["instance"].(map[string]any)["context"].(map[string]any)["approved_amount"] = "1"
	s2, b2 := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s2 != http.StatusOK {
		t.Fatalf("second resolve: want 200, got %d. body=%s", s2, b2)
	}
	var stillApproved decimal.Decimal
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT approved_amount FROM loan_applications WHERE id=$1`, appID).Scan(&stillApproved)
	}); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !stillApproved.Equal(decimal.NewFromInt(45000)) {
		t.Errorf("second resolve must NOT re-stamp approved_amount: want 45000, got %s", stillApproved)
	}
}

func TestResolveLoanAppFromWorkflow_Reject(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()
	scenario := seedCollectionScenario(t, env)
	appID := seedLoanAppForResolve(t, env, scenario.CounterpartyID)

	envelope := map[string]any{
		"tenant_id": env.TenantID,
		"event":     "rejected",
		"instance": map[string]any{
			"id": uuid.New(),
			"context": map[string]any{
				"decline_category": "affordability",
				"decline_reason":   "DTI > 60%",
			},
		},
	}
	url := env.Server.URL + "/internal/v1/loan-applications/" + appID.String() + "/resolve"
	s, b := httpJSONWithHeaders(t, "POST", url, envelope, map[string]string{
		"User-Agent": "nexus-workflow/1",
	})
	if s != http.StatusOK {
		t.Fatalf("reject resolve: want 200, got %d. body=%s", s, b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var status, category, reason string
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT status, COALESCE(decline_category,''), COALESCE(decline_reason,'')
			  FROM loan_applications WHERE id=$1
		`, appID).Scan(&status, &category, &reason)
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "declined" {
		t.Errorf("status: want declined, got %s", status)
	}
	if category != "affordability" {
		t.Errorf("decline_category: want affordability, got %s", category)
	}
	if reason != "DTI > 60%" {
		t.Errorf("decline_reason: want 'DTI > 60%%', got %s", reason)
	}
}
