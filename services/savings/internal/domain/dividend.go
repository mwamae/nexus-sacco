// Dividend engine domain types + state machine + per-line calculator.
//
// Reuses InterestPayoutMethod (PayoutCreditSavings/PayoutBuyShares/
// PayoutExternal). Status machine intentionally mirrors interest so
// the UI can render both with the same components.

package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Status ───────────

type DividendRunStatus string

const (
	DivDraft     DividendRunStatus = "draft"
	DivComputing DividendRunStatus = "computing"
	DivPreview   DividendRunStatus = "preview"
	DivApproved  DividendRunStatus = "approved"
	DivPosting   DividendRunStatus = "posting"
	DivPosted    DividendRunStatus = "posted"
	DivLocked    DividendRunStatus = "locked"
	DivCancelled DividendRunStatus = "cancelled"
)

func CanTransitionDividend(from, to DividendRunStatus) bool {
	switch from {
	case DivDraft:
		return to == DivPreview || to == DivComputing || to == DivCancelled
	case DivComputing:
		return to == DivPreview || to == DivDraft || to == DivCancelled
	case DivPreview:
		return to == DivPreview || to == DivApproved || to == DivDraft || to == DivCancelled
	case DivApproved:
		return to == DivPosting || to == DivPosted || to == DivCancelled
	case DivPosting:
		return to == DivPosted || to == DivApproved
	case DivPosted:
		return to == DivLocked
	}
	return false
}

// ─────────── Calc methods ───────────

type DividendCalcMethod string

const (
	CalcClosingBalance DividendCalcMethod = "closing_balance"
	CalcAverageMonthly DividendCalcMethod = "average_monthly"
	CalcProRated       DividendCalcMethod = "pro_rated"
)

func (m DividendCalcMethod) Valid() bool {
	switch m {
	case CalcClosingBalance, CalcAverageMonthly, CalcProRated:
		return true
	}
	return false
}

func (m DividendCalcMethod) Description() string {
	switch m {
	case CalcClosingBalance:
		return "Share balance at the close of the financial year"
	case CalcAverageMonthly:
		return "Average of the twelve month-end share balances over the FY"
	case CalcProRated:
		return "Closing balance pro-rated by days held during the FY (handles mid-year openings/exits)"
	}
	return ""
}

// ─────────── Entities ───────────

type DividendRun struct {
	ID                   uuid.UUID            `json:"id"`
	TenantID             uuid.UUID            `json:"tenant_id"`
	RunNo                string               `json:"run_no"`
	FinancialYearLabel   string               `json:"financial_year_label"`
	FYStart              time.Time            `json:"fy_start"`
	FYEnd                time.Time            `json:"fy_end"`
	Status               DividendRunStatus    `json:"status"`
	CalcMethod           DividendCalcMethod   `json:"calc_method"`
	AGMRatePct           decimal.Decimal      `json:"agm_rate_pct"`
	AGMResolutionRef     string               `json:"agm_resolution_ref"`
	AGMResolutionDate    time.Time            `json:"agm_resolution_date"`
	WHTRatePct           decimal.Decimal      `json:"wht_rate_pct"`
	MemberCount          *int                 `json:"member_count,omitempty"`
	TotalShareBasis      *decimal.Decimal     `json:"total_share_basis,omitempty"`
	TotalGrossDividend   *decimal.Decimal     `json:"total_gross_dividend,omitempty"`
	TotalWHT             *decimal.Decimal     `json:"total_wht,omitempty"`
	TotalNetDividend     *decimal.Decimal     `json:"total_net_dividend,omitempty"`
	Notes                *string              `json:"notes,omitempty"`
	CreatedAt            time.Time            `json:"created_at"`
	CreatedBy            uuid.UUID            `json:"created_by"`
	ComputedAt           *time.Time           `json:"computed_at,omitempty"`
	ComputedBy           *uuid.UUID           `json:"computed_by,omitempty"`
	SubmittedAt          *time.Time           `json:"submitted_at,omitempty"`
	SubmittedBy          *uuid.UUID           `json:"submitted_by,omitempty"`
	WorkflowInstanceID   *uuid.UUID           `json:"workflow_instance_id,omitempty"`
	ApprovedAt           *time.Time           `json:"approved_at,omitempty"`
	ApprovedBy           *uuid.UUID           `json:"approved_by,omitempty"`
	PostedAt             *time.Time           `json:"posted_at,omitempty"`
	PostedBy             *uuid.UUID           `json:"posted_by,omitempty"`
	LockedAt             *time.Time           `json:"locked_at,omitempty"`
	CancelledAt          *time.Time           `json:"cancelled_at,omitempty"`
	CancelledBy          *uuid.UUID           `json:"cancelled_by,omitempty"`
	CancellationReason   *string              `json:"cancellation_reason,omitempty"`
}

type DividendRunLine struct {
	ID                    uuid.UUID            `json:"id"`
	TenantID              uuid.UUID            `json:"tenant_id"`
	RunID                 uuid.UUID            `json:"run_id"`
	ShareAccountID        uuid.UUID            `json:"share_account_id"`
	CounterpartyID              uuid.UUID            `json:"counterparty_id"`
	CalcMethod            DividendCalcMethod   `json:"calc_method"`
	SharesBasis           decimal.Decimal      `json:"shares_basis"`
	ParValueAtRun         decimal.Decimal      `json:"par_value_at_run"`
	CapitalBasis          decimal.Decimal      `json:"capital_basis"`
	DaysHeldInFY          *int                 `json:"days_held_in_fy,omitempty"`
	DaysInFY              int                  `json:"days_in_fy"`
	RateAppliedPct        decimal.Decimal      `json:"rate_applied_pct"`
	WHTRatePct            decimal.Decimal      `json:"wht_rate_pct"`
	GrossDividend         decimal.Decimal      `json:"gross_dividend"`
	WHTAmount             decimal.Decimal      `json:"wht_amount"`
	NetDividend           decimal.Decimal      `json:"net_dividend"`
	PayoutMethod          InterestPayoutMethod `json:"payout_method"`
	PayoutTargetAccountID *uuid.UUID           `json:"payout_target_account_id,omitempty"`
	PayoutExternalChannel *string              `json:"payout_external_channel,omitempty"`
	PayoutExternalRef     *string              `json:"payout_external_ref,omitempty"`
	PostedAt              *time.Time           `json:"posted_at,omitempty"`
	PostedDepositTxnID    *uuid.UUID           `json:"posted_deposit_txn_id,omitempty"`
	PostedShareTxnID      *uuid.UUID           `json:"posted_share_txn_id,omitempty"`
	Notes                 *string              `json:"notes,omitempty"`
}

// ─────────── Errors ───────────

var (
	ErrDivAGMGateMissing   = errors.New("AGM resolution rate and reference are required before this transition")
	ErrDivInvalidStatusTxn = errors.New("invalid status transition")
	ErrDivFYInvalid        = errors.New("financial year start must precede end")
	ErrDivRunNotPostable   = errors.New("run is not in a postable state (must be 'approved')")
	ErrDivInvalidCalcMethod = errors.New("invalid calculation method")
)

// ─────────── Calculator (per-line) ───────────

// DivCalcInputs is the per-account raw input for a single line.
// shares_basis is the share count to which the dividend rate is
// applied. For closing_balance it's literally the closing share count;
// for average_monthly it's the average; for pro_rated it's the
// closing count multiplied by (days_held / days_in_fy).
//
// capital_basis = shares_basis × par_value_at_run. The rate applies to
// capital_basis (not shares_basis) so a 10% dividend on KES 100 par
// shares produces KES 10 per share regardless of par-value drift.
type DivCalcInputs struct {
	ShareAccountID  uuid.UUID
	CounterpartyID        uuid.UUID
	CalcMethod      DividendCalcMethod
	SharesBasis     decimal.Decimal     // weighted/closing/pro-rated count
	ParValueAtRun   decimal.Decimal
	DaysInFY        int
	DaysHeldInFY    *int                // only set for pro_rated
}

func DivCalcLine(in DivCalcInputs, ratePct, whtPct decimal.Decimal) DividendRunLine {
	hundred := decimal.NewFromInt(100)
	capital := in.SharesBasis.Mul(in.ParValueAtRun).Round(2)
	gross := capital.Mul(ratePct).Div(hundred).Round(2)
	wht := gross.Mul(whtPct).Div(hundred).Round(2)
	net := gross.Sub(wht)
	return DividendRunLine{
		ShareAccountID: in.ShareAccountID,
		CounterpartyID:       in.CounterpartyID,
		CalcMethod:     in.CalcMethod,
		SharesBasis:    in.SharesBasis,
		ParValueAtRun:  in.ParValueAtRun,
		CapitalBasis:   capital,
		DaysHeldInFY:   in.DaysHeldInFY,
		DaysInFY:       in.DaysInFY,
		RateAppliedPct: ratePct,
		WHTRatePct:     whtPct,
		GrossDividend:  gross,
		WHTAmount:      wht,
		NetDividend:    net,
		PayoutMethod:   PayoutCreditSavings,
	}
}

func SumDivLines(lines []DividendRunLine) (memberCount int, totBasis, totGross, totWHT, totNet decimal.Decimal) {
	seen := map[uuid.UUID]struct{}{}
	for _, l := range lines {
		seen[l.CounterpartyID] = struct{}{}
		totBasis = totBasis.Add(l.SharesBasis)
		totGross = totGross.Add(l.GrossDividend)
		totWHT = totWHT.Add(l.WHTAmount)
		totNet = totNet.Add(l.NetDividend)
	}
	return len(seen), totBasis, totGross, totWHT, totNet
}

// ─────────── Validation ───────────

func ValidateDividendRunForTransition(r *DividendRun, target DividendRunStatus) error {
	if !CanTransitionDividend(r.Status, target) {
		return fmt.Errorf("%w: %s → %s", ErrDivInvalidStatusTxn, r.Status, target)
	}
	if target != DivCancelled && target != DivDraft {
		if r.AGMRatePct.LessThanOrEqual(decimal.Zero) ||
			r.AGMResolutionRef == "" ||
			r.AGMResolutionDate.IsZero() {
			return ErrDivAGMGateMissing
		}
	}
	if r.FYStart.IsZero() || r.FYEnd.IsZero() || !r.FYEnd.After(r.FYStart) {
		return ErrDivFYInvalid
	}
	if !r.CalcMethod.Valid() {
		return ErrDivInvalidCalcMethod
	}
	return nil
}
