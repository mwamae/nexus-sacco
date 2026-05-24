// Domain entities — kept JSON-friendly because the wf_instances row
// stores its per-level state as a jsonb array of these objects.

package domain

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending      Status = "pending"
	StatusInProgress   Status = "in_progress"
	StatusApproved     Status = "approved"
	StatusRejected     Status = "rejected"
	StatusReturned     Status = "returned"
	StatusAwaitingInfo Status = "awaiting_info"
	StatusEscalated    Status = "escalated"
	StatusCancelled    Status = "cancelled"
	StatusExpired      Status = "expired"
)

type LevelStatus string

const (
	LvlWaiting       LevelStatus = "waiting"
	LvlInProgress    LevelStatus = "in_progress"
	LvlApproved      LevelStatus = "approved"
	LvlRejected      LevelStatus = "rejected"
	LvlReturned      LevelStatus = "returned"
	LvlAwaitingInfo  LevelStatus = "awaiting_info"
	LvlEscalated     LevelStatus = "escalated"
	LvlSkipped       LevelStatus = "skipped"
)

type ActionKind string

const (
	ActCreate         ActionKind = "create"
	ActApprove        ActionKind = "approve"
	ActReject         ActionKind = "reject"
	ActReturn         ActionKind = "return"
	ActRequestInfo    ActionKind = "request_info"
	ActResume         ActionKind = "resume"
	ActEscalate       ActionKind = "escalate"
	ActReassign       ActionKind = "reassign"
	ActCancel         ActionKind = "cancel"
	ActCallbackFired  ActionKind = "callback_fired"
	ActSLABreached    ActionKind = "sla_breached"
	ActComment        ActionKind = "comment"
	ActClaim          ActionKind = "claim"
	ActRelease        ActionKind = "release"
)

type Quorum string

const (
	QuorumAnyOne   Quorum = "any_one"
	QuorumAll      Quorum = "all"
	QuorumMajority Quorum = "majority"
)

// LevelDef is the definition-time level shape (stored in wf_levels).
type LevelDef struct {
	ID              uuid.UUID  `json:"id,omitempty"`
	LevelOrder      int        `json:"level_order"`
	Name            string     `json:"name"`
	ApproverRoles   []string   `json:"approver_roles"`
	ApproverUserIDs []uuid.UUID `json:"approver_user_ids"`
	Quorum          Quorum     `json:"quorum"`
	ConditionExpr   any        `json:"condition_expr,omitempty"` // nullable jsonb
	SLAHours        *int       `json:"sla_hours,omitempty"`
	EscalationRole  string     `json:"escalation_role,omitempty"`
	EscalationUser  *uuid.UUID `json:"escalation_user_id,omitempty"`
}

// LevelState is the run-time per-level shape (stored in
// wf_instances.levels). It snapshots the definition fields at instance
// creation so subsequent definition edits don't retroactively change
// running flows.
type LevelState struct {
	Order           int         `json:"order"`
	Name            string      `json:"name"`
	Status          LevelStatus `json:"status"`
	ApproverRoles   []string    `json:"approver_roles"`
	ApproverUserIDs []string    `json:"approver_user_ids,omitempty"`
	Quorum          Quorum      `json:"quorum"`
	Condition       any         `json:"condition,omitempty"`
	SLAHours        *int        `json:"sla_hours,omitempty"`
	SLADueAt        *time.Time  `json:"sla_due_at,omitempty"`
	ApprovedBy      []string    `json:"approved_by,omitempty"` // user IDs who already approved (quorum tracking)
	EnteredAt       *time.Time  `json:"entered_at,omitempty"`
	CompletedAt     *time.Time  `json:"completed_at,omitempty"`
	EscalationRole  string      `json:"escalation_role,omitempty"`
	EscalationUser  string      `json:"escalation_user_id,omitempty"`
}

type Definition struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	ProcessKind string     `json:"process_kind"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Version     int        `json:"version"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty"`
	Levels      []LevelDef `json:"levels"`
}

type Instance struct {
	ID            uuid.UUID    `json:"id"`
	TenantID      uuid.UUID    `json:"tenant_id"`
	DefinitionID  uuid.UUID    `json:"definition_id"`
	ProcessKind   string       `json:"process_kind"`
	SubjectKind   string       `json:"subject_kind"`
	SubjectID     uuid.UUID    `json:"subject_id"`
	Status        Status       `json:"status"`
	CurrentLevel  int          `json:"current_level"`
	Context       any          `json:"context"`
	CallbackURL   string       `json:"callback_url,omitempty"`
	CallbackStatus string      `json:"callback_status,omitempty"`
	CallbackDeliveredAt *time.Time `json:"callback_delivered_at,omitempty"`
	InitiatorID   *uuid.UUID   `json:"initiator_id,omitempty"`
	StartedAt     time.Time    `json:"started_at"`
	CompletedAt   *time.Time   `json:"completed_at,omitempty"`
	Levels        []LevelState `json:"levels"`
	// Unified Inbox additions (migration 0002):
	Summary       string       `json:"summary,omitempty"`        // one-line Inbox description
	SourceURL     string       `json:"source_url,omitempty"`     // deep-link back to originating page
	ClaimedBy     *uuid.UUID   `json:"claimed_by,omitempty"`
	ClaimedAt     *time.Time   `json:"claimed_at,omitempty"`
	ClaimExpires  *time.Time   `json:"claim_expires,omitempty"`
	SLABreachAt   *time.Time   `json:"sla_breach_at,omitempty"`  // mirror of active-level sla_due_at; indexed
}

type Action struct {
	ID         uuid.UUID  `json:"id"`
	InstanceID uuid.UUID  `json:"instance_id"`
	LevelOrder *int       `json:"level_order,omitempty"`
	Action     ActionKind `json:"action"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	ActorRole  string     `json:"actor_role,omitempty"`
	Comments   string     `json:"comments,omitempty"`
	Metadata   any        `json:"metadata,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}
