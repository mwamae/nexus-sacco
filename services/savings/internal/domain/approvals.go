// Pending approvals — maker-checker queue for cash-handling actions
// (Phase 7b). When the tenant has the per-kind toggle enabled, the
// original handler inserts a pending row and a second user must
// approve before the action posts to the ledger.

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type ApprovalKind string

const (
	// Stage 1 — deposits sub-module.
	ApprovalKindDeposit         ApprovalKind = "deposit"
	ApprovalKindWithdrawal      ApprovalKind = "withdrawal"
	ApprovalKindDepositTransfer ApprovalKind = "deposit_transfer"

	// Future stages — listed here so the dispatcher fails closed rather
	// than silently allowing an unknown kind. Wired in stages 2-3.
	ApprovalKindSharePurchase          ApprovalKind = "share_purchase"
	ApprovalKindShareTransfer          ApprovalKind = "share_transfer"
	ApprovalKindShareBonus             ApprovalKind = "share_bonus"
	ApprovalKindShareLien              ApprovalKind = "share_lien"
	ApprovalKindLoanDisbursement       ApprovalKind = "loan_disbursement"
	ApprovalKindLoanRepayment          ApprovalKind = "loan_repayment"
	ApprovalKindLoanSettle             ApprovalKind = "loan_settle"
	ApprovalKindLoanReverse            ApprovalKind = "loan_reverse"
	ApprovalKindLoanWriteoff           ApprovalKind = "loan_writeoff"
	ApprovalKindLoanReschedule         ApprovalKind = "loan_reschedule"
	ApprovalKindLoanMoratorium         ApprovalKind = "loan_moratorium"
	ApprovalKindLoanSettlementDiscount ApprovalKind = "loan_settlement_discount"
	// BOSA exit (member refund). Withdrawals are forbidden on BOSA
	// accounts via the normal /withdraw handler; officers route the
	// refund through /v1/bosa/exit, which queues an approval of this
	// kind. The executor is intentionally not wired in PR 1 —
	// approval just sits pending until a later PR ships the exit
	// ledger posting.
	ApprovalKindBOSAExit ApprovalKind = "member_bosa_exit"
)

func (k ApprovalKind) Valid() bool {
	switch k {
	case ApprovalKindDeposit, ApprovalKindWithdrawal, ApprovalKindDepositTransfer,
		ApprovalKindSharePurchase, ApprovalKindShareTransfer, ApprovalKindShareBonus, ApprovalKindShareLien,
		ApprovalKindLoanDisbursement, ApprovalKindLoanRepayment, ApprovalKindLoanSettle, ApprovalKindLoanReverse,
		ApprovalKindLoanWriteoff, ApprovalKindLoanReschedule, ApprovalKindLoanMoratorium,
		ApprovalKindLoanSettlementDiscount, ApprovalKindBOSAExit:
		return true
	}
	return false
}

type ApprovalStatus string

const (
	ApprovalStatusPending   ApprovalStatus = "pending"
	ApprovalStatusApproved  ApprovalStatus = "approved"
	ApprovalStatusDeclined  ApprovalStatus = "declined"
	ApprovalStatusCancelled ApprovalStatus = "cancelled"
	ApprovalStatusExecError ApprovalStatus = "execution_error"
)

type PendingApproval struct {
	ID               uuid.UUID         `json:"id"`
	TenantID         uuid.UUID         `json:"tenant_id"`
	Kind             ApprovalKind      `json:"kind"`
	Status           ApprovalStatus    `json:"status"`
	Title            string            `json:"title"`
	SubjectMemberID  *uuid.UUID        `json:"subject_member_id,omitempty"`
	SubjectAccountID *uuid.UUID        `json:"subject_account_id,omitempty"`
	SubjectLoanID    *uuid.UUID        `json:"subject_loan_id,omitempty"`
	Amount           *decimal.Decimal  `json:"amount,omitempty"`
	Payload          []byte            `json:"payload"`
	MakerUserID      uuid.UUID         `json:"maker_user_id"`
	MakerAt          time.Time         `json:"maker_at"`
	MakerNote        *string           `json:"maker_note,omitempty"`
	CheckerUserID    *uuid.UUID        `json:"checker_user_id,omitempty"`
	CheckerAt        *time.Time        `json:"checker_at,omitempty"`
	CheckerNote      *string           `json:"checker_note,omitempty"`
	ResultTxnID      *uuid.UUID        `json:"result_txn_id,omitempty"`
	ResultError      *string           `json:"result_error,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	// Set when the Unified Inbox cutover migrated this row into a
	// workflow_instance. Frontend uses it to deep-link from the
	// legacy /cash-approvals page to /approvals/{id}.
	WorkflowInstanceID *uuid.UUID `json:"workflow_instance_id,omitempty"`
}

var (
	ErrApprovalNotPending  = errors.New("approval is not in pending state")
	ErrApprovalSelfDenied  = errors.New("the maker cannot approve their own request")
	ErrApprovalKindUnknown = errors.New("approval kind is not recognised")
)
