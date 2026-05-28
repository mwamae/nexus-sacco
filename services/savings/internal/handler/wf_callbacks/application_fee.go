// application_fee terminal callback.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ApplicationFeeRunner interface {
	RunApplicationFeeTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewApplicationFeeCallback(runner ApplicationFeeRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewApplicationFeeCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "application_fee")
		if err != nil {
			return err
		}
		jeID, err := runner.RunApplicationFeeTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("application_fee executor: %w", err)
		}
		// JE id stored as posted_txn_id on the receipt line for parity
		// with the legacy executePayloadTx behaviour.
		return propagateReceiptLine(ctx, tx, receipts, inst, jeID)
	}
}
