// Loan-application ↔ workflow-service bridge (Unified Inbox PR #4).
//
// Two endpoints replace the legacy inline /v1/loan-applications/{id}/
// approve + /decline buttons when the tenant has unified_inbox_enabled:
//
//   POST /v1/loan-applications/{id}/submit-for-decision
//        Frontend CTA. Creates a workflow_instance of kind
//        'loan_application_decision', back-links it on the app row,
//        and returns the instance id so the page can deep-link to
//        /approvals/{id}. Idempotent — second call returns the
//        existing instance instead of creating a duplicate.
//
//   POST /internal/v1/loan-applications/{id}/resolve
//        Service-to-service callback target. The workflow engine
//        POSTs here when the wf_instance reaches a terminal state.
//        Mirrors the decision back onto loan_applications.status
//        + stamps approved_amount/term/rate from the workflow's
//        context. Idempotent on already-terminal status so a
//        redelivered webhook can't re-stamp the fields.

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

// ─────────── POST /v1/loan-applications/{id}/submit-for-decision ───────────

type submitDecisionResponse struct {
	WorkflowInstanceID uuid.UUID `json:"workflow_instance_id"`
	Status             string    `json:"status"`
}

func (h *LoanApplicationHandler) SubmitForDecision(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if h.WorkflowURL == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("workflow service not configured (WORKFLOW_SERVICE_URL)"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	actorID, _ := middleware.UserIDFrom(r)

	var app *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		got, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		app = got
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Idempotent: if we've already wired this app to a workflow
	// instance, return the existing one — re-clicking the CTA must
	// not spawn duplicates.
	if app.WorkflowInstanceID != nil {
		httpx.OK(w, submitDecisionResponse{
			WorkflowInstanceID: *app.WorkflowInstanceID,
			Status:             "existing",
		})
		return
	}

	// Only certain FSM states make sense to submit. The legacy buttons
	// were available at pending_approval; we extend slightly to cover
	// the auto-create-on-first-open path for in-flight rows.
	if !canSubmitForDecision(app.Status) {
		httpx.WriteErr(w, r, httpx.ErrConflict(fmt.Sprintf("cannot submit application in status=%s for credit decision", app.Status)))
		return
	}

	wfID, err := h.createLoanAppWorkflowInstance(r, tenantID, app, actorID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Back-link on the row so the auto-create path and the CTA
	// can't race into two instances.
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(),
			`UPDATE loan_applications SET workflow_instance_id = $1 WHERE id = $2`,
			wfID, id)
		return err
	}); err != nil {
		// Compensate: cancel the wf_instance we just created so the
		// frontend doesn't see a ghost row in the Inbox.
		h.cancelLoanAppWorkflowInstance(r, wfID)
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, submitDecisionResponse{
		WorkflowInstanceID: wfID,
		Status:             "created",
	})
}

// canSubmitForDecision encodes the legal FSM states from which a
// credit decision can be requested. Mirrors what the inline Approve
// button required, with the small relaxations the auto-create path
// needs (it fires on first page load for in-flight apps).
func canSubmitForDecision(s domain.LoanAppStatus) bool {
	switch s {
	case domain.AppPendingApproval,
		domain.AppPendingScoring,
		domain.AppPendingValidation,
		domain.AppReturnedForInfo:
		return true
	}
	return false
}

// createLoanAppWorkflowInstance POSTs the workflow service. Mirrors
// the existing interest.go createWorkflowInstance pattern — same
// auth forwarding (Authorization header + Host).
func (h *LoanApplicationHandler) createLoanAppWorkflowInstance(r *http.Request, tenantID uuid.UUID, app *domain.LoanApplication, actorID uuid.UUID) (uuid.UUID, error) {
	_ = tenantID
	callback := ""
	if h.SavingsSelfURL != "" {
		callback = strings.TrimRight(h.SavingsSelfURL, "/") +
			fmt.Sprintf("/internal/v1/loan-applications/%s/resolve", app.ID)
	}
	// Context the engine evaluates conditions against + the Inbox
	// renders in the per-process payload component.
	ctx := map[string]any{
		"application_id":     app.ID.String(),
		"application_no":     app.ApplicationNo,
		"counterparty_id":    app.CounterpartyID.String(),
		"product_id":         app.ProductID.String(),
		"requested_amount":   app.RequestedAmount,
		"requested_term":     app.RequestedTermMonths,
		"monthly_net_income": app.MonthlyNetIncome,
		"status":             string(app.Status),
	}
	if app.CreditScore != nil {
		ctx["credit_score"] = *app.CreditScore
	}
	if app.RiskBand != nil {
		ctx["risk_band"] = *app.RiskBand
	}
	if app.AffordabilityPass != nil {
		ctx["affordability_pass"] = *app.AffordabilityPass
	}
	if app.DTIRatio != nil {
		ctx["dti_ratio"] = *app.DTIRatio
	}
	body, _ := json.Marshal(map[string]any{
		"process_kind": "loan_application_decision",
		"subject_kind": "loan_application",
		"subject_id":   app.ID,
		"context":      ctx,
		"callback_url": callback,
		"initiator_id": actorID,
		"summary":      fmt.Sprintf("Loan %s — %s requested KES %s", app.ApplicationNo, app.CounterpartyID, app.RequestedAmount.String()),
		"source_url":   fmt.Sprintf("/loans/applications/%s", app.ID),
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
	var envelope struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return uuid.Nil, err
	}
	if envelope.Data.ID == uuid.Nil {
		return uuid.Nil, httpx.ErrConflict("workflow service returned no instance id")
	}
	return envelope.Data.ID, nil
}

func (h *LoanApplicationHandler) cancelLoanAppWorkflowInstance(r *http.Request, wfID uuid.UUID) {
	// Best-effort compensation; logged but never bubbled up.
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

func (h *LoanApplicationHandler) httpClient() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ─────────── POST /internal/v1/loan-applications/{id}/resolve ───────────
//
// Workflow callback target. Same auth model as the pending-approvals
// resolve endpoint (PR #3): X-Internal-Token in production, User-
// Agent prefix in dev. Idempotent — already-terminal status returns
// 200 no-op.

type loanAppResolveEnvelope struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Event    string    `json:"event"`
	Instance struct {
		ID      uuid.UUID      `json:"id"`
		Context map[string]any `json:"context"`
	} `json:"instance"`
}

func (h *LoanApplicationHandler) ResolveFromWorkflow(w http.ResponseWriter, r *http.Request) {
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
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var env loanAppResolveEnvelope
	if err := httpx.DecodeJSON(r, &env); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if env.TenantID == uuid.Nil || env.Event == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id and event required"))
		return
	}

	var resolved *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Idempotency: terminal status → no-op. Covers redelivered
		// callbacks AND the case where a human raced the workflow by
		// hitting the legacy /approve endpoint.
		if isLoanAppTerminal(app.Status) {
			resolved = app
			return nil
		}
		// Workflow callbacks come from a system actor, not a human;
		// stamp the row's existing creator as the "approver of record"
		// for now (the real approver audit lives in wf_actions for
		// the instance and is visible from /approvals).
		actor := app.CreatedBy
		switch env.Event {
		case "approved":
			amount, term, rate := approvedFieldsFromContext(env.Instance.Context, app)
			updated, uerr := h.Applications.UpdateStatusTx(r.Context(), tx, app.ID, store.AppTransition{
				To:                  domain.AppApproved,
				By:                  actor,
				ApprovedAmount:      &amount,
				ApprovedTermMonths:  &term,
				ApprovedInterestPct: rate,
			})
			if uerr != nil {
				return uerr
			}
			resolved = updated
		case "rejected":
			updated, uerr := h.Applications.UpdateStatusTx(r.Context(), tx, app.ID, store.AppTransition{
				To:              domain.AppDeclined,
				By:              actor,
				DeclineCategory: strFromContext(env.Instance.Context, "decline_category"),
				DeclineReason:   strFromContext(env.Instance.Context, "decline_reason"),
			})
			if uerr != nil {
				return uerr
			}
			resolved = updated
		case "cancelled":
			updated, uerr := h.Applications.UpdateStatusTx(r.Context(), tx, app.ID, store.AppTransition{
				To: domain.AppCancelled,
				By: actor,
			})
			if uerr != nil {
				return uerr
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
	httpx.OK(w, resolved)
}

func isLoanAppTerminal(s domain.LoanAppStatus) bool {
	switch s {
	case domain.AppApproved, domain.AppApprovedWithConditions,
		domain.AppDeclined, domain.AppCancelled, domain.AppExpired,
		domain.AppDisbursed:
		return true
	}
	return false
}

// approvedFieldsFromContext lifts amount/term/rate from the workflow
// context payload, falling back to the application's recommended-then-
// requested values when the Board didn't explicitly override them.
func approvedFieldsFromContext(ctx map[string]any, app *domain.LoanApplication) (decimal.Decimal, int, *decimal.Decimal) {
	amount := app.RequestedAmount
	if app.RecommendedAmount != nil {
		amount = *app.RecommendedAmount
	}
	if v, ok := ctx["approved_amount"]; ok {
		if d, err := decimalFromAny(v); err == nil {
			amount = d
		}
	}
	term := app.RequestedTermMonths
	if app.RecommendedTermMonths != nil {
		term = *app.RecommendedTermMonths
	}
	if v, ok := ctx["approved_term_months"]; ok {
		if n, ok2 := intFromAny(v); ok2 {
			term = n
		}
	}
	var rate *decimal.Decimal
	if v, ok := ctx["approved_interest_rate_pct"]; ok {
		if d, err := decimalFromAny(v); err == nil {
			rate = &d
		}
	}
	return amount, term, rate
}

func decimalFromAny(v any) (decimal.Decimal, error) {
	switch x := v.(type) {
	case string:
		return decimal.NewFromString(x)
	case float64:
		return decimal.NewFromFloat(x), nil
	case json.Number:
		return decimal.NewFromString(x.String())
	}
	return decimal.Zero, errors.New("decimal: unsupported type")
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case json.Number:
		i, err := x.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func strFromContext(ctx map[string]any, key string) *string {
	if v, ok := ctx[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return &s
		}
	}
	return nil
}

