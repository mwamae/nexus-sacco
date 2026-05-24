// Unified Inbox bridge for the year-end close (PR #6).
//
// Two endpoints replace the legacy POST /v1/fiscal-years/{year}/close
// once the tenant has unified_inbox_enabled:
//
//   POST /v1/fiscal-years/{year}/submit-for-close
//        Frontend CTA. Creates a year_end_close wf_instance + a
//        fiscal_year_close_proposals row. Idempotent — re-clicking
//        returns the existing pending proposal.
//
//   POST /internal/v1/fiscal-year-close-proposals/{id}/resolve
//        Service-to-service callback. On approve fires the same
//        executeCloseTx logic the legacy Close handler uses; on
//        reject/cancel marks the proposal terminal.

package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

// Fields the host service needs to call into the workflow service +
// authenticate the callback. Same conventions as the savings/member
// integrations from PR #3-5.
type FiscalYearWorkflowFields struct {
	WorkflowURL           string
	AccountingSelfURL     string
	WorkflowInternalToken string
	HTTP                  *http.Client
}

// SubmitForClose creates the workflow instance and proposal row.
// Idempotent: a second click while a proposal is pending returns the
// existing workflow id.
func (h *FiscalYearHandler) SubmitForClose(w http.ResponseWriter, r *http.Request) {
	if h.WorkflowURL == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("workflow service not configured (WORKFLOW_SERVICE_URL)"))
		return
	}
	yearStr := chi.URLParam(r, "year")
	year, err := strconv.Atoi(yearStr)
	if err != nil || year < 2000 || year > 2999 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("year must be a 4-digit number"))
		return
	}
	var in closeYearReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	// Reject if year already closed — same gate the legacy Close uses.
	var alreadyClosed bool
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		c, err := h.FY.IsClosedTx(r.Context(), tx, year)
		alreadyClosed = c
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if alreadyClosed {
		httpx.WriteErr(w, r, httpx.ErrConflict("fiscal year "+yearStr+" is already closed"))
		return
	}

	// Idempotency — return any in-flight pending proposal instead of
	// trying to create a second one (the partial unique index would
	// reject anyway, but this gives the UI a usable response).
	var existing *store.FiscalYearCloseProposal
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		p, err := h.Proposals.PendingForYearTx(r.Context(), tx, year)
		if err == nil {
			existing = p
		}
		return nil
	})
	if existing != nil {
		httpx.OK(w, map[string]any{
			"workflow_instance_id": existing.WorkflowInstanceID,
			"proposal_id":          existing.ID,
			"status":               "existing",
		})
		return
	}

	wfID, err := h.createCloseWorkflowInstance(r, tenantID, year, in.Notes, userID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var proposal *store.FiscalYearCloseProposal
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		p, err := h.Proposals.CreateTx(r.Context(), tx, tenantID, year, wfID, in.Notes, userID)
		if err != nil {
			return err
		}
		proposal = p
		return nil
	}); err != nil {
		// Compensate: cancel the wf_instance so a ghost row doesn't
		// linger in the Inbox.
		h.cancelWorkflowInstance(r, wfID)
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"workflow_instance_id": wfID,
		"proposal_id":          proposal.ID,
		"status":               "created",
	})
}

func (h *FiscalYearHandler) createCloseWorkflowInstance(r *http.Request, tenantID uuid.UUID, year int, notes string, actorID uuid.UUID) (uuid.UUID, error) {
	_ = tenantID
	callback := ""
	if h.AccountingSelfURL != "" {
		callback = strings.TrimRight(h.AccountingSelfURL, "/") + "/internal/v1/fiscal-year-close-proposals/resolve"
	}
	yearStr := strconv.Itoa(year)
	body, _ := json.Marshal(map[string]any{
		"process_kind": "year_end_close",
		"subject_kind": "fiscal_year",
		"subject_id":   uuid.New(), // synthetic — workflow.subject_id is uuid; fiscal years don't have a uuid PK
		"context": map[string]any{
			"year":  year,
			"notes": notes,
		},
		"callback_url": callback,
		"initiator_id": actorID,
		"summary":      fmt.Sprintf("Year-end close · FY %s", yearStr),
		"source_url":   "/accounting/year-end-close",
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

func (h *FiscalYearHandler) cancelWorkflowInstance(r *http.Request, wfID uuid.UUID) {
	body, _ := json.Marshal(map[string]any{"action": "cancel", "comments": "proposal write failed; compensating"})
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

func (h *FiscalYearHandler) httpClient() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ─────────── POST /internal/v1/fiscal-year-close-proposals/resolve ───────────
//
// Workflow callback target. The instance carries the proposal id in
// its context (the proposal row also stores the workflow id, so
// either side can dereference); we look up the proposal via the
// engine's instance id field on the envelope.

type fyResolveEnvelope struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Event    string    `json:"event"`
	Instance struct {
		ID uuid.UUID `json:"id"`
	} `json:"instance"`
}

func (h *FiscalYearHandler) ResolveFromWorkflow(w http.ResponseWriter, r *http.Request) {
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
	var env fyResolveEnvelope
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
		// Look up the proposal by workflow instance id.
		var proposal store.FiscalYearCloseProposal
		row := tx.QueryRow(r.Context(), `
			SELECT id, tenant_id, year, workflow_instance_id, notes,
			       submitted_by, submitted_at, status, applied_close_id, resolved_at, resolved_note
			  FROM fiscal_year_close_proposals
			 WHERE workflow_instance_id = $1
			 LIMIT 1`, env.Instance.ID)
		if err := row.Scan(
			&proposal.ID, &proposal.TenantID, &proposal.Year, &proposal.WorkflowInstanceID, &proposal.Notes,
			&proposal.SubmittedBy, &proposal.SubmittedAt, &proposal.Status, &proposal.AppliedCloseID,
			&proposal.ResolvedAt, &proposal.ResolvedNote); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Not ours — silently ack so the workflow doesn't retry.
				return nil
			}
			return err
		}
		// Idempotency — non-pending proposal is a no-op.
		if proposal.Status != "pending" {
			out = proposal
			return nil
		}
		switch env.Event {
		case "approved":
			notes := ""
			if proposal.Notes != nil {
				notes = *proposal.Notes
			}
			fyStart := time.Date(proposal.Year, 1, 1, 0, 0, 0, 0, time.UTC)
			fyEnd := time.Date(proposal.Year, 12, 31, 23, 59, 59, 0, time.UTC)
			recorded, err := h.executeCloseTx(r.Context(), tx, proposal.Year, fyStart, fyEnd, proposal.SubmittedBy, notes)
			if err != nil {
				// Stamp the failure so the operator can triage; don't
				// mark applied.
				_ = h.Proposals.SetTerminalTx(r.Context(), tx, proposal.ID, "approved", "execute failed: "+err.Error())
				return err
			}
			if err := h.Proposals.SetAppliedTx(r.Context(), tx, proposal.ID, recorded.ID, ""); err != nil {
				return err
			}
			out = recorded
		case "rejected", "cancelled":
			if err := h.Proposals.SetTerminalTx(r.Context(), tx, proposal.ID, env.Event, ""); err != nil {
				return err
			}
			out = proposal
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
