// wf_callbacks — terminal-action executors for cash kinds that
// queue under the workflow engine. The workflow service's
// callback-dispatcher POSTs to savings'
// /internal/v1/workflow-terminal-action endpoint when an instance
// reaches a terminal state; that endpoint looks up the registered
// callback for instance.process_kind and runs it inside a savings
// WithTenantTx.
//
// One Go file per process_kind:
//
//   deposit.go               cash_deposit
//   withdrawal.go            cash_withdrawal           (Slice 2)
//   deposit_transfer.go      cash_account_transfer    (Slice 2)
//   share_purchase.go        share_purchase           (Slice 2)
//   share_transfer.go        share_transfer           (Slice 2)
//   share_bonus.go           share_bonus_issue        (Slice 2)
//   share_lien.go            share_lien               (Slice 2)
//   loan_disbursement.go     loan_disbursement        (Slice 2)
//   loan_repayment.go        loan_repayment           (Slice 2)
//   loan_settle.go           loan_settle              (Slice 2)
//   loan_reverse.go          loan_reverse             (Slice 2)
//   loan_writeoff.go         loan_write_off           (Slice 2)
//   loan_reschedule.go       loan_reschedule          (Slice 2)
//   loan_moratorium.go       loan_moratorium          (Slice 2)
//   loan_settlement_discount.go                       (Slice 2)
//   fee_posting.go           fee_posting              (Slice 2)
//   welfare_posting.go       welfare_posting          (Slice 2)
//   application_fee.go       application_fee          (Slice 2)
//   member_bosa_exit.go      member_bosa_exit         (P3c — placeholder)
//
// Each file exports a constructor `NewXxxCallback(runner) Callback`
// where `runner` is a small interface declared in that same file —
// only the methods the callback actually calls. The savings handler
// package satisfies the interfaces implicitly; main.go wires the
// concrete handlers to the registry at boot. This shape avoids the
// circular import handler ↔ wf_callbacks while keeping the call-site
// code in the existing handler package.

package wf_callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Instance is the slim wf_instance shape the dispatcher POSTs to
// savings. We don't import workflow's domain.Instance because the
// workflow service runs in its own process; this is the on-the-wire
// JSON contract instead.
type Instance struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	ProcessKind string          `json:"process_kind"`
	SubjectKind string          `json:"subject_kind"`
	SubjectID   uuid.UUID       `json:"subject_id"`
	Status      string          `json:"status"`
	Context     json.RawMessage `json:"context"`
	InitiatorID *uuid.UUID      `json:"initiator_id,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// Callback is the per-kind executor signature. The endpoint opens
// the tx; the callback runs business logic inside it. A non-nil
// return rolls back the tx and the endpoint surfaces a 5xx, which
// the dispatcher reads as "retry with exponential backoff".
type Callback func(ctx context.Context, tx pgx.Tx, inst Instance) error

// Registry maps process_kind → Callback. Constructed once at boot,
// read concurrently by the dispatch endpoint. Safe for concurrent
// reads because Register is only called during boot.
type Registry struct {
	cbs map[string]Callback
}

func NewRegistry() *Registry {
	return &Registry{cbs: map[string]Callback{}}
}

// Register pins a callback for the given process_kind. Panics on
// double-registration so a copy-paste duplicate at boot crashes
// the server rather than silently picking one — failure to register
// is something we want to find at process start, not at the first
// approval after a rollout.
func (r *Registry) Register(processKind string, cb Callback) {
	if processKind == "" {
		panic("wf_callbacks: Register called with empty process_kind")
	}
	if cb == nil {
		panic("wf_callbacks: Register called with nil callback for " + processKind)
	}
	if _, exists := r.cbs[processKind]; exists {
		panic("wf_callbacks: duplicate registration for " + processKind)
	}
	r.cbs[processKind] = cb
}

// Lookup returns the registered callback for a process_kind. Returns
// false if none is registered — the dispatch endpoint translates
// that into 404 so the dispatcher hard-fails the row (rather than
// retrying forever for a kind savings doesn't know about).
func (r *Registry) Lookup(processKind string) (Callback, bool) {
	cb, ok := r.cbs[processKind]
	return cb, ok
}

// Kinds returns every registered process_kind. Used by the boot-
// time logger so an operator can read the registered set at startup.
func (r *Registry) Kinds() []string {
	out := make([]string, 0, len(r.cbs))
	for k := range r.cbs {
		out = append(out, k)
	}
	return out
}

// decodeContext is a small JSON helper used by every callback. The
// uniform error message helps an operator triage a failed callback
// from callback_last_error.
func decodeContext[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, fmt.Errorf("wf_callbacks: instance.context is empty")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("wf_callbacks: decode context as %T: %w", out, err)
	}
	return out, nil
}
