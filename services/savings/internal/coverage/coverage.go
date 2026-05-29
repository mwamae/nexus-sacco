// Phase 1.5a — security-coverage evaluator.
//
// Given a tenant's per-product security policy and the actual pledged
// security (sum of accepted guarantor pledges, sum of pledged collateral
// forced-sale-values), decides whether the loan meets the policy and
// produces a human-readable reason for the UI + the workflow's 409 body.
//
// Used by:
//   1. handler/collateral.go     — GET /v1/loan-applications/{id}/security-coverage
//   2. handler/loan_application_workflow.go — workflow approve gate
//   3. handler/loan_disburse.go  — second gate at disbursement time
//
// Pure function; no I/O. Inputs come from store queries the caller runs.

package coverage

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// SecurityModel — the five policy values per migration 0048.
const (
	ModelNone           = "none"
	ModelGuarantorOnly  = "guarantor_only"
	ModelCollateralOnly = "collateral_only"
	ModelEither         = "either"
	ModelBoth           = "both"
)

// Coverage is the actual numbers from the application's current state.
type Coverage struct {
	GuarantorPledged decimal.Decimal // sum of accepted loan_guarantees.amount_guaranteed
	CollateralFSV    decimal.Decimal // sum of pledged collateral_valuations (current) forced_sale_value
	LoanAmount       decimal.Decimal // approved_amount, falling back to requested_amount
}

// Policy is the loan_products.security_model + the two min-cover %'s.
type Policy struct {
	SecurityModel         string
	MinGuarantorCoverPct  decimal.Decimal
	MinCollateralCoverPct decimal.Decimal
}

// Result is what the evaluator returns. PolicyMet is the binary the
// workflow approve gate keys off; Reason is the human-readable
// explanation.
type Result struct {
	GuarantorPct     decimal.Decimal
	CollateralPct    decimal.Decimal
	GuarantorPasses  bool
	CollateralPasses bool
	PolicyMet        bool
	Reason           string

	// GuarantorShortfall + CollateralShortfall — the absolute amounts
	// the applicant needs to add to reach the minimums (when the
	// respective side fails). Zero when that side passes or isn't
	// required by the policy.
	GuarantorShortfall  decimal.Decimal
	CollateralShortfall decimal.Decimal
}

// Evaluate runs the policy. Always returns a sensible result; never
// errors (caller is expected to handle zero loan amounts in the calling
// context — here we treat zero as "no loan, no requirement").
func Evaluate(c Coverage, p Policy) Result {
	r := Result{}

	if c.LoanAmount.IsZero() {
		// No loan amount means nothing to cover. Treat as a pass with
		// the boundary "no loan amount yet" reason — callers (the UI
		// in particular) can decide whether to render the card at all.
		r.PolicyMet = true
		r.Reason = "No loan amount set; coverage check pending."
		return r
	}

	hundred := decimal.NewFromInt(100)

	// % = pledged / loan × 100, rounded to two places for display
	// stability. Underlying decimals stay precise.
	if c.LoanAmount.GreaterThan(decimal.Zero) {
		r.GuarantorPct = c.GuarantorPledged.Div(c.LoanAmount).Mul(hundred).Round(2)
		r.CollateralPct = c.CollateralFSV.Div(c.LoanAmount).Mul(hundred).Round(2)
	}

	r.GuarantorPasses = r.GuarantorPct.GreaterThanOrEqual(p.MinGuarantorCoverPct)
	r.CollateralPasses = r.CollateralPct.GreaterThanOrEqual(p.MinCollateralCoverPct)

	// Shortfalls — KES amounts needed to hit the minimum on each side.
	// Computed off the loan amount so the UI can show a concrete ask.
	if !r.GuarantorPasses {
		need := p.MinGuarantorCoverPct.Div(hundred).Mul(c.LoanAmount).Round(2)
		r.GuarantorShortfall = need.Sub(c.GuarantorPledged)
		if r.GuarantorShortfall.IsNegative() {
			r.GuarantorShortfall = decimal.Zero
		}
	}
	if !r.CollateralPasses {
		need := p.MinCollateralCoverPct.Div(hundred).Mul(c.LoanAmount).Round(2)
		r.CollateralShortfall = need.Sub(c.CollateralFSV)
		if r.CollateralShortfall.IsNegative() {
			r.CollateralShortfall = decimal.Zero
		}
	}

	switch p.SecurityModel {
	case ModelNone:
		r.PolicyMet = true
		r.Reason = "No external security required for this product."
	case ModelGuarantorOnly:
		r.PolicyMet = r.GuarantorPasses
		r.Reason = singleSideReason("guarantor_only", "guarantor", r.GuarantorPasses,
			r.GuarantorPct, p.MinGuarantorCoverPct, r.GuarantorShortfall)
	case ModelCollateralOnly:
		r.PolicyMet = r.CollateralPasses
		r.Reason = singleSideReason("collateral_only", "collateral", r.CollateralPasses,
			r.CollateralPct, p.MinCollateralCoverPct, r.CollateralShortfall)
	case ModelEither:
		r.PolicyMet = r.GuarantorPasses || r.CollateralPasses
		r.Reason = eitherReason(r, p)
	case ModelBoth:
		r.PolicyMet = r.GuarantorPasses && r.CollateralPasses
		r.Reason = bothReason(r, p)
	default:
		// Unknown model — treat as failed, surface the typo in the message.
		r.PolicyMet = false
		r.Reason = fmt.Sprintf("Unknown security model %q on product; cannot evaluate.", p.SecurityModel)
	}
	return r
}

func singleSideReason(model, side string, passes bool, pct, min, shortfall decimal.Decimal) string {
	if passes {
		return fmt.Sprintf("Policy met (%s): %s coverage %s%% ≥ %s%%.",
			model, side, pctString(pct), pctString(min))
	}
	return fmt.Sprintf("Policy not met (%s): %s coverage %s%% < %s%% — add %s shortfall.",
		model, side, pctString(pct), pctString(min), kes(shortfall))
}

func eitherReason(r Result, p Policy) string {
	if r.PolicyMet {
		// At least one side passes. Lead with the passing side.
		if r.CollateralPasses {
			return fmt.Sprintf("Policy met (either): collateral coverage %s%% ≥ %s%%.",
				pctString(r.CollateralPct), pctString(p.MinCollateralCoverPct))
		}
		return fmt.Sprintf("Policy met (either): guarantor coverage %s%% ≥ %s%%.",
			pctString(r.GuarantorPct), pctString(p.MinGuarantorCoverPct))
	}
	return fmt.Sprintf(
		"Policy not met (either): guarantor coverage %s%% < %s%% ✗, collateral coverage %s%% < %s%% ✗ — add %s guarantor pledges OR %s collateral FSV.",
		pctString(r.GuarantorPct), pctString(p.MinGuarantorCoverPct),
		pctString(r.CollateralPct), pctString(p.MinCollateralCoverPct),
		kes(r.GuarantorShortfall), kes(r.CollateralShortfall),
	)
}

func bothReason(r Result, p Policy) string {
	gMark := "✓"
	if !r.GuarantorPasses {
		gMark = "✗"
	}
	cMark := "✓"
	if !r.CollateralPasses {
		cMark = "✗"
	}

	if r.PolicyMet {
		return fmt.Sprintf("Policy met (both): guarantor coverage %s%% ≥ %s%% %s, collateral coverage %s%% ≥ %s%% %s.",
			pctString(r.GuarantorPct), pctString(p.MinGuarantorCoverPct), gMark,
			pctString(r.CollateralPct), pctString(p.MinCollateralCoverPct), cMark)
	}

	tail := ""
	switch {
	case !r.GuarantorPasses && !r.CollateralPasses:
		tail = fmt.Sprintf(" — add %s guarantor pledges AND %s collateral FSV.",
			kes(r.GuarantorShortfall), kes(r.CollateralShortfall))
	case !r.GuarantorPasses:
		tail = fmt.Sprintf(" — add %s guarantor pledges.", kes(r.GuarantorShortfall))
	case !r.CollateralPasses:
		tail = fmt.Sprintf(" — add %s collateral FSV.", kes(r.CollateralShortfall))
	}
	return fmt.Sprintf("Policy not met (both): guarantor coverage %s%% %s %s%%, collateral coverage %s%% %s %s%%%s",
		pctString(r.GuarantorPct), cmpSym(r.GuarantorPasses), pctString(p.MinGuarantorCoverPct),
		pctString(r.CollateralPct), cmpSym(r.CollateralPasses), pctString(p.MinCollateralCoverPct),
		tail,
	)
}

func cmpSym(passes bool) string {
	if passes {
		return "≥"
	}
	return "<"
}

// pctString prints e.g. "100" or "142.5" — strips trailing .00 but
// keeps non-zero fractional places. Trailing zeros are only stripped
// AFTER a decimal point so "100" stays "100".
func pctString(d decimal.Decimal) string {
	s := d.Round(2).String()
	if !strings.Contains(s, ".") {
		return s
	}
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if s == "" {
		s = "0"
	}
	return s
}

// kes prints a KES amount with thousand-separators, e.g. "KES 225,000".
func kes(d decimal.Decimal) string {
	// Round to whole shillings for display.
	whole := d.Round(0).String()
	// Insert thousand-separators.
	sign := ""
	if len(whole) > 0 && whole[0] == '-' {
		sign = "-"
		whole = whole[1:]
	}
	// Handle decimal part if present (shouldn't after Round(0), but defensive).
	intPart := whole
	fracPart := ""
	for i := 0; i < len(whole); i++ {
		if whole[i] == '.' {
			intPart = whole[:i]
			fracPart = whole[i:]
			break
		}
	}
	// Walk from right inserting commas every 3 digits.
	n := len(intPart)
	if n <= 3 {
		return "KES " + sign + intPart + fracPart
	}
	var out []byte
	first := n % 3
	if first > 0 {
		out = append(out, intPart[:first]...)
		if n > first {
			out = append(out, ',')
		}
	}
	for i := first; i < n; i += 3 {
		out = append(out, intPart[i:i+3]...)
		if i+3 < n {
			out = append(out, ',')
		}
	}
	return "KES " + sign + string(out) + fracPart
}
