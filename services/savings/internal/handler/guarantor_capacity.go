// Guarantor capacity endpoint — answers "how much can this member
// commit as a new guarantee right now?"
//
//   GET /v1/loans/guarantor-capacity?counterparty_id={id}
//
// Mounted under the /v1/loans prefix (not /v1/counterparties) so the
// admin SPA's dev proxy routes it to savings — `/api/v1/counterparties`
// is reserved for the member service in web/admin/vite.config.ts.
//
// Capacity model (pragmatic v1):
//
//   bosa_balance         = sum of current_balance across BOSA-segment
//                          deposit accounts of the counterparty.
//   own_loan_principal   = principal_balance summed across the
//                          counterparty's active / in_arrears /
//                          restructured loans (the member's own
//                          outstanding loans erode capacity).
//   existing_guarantees  = sum of amount_guaranteed across
//                          loan_guarantees rows where the counterparty
//                          is the guarantor AND status is still
//                          committing capacity (pending_consent |
//                          accepted | called_upon).
//   available_capacity   = MAX(0, bosa_balance - own_loan_principal - existing_guarantees)
//
// The borrower-side multiplier (e.g. "guarantor's BOSA × N") is NOT
// applied here — it varies by loan product and the form layer can
// surface the product's multiplier separately. This endpoint returns
// the raw "how much wallet headroom does this guarantor still have"
// figure, which is the input to that calculation.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type GuarantorCapacityHandler struct {
	DB     *db.Pool
	Logger *slog.Logger
}

type guarantorCapacityResp struct {
	CounterpartyID       uuid.UUID       `json:"counterparty_id"`
	BOSABalance          decimal.Decimal `json:"bosa_balance"`
	OwnLoanPrincipal     decimal.Decimal `json:"own_loan_principal"`
	ExistingGuarantees   decimal.Decimal `json:"existing_guarantees"`
	AvailableCapacity    decimal.Decimal `json:"available_capacity"`
	ActiveGuaranteeCount int             `json:"active_guarantee_count"`
	ActiveLoanCount      int             `json:"active_loan_count"`
}

func (h *GuarantorCapacityHandler) Get(w http.ResponseWriter, r *http.Request) {
	cpIDStr := r.URL.Query().Get("counterparty_id")
	if cpIDStr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id query parameter required"))
		return
	}
	cpID, err := uuid.Parse(cpIDStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id must be a UUID"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	resp := guarantorCapacityResp{CounterpartyID: cpID}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// BOSA balance across all the counterparty's BOSA accounts.
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.counterparty_id = $1
			   AND dp.segment::text = 'bosa'
			   AND da.status = 'active'
		`, cpID).Scan(&resp.BOSABalance); err != nil {
			return err
		}

		// Own active loan principal + loan count.
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(SUM(principal_balance), 0), count(*)
			  FROM loans
			 WHERE counterparty_id = $1
			   AND status IN ('active','in_arrears','restructured')
		`, cpID).Scan(&resp.OwnLoanPrincipal, &resp.ActiveLoanCount); err != nil {
			return err
		}

		// Existing guarantees still committing capacity.
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(SUM(amount_guaranteed), 0), count(*)
			  FROM loan_guarantees
			 WHERE guarantor_counterparty_id = $1
			   AND status IN ('pending_consent','accepted','called_upon')
		`, cpID).Scan(&resp.ExistingGuarantees, &resp.ActiveGuaranteeCount); err != nil {
			return err
		}

		available := resp.BOSABalance.Sub(resp.OwnLoanPrincipal).Sub(resp.ExistingGuarantees)
		if available.IsNegative() {
			available = decimal.Zero
		}
		resp.AvailableCapacity = available
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}
