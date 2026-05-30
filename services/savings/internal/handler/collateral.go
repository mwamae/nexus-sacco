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
	"fmt"
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
	// Phase 1.5b — internal lien/pledge placement for fixed_deposit_lien
	// and listed_shares kinds. Nil-safe; when nil the handler treats
	// those kinds as descriptive-text only.
	Liens  *store.CollateralLienStore
	Logger *slog.Logger
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

	// Phase 1.5b — third-party pledger (NULL ⇒ self-pledge).
	PledgerCounterpartyID *string `json:"pledger_counterparty_id,omitempty"`

	// Phase 1.5b — internal-kind pledge fields. Required when
	// kind = 'fixed_deposit_lien' (deposit_account_id + liened_amount)
	// or kind = 'listed_shares' (share_account_id + pledged_share_count).
	DepositAccountID  *string `json:"deposit_account_id,omitempty"`
	LienedAmount      *string `json:"liened_amount,omitempty"`
	ShareAccountID    *string `json:"share_account_id,omitempty"`
	PledgedShareCount *int    `json:"pledged_share_count,omitempty"`
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

	// Phase 1.5b — third-party pledger sanity check.
	var pledgerID *uuid.UUID
	if in.PledgerCounterpartyID != nil && *in.PledgerCounterpartyID != "" {
		pid, perr := uuid.Parse(*in.PledgerCounterpartyID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid pledger_counterparty_id"))
			return
		}
		pledgerID = &pid
	}

	// Phase 1.5b — parse internal-kind fields when present.
	isInternalKind := in.Kind == "fixed_deposit_lien" || in.Kind == "listed_shares"
	var (
		depositAccountID  uuid.UUID
		lienedAmount      decimal.Decimal
		shareAccountID    uuid.UUID
		pledgedShareCount int
	)
	if in.Kind == "fixed_deposit_lien" {
		if in.DepositAccountID == nil || in.LienedAmount == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("fixed_deposit_lien requires deposit_account_id + liened_amount"))
			return
		}
		dID, perr := uuid.Parse(*in.DepositAccountID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid deposit_account_id"))
			return
		}
		depositAccountID = dID
		la, derr := decimal.NewFromString(*in.LienedAmount)
		if derr != nil || la.LessThanOrEqual(decimal.Zero) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("liened_amount must be a positive decimal"))
			return
		}
		lienedAmount = la
	}
	if in.Kind == "listed_shares" {
		if in.ShareAccountID == nil || in.PledgedShareCount == nil || *in.PledgedShareCount <= 0 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("listed_shares requires share_account_id + positive pledged_share_count"))
			return
		}
		sID, perr := uuid.Parse(*in.ShareAccountID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid share_account_id"))
			return
		}
		shareAccountID = sID
		pledgedShareCount = *in.PledgedShareCount
	}

	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var created *domain.LoanCollateralItem
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Verify the application exists + the product allows the kind.
		var productID uuid.UUID
		var accepted []string
		var applicantCID uuid.UUID
		if err := tx.QueryRow(r.Context(), `
			SELECT a.product_id, p.accepted_collateral_kinds, a.counterparty_id
			  FROM loan_applications a
			  JOIN loan_products p ON p.id = a.product_id
			 WHERE a.id = $1
		`, appID).Scan(&productID, &accepted, &applicantCID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound("application not found")
			}
			return err
		}
		if !kindAccepted(accepted, in.Kind) {
			return httpx.ErrBadRequest("collateral kind not accepted by this product")
		}

		// Internal-kind validation — the pledger's account is the source
		// of truth. For self-pledge that's the applicant; for third-party
		// it's the pledger_counterparty_id.
		expectedOwner := applicantCID
		if pledgerID != nil {
			expectedOwner = *pledgerID
		}
		if isInternalKind && h.Liens == nil {
			return httpx.ErrConflict("internal lien feature not configured on this deployment")
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

		// Stamp third-party pledger metadata + initial consent_status=pending.
		if pledgerID != nil {
			if _, err := tx.Exec(r.Context(), `
				UPDATE loan_collateral SET
				  pledger_counterparty_id = $2,
				  pledger_consent_status  = 'pending'
				 WHERE id = $1
			`, c.ID, *pledgerID); err != nil {
				return err
			}
		}

		switch in.Kind {
		case "fixed_deposit_lien":
			// 1. Ownership check — account belongs to the expected owner.
			var ownerID uuid.UUID
			var balance decimal.Decimal
			if err := tx.QueryRow(r.Context(), `
				SELECT counterparty_id, current_balance
				  FROM deposit_accounts WHERE id = $1
			`, depositAccountID).Scan(&ownerID, &balance); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.ErrBadRequest("deposit_account_id not found")
				}
				return err
			}
			if ownerID != expectedOwner {
				return httpx.ErrConflict("deposit account does not belong to the pledger")
			}
			// 2. Available balance after existing liens.
			existingLiens, lerr := h.Liens.SumActiveDepositLiensTx(r.Context(), tx, depositAccountID)
			if lerr != nil {
				return lerr
			}
			available := balance.Sub(existingLiens)
			if lienedAmount.GreaterThan(available) {
				return httpx.ErrConflict(fmt.Sprintf(
					"available balance is KES %s after existing liens; cannot lien KES %s",
					available.StringFixed(2), lienedAmount.StringFixed(2),
				))
			}
			// 3. Place the lien + jump collateral to 'pledged' (system-verified).
			if _, lerr := h.Liens.PlaceDepositLienTx(r.Context(), tx, store.PlaceDepositLienInput{
				CollateralID:     c.ID,
				DepositAccountID: depositAccountID,
				LienedAmount:     lienedAmount,
				PlacedBy:         uid,
			}); lerr != nil {
				return lerr
			}
			if _, err := tx.Exec(r.Context(), `
				UPDATE loan_collateral SET
				  status            = 'pledged',
				  forced_sale_value = $2,
				  pledged_by        = $3,
				  pledged_at        = now(),
				  -- internal kinds are auto-verified by the system; stamp
				  -- the actor + timestamp so the timeline reads cleanly.
				  verified_by       = $3,
				  verified_at       = now()
				 WHERE id = $1
			`, c.ID, lienedAmount, uid); err != nil {
				return err
			}
			_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
				CollateralID: c.ID, Kind: "pledged", ActorUserID: &uid,
				Details: map[string]interface{}{
					"kind":               "fixed_deposit_lien",
					"deposit_account_id": depositAccountID.String(),
					"liened_amount":      lienedAmount.String(),
				},
			})

		case "listed_shares":
			// 1. Ownership + available share count.
			var ownerID uuid.UUID
			var heldShares, pledgedShares int
			if err := tx.QueryRow(r.Context(), `
				SELECT counterparty_id, shares_held, shares_pledged
				  FROM share_accounts WHERE id = $1
			`, shareAccountID).Scan(&ownerID, &heldShares, &pledgedShares); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.ErrBadRequest("share_account_id not found")
				}
				return err
			}
			if ownerID != expectedOwner {
				return httpx.ErrConflict("share account does not belong to the pledger")
			}
			available := heldShares - pledgedShares
			if pledgedShareCount > available {
				return httpx.ErrConflict(fmt.Sprintf(
					"available shares = %d after existing pledges; cannot pledge %d",
					available, pledgedShareCount,
				))
			}
			// 2. Place pledge (bumps share_accounts.shares_pledged in
			// the same tx) + jump to 'pledged'.
			if _, perr := h.Liens.PlaceSharePledgeTx(r.Context(), tx, store.PlaceSharePledgeInput{
				CollateralID:      c.ID,
				ShareAccountID:    shareAccountID,
				PledgedShareCount: pledgedShareCount,
				PlacedBy:          uid,
			}); perr != nil {
				return perr
			}
			// Forced-sale-value uses estimated_value as a system stand-in;
			// member can attach a panel valuation later if needed for
			// reporting purity, but coverage maths work off it as-is.
			if _, err := tx.Exec(r.Context(), `
				UPDATE loan_collateral SET
				  status            = 'pledged',
				  forced_sale_value = $2,
				  pledged_by        = $3,
				  pledged_at        = now(),
				  verified_by       = $3,
				  verified_at       = now()
				 WHERE id = $1
			`, c.ID, est, uid); err != nil {
				return err
			}
			_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
				CollateralID: c.ID, Kind: "pledged", ActorUserID: &uid,
				Details: map[string]interface{}{
					"kind":                "listed_shares",
					"share_account_id":    shareAccountID.String(),
					"pledged_share_count": pledgedShareCount,
				},
			})

		default:
			// External kinds — record the proposed event; lifecycle runs
			// through the verify-then-value chain.
			_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
				CollateralID: c.ID, Kind: "proposed", ActorUserID: &uid,
				Details: map[string]interface{}{
					"kind":            in.Kind,
					"estimated_value": est.String(),
				},
			})
		}

		// Re-fetch the row to return the (possibly mutated) status.
		fresh, err := h.Collaterals.GetTx(r.Context(), tx, c.ID)
		if err != nil {
			return err
		}
		created = fresh
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
		// Normalise nil slices to empty so JSON encodes as [] rather
		// than null — the React drawer reads .length immediately.
		if hist == nil {
			hist = []domain.CollateralValuation{}
		}
		if ev == nil {
			ev = []domain.CollateralEvent{}
		}
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

// ensurePledgerConsentedTx returns nil when either:
//   (a) the collateral is self-pledged (no pledger_counterparty_id), or
//   (b) the third-party pledger has accepted / offline-consented.
//
// Phase 1.5b — blocks Verify / Valuation / Pledge from advancing a
// row whose pledger hasn't said yes. Declined stays terminal until
// the officer creates a fresh collateral row.
func ensurePledgerConsentedTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) error {
	var pledger *uuid.UUID
	var status *string
	if err := tx.QueryRow(ctx, `
		SELECT pledger_counterparty_id, pledger_consent_status
		  FROM loan_collateral WHERE id = $1
	`, collateralID).Scan(&pledger, &status); err != nil {
		return err
	}
	if pledger == nil {
		return nil // self-pledged — always OK
	}
	if status == nil || *status == "pending" {
		return httpx.ErrConflict("third-party pledger consent is still pending; cannot advance status")
	}
	if *status == "declined" {
		return httpx.ErrConflict("third-party pledger declined; create a new collateral row")
	}
	// accepted | offline_consented — clear to advance.
	return nil
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
		if err := ensurePledgerConsentedTx(r.Context(), tx, id); err != nil {
			return err
		}
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
		if err := ensurePledgerConsentedTx(r.Context(), tx, id); err != nil {
			return err
		}
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
		if err := ensurePledgerConsentedTx(r.Context(), tx, id); err != nil {
			return err
		}
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
		// Phase 1.5b — also release any backing lien/pledge so the
		// borrower's deposit balance / share count frees up.
		if h.Liens != nil {
			if err := h.Liens.ReleaseLiensForCollateralTx(r.Context(), tx, id, uid, in.Reason); err != nil {
				return err
			}
		}
		updated = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Mark auctioned ───────────

type markAuctionedReq struct {
	Reason string `json:"reason"`
}

// MarkAuctioned flips status pledged → auctioned. Terminal state;
// granular auction-event tracking (handover, notice, sale, proceeds)
// then runs through POST /v1/collateral/{id}/auction-event. Also
// releases any backing internal lien/pledge so the borrower's locked
// balance frees up the same way the release path does.
func (h *CollateralHandler) MarkAuctioned(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in markAuctionedReq
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
		c, err := h.Collaterals.MarkAuctionedTx(r.Context(), tx, id, uid, in.Reason)
		if err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "auctioned", ActorUserID: &uid,
			Details: map[string]interface{}{"reason": in.Reason},
		})
		// Free any backing lien/pledge so the borrower's deposit / share
		// count unlocks (the underlying asset has gone to auction; the
		// lien on the internal account doesn't belong here anymore).
		if h.Liens != nil {
			if err := h.Liens.ReleaseLiensForCollateralTx(r.Context(), tx, id, uid, "collateral auctioned"); err != nil {
				return err
			}
		}
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

// ─────────── Pledges given by counterparty (Member 360 tab) ───────────

type pledgeGivenRow struct {
	CollateralID    uuid.UUID `json:"collateral_id"`
	ApplicationID   uuid.UUID `json:"application_id"`
	ApplicationNo   string    `json:"application_no"`
	LoanNo          *string   `json:"loan_no,omitempty"`
	BorrowerName    string    `json:"borrower_name"`
	Kind            string    `json:"kind"`
	Description     string    `json:"description"`
	EstimatedValue  string    `json:"estimated_value"`
	ForcedSaleValue *string   `json:"forced_sale_value,omitempty"`
	Status          string    `json:"status"`
	ConsentStatus   *string   `json:"pledger_consent_status,omitempty"`
	IsSelfPledge    bool      `json:"is_self_pledge"`
}

// PledgesGivenByCounterparty returns the collateral items this
// counterparty is on the hook for — either as borrower (self-pledge)
// or as third-party pledger (pledger_counterparty_id = counterparty).
// Drives the Member 360 → "Pledges given" tab.
func (h *CollateralHandler) PledgesGivenByCounterparty(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out []pledgeGivenRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT c.id, a.id, a.application_no,
			       l.loan_no,
			       COALESCE(cd_b.full_name, ''),
			       c.kind::text, c.description,
			       c.estimated_value::text, c.forced_sale_value::text,
			       c.status,
			       c.pledger_consent_status,
			       (c.pledger_counterparty_id IS NULL) AS is_self_pledge
			  FROM loan_collateral c
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN loans l ON l.application_id = a.id
			  LEFT JOIN counterparty_directory cd_b ON cd_b.counterparty_id = a.counterparty_id
			 WHERE (c.pledger_counterparty_id = $1
			        OR (c.pledger_counterparty_id IS NULL AND a.counterparty_id = $1))
			   AND c.status IN ('offered','verified','valued','pledged')
			 ORDER BY c.created_at DESC
		`, cpID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row pledgeGivenRow
			if err := rows.Scan(&row.CollateralID, &row.ApplicationID, &row.ApplicationNo,
				&row.LoanNo, &row.BorrowerName, &row.Kind, &row.Description,
				&row.EstimatedValue, &row.ForcedSaleValue, &row.Status,
				&row.ConsentStatus, &row.IsSelfPledge); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
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
