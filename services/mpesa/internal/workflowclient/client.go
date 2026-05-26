// workflowclient — thin shared-DB writer that creates a wf_instance
// from inside an mpesa-side tx.
//
// Why shared-DB instead of HTTP: cross-service writes in this monorepo
// already use the shared-DB pattern (architecture memory: "Cross-service
// writes use direct shared-DB inserts, not HTTP"). Doing the create
// inside the same WithTenantTx the webhook handler already opened lets
// us guarantee that "event row exists" ⇔ "workflow task created" — no
// half-states where Safaricom's retry sees a row without a task.
//
// What this duplicates from workflow's own Create handler: the level
// snapshot logic + the wf_instances insert column list. If the
// workflow service ever changes the shape of LevelState or adds
// required columns, this file must update in lockstep. The unit test
// pins the column list against what `\d wf_instances` reported when
// this client was written; CI will catch a drift.

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

type Client struct{}

func New() *Client { return &Client{} }

// CreateInstanceInput is the minimum the webhook handler needs to
// hand over. Fields mirror workflow's createInstanceReq but typed
// in Go because we write the row directly.
type CreateInstanceInput struct {
	TenantID    uuid.UUID
	ProcessKind string
	SubjectKind string
	SubjectID   uuid.UUID
	Context     map[string]any
	Summary     string
	SourceURL   string
	InitiatorID *uuid.UUID
}

// levelState mirrors workflow's domain.LevelState — exactly the
// columns wf_instances.levels stores as JSON.
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
// exists for (tenant_id, process_kind). Callers can decide to either
// surface this loudly (the seed migration is the canonical place to
// add a definition) or downgrade to a best-effort skip.
var ErrDefinitionNotFound = errors.New("workflowclient: no active workflow definition for the given process_kind")

// CreateInstanceTx writes a wf_instances row + snapshots wf_levels
// into it. Caller must already have set app.tenant_id on the tx
// (i.e. be inside WithTenantTx for in.TenantID). Returns the new
// instance id.
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
			var c any
			if err := json.Unmarshal([]byte(*cond), &c); err == nil {
				ls.Condition = c
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

	// 3. Compute sla_breach_at off the first level's sla_hours when
	// present. The engine uses this for its escalation index.
	var slaBreachAt *time.Time
	if firstSLA != nil && *firstSLA > 0 {
		t := time.Now().Add(time.Duration(*firstSLA) * time.Hour)
		slaBreachAt = &t
	}

	// 4. Insert. status defaults to 'pending', current_level to 0 —
	// matching what workflow's own Create handler does for a fresh
	// instance with no condition-skipped levels.
	var instanceID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO wf_instances (
			tenant_id, definition_id, process_kind, subject_kind, subject_id,
			context, levels, summary, source_url, initiator_id, sla_breach_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, NULLIF($8,''), NULLIF($9,''), $10, $11
		)
		RETURNING id
	`,
		in.TenantID, defID, in.ProcessKind, in.SubjectKind, in.SubjectID,
		ctxJSON, levelsJSON, in.Summary, in.SourceURL, in.InitiatorID, slaBreachAt,
	).Scan(&instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert wf_instance: %w", err)
	}
	return instanceID, nil
}
