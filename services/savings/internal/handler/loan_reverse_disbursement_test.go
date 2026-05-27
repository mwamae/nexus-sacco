// End-to-end acceptance test for the reverse-disbursement endpoint.
//
// Property under test: a loan that was disbursed via M-PESA (channel
// 'mpesa', status 'active') can be unwound by POST'ing the reverse-
// disbursement endpoint. After the call the loan is back to
// 'pending_disbursement', the cached balances are zero, the
// repayment schedule is gone, the application is back to 'approved',
// and a reversing journal entry is queued on the posting outbox.
//
// We seed the "active" loan state directly (UPDATE loans + schedule
// + posting_outbox) rather than running the Disburse handler with
// channel=mpesa — channel=mpesa is async in phase 4 (queues a B2C
// outbound row + defers the executor until the Daraja Result lands),
// and we want to exercise the reversal in isolation.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

func TestReverseDisbursement_UnwindsActiveLoanAndPostsReversingJE(t *testing.T) {
	// The internal endpoint resolves the tenant by reading the loan
	// row OUTSIDE any tenant-scoped tx (mirrors FinalizeDisbursement).
	// Under nexus_app + RLS that lookup returns zero rows, so for the
	// test we open the pool with DB_SKIP_SET_ROLE=1 — the same flag
	// the migration runner uses. Production wiring of this latent
	// behaviour is the same for the existing FinalizeDisbursement
	// endpoint and is out of scope to refactor here.
	prev := os.Getenv("DB_SKIP_SET_ROLE")
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	t.Cleanup(func() { _ = os.Setenv("DB_SKIP_SET_ROLE", prev) })

	env := newTestEnv(t)
	if env == nil {
		return
	}
	defer env.close()

	sc := seedLoanDisburseScenario(t, env)
	defer cleanupLoanDisburseScenario(t, env, sc)

	// Promote the loan to 'active' as if mpesa had successfully
	// disbursed. principal=50000, fees=2250 (matches the scenario's
	// 2.5%/1%/500 fees), net=47750.
	const (
		fees = 2250
		net  = 47750
	)
	disbursementTxnID := uuid.New()
	disbursementTxnNo := "LT-RREVE-" + env.MarkerSuffix
	if err := env.Pool.WithTenantTx(context.Background(), env.TenantID, func(tx pgx.Tx) error {
		// Promote loan + flip app status (mirror MarkDisbursedTx + the
		// final UpdateStatusTx in ExecuteDisbursementTx).
		if _, err := tx.Exec(context.Background(), `
			UPDATE loans SET
				status = 'active',
				disbursement_channel = 'mpesa',
				disbursement_ref = $2,
				net_disbursed = $3,
				total_fees_deducted = $4,
				disbursed_at = now(),
				disbursed_by = $5,
				principal_disbursed = principal,
				principal_balance = principal,
				fees_charged = $4,
				fees_balance = $4,
				next_installment_due_at = (now() + interval '30 days'),
				next_installment_amount = principal / term_months,
				first_due_date = (now() + interval '30 days')
			WHERE id = $1
		`, sc.LoanID, "MPESA-RVS-"+env.MarkerSuffix,
			decimal.NewFromInt(net), decimal.NewFromInt(fees), env.UserID); err != nil {
			return fmt.Errorf("promote loan: %w", err)
		}
		if _, err := tx.Exec(context.Background(),
			`UPDATE loan_applications SET status = 'disbursed' WHERE id = $1`, sc.AppID,
		); err != nil {
			return fmt.Errorf("flip app to disbursed: %w", err)
		}
		// Synthesise a schedule (one row is enough to prove the DELETE
		// fires; the executor would generate 12 for this loan).
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO loan_repayment_schedule
			  (tenant_id, loan_id, installment_no, due_date,
			   principal_due, interest_due, fee_due, total_due, outstanding_after)
			VALUES (current_tenant_id(), $1, 1, (now() + interval '30 days'),
			        $2, 0, 0, $2, 0)
		`, sc.LoanID, decimal.NewFromInt(50000/12)); err != nil {
			return fmt.Errorf("seed schedule: %w", err)
		}
		// Forward disbursement loan_txn row — kept around so we can
		// assert the reverse endpoint does NOT delete history. (Tests
		// the audit-preservation property.)
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO loan_transactions
			  (id, tenant_id, loan_id, counterparty_id, txn_no, txn_type,
			   amount, principal_component, channel, channel_ref, narration, initiated_by)
			VALUES ($1, current_tenant_id(), $2, $3, $4, 'disbursement',
			        50000, 50000, 'mpesa', $5, 'Net disbursement', $6)
		`, disbursementTxnID, sc.LoanID, sc.CounterID, disbursementTxnNo,
			"MPESA-RVS-"+env.MarkerSuffix, env.UserID); err != nil {
			return fmt.Errorf("seed disbursement txn: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed active loan: %v", err)
	}

	// Wire the reverse-disbursement handler behind a minimal router.
	// X-Internal-Token gate is set so the test exercises the auth
	// path too (mirrors production wiring).
	const token = "test-internal-token-reverse"
	_ = os.Setenv("SAVINGS_INTERNAL_TOKEN", token)
	t.Cleanup(func() { _ = os.Unsetenv("SAVINGS_INTERNAL_TOKEN") })

	h := buildLoanHandlerForTest(env)
	r := chi.NewRouter()
	r.Route("/internal/v1", func(r chi.Router) {
		r.Post("/loans/{loan_id}/reverse-disbursement", h.ReverseDisbursement)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	body := map[string]any{
		"mpesa_reversal_receipt": "RVS_" + env.MarkerSuffix,
		"reason":                 "Safaricom reversal during acceptance test",
	}
	req := httpReq(t, http.MethodPost,
		srv.URL+"/internal/v1/loans/"+sc.LoanID.String()+"/reverse-disbursement", body)
	req.Header.Set("X-Internal-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST reverse-disbursement: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := readAll(res.Body)
		t.Fatalf("POST reverse-disbursement: want 200, got %d. body=%s", res.StatusCode, string(body))
	}
	respBody, _ := io.ReadAll(res.Body)
	// httpx.OK wraps the response in {"data": ...}
	var out struct {
		Data struct {
			Status string `json:"status"`
			LoanID string `json:"loan_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decode response: %v. raw=%s", err, respBody)
	}
	if out.Data.Status != "reversed" {
		t.Fatalf("response status: want 'reversed', got %q. body=%s", out.Data.Status, respBody)
	}

	// ── Assertions ──
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.Pool.WithTenantTx(ctx, env.TenantID, func(tx pgx.Tx) error {
		// 1. Loan reset to pending_disbursement, balances cleared.
		var status string
		var disbChannel *string
		var netDisb *decimal.Decimal
		var principalBalance decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT status, disbursement_channel, net_disbursed, principal_balance
			  FROM loans WHERE id = $1
		`, sc.LoanID).Scan(&status, &disbChannel, &netDisb, &principalBalance); err != nil {
			return fmt.Errorf("re-read loan: %w", err)
		}
		if status != string(domain.LoanPendingDisbursement) {
			t.Errorf("loan status: want %q, got %q", domain.LoanPendingDisbursement, status)
		}
		if disbChannel != nil && *disbChannel != "" {
			t.Errorf("disbursement_channel: want NULL, got %q", *disbChannel)
		}
		if netDisb != nil && !netDisb.IsZero() {
			t.Errorf("net_disbursed: want 0/NULL, got %s", netDisb.String())
		}
		if !principalBalance.IsZero() {
			t.Errorf("principal_balance: want 0, got %s", principalBalance.String())
		}

		// 2. Schedule rows deleted.
		var scheduleRows int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM loan_repayment_schedule WHERE loan_id = $1`, sc.LoanID,
		).Scan(&scheduleRows); err != nil {
			return err
		}
		if scheduleRows != 0 {
			t.Errorf("schedule rows: want 0 (DELETE'd), got %d", scheduleRows)
		}

		// 3. Original disbursement loan_txn row preserved (audit trail).
		var txnCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM loan_transactions WHERE id = $1`, disbursementTxnID,
		).Scan(&txnCount); err != nil {
			return err
		}
		if txnCount != 1 {
			t.Errorf("disbursement loan_txn must be preserved for audit: want 1, got %d", txnCount)
		}

		// 4. Application back to 'approved'.
		var appStatus string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM loan_applications WHERE id = $1`, sc.AppID,
		).Scan(&appStatus); err != nil {
			return err
		}
		if appStatus != string(domain.AppApproved) {
			t.Errorf("application status: want %q, got %q", domain.AppApproved, appStatus)
		}

		// 5. Reversing journal entry on the outbox. Dedup key per spec:
		//    source_module="mpesa", source_ref="reverse:<original_disbursement_txn_id>".
		reverseRef := "reverse:" + disbursementTxnID.String()
		var outboxCount int
		var payload []byte
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM posting_outbox
			 WHERE payload->>'source_module' = 'mpesa'
			   AND payload->>'source_ref' = $1
		`, reverseRef).Scan(&outboxCount); err != nil {
			return err
		}
		if outboxCount != 1 {
			t.Errorf("reversing outbox row (source_module=mpesa, source_ref=%s): want 1, got %d",
				reverseRef, outboxCount)
		}
		if outboxCount == 1 {
			if err := tx.QueryRow(ctx, `
				SELECT payload FROM posting_outbox
				 WHERE payload->>'source_module' = 'mpesa'
				   AND payload->>'source_ref' = $1
				 LIMIT 1
			`, reverseRef).Scan(&payload); err != nil {
				return err
			}
			var p struct {
				Lines []struct {
					AccountCode string `json:"account_code"`
					Debit       string `json:"debit"`
					Credit      string `json:"credit"`
				} `json:"lines"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return fmt.Errorf("decode reversing JE: %w", err)
			}
			// Sanity-check the reverse leg: 1015 DR, 1100 CR, fees DR.
			seen := map[string]struct{ d, c string }{}
			for _, l := range p.Lines {
				seen[l.AccountCode] = struct{ d, c string }{l.Debit, l.Credit}
			}
			if dc := seen["1015"]; !strings.HasPrefix(dc.d, "47750") {
				t.Errorf("reversing 1015: want DR 47750, got %+v", dc)
			}
			if dc := seen["1100"]; !strings.HasPrefix(dc.c, "50000") {
				t.Errorf("reversing 1100: want CR 50000, got %+v", dc)
			}
			// Fee accounts should be DR'd (mirror of original CR).
			for _, code := range []string{"4010", "4020", "4190"} {
				if dc := seen[code]; dc.d == "" || dc.d == "0" {
					t.Errorf("reversing %s: want non-zero DR, got %+v", code, dc)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("assertions: %v", err)
	}

	// 6. Idempotency: a second POST must return 'no_op' (loan is now
	//    pending_disbursement; nothing to unwind).
	req2 := httpReq(t, http.MethodPost,
		srv.URL+"/internal/v1/loans/"+sc.LoanID.String()+"/reverse-disbursement", body)
	req2.Header.Set("X-Internal-Token", token)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST reverse-disbursement (retry): %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("retry: want 200, got %d", res2.StatusCode)
	}
	var out2 struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.NewDecoder(res2.Body).Decode(&out2)
	if out2.Data.Status != "no_op" {
		t.Errorf("retry status: want 'no_op', got %q", out2.Data.Status)
	}

	// 7. Audit row written for the original reversal AND the no_op
	//    retry. Both have action='loan.disbursement_reversed' but
	//    different outcomes in metadata.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	var auditTotal, auditReversed, auditNoOp int
	if err := env.Pool.WithTenantTx(ctx2, env.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx2, `
			SELECT count(*) FROM audit_log
			 WHERE action = 'loan.disbursement_reversed'
			   AND target_id = $1
		`, sc.LoanID.String()).Scan(&auditTotal); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx2, `
			SELECT count(*) FROM audit_log
			 WHERE action = 'loan.disbursement_reversed'
			   AND target_id = $1
			   AND metadata->>'outcome' = 'reversed'
		`, sc.LoanID.String()).Scan(&auditReversed); err != nil {
			return err
		}
		return tx.QueryRow(ctx2, `
			SELECT count(*) FROM audit_log
			 WHERE action = 'loan.disbursement_reversed'
			   AND target_id = $1
			   AND metadata->>'outcome' = 'no_op'
		`, sc.LoanID.String()).Scan(&auditNoOp)
	}); err != nil {
		t.Fatalf("audit assertions: %v", err)
	}
	if auditTotal != 2 {
		t.Errorf("audit rows: want 2 (one reversed + one no_op), got %d", auditTotal)
	}
	if auditReversed != 1 {
		t.Errorf("audit outcome=reversed: want 1, got %d", auditReversed)
	}
	if auditNoOp != 1 {
		t.Errorf("audit outcome=no_op: want 1, got %d", auditNoOp)
	}
	t.Cleanup(func() {
		_, _ = env.Pool.Exec(context.Background(),
			`DELETE FROM audit_log WHERE action = 'loan.disbursement_reversed' AND target_id = $1`,
			sc.LoanID.String())
	})
}

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// httpReq builds a JSON POST request without dragging the test through
// the existing httpJSON helper's status-code wrapping (we want to
// assert on status codes directly here).
func httpReq(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, url, strings.NewReader(string(buf)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}
