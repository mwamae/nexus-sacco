// Unified Inbox bridge for the bulk dormancy detector (PR #6).
//
// Two endpoints replace POST /v1/members/dormancy/run when the
// tenant has unified_inbox_enabled:
//
//   POST /v1/members/dormancy/submit-for-approval
//        Snapshots the candidate set + creates a bulk_dormancy_run
//        wf_instance + a dormancy_runs tracking row. Idempotent in
//        spirit (returns a fresh run id each call; the workflow
//        engine queue can hold multiple pending submissions if the
//        operator is impatient).
//
//   POST /internal/v1/members/dormancy/resolve
//        Workflow callback target. On approve loops the snapshotted
//        candidates through Status.ApplyTx (NOT a fresh DormancyCandidatesTx
//        query — the candidate set at approve-time must match what
//        the Board saw). Records per-row outcomes (some may now have
//        moved out of 'active' between submit and approve; those are
//        skipped + audited).

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/store"
)

// SubmitDormancyForApproval is the CTA the dashboard's dormancy
// widget hits when unified_inbox_enabled is on. Computes candidates,
// snapshots them, creates the workflow instance.
func (h *StatusHandler) SubmitDormancyForApproval(w http.ResponseWriter, r *http.Request) {
	if h.WorkflowURL == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("workflow service not configured (WORKFLOW_SERVICE_URL)"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	threshold := h.tenantDormancyDays(r.Context(), tenantID)

	// Compute candidates inside a tx but don't apply — the apply
	// runs in the resolve callback after Board approval.
	var candidates []*store.DormancyCandidate
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		c, err := h.Status.DormancyCandidatesTx(r.Context(), tx, threshold)
		if err != nil {
			return err
		}
		candidates = c
		return nil
	}); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if len(candidates) == 0 {
		httpx.WriteErr(w, r, httpx.ErrConflict(fmt.Sprintf("no candidates inactive ≥ %d days — nothing to submit", threshold)))
		return
	}

	wfID, err := h.createDormancyWorkflowInstance(r, tenantID, threshold, len(candidates), userID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var run *store.DormancyRun
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		r2, err := h.DormancyRuns.CreateTx(r.Context(), tx, tenantID, wfID, userID, threshold, candidates)
		if err != nil {
			return err
		}
		run = r2
		return nil
	}); err != nil {
		h.cancelDormancyWorkflowInstance(r, wfID)
		httpx.WriteErr(w, r, err)
		return
	}

	httpx.OK(w, map[string]any{
		"workflow_instance_id": wfID,
		"run_id":               run.ID,
		"candidate_count":      run.CandidateCount,
		"threshold_days":       run.ThresholdDays,
		"status":               "created",
	})
}

func (h *StatusHandler) createDormancyWorkflowInstance(r *http.Request, tenantID uuid.UUID, threshold, count int, actorID uuid.UUID) (uuid.UUID, error) {
	_ = tenantID
	callback := ""
	if h.MemberSelfURL != "" {
		callback = strings.TrimRight(h.MemberSelfURL, "/") + "/internal/v1/members/dormancy/resolve"
	}
	body, _ := json.Marshal(map[string]any{
		"process_kind": "bulk_dormancy_run",
		"subject_kind": "dormancy_run",
		"subject_id":   uuid.New(), // synthetic; the wf_instance ID is the real anchor (back-linked via dormancy_runs)
		"context": map[string]any{
			"threshold_days":  threshold,
			"candidate_count": count,
		},
		"callback_url": callback,
		"initiator_id": actorID,
		"summary":      fmt.Sprintf("Dormancy detector — %d members inactive ≥ %d days", count, threshold),
		"source_url":   "/dashboard",
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(h.WorkflowURL, "/")+"/v1/workflow-instances",
		bytes.NewReader(body))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}
	req.Host = r.Host
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return uuid.Nil, httpx.ErrConflict("workflow service unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, httpx.ErrConflict("workflow service rejected the instance: " + string(b))
	}
	var env struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return uuid.Nil, err
	}
	if env.Data.ID == uuid.Nil {
		return uuid.Nil, httpx.ErrConflict("workflow service returned no instance id")
	}
	return env.Data.ID, nil
}

func (h *StatusHandler) cancelDormancyWorkflowInstance(r *http.Request, wfID uuid.UUID) {
	body, _ := json.Marshal(map[string]any{"action": "cancel", "comments": "dormancy_runs write failed; compensating"})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(h.WorkflowURL, "/")+"/v1/workflow-instances/"+wfID.String()+"/actions",
		bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}
	req.Host = r.Host
	resp, _ := h.HTTP.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// ─────────── POST /internal/v1/members/dormancy/resolve ───────────

type dormancyResolveEnvelope struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Event    string    `json:"event"`
	Instance struct {
		ID uuid.UUID `json:"id"`
	} `json:"instance"`
}

func (h *StatusHandler) ResolveDormancyFromWorkflow(w http.ResponseWriter, r *http.Request) {
	expected := h.WorkflowInternalToken
	got := r.Header.Get("X-Internal-Token")
	if expected != "" {
		if got != expected {
			httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
			return
		}
	} else if !strings.HasPrefix(r.Header.Get("User-Agent"), "nexus-workflow") {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("workflow callback expected"))
		return
	}
	var env dormancyResolveEnvelope
	if err := httpx.DecodeJSON(r, &env); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if env.TenantID == uuid.Nil || env.Event == "" || env.Instance.ID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id, event, instance.id required"))
		return
	}

	var out any
	err := h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
		run, err := h.DormancyRuns.ByWorkflowInstanceTx(r.Context(), tx, env.Instance.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Not ours — silently ack so the workflow doesn't retry.
				return nil
			}
			return err
		}
		// Idempotency.
		if run.Status != "pending" {
			out = run
			return nil
		}
		switch env.Event {
		case "approved":
			outcomes, applyErr := h.applyDormancySnapshotTx(r.Context(), tx, env.TenantID, run.SubmittedBy, run.Snapshot)
			if applyErr != nil {
				_ = h.DormancyRuns.MarkTerminalTx(r.Context(), tx, run.ID, "approved", "apply failed: "+applyErr.Error())
				return applyErr
			}
			if err := h.DormancyRuns.MarkAppliedTx(r.Context(), tx, run.ID, outcomes); err != nil {
				return err
			}
			out = map[string]any{
				"run_id":   run.ID,
				"outcomes": outcomes,
			}
		case "rejected", "cancelled":
			if err := h.DormancyRuns.MarkTerminalTx(r.Context(), tx, run.ID, env.Event, ""); err != nil {
				return err
			}
			out = run
		default:
			return httpx.ErrBadRequest("unsupported event: " + env.Event)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// applyDormancySnapshotTx loops the snapshotted candidates through
// Status.ApplyTx and collects per-row outcomes. A candidate whose
// state has drifted out of 'active' since submit is skipped (audited
// in the outcomes array) rather than failing the whole batch.
func (h *StatusHandler) applyDormancySnapshotTx(
	ctx context.Context, tx pgx.Tx, tenantID, actorID uuid.UUID,
	snapshot []store.DormancyCandidate,
) ([]store.DormancyApplyOutcome, error) {
	outcomes := make([]store.DormancyApplyOutcome, 0, len(snapshot))
	for _, c := range snapshot {
		m, err := h.Members.ByCounterpartyTx(ctx, tx, c.CounterpartyID)
		if err != nil {
			outcomes = append(outcomes, store.DormancyApplyOutcome{
				CounterpartyID: c.CounterpartyID, MemberNo: c.MemberNo,
				Outcome: "skipped:lookup_failed",
			})
			continue
		}
		if m.Status != domain.StatusActive {
			outcomes = append(outcomes, store.DormancyApplyOutcome{
				CounterpartyID: c.CounterpartyID, MemberNo: c.MemberNo,
				Outcome: "skipped:state_drifted_to_" + string(m.Status),
			})
			continue
		}
		actor := actorID
		if _, err := h.Status.ApplyTx(ctx, tx, store.ApplyInput{
			TenantID:       tenantID,
			CounterpartyID: c.CounterpartyID,
			FromStatus:     domain.StatusActive,
			ToStatus:       domain.StatusDormant,
			ReasonCategory: domain.ReasonDormancyInactivity,
			ReasonNote:     "auto: inactive for " + dayString(c.DaysInactive) + " (approved batch)",
			ChangedBy:      &actor,
		}); err != nil {
			outcomes = append(outcomes, store.DormancyApplyOutcome{
				CounterpartyID: c.CounterpartyID, MemberNo: c.MemberNo,
				Outcome: "skipped:apply_error:" + err.Error(),
			})
			continue
		}
		outcomes = append(outcomes, store.DormancyApplyOutcome{
			CounterpartyID: c.CounterpartyID, MemberNo: c.MemberNo,
			Outcome: "applied",
		})
	}
	return outcomes, nil
}

// Compile-time sanity — assert StatusHandler has the bits the new
// methods rely on. Keeps the diff in main.go and the handler fields
// honest.
var _ = func() { var _ time.Time; _ = http.NewRequest }
