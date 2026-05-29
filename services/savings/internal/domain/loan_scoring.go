// Rule-based credit scoring (Phase 6b) — internal data only.
//
// The functions here are pure: they consume a ScoringInputs struct
// (assembled by the store from members, shares, deposits, and prior
// loans) and a LoanProduct, and emit a ScoreResult. No DB calls.
// This makes the scorer easy to test and easy to swap for an ML
// overlay later (Score → ScoreResult would become an interface).
//
// Scoring is intentionally transparent: each factor produces a
// 0-100 sub-score with a weight, and the final overall score is the
// weighted average rounded to integer. Flags (hard_block / soft_flag /
// advisory) surface non-numeric concerns the underwriter needs to see.

package domain

import (
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"
)

// ─────────── Scorer inputs / outputs ───────────

type ScoringInputs struct {
	// Member context
	MemberStatus     string          // 'active', 'dormant', 'blacklisted', ...
	MembershipMonths int             // months since member created_at
	SharesHeld       int
	ShareCapital     decimal.Decimal // shares × par
	// Deposit balances split by segment (BOSA / FOSA, introduced
	// with the regulatory taxonomy in PR 1). Loan-multiplier
	// computation looks at BosaBalance only for the new bases;
	// FosaBalance feeds savings-behaviour scoring and the legacy
	// combined sum (FosaBalance + BosaBalance) when the BOSA_FOSA
	// flag is off.
	BosaBalance decimal.Decimal
	FosaBalance decimal.Decimal

	// Savings behaviour (last 12 months)
	DepositTxnCount12mo int
	TotalDeposited12mo  decimal.Decimal
	AvgMonthlyDeposit   decimal.Decimal

	// Loan history
	ActiveLoans           int
	ActiveLoansInArrears  int  // active loans with status='in_arrears' OR DPD > 0
	ActiveLoansSameProduct int // for concurrent-loan check
	SettledLoans          int
	SettledLoansCleanly   int // settled with no arrears history
	HasWrittenOffLoan     bool
}

// ApplicationRequest is the bit of the application the scorer needs.
// Keeping it separate from the full LoanApplication entity so we can
// score an in-memory app before persisting (e.g. for previews).
type ApplicationRequest struct {
	RequestedAmount             decimal.Decimal
	RequestedTermMonths         int
	MonthlyNetIncome            decimal.Decimal
	OtherIncome                 decimal.Decimal
	MonthlyExpenses             decimal.Decimal
	MonthlyExistingObligations  decimal.Decimal
	EmploymentType              *LoanEmploymentType
}

type ScoreFactor struct {
	Name   string `json:"name"`
	Score  int    `json:"score"`  // 0..100
	Weight int    `json:"weight"` // contribution weight (sum = 100)
	Note   string `json:"note"`
}

type ScoreFlag struct {
	Severity string `json:"severity"` // 'hard_block' | 'soft_flag' | 'advisory'
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type ScoreResult struct {
	OverallScore           int             `json:"overall_score"`            // 0..100
	RiskBand               string          `json:"risk_band"`                // 'A'..'D'
	Factors                []ScoreFactor   `json:"factors"`
	Flags                  []ScoreFlag     `json:"flags"`
	HasHardBlock           bool            `json:"has_hard_block"`
	AffordabilityPass      bool            `json:"affordability_pass"`
	DTIRatio               decimal.Decimal `json:"dti_ratio"`
	NetDisposableIncome    decimal.Decimal `json:"net_disposable_income"`
	ComputedInstallment    decimal.Decimal `json:"computed_installment"`     // for the requested amount/term
	ComputedMaxAmount      decimal.Decimal `json:"computed_max_amount"`      // multiplier ceiling
	ComputedMaxInstallment decimal.Decimal `json:"computed_max_installment"` // affordability ceiling per tenant policy
	RecommendedAmount      decimal.Decimal `json:"recommended_amount"`
	RecommendedTermMonths  int             `json:"recommended_term_months"`
}

// ─────────── Public entry point ───────────

// Score runs the full assessment. dtiThresholdPct and maxInstallmentPctOfDisposable
// come from tenant_operations (defaults 40% and 50% respectively).
//
// bosaFosaEnabled toggles the loan-multiplier safety behaviour:
//
//	false (flag off, legacy): "deposits" / "shares_plus_deposits"
//	  bases sum BOSA + FOSA exactly as they did before PR 1.
//	  Existing tenants keep their ceilings; no warning fires.
//	true  (flag on, segmented): "deposits" / "shares_plus_deposits"
//	  silently route to the BOSA-only equivalent (matching what
//	  prudential practice expects) and emit a soft_flag pointing
//	  the credit officer at the loan-product config.
func Score(
	in ScoringInputs,
	product *LoanProduct,
	app ApplicationRequest,
	dtiThresholdPct, maxInstallmentPctOfDisposable decimal.Decimal,
	bosaFosaEnabled bool,
) ScoreResult {
	r := ScoreResult{}

	// ─── Hard blocks ───
	switch in.MemberStatus {
	case "blacklisted", "exited", "deceased", "rejected":
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "member_ineligible_status",
			Message:  "Member status '" + in.MemberStatus + "' does not permit lending.",
		})
	}
	if in.HasWrittenOffLoan {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "prior_write_off",
			Message:  "Member has a prior written-off loan with the SACCO.",
		})
	}
	if in.ActiveLoansInArrears > 0 {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "active_arrears",
			Message:  fmt.Sprintf("Member has %d active loan(s) in arrears.", in.ActiveLoansInArrears),
		})
	}
	if product.MinMembershipMonths > 0 && in.MembershipMonths < product.MinMembershipMonths {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "membership_too_short",
			Message:  fmt.Sprintf("Membership is %d months; product requires %d.", in.MembershipMonths, product.MinMembershipMonths),
		})
	}
	if product.MinSharesRequired > 0 && in.SharesHeld < product.MinSharesRequired {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "shares_below_minimum",
			Message:  fmt.Sprintf("Member holds %d shares; product requires %d.", in.SharesHeld, product.MinSharesRequired),
		})
	}
	if !product.AllowConcurrent && in.ActiveLoansSameProduct > 0 {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "concurrent_loan_forbidden",
			Message:  "Product does not permit a second concurrent loan of this type.",
		})
	}

	// ─── Per-factor scoring (0..100 each, weighted) ───
	r.Factors = []ScoreFactor{
		scoreMembership(in),
		scoreSavingsBehaviour(in),
		scoreShareHolding(in, product),
		scoreLoanHistory(in),
		scoreEmployment(app),
	}
	// Compute overall weighted average.
	totalWeight := 0
	weightedSum := 0
	for _, f := range r.Factors {
		totalWeight += f.Weight
		weightedSum += f.Score * f.Weight
	}
	if totalWeight > 0 {
		r.OverallScore = weightedSum / totalWeight
	}
	r.RiskBand = bandForScore(r.OverallScore)

	// ─── Multiplier ceiling (Computed max amount) ───
	ceiling, multiplierWarning := computeMultiplierCeiling(in, product, bosaFosaEnabled)
	r.ComputedMaxAmount = ceiling
	if multiplierWarning != nil {
		r.Flags = append(r.Flags, *multiplierWarning)
	}

	// ─── Affordability ───
	r.NetDisposableIncome = app.MonthlyNetIncome.Add(app.OtherIncome).
		Sub(app.MonthlyExpenses).
		Sub(app.MonthlyExistingObligations)
	if r.NetDisposableIncome.LessThan(decimal.Zero) {
		r.NetDisposableIncome = decimal.Zero
	}
	r.ComputedInstallment = ComputeMonthlyInstallment(
		app.RequestedAmount, product.InterestRatePct,
		app.RequestedTermMonths, product.InterestMethod,
	)
	// DTI = (existing obligations + proposed installment) / (gross monthly income).
	grossIncome := app.MonthlyNetIncome.Add(app.OtherIncome)
	if grossIncome.GreaterThan(decimal.Zero) {
		r.DTIRatio = app.MonthlyExistingObligations.Add(r.ComputedInstallment).
			Mul(decimal.NewFromInt(100)).
			Div(grossIncome).
			Round(2)
	}
	// Affordability ceiling = pct of disposable income.
	r.ComputedMaxInstallment = r.NetDisposableIncome.
		Mul(maxInstallmentPctOfDisposable).
		Div(decimal.NewFromInt(100)).Round(2)

	dtiOK := r.DTIRatio.LessThanOrEqual(dtiThresholdPct)
	installOK := r.ComputedInstallment.LessThanOrEqual(r.ComputedMaxInstallment)
	r.AffordabilityPass = dtiOK && installOK && r.NetDisposableIncome.GreaterThan(decimal.Zero)

	if !dtiOK {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "dti_exceeds_threshold",
			Message:  fmt.Sprintf("DTI %s%% exceeds the %s%% threshold.", r.DTIRatio, dtiThresholdPct),
		})
	}
	if !installOK {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "installment_exceeds_affordability",
			Message:  "Computed installment exceeds the affordability ceiling on disposable income.",
		})
	}
	if grossIncome.IsZero() {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "advisory",
			Code:     "no_declared_income",
			Message:  "No monthly income declared — manual verification required.",
		})
	}

	// ─── Multiplier breach (soft flag if requested > ceiling) ───
	if r.ComputedMaxAmount.GreaterThan(decimal.Zero) && app.RequestedAmount.GreaterThan(r.ComputedMaxAmount) {
		r.Flags = append(r.Flags, ScoreFlag{
			Severity: "hard_block",
			Code:     "multiplier_exceeded",
			Message:  fmt.Sprintf("Requested %s exceeds the multiplier ceiling of %s.", app.RequestedAmount, r.ComputedMaxAmount),
		})
	}

	// ─── Recommended terms ───
	// If affordability is met, recommend the requested. Otherwise scale
	// the amount down so the installment fits within the affordability
	// ceiling (best-effort suggestion the underwriter can review).
	r.RecommendedTermMonths = app.RequestedTermMonths
	if r.AffordabilityPass {
		r.RecommendedAmount = app.RequestedAmount
		if r.ComputedMaxAmount.GreaterThan(decimal.Zero) && r.RecommendedAmount.GreaterThan(r.ComputedMaxAmount) {
			r.RecommendedAmount = r.ComputedMaxAmount
		}
	} else if r.ComputedMaxInstallment.GreaterThan(decimal.Zero) && product.InterestRatePct.GreaterThan(decimal.Zero) {
		r.RecommendedAmount = recommendAmountFromInstallment(
			r.ComputedMaxInstallment, product.InterestRatePct, app.RequestedTermMonths, product.InterestMethod,
		)
		if r.ComputedMaxAmount.GreaterThan(decimal.Zero) && r.RecommendedAmount.GreaterThan(r.ComputedMaxAmount) {
			r.RecommendedAmount = r.ComputedMaxAmount
		}
		if r.RecommendedAmount.GreaterThan(product.MaxAmount) {
			r.RecommendedAmount = product.MaxAmount
		}
		if r.RecommendedAmount.LessThan(product.MinAmount) {
			r.RecommendedAmount = decimal.Zero
		}
	}

	// Final hard-block flag rollup.
	for _, f := range r.Flags {
		if f.Severity == "hard_block" {
			r.HasHardBlock = true
			break
		}
	}
	return r
}

// ─────────── Per-factor scorers ───────────

func scoreMembership(in ScoringInputs) ScoreFactor {
	// 0 months → 0; 12 → 50; 36+ → 100.
	score := int(math.Min(100, float64(in.MembershipMonths)*100.0/36.0))
	note := fmt.Sprintf("%d months of membership", in.MembershipMonths)
	return ScoreFactor{Name: "Membership duration", Score: score, Weight: 15, Note: note}
}

func scoreSavingsBehaviour(in ScoringInputs) ScoreFactor {
	// Score = how many of the last 12 months had at least one deposit txn.
	// Saturate at 12; cap at 100.
	c := in.DepositTxnCount12mo
	if c > 12 {
		c = 12
	}
	score := int(float64(c) * 100.0 / 12.0)
	note := fmt.Sprintf("%d deposit transactions in the last 12 months · avg %s/mo", in.DepositTxnCount12mo, in.AvgMonthlyDeposit.StringFixed(2))
	return ScoreFactor{Name: "Savings consistency", Score: score, Weight: 25, Note: note}
}

func scoreShareHolding(in ScoringInputs, product *LoanProduct) ScoreFactor {
	min := product.MinSharesRequired
	if min <= 0 {
		min = 1
	}
	ratio := float64(in.SharesHeld) / float64(min)
	// 1x minimum → 50, 2x → 75, 3x+ → 100.
	score := int(math.Min(100, 50.0+(ratio-1.0)*25.0))
	if score < 0 {
		score = 0
	}
	note := fmt.Sprintf("%d shares (%.1fx product minimum of %d)", in.SharesHeld, ratio, product.MinSharesRequired)
	return ScoreFactor{Name: "Share holding", Score: score, Weight: 20, Note: note}
}

func scoreLoanHistory(in ScoringInputs) ScoreFactor {
	// First-time borrower (no settled loans) → 50 (neutral, no history).
	if in.SettledLoans == 0 && in.ActiveLoans == 0 {
		return ScoreFactor{Name: "Loan history", Score: 50, Weight: 25, Note: "No prior loan history — neutral baseline"}
	}
	// % of settled loans paid cleanly (no arrears).
	pct := 0
	if in.SettledLoans > 0 {
		pct = (in.SettledLoansCleanly * 100) / in.SettledLoans
	}
	// Penalise active loan count modestly (multiple open loans = elevated risk).
	score := pct - (in.ActiveLoans * 5)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	note := fmt.Sprintf("%d settled (%d clean, %d%%), %d active", in.SettledLoans, in.SettledLoansCleanly, pct, in.ActiveLoans)
	return ScoreFactor{Name: "Loan history", Score: score, Weight: 25, Note: note}
}

func scoreEmployment(app ApplicationRequest) ScoreFactor {
	// Salaried earns the highest baseline (predictable income).
	score := 50
	note := "Income type unknown"
	if app.EmploymentType != nil {
		switch *app.EmploymentType {
		case EmpSalaried:
			score = 90; note = "Salaried — predictable income"
		case EmpBusinessOwn:
			score = 70; note = "Business owner"
		case EmpSelfEmployed:
			score = 60; note = "Self-employed"
		case EmpRetired:
			score = 65; note = "Retired — fixed pension assumed"
		case EmpStudent:
			score = 30; note = "Student — limited income"
		case EmpOther:
			score = 50; note = "Other / unspecified"
		}
	}
	return ScoreFactor{Name: "Employment type", Score: score, Weight: 15, Note: note}
}

// ─────────── Helpers ───────────

func bandForScore(score int) string {
	switch {
	case score >= 80:
		return "A"
	case score >= 65:
		return "B"
	case score >= 50:
		return "C"
	default:
		return "D"
	}
}

// QualifyingAmount is the pre-application snapshot of how much a
// borrower qualifies for under a given product, broken down so the
// UI can show why. Same math the scorer uses for ComputedMaxAmount.
type QualifyingAmount struct {
	Ceiling         decimal.Decimal `json:"ceiling"`           // qualifying amount (after multiplier + product cap)
	BasisKind       string          `json:"basis_kind"`        // none | shares | bosa | bosa_plus_shares | deposits | shares_plus_deposits
	BasisValue      decimal.Decimal `json:"basis_value"`       // the amount fed into the multiplier (0 when basis=none)
	MultiplierValue decimal.Decimal `json:"multiplier_value"`  // the multiplier, e.g. 3.0
	ProductMin      decimal.Decimal `json:"product_min_amount"`
	ProductMax      decimal.Decimal `json:"product_max_amount"`
	CappedByProduct bool            `json:"capped_by_product"` // true when multiplier×basis > ProductMax
	WarningMessage  string          `json:"warning_message,omitempty"` // populated when a legacy basis was used
}

// ComputeQualifyingAmount is the exported wrapper around
// computeMultiplierCeiling that ALSO returns the basis components so
// the UI can render "your KES 50,000 BOSA × multiplier 3.0 = ceiling
// of KES 150,000". Pure function; reused by the
// /v1/loans/qualifying-amount endpoint.
func ComputeQualifyingAmount(in ScoringInputs, product *LoanProduct, bosaFosaEnabled bool) QualifyingAmount {
	out := QualifyingAmount{
		BasisKind:  string(product.MultiplierBasis),
		ProductMin: product.MinAmount,
		ProductMax: product.MaxAmount,
	}
	if product.MultiplierValue != nil {
		out.MultiplierValue = *product.MultiplierValue
	}
	switch product.MultiplierBasis {
	case MultiplierShares:
		out.BasisValue = in.ShareCapital
	case MultiplierBOSA:
		out.BasisValue = in.BosaBalance
	case MultiplierBOSAPlusShares:
		out.BasisValue = in.ShareCapital.Add(in.BosaBalance)
	case MultiplierDeposits:
		if bosaFosaEnabled {
			out.BasisValue = in.BosaBalance
		} else {
			out.BasisValue = in.BosaBalance.Add(in.FosaBalance)
		}
	case MultiplierSharesPlusDeps:
		if bosaFosaEnabled {
			out.BasisValue = in.ShareCapital.Add(in.BosaBalance)
		} else {
			out.BasisValue = in.ShareCapital.Add(in.BosaBalance).Add(in.FosaBalance)
		}
	}
	ceiling, warning := computeMultiplierCeiling(in, product, bosaFosaEnabled)
	out.Ceiling = ceiling
	if warning != nil {
		out.WarningMessage = warning.Message
	}
	if product.MultiplierBasis != MultiplierNone && product.MultiplierValue != nil {
		raw := out.BasisValue.Mul(*product.MultiplierValue).Round(2)
		out.CappedByProduct = raw.GreaterThan(product.MaxAmount)
	}
	return out
}

// computeMultiplierCeiling returns the multiplier-derived loan amount
// cap + an optional soft-flag warning when a legacy basis was used
// under bosaFosaEnabled=true. The warning is non-blocking; the
// underwriter still sees the ceiling but should rebase the product.
//
// Acceptance scenarios (per PR 2 spec):
//
//	BOSA=0, FOSA=100k, basis=bosa, value=3 → ceiling 0 (NOT 300k).
//	BOSA=50k,           basis=bosa, value=3 → ceiling 150k.
//	bosaFosaEnabled=false → legacy bases retain the combined sum.
func computeMultiplierCeiling(in ScoringInputs, product *LoanProduct, bosaFosaEnabled bool) (decimal.Decimal, *ScoreFlag) {
	if product.MultiplierBasis == MultiplierNone || product.MultiplierValue == nil {
		return product.MaxAmount, nil
	}
	var (
		basis   decimal.Decimal
		warning *ScoreFlag
	)
	switch product.MultiplierBasis {
	case MultiplierShares:
		basis = in.ShareCapital
	case MultiplierBOSA:
		basis = in.BosaBalance
	case MultiplierBOSAPlusShares:
		basis = in.ShareCapital.Add(in.BosaBalance)
	case MultiplierDeposits:
		if bosaFosaEnabled {
			// Legacy → BOSA only. SACCO-correct interpretation that
			// matches what officers thought "deposits" meant when
			// they configured the product.
			basis = in.BosaBalance
			warning = &ScoreFlag{
				Severity: "soft_flag",
				Code:     "legacy_multiplier_basis",
				Message:  "Product uses the legacy 'deposits' multiplier basis; treating BOSA-only per SACCO prudential practice. Edit the product to use 'bosa' or 'bosa_plus_shares'.",
			}
		} else {
			basis = in.BosaBalance.Add(in.FosaBalance)
		}
	case MultiplierSharesPlusDeps:
		if bosaFosaEnabled {
			basis = in.ShareCapital.Add(in.BosaBalance)
			warning = &ScoreFlag{
				Severity: "soft_flag",
				Code:     "legacy_multiplier_basis",
				Message:  "Product uses the legacy 'shares_plus_deposits' multiplier basis; treating share capital + BOSA per SACCO prudential practice. Edit the product to use 'bosa_plus_shares'.",
			}
		} else {
			basis = in.ShareCapital.Add(in.BosaBalance).Add(in.FosaBalance)
		}
	}
	ceiling := basis.Mul(*product.MultiplierValue).Round(2)
	if ceiling.GreaterThan(product.MaxAmount) {
		ceiling = product.MaxAmount
	}
	return ceiling, warning
}

// ComputeMonthlyInstallment computes the monthly installment for a
// given principal, annual rate, term, and interest method.
//
// reducing_balance: PMT formula = P × r / (1 − (1+r)^−n) where r is
// monthly rate; if rate is 0, just P/n.
// flat_rate: (P + P × annual_rate × years) / n.
func ComputeMonthlyInstallment(
	principal, annualRatePct decimal.Decimal,
	termMonths int,
	method LoanInterestMethod,
) decimal.Decimal {
	if termMonths <= 0 || principal.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	if method == InterestFlat {
		years := decimal.NewFromInt(int64(termMonths)).Div(decimal.NewFromInt(12))
		totalInterest := principal.Mul(annualRatePct).Div(decimal.NewFromInt(100)).Mul(years)
		return principal.Add(totalInterest).Div(decimal.NewFromInt(int64(termMonths))).Round(2)
	}
	// Reducing balance — float math is fine for a payment estimate; the
	// authoritative amortisation table is built in Phase 6c with decimals.
	annual, _ := annualRatePct.Float64()
	r := annual / 100.0 / 12.0
	pf, _ := principal.Float64()
	if r == 0 {
		return principal.Div(decimal.NewFromInt(int64(termMonths))).Round(2)
	}
	n := float64(termMonths)
	pmt := pf * r / (1.0 - math.Pow(1.0+r, -n))
	return decimal.NewFromFloat(pmt).Round(2)
}

// recommendAmountFromInstallment is the inverse of ComputeMonthlyInstallment —
// given the maximum monthly installment a member can afford, derive the
// largest principal that fits within term + rate. Useful for surfacing
// a "recommended amount" when affordability fails.
func recommendAmountFromInstallment(maxInstallment, annualRatePct decimal.Decimal, termMonths int, method LoanInterestMethod) decimal.Decimal {
	if maxInstallment.LessThanOrEqual(decimal.Zero) || termMonths <= 0 {
		return decimal.Zero
	}
	if method == InterestFlat {
		years := decimal.NewFromInt(int64(termMonths)).Div(decimal.NewFromInt(12))
		// installment × n = P + P×rate×years = P × (1 + rate×years)
		factor := decimal.NewFromInt(1).Add(annualRatePct.Div(decimal.NewFromInt(100)).Mul(years))
		if factor.IsZero() {
			return decimal.Zero
		}
		return maxInstallment.Mul(decimal.NewFromInt(int64(termMonths))).Div(factor).Round(2)
	}
	mi, _ := maxInstallment.Float64()
	annual, _ := annualRatePct.Float64()
	r := annual / 100.0 / 12.0
	if r == 0 {
		return maxInstallment.Mul(decimal.NewFromInt(int64(termMonths))).Round(2)
	}
	n := float64(termMonths)
	p := mi * (1.0 - math.Pow(1.0+r, -n)) / r
	return decimal.NewFromFloat(p).Round(2)
}

// FlagsToJSONString — best-effort JSON serialization for storage. We
// keep this private (used only by the application store).
func flagsAsString(fs []ScoreFlag) string {
	if len(fs) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range fs {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"severity":"`)
		sb.WriteString(f.Severity)
		sb.WriteString(`","code":"`)
		sb.WriteString(f.Code)
		sb.WriteString(`","message":"`)
		sb.WriteString(jsonEscape(f.Message))
		sb.WriteString(`"}`)
	}
	sb.WriteByte(']')
	return sb.String()
}

func jsonEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
