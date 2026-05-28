// loan_disbursement terminal callback.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LoanDisbursementRunner interface {
	RunDisbursementTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewLoanDisbursementCallback(runner LoanDisbursementRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewLoanDisbursementCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "loan_disbursement")
		if err != nil {
			return err
		}
		txnID, err := runner.RunDisbursementTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID)
		if err != nil {
			return fmt.Errorf("loan_disbursement executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, txnID)
	}
}
