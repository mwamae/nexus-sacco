// Phase 1.5a — collateral lifecycle handler.
//
// Endpoints (see prompt §2 for the full table):
//
//   POST   /v1/loan-applications/{app_id}/collateral
//   GET    /v1/loan-applications/{app_id}/collateral
//   GET    /v1/loans/{loan_id}/collateral
//   GET    /v1/collateral/{id}
//   PATCH  /v1/collateral/{id}
//   POST   /v1/collateral/{id}/verify
//   POST   /v1/collateral/{id}/reject
//   POST   /v1/collateral/{id}/valuation
//   POST   /v1/collateral/{id}/pledge
//   POST   /v1/collateral/{id}/release
//   DELETE /v1/collateral/{id}
//   GET    /v1/loan-applications/{app_id}/security-coverage
//
// State-transition matrix:
//
//   offered    → verified | rejected (soft) | deleted
//   verified   → valued (on valuation) | rejected (soft, unwinds)
//   valued     → pledged | revalued (stays valued)
//   pledged    → released | auctioned
//   released, auctioned — terminal
//
// Every mutation also appends to loan_collateral_events for the
// drillable timeline. Forbidden transitions return 409 with the
// current status so callers can show "this item is already pledged".

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/coverage"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type CollateralHandler struct {
	DB          *db.Pool
	Collaterals *store.CollateralStore
	Logger      *slog.Logger
}

// hasPerm — local helper, mirrors middleware.RequirePermission but
// usable inside handler bodies (override-coverage check).
func hasPerm(r *http.Request, perm string) bool {
	c := middleware.ClaimsFrom(r)
	if c == nil {
		return false
	}
	if c.IsPlatformAdmin {
		return true
	}
	for _, p := range c.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// ─────────── Create (POST application/collateral) ───────────

type createCollateralReq struct {
	Kind           string  `json:"kind"`
	Description    string  `json:"description"`
	EstimatedValue string  `json:"estimated_value"`
	OwnershipPath  *string `json:"ownership_path,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}

func (h *CollateralHandler) CreateForApplication(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid app_id"))
		return
	}
	var in createCollateralReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Description == "" || in.Kind == "" || in.EstimatedValue == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind, description, estimated_value are required"))
		return
	}
	est, err := decimal.NewFromString(in.EstimatedValue)
	if err != nil || est.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("estimated_value must be a positive decimal"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var created *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Verify the application exists + the product allows the kind.
		var productID uuid.UUID
		var accepted []string
		if err := tx.QueryRow(r.Context(), `
			SELECT a.product_id, p.accepted_collateral_kinds
			  FROM loan_applications a
			  JOIN loan_products p ON p.id = a.product_id
			 WHERE a.id = $1
		`, appID).Scan(&productID, &accepted); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound("application not found")
			}
			return err
		}
		if !kindAccepted(accepted, in.Kind) {
			return httpx.ErrBadRequest("collateral kind not accepted by this product")
		}

		c, err := h.Collaterals.CreateTx(r.Context(), tx, store.CreateCollateralInput{
			ApplicationID:  appID,
			Kind:           domain.LoanCollateralKind(in.Kind),
			Description:    in.Description,
			EstimatedValue: est,
			OwnershipPath:  in.OwnershipPath,
			Notes:          in.Notes,
			ProposedBy:     uid,
		})
		if err != nil {
			return err
		}
		if err := h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: c.ID,
			Kind:         "proposed",
			ActorUserID:  &uid,
			Details: map[string]interface{}{
				"kind":            in.Kind,
				"estimated_value": est.String(),
			},
		}); err != nil {
			return err
		}
		created = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, created)
}

// kindAccepted — empty/nil accepted slice means "all kinds". A non-empty
// slice limits the set.
func kindAccepted(accepted []string, kind string) bool {
	if len(accepted) == 0 {
		return true
	}
	for _, k := range accepted {
		if k == kind {
			return true
		}
	}
	return false
}

// ─────────── List (GET application + loan) ───────────

func (h *CollateralHandler) ListByApplication(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid app_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Collaterals.ListByApplicationTx(r.Context(), tx, appID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]interface{}{"items": items, "total": len(items)})
}

func (h *CollateralHandler) ListByLoan(w http.ResponseWriter, r *http.Request) {
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Collaterals.ListByLoanTx(r.Context(), tx, loanID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]interface{}{"items": items, "total": len(items)})
}

// ─────────── Get full detail (GET /v1/collateral/{id}) ───────────

type collateralDetailResp struct {
	Item              *domain.LoanCollateralItem      `json:"item"`
	ValuationHistory  []domain.CollateralValuation    `json:"valuation_history"`
	Events            []domain.CollateralEvent        `json:"events"`
}

func (h *CollateralHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	out := &collateralDetailResp{}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.GetTx(r.Context(), tx, id)
		if err != nil {
			return mapCollateralErr(err)
		}
		// Hydrate current valuation from history's first row (cheapest path
		// since we're already listing history).
		hist, err := h.Collaterals.ValuationHistoryTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		for i := range hist {
			if hist[i].IsCurrent {
				c.CurrentValuation = &hist[i]
				break
			}
		}
		ev, err := h.Collaterals.EventsByCollateralTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out.Item = c
		out.ValuationHistory = hist
		out.Events = ev
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Patch (PATCH /v1/collateral/{id}) ───────────

type patchCollateralReq struct {
	Description    *string `json:"description,omitempty"`
	EstimatedValue *string `json:"estimated_value,omitempty"`
	OwnershipPath  *string `json:"ownership_path,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}

func (h *CollateralHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in patchCollateralReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var est *decimal.Decimal
	if in.EstimatedValue != nil {
		v, err := decimal.NewFromString(*in.EstimatedValue)
		if err != nil || v.LessThanOrEqual(decimal.Zero) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("estimated_value must be a positive decimal"))
			return
		}
		est = &v
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var updated *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.PatchOfferedTx(r.Context(), tx, id, in.Description, est, in.OwnershipPath, in.Notes)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "note_added", ActorUserID: &uid,
			Details: map[string]interface{}{"action": "patched"},
		})
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Verify ───────────

type verifyCollateralReq struct {
	Notes  string   `json:"notes"`
	Photos []string `json:"photos,omitempty"`
}

func (h *CollateralHandler) Verify(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in verifyCollateralReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Notes == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("notes are required (record what you inspected)"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var updated *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.VerifyTx(r.Context(), tx, id, uid, in.Notes, in.Photos)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "verified", ActorUserID: &uid,
			Details: map[string]interface{}{"notes": in.Notes, "photo_count": len(in.Photos)},
		})
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Reject (soft) ───────────

type rejectReq struct {
	Reason string `json:"reason"`
}

func (h *CollateralHandler) Reject(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in rejectReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var updated *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.RejectTx(r.Context(), tx, id, in.Reason)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "rejected", ActorUserID: &uid,
			Details: map[string]interface{}{"reason": in.Reason},
		})
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Valuation ───────────

type valuationReq struct {
	ValuerName          string  `json:"valuer_name"`
	ValuerContact       *string `json:"valuer_contact,omitempty"`
	ValuationDate       string  `json:"valuation_date"`
	MarketValue         string  `json:"market_value"`
	ForcedSaleValue     string  `json:"forced_sale_value"`
	ValuationReportPath *string `json:"valuation_report_path,omitempty"`
	ExpiresAt           *string `json:"expires_at,omitempty"`
	Notes               *string `json:"notes,omitempty"`
}

func (h *CollateralHandler) Valuation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in valuationReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ValuerName == "" || in.ValuationDate == "" || in.MarketValue == "" || in.ForcedSaleValue == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("valuer_name, valuation_date, market_value, forced_sale_value are required"))
		return
	}
	mv, err := decimal.NewFromString(in.MarketValue)
	if err != nil || !mv.GreaterThan(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("market_value must be a positive decimal"))
		return
	}
	fsv, err := decimal.NewFromString(in.ForcedSaleValue)
	if err != nil || !fsv.GreaterThan(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("forced_sale_value must be a positive decimal"))
		return
	}
	if fsv.GreaterThan(mv) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("forced_sale_value cannot exceed market_value"))
		return
	}
	valDate, err := time.Parse("2006-01-02", in.ValuationDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("valuation_date must be YYYY-MM-DD"))
		return
	}
	var expiresAt *time.Time
	if in.ExpiresAt != nil && *in.ExpiresAt != "" {
		t, err := time.Parse("2006-01-02", *in.ExpiresAt)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("expires_at must be YYYY-MM-DD"))
			return
		}
		expiresAt = &t
	}

	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var resp struct {
		Item      *domain.LoanCollateralItem   `json:"item"`
		Valuation *domain.CollateralValuation  `json:"valuation"`
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Forbid valuation against released/auctioned items.
		cur, err := h.Collaterals.GetTx(r.Context(), tx, id)
		if err != nil {
			return mapCollateralErr(err)
		}
		if cur.Status == "released" || cur.Status == "auctioned" {
			return httpx.ErrConflict("cannot value collateral in terminal status " + cur.Status)
		}

		v, c, err := h.Collaterals.CreateValuationTx(r.Context(), tx, store.CreateValuationInput{
			CollateralID:        id,
			ValuerName:          in.ValuerName,
			ValuerContact:       in.ValuerContact,
			ValuationDate:       valDate,
			MarketValue:         mv,
			ForcedSaleValue:     fsv,
			ValuationReportPath: in.ValuationReportPath,
			ExpiresAt:           expiresAt,
			Notes:               in.Notes,
			CreatedBy:           uid,
		})
		if err != nil {
			return err
		}
		kind := "valued"
		if cur.Status != "verified" {
			kind = "revalued"
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: kind, ActorUserID: &uid,
			Details: map[string]interface{}{
				"valuer_name":       in.ValuerName,
				"market_value":      mv.String(),
				"forced_sale_value": fsv.String(),
				"valuation_date":    in.ValuationDate,
			},
		})
		resp.Item = c
		resp.Valuation = v
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── Pledge ───────────

func (h *CollateralHandler) Pledge(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var updated *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.PledgeTx(r.Context(), tx, id, uid)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "pledged", ActorUserID: &uid,
		})
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Release ───────────

type releaseReq struct {
	Reason string `json:"reason"`
}

func (h *CollateralHandler) Release(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in releaseReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var updated *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Collaterals.ReleaseTx(r.Context(), tx, id, uid, in.Reason)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "released", ActorUserID: &uid,
			Details: map[string]interface{}{"reason": in.Reason},
		})
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Delete ───────────

func (h *CollateralHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return mapCollateralErr(h.Collaterals.DeleteTx(r.Context(), tx, id))
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Security coverage card ───────────

type securityCoverageResp struct {
	Coverage coverageDTO `json:"coverage"`
	Policy   coveragePolicyDTO   `json:"policy"`
	Result   resultDTO   `json:"result"`
}

type coverageDTO struct {
	GuarantorPledged string `json:"guarantor_pledged"`
	CollateralFSV    string `json:"collateral_fsv"`
	LoanAmount       string `json:"loan_amount"`
	LoanAmountBasis  string `json:"loan_amount_basis"` // "approved" | "requested"
}

type coveragePolicyDTO struct {
	SecurityModel         string `json:"security_model"`
	MinGuarantorCoverPct  string `json:"min_guarantor_cover_pct"`
	MinCollateralCoverPct string `json:"min_collateral_cover_pct"`
}

type resultDTO struct {
	GuarantorPct        string `json:"guarantor_pct"`
	CollateralPct       string `json:"collateral_pct"`
	GuarantorPasses     bool   `json:"guarantor_passes"`
	CollateralPasses    bool   `json:"collateral_passes"`
	PolicyMet           bool   `json:"policy_met"`
	Reason              string `json:"reason"`
	GuarantorShortfall  string `json:"guarantor_shortfall"`
	CollateralShortfall string `json:"collateral_shortfall"`
}

func (h *CollateralHandler) SecurityCoverage(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid app_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out securityCoverageResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		cov, pol, basis, err := LoadCoverageAndPolicyTx(r.Context(), tx, h.Collaterals, appID)
		if err != nil {
			return err
		}
		res := coverage.Evaluate(cov, pol)
		out.Coverage = coverageDTO{
			GuarantorPledged: cov.GuarantorPledged.String(),
			CollateralFSV:    cov.CollateralFSV.String(),
			LoanAmount:       cov.LoanAmount.String(),
			LoanAmountBasis:  basis,
		}
		out.Policy = coveragePolicyDTO{
			SecurityModel:         pol.SecurityModel,
			MinGuarantorCoverPct:  pol.MinGuarantorCoverPct.String(),
			MinCollateralCoverPct: pol.MinCollateralCoverPct.String(),
		}
		out.Result = resultDTO{
			GuarantorPct:        res.GuarantorPct.String(),
			CollateralPct:       res.CollateralPct.String(),
			GuarantorPasses:     res.GuarantorPasses,
			CollateralPasses:    res.CollateralPasses,
			PolicyMet:           res.PolicyMet,
			Reason:              res.Reason,
			GuarantorShortfall:  res.GuarantorShortfall.String(),
			CollateralShortfall: res.CollateralShortfall.String(),
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// LoadCoverageAndPolicyTx is exposed so the workflow gate +
// disbursement gate share the same load path. Returns the Coverage,
// the Policy, and the basis ("approved" | "requested") used for the
// loan amount.
func LoadCoverageAndPolicyTx(
	ctx context.Context, tx pgx.Tx, st *store.CollateralStore, appID uuid.UUID,
) (coverage.Coverage, coverage.Policy, string, error) {
	var (
		productID    uuid.UUID
		reqAmount    decimal.Decimal
		approvedAmt  *decimal.Decimal
	)
	err := tx.QueryRow(ctx, `
		SELECT product_id, requested_amount, approved_amount
		  FROM loan_applications
		 WHERE id = $1
	`, appID).Scan(&productID, &reqAmount, &approvedAmt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coverage.Coverage{}, coverage.Policy{}, "", httpx.ErrNotFound("application not found")
		}
		return coverage.Coverage{}, coverage.Policy{}, "", err
	}

	var pol coverage.Policy
	err = tx.QueryRow(ctx, `
		SELECT security_model, min_guarantor_cover_pct, min_collateral_cover_pct
		  FROM loan_products WHERE id = $1
	`, productID).Scan(&pol.SecurityModel, &pol.MinGuarantorCoverPct, &pol.MinCollateralCoverPct)
	if err != nil {
		return coverage.Coverage{}, coverage.Policy{}, "", err
	}

	loanAmt := reqAmount
	basis := "requested"
	if approvedAmt != nil && approvedAmt.GreaterThan(decimal.Zero) {
		loanAmt = *approvedAmt
		basis = "approved"
	}

	gp, err := st.SumAcceptedGuaranteesByApplicationTx(ctx, tx, appID)
	if err != nil {
		return coverage.Coverage{}, coverage.Policy{}, "", err
	}
	cf, err := st.SumPledgedFSVByApplicationTx(ctx, tx, appID)
	if err != nil {
		return coverage.Coverage{}, coverage.Policy{}, "", err
	}

	return coverage.Coverage{
		GuarantorPledged: gp,
		CollateralFSV:    cf,
		LoanAmount:       loanAmt,
	}, pol, basis, nil
}

// ─────────── Error mapping ───────────

func mapCollateralErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrCollateralNotFound):
		return httpx.ErrNotFound("collateral not found")
	case errors.Is(err, store.ErrCollateralWrongState):
		return httpx.ErrConflict("collateral is not in the required state for this action")
	default:
		return err
	}
}
