// Qualifying-amount endpoint — pre-application snapshot of how much
// a borrower qualifies for under a given product.
//
//   GET /v1/loans/qualifying-amount?counterparty_id={id}&product_id={id}
//
// The UI calls this when the operator picks both a borrower and a
// product so the form can show:
//
//   "You qualify for up to KES 150,000 (BOSA 50,000 × 3.0 multiplier)."
//
// and refuse to submit a higher amount. Without this, the scorer flags
// over-ceiling requests as a hard_block (`multiplier_exceeded`) and
// auto-declines the application — surprising and avoidable.
//
// Math is identical to the scorer's; this endpoint just exposes the
// same computation up-front via domain.ComputeQualifyingAmount.
//
// Mounted under /v1/loans/* (not /v1/counterparties/*) so the admin
// SPA dev proxy routes it to savings.

package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type QualifyingAmountHandler struct {
	DB           *db.Pool
	Applications *store.LoanApplicationStore
	LoanProducts *store.LoanProductStore
	Tenants      *store.TenantStore
	Logger       *slog.Logger
}

type qualifyingAmountResp struct {
	domain.QualifyingAmount
	CounterpartyID uuid.UUID `json:"counterparty_id"`
	ProductID      uuid.UUID `json:"product_id"`
	// Extra hints for the UI banner.
	Notes []string `json:"notes,omitempty"`
}

func (h *QualifyingAmountHandler) Get(w http.ResponseWriter, r *http.Request) {
	cpIDStr := r.URL.Query().Get("counterparty_id")
	pIDStr := r.URL.Query().Get("product_id")
	if cpIDStr == "" || pIDStr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id and product_id query parameters required"))
		return
	}
	cpID, err := uuid.Parse(cpIDStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id must be a UUID")); return
	}
	pID, err := uuid.Parse(pIDStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("product_id must be a UUID")); return
	}
	tid, _ := middleware.TenantIDFrom(r)

	resp := qualifyingAmountResp{CounterpartyID: cpID, ProductID: pID}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, err := h.LoanProducts.GetTx(r.Context(), tx, pID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrNotFound("loan product not found")
			}
			return err
		}
		if !product.IsActive {
			resp.Notes = append(resp.Notes, "This product is inactive — applications cannot be submitted against it.")
		}
		in, err := h.Applications.GatherScoringInputsTx(r.Context(), tx, cpID, pID)
		if err != nil {
			return err
		}
		bosaFosa, err := h.Tenants.BOSAFOSAEnabledTx(r.Context(), tx)
		if err != nil {
			return err
		}
		resp.QualifyingAmount = domain.ComputeQualifyingAmount(*in, product, bosaFosa)

		// Helpful surface hints — explains what the UI is showing.
		switch {
		case resp.BasisKind == "none":
			resp.Notes = append(resp.Notes, "This product has no multiplier basis; the product's max amount is the only cap.")
		case resp.BasisValue.LessThanOrEqual(decimal.Zero):
			resp.Notes = append(resp.Notes,
				"Qualifying basis is zero — the borrower has no shares / deposits backing this product's multiplier.")
		case resp.Ceiling.LessThan(product.MinAmount):
			resp.Notes = append(resp.Notes,
				"Qualifying ceiling is below the product's minimum amount; the borrower cannot take this product today.")
		case resp.CappedByProduct:
			resp.Notes = append(resp.Notes,
				"Ceiling capped at the product's maximum (your basis × multiplier exceeded the product cap).")
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, resp)
}
