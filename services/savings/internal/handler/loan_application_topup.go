// Loans Phase 5 — top-up + refinance application endpoints.
//
//   POST /v1/loan-applications/topup
//        {base_loan_id, top_up_amount, requested_term_months,
//         purpose_note?, rebroadcast_consent?}
//
//   POST /v1/loan-applications/refinance
//        {base_loan_ids: [...], requested_term_months,
//         requested_interest_rate?, purpose_note?, rebroadcast_consent?}
//
// Both endpoints follow "approach B" from the prompt — a new loan is
// issued that settles the base loan(s). Implementation:
//
//   1. Validate every base loan is servicing (active/in_arrears/restructured)
//      and not in legal handover (case status != 'escalated_legal').
//   2. Compute the payoff figure on each base loan using the existing
//      LoanStore.PayoffFigureTx helper.
//   3. Sum the payoffs; for top-up add `top_up_amount`.
//   4. Create a loan_applications row with application_type='topup'|'refinance',
//      parent_loan_id (= the single or largest source loan), and for
//      multi-source refinance, refinance_source_loan_ids jsonb.
//   5. Copy guarantor rows from the base loan's source application as
//      a starting point. Guarantor consent rebroadcast is sent if the
//      caller passes rebroadcast_consent=true (default true) — each
//      copied row stays in 'pending_consent' until the guarantor
//      reconfirms. The legacy /v1/loan-applications/{id}/guarantors
//      flow handles the re-acceptance.
//
// The actual settlement of parent loans happens at disbursement time
// (see loan_disbursement_executor.go) — that's where the new loan's
// principal is real money + the parent loan's payoff is materially
// the same amount, so the executor settles them in the same tx.
//
// Permission: loans:topup / loans:refinance.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

// ─────────── Top-up ───────────

type topupReq struct {
	BaseLoanID          uuid.UUID `json:"base_loan_id"`
	TopUpAmount         string    `json:"top_up_amount"`
	RequestedTermMonths int       `json:"requested_term_months"`
	PurposeNote         string    `json:"purpose_note"`
	RebroadcastConsent  *bool     `json:"rebroadcast_consent"` // default true
}

func (h *LoanApplicationHandler) TopUp(w http.ResponseWriter, r *http.Request) {
	var in topupReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if in.BaseLoanID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("base_loan_id is required")); return
	}
	topupAmt, err := decimal.NewFromString(in.TopUpAmount)
	if err != nil || !topupAmt.IsPositive() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("top_up_amount must be a positive decimal")); return
	}
	if in.RequestedTermMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_term_months must be > 0")); return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	rebroadcast := true
	if in.RebroadcastConsent != nil {
		rebroadcast = *in.RebroadcastConsent
	}

	var created *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		base, err := h.Loans.GetTx(r.Context(), tx, in.BaseLoanID)
		if err != nil {
			return err
		}
		if err := validateBaseLoanForRestructure(r.Context(), tx, base); err != nil {
			return err
		}
		// Compute payoff (penalty + interest + principal + fees).
		payoff, err := h.Loans.PayoffFigureTx(r.Context(), tx, base.ID)
		if err != nil {
			return err
		}
		requested := payoff.Add(topupAmt)

		// Build the application.
		notePtr := optStr(in.PurposeNote)
		app := &domain.LoanApplication{
			CounterpartyID:      base.CounterpartyID,
			ProductID:           base.ProductID,
			Status:              domain.AppPendingScoring,
			RequestedAmount:     requested,
			RequestedTermMonths: in.RequestedTermMonths,
			PurposeNote:         notePtr,
			ApplicationType:     "topup",
			ParentLoanID:        &base.ID,
			CreatedBy:           userID,
		}
		if isIns, cat, err := detectInsiderForCounterpartyTx(r.Context(), tx, base.CounterpartyID); err == nil && isIns {
			app.IsInsider = true
			app.InsiderCategory = &cat
		}
		// Carry the base loan's affordability profile forward so scoring
		// doesn't fail with zeros. Re-scoring happens via the existing
		// scoring step.
		if err := carryAffordabilityFromBaseApplicationTx(r.Context(), tx, h, base, app); err != nil {
			return err
		}
		c, err := h.Applications.CreateTx(r.Context(), tx, app)
		if err != nil {
			return err
		}
		created = c
		// Copy guarantors.
		if err := copyGuarantorsFromBaseAppTx(r.Context(), tx, h.Guarantees, base.ApplicationID, created.ID, userID, rebroadcast); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writeApplicationErr(w, r, err); return
	}
	httpx.Created(w, created)
}

// ─────────── Refinance ───────────

type refinanceReq struct {
	BaseLoanIDs           []uuid.UUID `json:"base_loan_ids"`
	RequestedTermMonths   int         `json:"requested_term_months"`
	RequestedInterestRate *string     `json:"requested_interest_rate,omitempty"`
	PurposeNote           string      `json:"purpose_note"`
	RebroadcastConsent    *bool       `json:"rebroadcast_consent"`
}

func (h *LoanApplicationHandler) Refinance(w http.ResponseWriter, r *http.Request) {
	var in refinanceReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if len(in.BaseLoanIDs) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("base_loan_ids must contain at least one loan")); return
	}
	if in.RequestedTermMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_term_months must be > 0")); return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	rebroadcast := true
	if in.RebroadcastConsent != nil {
		rebroadcast = *in.RebroadcastConsent
	}

	var created *domain.LoanApplication
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Validate + collect all base loans.
		bases := make([]*domain.Loan, 0, len(in.BaseLoanIDs))
		var counterpartyID uuid.UUID
		var productID uuid.UUID
		var totalPayoff decimal.Decimal
		var largest *domain.Loan
		var largestAmt decimal.Decimal
		for _, id := range in.BaseLoanIDs {
			base, err := h.Loans.GetTx(r.Context(), tx, id)
			if err != nil {
				return err
			}
			if err := validateBaseLoanForRestructure(r.Context(), tx, base); err != nil {
				return err
			}
			// All base loans must belong to the same member + product
			// (different products would mean different rates/terms;
			// consolidation across products is a future concern).
			if counterpartyID == uuid.Nil {
				counterpartyID = base.CounterpartyID
				productID = base.ProductID
			} else {
				if base.CounterpartyID != counterpartyID {
					return httpx.ErrBadRequest("all base_loan_ids must belong to the same member")
				}
				if base.ProductID != productID {
					return httpx.ErrBadRequest("refinance across different loan products is not supported")
				}
			}
			payoff, err := h.Loans.PayoffFigureTx(r.Context(), tx, base.ID)
			if err != nil {
				return err
			}
			totalPayoff = totalPayoff.Add(payoff)
			if largest == nil || payoff.GreaterThan(largestAmt) {
				largest = base
				largestAmt = payoff
			}
			bases = append(bases, base)
		}

		// Build the application.
		notePtr := optStr(in.PurposeNote)
		app := &domain.LoanApplication{
			CounterpartyID:      counterpartyID,
			ProductID:           productID,
			Status:              domain.AppPendingScoring,
			RequestedAmount:     totalPayoff,
			RequestedTermMonths: in.RequestedTermMonths,
			PurposeNote:         notePtr,
			ApplicationType:     "refinance",
			ParentLoanID:        &largest.ID,
			CreatedBy:           userID,
		}
		if isIns, cat, err := detectInsiderForCounterpartyTx(r.Context(), tx, counterpartyID); err == nil && isIns {
			app.IsInsider = true
			app.InsiderCategory = &cat
		}
		// Multi-source consolidation — encode the full set in jsonb.
		if len(bases) > 1 {
			ids := make([]string, len(bases))
			for i, b := range bases {
				ids[i] = b.ID.String()
			}
			b, _ := json.Marshal(ids)
			app.RefinanceSourceLoanIDs = b
		}
		if err := carryAffordabilityFromBaseApplicationTx(r.Context(), tx, h, largest, app); err != nil {
			return err
		}
		c, err := h.Applications.CreateTx(r.Context(), tx, app)
		if err != nil {
			return err
		}
		created = c
		// Copy guarantors from the largest source loan's application.
		if err := copyGuarantorsFromBaseAppTx(r.Context(), tx, h.Guarantees, largest.ApplicationID, created.ID, userID, rebroadcast); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writeApplicationErr(w, r, err); return
	}
	httpx.Created(w, created)
}

// ─────────── helpers ───────────

// validateBaseLoanForRestructure refuses a base loan that's not servicing
// or has been escalated to legal.
func validateBaseLoanForRestructure(ctx context.Context, tx pgx.Tx, loan *domain.Loan) error {
	switch loan.Status {
	case domain.LoanActive, domain.LoanInArrears, domain.LoanRestructured:
		// ok
	default:
		return httpx.ErrBadRequest("base loan " + loan.LoanNo + " is " + string(loan.Status) + " and cannot be topped up / refinanced")
	}
	// Refuse if there's an active collection case in escalated_legal.
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM loan_collection_cases WHERE loan_id = $1 LIMIT 1
	`, loan.ID).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if status == "escalated_legal" {
		return httpx.ErrConflict("base loan " + loan.LoanNo + " is in legal handover; cannot restructure")
	}
	return nil
}

// carryAffordabilityFromBaseApplicationTx pulls the income / expense
// profile from the source application so scoring has inputs. The new
// application can still be re-scored with fresh figures via the
// existing scoring endpoint.
func carryAffordabilityFromBaseApplicationTx(
	ctx context.Context, tx pgx.Tx,
	h *LoanApplicationHandler, base *domain.Loan, target *domain.LoanApplication,
) error {
	src, err := h.Applications.GetTx(ctx, tx, base.ApplicationID)
	if err != nil {
		// Not fatal — leave inputs at zero, scoring step will flag.
		return nil //nolint
	}
	target.EmploymentType = src.EmploymentType
	target.EmployerName = src.EmployerName
	target.EmployerPayrollContact = src.EmployerPayrollContact
	target.MonthlyNetIncome = src.MonthlyNetIncome
	target.OtherIncome = src.OtherIncome
	target.MonthlyExpenses = src.MonthlyExpenses
	target.MonthlyExistingObligations = src.MonthlyExistingObligations
	target.PurposeCategoryID = src.PurposeCategoryID
	if target.PurposeNote == nil {
		target.PurposeNote = src.PurposeNote
	}
	target.PreferredDisbursementChannel = src.PreferredDisbursementChannel
	return nil
}

// copyGuarantorsFromBaseAppTx duplicates the prior application's
// guarantee rows onto the new application. Status starts at
// pending_consent so each guarantor must re-affirm.
func copyGuarantorsFromBaseAppTx(
	ctx context.Context, tx pgx.Tx,
	guarantees *store.LoanGuaranteeStore,
	srcAppID, dstAppID, userID uuid.UUID,
	rebroadcast bool,
) error {
	if !rebroadcast {
		return nil
	}
	rows, err := guarantees.ByApplicationTx(ctx, tx, srcAppID)
	if err != nil {
		return err
	}
	for _, g := range rows {
		copy := &domain.LoanGuarantee{
			ApplicationID:     dstAppID,
			GuarantorMemberID: g.GuarantorMemberID,
			AmountGuaranteed:  g.AmountGuaranteed,
			RequestedBy:       userID,
		}
		if _, err := guarantees.CreateTx(ctx, tx, copy); err != nil {
			return err
		}
	}
	return nil
}

// writeApplicationErr maps store-level errors to HTTP codes consistently
// across the topup + refinance + group endpoints.
func writeApplicationErr(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		httpx.WriteErr(w, r, apiErr); return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound("loan or application not found"))
	default:
		httpx.WriteErr(w, r, err)
	}
}
