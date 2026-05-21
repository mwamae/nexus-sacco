// Instance lifecycle handler.
//
//   POST /v1/workflow-instances           — create (host module calls in)
//   GET  /v1/workflow-instances           — list
//   GET  /v1/workflow-instances/{id}      — instance + action audit
//   POST /v1/workflow-instances/{id}/actions — approve/reject/return/info/escalate/reassign/cancel

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/workflow/internal/auth"
	"github.com/nexussacco/workflow/internal/db"
	"github.com/nexussacco/workflow/internal/domain"
	"github.com/nexussacco/workflow/internal/httpx"
	"github.com/nexussacco/workflow/internal/jsonlogic"
	"github.com/nexussacco/workflow/internal/middleware"
	"github.com/nexussacco/workflow/internal/notifier"
	"github.com/nexussacco/workflow/internal/store"
)

type InstanceHandler struct {
	DB              *db.Pool
	Defs            *store.DefinitionStore
	Instances       *store.InstanceStore
	Actions         *store.ActionStore
	Tenants         *store.TenantStore
	HTTP            *http.Client      // for webhook delivery
	CallbackTimeout time.Duration
	Logger          *slog.Logger
	Notifier        *notifier.Client
}

// ─────────── POST /v1/workflow-instances ───────────

type createInstanceReq struct {
	ProcessKind    string         `json:"process_kind"`
	DefinitionID   *uuid.UUID     `json:"definition_id"` // optional; falls back to active def for process_kind
	SubjectKind    string         `json:"subject_kind"`
	SubjectID      string         `json:"subject_id"`
	Context        map[string]any `json:"context"`
	CallbackURL    string         `json:"callback_url"`
	CallbackSecret string         `json:"callback_secret"`
	InitiatorID    *uuid.UUID     `json:"initiator_id"`
}

func (h *InstanceHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var req createInstanceReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.ProcessKind = strings.TrimSpace(req.ProcessKind)
	req.SubjectKind = strings.TrimSpace(req.SubjectKind)
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	if req.ProcessKind == "" || req.SubjectKind == "" || req.SubjectID == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("process_kind, subject_kind, and subject_id are required"))
		return
	}
	if req.Context == nil {
		req.Context = map[string]any{}
	}
	// Default initiator to the authenticated user if none was specified.
	actorID, _ := middleware.UserIDFrom(r)
	initiator := req.InitiatorID
	if initiator == nil {
		initiator = nonZero(actorID)
	}

	var i *domain.Instance
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Resolve definition.
		var def *domain.Definition
		var err error
		if req.DefinitionID != nil {
			def, err = h.Defs.ByIDTx(r.Context(), tx, *req.DefinitionID)
		} else {
			def, err = h.Defs.ActiveByKindTx(r.Context(), tx, req.ProcessKind)
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrBadRequest("no active workflow definition for process_kind=" + req.ProcessKind)
			}
			return err
		}

		// Snapshot levels into LevelState, evaluating conditions to mark
		// non-matching levels as skipped up front.
		snap := make([]domain.LevelState, len(def.Levels))
		for ix, l := range def.Levels {
			ls := domain.LevelState{
				Order:           ix,
				Name:            l.Name,
				Status:          domain.LvlWaiting,
				ApproverRoles:   l.ApproverRoles,
				Quorum:          l.Quorum,
				Condition:       l.ConditionExpr,
				SLAHours:        l.SLAHours,
				EscalationRole:  l.EscalationRole,
			}
			for _, u := range l.ApproverUserIDs {
				ls.ApproverUserIDs = append(ls.ApproverUserIDs, u.String())
			}
			if l.EscalationUser != nil {
				ls.EscalationUser = l.EscalationUser.String()
			}
			if l.ConditionExpr != nil {
				ok, err := jsonlogic.Eval(l.ConditionExpr, req.Context)
				if err != nil {
					return httpx.ErrBadRequest("level " + l.Name + ": condition evaluation failed: " + err.Error())
				}
				if !ok {
					ls.Status = domain.LvlSkipped
				}
			}
			snap[ix] = ls
		}

		// Find the first non-skipped level and mark it in_progress; if all skipped, auto-approve.
		startLevel := -1
		for ix := range snap {
			if snap[ix].Status != domain.LvlSkipped {
				startLevel = ix
				break
			}
		}
		var startStatus domain.Status
		if startLevel == -1 {
			startStatus = domain.StatusApproved
		} else {
			startStatus = domain.StatusInProgress
			now := time.Now().UTC()
			snap[startLevel].Status = domain.LvlInProgress
			snap[startLevel].EnteredAt = &now
			if snap[startLevel].SLAHours != nil {
				due := now.Add(time.Duration(*snap[startLevel].SLAHours) * time.Hour)
				snap[startLevel].SLADueAt = &due
			}
		}

		inst, err := h.Instances.CreateTx(r.Context(), tx, store.CreateInstanceInput{
			TenantID: tenantID, DefinitionID: def.ID,
			ProcessKind: req.ProcessKind, SubjectKind: req.SubjectKind, SubjectID: req.SubjectID,
			Context: req.Context, CallbackURL: req.CallbackURL, CallbackSecret: req.CallbackSecret,
			InitiatorID:    initiator,
			LevelsSnapshot: snap,
			StartingLevel:  maxInt(startLevel, 0),
			StartingStatus: startStatus,
		})
		if err != nil {
			return err
		}
		// Audit create.
		if _, err := h.Actions.WriteTx(r.Context(), tx, store.CreateActionInput{
			TenantID: tenantID, InstanceID: inst.ID,
			Action: domain.ActCreate, ActorID: initiator,
			Comments: "instance created",
			Metadata: map[string]any{"definition_id": def.ID, "definition_version": def.Version},
		}); err != nil {
			return err
		}
		i = inst
		// If we auto-approved (all skipped), fire the callback.
		if startStatus == domain.StatusApproved {
			now := time.Now().UTC()
			i.CompletedAt = &now
			if err := h.Instances.UpdateProgressTx(r.Context(), tx, i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Webhook delivery if terminal (out-of-txn).
	if i.Status == domain.StatusApproved {
		h.fireCallback(r.Context(), tenantID, i)
	}
	// Notify approvers that a new request landed in their queue. We
	// only fire when the instance is still pending (someone needs to
	// act); auto-approved chains skip notification — the host module
	// is already getting a webhook.
	if i.Status != domain.StatusApproved {
		h.fireApprovalNotification(r.Context(), tenantID, i, "APPROVAL_REQUEST_SENT")
	}
	httpx.Created(w, i)
}

// ─────────── GET /v1/workflow-instances ───────────

func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	in := store.ListInstancesInput{
		Status:      domain.Status(strings.TrimSpace(q.Get("status"))),
		ProcessKind: strings.TrimSpace(q.Get("process_kind")),
		SubjectKind: strings.TrimSpace(q.Get("subject_kind")),
		SubjectID:   strings.TrimSpace(q.Get("subject_id")),
	}
	if l := q.Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &in.Limit)
	}
	if o := q.Get("offset"); o != "" {
		fmt.Sscanf(o, "%d", &in.Offset)
	}
	var result *store.ListInstancesResult
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		result, err = h.Instances.ListTx(r.Context(), tx, in)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if result.Instances == nil {
		result.Instances = []*domain.Instance{}
	}
	httpx.OK(w, result)
}

// ─────────── GET /v1/workflow-instances/{id} ───────────

type instanceDetail struct {
	*domain.Instance
	Actions []*domain.Action `json:"actions"`
}

func (h *InstanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var out instanceDetail
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		inst, err := h.Instances.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out.Instance = inst
		out.Actions, err = h.Actions.ListForInstanceTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("instance not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if out.Actions == nil {
		out.Actions = []*domain.Action{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/workflow-instances/{id}/actions ───────────

type actionRequest struct {
	Action       string     `json:"action"`     // approve | reject | return | request_info | resume | escalate | reassign | cancel
	Comments     string     `json:"comments"`
	ReassignTo   *uuid.UUID `json:"reassign_to,omitempty"`
	ActingAsRole string     `json:"acting_as_role,omitempty"` // when caller has multiple roles, which one
}

func (h *InstanceHandler) Action(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var req actionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	action := domain.ActionKind(strings.ToLower(strings.TrimSpace(req.Action)))
	switch action {
	case domain.ActApprove, domain.ActReject, domain.ActReturn,
		domain.ActRequestInfo, domain.ActResume, domain.ActEscalate,
		domain.ActReassign, domain.ActCancel:
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("unsupported action"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	claims := middleware.ClaimsFrom(r)
	if claims == nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}
	var (
		fireCallback bool
		updated      *domain.Instance
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		inst, err := h.Instances.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !canActOnInstance(inst) {
			return httpx.ErrBadRequest("instance is " + string(inst.Status) + " and cannot accept actions")
		}
		// Cancel is initiator-or-admin only; other actions need approver match.
		if action == domain.ActCancel {
			if !isInitiatorOrAdmin(inst, actorID, claims) {
				return httpx.ErrForbidden("only the initiator or a platform admin can cancel")
			}
		} else {
			role, ok := authorisedRoleAt(inst, inst.CurrentLevel, actorID, claims, req.ActingAsRole)
			if !ok {
				return httpx.ErrForbidden("not authorised to act at the current level")
			}
			req.ActingAsRole = role
		}

		now := time.Now().UTC()
		level := &inst.Levels[inst.CurrentLevel]

		// Check whether the level's SLA has been breached; if so, log it
		// before continuing (next-action policy).
		if level.SLADueAt != nil && now.After(*level.SLADueAt) && level.Status != domain.LvlEscalated {
			_, _ = h.Actions.WriteTx(r.Context(), tx, store.CreateActionInput{
				TenantID: tenantID, InstanceID: inst.ID,
				LevelOrder: &inst.CurrentLevel, Action: domain.ActSLABreached,
				Comments: "SLA breached before action",
				Metadata: map[string]any{"sla_due_at": level.SLADueAt, "now": now},
			})
		}

		switch action {
		case domain.ActApprove:
			level.ApprovedBy = append(level.ApprovedBy, actorID.String())
			// Quorum check.
			if level.Quorum == domain.QuorumAll {
				required := uniqueApproversAt(level)
				if len(distinctApprovals(level)) < required {
					// Need more approvers.
					inst.Status = domain.StatusInProgress
					break
				}
			}
			level.Status = domain.LvlApproved
			level.CompletedAt = &now
			// Advance.
			next := nextActiveLevel(inst.Levels, inst.CurrentLevel+1)
			if next == -1 {
				inst.Status = domain.StatusApproved
				inst.CompletedAt = &now
				fireCallback = true
			} else {
				inst.CurrentLevel = next
				inst.Status = domain.StatusInProgress
				nl := &inst.Levels[next]
				nl.Status = domain.LvlInProgress
				nl.EnteredAt = &now
				if nl.SLAHours != nil {
					due := now.Add(time.Duration(*nl.SLAHours) * time.Hour)
					nl.SLADueAt = &due
				}
			}

		case domain.ActReject:
			if strings.TrimSpace(req.Comments) == "" {
				return httpx.ErrBadRequest("comments are required when rejecting")
			}
			level.Status = domain.LvlRejected
			level.CompletedAt = &now
			inst.Status = domain.StatusRejected
			inst.CompletedAt = &now
			fireCallback = true

		case domain.ActReturn:
			if strings.TrimSpace(req.Comments) == "" {
				return httpx.ErrBadRequest("comments are required when returning")
			}
			level.Status = domain.LvlReturned
			inst.Status = domain.StatusReturned
			// initiator handles it; current_level stays.

		case domain.ActRequestInfo:
			level.Status = domain.LvlAwaitingInfo
			inst.Status = domain.StatusAwaitingInfo

		case domain.ActResume:
			// Returning from a returned/awaiting_info state — only the initiator can do this.
			if !isInitiator(inst, actorID) {
				return httpx.ErrForbidden("only the initiator can resume")
			}
			level.Status = domain.LvlInProgress
			inst.Status = domain.StatusInProgress
			if level.SLAHours != nil {
				due := now.Add(time.Duration(*level.SLAHours) * time.Hour)
				level.SLADueAt = &due
			}

		case domain.ActEscalate:
			level.Status = domain.LvlEscalated
			inst.Status = domain.StatusEscalated

		case domain.ActReassign:
			if req.ReassignTo == nil {
				return httpx.ErrBadRequest("reassign_to is required")
			}
			level.ApproverUserIDs = []string{req.ReassignTo.String()}

		case domain.ActCancel:
			level.Status = domain.LvlReturned // bookkeeping
			inst.Status = domain.StatusCancelled
			inst.CompletedAt = &now
			fireCallback = true
		}

		if err := h.Instances.UpdateProgressTx(r.Context(), tx, inst); err != nil {
			return err
		}
		levelOrder := inst.CurrentLevel
		if _, err := h.Actions.WriteTx(r.Context(), tx, store.CreateActionInput{
			TenantID: tenantID, InstanceID: inst.ID,
			LevelOrder: &levelOrder, Action: action,
			ActorID: nonZero(actorID), ActorRole: req.ActingAsRole,
			Comments: req.Comments,
			Metadata: map[string]any{"reassign_to": req.ReassignTo},
		}); err != nil {
			return err
		}
		updated = inst
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if fireCallback {
		h.fireCallback(r.Context(), tenantID, updated)
	}
	// Mirror the action onto the notification stream. ESCALATE has its
	// own event; APPROVE / REJECT / CANCEL all roll up to APPROVAL_ACTIONED
	// with the action embedded in the payload.
	switch action {
	case domain.ActEscalate:
		h.fireApprovalNotification(r.Context(), tenantID, updated, "APPROVAL_ESCALATED")
	case domain.ActApprove, domain.ActReject, domain.ActCancel:
		h.fireApprovalNotification(r.Context(), tenantID, updated, "APPROVAL_ACTIONED")
	}
	httpx.OK(w, updated)
}

// fireApprovalNotification routes a workflow event through the central
// notification service. Recipient is the request initiator (so they
// know their request moved); for APPROVAL_REQUEST_SENT we'd ideally
// also page the approvers, but that requires a user-ID list which
// the current instance shape doesn't carry cheaply — covered by the
// in-app inbox via approval:view dashboards.
func (h *InstanceHandler) fireApprovalNotification(ctx context.Context, tenantID uuid.UUID, inst *domain.Instance, eventCode string) {
	if h.Notifier == nil || inst == nil {
		return
	}
	sourceModule := "workflow"
	recordID := inst.ID
	deepLink := "/approvals/" + inst.ID.String()
	payload := map[string]any{
		"process_kind": inst.ProcessKind,
		"status":       string(inst.Status),
		"subject_kind": inst.SubjectKind,
		"subject_id":   inst.SubjectID,
	}
	// Notify the initiator about their request's progress.
	var recipient *uuid.UUID
	if inst.InitiatorID != nil && *inst.InitiatorID != uuid.Nil {
		id := *inst.InitiatorID
		recipient = &id
	}
	h.Notifier.Notify(ctx, notifier.Request{
		TenantID:        tenantID,
		EventCode:       eventCode,
		RecipientUserID: recipient,
		RecipientName:   "Request initiator",
		SourceModule:    &sourceModule,
		SourceRecordID:  &recordID,
		DeepLink:        &deepLink,
		Payload:         payload,
	})
}

// ─────────── GET /v1/workflows/dashboard ───────────

func (h *InstanceHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var d *store.DashboardCounts
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		d, err = h.Instances.DashboardTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, d)
}

// ─────────── helpers ───────────

func canActOnInstance(i *domain.Instance) bool {
	switch i.Status {
	case domain.StatusPending, domain.StatusInProgress, domain.StatusReturned,
		domain.StatusAwaitingInfo, domain.StatusEscalated:
		return true
	}
	return false
}

func isInitiator(i *domain.Instance, actor uuid.UUID) bool {
	return i.InitiatorID != nil && *i.InitiatorID == actor
}

func isInitiatorOrAdmin(i *domain.Instance, actor uuid.UUID, claims *auth.AccessClaims) bool {
	if isInitiator(i, actor) {
		return true
	}
	if claims != nil && (claims.IsPlatformAdmin || claims.HasPermission("workflow:configure")) {
		return true
	}
	return false
}

// authorisedRoleAt returns the role the caller is acting under for this
// level, or false if they're not authorised. Platform admins and users
// explicitly listed in approver_user_ids are always authorised.
func authorisedRoleAt(i *domain.Instance, lvlIdx int, actor uuid.UUID, claims *auth.AccessClaims, preferredRole string) (string, bool) {
	if claims == nil {
		return "", false
	}
	if claims.IsPlatformAdmin {
		return "platform_admin", true
	}
	level := i.Levels[lvlIdx]
	for _, u := range level.ApproverUserIDs {
		if u == actor.String() {
			return "direct", true
		}
	}
	for _, role := range claims.Roles {
		if preferredRole != "" && role != preferredRole {
			continue
		}
		for _, lr := range level.ApproverRoles {
			if lr == role {
				return role, true
			}
		}
	}
	return "", false
}

func nextActiveLevel(levels []domain.LevelState, fromIdx int) int {
	for i := fromIdx; i < len(levels); i++ {
		if levels[i].Status != domain.LvlSkipped {
			return i
		}
	}
	return -1
}

func uniqueApproversAt(level *domain.LevelState) int {
	return len(level.ApproverUserIDs)
}

func distinctApprovals(level *domain.LevelState) []string {
	seen := map[string]struct{}{}
	for _, u := range level.ApprovedBy {
		seen[u] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─────────── webhook delivery ───────────

func (h *InstanceHandler) fireCallback(parent context.Context, tenantID uuid.UUID, i *domain.Instance) {
	if i.CallbackURL == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), h.CallbackTimeout)
		defer cancel()
		body, _ := json.Marshal(map[string]any{
			"tenant_id":     tenantID,
			"instance":      i,
			"event":         string(i.Status),
			"delivered_at":  time.Now().UTC(),
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.CallbackURL, bytes.NewReader(body))
		if err != nil {
			h.recordCallback(parent, tenantID, i.ID, "failed:request: "+err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "nexus-workflow/1")
		req.Header.Set("X-Nexus-Workflow-Event", string(i.Status))
		resp, err := h.HTTP.Do(req)
		if err != nil {
			h.recordCallback(parent, tenantID, i.ID, "failed:transport: "+err.Error())
			return
		}
		defer resp.Body.Close()
		// Drain to allow connection reuse.
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			h.recordCallback(parent, tenantID, i.ID, "delivered")
		} else {
			h.recordCallback(parent, tenantID, i.ID, "failed:status:"+resp.Status)
		}
	}()
}

func (h *InstanceHandler) recordCallback(parent context.Context, tenantID, instanceID uuid.UUID, status string) {
	delivered := time.Now().UTC()
	_ = h.DB.WithTenantTx(parent, tenantID, func(tx pgx.Tx) error {
		if err := h.Instances.SetCallbackStatusTx(parent, tx, instanceID, status, &delivered); err != nil {
			return err
		}
		_, err := h.Actions.WriteTx(parent, tx, store.CreateActionInput{
			TenantID:   tenantID,
			InstanceID: instanceID,
			Action:     domain.ActCallbackFired,
			Comments:   status,
		})
		return err
	})
}
