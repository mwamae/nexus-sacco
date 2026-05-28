// loan_moratorium terminal callback. No GL leg.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LoanMoratoriumRunner interface {
	RunMoratoriumTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewLoanMoratoriumCallback(runner LoanMoratoriumRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewLoanMoratoriumCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "loan_moratorium")
		if err != nil {
			return err
		}
		if _, err := runner.RunMoratoriumTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID); err != nil {
			return fmt.Errorf("loan_moratorium executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
	}
}
