// BOSA exit — member refund handler.
//
// BOSA accounts (segment = 'bosa') are non-withdrawable by design.
// The /v1/deposit-accounts/{id}/withdraw handler short-circuits with
// BOSA_WITHDRAW_FORBIDDEN. Officers route refunds through this
// endpoint instead, which queues a Board-level approval of kind
// `member_bosa_exit` — the executor side (which actually drains the
// account + posts the ledger debit) lands in a later PR alongside
// the rest of the exit workflow.
//
// In PR 1, approving a queued member_bosa_exit will fail at execute
// time because no executor is registered. That is intentional —
// it keeps the surface area committed while the Board / KYC
// off-boarding flow is still being designed.

package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type BOSAExitHandler struct {
	DB        *db.Pool
	Deposit   *DepositHandler
	Approvals *store.ApprovalsStore

	Workflow       *workflowclient.Client
	SavingsSelfURL string
}

type bosaExitReq struct {
	Reason string `json:"reason"`
}

// RequestExit reads the BOSA account and queues an approval of the
// full current balance. We deliberately don't accept a custom amount
// in PR 1 — partial BOSA refunds aren't a concept yet, and forcing
// the full-balance shape now means the approver UI doesn't have to
// branch later.
func (h *BOSAExitHandler) RequestExit(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in bosaExitReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required for a BOSA exit"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, acct, _, lerr := h.Deposit.loadProductAccount(r.Context(), tx, accountID)
		if lerr != nil {
			return lerr
		}
		// The endpoint is intentionally permissive about the source
		// account: a FOSA account here means the officer hit the
		// wrong route. Reject so the mistake surfaces immediately
		// rather than leaking into the approvals queue under the
		// wrong kind.
		if product.Segment != domain.SegmentBOSA {
			return httpx.ErrBadRequest("BOSA exit is only valid for BOSA accounts; use /withdraw for FOSA")
		}
		memberID := acct.CounterpartyID
		amount := acct.CurrentBalance
		reason := in.Reason
		// queueApproval doesn't currently carry MakerNote — the wf
		// path stores it in instance.context instead. Embed in the
		// payload map so the BOSA-exit executor can read it later.
		pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
			Workflow:       h.Workflow,
			Approvals:      h.Approvals,
			SavingsSelfURL: h.SavingsSelfURL,
		}, QueueApprovalInput{
			TenantID:         tid,
			Kind:             domain.ApprovalKindBOSAExit,
			Title:            fmt.Sprintf("BOSA exit refund · a/c %s · %s", acct.AccountNo, amount.StringFixed(2)),
			SubjectID:        memberID,
			SubjectMemberID:  &memberID,
			SubjectAccountID: &accountID,
			Amount:           &amount,
			Payload:          map[string]any{"account_id": accountID, "reason": reason, "maker_note": reason},
			MakerUserID:      userID,
		})
		if qerr != nil {
			return qerr
		}
		pending = pa
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("deposit account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	writePendingResponse(w, r, pending)
}
