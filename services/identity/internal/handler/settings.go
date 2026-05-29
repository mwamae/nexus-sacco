// Tenant-side settings: branding, region, operations.
//
//   GET    /v1/tenant/settings              — all three sections + tenant base
//   PATCH  /v1/tenant/branding              — colors, font, sms/email overrides, custom domain
//   POST   /v1/tenant/branding/logo         — multipart logo upload
//   GET    /v1/tenant/branding/logo         — serve current logo bytes (public-ish; auth required)
//   DELETE /v1/tenant/branding/logo         — drop the logo
//   PATCH  /v1/tenant/region                — timezone, language, date format, tax rates
//   PATCH  /v1/tenant/operations            — lending / savings / dividend / penalty / guarantor / approvals
//
// All endpoints require the request to be on a tenant subdomain
// (RequireTenant) and the caller to hold tenant:settings:view or
// :edit as appropriate.

package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/storage"
	"github.com/nexussacco/identity/internal/store"
)

type SettingsHandler struct {
	DB        *db.Pool
	Settings  *store.SettingsStore
	Audit     *store.AuditStore
	Storage   storage.Storage
	MaxUpload int64
	Logger    *slog.Logger
}

// ─────────── GET /v1/tenant/settings ───────────

type settingsResponse struct {
	Tenant     *domain.Tenant            `json:"tenant"`
	Branding   *domain.TenantBranding    `json:"branding"`
	Region     *domain.TenantRegion      `json:"region"`
	Operations *domain.TenantOperations  `json:"operations"`
	Membership *domain.TenantMembership  `json:"membership"`
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var out settingsResponse
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		b, err := h.Settings.GetOrInitBrandingTx(r.Context(), tx, tenant.ID)
		if err != nil {
			return err
		}
		rg, err := h.Settings.GetOrInitRegionTx(r.Context(), tx, tenant.ID)
		if err != nil {
			return err
		}
		op, err := h.Settings.GetOrInitOperationsTx(r.Context(), tx, tenant.ID)
		if err != nil {
			return err
		}
		mb, err := h.Settings.GetOrInitMembershipTx(r.Context(), tx, tenant.ID)
		if err != nil {
			return err
		}
		out.Branding = b
		out.Region = rg
		out.Operations = op
		out.Membership = mb
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	out.Tenant = tenant
	httpx.OK(w, out)
}

// ─────────── PATCH /v1/tenant/branding ───────────

type brandingPatchDTO struct {
	PrimaryColor  *string `json:"primary_color"`
	AccentColor   *string `json:"accent_color"`
	FontFamily    *string `json:"font_family"`
	EmailFromName *string `json:"email_from_name"`
	SMSSenderID   *string `json:"sms_sender_id"`
	CustomDomain  *string `json:"custom_domain"`
}

var hexRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func (h *SettingsHandler) UpdateBranding(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var req brandingPatchDTO
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.PrimaryColor != nil && !hexRE.MatchString(*req.PrimaryColor) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("primary_color must be #RRGGBB"))
		return
	}
	if req.AccentColor != nil && !hexRE.MatchString(*req.AccentColor) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("accent_color must be #RRGGBB"))
		return
	}

	var updated *domain.TenantBranding
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		patch := store.BrandingPatch{
			PrimaryColor:  req.PrimaryColor,
			AccentColor:   req.AccentColor,
			FontFamily:    req.FontFamily,
			EmailFromName: req.EmailFromName,
			SMSSenderID:   req.SMSSenderID,
			CustomDomain:  req.CustomDomain,
		}
		if err := h.Settings.UpdateBrandingTx(r.Context(), tx, tenant.ID, patch); err != nil {
			return err
		}
		var err error
		updated, err = h.Settings.GetOrInitBrandingTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenant.ID, "tenant.branding_updated", nil)
	httpx.OK(w, updated)
}

// ─────────── POST /v1/tenant/branding/logo ───────────

func (h *SettingsHandler) UploadLogo(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	r.Body = http.MaxBytesReader(w, r.Body, h.MaxUpload+1024)
	if err := r.ParseMultipartForm(h.MaxUpload); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid upload: "+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing 'file' field"))
		return
	}
	defer file.Close()
	if header.Size > h.MaxUpload {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file too large"))
		return
	}
	mime := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/svg+xml", "image/webp":
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("logo must be PNG, JPEG, WebP, or SVG"))
		return
	}

	path, size, err := h.Storage.Save(tenant.ID, uuid.Nil, "branding/logo", mime, file, header.Size)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var updated *domain.TenantBranding
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		if err := h.Settings.SetLogoTx(r.Context(), tx, tenant.ID, store.LogoMeta{
			StoragePath: path, MIME: mime, SizeBytes: size,
		}); err != nil {
			return err
		}
		updated, err = h.Settings.GetOrInitBrandingTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		_ = h.Storage.Delete(path)
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenant.ID, "tenant.logo_uploaded", map[string]any{"mime": mime, "size": size})
	httpx.Created(w, updated)
}

// ─────────── GET /v1/tenant/branding/logo ───────────

func (h *SettingsHandler) DownloadLogo(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var path, mime string
	var size int64
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		var err error
		path, mime, size, err = h.Settings.LogoPathTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if path == "" {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no logo on file"))
		return
	}
	f, err := h.Storage.Open(path)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mime)
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, f)
}

// ─────────── DELETE /v1/tenant/branding/logo ───────────

func (h *SettingsHandler) ClearLogo(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var oldPath string
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		p, _, _, err := h.Settings.LogoPathTx(r.Context(), tx, tenant.ID)
		if err != nil {
			return err
		}
		oldPath = p
		return h.Settings.ClearLogoTx(r.Context(), tx, tenant.ID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if oldPath != "" {
		_ = h.Storage.Delete(oldPath)
	}
	h.audit(r, tenant.ID, "tenant.logo_cleared", nil)
	httpx.NoContent(w)
}

// ─────────── PATCH /v1/tenant/region ───────────

type regionPatchDTO struct {
	Timezone           *string  `json:"timezone"`
	Language           *string  `json:"language"`
	DateFormat         *string  `json:"date_format"`
	Regulator          *string  `json:"regulator"`
	Jurisdiction       *string  `json:"jurisdiction"`
	VATRate            *float64 `json:"vat_rate"`
	WithholdingTaxRate *float64 `json:"withholding_tax_rate"`
}

func (h *SettingsHandler) UpdateRegion(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var req regionPatchDTO
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid timezone: "+*req.Timezone))
			return
		}
	}
	if req.Language != nil {
		l := strings.ToLower(strings.TrimSpace(*req.Language))
		if len(l) < 2 || len(l) > 5 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("language must be an ISO code"))
			return
		}
		req.Language = &l
	}
	if req.VATRate != nil && (*req.VATRate < 0 || *req.VATRate > 100) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("vat_rate must be between 0 and 100"))
		return
	}
	if req.WithholdingTaxRate != nil && (*req.WithholdingTaxRate < 0 || *req.WithholdingTaxRate > 100) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("withholding_tax_rate must be between 0 and 100"))
		return
	}

	var updated *domain.TenantRegion
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		patch := store.RegionPatch{
			Timezone: req.Timezone, Language: req.Language, DateFormat: req.DateFormat,
			Regulator: req.Regulator, Jurisdiction: req.Jurisdiction,
			VATRate: req.VATRate, WithholdingTaxRate: req.WithholdingTaxRate,
		}
		if err := h.Settings.UpdateRegionTx(r.Context(), tx, tenant.ID, patch); err != nil {
			return err
		}
		var err error
		updated, err = h.Settings.GetOrInitRegionTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenant.ID, "tenant.region_updated", nil)
	httpx.OK(w, updated)
}

// ─────────── PATCH /v1/tenant/operations ───────────

type operationsPatchDTO struct {
	LoanMinAmount         *float64 `json:"loan_min_amount"`
	LoanMaxAmount         *float64 `json:"loan_max_amount"`
	LoanMaxTermMonths     *int     `json:"loan_max_term_months"`
	DefaultInterestMethod *string  `json:"default_interest_method"`
	DefaultInterestRate   *float64 `json:"default_interest_rate"`

	SavingsMinOpeningBal *float64 `json:"savings_min_opening_bal"`
	SavingsMinRunningBal *float64 `json:"savings_min_running_bal"`
	SavingsWithdrawalFee *float64 `json:"savings_withdrawal_fee"`

	DividendRate      *float64 `json:"dividend_rate"`
	DividendFrequency *string  `json:"dividend_frequency"`

	PenaltyLateFeeRate     *float64 `json:"penalty_late_fee_rate"`
	PenaltyGracePeriodDays *int     `json:"penalty_grace_period_days"`

	GuarantorMinCount      *int     `json:"guarantor_min_count"`
	GuarantorSelfMaxAmount *float64 `json:"guarantor_self_max_amount"`

	ApprovalBranchLimit *float64 `json:"approval_branch_limit"`
	ApprovalCreditLimit *float64 `json:"approval_credit_limit"`
	ApprovalBoardLimit  *float64 `json:"approval_board_limit"`

	// Phase 1.5a — collateral defaults.
	DefaultSecurityModel         *string  `json:"default_security_model"`
	DefaultMinGuarantorCoverPct  *float64 `json:"default_min_guarantor_cover_pct"`
	DefaultMinCollateralCoverPct *float64 `json:"default_min_collateral_cover_pct"`
	CollateralRevaluationMonths  *int     `json:"collateral_revaluation_months"`
}

func (h *SettingsHandler) UpdateOperations(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var req operationsPatchDTO
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if err := validateOps(req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var updated *domain.TenantOperations
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		patch := store.OperationsPatch{
			LoanMinAmount: req.LoanMinAmount, LoanMaxAmount: req.LoanMaxAmount,
			LoanMaxTermMonths: req.LoanMaxTermMonths,
			DefaultInterestMethod: req.DefaultInterestMethod, DefaultInterestRate: req.DefaultInterestRate,

			SavingsMinOpeningBal: req.SavingsMinOpeningBal,
			SavingsMinRunningBal: req.SavingsMinRunningBal,
			SavingsWithdrawalFee: req.SavingsWithdrawalFee,

			DividendRate: req.DividendRate, DividendFrequency: req.DividendFrequency,

			PenaltyLateFeeRate: req.PenaltyLateFeeRate, PenaltyGracePeriodDays: req.PenaltyGracePeriodDays,

			GuarantorMinCount: req.GuarantorMinCount, GuarantorSelfMaxAmount: req.GuarantorSelfMaxAmount,

			ApprovalBranchLimit: req.ApprovalBranchLimit,
			ApprovalCreditLimit: req.ApprovalCreditLimit,
			ApprovalBoardLimit:  req.ApprovalBoardLimit,

			DefaultSecurityModel:         req.DefaultSecurityModel,
			DefaultMinGuarantorCoverPct:  req.DefaultMinGuarantorCoverPct,
			DefaultMinCollateralCoverPct: req.DefaultMinCollateralCoverPct,
			CollateralRevaluationMonths:  req.CollateralRevaluationMonths,
		}
		if err := h.Settings.UpdateOperationsTx(r.Context(), tx, tenant.ID, patch); err != nil {
			return err
		}
		var err error
		updated, err = h.Settings.GetOrInitOperationsTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenant.ID, "tenant.operations_updated", nil)
	httpx.OK(w, updated)
}

func validateOps(p operationsPatchDTO) error {
	if p.LoanMinAmount != nil && *p.LoanMinAmount < 0 {
		return httpx.ErrBadRequest("loan_min_amount must be ≥ 0")
	}
	if p.LoanMaxAmount != nil && *p.LoanMaxAmount < 0 {
		return httpx.ErrBadRequest("loan_max_amount must be ≥ 0")
	}
	if p.LoanMinAmount != nil && p.LoanMaxAmount != nil && *p.LoanMinAmount > *p.LoanMaxAmount {
		return httpx.ErrBadRequest("loan_min_amount cannot exceed loan_max_amount")
	}
	if p.LoanMaxTermMonths != nil && *p.LoanMaxTermMonths < 1 {
		return httpx.ErrBadRequest("loan_max_term_months must be ≥ 1")
	}
	if p.DefaultInterestMethod != nil {
		m := domain.InterestMethod(strings.ToLower(strings.TrimSpace(*p.DefaultInterestMethod)))
		switch m {
		case domain.InterestFlat, domain.InterestReducingBalance, domain.InterestDecliningBalance:
		default:
			return httpx.ErrBadRequest("default_interest_method must be flat, reducing_balance, or declining_balance")
		}
	}
	if p.DefaultInterestRate != nil && (*p.DefaultInterestRate < 0 || *p.DefaultInterestRate > 200) {
		return httpx.ErrBadRequest("default_interest_rate must be between 0 and 200")
	}
	if p.DividendRate != nil && (*p.DividendRate < 0 || *p.DividendRate > 100) {
		return httpx.ErrBadRequest("dividend_rate must be between 0 and 100")
	}
	if p.DividendFrequency != nil {
		f := strings.ToLower(strings.TrimSpace(*p.DividendFrequency))
		switch f {
		case "annual", "semi_annual", "quarterly":
		default:
			return httpx.ErrBadRequest("dividend_frequency must be annual, semi_annual, or quarterly")
		}
	}
	if p.PenaltyLateFeeRate != nil && (*p.PenaltyLateFeeRate < 0 || *p.PenaltyLateFeeRate > 100) {
		return httpx.ErrBadRequest("penalty_late_fee_rate must be between 0 and 100")
	}
	if p.PenaltyGracePeriodDays != nil && *p.PenaltyGracePeriodDays < 0 {
		return httpx.ErrBadRequest("penalty_grace_period_days must be ≥ 0")
	}
	if p.GuarantorMinCount != nil && *p.GuarantorMinCount < 0 {
		return httpx.ErrBadRequest("guarantor_min_count must be ≥ 0")
	}
	if p.GuarantorSelfMaxAmount != nil && *p.GuarantorSelfMaxAmount < 0 {
		return httpx.ErrBadRequest("guarantor_self_max_amount must be ≥ 0")
	}
	if p.ApprovalBranchLimit != nil && p.ApprovalCreditLimit != nil && *p.ApprovalBranchLimit > *p.ApprovalCreditLimit {
		return httpx.ErrBadRequest("approval_branch_limit must be ≤ approval_credit_limit")
	}
	if p.ApprovalCreditLimit != nil && p.ApprovalBoardLimit != nil && *p.ApprovalCreditLimit > *p.ApprovalBoardLimit {
		return httpx.ErrBadRequest("approval_credit_limit must be ≤ approval_board_limit")
	}
	if p.DefaultSecurityModel != nil {
		m := strings.ToLower(strings.TrimSpace(*p.DefaultSecurityModel))
		switch m {
		case "none", "guarantor_only", "collateral_only", "either", "both":
		default:
			return httpx.ErrBadRequest("default_security_model must be none, guarantor_only, collateral_only, either, or both")
		}
	}
	if p.DefaultMinGuarantorCoverPct != nil && (*p.DefaultMinGuarantorCoverPct < 0 || *p.DefaultMinGuarantorCoverPct > 1000) {
		return httpx.ErrBadRequest("default_min_guarantor_cover_pct must be between 0 and 1000")
	}
	if p.DefaultMinCollateralCoverPct != nil && (*p.DefaultMinCollateralCoverPct < 0 || *p.DefaultMinCollateralCoverPct > 1000) {
		return httpx.ErrBadRequest("default_min_collateral_cover_pct must be between 0 and 1000")
	}
	if p.CollateralRevaluationMonths != nil && (*p.CollateralRevaluationMonths < 1 || *p.CollateralRevaluationMonths > 120) {
		return httpx.ErrBadRequest("collateral_revaluation_months must be between 1 and 120")
	}
	return nil
}

// ─────────── PATCH /v1/tenant/membership ───────────

type membershipPatchDTO struct {
	CollectRegistrationFee       *bool      `json:"collect_registration_fee"`
	RegistrationFeeIndividual    *float64   `json:"registration_fee_individual"`
	RegistrationFeeInstitutional *float64   `json:"registration_fee_institutional"`
	AcceptedPaymentChannels      *[]string  `json:"accepted_payment_channels"`
	FeeRefundableOnRejection     *bool      `json:"fee_refundable_on_rejection"`
	DefaultDepositProductID      *uuid.UUID `json:"default_deposit_product_id"`
}

// validChannel enumerates the payment channels accepted as proof of
// registration-fee payment. The set mirrors the deposit-channel enum
// so the onboarding workflow + accounting auto-poster can resolve a
// CoA cash code from the channel without a translation table.
var validRegistrationChannels = map[string]bool{
	"mpesa": true, "airtel_money": true, "bank_transfer": true,
	"cash": true, "cheque": true,
}

func (h *SettingsHandler) UpdateMembership(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var req membershipPatchDTO
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.RegistrationFeeIndividual != nil && *req.RegistrationFeeIndividual < 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("registration_fee_individual must be ≥ 0"))
		return
	}
	if req.RegistrationFeeInstitutional != nil && *req.RegistrationFeeInstitutional < 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("registration_fee_institutional must be ≥ 0"))
		return
	}
	if req.AcceptedPaymentChannels != nil {
		for _, c := range *req.AcceptedPaymentChannels {
			if !validRegistrationChannels[c] {
				httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid payment channel: "+c+" (allowed: mpesa, airtel_money, bank_transfer, cash, cheque)"))
				return
			}
		}
	}

	var updated *domain.TenantMembership
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		patch := store.MembershipPatch{
			CollectRegistrationFee:       req.CollectRegistrationFee,
			RegistrationFeeIndividual:    req.RegistrationFeeIndividual,
			RegistrationFeeInstitutional: req.RegistrationFeeInstitutional,
			AcceptedPaymentChannels:      req.AcceptedPaymentChannels,
			FeeRefundableOnRejection:     req.FeeRefundableOnRejection,
			DefaultDepositProductID:      req.DefaultDepositProductID,
		}
		if err := h.Settings.UpdateMembershipTx(r.Context(), tx, tenant.ID, patch); err != nil {
			return err
		}
		var err error
		updated, err = h.Settings.GetOrInitMembershipTx(r.Context(), tx, tenant.ID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenant.ID, "tenant.membership_updated", nil)
	httpx.OK(w, updated)
}

// ─────────── helpers ───────────

func (h *SettingsHandler) audit(r *http.Request, tenantID uuid.UUID, action string, meta map[string]any) {
	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenantID, ActorID: nonZero(actorID),
		Action: action, TargetKind: "tenant", TargetID: tenantID.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: meta,
	})
}
