// fee_posting + welfare_posting terminal callbacks. The two kinds
// share an executor — same payload shape, same business logic — so
// the same Runner is registered under both process_kinds in main.go.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FeePostingRunner interface {
	RunFeePostingTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewFeePostingCallback(runner FeePostingRunner, receipts ReceiptLineUpdater, label string) Callback {
	if runner == nil {
		panic("wf_callbacks.NewFeePostingCallback: runner is required")
	}
	if label == "" {
		label = "fee_posting"
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, label)
		if err != nil {
			return err
		}
		txnID, err := runner.RunFeePostingTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("%s executor: %w", label, err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
