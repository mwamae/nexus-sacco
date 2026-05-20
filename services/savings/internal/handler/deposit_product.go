// Deposit product configuration handlers — CRUD against deposit_products.

package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type ProductHandler struct {
	DB       *db.Pool
	Products *store.DepositProductStore
	Logger   *slog.Logger
}

type productReq struct {
	Code                        string                       `json:"code"`
	Name                        string                       `json:"name"`
	ProductType                 domain.DepositProductType    `json:"product_type"`
	Description                 string                       `json:"description"`
	IsActive                    *bool                        `json:"is_active"`
	MinOpeningBalance           decimal.Decimal              `json:"min_opening_balance"`
	MinOperatingBalance         decimal.Decimal              `json:"min_operating_balance"`
	MaxBalance                  *decimal.Decimal             `json:"max_balance"`
	MinDepositAmount            decimal.Decimal              `json:"min_deposit_amount"`
	MaxDepositAmount            *decimal.Decimal             `json:"max_deposit_amount"`
	MinWithdrawalAmount         decimal.Decimal              `json:"min_withdrawal_amount"`
	MaxWithdrawalAmount         *decimal.Decimal             `json:"max_withdrawal_amount"`
	NoticePeriodDays            int                          `json:"notice_period_days"`
	MaxWithdrawalsPerMonth      *int                         `json:"max_withdrawals_per_month"`
	PartialWithdrawalAllowed    *bool                        `json:"partial_withdrawal_allowed"`
	LargeWithdrawalThreshold    *decimal.Decimal             `json:"large_withdrawal_threshold"`
	LockInMonths                int                          `json:"lock_in_months"`
	DefaultTermMonths           *int                         `json:"default_term_months"`
	MaturityAction              domain.MaturityAction        `json:"maturity_action"`
	Eligibility                 domain.DepositEligibility    `json:"eligibility"`
	RequiresApprovalToOpen      *bool                        `json:"requires_approval_to_open"`
	WithdrawalWindowStartMonth  *int                         `json:"withdrawal_window_start_month"`
	WithdrawalWindowEndMonth    *int                         `json:"withdrawal_window_end_month"`
	MaintenanceFee              decimal.Decimal              `json:"maintenance_fee"`
	MaintenanceFeeFrequency     domain.FeeFrequency          `json:"maintenance_fee_frequency"`
	EarlyWithdrawalPenaltyPct   decimal.Decimal              `json:"early_withdrawal_penalty_pct"`
	BelowMinBalanceFee          decimal.Decimal              `json:"below_min_balance_fee"`
	DormancyFeeMonthly          decimal.Decimal              `json:"dormancy_fee_monthly"`
}

func (in *productReq) fill(p *domain.DepositProduct) error {
	if in.Code != "" {
		p.Code = domain.NormalizeProductCode(in.Code)
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	if in.ProductType != "" {
		if !in.ProductType.Valid() {
			return errors.New("invalid product_type")
		}
		p.ProductType = in.ProductType
	}
	if in.Eligibility != "" {
		if !in.Eligibility.Valid() {
			return errors.New("invalid eligibility")
		}
		p.Eligibility = in.Eligibility
	}
	if in.MaturityAction != "" {
		p.MaturityAction = in.MaturityAction
	}
	if in.MaintenanceFeeFrequency != "" {
		p.MaintenanceFeeFrequency = in.MaintenanceFeeFrequency
	}
	if in.Description != "" {
		s := in.Description
		p.Description = &s
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if in.PartialWithdrawalAllowed != nil {
		p.PartialWithdrawalAllowed = *in.PartialWithdrawalAllowed
	}
	if in.RequiresApprovalToOpen != nil {
		p.RequiresApprovalToOpen = *in.RequiresApprovalToOpen
	}
	// Numeric / int / nullable fields — overwrite always, since "missing"
	// fields decode to zero values and zero is a meaningful config.
	p.MinOpeningBalance = in.MinOpeningBalance
	p.MinOperatingBalance = in.MinOperatingBalance
	p.MaxBalance = in.MaxBalance
	p.MinDepositAmount = in.MinDepositAmount
	p.MaxDepositAmount = in.MaxDepositAmount
	p.MinWithdrawalAmount = in.MinWithdrawalAmount
	p.MaxWithdrawalAmount = in.MaxWithdrawalAmount
	p.NoticePeriodDays = in.NoticePeriodDays
	p.MaxWithdrawalsPerMonth = in.MaxWithdrawalsPerMonth
	p.LargeWithdrawalThreshold = in.LargeWithdrawalThreshold
	p.LockInMonths = in.LockInMonths
	p.DefaultTermMonths = in.DefaultTermMonths
	p.WithdrawalWindowStartMonth = in.WithdrawalWindowStartMonth
	p.WithdrawalWindowEndMonth = in.WithdrawalWindowEndMonth
	p.MaintenanceFee = in.MaintenanceFee
	p.EarlyWithdrawalPenaltyPct = in.EarlyWithdrawalPenaltyPct
	p.BelowMinBalanceFee = in.BelowMinBalanceFee
	p.DormancyFeeMonthly = in.DormancyFeeMonthly
	return nil
}

func (h *ProductHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in productReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Code == "" || in.Name == "" || in.ProductType == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code, name, and product_type are required"))
		return
	}
	if in.Eligibility == "" {
		in.Eligibility = domain.EligibilityIndividuals
	}
	if in.MaturityAction == "" {
		in.MaturityAction = domain.MaturityNone
	}
	if in.MaintenanceFeeFrequency == "" {
		in.MaintenanceFeeFrequency = domain.FeeNone
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	// Defaults match the schema's column defaults so omitting a flag
	// in the create payload keeps the safe behaviour.
	p := &domain.DepositProduct{
		IsActive:                 true,
		PartialWithdrawalAllowed: true,
		CreatedBy:                &userID,
	}
	if err := in.fill(p); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(err.Error()))
		return
	}

	var out *domain.DepositProduct
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Products.CreateTx(r.Context(), tx, p)
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a product with that code already exists"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *ProductHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in productReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.DepositProduct
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Products.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Code is immutable post-create.
		in.Code = ""
		// ProductType is immutable post-create.
		in.ProductType = ""
		if err := in.fill(existing); err != nil {
			return httpx.ErrBadRequest(err.Error())
		}
		out, err = h.Products.UpdateTx(r.Context(), tx, existing)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("product not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *ProductHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	includeInactive := r.URL.Query().Get("include_inactive") == "1"
	var out []domain.DepositProduct
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Products.ListTx(r.Context(), tx, includeInactive)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.DepositProduct{}
	}
	httpx.OK(w, out)
}

func (h *ProductHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var p *domain.DepositProduct
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		p, err = h.Products.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("product not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, p)
}

func (h *ProductHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
			httpx.WriteErr(w, r, httpx.ErrNotFound("product not found"))
			return
		}
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		return
	}
	httpx.NoContent(w)
}

// Silence "imported and not used" for chi when stub is empty.
var _ = chi.NewRouter
var _ = uuid.Nil
