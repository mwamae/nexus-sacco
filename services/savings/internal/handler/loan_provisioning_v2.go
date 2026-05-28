// Phase 3 — provisioning v2 HTTP surface.
//
// Mounted at /v1/loans/provisioning/* with NEW perms:
//
//   GET    /v1/loans/provisioning/runs              loans:view
//   GET    /v1/loans/provisioning/runs/{run_id}     loans:view
//   POST   /v1/loans/provisioning/runs              loans:provisioning:run
//   POST   /v1/loans/provisioning/runs/{run_id}/post    loans:provisioning:post
//   POST   /v1/loans/provisioning/runs/{run_id}/cancel  loans:provisioning:run
//
// The legacy /v1/provisioning/* stays mounted alongside for one
// release; new UI hits v2, old UI keeps working.
//
// Workflow per the prompt:
//   1. POST /v1/loans/provisioning/runs {period_month: "2026-04-01"}
//        → 'draft' run computed from dpd_snapshots × ecl_rate_matrix
//   2. POST .../runs/{id}/post  → posts the movement JE, status='posted'
//   3. POST .../runs/{id}/cancel {reason: "..."} on a draft, sets
//      status='cancelled' and frees the (tenant, period_month) slot
//      for a re-run.

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

type LoanProvisioningV2Handler struct {
	V2      *store.LoanProvisioningV2Store
	Legacy  *store.ProvisioningStore // reuse Get / List / LinesByRun reads
	Posting *posting.Client
	Logger  *slog.Logger
}

// ─────────── Create ───────────

type createRunV2Req struct {
	PeriodMonth string `json:"period_month"` // YYYY-MM-DD; any day in the target month is OK
	Notes       string `json:"notes,omitempty"`
}

func (h *LoanProvisioningV2Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in createRunV2Req
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	period, err := time.Parse("2006-01-02", in.PeriodMonth)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("period_month must be YYYY-MM-DD (any day in the target month)"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var notesPtr *string
	if in.Notes != "" {
		notesPtr = &in.Notes
	}
	run, err := h.V2.CreateRunV2(r.Context(), tid, period, notesPtr, uid)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrProvRunForMonthExists):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		case errors.Is(err, store.ErrProvNoSnapshots):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		default:
			h.Logger.Error("create provisioning v2 run", "err", err)
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.Created(w, run)
}

// ─────────── Cancel ───────────

type cancelRunReq struct {
	Reason string `json:"reason"`
}

func (h *LoanProvisioningV2Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	var in cancelRunReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	run, err := h.V2.CancelRunV2(r.Context(), tid, runID, uid, in.Reason)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrProvRunNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("provisioning run not found"))
		case errors.Is(err, store.ErrProvRunNotCancellable):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		default:
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.OK(w, run)
}

// ─────────── Post (commit GL movement) ───────────

// postingcheck:ignore — like the legacy provisioning handler, this
// posts the JE after the store tx commits. Same migration to
// WithTenantTx + outbox is tracked separately. Until then, an
// accounting outage during the call leaves the run in 'draft' (NOT
// 'posted') so a retry is safe — the post is idempotent on
// status='draft'.
func (h *LoanProvisioningV2Handler) Post(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	run, err := h.Legacy.Get(r.Context(), tid, runID)
	if err != nil {
		if errors.Is(err, store.ErrProvRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("provisioning run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if run.Status != "draft" && run.Status != "computed" {
		httpx.WriteErr(w, r, httpx.ErrConflict(
			fmt.Sprintf("run is %s — only draft / computed runs can be posted", run.Status)))
		return
	}

	movement := run.Movement
	sourceRef := fmt.Sprintf("loan-provision-v2-%s", run.ID)

	if movement.IsZero() {
		// No JE needed — mark posted with a synthetic ref.
		updated, err := h.V2.MarkPostedV2(r.Context(), tid, run.ID, sourceRef, nil, uid)
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		httpx.OK(w, updated)
		return
	}

	periodForJE := run.AsOfDate
	if run.PeriodMonth != nil {
		periodForJE = *run.PeriodMonth
	}
	narration := fmt.Sprintf(
		"Loan loss provisioning movement %s (period %s, %d loans, total provision %s)",
		movement.StringFixed(2), periodForJE.Format("2006-01"),
		run.LoansClassified, run.TotalProvision.StringFixed(2),
	)

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

	err = h.Posting.Post(r.Context(), posting.PostInput{
		TenantID:     tid,
		EntryDate:    periodForJE,
		ValueDate:    periodForJE,
		SourceModule: "savings.loan_provisioning_v2",
		SourceRef:    sourceRef,
		Narration:    narration,
		Lines:        lines,
	})
	if err != nil {
		if errors.Is(err, posting.ErrPostingDisabled) {
			updated, mErr := h.V2.MarkPostedV2(r.Context(), tid, run.ID, "posting-disabled", nil, uid)
			if mErr != nil {
				httpx.WriteErr(w, r, mErr)
				return
			}
			httpx.OK(w, updated)
			return
		}
		h.Logger.Error("post v2 provisioning to GL", "run", run.ID, "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}

	updated, err := h.V2.MarkPostedV2(r.Context(), tid, run.ID, sourceRef, nil, uid)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Reads — reuse the legacy store reads ───────────

func (h *LoanProvisioningV2Handler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	items, err := h.Legacy.List(r.Context(), tid, 50)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *LoanProvisioningV2Handler) Get(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	run, err := h.Legacy.Get(r.Context(), tid, runID)
	if err != nil {
		if errors.Is(err, store.ErrProvRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("provisioning run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	lines, err := h.Legacy.LinesByRun(r.Context(), tid, runID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"run": run, "lines": lines})
}
