// Phase-4 finalize-disbursement endpoint.
//
// Called by services/mpesa's B2C result handler after Daraja
// confirms a loan disbursement actually landed on the member's
// phone. Re-runs the deferred ExecuteDisbursementTx + posts the GL
// entry. Idempotent: a loan that's already 'active' returns 200
// with status=already_finalized so the mpesa-side reconciler can
// safely retry without double-posting.
//
// Auth: X-Internal-Token gate (same shared secret accounting +
// notification use). Lives under /internal/v1 so the ingress can
// firewall it from the public internet.

package handler

import (
	"errors"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/posting"
)

type finalizeDisbReq struct {
	MpesaReceipt string `json:"mpesa_receipt"`
}

// FinalizeDisbursement is the internal endpoint the mpesa service
// calls after a successful B2C Result callback.
func (h *LoanHandler) FinalizeDisbursement(w http.ResponseWriter, r *http.Request) {
	// Internal-token auth. Empty token in dev disables the check —
	// matches accounting/internal_post.go's pattern. Phase 5 will
	// thread an InternalToken field through LoanHandler; for phase
	// 4 we read from env directly so we don't churn the constructor
	// across savings's many entry points.
	if want := internalTokenFromEnv(); want != "" && r.Header.Get("X-Internal-Token") != want {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var req finalizeDisbReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// The finalize call doesn't carry a tenant subdomain (it's
	// service-to-service). We pull the tenant_id off the loan row
	// itself; this is safe because the loan_id was supplied by the
	// mpesa side which received it as part of the trusted
	// source_ref + the internal-token gate above proves the caller
	// is one of our own services.
	var tenantID = uuid.Nil
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

	payload := LoanDisbursementPayload{
		LoanID:      loanID,
		Channel:     "mpesa",
		ExternalRef: nilIfEmpty(req.MpesaReceipt),
	}
	var (
		result      *LoanDisbursementResult
		alreadyDone bool
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
		if err != nil {
			return err
		}
		// Idempotency. ExecuteDisbursementTx errors with
		// ErrAppNotDisbursable when the loan is past
		// pending_disbursement; we treat that as a successful
		// no-op so retries don't fail.
		if loan.Status != domain.LoanPendingDisbursement {
			alreadyDone = true
			return nil
		}
		// makerID: phase 4 doesn't carry a human actor through the
		// callback chain, so we use the system uuid.Nil here. The
		// disbursement still records the M-PESA receipt via
		// ExternalRef which provides the audit trail.
		res, err := h.ExecuteDisbursementTx(r.Context(), tx, payload, uuid.Nil)
		if err != nil {
			return err
		}
		if perr := h.postLoanDisbursementToGLTx(r.Context(), tx, tenantID, res, "mpesa"); perr != nil {
			return perr
		}
		result = res
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeLoanAppErr(w, r, err)
		return
	}
	if alreadyDone {
		httpx.OK(w, map[string]any{"status": "already_finalized", "loan_id": loanID})
		return
	}
	httpx.OK(w, map[string]any{
		"status":  "finalized",
		"loan_id": loanID,
		"loan":    result.Loan,
	})

}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func internalTokenFromEnv() string {
	return os.Getenv("SAVINGS_INTERNAL_TOKEN")
}
