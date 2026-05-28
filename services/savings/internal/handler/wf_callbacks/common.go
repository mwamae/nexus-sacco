// Common helpers shared across every wf_callbacks/<kind>.go file.
//
// The dedupe target: every callback that may have been queued from a
// Collection Desk receipt line ends with the same pattern —
//
//   on approve : look up linked receipt line by inst.ID, mark posted
//   on decline : look up linked receipt line by inst.ID, mark declined
//
// Pulling that into propagateReceiptLine keeps the per-kind callback
// file focused on its executor body.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// propagateReceiptLine mirrors the workflow's terminal state onto
// the linked receipt line (if any). Safe to call from any callback;
// callers that never queue from a receipt line (e.g. loan_writeoff
// from the Loans report) can pass a nil updater and we skip the
// whole flow.
//
//   approve  → MarkReceiptLinePostedTx (needs the posted txn id)
//   decline  → MarkReceiptLineDeclinedTx
//   cancel   → MarkReceiptLineDeclinedTx (same UI effect — the line
//              never posted)
//
// Returns the original error wrapped with a "propagate receipt line"
// prefix so the operator triaging callback_last_error can tell where
// the failure occurred (executor vs propagation).
func propagateReceiptLine(
	ctx context.Context, tx pgx.Tx,
	updater ReceiptLineUpdater, inst Instance, postedTxnID uuid.UUID,
) error {
	if updater == nil {
		return nil
	}
	lineID, found, err := updater.FindReceiptLineByApprovalIDTx(ctx, tx, inst.ID)
	if err != nil {
		return fmt.Errorf("propagate receipt line: lookup: %w", err)
	}
	if !found {
		return nil
	}
	switch inst.Status {
	case "approved":
		if postedTxnID == uuid.Nil {
			// Some kinds (share_bonus_issue, share_transfer, lien,
			// loan_reschedule, loan_moratorium) don't produce a txn
			// id. The receipt line for those flows is also rare —
			// flip to posted with a NIL ref so the line clears the
			// pending state.
			return updater.MarkReceiptLinePostedTx(ctx, tx, lineID, uuid.Nil)
		}
		return updater.MarkReceiptLinePostedTx(ctx, tx, lineID, postedTxnID)
	case "rejected", "cancelled":
		return updater.MarkReceiptLineDeclinedTx(ctx, tx, lineID)
	default:
		// Engine sent a non-terminal status — shouldn't happen given
		// the dispatcher only fires on terminal transitions, but
		// guard so a future event type doesn't break the no-op path.
		return nil
	}
}

// requireMaker pulls the maker user id from an instance.InitiatorID,
// returning a descriptive error when missing. Every callback needs
// this for the underlying ExecuteXxxTx call's makerID arg.
func requireMaker(inst Instance, kindLabel string) (uuid.UUID, error) {
	if inst.InitiatorID == nil || *inst.InitiatorID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%s callback: instance.initiator_id is required for executor maker attribution", kindLabel)
	}
	return *inst.InitiatorID, nil
}
