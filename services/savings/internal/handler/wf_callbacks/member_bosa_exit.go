// member_bosa_exit terminal callback — placeholder.
//
// The BOSA exit flow (drain BOSA balance + close account + release
// pledged shares + post equity refund JE) needs Board + Finance
// sign-off on the JE shape before the executor body is implemented.
// Until then this callback is registered for completeness — so a
// queued BOSA exit doesn't silently 404 at the dispatch endpoint —
// but it returns a structured error every time it fires.
//
// Failure path: the dispatcher records the error string in
// callback_last_error, bumps callback_attempts, and retries with
// exponential backoff. After 12 attempts the row settles to
// callback_status = 'failed:executor:<msg>'. Operators triage from
// the DLQ surface; an approved BOSA exit shows in the Inbox as
// approved but with the failure message attached.
//
// The wf_instance.status flips to 'approved' the moment the approver
// clicks Approve — the engine doesn't reverse on callback failure
// (matches the architecture's "engine and executor are decoupled"
// guarantee). The audit trail is intact even though the executor
// body is intentionally absent.

package wf_callbacks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func NewMemberBOSAExitCallback() Callback {
	return func(_ context.Context, _ pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			// Reject / cancel paths are fine — the exit didn't happen
			// so nothing to refund. No receipt-line linkage on this
			// flow (BOSA exit comes from the savings/bosa/exit endpoint,
			// not the Collection Desk).
			return nil
		}
		return fmt.Errorf(
			"member_bosa_exit terminal action: executor not yet implemented " +
				"— see services/savings/internal/handler/wf_callbacks/member_bosa_exit.go. " +
				"The exit flow (drain BOSA balance + close account + release pledged " +
				"shares + post equity refund JE) needs Board+Finance sign-off on the " +
				"JE shape before implementation.",
		)
	}
}
