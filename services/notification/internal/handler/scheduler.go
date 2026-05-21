// Scheduled-jobs HTTP endpoints.
//
//   GET  /v1/scheduled-jobs              list jobs for tenant
//   GET  /v1/scheduled-jobs/{id}         detail
//   PUT  /v1/scheduled-jobs/{id}         update cron_expr / is_active
//   POST /v1/scheduled-jobs/{id}/run     manual one-off trigger
//   GET  /v1/scheduled-jobs/{id}/runs    recent run history
//   POST /v1/scheduled-jobs/preview-cron sanity-check a cron expression

package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
	"github.com/nexussacco/notification/internal/worker"
)

type SchedulerHandler struct {
	DB        *db.Pool
	Sched     *store.SchedulerStore
	Scheduler *worker.Scheduler // for NextFiring + handler lookup on manual runs
	Logger    *slog.Logger
}

func (h *SchedulerHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.ScheduledJob
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Sched.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Annotate each row with the next firing the cron expression would
	// produce — useful when the user has just changed the schedule.
	type jobOut struct {
		domain.ScheduledJob
		NextComputed *time.Time `json:"next_computed,omitempty"`
	}
	out := make([]jobOut, 0, len(items))
	now := time.Now()
	for _, j := range items {
		row := jobOut{ScheduledJob: j}
		if n, err := h.Scheduler.NextFiring(j.CronExpr, now); err == nil {
			row.NextComputed = &n
		}
		out = append(out, row)
	}
	httpx.OK(w, map[string]any{"items": out})
}

func (h *SchedulerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var j *domain.ScheduledJob
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		j, err = h.Sched.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, j)
}

type updateJobReq struct {
	CronExpr string `json:"cron_expr"`
	IsActive *bool  `json:"is_active,omitempty"`
}

func (h *SchedulerHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in updateJobReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CronExpr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("cron_expr is required"))
		return
	}
	next, perr := h.Scheduler.NextFiring(in.CronExpr, time.Now())
	if perr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid cron expression: "+perr.Error()))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.ScheduledJob
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		cur, err := h.Sched.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		isActive := cur.IsActive
		if in.IsActive != nil {
			isActive = *in.IsActive
		}
		if err := h.Sched.UpdateScheduleTx(r.Context(), tx, id, in.CronExpr, isActive, next); err != nil {
			return err
		}
		out, err = h.Sched.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// Run — manually triggers a job for the current tenant. Runs synchronously
// in the request goroutine; admin UI shows a spinner. For long jobs the
// run row is still created so the user can refresh and watch progress.
func (h *SchedulerHandler) Run(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var job *domain.ScheduledJob
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		job, err = h.Sched.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	handler, ok := h.Scheduler.Registry.Get(job.JobKey)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrConflict("no handler registered for "+job.JobKey))
		return
	}

	now := time.Now()
	var runID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		runID, err = h.Sched.CreateRunTx(r.Context(), tx, job, now)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	processed, failed, runErr := handler(r.Context(), h.DB, tid, job)
	status := "succeeded"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
		h.Logger.Error("manual job run failed", "job_key", job.JobKey, "err", runErr)
	}
	_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Sched.FinishRunTx(r.Context(), tx, runID, processed, failed, status, errMsg)
	})
	httpx.OK(w, map[string]any{
		"run_id":    runID,
		"status":    status,
		"processed": processed,
		"failed":    failed,
	})
}

func (h *SchedulerHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var runs []domain.JobRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		runs, err = h.Sched.ListRunsTx(r.Context(), tx, id, limit)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": runs})
}

type previewCronReq struct {
	CronExpr string `json:"cron_expr"`
}

func (h *SchedulerHandler) PreviewCron(w http.ResponseWriter, r *http.Request) {
	var in previewCronReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CronExpr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("cron_expr is required"))
		return
	}
	now := time.Now()
	firings := make([]time.Time, 0, 5)
	cursor := now
	for i := 0; i < 5; i++ {
		n, err := h.Scheduler.NextFiring(in.CronExpr, cursor)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid cron expression: "+err.Error()))
			return
		}
		firings = append(firings, n)
		cursor = n
	}
	httpx.OK(w, map[string]any{"next_firings": firings})
}
