// standing_order_resume terminal callback (Phase 2.2). On approve,
// flips a paused/suspended recurring_deposits row back to active and
// resets consecutive_failures so the next processor tick re-attempts.

package wf_callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func NewStandingOrderResumeCallback() Callback {
	return func(ctx context.Context, tx pgx.Tx, inst Instance) error {
		if inst.Status != "approved" {
			return nil
		}
		var ctxObj struct {
			StandingOrderID string `json:"standing_order_id"`
		}
		if err := json.Unmarshal([]byte(inst.Context), &ctxObj); err != nil {
			return fmt.Errorf("standing_order_resume: parse context: %w", err)
		}
		id, err := uuid.Parse(ctxObj.StandingOrderID)
		if err != nil {
			return fmt.Errorf("standing_order_resume: invalid id: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE recurring_deposits
			   SET status              = 'active',
			       consecutive_failures = 0,
			       next_run_at         = GREATEST(next_run_at, now()),
			       updated_at          = now()
			 WHERE id = $1 AND status IN ('paused', 'suspended')
		`, id)
		if err != nil {
			return fmt.Errorf("standing_order_resume: flip: %w", err)
		}
		return nil
	}
}
