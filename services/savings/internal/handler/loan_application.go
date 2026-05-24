// Loan application HTTP handlers (Phase 6b).
//
//   POST  /v1/loan-applications                         create + validate + score
//   GET   /v1/loan-applications                         list with filters
//   GET   /v1/loan-applications/{id}                    detail + guarantees + collateral
//   POST  /v1/loan-applications/{id}/score              re-run scoring
//   POST  /v1/loan-applications/{id}/approve            manual approval
//   POST  /v1/loan-applications/{id}/decline            manual decline
//   POST  /v1/loan-guarantees/{id}/respond              guarantor consent (accept/decline)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type LoanApplicationHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	LoanProducts   *store.LoanProductStore
	Applications   *store.LoanApplicationStore
	Guarantees     *store.LoanGuaranteeStore
	Notifier       *notifier.Client
	Logger         *slog.Logger

	// Unified Inbox integration (PR #4). When set, the
	// submit-for-decision endpoint forwards into this workflow
	// service; the resolve endpoint authenticates against this
	// token. Empty in dev — same fallback pattern as the savings
	// pending-approvals resolve callback (PR #3).
	WorkflowURL           string
	WorkflowInternalToken string
	SavingsSelfURL        string
	HTTP                  *http.Client
}

// ─────────── Create ───────────

type guarantorIn struct {
	CounterpartyID         uuid.UUID       `json:"counterparty_id"`
	AmountGuaranteed decimal.Decimal `json:"amount_guaranteed"`
}

type collateralIn struct {
	Kind            domain.LoanCollateralKind `json:"kind"`
	Description     string                    `json:"description"`
	EstimatedValue  decimal.Decimal           `json:"estimated_value"`
	ForcedSaleValue *decimal.Decimal          `json:"forced_sale_value,omitempty"`
	ValuationDate   *string                   `json:"valuation_date,omitempty"`  // YYYY-MM-DD
	Notes           *string                   `json:"notes,omitempty"`
}

type createAppReq struct {
	CounterpartyID                     uuid.UUID                  `json:"counterparty_id"`
	ProductID                    uuid.UUID                  `json:"product_id"`
	RequestedAmount              decimal.Decimal            `json:"requested_amount"`
	RequestedTermMonths          int                        `json:"requested_term_months"`
	PurposeCategoryID            *uuid.UUID                 `json:"purpose_category_id,omitempty"`
	PurposeNote                  *string                    `json:"purpose_note,omitempty"`
	PreferredDisbursementChannel *string                    `json:"preferred_disbursement_channel,omitempty"`

	EmploymentType             *domain.LoanEmploymentType `json:"employment_type,omitempty"`
	EmployerName               *string                    `json:"employer_name,omitempty"`
	EmployerPayrollContact     *string                    `json:"employer_payroll_contact,omitempty"`
	MonthlyNetIncome           decimal.Decimal            `json:"monthly_net_income"`
	OtherIncome                decimal.Decimal            `json:"other_income"`
	MonthlyExpenses            decimal.Decimal            `json:"monthly_expenses"`
	MonthlyExistingObligations decimal.Decimal            `json:"monthly_existing_obligations"`

	Guarantors []guarantorIn  `json:"guarantors"`
	Collateral []collateralIn `json:"collateral"`
	Notes      *string        `json:"notes,omitempty"`
}

type createAppResp struct {
	Application domain.LoanApplication       `json:"application"`
	Guarantees  []domain.LoanGuarantee       `json:"guarantees"`
	Collateral  []domain.LoanCollateralItem  `json:"collateral"`
	Score       domain.ScoreResult           `json:"score"`
	Schedule    *store.ScheduleSnapshot      `json:"schedule,omitempty"`
}

func (h *LoanApplicationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createAppReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CounterpartyID == uuid.Nil || in.ProductID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id and product_id are required"))
		return
	}
	if in.RequestedAmount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_amount must be > 0"))
		return
	}
	if in.RequestedTermMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_term_months must be > 0"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var resp createAppResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, err := h.LoanProducts.GetTx(r.Context(), tx, in.ProductID)
		if err != nil {
			return err
		}
		if !product.IsActive {
			return domain.ErrLoanProductInactive
		}
		// Product bounds.
		if in.RequestedAmount.LessThan(product.MinAmount) || in.RequestedAmount.GreaterThan(product.MaxAmount) {
			return domain.ErrLoanAmountOutsideRange
		}
		if in.RequestedTermMonths < product.MinTermMonths || in.RequestedTermMonths > product.MaxTermMonths {
			return domain.ErrLoanTermOutsideRange
		}
		if int(len(in.Guarantors)) < product.MinGuarantors {
			return domain.ErrInsufficientGuarantors
		}
		if product.CollateralRequirement == domain.CollateralRequired && len(in.Collateral) == 0 {
			return domain.ErrCollateralMissing
		}

		// Insert application
		app := &domain.LoanApplication{
			CounterpartyID:                   in.CounterpartyID,
			ProductID:                  in.ProductID,
			Status:                     domain.AppPendingScoring,
			RequestedAmount:            in.RequestedAmount,
			RequestedTermMonths:        in.RequestedTermMonths,
			PurposeCategoryID:          in.PurposeCategoryID,
			PurposeNote:                in.PurposeNote,
			PreferredDisbursementChannel: in.PreferredDisbursementChannel,
			EmploymentType:             in.EmploymentType,
			EmployerName:               in.EmployerName,
			EmployerPayrollContact:     in.EmployerPayrollContact,
			MonthlyNetIncome:           in.MonthlyNetIncome,
			OtherIncome:                in.OtherIncome,
			MonthlyExpenses:            in.MonthlyExpenses,
			MonthlyExistingObligations: in.MonthlyExistingObligations,
			Notes:                      in.Notes,
			CreatedBy:                  userID,
		}
		created, err := h.Applications.CreateTx(r.Context(), tx, app)
		if err != nil {
			return err
		}

		// Insert guarantees + collateral
		for _, g := range in.Guarantors {
			if g.CounterpartyID == uuid.Nil || g.AmountGuaranteed.LessThanOrEqual(decimal.Zero) {
				return httpx.ErrBadRequest("each guarantor needs counterparty_id and a positive amount_guaranteed")
			}
			gp, err := h.Guarantees.CreateTx(r.Context(), tx, &domain.LoanGuarantee{
				ApplicationID:     created.ID,
				GuarantorMemberID: g.CounterpartyID,
				AmountGuaranteed:  g.AmountGuaranteed,
				RequestedBy:       userID,
			})
			if err != nil {
				return err
			}
			resp.Guarantees = append(resp.Guarantees, *gp)
		}
		for _, c := range in.Collateral {
			var vd *time.Time
			if c.ValuationDate != nil && *c.ValuationDate != "" {
				d, err := time.Parse("2006-01-02", *c.ValuationDate)
				if err != nil {
					return httpx.ErrBadRequest("valuation_date must be YYYY-MM-DD")
				}
				vd = &d
			}
			cp, err := h.Guarantees.CreateCollateralTx(r.Context(), tx, &domain.LoanCollateralItem{
				ApplicationID:    created.ID,
				Kind:             c.Kind,
				Description:      c.Description,
				EstimatedValue:   c.EstimatedValue,
				ForcedSaleValue:  c.ForcedSaleValue,
				ValuationDate:    vd,
				Notes:            c.Notes,
			})
			if err != nil {
				return err
			}
			resp.Collateral = append(resp.Collateral, *cp)
		}

		// Score immediately (Phase 6b is synchronous, internal data only).
		scored, err := h.runScoringTx(r.Context(), tx, created, product)
		if err != nil {
			return err
		}
		// Project the repayment schedule using product config + requested
		// amount/term so the reviewer (and applicant) see what they will pay.
		snap := store.ComputeScheduleSnapshot(
			created.RequestedAmount, product.InterestRatePct,
			created.RequestedTermMonths, product.GracePeriodMonths,
			product.InterestMethod, product.RepaymentMethod,
			time.Now().UTC(),
			product,
		)
		if err := h.Applications.StoreAppScheduleSnapshotTx(r.Context(), tx, created.ID, snap); err != nil {
			return err
		}
		// Reload to include the snapshot fields in the response.
		fresh, err := h.Applications.GetTx(r.Context(), tx, created.ID)
		if err == nil && fresh != nil {
			scored.application = fresh
		}
		resp.Application = *scored.application
		resp.Score = scored.score
		resp.Schedule = snap
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if resp.Guarantees == nil {
		resp.Guarantees = []domain.LoanGuarantee{}
	}
	if resp.Collateral == nil {
		resp.Collateral = []domain.LoanCollateralItem{}
	}
	// Fire LOAN_APPLICATION_RECEIVED for the applicant, and
	// GUARANTOR_REQUEST_SENT once per guarantor that was attached.
	if h.Notifier != nil {
		var applicant *store.CounterpartyView
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			var lerr error
			applicant, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, resp.Application.CounterpartyID)
			return lerr
		})
		if applicant != nil {
			sourceModule := "savings.loans"
			recordID := resp.Application.ID
			deepLink := "/loans/applications/" + resp.Application.ID.String()
			mid := applicant.ID
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          tid,
				EventCode:         "LOAN_APPLICATION_RECEIVED",
				RecipientMemberID: &mid,
				RecipientName:     applicant.FullName,
				RecipientPhone:    strNilIfEmpty(applicant.Phone),
				RecipientEmail:    strNilIfEmpty(applicant.Email),
				SourceModule:      &sourceModule,
				SourceRecordID:    &recordID,
				DeepLink:          &deepLink,
				InitiatedBy:       nonZeroUUID(userID),
				Payload: map[string]any{
					"member_no":         applicant.MemberNo,
					"full_name":         applicant.FullName,
					"application_no":    resp.Application.ApplicationNo,
					"requested_amount":  resp.Application.RequestedAmount.String(),
					"requested_term":    resp.Application.RequestedTermMonths,
				},
			})
		}
		// One notification per guarantor invited.
		for _, g := range resp.Guarantees {
			var guarantor *store.CounterpartyView
			_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
				var lerr error
				guarantor, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, g.GuarantorMemberID)
				return lerr
			})
			if guarantor == nil {
				continue
			}
			sourceModule := "savings.loans"
			recordID := g.ID
			deepLink := "/loans/applications/" + resp.Application.ID.String()
			gid := guarantor.ID
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          tid,
				EventCode:         "GUARANTOR_REQUEST_SENT",
				RecipientMemberID: &gid,
				RecipientName:     guarantor.FullName,
				RecipientPhone:    strNilIfEmpty(guarantor.Phone),
				RecipientEmail:    strNilIfEmpty(guarantor.Email),
				SourceModule:      &sourceModule,
				SourceRecordID:    &recordID,
				DeepLink:          &deepLink,
				InitiatedBy:       nonZeroUUID(userID),
				Payload: map[string]any{
					"member_no":          guarantor.MemberNo,
					"full_name":          guarantor.FullName,
					"application_no":     resp.Application.ApplicationNo,
					"amount_guaranteed":  g.AmountGuaranteed.String(),
				},
			})
		}
	}
	httpx.Created(w, resp)
}

// ─────────── Re-score ───────────

func (h *LoanApplicationHandler) ReScore(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp createAppResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		product, err := h.LoanProducts.GetTx(r.Context(), tx, app.ProductID)
		if err != nil {
			return err
		}
		scored, err := h.runScoringTx(r.Context(), tx, app, product)
		if err != nil {
			return err
		}
		guars, err := h.Guarantees.ByApplicationTx(r.Context(), tx, app.ID)
		if err != nil {
			return err
		}
		coll, err := h.Guarantees.CollateralByApplicationTx(r.Context(), tx, app.ID)
		if err != nil {
			return err
		}
		resp.Application = *scored.application
		resp.Score = scored.score
		resp.Guarantees = guars
		resp.Collateral = coll
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if resp.Guarantees == nil {
		resp.Guarantees = []domain.LoanGuarantee{}
	}
	if resp.Collateral == nil {
		resp.Collateral = []domain.LoanCollateralItem{}
	}
	httpx.OK(w, resp)
}

// runScoringTx is the shared scoring path used by Create + ReScore.
// Returns the persisted (updated) application + the score result.
type scoreOutcome struct {
	application *domain.LoanApplication
	score       domain.ScoreResult
}

func (h *LoanApplicationHandler) runScoringTx(ctx context.Context, tx pgx.Tx, app *domain.LoanApplication, product *domain.LoanProduct) (*scoreOutcome, error) {
	// Pull tenant affordability thresholds.
	var dtiThresh, maxInstallPct decimal.Decimal
	if err := tx.QueryRow(ctx, `
		SELECT affordability_dti_threshold_pct, affordability_max_installment_pct_of_disposable
		FROM tenant_operations
	`).Scan(&dtiThresh, &maxInstallPct); err != nil {
		return nil, err
	}
	// Inputs.
	in, err := h.Applications.GatherScoringInputsTx(ctx, tx, app.CounterpartyID, app.ProductID)
	if err != nil {
		return nil, err
	}
	// Pure scorer.
	req := domain.ApplicationRequest{
		RequestedAmount:            app.RequestedAmount,
		RequestedTermMonths:        app.RequestedTermMonths,
		MonthlyNetIncome:           app.MonthlyNetIncome,
		OtherIncome:                app.OtherIncome,
		MonthlyExpenses:            app.MonthlyExpenses,
		MonthlyExistingObligations: app.MonthlyExistingObligations,
		EmploymentType:             app.EmploymentType,
	}
	result := domain.Score(*in, product, req, dtiThresh, maxInstallPct)

	// JSON serialise factors + flags.
	detailsJSON, _ := json.Marshal(result.Factors)
	flagsJSON, _ := json.Marshal(result.Flags)

	// Next status:
	//   hard block          → declined
	//   auto-approve match  → approved
	//   otherwise           → pending_approval
	next := domain.AppPendingApproval
	if result.HasHardBlock {
		next = domain.AppDeclined
	} else if product.AutoApprovalThreshold != nil &&
		app.RequestedAmount.LessThanOrEqual(*product.AutoApprovalThreshold) &&
		product.AutoApprovalMinScore != nil &&
		result.OverallScore >= *product.AutoApprovalMinScore &&
		result.AffordabilityPass {
		next = domain.AppApproved
	}

	updated, err := h.Applications.SaveScoringTx(ctx, tx, app.ID, &result, detailsJSON, flagsJSON, next)
	if err != nil {
		return nil, err
	}
	// If auto-approved, also stamp approved_amount / term / rate from the request.
	if next == domain.AppApproved {
		updated, err = h.Applications.UpdateStatusTx(ctx, tx, app.ID, store.AppTransition{
			To: domain.AppApproved, By: app.CreatedBy,
			ApprovedAmount: &app.RequestedAmount, ApprovedTermMonths: &app.RequestedTermMonths,
			ApprovedInterestPct: &product.InterestRatePct,
		})
		if err != nil {
			return nil, err
		}
	}
	// If declined for hard blocks, stamp the decline category.
	if next == domain.AppDeclined {
		cat := "scoring_hard_block"
		// Best-effort: first hard-block flag's message
		reason := "Application blocked by automated scoring."
		for _, f := range result.Flags {
			if f.Severity == "hard_block" {
				reason = f.Message
				break
			}
		}
		updated, err = h.Applications.UpdateStatusTx(ctx, tx, app.ID, store.AppTransition{
			To: domain.AppDeclined, By: app.CreatedBy,
			DeclineCategory: &cat, DeclineReason: &reason,
		})
		if err != nil {
			return nil, err
		}
	}
	return &scoreOutcome{application: updated, score: result}, nil
}

// ─────────── Reads ───────────

func (h *LoanApplicationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	type detail struct {
		Application domain.LoanApplication      `json:"application"`
		Guarantees  []domain.LoanGuarantee      `json:"guarantees"`
		Collateral  []domain.LoanCollateralItem `json:"collateral"`
		Schedule    *store.ScheduleSnapshot     `json:"schedule,omitempty"`
	}
	var out detail
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		guars, err := h.Guarantees.ByApplicationTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		coll, err := h.Guarantees.CollateralByApplicationTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		snap, err := h.Applications.GetAppScheduleSnapshotTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = detail{Application: *app, Guarantees: guars, Collateral: coll, Schedule: snap}
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if out.Guarantees == nil {
		out.Guarantees = []domain.LoanGuarantee{}
	}
	if out.Collateral == nil {
		out.Collateral = []domain.LoanCollateralItem{}
	}
	httpx.OK(w, out)
}

func (h *LoanApplicationHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.AppListFilter{Status: q.Get("status"), Q: q.Get("q"), Limit: limit, Offset: offset}
	if v := q.Get("counterparty_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.CounterpartyID = &id
		}
	}
	if v := q.Get("product_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.ProductID = &id
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.AppListItem
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Applications.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.AppListItem{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// ─────────── Approve / decline ───────────

type loanApproveReq struct {
	Amount      *decimal.Decimal `json:"approved_amount,omitempty"`
	TermMonths  *int             `json:"approved_term_months,omitempty"`
	InterestPct *decimal.Decimal `json:"approved_interest_rate_pct,omitempty"`
	Conditions  *string          `json:"approval_conditions,omitempty"`
}

func (h *LoanApplicationHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in loanApproveReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if app.Status != domain.AppPendingApproval && app.Status != domain.AppReturnedForInfo {
			return domain.ErrAppNotApprovable
		}
		// Default to as-applied if not overridden.
		amt := in.Amount
		if amt == nil {
			amt = &app.RequestedAmount
		}
		term := in.TermMonths
		if term == nil {
			term = &app.RequestedTermMonths
		}
		ratePct := in.InterestPct
		if ratePct == nil {
			product, err := h.LoanProducts.GetTx(r.Context(), tx, app.ProductID)
			if err != nil {
				return err
			}
			r := product.InterestRatePct
			ratePct = &r
		}
		target := domain.AppApproved
		if in.Conditions != nil && *in.Conditions != "" {
			target = domain.AppApprovedWithConditions
		}
		out, err = h.Applications.UpdateStatusTx(r.Context(), tx, id, store.AppTransition{
			To: target, By: userID,
			ApprovedAmount: amt, ApprovedTermMonths: term, ApprovedInterestPct: ratePct,
			ApprovalConditions: in.Conditions,
		})
		if err != nil {
			return err
		}
		// Re-project the schedule against approved amount/term/rate so the
		// applicant sees what they're being offered.
		product, perr := h.LoanProducts.GetTx(r.Context(), tx, app.ProductID)
		if perr != nil {
			return perr
		}
		snap := store.ComputeScheduleSnapshot(
			*amt, *ratePct, *term, product.GracePeriodMonths,
			product.InterestMethod, product.RepaymentMethod,
			time.Now().UTC(),
			product,
		)
		if err := h.Applications.StoreAppScheduleSnapshotTx(r.Context(), tx, id, snap); err != nil {
			return err
		}
		out, err = h.Applications.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	// Fire LOAN_APPROVED through the central notifier. Non-blocking on
	// the caller's perspective — the approval already committed above.
	if h.Notifier != nil && out != nil {
		approvedAmt := "0"
		if out.ApprovedAmount != nil {
			approvedAmt = out.ApprovedAmount.String()
		}
		termMonths := 0
		if out.ApprovedTermMonths != nil {
			termMonths = *out.ApprovedTermMonths
		}
		rate := "0"
		if out.ApprovedInterestRatePct != nil {
			rate = out.ApprovedInterestRatePct.String()
		}
		deepLink := "/loans/applications/" + out.ID.String()
		sourceModule := "savings.loans"
		recordID := out.ID
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:        tid,
			EventCode:       "LOAN_APPROVED",
			RecipientUserID: &userID,
			RecipientName:   "Loan officer",
			SourceModule:    &sourceModule,
			SourceRecordID:  &recordID,
			DeepLink:        &deepLink,
			InitiatedBy:     &userID,
			Payload: map[string]any{
				"application_no":  out.ApplicationNo,
				"approved_amount": approvedAmt,
				"term_months":     termMonths,
				"interest_rate":   rate,
			},
		})
	}
	httpx.OK(w, out)
}

type loanDeclineReq struct {
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

func (h *LoanApplicationHandler) Decline(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in loanDeclineReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required to decline"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		switch app.Status {
		case domain.AppPendingApproval, domain.AppReturnedForInfo, domain.AppPendingScoring:
			// declinable
		default:
			return domain.ErrAppNotApprovable
		}
		out, err = h.Applications.UpdateStatusTx(r.Context(), tx, id, store.AppTransition{
			To: domain.AppDeclined, By: userID,
			DeclineCategory: &in.Category, DeclineReason: &in.Reason,
		})
		return err
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	// Notify the applicant that their loan was declined.
	if h.Notifier != nil && out != nil {
		var member *store.CounterpartyView
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			var lerr error
			member, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, out.CounterpartyID)
			return lerr
		})
		if member != nil {
			sourceModule := "savings.loans"
			recordID := out.ID
			deepLink := "/loans/applications/" + out.ID.String()
			mid := member.ID
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          tid,
				EventCode:         "LOAN_DECLINED",
				RecipientMemberID: &mid,
				RecipientName:     member.FullName,
				RecipientPhone:    strNilIfEmpty(member.Phone),
				RecipientEmail:    strNilIfEmpty(member.Email),
				SourceModule:      &sourceModule,
				SourceRecordID:    &recordID,
				DeepLink:          &deepLink,
				InitiatedBy:       nonZeroUUID(userID),
				Payload: map[string]any{
					"member_no":        member.MemberNo,
					"full_name":        member.FullName,
					"application_no":   out.ApplicationNo,
					"decline_reason":   in.Reason,
					"decline_category": in.Category,
				},
			})
		}
	}
	httpx.OK(w, out)
}

// ─────────── Guarantor consent ───────────

type guaranteeRespondReq struct {
	Accept        bool    `json:"accept"`
	DeclineReason *string `json:"decline_reason,omitempty"`
}

// ListByGuarantor — Member Profile → People tab. Returns every
// loan-guarantee this member is on, with the borrower's name + loan
// reference resolved. The frontend renders this as the
// "Guarantorships" card; empty array → "no guarantorships on record"
// empty state.
func (h *LoanApplicationHandler) ListByGuarantor(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.GuarantorshipRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var lerr error
		rows, lerr = h.Guarantees.ByGuarantorMemberTx(r.Context(), tx, memberID)
		return lerr
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []store.GuarantorshipRow{}
	}
	httpx.OK(w, rows)
}

func (h *LoanApplicationHandler) GuaranteeRespond(w http.ResponseWriter, r *http.Request) {
	gID, err := parseUUIDParam(r, "guarantee_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in guaranteeRespondReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.LoanGuarantee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Guarantees.RespondTx(r.Context(), tx, gID, in.Accept, in.DeclineReason)
		return err
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	// Notify the application creator that the guarantor responded.
	// We don't have the loan officer's user ID cheaply here; fire a
	// member-targeted event using the applicant's member ID so the
	// applicant + their officer's inbox views both pick it up.
	if h.Notifier != nil && out != nil {
		var app *domain.LoanApplication
		var guarantor *store.CounterpartyView
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			var lerr error
			app, lerr = h.Applications.GetTx(r.Context(), tx, out.ApplicationID)
			if lerr != nil {
				return lerr
			}
			guarantor, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, out.GuarantorMemberID)
			return lerr
		})
		if app != nil && guarantor != nil {
			eventCode := "GUARANTOR_REQUEST_ACCEPTED"
			if !in.Accept {
				eventCode = "GUARANTOR_REQUEST_DECLINED"
			}
			var applicant *store.CounterpartyView
			_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
				var lerr error
				applicant, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, app.CounterpartyID)
				return lerr
			})
			if applicant != nil {
				sourceModule := "savings.loans"
				recordID := out.ID
				deepLink := "/loans/applications/" + app.ID.String()
				mid := applicant.ID
				payload := map[string]any{
					"member_no":        applicant.MemberNo,
					"full_name":        applicant.FullName,
					"application_no":   app.ApplicationNo,
					"guarantor_name":   guarantor.FullName,
					"guarantor_member": guarantor.MemberNo,
				}
				if !in.Accept && in.DeclineReason != nil {
					payload["decline_reason"] = *in.DeclineReason
				}
				h.Notifier.Notify(r.Context(), notifier.Request{
					TenantID:          tid,
					EventCode:         eventCode,
					RecipientMemberID: &mid,
					RecipientName:     applicant.FullName,
					RecipientPhone:    strNilIfEmpty(applicant.Phone),
					RecipientEmail:    strNilIfEmpty(applicant.Email),
					SourceModule:      &sourceModule,
					SourceRecordID:    &recordID,
					DeepLink:          &deepLink,
					Payload:           payload,
				})
			}
		}
	}
	httpx.OK(w, out)
}

// ─────────── Error mapping ───────────

func writeLoanAppErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrLoanProductInactive),
		errors.Is(err, domain.ErrLoanAmountOutsideRange),
		errors.Is(err, domain.ErrLoanTermOutsideRange),
		errors.Is(err, domain.ErrInsufficientGuarantors),
		errors.Is(err, domain.ErrGuarantorsNotConsented),
		errors.Is(err, domain.ErrCollateralMissing),
		errors.Is(err, domain.ErrMemberIneligibleForLoan),
		errors.Is(err, domain.ErrInsufficientSharesForLoan),
		errors.Is(err, domain.ErrMembershipTooShort),
		errors.Is(err, domain.ErrConcurrentLoanForbidden),
		errors.Is(err, domain.ErrMultiplierExceeded),
		errors.Is(err, domain.ErrAppNotApprovable),
		errors.Is(err, domain.ErrAppNotOfferable),
		errors.Is(err, domain.ErrAppNotAcceptable),
		errors.Is(err, domain.ErrAppNotDisbursable):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
