// Unit tests for the Unified Inbox additions in status.go (PR #5).
//
// processKindForTarget is a pure mapping that routes each target
// status to the seeded workflow definition kind. Bug here would
// land every variant on the wrong level structure (e.g. a blacklist
// hitting a Reviewer-only definition that should require Board
// quorum=all). Cheap to pin.

package handler

import (
	"testing"

	"github.com/nexussacco/member/internal/domain"
)

func TestProcessKindForTarget(t *testing.T) {
	cases := []struct {
		target domain.MemberStatus
		want   string
	}{
		{domain.StatusBlacklisted, "member_blacklist"},
		{domain.StatusExited, "member_close"},
		{domain.StatusDeceased, "member_close"},
		{domain.StatusActive, "member_reactivate"},
		{domain.StatusSuspended, "member_status_change"},
		// Unmapped statuses fall back to "" — the caller substitutes
		// the umbrella "member_status_change" so the legacy path still
		// works. We don't enumerate every transition; one negative
		// case is enough to pin the contract.
		{domain.StatusDormant, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.target), func(t *testing.T) {
			got := processKindForTarget(tc.target)
			if got != tc.want {
				t.Errorf("processKindForTarget(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}
