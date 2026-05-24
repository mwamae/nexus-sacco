// Unified Inbox bridge for manual journal entries + reversals (PR #7).
//
// Three pieces:
//
//   • tenantHasUnifiedInbox(...)   per-request feature gate
//   • createJEWorkflowInstance(...) shared helper: POSTs the workflow
//                                  service with the right kind +
//                                  context for both manual entries
//                                  and reversals
//   • ResolveFromWorkflow handler   /internal/v1/journal-entries/resolve
//                                  callback target. Single endpoint
//                                  handles both kinds — dispatches by
//                                  the stored entry's reversal_of field.
//
// The reversal endpoint (POST /v1/journal-entries/{id}/reverse) is
// also in this file since it's a thin wrapper around store.CreateReversalDraftTx
// + the same workflow helper.

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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

// tenantHasUnifiedInbox queries the tenants table once per request.
// Failures fall back to false so the legacy maker/checker path stays
// usable if the column is somehow missing (older identity migration).
func (h *JournalHandler) tenantHasUnifiedInbox(ctx context.Context, tenantID uuid.UUID) bool {
	var enabled bool
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(unified_inbox_enabled, false) FROM tenants WHERE id = $1`,
			tenantID).Scan(&enabled)
	})
	if err != nil {
		return false
	}
	return enabled
}

// createJEWorkflowInstance POSTs the workflow service to create a
// workflow_instance. processKind is one of "manual_journal_entry"
// or "journal_reversal" — the engine's seeded definition for each
// has its own level structure and conditional rules.
func (h *JournalHandler) createJEWorkflowInstance(
	r *http.Request, tenantID uuid.UUID,
	entry *domain.JournalEntry,
	totalDebit decimal.Decimal, affectsEquity bool,
	actorID uuid.UUID, processKind string,
) (uuid.UUID, error) {
	callback := ""
	if h.AccountingSelfURL != "" {
		callback = strings.TrimRight(h.AccountingSelfURL, "/") + "/internal/v1/journal-entries/resolve"
	}
	summary := fmt.Sprintf("Journal — %s · KES %s", entry.Narration, totalDebit.StringFixed(2))
	if processKind == "journal_reversal" && entry.ReversalOf != nil {
		summary = fmt.Sprintf("Reversal of %s · KES %s", entry.ReversalOf.String()[:8], totalDebit.StringFixed(2))
	}
	body, _ := json.Marshal(map[string]any{
		"process_kind": processKind,
		"subject_kind": "journal_entry",
		"subject_id":   entry.ID,
		"context": map[string]any{
			"entry_id":       entry.ID.String(),
			"narration":      entry.Narration,
			"amount":         totalDebit, // shopspring serialises as quoted string; jsonlogic parses it as a number
			"affects_equity": affectsEquity,
			"entry_type":     string(entry.EntryType),
		},
		"callback_url": callback,
		"initiator_id": actorID,
		"summary":      summary,
		"source_url":   fmt.Sprintf("/accounting/journal-entries?id=%s", entry.ID),
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

func (h *JournalHandler) httpClient() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ─────────── POST /v1/journal-entries/{id}/reverse ───────────
//
// Creates an inverse-lines draft entry that points at the original
// via reversal_of, then submits it to the journal_reversal workflow
// (Board-only). The reversal POSTS to the GL on Board approval via
// the resolve callback — same path as a manual entry above
// threshold.

type reverseEntryReq struct {
	ReversalDate string `json:"reversal_date,omitempty"` // YYYY-MM-DD; defaults to today
	Narration    string `json:"narration,omitempty"`     // prepended to the original's narration
}

func (h *JournalHandler) Reverse(w http.ResponseWriter, r *http.Request) {
	origID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in reverseEntryReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	revDate := time.Now().UTC()
	if in.ReversalDate != "" {
		t, err := time.Parse("2006-01-02", in.ReversalDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("reversal_date must be YYYY-MM-DD"))
			return
		}
		revDate = t
	}
	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)

	// Build the inverse-lines draft + capture totals for the workflow
	// payload. Whole flow lives in one tx so a workflow failure can
	// roll the draft back.
	var (
		draft         *domain.JournalEntry
		totalDebit    decimal.Decimal
		affectsEquity bool
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		orig, lines, err := h.loadOriginalForReverse(r.Context(), tx, origID)
		if err != nil {
			return err
		}
		if _, perr := h.Periods.EnsureOpenForDateTx(r.Context(), tx, revDate); perr != nil {
			return httpx.ErrConflict(perr.Error())
		}
		narration := "Reversal of " + orig.Narration
		if in.Narration != "" {
			narration = in.Narration + " — " + narration
		}
		// Swap each line's debit/credit. Account class for affects_equity
		// is the same as the original's lines (no new accounts touched).
		revLines := make([]store.EntryLineInput, 0, len(lines))
		for _, ln := range lines {
			revLines = append(revLines, store.EntryLineInput{
				AccountID: ln.AccountID,
				Debit:     ln.Credit,
				Credit:    ln.Debit,
				Narration: "Reverse: " + ln.Narration,
			})
			totalDebit = totalDebit.Add(ln.Credit) // line.Credit becomes draft's debit
			if ln.AccountClass == string(domain.ClassEquity) {
				affectsEquity = true
			}
		}
		creator := nullableUserID(actor)
		entry, cerr := h.Journals.CreateDraftTx(r.Context(), tx, store.CreateEntryInput{
			EntryDate: revDate, ValueDate: revDate,
			EntryType: domain.TypeReversal,
			Narration: narration,
			Lines:     revLines, CreatedBy: creator,
		})
		if cerr != nil {
			return mapStoreErr(cerr)
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE journal_entries SET reversal_of = $1 WHERE id = $2`, origID, entry.ID); err != nil {
			return err
		}
		entry.ReversalOf = &origID
		draft = entry
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Reversals always go to Board via the journal_reversal workflow.
	// When the workflow service isn't configured we leave the draft in
	// pending_approval so an operator can still act via the legacy
	// Approve endpoint.
	if h.WorkflowURL != "" {
		wfID, wfErr := h.createJEWorkflowInstance(r, tid, draft, totalDebit, affectsEquity, actor, "journal_reversal")
		if wfErr != nil {
			httpx.WriteErr(w, r, wfErr)
			return
		}
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			_, e := tx.Exec(r.Context(),
				`UPDATE journal_entries SET workflow_instance_id = $1 WHERE id = $2`, wfID, draft.ID)
			return e
		})
		draft.WorkflowInstanceID = &wfID
	}
	httpx.Created(w, draft)
}

// reverseLine carries just the fields we need to build the inverse
// (no full domain.JournalLine — that lacks AccountClass).
type reverseLine struct {
	AccountID    uuid.UUID
	AccountClass string
	Debit        decimal.Decimal
	Credit       decimal.Decimal
	Narration    string
}

// loadOriginalForReverse fetches the original entry + a flat slice
// of its lines joined to the chart of accounts so the class lookup
// is one round-trip. The original must be in status='posted'; we
// don't allow reversing a draft or rejected entry.
func (h *JournalHandler) loadOriginalForReverse(ctx context.Context, tx pgx.Tx, origID uuid.UUID) (*domain.JournalEntry, []reverseLine, error) {
	orig, err := h.Journals.GetTx(ctx, tx, origID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, httpx.ErrNotFound("original journal entry not found")
	}
	if err != nil {
		return nil, nil, err
	}
	if orig.Status != domain.JournalEntryStatus("posted") {
		return nil, nil, httpx.ErrConflict("only posted journal entries can be reversed (current status: " + string(orig.Status) + ")")
	}
	rows, err := tx.Query(ctx, `
		SELECT l.account_id, COALESCE(a.class::text, ''), l.debit, l.credit, COALESCE(l.narration,'')
		  FROM journal_lines l JOIN accounts a ON a.id = l.account_id
		 WHERE l.entry_id = $1
		 ORDER BY l.line_no`, origID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var lines []reverseLine
	for rows.Next() {
		var rl reverseLine
		if err := rows.Scan(&rl.AccountID, &rl.AccountClass, &rl.Debit, &rl.Credit, &rl.Narration); err != nil {
			return nil, nil, err
		}
		lines = append(lines, rl)
	}
	if len(lines) < 2 {
		return nil, nil, httpx.ErrConflict("original entry has < 2 lines; cannot reverse")
	}
	return orig, lines, nil
}

// ─────────── POST /internal/v1/journal-entries/resolve ───────────
//
// Workflow callback target. Single endpoint serves both manual JE
// and reversal kinds — the JE row's reversal_of distinguishes them,
// but the resolve action is the same (approve → post + allocate
// entry_no; reject → mark rejected). Idempotent on terminal status.

type jeResolveEnvelope struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Event    string    `json:"event"`
	Instance struct {
		ID uuid.UUID `json:"id"`
	} `json:"instance"`
}

func (h *JournalHandler) ResolveFromWorkflow(w http.ResponseWriter, r *http.Request) {
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
	var env jeResolveEnvelope
	if err := httpx.DecodeJSON(r, &env); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if env.TenantID == uuid.Nil || env.Event == "" || env.Instance.ID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id, event, instance.id required"))
		return
	}

	var resolved *domain.JournalEntry
	err := h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
		// Reverse-lookup the JE by wf_instance_id.
		var entryID uuid.UUID
		err := tx.QueryRow(r.Context(),
			`SELECT id FROM journal_entries WHERE workflow_instance_id = $1 LIMIT 1`,
			env.Instance.ID).Scan(&entryID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Not ours — silently ack.
			return nil
		}
		if err != nil {
			return err
		}
		entry, err := h.Journals.GetTx(r.Context(), tx, entryID)
		if err != nil {
			return err
		}
		// Idempotency — already terminal → no-op.
		if entry.Status == domain.JournalEntryStatus("posted") ||
			entry.Status == domain.JournalEntryStatus("rejected") {
			resolved = entry
			return nil
		}
		// Use the creator as the system actor for posting attribution.
		// The actual human approver lives in wf_actions on the
		// workflow instance and is visible from /approvals.
		var actor uuid.UUID
		if entry.CreatedBy != nil {
			actor = *entry.CreatedBy
		}
		switch env.Event {
		case "approved":
			posted, err := h.Journals.ApproveAndPostTx(r.Context(), tx, entry.ID, actor)
			if err != nil {
				return err
			}
			resolved = posted
		case "rejected", "cancelled":
			if err := h.Journals.RejectTx(r.Context(), tx, entry.ID, actor,
				"workflow "+env.Event); err != nil {
				return err
			}
			resolved, _ = h.Journals.GetTx(r.Context(), tx, entry.ID)
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
