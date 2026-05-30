// deposit_account_reactivation terminal callback (Phase 2.2).
// On approve, flips the dormant deposit account to status='active'
// and stamps last_activity_at = now() so the dormancy detector
// doesn't immediately flip it back.

package wf_callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func NewDepositReactivationCallback() Callback {
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return nil
		}
		var ctxObj struct {
			AccountID string `json:"account_id"`
		}
		if err := json.Unmarshal([]byte(inst.Context), &ctxObj); err != nil {
			return fmt.Errorf("deposit_reactivation: parse context: %w", err)
		}
		accountID, err := uuid.Parse(ctxObj.AccountID)
		if err != nil {
			return fmt.Errorf("deposit_reactivation: invalid account_id: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE deposit_accounts
			   SET status            = 'active',
			       last_activity_at  = now(),
			       updated_at        = now()
			 WHERE id = $1 AND status = 'dormant'
		`, accountID)
		if err != nil {
			return fmt.Errorf("deposit_reactivation: flip status: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Either the account vanished or it was reactivated by a
			// concurrent process — neither is a hard error. Surface a
			// log line in the dispatcher's caller via a non-nil err? No
			// — silent success is correct here; the audit row at file-
			// time already records what was requested.
			return nil
		}
		return nil
	}
}
