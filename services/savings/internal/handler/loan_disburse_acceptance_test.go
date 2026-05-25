// End-to-end acceptance test for loan disbursement → batched GL post.
//
// Property under test: a loan with three upfront fees (Processing 2.5%,
// Insurance/LPF 1%, Appraisal 500 flat) disbursed via mpesa produces
// exactly one batched journal entry on the outbox with the correct
// per-fee aggregation and a balanced DR/CR.
//
// The test temporarily flips approval_loan_disbursement OFF so the
// handler hits the executor + GL post directly (the pending-approval
// detour exercises the same executor code on /approve, but the extra
// hop adds noise; this test isolates the GL behaviour). Original
// toggle value is restored on cleanup.

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

type loanDisburseScenario struct {
	LoanID      uuid.UUID
	LoanNo      string
	ProductID   uuid.UUID
	CounterID   uuid.UUID
	AppID       uuid.UUID
	Principal   decimal.Decimal
	OrigToggle  bool // restore on cleanup
}

func seedLoanDisburseScenario(t *testing.T, env *testEnv) loanDisburseScenario {
	t.Helper()
	ctx := context.Background()
	sc := loanDisburseScenario{Principal: decimal.NewFromInt(50000)}

	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// Capture + flip the loan-disbursement toggle so the handler
		// runs the executor directly.
		if err := tx.QueryRow(ctx,
			`SELECT approval_loan_disbursement FROM tenant_operations`).
			Scan(&sc.OrigToggle); err != nil {
			return fmt.Errorf("read approval toggle: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_operations SET approval_loan_disbursement = false`); err != nil {
			return fmt.Errorf("flip approval toggle off: %w", err)
		}

		// Pick an active counterparty.
		if err := tx.QueryRow(ctx, `
			SELECT id FROM counterparties
			 WHERE tenant_id=$1 AND status='active' ORDER BY id LIMIT 1
		`, env.TenantID).Scan(&sc.CounterID); err != nil {
			return fmt.Errorf("pick counterparty: %w", err)
		}

		// Create a fresh loan product with 3 known fees + explicit
		// gl_credit_codes. Marker-suffixed code → cleanup catches it.
		if err := tx.QueryRow(ctx, `
			INSERT INTO loan_products (
			  tenant_id, code, name, category,
			  min_amount, max_amount, min_term_months, max_term_months,
			  interest_rate_pct, interest_method, repayment_method,
			  penalty_rate_pct
			) VALUES ($1, $2, 'Acceptance loan', 'short_term',
			  0, 1000000, 1, 60, 12.0,
			  'reducing_balance', 'reducing_balance', 0)
			RETURNING id
		`, env.TenantID, "LP-"+env.MarkerSuffix).Scan(&sc.ProductID); err != nil {
			return fmt.Errorf("seed loan_product: %w", err)
		}

		insertFee := func(name string, amount decimal.Decimal, isPct bool, gl string, order int) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO loan_product_fees
				  (tenant_id, product_id, name, amount, is_pct, timing, display_order, gl_credit_code)
				VALUES ($1, $2, $3, $4, $5, 'upfront', $6, $7)
			`, env.TenantID, sc.ProductID, name, amount, isPct, order, gl)
			return err
		}
		if err := insertFee("Processing fee", decimal.NewFromFloat(2.5), true, "4010", 1); err != nil {
			return fmt.Errorf("seed processing fee: %w", err)
		}
		if err := insertFee("Insurance / LPF fee", decimal.NewFromInt(1), true, "4020", 2); err != nil {
			return fmt.Errorf("seed insurance fee: %w", err)
		}
		if err := insertFee("Appraisal fee", decimal.NewFromInt(500), false, "4190", 3); err != nil {
			return fmt.Errorf("seed appraisal fee: %w", err)
		}

		// Loan application (disbursed isn't right — needs 'approved').
		if err := tx.QueryRow(ctx, `
			INSERT INTO loan_applications (
			  tenant_id, application_no, counterparty_id, product_id, status,
			  requested_amount, requested_term_months, monthly_net_income, created_by,
			  approved_amount, approved_term_months
			) VALUES ($1, $2, $3, $4, 'approved',
			  $5, 12, 30000, $6, $5, 12)
			RETURNING id
		`, env.TenantID, "LA-"+env.MarkerSuffix, sc.CounterID, sc.ProductID,
			sc.Principal, env.UserID).Scan(&sc.AppID); err != nil {
			return fmt.Errorf("seed loan_application: %w", err)
		}

		// Loan in 'pending_disbursement' state.
		sc.LoanNo = "L-" + env.MarkerSuffix
		if err := tx.QueryRow(ctx, `
			INSERT INTO loans (
			  tenant_id, loan_no, application_id, counterparty_id, product_id, status,
			  principal, interest_rate_pct, interest_method, repayment_method,
			  term_months, installment_count
			) VALUES ($1, $2, $3, $4, $5, 'pending_disbursement',
			  $6, 12.0, 'reducing_balance', 'reducing_balance', 12, 12)
			RETURNING id
		`, env.TenantID, sc.LoanNo, sc.AppID, sc.CounterID, sc.ProductID,
			sc.Principal).Scan(&sc.LoanID); err != nil {
			return fmt.Errorf("seed loan: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seedLoanDisburseScenario: %v", err)
	}
	return sc
}

func cleanupLoanDisburseScenario(t *testing.T, env *testEnv, sc loanDisburseScenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Each statement in its own tx — a single FK error here used to
	// abort every subsequent delete in the same tx (PG aborts the
	// surrounding tx on the first error). Isolated execs let later
	// deletes still land if earlier ones run into a transient miss.
	exec := func(label, sql string, args ...any) {
		if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Logf("cleanup %s: %v", label, err)
		}
	}
	// Restore the original approval toggle FIRST so the test doesn't
	// pollute neighbouring tests in the same run.
	exec("approval_toggle_restore",
		`UPDATE tenant_operations SET approval_loan_disbursement = $1`, sc.OrigToggle)
	// Outbox row for this loan's disbursement.
	exec("posting_outbox",
		`DELETE FROM posting_outbox WHERE payload->>'source_module' = 'savings.loans.disbursement'
		   AND (payload->>'narration') LIKE '%' || $1 || '%'`, sc.LoanNo)
	// Loan-side rows in FK order — children before parents — before
	// env.close's marker-LIKE cleanup runs. env.close will then no-op
	// for these (rows already gone) instead of FK-erroring on the
	// loan_products drop.
	exec("loan_transactions",
		`DELETE FROM loan_transactions WHERE loan_id = $1`, sc.LoanID)
	exec("loan_repayment_schedule",
		`DELETE FROM loan_repayment_schedule WHERE loan_id = $1`, sc.LoanID)
	exec("loans",
		`DELETE FROM loans WHERE id = $1`, sc.LoanID)
	exec("loan_applications",
		`DELETE FROM loan_applications WHERE id = $1`, sc.AppID)
	// loan_product_fees CASCADEs via loan_products.
	exec("loan_products",
		`DELETE FROM loan_products WHERE id = $1`, sc.ProductID)
}

func buildLoanHandlerForTest(env *testEnv) *LoanHandler {
	pool := env.Pool
	return &LoanHandler{
		DB:              pool,
		Tenants:         store.NewTenantStore(pool.Pool),
		Members:         store.NewMemberStore(pool.Pool),
		Counterparties:  store.NewCounterpartyStore(pool.Pool),
		LoanProducts:    store.NewLoanProductStore(pool.Pool),
		Applications:    store.NewLoanApplicationStore(pool.Pool),
		Guarantees:      store.NewLoanGuaranteeStore(pool.Pool),
		Loans:           store.NewLoanStore(pool.Pool),
		Deposits:        store.NewDepositStore(pool.Pool),
		DepositProducts: store.NewDepositProductStore(pool.Pool),
		Approvals:       store.NewApprovalsStore(pool.Pool),
		// Live posting client — PostTx writes the outbox row.
		Posting: &posting.Client{Disabled: false},
	}
}

func TestLoanDisbursement_PostsBatchedJEWithFees(t *testing.T) {
	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	sc := seedLoanDisburseScenario(t, env)
	defer cleanupLoanDisburseScenario(t, env, sc)

	h := buildLoanHandlerForTest(env)

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
	r.Post("/v1/loans/{loan_id}/disburse", h.Disburse)

	srv := httptest.NewServer(r)
	defer srv.Close()

	body := map[string]any{
		"channel":      "mpesa",
		"external_ref": "MPS-DISB-" + env.MarkerSuffix,
	}
	status, raw := httpJSON(t, "POST",
		srv.URL+"/v1/loans/"+sc.LoanID.String()+"/disburse", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /disburse: want 201, got %d. body=%s", status, raw)
	}

	// Expected aggregation for 50,000 principal with these fees:
	//   Processing 2.5%  → 4010 = 1250
	//   Insurance  1%    → 4020 =  500
	//   Appraisal 500    → 4190 =  500
	//   Net disbursed    = 50000 − 2250 = 47750
	wantLines := map[string]struct {
		Debit  decimal.Decimal
		Credit decimal.Decimal
	}{
		"1100": {Debit: decimal.NewFromInt(50000)}, // Member Loans Receivable
		"1030": {Credit: decimal.NewFromInt(47750)}, // M-Pesa Float
		"4010": {Credit: decimal.NewFromInt(1250)},  // Processing
		"4020": {Credit: decimal.NewFromInt(500)},   // Insurance
		"4190": {Credit: decimal.NewFromInt(500)},   // Appraisal
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var (
		rowCount int
		payload  []byte
	)
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.loans.disbursement'
			   AND (payload->>'narration') LIKE '%' || $1 || '%'
		`, sc.LoanNo).Scan(&rowCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT payload FROM posting_outbox
			 WHERE payload->>'source_module' = 'savings.loans.disbursement'
			   AND (payload->>'narration') LIKE '%' || $1 || '%'
		`, sc.LoanNo).Scan(&payload)
	}); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("outbox rows for loan: want 1, got %d", rowCount)
	}

	var got struct {
		SourceModule string `json:"source_module"`
		Lines        []struct {
			AccountCode string `json:"account_code"`
			Debit       string `json:"debit"`
			Credit      string `json:"credit"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v. raw=%s", err, payload)
	}
	if got.SourceModule != "savings.loans.disbursement" {
		t.Errorf("source_module: want savings.loans.disbursement, got %s", got.SourceModule)
	}
	totalDR, totalCR := decimal.Zero, decimal.Zero
	gotByCode := map[string]struct{ Debit, Credit decimal.Decimal }{}
	for _, l := range got.Lines {
		d, _ := decimal.NewFromString(l.Debit)
		c, _ := decimal.NewFromString(l.Credit)
		gotByCode[l.AccountCode] = struct{ Debit, Credit decimal.Decimal }{d, c}
		totalDR = totalDR.Add(d)
		totalCR = totalCR.Add(c)
	}
	for code, exp := range wantLines {
		g, ok := gotByCode[code]
		if !ok {
			t.Errorf("missing line for account %s", code)
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
	if !totalDR.Equal(decimal.NewFromInt(50000)) {
		t.Errorf("total DR: want 50000, got %s", totalDR.StringFixed(2))
	}
}
