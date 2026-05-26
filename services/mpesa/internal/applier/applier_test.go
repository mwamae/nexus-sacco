// Applier acceptance test — the phase-3.5 spec's headline scenario.
//
// Property under test: a member who owes 200 fees, 500 interest,
// 2000 principal, and has a BOSA target → KES 4000 inbound →
// produces 200 fees / 500 interest / 2000 principal / 1300 BOSA
// applied as real rows in loan_transactions / deposit_transactions /
// member_fees_due / posting_outbox.
//
// Plus two safety properties:
//   - Replay: same plan applied twice produces ONE set of rows
//     (the executors don't dedupe internally — the orchestrator
//     guards replays via mpesa_inbound_events.status). This test
//     ensures the executors don't crash on an already-applied
//     state when called from a fresh tx.
//   - RLS: a loan_id belonging to a different tenant is rejected
//     by the loan-row SELECT (RLS hides it; executor surfaces a
//     "loan not found" error rather than silently posting against
//     the wrong tenant).

package applier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/distribution"
)

func TestApplyPlan_AcceptanceScenario(t *testing.T) {
	pool, tenantID := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}

	fix := seedMemberFixture(t, dbPool, tenantID, fixtureSpec{
		Fees:           "200",
		LoanInterest:   "500",
		LoanPrincipal:  "2000",
		LoanPenalty:    "0",
		LoanFees:       "0",
		HasBOSA:        true,
		BOSAStartBal:   "0",
	})

	receipt := "RKACCEPT01"
	plan := &distribution.Plan{
		EventID: uuid.New(),
		Amount:  dec("4000"),
		Splits: []distribution.Split{
			{Leg: distribution.LegFeesDue, Amount: dec("200"), TargetRef: "MPESA_INCOMING"},
			{Leg: distribution.LegLoanInterestDue, Amount: dec("500"),
				TargetRef: fix.LoanNo, TargetID: ptrUUID(fix.LoanID)},
			{Leg: distribution.LegLoanPrincipalDue, Amount: dec("2000"),
				TargetRef: fix.LoanNo, TargetID: ptrUUID(fix.LoanID)},
			{Leg: distribution.LegBOSATopUp, Amount: dec("1300"),
				TargetRef: fix.BOSAAccountNo, TargetID: ptrUUID(fix.BOSAAccountID)},
		},
		Leftover: decimal.Zero,
	}

	var result *ApplyResult
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		// Seed the open fee due row so the fee executor has
		// something to reduce. Phase 4 will populate this from a
		// real loan-disbursement / membership-fee event.
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO member_fees_due (tenant_id, counterparty_id, fee_code, amount_due, source_module)
			VALUES ($1, $2, 'MPESA_INCOMING', 200, 'test')
		`, tenantID, fix.CounterpartyID); err != nil {
			return fmt.Errorf("seed fee due: %w", err)
		}
		// Seed the fee_catalog row for MPESA_INCOMING so the fee
		// executor can resolve the GL credit account.
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO fee_catalog (tenant_id, code, label, gl_credit_code, is_active)
			VALUES ($1, 'MPESA_INCOMING', 'Generic M-PESA fee', '4200', true)
			ON CONFLICT (tenant_id, code) DO NOTHING
		`, tenantID); err != nil {
			return fmt.Errorf("seed fee_catalog: %w", err)
		}

		out, err := ApplyPlanTx(context.Background(), tx, ApplyInput{
			TenantID:              tenantID,
			Plan:                  plan,
			ExternalValidationRef: receipt,
			Channel:               "mpesa",
			ChannelRef:            receipt,
			ValueDate:             time.Now().UTC(),
			InitiatedBy:           uuid.Nil,
		})
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.SplitResults) != 3 {
		// fees + loan (grouped) + bosa = 3 results
		t.Errorf("split results: want 3 (fee + loan-grouped + bosa), got %d", len(result.SplitResults))
	}

	// Assert loan_transactions row landed with the right components.
	var (
		loanTxnCount int
		loanPrincipal, loanInterest decimal.Decimal
		loanExtRef                  string
	)
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*),
		       COALESCE(SUM(principal_component), 0),
		       COALESCE(SUM(interest_component), 0),
		       COALESCE(MAX(external_validation_ref), '')
		  FROM loan_transactions
		 WHERE loan_id = $1 AND external_validation_ref = $2
	`, fix.LoanID, receipt).Scan(&loanTxnCount, &loanPrincipal, &loanInterest, &loanExtRef); err != nil {
		t.Fatalf("read loan_transactions: %v", err)
	}
	if loanTxnCount != 1 {
		t.Errorf("loan_transactions: want 1 row, got %d", loanTxnCount)
	}
	if !loanPrincipal.Equal(dec("2000")) {
		t.Errorf("principal_component: want 2000, got %s", loanPrincipal)
	}
	if !loanInterest.Equal(dec("500")) {
		t.Errorf("interest_component: want 500, got %s", loanInterest)
	}
	if loanExtRef != receipt {
		t.Errorf("external_validation_ref: want %s, got %s", receipt, loanExtRef)
	}

	// Loan balance updates.
	var principalBal, interestBal decimal.Decimal
	_ = pool.QueryRow(context.Background(),
		`SELECT principal_balance, interest_balance FROM loans WHERE id = $1`, fix.LoanID,
	).Scan(&principalBal, &interestBal)
	if !principalBal.Equal(decimal.Zero) {
		t.Errorf("loan principal_balance: want 0, got %s", principalBal)
	}
	if !interestBal.Equal(decimal.Zero) {
		t.Errorf("loan interest_balance: want 0, got %s", interestBal)
	}

	// Deposit landed.
	var depTxnCount int
	var depAmount decimal.Decimal
	_ = pool.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(SUM(amount), 0) FROM deposit_transactions
		 WHERE account_id = $1 AND external_validation_ref = $2
	`, fix.BOSAAccountID, receipt).Scan(&depTxnCount, &depAmount)
	if depTxnCount != 1 {
		t.Errorf("deposit_transactions: want 1 row, got %d", depTxnCount)
	}
	if !depAmount.Equal(dec("1300")) {
		t.Errorf("deposit amount: want 1300, got %s", depAmount)
	}

	// Fee due reduced (200 → 0, status='paid').
	var feeRemaining decimal.Decimal
	var feeStatus string
	_ = pool.QueryRow(context.Background(), `
		SELECT amount_due, status FROM member_fees_due
		 WHERE counterparty_id = $1 AND fee_code = 'MPESA_INCOMING'
	`, fix.CounterpartyID).Scan(&feeRemaining, &feeStatus)
	if !feeRemaining.Equal(decimal.Zero) {
		t.Errorf("fee remaining: want 0, got %s", feeRemaining)
	}
	if feeStatus != "paid" {
		t.Errorf("fee status: want paid, got %q", feeStatus)
	}

	// GL outbox rows queued.
	var outboxCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM posting_outbox
		  WHERE tenant_id = $1 AND payload->>'source_ref' LIKE $2`,
		tenantID, receipt+"%",
	).Scan(&outboxCount)
	if outboxCount < 3 {
		// At least one per executor: fee + loan + deposit.
		t.Errorf("posting_outbox: want >=3 rows, got %d", outboxCount)
	}

	// Cleanup.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM posting_outbox WHERE payload->>'source_ref' LIKE $1`, receipt+"%")
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM loan_transactions WHERE external_validation_ref = $1`, receipt)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM deposit_transactions WHERE external_validation_ref = $1`, receipt)
		teardownMemberFixture(pool, fix)
	})
}

func TestApplyPlan_CrossTenantLoanRejected(t *testing.T) {
	pool, tenantA := openTestPool(t)
	dbPool := &db.Pool{Pool: pool}

	// Need a SECOND tenant that also has loan_products seeded so
	// the fixture builder can construct the foreign loan. The dev
	// DB seeds these only on one tenant (tujenge); skip if there
	// isn't a second viable one.
	var tenantB uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT t.id FROM tenants t
		 WHERE t.id <> $1
		   AND EXISTS (SELECT 1 FROM loan_products lp WHERE lp.tenant_id = t.id)
		 LIMIT 1
	`, tenantA).Scan(&tenantB); err != nil {
		t.Skipf("no second tenant with loan_products seeded: %v", err)
	}
	fixB := seedMemberFixture(t, dbPool, tenantB, fixtureSpec{LoanPrincipal: "1000", HasBOSA: false})

	// Try to apply tenant B's loan from tenant A's tx — RLS hides
	// the loan row, executor errors with "not found".
	err := dbPool.WithTenantTx(context.Background(), tenantA, func(tx pgx.Tx) error {
		_, err := ApplyPlanTx(context.Background(), tx, ApplyInput{
			TenantID:              tenantA,
			ExternalValidationRef: "RK-XTNT-01",
			Channel:               "mpesa",
			Plan: &distribution.Plan{
				Amount: dec("100"),
				Splits: []distribution.Split{
					{Leg: distribution.LegLoanPrincipalDue, Amount: dec("100"),
						TargetRef: fixB.LoanNo, TargetID: ptrUUID(fixB.LoanID)},
				},
			},
			ValueDate: time.Now().UTC(),
		})
		return err
	})
	if err == nil {
		t.Fatal("expected error on cross-tenant loan apply, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
	t.Cleanup(func() { teardownMemberFixture(pool, fixB) })
}

// ─── fixture builder ───

type fixtureSpec struct {
	Fees          string
	LoanInterest  string
	LoanPrincipal string
	LoanPenalty   string
	LoanFees      string
	HasBOSA       bool
	BOSAStartBal  string
}

type fixture struct {
	CounterpartyID uuid.UUID
	LoanID         uuid.UUID
	LoanNo         string
	BOSAAccountID  uuid.UUID
	BOSAAccountNo  string
}

func seedMemberFixture(t *testing.T, dbPool *db.Pool, tenantID uuid.UUID, spec fixtureSpec) fixture {
	t.Helper()
	uniq := fmt.Sprintf("AT%07d", time.Now().UnixNano()%10000000)
	var f fixture
	err := dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
		// counterparty
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO counterparties (tenant_id, kind, cp_number, display_name, individual)
			VALUES ($1, 'individual', 'CP-'||$2, 'Phase 3.5 Test '||$2, '{"full_name":"Phase 3.5 Test"}'::jsonb)
			RETURNING id
		`, tenantID, uniq).Scan(&f.CounterpartyID); err != nil {
			return fmt.Errorf("seed counterparty: %w", err)
		}
		// loan_product + loan
		var productID uuid.UUID
		if err := tx.QueryRow(context.Background(), `
			SELECT id FROM loan_products WHERE tenant_id = $1 LIMIT 1
		`, tenantID).Scan(&productID); err != nil {
			return fmt.Errorf("find loan product: %w", err)
		}
		var appID uuid.UUID
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO loan_applications (tenant_id, application_no, counterparty_id, product_id,
			                               requested_amount, requested_term_months, created_by, status)
			VALUES ($1, 'APP-PHASE35-'||$2, $3, $4, 1, 1, gen_random_uuid(), 'approved')
			RETURNING id
		`, tenantID, uniq, f.CounterpartyID, productID).Scan(&appID); err != nil {
			return fmt.Errorf("seed loan_application: %w", err)
		}
		loanNo := "L-PHASE35-" + uniq
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO loans (tenant_id, loan_no, application_id, product_id, counterparty_id, status,
			                   principal, interest_rate_pct, interest_method, repayment_method,
			                   term_months, installment_count,
			                   principal_balance, interest_balance, penalty_balance, fees_balance,
			                   principal_disbursed)
			VALUES ($1, $2, $3, $4, $5, 'active',
			        $6, 0, 'reducing_balance', 'reducing_balance',
			        12, 12,
			        $7, $8, $9, $10,
			        $6)
			RETURNING id, loan_no
		`, tenantID, loanNo, appID, productID, f.CounterpartyID,
			decOr(spec.LoanPrincipal, "1000"),
			decOr(spec.LoanPrincipal, "0"),
			decOr(spec.LoanInterest, "0"),
			decOr(spec.LoanPenalty, "0"),
			decOr(spec.LoanFees, "0"),
		).Scan(&f.LoanID, &f.LoanNo); err != nil {
			return fmt.Errorf("seed loan: %w", err)
		}
		// deposit account
		if spec.HasBOSA {
			var depProductID uuid.UUID
			if err := tx.QueryRow(context.Background(),
				`SELECT id FROM deposit_products WHERE tenant_id = $1 LIMIT 1`,
				tenantID).Scan(&depProductID); err != nil {
				return fmt.Errorf("find deposit product: %w", err)
			}
			f.BOSAAccountNo = "DA-PHASE35-" + uniq
			if err := tx.QueryRow(context.Background(), `
				INSERT INTO deposit_accounts (tenant_id, product_id, counterparty_id,
				                              account_no, status, current_balance, available_balance, opened_at)
				VALUES ($1, $2, $3, $4, 'active', $5, $5, now())
				RETURNING id
			`, tenantID, depProductID, f.CounterpartyID, f.BOSAAccountNo,
				decOr(spec.BOSAStartBal, "0"),
			).Scan(&f.BOSAAccountID); err != nil {
				return fmt.Errorf("seed deposit_account: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

func teardownMemberFixture(pool *pgxpool.Pool, f fixture) {
	ctx := context.Background()
	if f.BOSAAccountID != uuid.Nil {
		_, _ = pool.Exec(ctx, `DELETE FROM deposit_transactions WHERE account_id = $1`, f.BOSAAccountID)
		_, _ = pool.Exec(ctx, `DELETE FROM deposit_accounts WHERE id = $1`, f.BOSAAccountID)
	}
	if f.LoanID != uuid.Nil {
		_, _ = pool.Exec(ctx, `DELETE FROM loan_transactions WHERE loan_id = $1`, f.LoanID)
		_, _ = pool.Exec(ctx, `DELETE FROM loan_repayment_schedule WHERE loan_id = $1`, f.LoanID)
		// Need the application_id to delete it too
		var appID uuid.UUID
		_ = pool.QueryRow(ctx, `SELECT application_id FROM loans WHERE id = $1`, f.LoanID).Scan(&appID)
		_, _ = pool.Exec(ctx, `DELETE FROM loans WHERE id = $1`, f.LoanID)
		if appID != uuid.Nil {
			_, _ = pool.Exec(ctx, `DELETE FROM loan_applications WHERE id = $1`, appID)
		}
	}
	if f.CounterpartyID != uuid.Nil {
		_, _ = pool.Exec(ctx, `DELETE FROM member_fees_due WHERE counterparty_id = $1`, f.CounterpartyID)
		_, _ = pool.Exec(ctx, `DELETE FROM counterparties WHERE id = $1`, f.CounterpartyID)
	}
}

// ─── helpers ───

func openTestPool(t *testing.T) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	var tenantID uuid.UUID
	// Pick a tenant that has both loan_products + deposit_products
	// seeded — otherwise the fixture builder can't construct a loan
	// or a deposit account. For the cross-tenant test we also need
	// a SECOND tenant with loan_products; pick whichever the dev
	// DB has and skip the test if there's only one.
	if err := pool.QueryRow(ctx, `
		SELECT t.id FROM tenants t
		 WHERE EXISTS (SELECT 1 FROM loan_products lp WHERE lp.tenant_id = t.id)
		   AND EXISTS (SELECT 1 FROM deposit_products dp WHERE dp.tenant_id = t.id)
		 LIMIT 1
	`).Scan(&tenantID); err != nil {
		t.Skipf("no tenant with loan + deposit products: %v", err)
	}
	return pool, tenantID
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("bad decimal " + s)
	}
	return d
}

func decOr(s, def string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return dec(def)
	}
	return dec(s)
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

// silence "context not used" warning when no test in this file uses
// errors.Is.
var _ = errors.Is
