// Reverse-disbursement endpoint. Called by services/mpesa's B2C
// reversal handler when Safaricom bounces a disbursement that we'd
// already marked sent. Undoes the corresponding loan state + posts
// a reversing GL entry so the books balance.
//
// Decision tree (mirrors docs/mpesa/runbook.md):
//   - status='pending_disbursement' → 200 no_op + audit row. The
//     forward FinalizeDisbursement never ran; nothing to undo.
//   - status='active' + zero activity → unwind: flip status back,
//     clear cached balances, drop the schedule, post the reversing
//     JE + audit row.
//   - status='active' WITH activity → 409. The member has touched
//     this loan; we cannot silently unwind. The mpesa wf task is
//     the durable handle; operator turns the reversal into a
//     write-off / journal correction per runbook.
//   - any other status (settled / written_off) → 409. Reversing a
//     closed loan is always a manual recovery case.
//
// Auth: X-Internal-Token (same shared secret pattern as
// FinalizeDisbursement). Tenant resolved off the loan row — the
// caller is a trusted service that already received the loan_id
// from us via source_ref.
//
// Idempotency: the reversing GL post uses
// source_module="mpesa" + source_ref="reverse:<original_disbursement_txn_id>".
// The accounting service's (source_module, source_ref) unique key
// makes the post a no-op on the second delivery, so re-posting a
// reversal payload is safe end-to-end.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type reverseDisbReq struct {
	// MpesaReversalReceipt is the Daraja TransactionID from the
	// reversal callback. Stamped into the reversing GL narration +
	// audit trail so operators can correlate to the
	// mpesa_outbound_requests row.
	MpesaReversalReceipt string `json:"mpesa_reversal_receipt"`
	// Reason carries the Safaricom ResultDesc (or staff-entered text
	// when staff initiates the reversal). Surfaced verbatim in the
	// reversing JE narration + audit metadata.
	Reason string `json:"reason"`
}

type reverseDisbResp struct {
	Status string `json:"status"` // "reversed" | "no_op"
	LoanID string `json:"loan_id"`
}

type reverseDisbConflict struct {
	Error         string `json:"error"`
	CurrentStatus string `json:"current_status"`
}

// ReverseDisbursement is the internal endpoint the mpesa service
// calls when a B2C reversal fires.
func (h *LoanHandler) ReverseDisbursement(w http.ResponseWriter, r *http.Request) {
	if want := internalTokenFromEnv(); want != "" && r.Header.Get("X-Internal-Token") != want {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var req reverseDisbReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var tenantID uuid.UUID
	if err := h.DB.Pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM loans WHERE id = $1`, loanID,
	).Scan(&tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("loan not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	var (
		outcome               string
		conflictStatus        domain.LoanStatus
		conflictMsg           string
		origDisbursementTxnID uuid.UUID
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
		if err != nil {
			return err
		}
		switch loan.Status {
		case domain.LoanPendingDisbursement:
			// Forward path never executed; nothing to unwind.
			outcome = "no_op"
			return h.writeReverseDisbAudit(r.Context(), tx, tenantID, loanID,
				"no_op", req.MpesaReversalReceipt, req.Reason, uuid.Nil)

		case domain.LoanActive:
			// Status says active but activity-based defence-in-depth:
			// a loan with any repayment activity must NOT auto-unwind
			// because the unwind would orphan repayment txns. The
			// runbook covers this as a manual recovery case.
			if hasRepaymentActivity(loan) {
				outcome = "conflict"
				conflictStatus = loan.Status
				conflictMsg = "loan has repayment activity; reversal must be handled manually"
				return nil
			}
			// Channel sanity: only mpesa disbursements know how to
			// reverse against the M-PESA cash leg (1015). Other
			// channels need staff judgement.
			ch := ""
			if loan.DisbursementChannel != nil {
				ch = strings.ToLower(*loan.DisbursementChannel)
			}
			if ch != "mpesa" {
				outcome = "conflict"
				conflictStatus = loan.Status
				conflictMsg = fmt.Sprintf("disbursement channel %q is not reversible via this endpoint", ch)
				return nil
			}

			// Look up the original disbursement loan_txn — its id is
			// the "original_source_ref" the reversing GL post keys
			// against for idempotency. The forward
			// postLoanDisbursementToGLTx used this same id.
			if err := tx.QueryRow(r.Context(), `
				SELECT id FROM loan_transactions
				 WHERE loan_id = $1 AND txn_type = 'disbursement'
				 ORDER BY posted_at ASC LIMIT 1
			`, loanID).Scan(&origDisbursementTxnID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// Shouldn't happen for an active loan, but bail
					// cleanly instead of writing a dangling reverse.
					outcome = "conflict"
					conflictStatus = loan.Status
					conflictMsg = "active loan has no disbursement transaction; reversal must be handled manually"
					return nil
				}
				return err
			}

			origPrincipal := loan.Principal
			origNet := loan.Principal
			if loan.NetDisbursed != nil {
				origNet = *loan.NetDisbursed
			}
			origFees := loan.TotalFeesDeducted

			if _, err := h.Loans.UnwindDisbursementTx(r.Context(), tx, loanID); err != nil {
				return err
			}
			// Flip the application back so the next disburse picks it
			// up. Don't fail the reversal on an app-status flip — the
			// loan row IS the source of truth.
			if _, err := h.Applications.UpdateStatusTx(r.Context(), tx, loan.ApplicationID, store.AppTransition{
				To: domain.AppApproved, By: uuid.Nil,
			}); err != nil {
				h.Logger.Warn("reverse-disbursement: app status flip skipped",
					"err", err, "loan_id", loanID, "application_id", loan.ApplicationID)
			}

			if err := h.postReverseDisbursementToGLTx(r.Context(), tx, tenantID, loan,
				origPrincipal, origNet, origFees, origDisbursementTxnID,
				req.MpesaReversalReceipt, req.Reason,
			); err != nil {
				return err
			}

			outcome = "reversed"
			return h.writeReverseDisbAudit(r.Context(), tx, tenantID, loanID,
				"reversed", req.MpesaReversalReceipt, req.Reason, origDisbursementTxnID)

		default:
			outcome = "conflict"
			conflictStatus = loan.Status
			conflictMsg = "loan is past disbursement; reversal must be handled manually"
			return nil
		}
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeLoanAppErr(w, r, err)
		return
	}

	if outcome == "conflict" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(reverseDisbConflict{
			Error:         conflictMsg,
			CurrentStatus: string(conflictStatus),
		})
		return
	}
	httpx.OK(w, reverseDisbResp{LoanID: loanID.String(), Status: outcome})
}

// hasRepaymentActivity returns true when the loan has any sign that
// money has moved on the books — a repayment, accrued interest,
// penalty, or paid fees. A loan with activity cannot be auto-unwound
// because the unwinding would orphan repayment txns.
func hasRepaymentActivity(loan *domain.Loan) bool {
	if loan.PrincipalRepaid.GreaterThan(decimal.Zero) {
		return true
	}
	if loan.InterestPaid.GreaterThan(decimal.Zero) || loan.InterestCharged.GreaterThan(decimal.Zero) {
		return true
	}
	if loan.PenaltyAccrued.GreaterThan(decimal.Zero) || loan.PenaltyPaid.GreaterThan(decimal.Zero) {
		return true
	}
	if loan.FeesPaid.GreaterThan(decimal.Zero) {
		return true
	}
	if loan.InstallmentsPaid > 0 {
		return true
	}
	if loan.LastRepaymentAt != nil {
		return true
	}
	return false
}

// writeReverseDisbAudit appends a row to the shared audit_log table
// so operators can grep the full reversal trail without joining
// loan_transactions. Action is always loan.disbursement_reversed;
// the outcome ("reversed" | "no_op") + the mpesa receipt + reason +
// original disbursement txn id live in metadata.
func (h *LoanHandler) writeReverseDisbAudit(
	ctx context.Context, tx pgx.Tx, tenantID, loanID uuid.UUID,
	outcome, mpesaReceipt, reason string, origTxnID uuid.UUID,
) error {
	meta := map[string]any{
		"outcome":                outcome,
		"mpesa_reversal_receipt": mpesaReceipt,
		"reason":                 reason,
	}
	if origTxnID != uuid.Nil {
		meta["original_disbursement_txn_id"] = origTxnID.String()
	}
	mb, _ := json.Marshal(meta)
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, action, target_kind, target_id, metadata)
		VALUES ($1, 'loan.disbursement_reversed', 'loan', $2, $3::jsonb)
	`, tenantID, loanID.String(), mb)
	return err
}

// postReverseDisbursementToGLTx writes the reversing journal entry to
// the posting outbox. Lines exactly mirror postLoanDisbursementToGLTx
// with debit/credit swapped. We re-derive the fee credit lines from
// the product because the FeeGLLines slice from the original
// disbursement isn't persisted.
//
// Dedup key: source_module="mpesa" + source_ref="reverse:<original_disbursement_txn_id>".
// The accounting outbox's UNIQUE on (source_module, source_ref) drops
// a duplicate reversal post silently — re-delivery from Safaricom is
// idempotent end to end.
func (h *LoanHandler) postReverseDisbursementToGLTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	loan *domain.Loan,
	origPrincipal, origNet, origFees decimal.Decimal,
	origDisbursementTxnID uuid.UUID,
	mpesaReceipt, reason string,
) error {
	if h.Posting == nil {
		return nil
	}
	lines := []posting.Line{
		{AccountCode: "1015", Debit: origNet, Narration: "Reverse M-PESA cash leg"},
		{AccountCode: "1100", Credit: origPrincipal, Narration: "Reverse loan receivable"},
	}
	if origFees.GreaterThan(decimal.Zero) && h.LoanProducts != nil {
		product, err := h.LoanProducts.GetTx(ctx, tx, loan.ProductID)
		if err == nil && product != nil {
			feeByGLCode := map[string]decimal.Decimal{}
			for _, f := range product.Fees {
				if f.Timing != domain.FeeUpfront {
					continue
				}
				amt := domain.ApplyFee(loan.Principal, f.Amount, f.IsPct)
				if amt.IsZero() {
					continue
				}
				code := f.GLCreditCode
				if code == "" {
					code = "4010"
				}
				feeByGLCode[code] = feeByGLCode[code].Add(amt)
			}
			for code, amt := range feeByGLCode {
				lines = append(lines, posting.Line{
					AccountCode: code, Debit: amt,
					Narration: "Reverse loan fee income (" + code + ")",
				})
			}
		}
	}
	narration := fmt.Sprintf("Reverse loan %s disbursement (%s)", loan.LoanNo, reason)
	if mpesaReceipt != "" {
		narration += " · receipt " + mpesaReceipt
	}
	return h.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "mpesa",
		SourceRef:    "reverse:" + origDisbursementTxnID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}
