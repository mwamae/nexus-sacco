// Provisioning HTTP surface.
//
// Workflow:
//   1. POST /v1/provisioning/runs           — compute snapshot for a date
//   2. POST /v1/provisioning/runs/{id}/post — post the GL movement
//                                              DR 5210 Provisioning Expense
//                                              CR 1120 Loan Loss Provision
//                                              (legs flip on a release)
//   3. POST /v1/provisioning/runs/{id}/supersede — admin override to allow
//                                                   a re-run for the same date
//
// A provision run is two-step on purpose so finance can review the
// snapshot + bucket distribution before committing the movement to
// the ledger. Same maker/checker spirit as interest/dividend runs,
// trimmed down — the calculation is deterministic so a heavy AGM-
// style workflow is overkill.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type ProvisioningHandler struct {
	Store   *store.ProvisioningStore
	Posting *posting.Client
	Logger  *slog.Logger
}

// ─────────── Create (compute snapshot) ───────────

type createProvisioningRunReq struct {
	AsOfDate string `json:"as_of_date"` // YYYY-MM-DD
	Notes    string `json:"notes,omitempty"`
}

func (h *ProvisioningHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createProvisioningRunReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	asOf, err := time.Parse("2006-01-02", in.AsOfDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("as_of_date must be YYYY-MM-DD"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	var notesPtr *string
	if in.Notes != "" {
		notesPtr = &in.Notes
	}

	run, err := h.Store.CreateRun(r.Context(), tid, asOf, notesPtr, userID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrProvAlreadyPosted):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		default:
			h.Logger.Error("create provisioning run", "err", err)
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.Created(w, run)
}

// ─────────── Post (commit GL movement) ───────────

func (h *ProvisioningHandler) Post(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	run, err := h.Store.Get(r.Context(), tid, runID)
	if err != nil {
		if errors.Is(err, store.ErrProvRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("provisioning run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if run.Status != "computed" {
		httpx.WriteErr(w, r, httpx.ErrConflict(
			fmt.Sprintf("run is %s — only computed runs can be posted", run.Status)))
		return
	}

	movement := run.Movement
	if movement.IsZero() {
		// No movement → no GL entry. Mark the run posted directly so the
		// audit trail still reflects that the snapshot was finalised.
		ref := fmt.Sprintf("provision-run-%s", run.ID)
		updated, err := h.Store.MarkPosted(r.Context(), tid, run.ID, ref, userID)
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		httpx.OK(w, updated)
		return
	}

	// Build the journal entry. Movement positive = provisions increased,
	// movement negative = provisions released.
	asOf := run.AsOfDate
	narration := fmt.Sprintf("Loan loss provisioning movement %s (as of %s, %d loans, total provision %s)",
		movement.StringFixed(2), asOf.Format("2006-01-02"),
		run.LoansClassified, run.TotalProvision.StringFixed(2))

	var lines []posting.Line
	if movement.IsPositive() {
		lines = []posting.Line{
			{AccountCode: "5210", Debit: movement, Narration: "Provisioning expense (increase)"},
			{AccountCode: "1120", Credit: movement, Narration: "Loan loss provision (increase)"},
		}
	} else {
		release := movement.Neg()
		lines = []posting.Line{
			{AccountCode: "1120", Debit: release, Narration: "Loan loss provision (release)"},
			{AccountCode: "5210", Credit: release, Narration: "Provisioning expense (release)"},
		}
	}

	sourceRef := fmt.Sprintf("provision-run-%s", run.ID)
	err = h.Posting.Post(r.Context(), posting.PostInput{
		TenantID:     tid,
		EntryDate:    asOf,
		ValueDate:    asOf,
		SourceModule: "savings.provisioning",
		SourceRef:    sourceRef,
		Narration:    narration,
		Lines:        lines,
	})
	if err != nil {
		// If posting is disabled in dev, finalise the run anyway so the UI
		// shows a usable status — but flag in the journal_entry_ref.
		if errors.Is(err, posting.ErrPostingDisabled) {
			updated, mErr := h.Store.MarkPosted(r.Context(), tid, run.ID, "posting-disabled", userID)
			if mErr != nil {
				httpx.WriteErr(w, r, mErr)
				return
			}
			httpx.OK(w, updated)
			return
		}
		h.Logger.Error("post provisioning to GL", "run", run.ID, "err", err)
		_ = h.Store.MarkFailed(r.Context(), tid, run.ID, err.Error())
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}

	updated, err := h.Store.MarkPosted(r.Context(), tid, run.ID, sourceRef, userID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Supersede (admin override) ───────────

func (h *ProvisioningHandler) Supersede(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	if err := h.Store.Supersede(r.Context(), tid, runID); err != nil {
		if errors.Is(err, store.ErrProvRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("posted run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Reads ───────────

func (h *ProvisioningHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	items, err := h.Store.List(r.Context(), tid, 50)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *ProvisioningHandler) Get(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	run, err := h.Store.Get(r.Context(), tid, runID)
	if err != nil {
		if errors.Is(err, store.ErrProvRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("provisioning run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	lines, err := h.Store.LinesByRun(r.Context(), tid, runID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"run": run, "lines": lines})
}
