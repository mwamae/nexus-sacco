// PR 1 (BOSA / FOSA) — pure unit tests for the segment validation
// rules. These do not hit the database; they just exercise the
// DepositSegment.Valid + DepositProduct.ValidateBOSAConstraints
// invariants. The same rules are enforced again in the handler
// (defence in depth), so the live-edge test of these conditions
// also exists at the HTTP layer.

package domain

import "testing"

func TestDepositSegmentValid(t *testing.T) {
	cases := []struct {
		in   DepositSegment
		want bool
	}{
		{SegmentBOSA, true},
		{SegmentFOSA, true},
		{"", false},
		{"unknown", false},
		{"BOSA", false}, // case-sensitive
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("DepositSegment(%q).Valid() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidateBOSAConstraints(t *testing.T) {
	cases := []struct {
		name             string
		segment          DepositSegment
		partialAllowed   bool
		noticePeriodDays int
		wantErr          bool
		wantErrSubstr    string
	}{
		{
			name:    "FOSA + partial withdrawals OK",
			segment: SegmentFOSA, partialAllowed: true,
		},
		{
			name:    "FOSA + notice period OK",
			segment: SegmentFOSA, noticePeriodDays: 30,
		},
		{
			name:    "BOSA + partial=false + notice=0 OK",
			segment: SegmentBOSA, partialAllowed: false, noticePeriodDays: 0,
		},
		{
			name:           "BOSA + partial=true rejected",
			segment:        SegmentBOSA, partialAllowed: true,
			wantErr: true, wantErrSubstr: "partial",
		},
		{
			name:             "BOSA + notice>0 rejected",
			segment:          SegmentBOSA, noticePeriodDays: 15,
			wantErr: true, wantErrSubstr: "notice",
		},
		{
			// Both invalid; we accept either complaint — the rule
			// is "one of these failed", not "all failures
			// reported".
			name:           "BOSA + both invalid rejected",
			segment:        SegmentBOSA, partialAllowed: true, noticePeriodDays: 7,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &DepositProduct{
				Segment:                  c.segment,
				PartialWithdrawalAllowed: c.partialAllowed,
				NoticePeriodDays:         c.noticePeriodDays,
			}
			err := p.ValidateBOSAConstraints()
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErrSubstr != "" && err != nil && !containsLower(err.Error(), c.wantErrSubstr) {
				t.Errorf("error %q does not mention %q", err.Error(), c.wantErrSubstr)
			}
		})
	}
}

func containsLower(haystack, needle string) bool {
	// Tiny case-insensitive substring match. Avoids pulling in strings
	// to keep the dep surface flat.
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := 0; j < len(needle); j++ {
			a := haystack[i+j]
			b := needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
