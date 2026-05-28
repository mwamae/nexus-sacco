// Loans Phase 6 — credit-life insurance HTTP surface.
//
//   GET  /v1/loans/{loan_id}/insurance-policy
//        Returns the policy (if any) for the loan.
//
//   POST /v1/loans/{loan_id}/insurance-policy
//        Manually create / re-issue a policy. Used when a disbursement
//        finalised without insurance (e.g. provider call failed) and
//        the officer wants to retry. Idempotent on (loan_id) via the
//        UNIQUE constraint on loan_insurance_policies.
//
// Disbursement-time placement lives in
// loan_disbursement_executor.go::placeInsurancePolicyOnDisburseTx —
// it runs when the loan's product has insurance_provider_id set.

package handler

import (
	"context"
	"encoding/json"
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
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/insurance"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type InsuranceHandler struct {
	DB     *db.Pool
	Loans  *store.LoanStore
	Logger *slog.Logger
}

type insurancePolicyRow struct {
	ID             uuid.UUID  `json:"id"`
	LoanID         uuid.UUID  `json:"loan_id"`
	ProviderID     uuid.UUID  `json:"provider_id"`
	ProviderName   string     `json:"provider_name"`
	PolicyNo       *string    `json:"policy_no"`
	PremiumAmount  string     `json:"premium_amount"`
	CoverageAmount string     `json:"coverage_amount"`
	EffectiveFrom  string     `json:"effective_from"`
	EffectiveTo    string     `json:"effective_to"`
	Status         string     `json:"status"`
	Sandbox        bool       `json:"sandbox"`
	CreatedAt      time.Time  `json:"created_at"`
	CancelledAt    *time.Time `json:"cancelled_at,omitempty"`
}

// ─────────── GET ───────────

func (h *InsuranceHandler) Get(w http.ResponseWriter, r *http.Request) {
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var row *insurancePolicyRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var p insurancePolicyRow
		err := tx.QueryRow(r.Context(), `
			SELECT lip.id, lip.loan_id, lip.provider_id, ip.name,
			       lip.policy_no, lip.premium_amount::text, lip.coverage_amount::text,
			       lip.effective_from::text, lip.effective_to::text,
			       lip.status, lip.sandbox, lip.created_at, lip.cancelled_at
			  FROM loan_insurance_policies lip
			  JOIN insurance_providers ip ON ip.id = lip.provider_id
			 WHERE lip.loan_id = $1
		`, loanID).Scan(
			&p.ID, &p.LoanID, &p.ProviderID, &p.ProviderName,
			&p.PolicyNo, &p.PremiumAmount, &p.CoverageAmount,
			&p.EffectiveFrom, &p.EffectiveTo,
			&p.Status, &p.Sandbox, &p.CreatedAt, &p.CancelledAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // row stays nil → 200 with policy=null
		}
		if err != nil {
			return err
		}
		row = &p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, map[string]any{"policy": row})
}

// ─────────── POST (manual placement) ───────────

func (h *InsuranceHandler) Place(w http.ResponseWriter, r *http.Request) {
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var placed *insurancePolicyRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
		if err != nil {
			return err
		}
		policy, err := placeInsuranceForLoanTx(r.Context(), tx, loan)
		if err != nil {
			return err
		}
		if policy == nil {
			return httpx.ErrBadRequest("loan's product has no insurance_provider_id; cannot place")
		}
		placed = policy
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.Created(w, map[string]any{"policy": placed})
}

// ─────────── Disbursement hook ───────────

// placeInsuranceForLoanTx issues a credit-life policy for a loan
// whose product has insurance_provider_id set. Returns (nil, nil)
// if the product has no provider configured (no-op). Idempotent on
// loan_insurance_policies.UNIQUE(loan_id) — re-running an existing
// row just returns it.
func placeInsuranceForLoanTx(
	ctx context.Context, tx pgx.Tx, loan *domain.Loan,
) (*insurancePolicyRow, error) {
	// Existing policy?
	var existing insurancePolicyRow
	err := tx.QueryRow(ctx, `
		SELECT lip.id, lip.loan_id, lip.provider_id, ip.name,
		       lip.policy_no, lip.premium_amount::text, lip.coverage_amount::text,
		       lip.effective_from::text, lip.effective_to::text,
		       lip.status, lip.sandbox, lip.created_at, lip.cancelled_at
		  FROM loan_insurance_policies lip
		  JOIN insurance_providers ip ON ip.id = lip.provider_id
		 WHERE lip.loan_id = $1
	`, loan.ID).Scan(
		&existing.ID, &existing.LoanID, &existing.ProviderID, &existing.ProviderName,
		&existing.PolicyNo, &existing.PremiumAmount, &existing.CoverageAmount,
		&existing.EffectiveFrom, &existing.EffectiveTo,
		&existing.Status, &existing.Sandbox, &existing.CreatedAt, &existing.CancelledAt,
	)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Resolve provider via the loan's product.
	var providerID *uuid.UUID
	var insuranceMandatory bool
	if err := tx.QueryRow(ctx, `
		SELECT insurance_provider_id, insurance_mandatory FROM loan_products WHERE id = $1
	`, loan.ProductID).Scan(&providerID, &insuranceMandatory); err != nil {
		return nil, err
	}
	if providerID == nil {
		return nil, nil
	}

	// Load provider config.
	var providerCode, name string
	var ratePct, minPremium decimal.Decimal
	var maxPremiumRaw *decimal.Decimal
	var sandbox bool
	if err := tx.QueryRow(ctx, `
		SELECT provider_code, name, premium_rate_pct, min_premium, max_premium, sandbox
		  FROM insurance_providers WHERE id = $1
	`, *providerID).Scan(&providerCode, &name, &ratePct, &minPremium, &maxPremiumRaw, &sandbox); err != nil {
		return nil, err
	}

	premium := insurance.QuotePremium(loan.Principal, ratePct, minPremium, maxPremiumRaw)

	// Resolve member details for the policy call.
	var memberName, nationalID, phone string
	_ = tx.QueryRow(ctx, `
		SELECT cd.full_name, COALESCE(m.id_doc_number, ''), COALESCE(m.phone, '')
		  FROM counterparty_directory cd
		  LEFT JOIN members m ON m.id = cd.member_id
		 WHERE cd.counterparty_id = $1
	`, loan.CounterpartyID).Scan(&memberName, &nationalID, &phone)

	provider, err := insurance.NewProvider(providerCode, insurance.Creds{})
	if err != nil {
		return nil, err
	}
	effFrom := time.Now().UTC()
	effTo := effFrom.AddDate(0, loan.TermMonths, 0)
	res, err := provider.CreatePolicy(ctx, insurance.PolicyInput{
		LoanID: loan.ID, MemberName: memberName, NationalID: nationalID, Phone: phone,
		PrincipalAmount: loan.Principal, TermMonths: loan.TermMonths,
		EffectiveFrom: effFrom, EffectiveTo: effTo,
	})
	if err != nil {
		return nil, err
	}
	vendorJSON, _ := json.Marshal(res.VendorResponse)

	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO loan_insurance_policies (
		  tenant_id, loan_id, provider_id, policy_no,
		  premium_amount, coverage_amount,
		  effective_from, effective_to, status, vendor_response, sandbox
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5,
		  $6, $7, 'active', $8::jsonb, $9
		)
		ON CONFLICT (loan_id) DO NOTHING
		RETURNING id
	`,
		loan.ID, *providerID, res.PolicyNo,
		premium, res.CoverageAmount,
		effFrom, effTo, vendorJSON, sandbox || res.Sandbox,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Race — another caller inserted between SELECT + INSERT.
			// Re-load.
			return placeInsuranceForLoanTx(ctx, tx, loan)
		}
		return nil, err
	}

	return &insurancePolicyRow{
		ID:             id,
		LoanID:         loan.ID,
		ProviderID:     *providerID,
		ProviderName:   name,
		PolicyNo:       &res.PolicyNo,
		PremiumAmount:  premium.String(),
		CoverageAmount: res.CoverageAmount.String(),
		EffectiveFrom:  effFrom.Format("2006-01-02"),
		EffectiveTo:    effTo.Format("2006-01-02"),
		Status:         "active",
		Sandbox:        sandbox || res.Sandbox,
		CreatedAt:      time.Now().UTC(),
	}, nil
}
