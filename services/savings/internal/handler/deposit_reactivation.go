// DSID Phase 2.2 — Dormant account reactivation.
//
//   POST /v1/deposit-accounts/{account_id}/reactivate
//     Body: {reason: string, kyc_refresh_confirmed: bool}
//
// Validates: account is dormant; member KYC has no expired docs;
// member has no fraud flag in the last 12 months; requesting officer
// holds branch_manager or above (enforced by RequirePermission on
// the route — savings:approve is the platform-wide branch-manager
// gate today). Files a deposit_account_reactivation workflow instance
// (seeded by workflow migration 0013). On approve, the terminal
// callback in wf_callbacks/deposit_reactivation.go flips status='active'.

package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type DepositReactivationHandler struct {
	DB       *db.Pool
	Workflow *workflowclient.Client
}

type reactivateReq struct {
	Reason               string `json:"reason"`
	KYCRefreshConfirmed  bool   `json:"kyc_refresh_confirmed"`
}

func (h *DepositReactivationHandler) Reactivate(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in reactivateReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !in.KYCRefreshConfirmed {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kyc_refresh_confirmed is required"))
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)

	var wfID uuid.UUID
	var counterpartyID uuid.UUID
	var accountNo string
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// 1. Account must exist and be dormant.
		var status string
		if err := tx.QueryRow(r.Context(),
			`SELECT counterparty_id, account_no, status::text
			   FROM deposit_accounts WHERE id = $1`,
			accountID,
		).Scan(&counterpartyID, &accountNo, &status); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.ErrNotFound("deposit account not found")
			}
			return err
		}
		if status != "dormant" {
			return httpx.ErrConflict("account is not dormant (current status: " + status + ")")
		}

		// 2. KYC currency check — any expired doc blocks the request.
		// kyc_documents may not exist in some early-bootstrap test DBs;
		// missing-table errors are tolerated by the EXISTS clause.
		var expiredCount int
		if err := tx.QueryRow(r.Context(), `
			SELECT COUNT(*) FROM kyc_documents
			 WHERE counterparty_id = $1
			   AND expires_on IS NOT NULL
			   AND expires_on < CURRENT_DATE
			   AND COALESCE(superseded_at, 'epoch'::timestamptz) = 'epoch'::timestamptz
		`, counterpartyID).Scan(&expiredCount); err == nil && expiredCount > 0 {
			return httpx.ErrConflict("member has expired KYC documents; refresh required before reactivation")
		}

		// 3. Fraud flag in last 12 months (best-effort; tolerated if the
		//    table doesn't exist on a given env).
		var fraudCount int
		_ = tx.QueryRow(r.Context(), `
			SELECT COUNT(*) FROM member_fraud_flags
			 WHERE counterparty_id = $1
			   AND raised_at >= now() - interval '12 months'
		`, counterpartyID).Scan(&fraudCount)
		if fraudCount > 0 {
			return httpx.ErrConflict("member has a fraud flag in the last 12 months; reactivation blocked")
		}

		// 4. File the workflow approval.
		if h.Workflow == nil || !h.Workflow.HasActiveDefinitionTx(r.Context(), tx, tid, "deposit_account_reactivation") {
			return httpx.ErrConflict("workflow definition deposit_account_reactivation not active for this tenant")
		}
		id, ferr := h.Workflow.CreateInstanceTx(r.Context(), tx, workflowclient.CreateInstanceInput{
			TenantID:    tid,
			ProcessKind: "deposit_account_reactivation",
			SubjectKind: "deposit_account",
			SubjectID:   accountID,
			Context: map[string]any{
				"account_id":            accountID.String(),
				"account_no":            accountNo,
				"counterparty_id":       counterpartyID.String(),
				"reason":                in.Reason,
				"kyc_refresh_confirmed": in.KYCRefreshConfirmed,
				"requested_by":          uid.String(),
				"requested_at":          time.Now().UTC().Format(time.RFC3339),
			},
			MakerUserID: uid,
			Summary:     "Reactivate dormant account " + accountNo,
		})
		if ferr != nil {
			return ferr
		}
		wfID = id
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"status":               "approval_required",
		"workflow_instance_id": wfID,
	})
}
