// Unified Inbox bridge for membership applications (PR #8).
//
// Two endpoints replace the inline Approve / Decline buttons on
// /applications/{id} when the tenant has unified_inbox_enabled:
//
//   POST /v1/applications/{id}/submit-for-onboarding-decision
//        Frontend CTA. Creates a member_onboarding wf_instance +
//        back-links it on membership_applications.workflow_instance_id.
//        Idempotent — re-clicking returns the existing instance.
//
//   POST /internal/v1/applications/{id}/resolve
//        Service-to-service callback. On approve runs the same
//        approveAndActivateTx logic the inline Approve uses (status
//        flip + member materialisation + share/savings account
//        creation + counterparty co-create). On reject calls
//        Decline. Idempotent on terminal status.

package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/notifier"
	"github.com/nexussacco/member/internal/store"
)

// SubmitForOnboardingDecision creates the workflow instance + back-
// link. Idempotent.
type submitDecisionResponse struct {
	WorkflowInstanceID uuid.UUID `json:"workflow_instance_id"`
	Status             string    `json:"status"` // "created" | "existing"
}

func (h *ApplicationHandler) SubmitForOnboardingDecision(w http.ResponseWriter, r *http.Request) {
	if h.WorkflowURL == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("workflow service not configured (WORKFLOW_SERVICE_URL)"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	actorID, _ := middleware.UserIDFrom(r)

	var app *domain.MembershipApplication
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		got, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		app = got
		return nil
	}); err != nil {
		if errors.Is(err, store.ErrApplicationNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("application not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	// Idempotency — if we already wired this app to a workflow
	// instance, return the existing one.
	if app.WorkflowInstanceID != nil {
		httpx.OK(w, submitDecisionResponse{WorkflowInstanceID: *app.WorkflowInstanceID, Status: "existing"})
		return
	}

	// Only certain FSM states make sense to submit. The legacy
	// Approve was available at reviewed_pending_approval; we relax
	// slightly so the auto-create-on-first-open path can fire while
	// the app is still under_review (no decision will be taken until
	// it reaches reviewed_pending_approval anyway — workflow just
	// surfaces it in the Inbox sooner).
	if !canSubmitOnboardingDecision(app.Status) {
		httpx.WriteErr(w, r, httpx.ErrConflict(fmt.Sprintf("cannot submit application in status=%s for onboarding decision", app.Status)))
		return
	}

	wfID, err := h.createAppWorkflowInstance(r, tenantID, app, actorID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Back-link. Compensate by cancelling the wf_instance on write
	// failure so we don't leak ghost rows into the Inbox.
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(),
			`UPDATE membership_applications SET workflow_instance_id = $1 WHERE id = $2`, wfID, id)
		return e
	}); err != nil {
		h.cancelAppWorkflowInstance(r, wfID)
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, submitDecisionResponse{WorkflowInstanceID: wfID, Status: "created"})
}

func canSubmitOnboardingDecision(s domain.ApplicationStatus) bool {
	switch s {
	case domain.AppStatusSubmitted,
		domain.AppStatusUnderReview,
		domain.AppStatusReviewedPendingApp,
		domain.AppStatusReturnedForCorrection:
		return true
	}
	return false
}

func (h *ApplicationHandler) createAppWorkflowInstance(r *http.Request, tenantID uuid.UUID, app *domain.MembershipApplication, actorID uuid.UUID) (uuid.UUID, error) {
	callback := ""
	if h.MemberSelfURL != "" {
		callback = strings.TrimRight(h.MemberSelfURL, "/") +
			fmt.Sprintf("/internal/v1/applications/%s/resolve", app.ID)
	}
	body, _ := json.Marshal(map[string]any{
		"process_kind": "member_onboarding",
		"subject_kind": "membership_application",
		"subject_id":   app.ID,
		"context": map[string]any{
			"application_id":   app.ID.String(),
			"application_no":   app.ApplicationNo,
			"kind":             string(app.Kind),
			"applicant_name":   app.ApplicantName,
			"primary_phone":    derefStringPtr(app.PrimaryPhone),
			"primary_email":    derefStringPtr(app.PrimaryEmail),
			"fee_required":     app.FeeRequired,
			"fee_amount_paid":  app.FeeAmountPaid,
			"status":           string(app.Status),
		},
		"callback_url": callback,
		"initiator_id": actorID,
		"summary":      fmt.Sprintf("Onboarding: %s — %s (%s)", app.ApplicationNo, app.ApplicantName, app.Kind),
		"source_url":   fmt.Sprintf("/applications/%s", app.ID),
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
	resp, err := h.httpClient().Do(req)
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

func (h *ApplicationHandler) cancelAppWorkflowInstance(r *http.Request, wfID uuid.UUID) {
	body, _ := json.Marshal(map[string]any{"action": "cancel", "comments": "back-link write failed; compensating"})
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
	resp, _ := h.httpClient().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
}

func (h *ApplicationHandler) httpClient() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ─────────── POST /internal/v1/applications/{id}/resolve ───────────

type appResolveEnvelope struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Event    string    `json:"event"`
	Instance struct {
		ID      uuid.UUID      `json:"id"`
		Context map[string]any `json:"context"`
	} `json:"instance"`
}

func (h *ApplicationHandler) ResolveFromWorkflow(w http.ResponseWriter, r *http.Request) {
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
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var env appResolveEnvelope
	if err := httpx.DecodeJSON(r, &env); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if env.TenantID == uuid.Nil || env.Event == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id and event required"))
		return
	}

	var (
		resolved   *domain.MembershipApplication
		activation *store.ActivationResult
	)
	err = h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Idempotency — terminal status short-circuits.
		if app.Status == domain.AppStatusApprovedActive || app.Status == domain.AppStatusDeclined ||
			app.Status == domain.AppStatusWithdrawn {
			resolved = app
			return nil
		}
		// Use the application's submitter as the actor of record for
		// the FSM transition; the real human approver is in
		// wf_actions on the workflow instance.
		actor := app.SubmittedBy
		switch env.Event {
		case "approved":
			conditions := strFromCtx(env.Instance.Context, "conditions")
			updated, act, aerr := h.approveAndActivateTx(r.Context(), tx, env.TenantID, app.ID, actor, conditions)
			if aerr != nil {
				return aerr
			}
			resolved = updated
			activation = act
		case "rejected":
			reason := strFromCtx(env.Instance.Context, "decline_reason")
			if reason == "" {
				reason = "Declined via workflow"
			}
			updated, terr := h.Applications.TransitionTx(r.Context(), tx, store.TransitionInput{
				ID:            app.ID,
				From:          app.Status,
				To:            domain.AppStatusDeclined,
				ActorUserID:   actor,
				DeclineReason: reason,
			})
			if terr != nil {
				return terr
			}
			resolved = updated
		case "cancelled":
			updated, terr := h.Applications.TransitionTx(r.Context(), tx, store.TransitionInput{
				ID:          app.ID,
				From:        app.Status,
				To:          domain.AppStatusWithdrawn,
				ActorUserID: actor,
			})
			if terr != nil {
				return terr
			}
			resolved = updated
		default:
			return httpx.ErrBadRequest("unsupported event: " + env.Event)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Post-tx side effects — same as the legacy Approve handler.
	// Only fire on approve, and only if the activation actually
	// happened (terminal-status short-circuit returned activation=nil).
	if env.Event == "approved" && activation != nil && resolved != nil {
		if resolved.FeeRequired && resolved.FeeAmountPaid.IsPositive() {
			if jeID := h.postFeeToGL(r, env.TenantID, resolved, resolved.SubmittedBy, false); jeID != uuid.Nil {
				_ = h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
					return h.Applications.SetFeeJournalEntryTx(r.Context(), tx, resolved.ID, jeID)
				})
				resolved.FeeJournalEntryID = &jeID
			}
		}
		if h.Notifier != nil {
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          env.TenantID,
				EventCode:         "MEMBER_WELCOME",
				Priority:          "normal",
				Channels:          []notifier.Channel{notifier.ChannelSMS, notifier.ChannelEmail, notifier.ChannelInApp},
				RecipientMemberID: &activation.CounterpartyID,
				RecipientName:     resolved.ApplicantName,
				RecipientPhone:    resolved.PrimaryPhone,
				RecipientEmail:    resolved.PrimaryEmail,
				SourceModule:      strPtrConst("member.onboarding.activation"),
				SourceRecordID:    &resolved.ID,
				Payload: map[string]any{
					"member_no":          activation.MemberNo,
					"share_account_no":   activation.ShareAccountNo,
					"deposit_account_no": derefStringPtr(activation.DepositAccountNo),
					"applicant_name":     resolved.ApplicantName,
					"application_no":     resolved.ApplicationNo,
				},
			})
		}
	}
	httpx.OK(w, resolved)
}

func strFromCtx(ctx map[string]any, key string) string {
	if v, ok := ctx[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
