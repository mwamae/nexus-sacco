// cash_withdrawal terminal callback.
//
// Replaces case domain.ApprovalKindWithdrawal of pending_approvals
// .executePayloadTx. Same structure as the cash_deposit callback —
// the only delta is which Run method on the handler gets called.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WithdrawalRunner is the narrow interface the cash_withdrawal
// callback needs. Satisfied implicitly by *handler.DepositHandler.
type WithdrawalRunner interface {
	RunWithdrawalTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewCashWithdrawalCallback(runner WithdrawalRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewCashWithdrawalCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "cash_withdrawal")
		if err != nil {
			return err
		}
		txnID, err := runner.RunWithdrawalTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("cash_withdrawal executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
