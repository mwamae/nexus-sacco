// PR 3 (BOSA / FOSA CoA reorg) — unit tests for the segment-first GL
// mapping. Pure function, no DB, no fixtures.
//
// The rules under test:
//
//   1. BOSA segment always maps to 2050 regardless of product_type.
//   2. FOSA segment routes to the existing 2000-range mapping.
//   3. The previously-fall-through 'group' product type now has an
//      explicit case (correction #5 in the Step 0 brief).
//   4. Unknown product type defaults to 2000.

package handler

import (
	"testing"

	"github.com/nexussacco/savings/internal/domain"
)

func TestDepositLiabilityCode(t *testing.T) {
	cases := []struct {
		segment domain.DepositSegment
		typ     domain.DepositProductType
		want    string
	}{
		// BOSA: segment wins over product_type.
		{domain.SegmentBOSA, domain.ProductMemberDeposit, "2050"},
		{domain.SegmentBOSA, domain.ProductOrdinary, "2050"}, // hypothetical mis-tagged, still safe
		{domain.SegmentBOSA, "", "2050"},

		// FOSA: product_type-driven mapping (the legacy table).
		{domain.SegmentFOSA, domain.ProductOrdinary, "2000"},
		{domain.SegmentFOSA, domain.ProductHoliday, "2010"},
		{domain.SegmentFOSA, domain.ProductEmergency, "2020"},
		{domain.SegmentFOSA, domain.ProductGoal, "2030"},
		{domain.SegmentFOSA, domain.ProductJunior, "2040"},
		{domain.SegmentFOSA, domain.ProductFixed, "2100"},

		// Group: pooled FOSA; treated as ordinary for GL purposes.
		// Used to silently fall through; now an explicit case.
		{domain.SegmentFOSA, domain.ProductGroup, "2000"},

		// Mis-tagged: FOSA segment + member_deposit type would be a
		// data-quality bug. We bias toward 2050 (BOSA line) so the
		// misclassification is visible in reconciliation rather than
		// silently posting member-bond money to Ordinary Savings.
		{domain.SegmentFOSA, domain.ProductMemberDeposit, "2050"},

		// Unknown product type falls back to 2000.
		{domain.SegmentFOSA, "unknown_type", "2000"},
	}
	for _, c := range cases {
		t.Run(string(c.segment)+"/"+string(c.typ), func(t *testing.T) {
			if got := depositLiabilityCode(c.segment, c.typ); got != c.want {
				t.Errorf("depositLiabilityCode(%q, %q) = %q, want %q", c.segment, c.typ, got, c.want)
			}
		})
	}
}
