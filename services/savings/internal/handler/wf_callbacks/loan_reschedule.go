// loan_reschedule terminal callback. No GL leg.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LoanRescheduleRunner interface {
	RunRescheduleTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewLoanRescheduleCallback(runner LoanRescheduleRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewLoanRescheduleCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "loan_reschedule")
		if err != nil {
			return err
		}
		if _, err := runner.RunRescheduleTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID); err != nil {
			return fmt.Errorf("loan_reschedule executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
	}
}
