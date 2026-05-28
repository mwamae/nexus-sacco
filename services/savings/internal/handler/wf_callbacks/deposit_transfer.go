// cash_account_transfer terminal callback.
//
// Replaces case domain.ApprovalKindDepositTransfer of
// pending_approvals.executePayloadTx. Inter-account transfer between
// two of the same member's savings accounts; the executor produces a
// FROM + TO transaction pair sharing a transfer-pair id. We return
// the FROM txn id to the receipt-line linkage path.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DepTransferRunner interface {
	RunDepTransferTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewCashAccountTransferCallback(runner DepTransferRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewCashAccountTransferCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "cash_account_transfer")
		if err != nil {
			return err
		}
		txnID, err := runner.RunDepTransferTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("cash_account_transfer executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
