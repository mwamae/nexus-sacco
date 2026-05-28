// loan_settle terminal callback.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LoanSettleRunner interface {
	RunSettleTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewLoanSettleCallback(runner LoanSettleRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewLoanSettleCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "loan_settle")
		if err != nil {
			return err
		}
		txnID, err := runner.RunSettleTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("loan_settle executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
