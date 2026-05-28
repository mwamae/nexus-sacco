// cash_deposit terminal callback.
//
// Replaces the case domain.ApprovalKindDeposit branch of
// pending_approvals.executePayloadTx (lines ~594–614 before this PR).
// Runs end-to-end:
//
//   1. Decode the DepositPayload from instance.Context.
//   2. Call DepositRunner.RunDepositTx — wraps ExecuteDepositTx +
//      postDepositToGLTx so the executor tx commits both the
//      member-side transaction row and the GL posting in one shot.
//   3. If instance.Context carries a receipt_line_id, mark the
//      receipt line posted with the resulting txn id so the
//      Collection Desk's per-line UI flips from pending to posted
//      atomically with the executor.
//
// Idempotency: ExecuteDepositTx upserts on (account_id, source_ref)
// when the channel_ref is populated; the dispatcher will redeliver
// after a transient failure and the second call will recognise the
// existing transaction. Callbacks fired on non-approved terminal
// statuses (rejected / cancelled) skip the executor entirely — for
// those events we just flip the receipt line (if any) to declined.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DepositRunner is the small slice of *handler.DepositHandler the
// cash_deposit callback needs. The handler package satisfies this
// implicitly; main.go wires the concrete instance into the
// constructor below.
//
// Deliberately structured around a single RunDepositTx method
// (rather than separate ExecuteDepositTx + postDepositToGLTx
// methods on the interface) so the callback doesn't have to
// re-derive the channel arg from the payload. The handler-side
// method is a thin wrapper that mirrors what executePayloadTx
// did inline today.
type DepositRunner interface {
	// RunDepositTx decodes the raw context JSON, runs ExecuteDepositTx,
	// posts the GL via postDepositToGLTx, and returns the resulting
	// transaction id. tenantID is passed through so the GL post lands
	// on the correct tenant.
	RunDepositTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

// ReceiptLineUpdater handles the optional Collection-Desk-originated
// case. Most cash_deposit instances DON'T have a linked receipt
// line (legacy direct-deposit flow); for the ones that do, the
// callback flips the receipt line's status to match the instance's
// terminal state.
//
// Lookup is by approval_id, which is the wf_instance.ID — the
// caller (executeDepositInlineTx etc.) stores it on the receipt
// line via Receipts.AttachApprovalTx when the instance is created.
// The same pattern the legacy pending_approvals path used; only
// the source-of-truth table changes. Returning (nil, nil) for "no
// linked line" lets the callback no-op without an error.
type ReceiptLineUpdater interface {
	FindReceiptLineByApprovalIDTx(ctx context.Context, tx pgx.Tx, approvalID uuid.UUID) (lineID uuid.UUID, found bool, err error)
	MarkReceiptLinePostedTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, txnID uuid.UUID) error
	MarkReceiptLineDeclinedTx(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) error
}

// NewCashDepositCallback returns a Callback that the registry
// pins under "cash_deposit". The deposit + receipt runners are
// captured in the closure; nil deposit runner is a programmer
// error and panics at boot via the registry's nil-check.
func NewCashDepositCallback(deposit DepositRunner, receipts ReceiptLineUpdater) Callback {
	if deposit == nil {
		panic("wf_callbacks.NewCashDepositCallback: deposit runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "cash_deposit")
		if err != nil {
			return err
		}
		txnID, err := deposit.RunDepositTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("cash_deposit executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
