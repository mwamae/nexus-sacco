// Orchestrator — ties the engine, the run-store, posting, audit,
// and the workflow client into one Process() call the distributor
// loop invokes per leased event.
//
// One tx per event. The lock is the SELECT … FOR UPDATE SKIP LOCKED
// the caller already used to lease the event. Process does NOT lease
// — it expects the event to be locked in the supplied tx.
//
// Failure handling: an error from any step inside Process unwinds
// the whole tx via the caller's defer-rollback. The caller then
// opens a SECOND tx to record the attempt failure (attempts++) so
// the next worker can retry.

package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

// HardFailAttempts is the threshold beyond which an event flips to
// status='failed' and an alert task lands in the Unified Inbox.
const HardFailAttempts = 6

// Orchestrator bundles the dependencies. One per service; safe for
// concurrent use across worker goroutines (the underlying pool +
// stores are thread-safe; per-event state lives in the supplied tx).
type Orchestrator struct {
	Events   *store.InboundEventStore
	Runs     *store.DistributionRunStore
	Balances Balances
	Audit    *store.AuditStore
	Workflow *workflowclient.Client
	Logger   *slog.Logger

	// CashAccountCode / ClearingAccountCode are tenant-static for
	// phase 3 (defer-apply). Phase 3.5 reads them from the paybill
	// + COA mapping; for now we use the platform defaults.
	CashAccountCode     string
	ClearingAccountCode string
}

// Result is what Process returns on success. nil + error means the
// caller should roll back + record an attempt failure.
type Result struct {
	EventID  uuid.UUID
	RunID    uuid.UUID
	Splits   []Split
	Leftover decimal.Decimal
}

// Process is the per-event orchestration. The eventID was leased
// (and locked) by the caller; tx is the same tx the lease ran in.
func (o *Orchestrator) Process(
	ctx context.Context, tx pgx.Tx,
	tenantID, eventID uuid.UUID,
) (*Result, error) {
	event, err := o.Events.ByIDTx(ctx, tx, eventID)
	if err != nil {
		return nil, fmt.Errorf("re-read event %s: %w", eventID, err)
	}
	if event.Status != domain.InboundReceived {
		// A replay attempt. Caller already committed; bail without
		// touching anything so the audit chain stays clean.
		return nil, fmt.Errorf("event %s status=%s, expected 'received' — skipping replay",
			eventID, event.Status)
	}

	o.auditFireAndForget(ctx, tenantID, eventID, "mpesa.distribution_run.started", map[string]any{
		"amount":       event.Amount,
		"resolved_via": viaOrEmpty(event.ResolvedVia),
	})

	// 1. Build the plan.
	plan, err := Run(ctx, tx, o.Balances, event, nil)
	if err != nil {
		return nil, fmt.Errorf("engine.Run: %w", err)
	}

	// 2. Persist the run row. Normalise nil-slice to [] so the
	// stored jsonb is always a JSON array (downstream queries +
	// the staff UI rely on jsonb_array_length being defined).
	splitsForJSON := plan.Splits
	if splitsForJSON == nil {
		splitsForJSON = []Split{}
	}
	splitsJSON, err := json.Marshal(splitsForJSON)
	if err != nil {
		return nil, fmt.Errorf("marshal splits: %w", err)
	}
	runID, err := o.Runs.CreateRunTx(ctx, tx, store.CreateRunInput{
		TenantID:            tenantID,
		InboundEventID:      eventID,
		ResolvedMemberID:    event.ResolvedMemberID,
		ResolvedVia:         viaOrEmpty(event.ResolvedVia),
		Amount:              plan.Amount,
		Splits:              splitsJSON,
		CashAccountCode:     o.CashAccountCode,
		ClearingAccountCode: o.ClearingAccountCode,
	})
	if err != nil {
		return nil, fmt.Errorf("persist distribution run: %w", err)
	}

	// 3. Emit per-split audit entries — one per Split so the audit
	// log is the canonical replayable record of "who got what".
	for _, sp := range plan.Splits {
		o.auditFireAndForget(ctx, tenantID, eventID, "mpesa.distribution_run.split", map[string]any{
			"run_id":     runID,
			"leg":        string(sp.Leg),
			"amount":     sp.Amount.StringFixed(2),
			"target_ref": sp.TargetRef,
			"target_id":  sp.TargetID,
		})
	}

	// 4. Post the cash leg to posting_outbox. Always — even when
	// the plan has zero splits (unallocated). The clearing account
	// is the right home for parked-but-unallocated money; the
	// reconciliation workflow task handles the rest.
	valueDate := time.Now().UTC()
	if event.TransactionTime != nil {
		valueDate = *event.TransactionTime
	}
	outboxID, err := PostCashLegTx(ctx, tx, tenantID, eventID,
		plan.Amount, o.CashAccountCode, o.ClearingAccountCode, valueDate)
	if err != nil {
		return nil, fmt.Errorf("post cash leg: %w", err)
	}

	// 5. Mark the run + the event as completed.
	if err := o.Runs.MarkRunPostedTx(ctx, tx, runID, &outboxID); err != nil {
		return nil, fmt.Errorf("mark run posted: %w", err)
	}
	if err := o.Runs.MarkDistributedTx(ctx, tx, eventID, runID); err != nil {
		return nil, fmt.Errorf("mark event distributed: %w", err)
	}

	o.auditFireAndForget(ctx, tenantID, eventID, "mpesa.distribution_run.completed", map[string]any{
		"run_id":      runID,
		"splits":      len(plan.Splits),
		"leftover":    plan.Leftover.StringFixed(2),
		"outbox_id":   outboxID,
	})

	return &Result{
		EventID: eventID, RunID: runID,
		Splits: plan.Splits, Leftover: plan.Leftover,
	}, nil
}

// RecordFailure runs in a FRESH tx after Process returned an error.
// Increments attempts, stamps error_text, hard-fails + raises an
// alert workflow task when HardFailAttempts is reached.
func (o *Orchestrator) RecordFailure(
	ctx context.Context, tx pgx.Tx,
	tenantID, eventID uuid.UUID, cause error,
) error {
	attempts, err := o.Runs.RecordAttemptFailureTx(ctx, tx, eventID, cause.Error())
	if err != nil {
		return err
	}
	if attempts < HardFailAttempts {
		o.Logger.Warn("mpesa distributor: attempt failed, will retry",
			"event_id", eventID, "attempts", attempts, "err", cause)
		return nil
	}
	// Hard-fail path.
	if err := o.Runs.MarkEventFailedTx(ctx, tx, eventID); err != nil {
		return err
	}
	if o.Workflow != nil {
		_, err := o.Workflow.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
			TenantID:    tenantID,
			ProcessKind: "mpesa_unallocated_reconciliation",
			SubjectKind: "mpesa_inbound_event",
			SubjectID:   eventID,
			Summary:     fmt.Sprintf("M-PESA distribution hard-failed after %d attempts", attempts),
			SourceURL:   "/accounting/mpesa-reconciliation?event=" + eventID.String(),
			Context: map[string]any{
				"event_id":    eventID,
				"attempts":    attempts,
				"last_error":  cause.Error(),
				"distributor": "phase3",
			},
		})
		if err != nil && !errors.Is(err, workflowclient.ErrDefinitionNotFound) {
			o.Logger.Error("create alert task", "err", err, "event_id", eventID)
		}
	}
	o.auditFireAndForget(ctx, tenantID, eventID, "mpesa.distribution_run.failed", map[string]any{
		"attempts": attempts,
		"error":    cause.Error(),
	})
	return nil
}

// auditFireAndForget never blocks the distributor on the audit
// table's availability — a logging failure is logged but the
// distribution tx commits regardless.
func (o *Orchestrator) auditFireAndForget(
	ctx context.Context, tenantID, eventID uuid.UUID,
	action string, meta map[string]any,
) {
	if o.Audit == nil {
		return
	}
	t := tenantID
	if err := o.Audit.Write(ctx, store.AuditEntry{
		TenantID:   &t,
		Action:     action,
		TargetKind: "mpesa_inbound_event",
		TargetID:   eventID.String(),
		Metadata:   meta,
	}); err != nil {
		o.Logger.Warn("audit write failed", "action", action, "err", err)
	}
}

func viaOrEmpty(v *domain.ResolvedVia) string {
	if v == nil {
		return ""
	}
	return string(*v)
}
