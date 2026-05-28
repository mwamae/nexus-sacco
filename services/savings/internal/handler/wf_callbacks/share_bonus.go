// share_bonus_issue terminal callback. Bonus issue impacts many
// ledger rows, no single posted txn id. The receipt-line propagation
// flips any linked line to posted with NULL posted_txn_id (rare in
// practice — bonus issues are admin-driven, not desk-driven).

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ShareBonusRunner interface {
	RunShareBonusTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewShareBonusCallback(runner ShareBonusRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewShareBonusCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "share_bonus_issue")
		if err != nil {
			return err
		}
		if _, err := runner.RunShareBonusTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID); err != nil {
			return fmt.Errorf("share_bonus_issue executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
	}
}
