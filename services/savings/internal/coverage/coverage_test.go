package coverage

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Standard fixtures the table cases re-use.
func standardPolicy(model string) Policy {
	return Policy{
		SecurityModel:         model,
		MinGuarantorCoverPct:  dec("100"),
		MinCollateralCoverPct: dec("125"),
	}
}

func TestEvaluate_AcrossModels(t *testing.T) {
	loan := dec("200000")

	tests := []struct {
		name          string
		model         string
		guarantor     string
		collateral    string
		wantPolicyMet bool
		wantReasonSub string // substring assertion (full string is brittle)
	}{
		// ───── none ─────
		{"none/no security", ModelNone, "0", "0", true, "No external security required"},
		{"none/with security present (still trivially passes)", ModelNone, "500000", "500000", true, "No external security required"},

		// ───── guarantor_only ─────
		{"guarantor_only/under", ModelGuarantorOnly, "100000", "0", false, "guarantor coverage 50% < 100%"},
		{"guarantor_only/at threshold", ModelGuarantorOnly, "200000", "0", true, "guarantor coverage 100% ≥ 100%"},
		{"guarantor_only/over", ModelGuarantorOnly, "250000", "0", true, "guarantor coverage 125% ≥ 100%"},
		{"guarantor_only/collateral irrelevant", ModelGuarantorOnly, "100000", "500000", false, "Policy not met"},

		// ───── collateral_only ─────
		{"collateral_only/under", ModelCollateralOnly, "0", "200000", false, "collateral coverage 100% < 125%"},
		{"collateral_only/at threshold", ModelCollateralOnly, "0", "250000", true, "collateral coverage 125% ≥ 125%"},
		{"collateral_only/over", ModelCollateralOnly, "0", "400000", true, "collateral coverage 200% ≥ 125%"},
		{"collateral_only/guarantor irrelevant", ModelCollateralOnly, "200000", "100000", false, "Policy not met"},

		// ───── either ─────
		{"either/neither side passes", ModelEither, "100000", "200000", false, "Policy not met (either)"},
		{"either/guarantor passes only", ModelEither, "200000", "100000", true, "Policy met (either): guarantor coverage 100%"},
		{"either/collateral passes only", ModelEither, "50000", "250000", true, "Policy met (either): collateral coverage 125%"},
		{"either/both pass — collateral side cited first", ModelEither, "200000", "300000", true, "Policy met (either): collateral coverage 150%"},

		// ───── both ─────
		{"both/neither", ModelBoth, "100000", "100000", false, "Policy not met (both)"},
		{"both/guarantor only", ModelBoth, "200000", "100000", false, "add KES 150,000 collateral FSV"},
		{"both/collateral only", ModelBoth, "100000", "300000", false, "add KES 100,000 guarantor pledges"},
		{"both/both fail", ModelBoth, "50000", "100000", false, "AND"},
		{"both/both pass", ModelBoth, "200000", "300000", true, "Policy met (both)"},

		// ───── unknown model ─────
		{"unknown model", "weird", "0", "0", false, "Unknown security model"},

		// ───── zero loan ─────
		{"zero loan/no requirement", ModelGuarantorOnly, "0", "0", true, "No loan amount set"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			amt := loan
			if strings.Contains(tc.name, "zero loan") {
				amt = decimal.Zero
			}
			r := Evaluate(Coverage{
				GuarantorPledged: dec(tc.guarantor),
				CollateralFSV:    dec(tc.collateral),
				LoanAmount:       amt,
			}, standardPolicy(tc.model))
			if r.PolicyMet != tc.wantPolicyMet {
				t.Errorf("PolicyMet=%v, want %v.  Reason=%s", r.PolicyMet, tc.wantPolicyMet, r.Reason)
			}
			if !strings.Contains(r.Reason, tc.wantReasonSub) {
				t.Errorf("Reason missing substring %q.  Got: %s", tc.wantReasonSub, r.Reason)
			}
		})
	}
}

func TestEvaluate_PercentageRounding(t *testing.T) {
	// 33333 / 100000 = 33.333% → rounded to 33.33.
	r := Evaluate(Coverage{
		GuarantorPledged: dec("33333"),
		CollateralFSV:    decimal.Zero,
		LoanAmount:       dec("100000"),
	}, standardPolicy(ModelGuarantorOnly))
	if r.GuarantorPct.String() != "33.33" {
		t.Errorf("GuarantorPct=%s, want 33.33", r.GuarantorPct.String())
	}
}

func TestEvaluate_Shortfall(t *testing.T) {
	// guarantor_only: need 100%, have 60%. Shortfall = 100k - 60k = 40k.
	r := Evaluate(Coverage{
		GuarantorPledged: dec("60000"),
		CollateralFSV:    decimal.Zero,
		LoanAmount:       dec("100000"),
	}, standardPolicy(ModelGuarantorOnly))
	if !r.GuarantorShortfall.Equal(dec("40000")) {
		t.Errorf("GuarantorShortfall=%s, want 40000", r.GuarantorShortfall.String())
	}

	// collateral_only: need 125%, have 0%. Shortfall = 125k.
	r2 := Evaluate(Coverage{
		GuarantorPledged: decimal.Zero,
		CollateralFSV:    decimal.Zero,
		LoanAmount:       dec("100000"),
	}, standardPolicy(ModelCollateralOnly))
	if !r2.CollateralShortfall.Equal(dec("125000")) {
		t.Errorf("CollateralShortfall=%s, want 125000", r2.CollateralShortfall.String())
	}
}

func TestEvaluate_ShortfallZeroOnPass(t *testing.T) {
	r := Evaluate(Coverage{
		GuarantorPledged: dec("200000"),
		CollateralFSV:    dec("400000"),
		LoanAmount:       dec("200000"),
	}, standardPolicy(ModelBoth))
	if !r.GuarantorShortfall.IsZero() {
		t.Errorf("GuarantorShortfall should be zero when side passes, got %s", r.GuarantorShortfall)
	}
	if !r.CollateralShortfall.IsZero() {
		t.Errorf("CollateralShortfall should be zero when side passes, got %s", r.CollateralShortfall)
	}
}

func TestPctString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"100", "100"},
		{"125.00", "125"},
		{"33.33", "33.33"},
		{"50.5", "50.5"},
		{"0", "0"},
	}
	for _, c := range cases {
		got := pctString(dec(c.in))
		if got != c.want {
			t.Errorf("pctString(%s)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestKes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "KES 0"},
		{"500", "KES 500"},
		{"1000", "KES 1,000"},
		{"100000", "KES 100,000"},
		{"1234567", "KES 1,234,567"},
		{"225000.5", "KES 225,001"}, // rounded
	}
	for _, c := range cases {
		got := kes(dec(c.in))
		if got != c.want {
			t.Errorf("kes(%s)=%q, want %q", c.in, got, c.want)
		}
	}
}
