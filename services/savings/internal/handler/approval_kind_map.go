// approval_kind_map.go — translation table from the legacy
// domain.ApprovalKind enum (services/savings/internal/domain/approvals.go)
// to the canonical wf_definitions.process_kind values seeded in
// workflow migrations 0003 + 0010.
//
// The mapping isn't 1:1 — 5 of the 19 legacy kinds had names that
// drifted from the process_kind seeds. Keep this file as the single
// source of truth: every Approvals.QueueTx call site reads from
// here when deciding whether to queue under the workflow engine or
// fall back to the legacy pending_approvals path.
//
// When this PR's migration completes for every tenant the fallback
// goes away and this file becomes the only path. The map stays —
// other callers (the legacy-approvals-migrate backfill, audit
// readers) still need it.

package handler

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/nexussacco/savings/internal/domain"
)

// processKindForApprovalKind returns the workflow process_kind that
// gates the given legacy approval kind. Five of the 19 enum values
// have name drift from the wf seeds:
//
//   deposit            → cash_deposit
//   withdrawal         → cash_withdrawal
//   deposit_transfer   → cash_account_transfer
//   share_bonus        → share_bonus_issue
//   loan_writeoff      → loan_write_off
//
// The other 14 are identity-mapped. An unknown kind returns "" —
// callers treat that as "skip the workflow path, fall back to
// legacy" so a new ApprovalKind added without a corresponding map
// entry can't accidentally land in the wrong bucket.
func ProcessKindForApprovalKind(k domain.ApprovalKind) string {
	switch k {
	case domain.ApprovalKindDeposit:
		return "cash_deposit"
	case domain.ApprovalKindWithdrawal:
		return "cash_withdrawal"
	case domain.ApprovalKindDepositTransfer:
		return "cash_account_transfer"
	case domain.ApprovalKindSharePurchase:
		return "share_purchase"
	case domain.ApprovalKindShareTransfer:
		return "share_transfer"
	case domain.ApprovalKindShareBonus:
		return "share_bonus_issue"
	case domain.ApprovalKindShareLien:
		return "share_lien"
	case domain.ApprovalKindLoanDisbursement:
		return "loan_disbursement"
	case domain.ApprovalKindLoanRepayment:
		return "loan_repayment"
	case domain.ApprovalKindLoanSettle:
		return "loan_settle"
	case domain.ApprovalKindLoanReverse:
		return "loan_reverse"
	case domain.ApprovalKindLoanWriteoff:
		return "loan_write_off"
	case domain.ApprovalKindLoanReschedule:
		return "loan_reschedule"
	case domain.ApprovalKindLoanMoratorium:
		return "loan_moratorium"
	case domain.ApprovalKindLoanSettlementDiscount:
		return "loan_settlement_discount"
	case domain.ApprovalKindBOSAExit:
		return "member_bosa_exit"
	case domain.ApprovalKindFeePosting:
		return "fee_posting"
	case domain.ApprovalKindWelfarePosting:
		return "welfare_posting"
	case domain.ApprovalKindApplicationFee:
		return "application_fee"
	}
	return ""
}

// subjectKindFor returns the wf_instances.subject_kind value to use
// for a given legacy approval kind. The convention (set by existing
// callers in mpesa / member / accounting) is a domain noun: "account",
// "loan", "member", etc. — not the action name. Approvers see this in
// the Inbox's "Re:" column.
func SubjectKindFor(k domain.ApprovalKind) string {
	switch k {
	case domain.ApprovalKindDeposit, domain.ApprovalKindWithdrawal, domain.ApprovalKindDepositTransfer:
		return "savings_account"
	case domain.ApprovalKindSharePurchase, domain.ApprovalKindShareTransfer,
		domain.ApprovalKindShareBonus, domain.ApprovalKindShareLien:
		return "share_holding"
	case domain.ApprovalKindLoanDisbursement, domain.ApprovalKindLoanRepayment,
		domain.ApprovalKindLoanSettle, domain.ApprovalKindLoanReverse,
		domain.ApprovalKindLoanWriteoff, domain.ApprovalKindLoanReschedule,
		domain.ApprovalKindLoanMoratorium, domain.ApprovalKindLoanSettlementDiscount:
		return "loan"
	case domain.ApprovalKindBOSAExit:
		return "member"
	case domain.ApprovalKindFeePosting, domain.ApprovalKindWelfarePosting,
		domain.ApprovalKindApplicationFee:
		return "receipt_line"
	}
	return "savings_approval"
}

// sourceURLFor produces the deep-link the Approvals Inbox renders
// next to the summary so the approver can open the underlying record
// (member profile, loan detail, etc.) in a new tab. Returns "" when
// no canonical detail page exists for the subject.
func SourceURLFor(k domain.ApprovalKind, subjectID uuid.UUID) string {
	if subjectID == uuid.Nil {
		return ""
	}
	switch SubjectKindFor(k) {
	case "savings_account":
		return fmt.Sprintf("/deposits/%s", subjectID)
	case "share_holding":
		return fmt.Sprintf("/shares/%s", subjectID)
	case "loan":
		return fmt.Sprintf("/loans/%s", subjectID)
	case "member":
		return fmt.Sprintf("/members/%s", subjectID)
	case "receipt_line":
		return fmt.Sprintf("/collect/receipts?line=%s", subjectID)
	}
	return ""
}
