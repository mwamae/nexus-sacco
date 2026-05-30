// Phase 1.5b — charge / insurance / custody / auction handlers.
//
// All endpoints mount under /v1/collateral/{id}/* and reuse the
// CollateralAdvancedStore for SQL primitives. The lifecycle gate
// (Phase 1.5a) is extended here to additionally block approval when
// kind ∈ tenant_operations.collateral_charge_required_kinds and the
// charge isn't registered, or kind ∈ insurance_required_kinds and
// there's no current active policy. The gate sits in
// CheckCollateralChargeInsuranceGateTx (called by the approval
// workflow callback in S9 wiring).

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

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/filestore"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type CollateralAdvancedHandler struct {
	DB          *db.Pool
	Advanced    *store.CollateralAdvancedStore
	Collaterals *store.CollateralStore
	Files       *filestore.Store
	Logger      *slog.Logger
}

// ─────────── Charge registration ───────────

type recordChargeReq struct {
	Registry        string  `json:"registry"`
	Reference       string  `json:"reference"`
	RegisteredAt    string  `json:"registered_at"`
	CertificatePath *string `json:"certificate_path,omitempty"`
}

func (h *CollateralAdvancedHandler) RecordCharge(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in recordChargeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Registry == "" || in.Reference == "" || in.RegisteredAt == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("registry, reference, registered_at required"))
		return
	}
	switch in.Registry {
	case "lands_registry", "ntsa", "stockbroker_custodian", "kra", "other":
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid registry"))
		return
	}
	regAt, err := time.Parse(time.RFC3339, in.RegisteredAt)
	if err != nil {
		if d, derr := time.Parse("2006-01-02", in.RegisteredAt); derr == nil {
			regAt = d
		} else {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("registered_at must be YYYY-MM-DD or RFC3339"))
			return
		}
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := h.Advanced.RecordChargeTx(r.Context(), tx, store.RecordChargeInput{
			CollateralID:    id,
			Registry:        in.Registry,
			Reference:       in.Reference,
			RegisteredAt:    regAt,
			RegisteredBy:    uid,
			CertificatePath: in.CertificatePath,
		}); err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "documents_attached", ActorUserID: &uid,
			Details: map[string]interface{}{
				"action":    "charge_registered",
				"registry":  in.Registry,
				"reference": in.Reference,
			},
		})
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type dischargeChargeReq struct {
	DischargeRef string `json:"discharge_ref"`
}

func (h *CollateralAdvancedHandler) DischargeCharge(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in dischargeChargeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.DischargeRef == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("discharge_ref required"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := h.Advanced.DischargeChargeTx(r.Context(), tx, store.DischargeChargeInput{
			CollateralID: id, DischargeRef: in.DischargeRef, DischargedAt: time.Now().UTC(),
		}); err != nil {
			return mapCollateralErr(err)
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "documents_attached", ActorUserID: &uid,
			Details: map[string]interface{}{
				"action":        "charge_discharged",
				"discharge_ref": in.DischargeRef,
			},
		})
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Insurance ───────────

type recordInsuranceReq struct {
	ProviderName  string  `json:"provider_name"`
	PolicyNo      string  `json:"policy_no"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   string  `json:"effective_to"`
	PremiumAmount *string `json:"premium_amount,omitempty"`
	SumInsured    string  `json:"sum_insured"`
	PolicyDocPath *string `json:"policy_doc_path,omitempty"`
	Notes         *string `json:"notes,omitempty"`
}

func (h *CollateralAdvancedHandler) RecordInsurance(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in recordInsuranceReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ProviderName == "" || in.PolicyNo == "" ||
		in.EffectiveFrom == "" || in.EffectiveTo == "" || in.SumInsured == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("provider_name, policy_no, effective_from, effective_to, sum_insured required"))
		return
	}
	from, err := time.Parse("2006-01-02", in.EffectiveFrom)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("effective_from must be YYYY-MM-DD"))
		return
	}
	to, err := time.Parse("2006-01-02", in.EffectiveTo)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("effective_to must be YYYY-MM-DD"))
		return
	}
	if !to.After(from) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("effective_to must be after effective_from"))
		return
	}
	sumInsured, err := decimal.NewFromString(in.SumInsured)
	if err != nil || !sumInsured.GreaterThan(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("sum_insured must be a positive decimal"))
		return
	}
	var premium *decimal.Decimal
	if in.PremiumAmount != nil && *in.PremiumAmount != "" {
		p, perr := decimal.NewFromString(*in.PremiumAmount)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("premium_amount must be a decimal"))
			return
		}
		premium = &p
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var policy *domain.CollateralInsurancePolicy
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		p, err := h.Advanced.RecordInsuranceTx(r.Context(), tx, store.RecordInsuranceInput{
			CollateralID:  id,
			ProviderName:  in.ProviderName,
			PolicyNo:      in.PolicyNo,
			EffectiveFrom: from,
			EffectiveTo:   to,
			PremiumAmount: premium,
			SumInsured:    sumInsured,
			PolicyDocPath: in.PolicyDocPath,
			Notes:         in.Notes,
			CreatedBy:     uid,
		})
		if err != nil {
			return err
		}
		_ = h.Collaterals.AppendEventTx(r.Context(), tx, store.AppendEventInput{
			CollateralID: id, Kind: "documents_attached", ActorUserID: &uid,
			Details: map[string]interface{}{
				"action":    "insurance_recorded",
				"provider":  in.ProviderName,
				"policy_no": in.PolicyNo,
				"expires":   in.EffectiveTo,
			},
		})
		policy = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, policy)
}

// GetInsuranceHistory — drives the slide-over Insurance sub-tab.
func (h *CollateralAdvancedHandler) GetInsuranceHistory(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.CollateralInsurancePolicy
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Advanced.InsuranceHistoryTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Custody ───────────

type recordCustodyReq struct {
	DocumentKind          string  `json:"document_kind"`
	Movement              string  `json:"movement"`
	CustodianUserID       *string `json:"custodian_user_id,omitempty"`
	BorrowerSignaturePath *string `json:"borrower_signature_path,omitempty"`
	LocationCode          *string `json:"location_code,omitempty"`
	Notes                 *string `json:"notes,omitempty"`
}

func (h *CollateralAdvancedHandler) RecordCustody(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in recordCustodyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.DocumentKind == "" || in.Movement == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("document_kind + movement required"))
		return
	}
	switch in.Movement {
	case "checked_in", "checked_out", "returned_to_borrower":
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("movement must be checked_in | checked_out | returned_to_borrower"))
		return
	}
	var custodianID *uuid.UUID
	if in.CustodianUserID != nil && *in.CustodianUserID != "" {
		c, perr := uuid.Parse(*in.CustodianUserID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid custodian_user_id"))
			return
		}
		custodianID = &c
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var movement *domain.CollateralCustodyMovement
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		m, err := h.Advanced.RecordCustodyTx(r.Context(), tx, store.RecordCustodyInput{
			CollateralID:          id,
			DocumentKind:          in.DocumentKind,
			Movement:              in.Movement,
			MovementBy:            uid,
			CustodianUserID:       custodianID,
			BorrowerSignaturePath: in.BorrowerSignaturePath,
			LocationCode:          in.LocationCode,
			Notes:                 in.Notes,
		})
		if err != nil {
			return err
		}
		movement = m
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, movement)
}

func (h *CollateralAdvancedHandler) GetCustodyTimeline(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.CollateralCustodyMovement
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Advanced.CustodyTimelineTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Auction events ───────────

type recordAuctionEventReq struct {
	EventKind      string  `json:"event_kind"`
	OccurredAt     string  `json:"occurred_at,omitempty"` // optional; defaults to now()
	Amount         *string `json:"amount,omitempty"`
	BuyerDetails   *string `json:"buyer_details,omitempty"`
	AuctioneerName *string `json:"auctioneer_name,omitempty"`
	Notes          *string `json:"notes,omitempty"`
	DocPath        *string `json:"doc_path,omitempty"`
}

func (h *CollateralAdvancedHandler) RecordAuctionEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in recordAuctionEventReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	switch in.EventKind {
	case "handover_to_auctioneer", "auction_notice_published", "auction_held",
		"sold", "reserve_not_met", "rescheduled", "proceeds_received":
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid event_kind"))
		return
	}
	occurredAt := time.Now().UTC()
	if in.OccurredAt != "" {
		t, terr := time.Parse(time.RFC3339, in.OccurredAt)
		if terr != nil {
			if d, derr := time.Parse("2006-01-02", in.OccurredAt); derr == nil {
				occurredAt = d
			} else {
				httpx.WriteErr(w, r, httpx.ErrBadRequest("occurred_at must be YYYY-MM-DD or RFC3339"))
				return
			}
		} else {
			occurredAt = t
		}
	}
	var amount *decimal.Decimal
	if in.Amount != nil && *in.Amount != "" {
		a, perr := decimal.NewFromString(*in.Amount)
		if perr != nil || !a.GreaterThan(decimal.Zero) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be a positive decimal"))
			return
		}
		amount = &a
	}
	if (in.EventKind == "sold" || in.EventKind == "proceeds_received") && amount == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount is required for sold + proceeds_received events"))
		return
	}

	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	// Pull the loan_id off the collateral row (if any) so the event
	// joins to the loan for reporting.
	var loanID *uuid.UUID
	var event *domain.CollateralAuctionEvent
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			SELECT loan_id FROM loan_collateral WHERE id = $1
		`, id).Scan(&loanID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound("collateral not found")
			}
			return err
		}
		e, err := h.Advanced.RecordAuctionEventTx(r.Context(), tx, store.RecordAuctionEventInput{
			CollateralID:   id,
			LoanID:         loanID,
			EventKind:      in.EventKind,
			OccurredAt:     occurredAt,
			Amount:         amount,
			BuyerDetails:   in.BuyerDetails,
			AuctioneerName: in.AuctioneerName,
			Notes:          in.Notes,
			DocPath:        in.DocPath,
			CreatedBy:      uid,
		})
		if err != nil {
			return err
		}
		event = e
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, event)
}

func (h *CollateralAdvancedHandler) GetAuctionEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.CollateralAuctionEvent
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Advanced.AuctionEventsByCollateralTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Approval-gate extension ───────────
//
// CheckCollateralChargeInsuranceGateTx is called by the approval
// workflow callback alongside coverage.Evaluate. Returns a 409-style
// error when any pledged collateral on the application is missing a
// required charge or insurance, per tenant policy.

func CheckCollateralChargeInsuranceGateTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) error {
	// Load tenant policy arrays.
	var chargeKinds, insuranceKinds []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(collateral_charge_required_kinds, ARRAY[]::text[]),
		       COALESCE(collateral_insurance_required_kinds, ARRAY[]::text[])
		  FROM tenant_operations LIMIT 1
	`).Scan(&chargeKinds, &insuranceKinds); err != nil {
		return err
	}
	if len(chargeKinds) == 0 && len(insuranceKinds) == 0 {
		return nil
	}

	// Pledged collateral rows for this app — only those in 'pledged' state
	// count (offered/verified/valued don't form security yet).
	rows, err := tx.Query(ctx, `
		SELECT id, kind::text, description,
		       charge_registered_at,
		       (SELECT 1 FROM collateral_insurance_policies p
		         WHERE p.collateral_id = c.id AND p.is_current = true AND p.status = 'active'
		         LIMIT 1)
		  FROM loan_collateral c
		 WHERE application_id = $1 AND status = 'pledged'
	`, appID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var kind, desc string
		var registeredAt *time.Time
		var hasInsurance *int
		if err := rows.Scan(&id, &kind, &desc, &registeredAt, &hasInsurance); err != nil {
			return err
		}
		if contains(chargeKinds, kind) && registeredAt == nil {
			return httpx.ErrConflict(
				"Cannot approve: charge registration not recorded for " + kind + " (" + desc + ").")
		}
		if contains(insuranceKinds, kind) && hasInsurance == nil {
			return httpx.ErrConflict(
				"Cannot approve: no active insurance on " + kind + " (" + desc + ").")
		}
	}
	return rows.Err()
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
