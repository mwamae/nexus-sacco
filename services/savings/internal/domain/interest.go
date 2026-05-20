// Interest engine domain types + state machine + per-line calculator.
//
// The AGM gate is enforced by the state machine: a run cannot move past
// 'draft' without an AGM rate and resolution reference (validated at
// state-transition time, not by the schema constraint, so the message
// surfaces cleanly).

package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Status + transitions ───────────

type InterestRunStatus string

const (
	RunDraft     InterestRunStatus = "draft"
	RunComputing InterestRunStatus = "computing"
	RunPreview   InterestRunStatus = "preview"
	RunApproved  InterestRunStatus = "approved"
	RunPosting   InterestRunStatus = "posting"
	RunPosted    InterestRunStatus = "posted"
	RunLocked    InterestRunStatus = "locked"
	RunCancelled InterestRunStatus = "cancelled"
)

// CanTransition encodes the legal status transitions. Any other move is
// rejected. Locking, posting, and cancellation are terminal-ish: a
// posted run can only go to locked; a locked run is fully immutable; a
// cancelled run cannot be resurrected.
func CanTransition(from, to InterestRunStatus) bool {
	switch from {
	case RunDraft:
		// Compute is synchronous → draft → preview is allowed directly.
		// `computing` exists for future async/long-running runs.
		return to == RunPreview || to == RunComputing || to == RunCancelled
	case RunComputing:
		return to == RunPreview || to == RunDraft || to == RunCancelled
	case RunPreview:
		// Re-compute (preview → preview) is a no-op-friendly self-loop so
		// the operator can iterate after editing line payout methods.
		return to == RunPreview || to == RunApproved || to == RunDraft || to == RunCancelled
	case RunApproved:
		// Once approved, the only forward path is posting. Re-computing
		// after approval would invalidate the audit chain — operators
		// must cancel and create a new run instead.
		return to == RunPosting || to == RunPosted || to == RunCancelled
	case RunPosting:
		return to == RunPosted || to == RunApproved
	case RunPosted:
		return to == RunLocked
	}
	return false
}

// ─────────── Payout method ───────────

type InterestPayoutMethod string

const (
	PayoutCreditSavings InterestPayoutMethod = "credit_savings"
	PayoutBuyShares     InterestPayoutMethod = "buy_shares"
	PayoutExternal      InterestPayoutMethod = "external"
)

func (p InterestPayoutMethod) Valid() bool {
	switch p {
	case PayoutCreditSavings, PayoutBuyShares, PayoutExternal:
		return true
	}
	return false
}

// ─────────── Entities ───────────

type InterestRun struct {
	ID                   uuid.UUID         `json:"id"`
	TenantID             uuid.UUID         `json:"tenant_id"`
	RunNo                string            `json:"run_no"`
	FinancialYearLabel   string            `json:"financial_year_label"`
	FYStart              time.Time         `json:"fy_start"`
	FYEnd                time.Time         `json:"fy_end"`
	Status               InterestRunStatus `json:"status"`
	AGMRatePct           decimal.Decimal   `json:"agm_rate_pct"`
	AGMResolutionRef     string            `json:"agm_resolution_ref"`
	AGMResolutionDate    time.Time         `json:"agm_resolution_date"`
	WHTRatePct           decimal.Decimal   `json:"wht_rate_pct"`
	ProductIDs           []uuid.UUID       `json:"product_ids"`
	MemberCount          *int              `json:"member_count,omitempty"`
	TotalWeightedBalance *decimal.Decimal  `json:"total_weighted_balance,omitempty"`
	TotalGrossInterest   *decimal.Decimal  `json:"total_gross_interest,omitempty"`
	TotalWHT             *decimal.Decimal  `json:"total_wht,omitempty"`
	TotalNetInterest     *decimal.Decimal  `json:"total_net_interest,omitempty"`
	Notes                *string           `json:"notes,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	CreatedBy            uuid.UUID         `json:"created_by"`
	ComputedAt           *time.Time        `json:"computed_at,omitempty"`
	ComputedBy           *uuid.UUID        `json:"computed_by,omitempty"`
	SubmittedAt          *time.Time        `json:"submitted_at,omitempty"`
	SubmittedBy          *uuid.UUID        `json:"submitted_by,omitempty"`
	WorkflowInstanceID   *uuid.UUID        `json:"workflow_instance_id,omitempty"`
	ApprovedAt           *time.Time        `json:"approved_at,omitempty"`
	ApprovedBy           *uuid.UUID        `json:"approved_by,omitempty"`
	PostedAt             *time.Time        `json:"posted_at,omitempty"`
	PostedBy             *uuid.UUID        `json:"posted_by,omitempty"`
	LockedAt             *time.Time        `json:"locked_at,omitempty"`
	CancelledAt          *time.Time        `json:"cancelled_at,omitempty"`
	CancelledBy          *uuid.UUID        `json:"cancelled_by,omitempty"`
	CancellationReason   *string           `json:"cancellation_reason,omitempty"`
}

type InterestRunLine struct {
	ID                    uuid.UUID            `json:"id"`
	TenantID              uuid.UUID            `json:"tenant_id"`
	RunID                 uuid.UUID            `json:"run_id"`
	AccountID             uuid.UUID            `json:"account_id"`
	MemberID              uuid.UUID            `json:"member_id"`
	ProductID             uuid.UUID            `json:"product_id"`
	DaysInFY              int                  `json:"days_in_fy"`
	DaysWithSnapshots     int                  `json:"days_with_snapshots"`
	SumOfDailyBalances    decimal.Decimal      `json:"sum_of_daily_balances"`
	WeightedAvgBalance    decimal.Decimal      `json:"weighted_avg_balance"`
	RateAppliedPct        decimal.Decimal      `json:"rate_applied_pct"`
	WHTRatePct            decimal.Decimal      `json:"wht_rate_pct"`
	GrossInterest         decimal.Decimal      `json:"gross_interest"`
	WHTAmount             decimal.Decimal      `json:"wht_amount"`
	NetInterest           decimal.Decimal      `json:"net_interest"`
	PayoutMethod          InterestPayoutMethod `json:"payout_method"`
	PayoutTargetAccountID *uuid.UUID           `json:"payout_target_account_id,omitempty"`
	PayoutExternalChannel *string              `json:"payout_external_channel,omitempty"`
	PayoutExternalRef     *string              `json:"payout_external_ref,omitempty"`
	PostedAt              *time.Time           `json:"posted_at,omitempty"`
	PostedTxnID           *uuid.UUID           `json:"posted_txn_id,omitempty"`
	ShareTxnID            *uuid.UUID           `json:"share_txn_id,omitempty"`
	Notes                 *string              `json:"notes,omitempty"`
}

type TaxPayableEntry struct {
	ID            uuid.UUID       `json:"id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	SourceKind    string          `json:"source_kind"`
	SourceID      *uuid.UUID      `json:"source_id,omitempty"`
	MemberID      uuid.UUID       `json:"member_id"`
	MemberNo      string          `json:"member_no"`
	MemberName    string          `json:"member_name"`
	FYLabel       string          `json:"fy_label"`
	GrossAmount   decimal.Decimal `json:"gross_amount"`
	WHTRatePct    decimal.Decimal `json:"wht_rate_pct"`
	WHTAmount     decimal.Decimal `json:"wht_amount"`
	PostedAt      time.Time       `json:"posted_at"`
	PostedBy      uuid.UUID       `json:"posted_by"`
	RemittedAt    *time.Time      `json:"remitted_at,omitempty"`
	RemittanceRef *string         `json:"remittance_ref,omitempty"`
}

// ─────────── Errors ───────────

var (
	ErrAGMGateMissing   = errors.New("AGM resolution rate and reference are required before this transition")
	ErrInvalidStatusTxn = errors.New("invalid status transition")
	ErrNoProductsInScope = errors.New("at least one interest-eligible product must be in scope")
	ErrFYInvalid        = errors.New("financial year start must precede end")
	ErrLineAlreadyPosted = errors.New("this line has already been posted")
	ErrPayoutTargetMissing = errors.New("payout_target_account_id is required for credit_savings payout")
	ErrPayoutChannelMissing = errors.New("payout_external_channel is required for external payout")
	ErrRunNotPostable   = errors.New("run is not in a postable state (must be 'approved')")
)

// ─────────── Calculator ───────────

// CalcInputs holds the raw inputs the calculator needs per (account):
//   - sum of daily balances across the FY (from deposit_daily_balances)
//   - count of distinct snapshot days seen
//   - days in the FY (FY_end - FY_start + 1)
//
// Output is a fully-built InterestRunLine with monetary fields rounded
// to 2 decimal places. Gross = avg × rate; WHT = gross × wht_rate;
// net = gross − wht.
type CalcInputs struct {
	AccountID            uuid.UUID
	MemberID             uuid.UUID
	ProductID            uuid.UUID
	DaysInFY             int
	DaysWithSnapshots    int
	SumOfDailyBalances   decimal.Decimal
}

func CalcLine(in CalcInputs, ratePct, whtPct decimal.Decimal) InterestRunLine {
	hundred := decimal.NewFromInt(100)
	daysInFY := decimal.NewFromInt(int64(in.DaysInFY))
	if daysInFY.IsZero() {
		daysInFY = decimal.NewFromInt(1)
	}
	weightedAvg := in.SumOfDailyBalances.Div(daysInFY).Round(2)
	gross := weightedAvg.Mul(ratePct).Div(hundred).Round(2)
	wht := gross.Mul(whtPct).Div(hundred).Round(2)
	net := gross.Sub(wht)
	return InterestRunLine{
		AccountID:          in.AccountID,
		MemberID:           in.MemberID,
		ProductID:          in.ProductID,
		DaysInFY:           in.DaysInFY,
		DaysWithSnapshots:  in.DaysWithSnapshots,
		SumOfDailyBalances: in.SumOfDailyBalances,
		WeightedAvgBalance: weightedAvg,
		RateAppliedPct:     ratePct,
		WHTRatePct:         whtPct,
		GrossInterest:      gross,
		WHTAmount:          wht,
		NetInterest:        net,
		PayoutMethod:       PayoutCreditSavings,
	}
}

// SumLines aggregates per-line totals for the run header.
func SumLines(lines []InterestRunLine) (memberCount int, totalWeighted, totalGross, totalWHT, totalNet decimal.Decimal) {
	seen := map[uuid.UUID]struct{}{}
	for _, l := range lines {
		seen[l.MemberID] = struct{}{}
		totalWeighted = totalWeighted.Add(l.WeightedAvgBalance)
		totalGross = totalGross.Add(l.GrossInterest)
		totalWHT = totalWHT.Add(l.WHTAmount)
		totalNet = totalNet.Add(l.NetInterest)
	}
	return len(seen), totalWeighted, totalGross, totalWHT, totalNet
}

// ─────────── Validation ───────────

// ValidateRunForTransition returns an error if the run does not satisfy
// the AGM gate or business invariants required for the requested move.
func ValidateRunForTransition(r *InterestRun, target InterestRunStatus) error {
	if !CanTransition(r.Status, target) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidStatusTxn, r.Status, target)
	}
	// AGM gate — required before any forward progress.
	if target != RunCancelled && target != RunDraft {
		if r.AGMRatePct.LessThanOrEqual(decimal.Zero) ||
			r.AGMResolutionRef == "" ||
			r.AGMResolutionDate.IsZero() {
			return ErrAGMGateMissing
		}
	}
	if r.FYStart.IsZero() || r.FYEnd.IsZero() || !r.FYEnd.After(r.FYStart) {
		return ErrFYInvalid
	}
	if len(r.ProductIDs) == 0 {
		return ErrNoProductsInScope
	}
	return nil
}

// ─────────── FY helpers ───────────

// DefaultFYRange returns the FY that just ended for a tenant whose FY
// starts at startMonth/startDay. If today is during the FY (or hasn't
// crossed an FY boundary yet), the function returns the prior FY.
//
// E.g. for a Jan-Dec tenant on 2026-05-20, this returns 2025-01-01 to
// 2025-12-31. For a Jul-Jun tenant on 2026-05-20, it returns 2025-07-01
// to 2026-06-30 (still in progress; caller decides whether to allow
// computing on an open FY).
func DefaultFYRange(now time.Time, startMonth, startDay int) (start, end time.Time) {
	year := now.Year()
	// Build "this year's FY start"
	candidateStart := time.Date(year, time.Month(startMonth), startDay, 0, 0, 0, 0, time.UTC)
	if now.Before(candidateStart) {
		// We haven't crossed this year's FY start yet; the prior FY ran from
		// (year-1) startMonth to (year) startMonth-1.
		start = time.Date(year-1, time.Month(startMonth), startDay, 0, 0, 0, 0, time.UTC)
		end = candidateStart.AddDate(0, 0, -1)
	} else {
		// Inside the current FY → return the *previous* one as the most
		// recent "closeable" year.
		start = time.Date(year-1, time.Month(startMonth), startDay, 0, 0, 0, 0, time.UTC)
		end = candidateStart.AddDate(0, 0, -1)
	}
	return start, end
}

// FYLabel produces a human-readable label like "FY 2025-2026" or
// "FY 2025" when start/end fall in the same calendar year.
func FYLabel(start, end time.Time) string {
	if start.Year() == end.Year() {
		return fmt.Sprintf("FY %d", start.Year())
	}
	return fmt.Sprintf("FY %d-%d", start.Year(), end.Year())
}

// DaysInFY returns end - start + 1 (inclusive).
func DaysInFY(start, end time.Time) int {
	return int(end.Sub(start).Hours()/24) + 1
}
