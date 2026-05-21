// Loan product configuration handlers — CRUD against loan_products
// plus the per-tenant purpose-category list.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type LoanProductHandler struct {
	DB       *db.Pool
	Products *store.LoanProductStore
	Logger   *slog.Logger
}

// feeIn is the request-side shape for a single product fee. id and
// display_order are accepted but not used — the store regenerates rows
// on each save and derives order from the slice position. Accepting
// them prevents the strict JSON decoder from rejecting payloads that
// round-trip a GET-response back into a PUT.
type feeIn struct {
	ID           string               `json:"id,omitempty"`
	ProductID    string               `json:"product_id,omitempty"`
	Name         string               `json:"name"`
	Amount       decimal.Decimal      `json:"amount"`
	IsPct        bool                 `json:"is_pct"`
	Timing       domain.LoanFeeTiming `json:"timing"`
	DisplayOrder int                  `json:"display_order,omitempty"`
	CreatedAt    string               `json:"created_at,omitempty"`
	UpdatedAt    string               `json:"updated_at,omitempty"`
}

func feesFromReq(in []feeIn) ([]domain.LoanProductFee, error) {
	out := make([]domain.LoanProductFee, 0, len(in))
	for i, f := range in {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			return nil, fmt.Errorf("fee #%d is missing a name", i+1)
		}
		if f.Amount.LessThan(decimal.Zero) {
			return nil, fmt.Errorf("fee %q has negative amount", name)
		}
		t := f.Timing
		if t == "" {
			t = domain.FeeUpfront
		}
		if !validTiming(t) {
			return nil, fmt.Errorf("fee %q has invalid timing %q", name, t)
		}
		out = append(out, domain.LoanProductFee{
			Name: name, Amount: f.Amount, IsPct: f.IsPct, Timing: t, DisplayOrder: i + 1,
		})
	}
	return out, nil
}

func validTiming(t domain.LoanFeeTiming) bool {
	switch t {
	case domain.FeeUpfront, domain.FeeAddedToLoan, domain.FeeAtEachInstallment:
		return true
	}
	return false
}

type loanProductReq struct {
	Code                       string                            `json:"code"`
	Name                       string                            `json:"name"`
	Category                   domain.LoanCategory               `json:"category"`
	Description                *string                           `json:"description,omitempty"`
	IsActive                   *bool                             `json:"is_active,omitempty"`

	MinAmount                  decimal.Decimal                   `json:"min_amount"`
	MaxAmount                  decimal.Decimal                   `json:"max_amount"`
	MultiplierBasis            domain.LoanMultiplierBasis        `json:"multiplier_basis"`
	MultiplierValue            *decimal.Decimal                  `json:"multiplier_value,omitempty"`

	MinTermMonths              int                               `json:"min_term_months"`
	MaxTermMonths              int                               `json:"max_term_months"`
	DefaultTermMonths          *int                              `json:"default_term_months,omitempty"`
	GracePeriodMonths          int                               `json:"grace_period_months"`

	InterestRatePct            decimal.Decimal                   `json:"interest_rate_pct"`
	InterestMethod             domain.LoanInterestMethod         `json:"interest_method"`
	RepaymentMethod            domain.LoanRepaymentMethod        `json:"repayment_method"`

	// Fees is the full list of fees this product charges. Send an empty
	// array (or omit the field) to charge no fees. Any names are allowed.
	Fees                       []feeIn                           `json:"fees,omitempty"`

	PenaltyRatePct             decimal.Decimal                   `json:"penalty_rate_pct"`

	MinGuarantors              int                               `json:"min_guarantors"`
	MaxGuarantorExposurePct    decimal.Decimal                   `json:"max_guarantor_exposure_pct"`
	GuarantorMustBeMember      *bool                             `json:"guarantor_must_be_member,omitempty"`
	CollateralRequirement      domain.LoanCollateralRequirement  `json:"collateral_requirement"`

	MinMembershipMonths        int                               `json:"min_membership_months"`
	MinSharesRequired          int                               `json:"min_shares_required"`
	AllowConcurrent            *bool                             `json:"allow_concurrent,omitempty"`

	WorkflowDefinitionCode     *string                           `json:"workflow_definition_code,omitempty"`
	AutoApprovalThreshold      *decimal.Decimal                  `json:"auto_approval_threshold,omitempty"`
	AutoApprovalMinScore       *int                              `json:"auto_approval_min_score,omitempty"`

	AllowTopup                 *bool                             `json:"allow_topup,omitempty"`
	AllowRefinance             *bool                             `json:"allow_refinance,omitempty"`
}

func (in *loanProductReq) fill(p *domain.LoanProduct) error {
	if in.Code != "" {
		p.Code = domain.NormalizeLoanCode(in.Code)
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	if in.Category != "" {
		if !in.Category.Valid() {
			return errors.New("invalid category")
		}
		p.Category = in.Category
	}
	if in.Description != nil {
		p.Description = in.Description
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if in.MultiplierBasis != "" {
		p.MultiplierBasis = in.MultiplierBasis
	}
	p.MultiplierValue = in.MultiplierValue
	if in.InterestMethod != "" {
		if !in.InterestMethod.Valid() {
			return errors.New("invalid interest_method")
		}
		p.InterestMethod = in.InterestMethod
	}
	if in.RepaymentMethod != "" {
		if !in.RepaymentMethod.Valid() {
			return errors.New("invalid repayment_method")
		}
		p.RepaymentMethod = in.RepaymentMethod
	}
	if in.CollateralRequirement != "" {
		p.CollateralRequirement = in.CollateralRequirement
	}
	if in.GuarantorMustBeMember != nil {
		p.GuarantorMustBeMember = *in.GuarantorMustBeMember
	}
	if in.AllowConcurrent != nil {
		p.AllowConcurrent = *in.AllowConcurrent
	}
	if in.AllowTopup != nil {
		p.AllowTopup = *in.AllowTopup
	}
	if in.AllowRefinance != nil {
		p.AllowRefinance = *in.AllowRefinance
	}
	// Numeric / int overwrites (zero is meaningful for these)
	p.MinAmount = in.MinAmount
	p.MaxAmount = in.MaxAmount
	p.MinTermMonths = in.MinTermMonths
	p.MaxTermMonths = in.MaxTermMonths
	p.DefaultTermMonths = in.DefaultTermMonths
	p.GracePeriodMonths = in.GracePeriodMonths
	p.InterestRatePct = in.InterestRatePct
	p.PenaltyRatePct = in.PenaltyRatePct
	p.MinGuarantors = in.MinGuarantors
	p.MaxGuarantorExposurePct = in.MaxGuarantorExposurePct
	p.MinMembershipMonths = in.MinMembershipMonths
	p.MinSharesRequired = in.MinSharesRequired
	p.WorkflowDefinitionCode = in.WorkflowDefinitionCode
	p.AutoApprovalThreshold = in.AutoApprovalThreshold
	p.AutoApprovalMinScore = in.AutoApprovalMinScore
	fees, ferr := feesFromReq(in.Fees)
	if ferr != nil {
		return ferr
	}
	p.Fees = fees
	return nil
}

func (h *LoanProductHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in loanProductReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Code == "" || in.Name == "" || in.Category == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code, name, and category are required"))
		return
	}
	if in.MaxAmount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("max_amount must be > 0"))
		return
	}
	if in.MaxTermMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("max_term_months must be > 0"))
		return
	}
	if in.MinTermMonths <= 0 {
		in.MinTermMonths = 1
	}
	if in.InterestMethod == "" {
		in.InterestMethod = domain.InterestReducing
	}
	if in.RepaymentMethod == "" {
		in.RepaymentMethod = domain.RepayReducingBalance
	}
	if in.CollateralRequirement == "" {
		in.CollateralRequirement = domain.CollateralNotApplicable
	}
	if in.MultiplierBasis == "" {
		in.MultiplierBasis = domain.MultiplierNone
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	p := &domain.LoanProduct{
		IsActive:              true,
		GuarantorMustBeMember: true,
		AllowConcurrent:       false,
		AllowTopup:            false,
		AllowRefinance:        false,
		CreatedBy:             &userID,
	}
	if err := in.fill(p); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(err.Error()))
		return
	}
	if p.MinAmount.GreaterThan(p.MaxAmount) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("min_amount must be <= max_amount"))
		return
	}
	if p.MinTermMonths > p.MaxTermMonths {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("min_term_months must be <= max_term_months"))
		return
	}
	var out *domain.LoanProduct
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Products.CreateTx(r.Context(), tx, p)
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a loan product with that code already exists"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *LoanProductHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in loanProductReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanProduct
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Products.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Code + category are immutable post-create (avoid breaking audits).
		in.Code = ""
		in.Category = ""
		if err := in.fill(existing); err != nil {
			return httpx.ErrBadRequest(err.Error())
		}
		out, err = h.Products.UpdateTx(r.Context(), tx, existing)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("loan product not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *LoanProductHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	includeInactive := r.URL.Query().Get("include_inactive") == "1"
	out := []domain.LoanProduct{}
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.Products.ListTx(r.Context(), tx, includeInactive)
		if err != nil {
			return err
		}
		if l != nil {
			out = l
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *LoanProductHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var p *domain.LoanProduct
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		p, err = h.Products.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("loan product not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, p)
}

func (h *LoanProductHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Products.DeleteTx(r.Context(), tx, id)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("loan product not found"))
			return
		}
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		return
	}
	httpx.NoContent(w)
}

// ─────────── Purpose categories ───────────

type purposeReq struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	IsActive *bool  `json:"is_active,omitempty"`
}

func (h *LoanProductHandler) ListPurposeCategories(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	includeInactive := r.URL.Query().Get("include_inactive") == "1"
	out := []domain.LoanPurposeCategory{}
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.Products.ListPurposeCategoriesTx(r.Context(), tx, includeInactive)
		if err != nil {
			return err
		}
		if l != nil {
			out = l
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *LoanProductHandler) CreatePurposeCategory(w http.ResponseWriter, r *http.Request) {
	var in purposeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Code == "" || in.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code and name are required"))
		return
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanPurposeCategory
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Products.CreatePurposeCategoryTx(r.Context(), tx, &domain.LoanPurposeCategory{
			Code: domain.NormalizeLoanCode(in.Code), Name: in.Name, IsActive: active,
		})
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a purpose category with that code already exists"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

var _ = uuid.Nil
