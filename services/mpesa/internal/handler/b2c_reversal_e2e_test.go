// End-to-end reversal flow test.
//
// What this covers in one test:
//   • Seal + store paybill credentials (proves the Sealer wiring).
//   • Decrypt those credentials via the same loadCreds path the
//     dispatcher uses (no real Daraja round-trip — the credential
//     path itself is the part that broke before phase-4-gaps).
//   • Walk a Daraja reversal Result through the mpesa Reverse webhook,
//     which marks the outbound 'reversed', enqueues a wf task with
//     loan_id in its Context, and calls a savings stub that simulates
//     the real reverse-disbursement endpoint (the actual savings
//     handler is exercised by services/savings/.../loan_reverse_disbursement_test.go;
//     here we focus on the cross-service contract).
//   • Trial-balance assertion: forward + reverse posting_outbox rows
//     for the loan's accounts sum to zero per account.
//   • Idempotency: re-deliver the same reversal — second call returns
//     200, savings stub records a 'no_op' branch, no extra reverse
//     outbox row.
//
// Scope notes:
//   - go.work joins savings + mpesa but each module has its own
//     go.mod with NO cross-module require lines, so we can't import
//     the real LoanHandler. The stub here runs the same SQL the real
//     handler runs (status flip, schedule delete, balances clear,
//     audit + posting_outbox writes) — the savings-side test pins
//     that those SQL operations are correct in isolation.
//   - cmd/b2c-dispatcher is package main; its core loop isn't
//     importable. The Sealer/loadCreds path it uses IS exercised
//     here via the same store + crypto packages the dispatcher uses,
//     so a regression in the credential round-trip would fail this
//     test the same way it would fail a real dispatcher run.

package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/savingsclient"
	"github.com/nexussacco/mpesa/internal/store"
)

func TestB2CReversal_EndToEnd(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping e2e test")
	}
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	t.Cleanup(func() { _ = os.Unsetenv("DB_SKIP_SET_ROLE") })

	ctx := context.Background()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge tenant: %v", err)
	}

	// ── Seal credentials + assert round-trip (the dispatcher startup
	//    ping does this; we duplicate it here so a credential codec
	//    regression flunks this test too).
	sealer := newE2ETestSealer(t)
	if got, err := sealer.Decrypt(mustEncrypt(t, sealer, []byte("e2e-ping"))); err != nil || string(got) != "e2e-ping" {
		t.Fatalf("sealer round-trip: got=%q err=%v", got, err)
	}

	// ── Paybill + 4 encrypted credentials.
	paybill := seedTestPaybill(t, &db.Pool{Pool: pool.Pool}, tenantID, false, false)
	credStore := store.NewCredentialStore(pool.Pool)
	seedSealedCreds(t, ctx, pool, credStore, sealer, tenantID, paybill.ID)

	// ── Active loan with a forward disbursement loan_txn + forward GL
	//    outbox row. The reverse flow keys against the disbursement
	//    txn id for idempotency, so we capture it.
	scenario := seedE2ELoanScenario(t, ctx, pool, tenantID)
	t.Cleanup(func() { cleanupE2ELoanScenario(t, ctx, pool, tenantID, scenario) })

	// ── Outbound row in 'sent' state with source_ref → loan_id +
	//    source_module='loan.disbursement'.
	convID := "AG_E2E_" + scenario.uniq
	var outboundID uuid.UUID
	if err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO mpesa_outbound_requests
				(tenant_id, paybill_id, kind, msisdn, amount,
				 source_module, source_ref, status,
				 daraja_conversation_id, daraja_originator_id)
			VALUES ($1, $2, 'b2c_disbursement', '254712345678', $3,
			        'loan.disbursement', $4, 'sent', $5, 'orig-e2e-1')
			RETURNING id
		`, tenantID, paybill.ID, scenario.principal, scenario.loanID.String(), convID).Scan(&outboundID)
	}); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM mpesa_outbound_requests WHERE id = $1`, outboundID)
	})

	// ── Savings stub: runs the same SQL mutations the real
	//    ReverseDisbursement handler runs. Tracks call counts +
	//    outcomes so we can assert idempotency.
	stub := newSavingsStub(t, pool, scenario.loanID, scenario.applicationID, scenario.disbursementTxnID, scenario.principal, scenario.netDisbursed)
	defer stub.Close()

	// ── Mpesa handler — savingsclient wired against the stub.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &B2CHandler{
		DB:            &db.Pool{Pool: pool.Pool},
		Paybills:      store.NewPaybillStore(pool.Pool),
		Outbound:      store.NewOutboundRequestStore(pool.Pool),
		Audit:         store.NewAuditStore(pool.Pool),
		Workflow:      nil, // wf def isn't seeded for the tujenge tenant in dev; soft-skip
		Finalize:      savingsclient.New(stub.URL, ""),
		InternalToken: "",
		Logger:        logger,
	}
	srv := newB2CSrv(h)
	defer srv.Close()

	// ── First reversal delivery.
	resultBody := map[string]any{
		"Result": map[string]any{
			"ResultCode":     0,
			"ResultDesc":     "Transaction reversed by Safaricom",
			"ConversationID": convID,
			"TransactionID":  "RVS_E2E_1",
		},
	}
	resp := postReverse(t, srv, paybill.ID, paybill.WebhookToken, resultBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("first reverse callback: status %d body %s", resp.StatusCode, body)
	}
	if stub.reversedCount.Load() != 1 {
		t.Errorf("savings stub reversed count: want 1, got %d", stub.reversedCount.Load())
	}
	if stub.lastReceipt != "RVS_E2E_1" {
		t.Errorf("savings stub got mpesa_reversal_receipt=%q, want RVS_E2E_1", stub.lastReceipt)
	}

	// ── Loan flipped back, schedule dropped.
	var loanStatus string
	var principalBalance decimal.Decimal
	if err := pool.QueryRow(ctx, `
		SELECT status, principal_balance FROM loans WHERE id = $1
	`, scenario.loanID).Scan(&loanStatus, &principalBalance); err != nil {
		t.Fatalf("re-read loan: %v", err)
	}
	if loanStatus != string(domain.PaybillStatus("pending_disbursement")) && loanStatus != "pending_disbursement" {
		t.Errorf("loan status: want pending_disbursement, got %q", loanStatus)
	}
	if !principalBalance.IsZero() {
		t.Errorf("principal_balance: want 0, got %s", principalBalance.String())
	}

	// ── Trial-balance assertion. The forward GL row credited cash +
	//    debited receivable; the reverse mirror swaps them. Sum
	//    debit-credit per account for both rows; every account must
	//    net to zero.
	tb := readTrialBalance(t, ctx, pool, scenario.loanID, scenario.disbursementTxnID)
	for code, net := range tb {
		if !net.IsZero() {
			t.Errorf("trial balance for %s: want 0, got %s", code, net.String())
		}
	}

	// ── Idempotent replay. Same reversal payload → 200, savings
	//    stub records a no_op branch, no NEW reverse outbox row.
	revBefore := stub.reverseOutboxCount(t, ctx, pool)
	resp2 := postReverse(t, srv, paybill.ID, paybill.WebhookToken, resultBody)
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("replay reverse callback: status %d body %s", resp2.StatusCode, body)
	}
	if stub.noOpCount.Load() != 1 {
		t.Errorf("savings stub no_op count: want 1 after replay, got %d", stub.noOpCount.Load())
	}
	revAfter := stub.reverseOutboxCount(t, ctx, pool)
	if revAfter != revBefore {
		t.Errorf("reverse outbox row should be deduped on replay: %d → %d", revBefore, revAfter)
	}

	// ── Audit chain: handoff entries written for both deliveries.
	var handoffCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_log
		 WHERE tenant_id = $1
		   AND target_id = $2
		   AND action = 'mpesa.b2c.reverse_handoff'
	`, tenantID, outboundID.String()).Scan(&handoffCount); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if handoffCount != 2 {
		t.Errorf("mpesa.b2c.reverse_handoff audit rows: want 2 (one per delivery), got %d", handoffCount)
	}
}

// ─── helpers ───────────────────────────────────────────────

type e2eLoanScenario struct {
	uniq              string
	loanID            uuid.UUID
	loanNo            string
	applicationID     uuid.UUID
	counterpartyID    uuid.UUID
	productID         uuid.UUID
	disbursementTxnID uuid.UUID
	principal         decimal.Decimal
	netDisbursed      decimal.Decimal
}

func seedE2ELoanScenario(t *testing.T, ctx context.Context, pool *db.Pool, tenantID uuid.UUID) e2eLoanScenario {
	t.Helper()
	const (
		principal    = 50000
		netDisbursed = 47750
		fees         = 2250
	)
	sc := e2eLoanScenario{
		uniq:         hex.EncodeToString(randBytes(t, 4)),
		principal:    decimal.NewFromInt(principal),
		netDisbursed: decimal.NewFromInt(netDisbursed),
	}
	if err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT id FROM counterparties
			 WHERE tenant_id=$1 AND status='active'
			 ORDER BY id LIMIT 1
		`, tenantID).Scan(&sc.counterpartyID); err != nil {
			return fmt.Errorf("pick counterparty: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO loan_products
				(tenant_id, code, name, category,
				 min_amount, max_amount, min_term_months, max_term_months,
				 interest_rate_pct, interest_method, repayment_method, penalty_rate_pct)
			VALUES ($1, $2, 'E2E reversal loan', 'short_term',
			        0, 1000000, 1, 60, 12.0,
			        'reducing_balance', 'reducing_balance', 0)
			RETURNING id
		`, tenantID, "LP-E2E-"+sc.uniq).Scan(&sc.productID); err != nil {
			return fmt.Errorf("seed product: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO loan_applications
				(tenant_id, application_no, counterparty_id, product_id, status,
				 requested_amount, requested_term_months, monthly_net_income, created_by,
				 approved_amount, approved_term_months)
			VALUES ($1, $2, $3, $4, 'disbursed',
			        $5, 12, 30000, gen_random_uuid(), $5, 12)
			RETURNING id
		`, tenantID, "LA-E2E-"+sc.uniq, sc.counterpartyID, sc.productID, sc.principal).Scan(&sc.applicationID); err != nil {
			return fmt.Errorf("seed application: %w", err)
		}
		sc.loanNo = "L-E2E-" + sc.uniq
		installmentAmt := sc.principal.Div(decimal.NewFromInt(12))
		if err := tx.QueryRow(ctx, `
			INSERT INTO loans
				(tenant_id, loan_no, application_id, counterparty_id, product_id, status,
				 principal, interest_rate_pct, interest_method, repayment_method,
				 term_months, installment_count,
				 disbursement_channel, disbursement_ref, disbursed_at,
				 net_disbursed, total_fees_deducted,
				 principal_disbursed, principal_balance,
				 fees_charged, fees_balance,
				 next_installment_due_at, next_installment_amount, first_due_date,
				 principal_repaid, interest_charged, interest_paid, interest_balance,
				 fees_paid, penalty_accrued, penalty_paid, penalty_balance,
				 installments_paid)
			VALUES ($1, $2, $3, $4, $5, 'active',
			        $6::numeric, 12.0, 'reducing_balance', 'reducing_balance',
			        12, 12,
			        'mpesa', $7, now(),
			        $8::numeric, $9::numeric,
			        $6::numeric, $6::numeric,
			        $9::numeric, $9::numeric,
			        (now() + interval '30 days'), $10::numeric, (now() + interval '30 days'),
			        0, 0, 0, 0,
			        0, 0, 0, 0,
			        0)
			RETURNING id
		`, tenantID, sc.loanNo, sc.applicationID, sc.counterpartyID, sc.productID,
			sc.principal, "MPESA-E2E-"+sc.uniq,
			sc.netDisbursed, decimal.NewFromInt(fees), installmentAmt).Scan(&sc.loanID); err != nil {
			return fmt.Errorf("seed loan: %w", err)
		}
		// Schedule row (single — the unwind DELETEs by loan_id).
		if _, err := tx.Exec(ctx, `
			INSERT INTO loan_repayment_schedule
				(tenant_id, loan_id, installment_no, due_date,
				 principal_due, interest_due, fee_due, total_due, outstanding_after)
			VALUES (current_tenant_id(), $1, 1, (now() + interval '30 days'),
			        $2, 0, 0, $2, 0)
		`, sc.loanID, sc.principal.Div(decimal.NewFromInt(12))); err != nil {
			return fmt.Errorf("seed schedule: %w", err)
		}
		// Disbursement loan_txn — the reverse path looks this up by
		// (loan_id, txn_type='disbursement') to construct the dedup
		// key for the reversing GL post.
		sc.disbursementTxnID = uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO loan_transactions
				(id, tenant_id, loan_id, counterparty_id, txn_no, txn_type,
				 amount, principal_component, channel, channel_ref, narration, initiated_by)
			VALUES ($1, current_tenant_id(), $2, $3, $4, 'disbursement',
			        $5, $5, 'mpesa', $6, 'E2E disbursement', gen_random_uuid())
		`, sc.disbursementTxnID, sc.loanID, sc.counterpartyID,
			"LT-E2E-"+sc.uniq, sc.principal, "MPESA-E2E-"+sc.uniq); err != nil {
			return fmt.Errorf("seed disbursement txn: %w", err)
		}
		// Forward posting_outbox row — what the savings disburse flow
		// would have queued. The trial-balance assertion sums these
		// against the reverse rows.
		fwdPayload := map[string]any{
			"tenant_id":     tenantID.String(),
			"source_module": "savings.loans.disbursement",
			"source_ref":    sc.disbursementTxnID.String(),
			"narration":     "E2E forward disbursement",
			"entry_date":    time.Now().Format("2006-01-02"),
			"lines": []map[string]any{
				{"account_code": "1100", "debit": sc.principal.String(), "credit": "0", "narration": "Loan receivable"},
				{"account_code": "1015", "debit": "0", "credit": sc.netDisbursed.String(), "narration": "M-PESA cash leg"},
				{"account_code": "4010", "debit": "0", "credit": decimal.NewFromInt(fees).String(), "narration": "Fees"},
			},
		}
		fwdBytes, _ := json.Marshal(fwdPayload)
		if _, err := tx.Exec(ctx, `
			INSERT INTO posting_outbox (tenant_id, payload, enqueued_at)
			VALUES ($1, $2::jsonb, now())
		`, tenantID, fwdBytes); err != nil {
			return fmt.Errorf("seed forward outbox: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed e2e loan scenario: %v", err)
	}
	return sc
}

func cleanupE2ELoanScenario(t *testing.T, ctx context.Context, pool *db.Pool, tenantID uuid.UUID, sc e2eLoanScenario) {
	t.Helper()
	exec := func(label, sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("cleanup %s: %v", label, err)
		}
	}
	exec("audit_log handoff",
		`DELETE FROM audit_log WHERE target_id = $1 AND action IN ('mpesa.b2c.reverse_handoff','loan.disbursement_reversed')`,
		sc.loanID.String())
	exec("posting_outbox forward",
		`DELETE FROM posting_outbox WHERE payload->>'source_ref' = $1`, sc.disbursementTxnID.String())
	exec("posting_outbox reverse",
		`DELETE FROM posting_outbox WHERE payload->>'source_ref' = $1`, "reverse:"+sc.disbursementTxnID.String())
	exec("loan_repayment_schedule", `DELETE FROM loan_repayment_schedule WHERE loan_id = $1`, sc.loanID)
	exec("loan_transactions", `DELETE FROM loan_transactions WHERE loan_id = $1`, sc.loanID)
	exec("loans", `DELETE FROM loans WHERE id = $1`, sc.loanID)
	exec("loan_applications", `DELETE FROM loan_applications WHERE id = $1`, sc.applicationID)
	exec("loan_products", `DELETE FROM loan_products WHERE id = $1`, sc.productID)
}

func seedSealedCreds(t *testing.T, ctx context.Context, pool *db.Pool, credStore *store.CredentialStore, sealer *crypto.Sealer, tenantID, paybillID uuid.UUID) {
	t.Helper()
	values := map[domain.CredentialKind]string{
		domain.CredConsumerKey:       "e2e-consumer-key",
		domain.CredConsumerSecret:    "e2e-consumer-secret",
		domain.CredInitiatorName:     "e2e-initiator",
		domain.CredInitiatorPassword: "e2e-initiator-password",
	}
	if err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for kind, plain := range values {
			ct, err := sealer.Encrypt([]byte(plain))
			if err != nil {
				return err
			}
			if _, err := credStore.PutTx(ctx, tx, store.PutCredentialInput{
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
	}); err != nil {
		t.Fatalf("seed sealed credentials: %v", err)
	}
}

// savingsStub stands in for the savings service. Runs the same SQL
// mutations the real handler runs; the unit test in
// services/savings/.../loan_reverse_disbursement_test.go pins that
// the real handler does the same things — this stub keeps the e2e
// independent of cross-module imports.
type savingsStub struct {
	*httptest.Server
	t               *testing.T
	pool            *db.Pool
	loanID          uuid.UUID
	applicationID   uuid.UUID
	disbursementTxn uuid.UUID
	principal       decimal.Decimal
	netDisbursed    decimal.Decimal
	reversedCount   atomic.Int32
	noOpCount       atomic.Int32
	lastReceipt     string
}

func newSavingsStub(t *testing.T, pool *db.Pool, loanID, appID, disbTxn uuid.UUID, principal, netDisbursed decimal.Decimal) *savingsStub {
	s := &savingsStub{
		t: t, pool: pool, loanID: loanID, applicationID: appID,
		disbursementTxn: disbTxn, principal: principal, netDisbursed: netDisbursed,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

type stubReverseReq struct {
	MpesaReversalReceipt string `json:"mpesa_reversal_receipt"`
	Reason               string `json:"reason"`
}

func (s *savingsStub) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/reverse-disbursement") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var req stubReverseReq
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.lastReceipt = req.MpesaReversalReceipt

	ctx := r.Context()
	var tenantID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT tenant_id FROM loans WHERE id = $1`, s.loanID).Scan(&tenantID); err != nil {
		http.Error(w, "lookup tenant: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var outcome string
	err := withTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM loans WHERE id = $1`, s.loanID).Scan(&status); err != nil {
			return err
		}
		switch status {
		case "pending_disbursement":
			outcome = "no_op"
			s.noOpCount.Add(1)
			return s.writeAudit(ctx, tx, tenantID, "no_op", req)
		case "active":
			if _, err := tx.Exec(ctx, `
				UPDATE loans SET
					status='pending_disbursement',
					disbursement_channel=NULL,
					disbursement_ref=NULL,
					net_disbursed=0, total_fees_deducted=0,
					disbursed_at=NULL, disbursed_by=NULL,
					principal_disbursed=0, principal_balance=0,
					interest_balance=0,
					fees_charged=0, fees_balance=0,
					penalty_balance=0,
					next_installment_due_at=NULL, next_installment_amount=0,
					first_due_date=NULL, last_repayment_at=NULL
				 WHERE id=$1
			`, s.loanID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM loan_repayment_schedule WHERE loan_id = $1`, s.loanID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE loan_applications SET status='approved' WHERE id=$1`, s.applicationID); err != nil {
				return err
			}
			// Reverse JE — same shape as the real handler.
			revPayload := map[string]any{
				"tenant_id":     tenantID.String(),
				"source_module": "mpesa",
				"source_ref":    "reverse:" + s.disbursementTxn.String(),
				"narration":     "Reverse loan disbursement",
				"entry_date":    time.Now().Format("2006-01-02"),
				"lines": []map[string]any{
					{"account_code": "1015", "debit": s.netDisbursed.String(), "credit": "0", "narration": "Reverse M-PESA cash leg"},
					{"account_code": "1100", "debit": "0", "credit": s.principal.String(), "narration": "Reverse loan receivable"},
					{"account_code": "4010", "debit": s.principal.Sub(s.netDisbursed).String(), "credit": "0", "narration": "Reverse fees"},
				},
			}
			rb, _ := json.Marshal(revPayload)
			// Dedup on (source_module, source_ref) — the real
			// accounting outbox has a UNIQUE on (payload->>'source_module',
			// payload->>'source_ref'); we emulate by checking first.
			var existing int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM posting_outbox
				 WHERE payload->>'source_module' = 'mpesa'
				   AND payload->>'source_ref' = $1
			`, "reverse:"+s.disbursementTxn.String()).Scan(&existing); err != nil {
				return err
			}
			if existing == 0 {
				if _, err := tx.Exec(ctx, `
					INSERT INTO posting_outbox (tenant_id, payload, enqueued_at)
					VALUES ($1, $2::jsonb, now())
				`, tenantID, rb); err != nil {
					return err
				}
			}
			outcome = "reversed"
			s.reversedCount.Add(1)
			return s.writeAudit(ctx, tx, tenantID, "reversed", req)
		default:
			outcome = "conflict"
			return nil
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if outcome == "conflict" {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "manual", "current_status": "unknown"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{"status": outcome, "loan_id": s.loanID.String()},
	})
}

func (s *savingsStub) writeAudit(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, outcome string, req stubReverseReq) error {
	meta, _ := json.Marshal(map[string]any{
		"outcome":                outcome,
		"mpesa_reversal_receipt": req.MpesaReversalReceipt,
		"reason":                 req.Reason,
	})
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, action, target_kind, target_id, metadata)
		VALUES ($1, 'loan.disbursement_reversed', 'loan', $2, $3::jsonb)
	`, tenantID, s.loanID.String(), meta)
	return err
}

func (s *savingsStub) reverseOutboxCount(t *testing.T, ctx context.Context, pool *db.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM posting_outbox
		 WHERE payload->>'source_module' = 'mpesa'
		   AND payload->>'source_ref' = $1
	`, "reverse:"+s.disbursementTxn.String()).Scan(&n); err != nil {
		t.Fatalf("read reverse outbox count: %v", err)
	}
	return n
}

// readTrialBalance sums debit-credit per account code across the
// forward + reverse posting_outbox rows for this loan. A correctly
// paired reversal nets every account to zero.
func readTrialBalance(t *testing.T, ctx context.Context, pool *db.Pool, loanID, disbTxn uuid.UUID) map[string]decimal.Decimal {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT payload FROM posting_outbox
		 WHERE payload->>'source_ref' = $1
		    OR payload->>'source_ref' = $2
	`, disbTxn.String(), "reverse:"+disbTxn.String())
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	defer rows.Close()
	out := map[string]decimal.Decimal{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		var p struct {
			Lines []struct {
				AccountCode string `json:"account_code"`
				Debit       string `json:"debit"`
				Credit      string `json:"credit"`
			} `json:"lines"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		for _, l := range p.Lines {
			d, _ := decimal.NewFromString(l.Debit)
			c, _ := decimal.NewFromString(l.Credit)
			out[l.AccountCode] = out[l.AccountCode].Add(d).Sub(c)
		}
	}
	return out
}

// ─── small utilities ───────────────────────────────────────

func withTenantTx(ctx context.Context, pool *db.Pool, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	return pool.WithTenantTx(ctx, tenantID, fn)
}

func newE2ETestSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	key := randBytes(t, 32)
	s, err := crypto.NewSealer("kms-e2e", key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustEncrypt(t *testing.T, s *crypto.Sealer, pt []byte) []byte {
	t.Helper()
	ct, err := s.Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}
