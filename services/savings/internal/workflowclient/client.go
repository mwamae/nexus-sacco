// workflowclient — thin shared-DB writer that creates a wf_instance
// from inside a savings-side tx. Companion to the mpesa workflowclient
// (services/mpesa/internal/workflowclient/client.go); the two are
// intentionally near-duplicates because the only thing that varies
// per caller is which CreateInstanceInput fields get filled in.
//
// Why shared-DB instead of HTTP: cross-service writes in this monorepo
// use shared-DB inserts inside the caller's existing tx. Doing the
// wf_instance create inside the same WithTenantTx the savings handler
// already opened guarantees that "queue receipt row exists" ⇔
// "workflow instance exists" — no half-states where the receipt is
// drafted but the instance never lands.
//
// What flows the other way (workflow → savings) DOES use HTTP — the
// callback dispatcher in workflow's cmd/callback-dispatcher fires
// POSTs to savings' /internal/v1/workflow-terminal-action when an
// instance reaches a terminal state. That direction is asynchronous
// by design (the approver's API call shouldn't block on savings'
// availability), and the dispatcher's outbox-on-the-row pattern gives
// it the same retry-and-DLQ semantics the posting-dispatcher uses.

package workflowclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Client is stateless. Held on handler structs as a pointer so the
// constructor can rebuild it under test without touching the
// handler's other fields.
type Client struct{}

func New() *Client { return &Client{} }

// CreateInstanceInput is the minimum a savings handler hands over.
// Fields mirror workflow's createInstanceReq but typed in Go because
// the create runs as a direct insert rather than an HTTP round trip.
type CreateInstanceInput struct {
	TenantID    uuid.UUID
	ProcessKind string
	SubjectKind string
	SubjectID   uuid.UUID

	// Context is the original request payload (or a normalised version
	// of it) — copied verbatim into wf_instances.context. The terminal
	// callback receives the full instance including this map and
	// replays the action from it deterministically. Keep the shape
	// stable across versions; downstream callbacks must tolerate
	// missing keys when older instances are re-delivered after a
	// schema bump.
	Context map[string]any

	// CallbackURL is the savings-side internal endpoint the workflow
	// dispatcher POSTs to when the instance reaches a terminal state.
	// Almost always "<SAVINGS_SELF_URL>/internal/v1/workflow-terminal-action".
	// Left optional so a test can fire-and-forget without wiring the
	// dispatcher; non-test code should always populate it.
	CallbackURL string

	// MakerUserID is the user who triggered the workflow (cashier who
	// captured the deposit, etc.). Stored as wf_instances.initiator_id;
	// the engine uses it for "can the initiator cancel" semantics and
	// for the maker-checker segregation check (the same user can't
	// approve their own instance unless approval_allow_self is on).
	MakerUserID uuid.UUID

	// Summary + SourceURL are surfaced verbatim in the Approvals Inbox
	// list. Summary should be a one-liner like "Deposit to a/c 1234"
	// that an approver can act on without opening the row.
	Summary   string
	SourceURL string
}

// levelState mirrors workflow's domain.LevelState — exactly the
// columns wf_instances.levels stores as JSON. Stays a local type to
// avoid the savings module depending on workflow's domain package
// (the workflow service runs in its own process; this client only
// reads/writes the shared schema).
type levelState struct {
	Order           int      `json:"order"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	ApproverRoles   []string `json:"approver_roles"`
	ApproverUserIDs []string `json:"approver_user_ids"`
	Quorum          string   `json:"quorum"`
	Condition       any      `json:"condition,omitempty"`
	SLAHours        *int     `json:"sla_hours,omitempty"`
	EscalationRole  string   `json:"escalation_role,omitempty"`
	EscalationUser  string   `json:"escalation_user,omitempty"`
}

// ErrDefinitionNotFound is returned when no active wf_definition
// exists for (tenant_id, process_kind). Callers branch on this
// to fall back to the legacy pending_approvals path (during the
// migration window) or to inline-post (once the legacy path is gone).
var ErrDefinitionNotFound = errors.New("workflowclient: no active workflow definition for the given process_kind")

// HasActiveDefinitionTx is the cheap "should I queue through the
// workflow engine?" probe callers use to decide between the new
// workflow path and the legacy fallback. Read-only single-row
// SELECT; safe to call inside any tx.
func (c *Client) HasActiveDefinitionTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, processKind string) bool {
	if tenantID == uuid.Nil || processKind == "" {
		return false
	}
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wf_definitions
			 WHERE tenant_id = $1 AND process_kind = $2 AND active = true
		)
	`, tenantID, processKind).Scan(&exists)
	if err != nil {
		// Pessimistic: any DB-level surprise → assume no definition
		// and let the caller fall back to the legacy path. Surfacing
		// the error would force every caller into an error-handling
		// branch for a code path that's purely a "should I queue
		// where?" check.
		return false
	}
	return exists
}

// CreateInstanceTx writes a wf_instances row + snapshots wf_levels
// into it. Caller must already be inside WithTenantTx for in.TenantID
// (so app.tenant_id is set). Returns the new instance id.
//
// Sets callback_status='pending' inline when CallbackURL is populated
// so the dispatcher picks it up immediately on terminal transition —
// the engine's Action handler only flips the status when an approval
// lands; setting it at creation handles the "all levels auto-skip,
// instance lands terminal at create time" path. The Create-time
// markPending is harmless when the instance starts non-terminal
// because the dispatcher predicates on status IN ('approved',…).
func (c *Client) CreateInstanceTx(ctx context.Context, tx pgx.Tx, in CreateInstanceInput) (uuid.UUID, error) {
	if in.TenantID == uuid.Nil || in.ProcessKind == "" || in.SubjectKind == "" || in.SubjectID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("workflowclient: tenant_id, process_kind, subject_kind, subject_id are required")
	}
	if in.Context == nil {
		in.Context = map[string]any{}
	}

	// 1. Resolve the active definition.
	var defID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM wf_definitions
		 WHERE tenant_id = $1 AND process_kind = $2 AND active = true
		 LIMIT 1
	`, in.TenantID, in.ProcessKind).Scan(&defID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrDefinitionNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("lookup wf_definition: %w", err)
	}

	// 2. Snapshot the levels into the JSON shape wf_instances.levels expects.
	rows, err := tx.Query(ctx, `
		SELECT level_order, name, approver_roles, approver_user_ids, quorum,
		       condition_expr, sla_hours, escalation_role, escalation_user_id
		  FROM wf_levels WHERE definition_id = $1 ORDER BY level_order
	`, defID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("snapshot levels: %w", err)
	}
	defer rows.Close()
	var levels []levelState
	var firstSLA *int
	for rows.Next() {
		var ls levelState
		var approverIDs []uuid.UUID
		var cond *string
		var sla *int
		var escRole *string
		var escUser *uuid.UUID
		if err := rows.Scan(
			&ls.Order, &ls.Name, &ls.ApproverRoles, &approverIDs, &ls.Quorum,
			&cond, &sla, &escRole, &escUser,
		); err != nil {
			return uuid.Nil, err
		}
		ls.Status = "waiting"
		for _, u := range approverIDs {
			ls.ApproverUserIDs = append(ls.ApproverUserIDs, u.String())
		}
		if cond != nil && *cond != "" && *cond != "null" {
			var cv any
			if err := json.Unmarshal([]byte(*cond), &cv); err == nil {
				ls.Condition = cv
			}
		}
		ls.SLAHours = sla
		if escRole != nil {
			ls.EscalationRole = *escRole
		}
		if escUser != nil {
			ls.EscalationUser = escUser.String()
		}
		if firstSLA == nil && sla != nil {
			firstSLA = sla
		}
		levels = append(levels, ls)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	if len(levels) == 0 {
		return uuid.Nil, fmt.Errorf("workflowclient: definition %s has no levels", defID)
	}

	levelsJSON, err := json.Marshal(levels)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal levels: %w", err)
	}
	ctxJSON, err := json.Marshal(in.Context)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal context: %w", err)
	}

	// 3. Compute sla_breach_at off the first level's sla_hours.
	var slaBreachAt *time.Time
	if firstSLA != nil && *firstSLA > 0 {
		t := time.Now().Add(time.Duration(*firstSLA) * time.Hour)
		slaBreachAt = &t
	}

	// 4. Insert. status defaults to 'pending', current_level to 0.
	var instanceID uuid.UUID
	var initiatorID *uuid.UUID
	if in.MakerUserID != uuid.Nil {
		initiatorID = &in.MakerUserID
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO wf_instances (
			tenant_id, definition_id, process_kind, subject_kind, subject_id,
			context, levels, summary, source_url, callback_url, initiator_id, sla_breach_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, NULLIF($8,''), NULLIF($9,''), NULLIF($10,''), $11, $12
		)
		RETURNING id
	`,
		in.TenantID, defID, in.ProcessKind, in.SubjectKind, in.SubjectID,
		ctxJSON, levelsJSON, in.Summary, in.SourceURL, in.CallbackURL, initiatorID, slaBreachAt,
	).Scan(&instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert wf_instance: %w", err)
	}
	return instanceID, nil
}
