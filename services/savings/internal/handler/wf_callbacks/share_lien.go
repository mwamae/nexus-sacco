// share_lien terminal callback. Lien isn't a ledger txn — the
// returned txnID is uuid.Nil so any linked receipt line flips to
// posted with NULL posted_txn_id.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ShareLienRunner interface {
	RunShareLienTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error)
}

func NewShareLienCallback(runner ShareLienRunner, receipts ReceiptLineUpdater) Callback {
	if runner == nil {
		panic("wf_callbacks.NewShareLienCallback: runner is required")
	}
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
		}
		makerID, err := requireMaker(inst, "share_lien")
		if err != nil {
			return err
		}
		if _, err := runner.RunShareLienTx(ctx, tx, inst.TenantID, []byte(inst.Context), makerID); err != nil {
			return fmt.Errorf("share_lien executor: %w", err)
		}
		return propagateReceiptLine(ctx, tx, receipts, inst, uuid.Nil)
	}
}
