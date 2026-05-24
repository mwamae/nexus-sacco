// PR 2 (BOSA / FOSA loan-multiplier safety) — pure unit tests for
// computeMultiplierCeiling. Covers:
//
//   1. The two new BOSA-aware bases produce the SACCO-correct cap.
//   2. The acceptance scenarios in the spec: 100k FOSA + 0 BOSA on
//      a basis=bosa product caps at 0 (NOT 300k); 50k BOSA caps at
//      150k.
//   3. Legacy bases under flag=off retain pre-PR-1 combined-sum
//      ceilings (existing tenants don't see a regression).
//   4. Legacy bases under flag=on silently route to the BOSA-only
//      computation AND attach a soft_flag the UI surfaces.
//   5. MultiplierNone short-circuits to product.MaxAmount.

package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func ptrDec(v string) *decimal.Decimal {
	d := decimal.RequireFromString(v)
	return &d
}

func newMultiplierProduct(basis LoanMultiplierBasis, value, max string) *LoanProduct {
	return &LoanProduct{
		MultiplierBasis: basis,
		MultiplierValue: ptrDec(value),
		MaxAmount:       decimal.RequireFromString(max),
	}
}

func TestComputeMultiplierCeiling(t *testing.T) {
	cases := []struct {
		name             string
		basis            LoanMultiplierBasis
		multiplier       string
		productMax       string
		shareCapital     string
		bosa             string
		fosa             string
		bosaFosaEnabled  bool
		wantCeiling      string
		wantWarningCode  string // "" = no warning expected
	}{
		// ─── Spec acceptance ───
		{
			name:        "Acceptance: 100k FOSA + 0 BOSA on basis=bosa caps at 0",
			basis:       MultiplierBOSA, multiplier: "3", productMax: "10000000",
			shareCapital: "0", bosa: "0", fosa: "100000",
			bosaFosaEnabled: true,
			wantCeiling:     "0",
		},
		{
			name:        "Acceptance: 50k BOSA on basis=bosa caps at 150k",
			basis:       MultiplierBOSA, multiplier: "3", productMax: "10000000",
			shareCapital: "0", bosa: "50000", fosa: "0",
			bosaFosaEnabled: true,
			wantCeiling:     "150000",
		},

		// ─── New bases (flag-state irrelevant) ───
		{
			name:        "bosa_plus_shares = ShareCapital + BOSA, FOSA ignored",
			basis:       MultiplierBOSAPlusShares, multiplier: "2", productMax: "10000000",
			shareCapital: "10000", bosa: "50000", fosa: "100000",
			bosaFosaEnabled: true,
			wantCeiling:     "120000",
		},
		{
			name:        "shares basis ignores deposits entirely",
			basis:       MultiplierShares, multiplier: "5", productMax: "10000000",
			shareCapital: "20000", bosa: "999999", fosa: "999999",
			bosaFosaEnabled: false,
			wantCeiling:     "100000",
		},

		// ─── Legacy bases under flag off (pre-PR-1 behaviour) ───
		{
			name:        "Legacy 'deposits' under flag=off sums BOSA + FOSA, no warning",
			basis:       MultiplierDeposits, multiplier: "3", productMax: "10000000",
			shareCapital: "0", bosa: "20000", fosa: "30000",
			bosaFosaEnabled: false,
			wantCeiling:     "150000",
		},
		{
			name:        "Legacy 'shares_plus_deposits' under flag=off sums shares + BOSA + FOSA",
			basis:       MultiplierSharesPlusDeps, multiplier: "2", productMax: "10000000",
			shareCapital: "10000", bosa: "20000", fosa: "30000",
			bosaFosaEnabled: false,
			wantCeiling:     "120000",
		},

		// ─── Legacy bases under flag on (BOSA-only + warning) ───
		{
			name:            "Legacy 'deposits' under flag=on uses BOSA only + warns",
			basis:           MultiplierDeposits, multiplier: "3", productMax: "10000000",
			shareCapital:    "0", bosa: "20000", fosa: "30000",
			bosaFosaEnabled: true,
			wantCeiling:     "60000", // BOSA × 3 (FOSA dropped)
			wantWarningCode: "legacy_multiplier_basis",
		},
		{
			name:            "Legacy 'shares_plus_deposits' under flag=on uses ShareCapital + BOSA + warns",
			basis:           MultiplierSharesPlusDeps, multiplier: "2", productMax: "10000000",
			shareCapital:    "10000", bosa: "20000", fosa: "30000",
			bosaFosaEnabled: true,
			wantCeiling:     "60000",
			wantWarningCode: "legacy_multiplier_basis",
		},

		// ─── Ceiling capped at product.MaxAmount ───
		{
			name:        "Ceiling is capped at product.MaxAmount",
			basis:       MultiplierBOSA, multiplier: "10", productMax: "100000",
			shareCapital: "0", bosa: "50000", fosa: "0",
			bosaFosaEnabled: true,
			wantCeiling:     "100000", // 500k computed, capped to 100k
		},

		// ─── MultiplierNone short-circuits ───
		{
			name:        "MultiplierNone returns product.MaxAmount and no warning",
			basis:       MultiplierNone, multiplier: "999", productMax: "75000",
			shareCapital: "999", bosa: "999", fosa: "999",
			bosaFosaEnabled: true,
			wantCeiling:     "75000",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ScoringInputs{
				ShareCapital: decimal.RequireFromString(c.shareCapital),
				BosaBalance:  decimal.RequireFromString(c.bosa),
				FosaBalance:  decimal.RequireFromString(c.fosa),
			}
			product := newMultiplierProduct(c.basis, c.multiplier, c.productMax)
			ceiling, warn := computeMultiplierCeiling(in, product, c.bosaFosaEnabled)
			want := decimal.RequireFromString(c.wantCeiling)
			if !ceiling.Equal(want) {
				t.Errorf("ceiling = %s, want %s", ceiling.String(), want.String())
			}
			if c.wantWarningCode == "" {
				if warn != nil {
					t.Errorf("expected no warning, got %+v", warn)
				}
			} else {
				if warn == nil {
					t.Fatalf("expected warning code %q, got nil", c.wantWarningCode)
				}
				if warn.Code != c.wantWarningCode {
					t.Errorf("warning code = %q, want %q", warn.Code, c.wantWarningCode)
				}
				if warn.Severity != "soft_flag" {
					t.Errorf("warning severity = %q, want %q", warn.Severity, "soft_flag")
				}
			}
		})
	}
}

func TestIsLegacyMultiplierBasis(t *testing.T) {
	legacy := []LoanMultiplierBasis{MultiplierDeposits, MultiplierSharesPlusDeps}
	modern := []LoanMultiplierBasis{MultiplierNone, MultiplierShares, MultiplierBOSA, MultiplierBOSAPlusShares}
	for _, b := range legacy {
		if !b.IsLegacyMultiplierBasis() {
			t.Errorf("%q should be legacy", b)
		}
	}
	for _, b := range modern {
		if b.IsLegacyMultiplierBasis() {
			t.Errorf("%q should not be legacy", b)
		}
	}
}
