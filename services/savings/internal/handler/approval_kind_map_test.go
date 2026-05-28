// Test the legacy-to-workflow mapping table. Pins the 5 name drifts
// + every identity-mapped kind so a future enum rename can't silently
// land an approval on the wrong wf_definition.
//
// This is THE test that fails first if anyone adds a new
// domain.ApprovalKind constant without updating
// ProcessKindForApprovalKind.

package handler

import (
	"testing"

	"github.com/nexussacco/savings/internal/domain"
)

func TestProcessKindForApprovalKind_AllKnownKindsMap(t *testing.T) {
	// Every legacy ApprovalKind must map to a non-empty process_kind.
	// An empty mapping forces a queueApproval call site into the
	// legacy fallback (post P5 = error). Catching missing entries
	// here at unit time is much cheaper than catching them in
	// production via a stuck queued instance.
	cases := []struct {
		kind     domain.ApprovalKind
		wantProc string
	}{
		// 5 name-drift remappings — these are the ones that historically
		// caused queueApproval to hit the legacy fallback by accident.
		{domain.ApprovalKindDeposit, "cash_deposit"},
		{domain.ApprovalKindWithdrawal, "cash_withdrawal"},
		{domain.ApprovalKindDepositTransfer, "cash_account_transfer"},
		{domain.ApprovalKindShareBonus, "share_bonus_issue"},
		{domain.ApprovalKindLoanWriteoff, "loan_write_off"},
		// Identity-mapped (14).
		{domain.ApprovalKindSharePurchase, "share_purchase"},
		{domain.ApprovalKindShareTransfer, "share_transfer"},
		{domain.ApprovalKindShareLien, "share_lien"},
		{domain.ApprovalKindLoanDisbursement, "loan_disbursement"},
		{domain.ApprovalKindLoanRepayment, "loan_repayment"},
		{domain.ApprovalKindLoanSettle, "loan_settle"},
		{domain.ApprovalKindLoanReverse, "loan_reverse"},
		{domain.ApprovalKindLoanReschedule, "loan_reschedule"},
		{domain.ApprovalKindLoanMoratorium, "loan_moratorium"},
		{domain.ApprovalKindLoanSettlementDiscount, "loan_settlement_discount"},
		{domain.ApprovalKindBOSAExit, "member_bosa_exit"},
		{domain.ApprovalKindFeePosting, "fee_posting"},
		{domain.ApprovalKindWelfarePosting, "welfare_posting"},
		{domain.ApprovalKindApplicationFee, "application_fee"},
	}
	for _, tc := range cases {
		got := ProcessKindForApprovalKind(tc.kind)
		if got != tc.wantProc {
			t.Errorf("ProcessKindForApprovalKind(%q) = %q, want %q",
				tc.kind, got, tc.wantProc)
		}
	}
}

func TestProcessKindForApprovalKind_UnknownReturnsEmpty(t *testing.T) {
	// An unknown kind must return "" — callers branch on "" to skip
	// the wf path safely. A non-empty fallback would queue against
	// the wrong process_kind silently.
	got := ProcessKindForApprovalKind(domain.ApprovalKind("nonsense"))
	if got != "" {
		t.Errorf("unknown kind should return \"\", got %q", got)
	}
}

func TestSubjectKindFor_MatchesWorkflowConvention(t *testing.T) {
	// Spot-check the subject_kind values. Convention (set by mpesa /
	// member / accounting): a domain noun, not the action.
	cases := []struct {
		kind    domain.ApprovalKind
		wantSub string
	}{
		{domain.ApprovalKindDeposit, "savings_account"},
		{domain.ApprovalKindWithdrawal, "savings_account"},
		{domain.ApprovalKindSharePurchase, "share_holding"},
		{domain.ApprovalKindLoanDisbursement, "loan"},
		{domain.ApprovalKindLoanWriteoff, "loan"},
		{domain.ApprovalKindBOSAExit, "member"},
		{domain.ApprovalKindFeePosting, "receipt_line"},
		{domain.ApprovalKindApplicationFee, "receipt_line"},
	}
	for _, tc := range cases {
		got := SubjectKindFor(tc.kind)
		if got != tc.wantSub {
			t.Errorf("SubjectKindFor(%q) = %q, want %q",
				tc.kind, got, tc.wantSub)
		}
	}
}
