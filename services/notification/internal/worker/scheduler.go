// Scheduler — runs registered jobs on their cron schedule. Each tick
// (60s) scans for any job whose next_run_at has elapsed, dispatches
// it via its registered handler, logs the run, and bumps next_run_at
// to the next cron firing.
//
// Job handlers are plain functions (signature JobHandler) registered
// at boot — the registry is keyed by the job_key stored on the row.

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

// JobHandler is the worker-side function that executes a scheduled job
// for a single tenant. It is responsible for setting up its own tenant
// context if needed beyond what the framework provides.
//
// Return:
//   processed — count of records the job touched
//   failed    — count of records that failed
//   err       — non-nil sets the run to status=failed; nil is success
type JobHandler func(ctx context.Context, db *db.Pool, tenantID uuid.UUID, job *domain.ScheduledJob) (processed, failed int, err error)

type JobRegistry struct {
	handlers map[string]JobHandler
}

func NewJobRegistry() *JobRegistry { return &JobRegistry{handlers: map[string]JobHandler{}} }

func (r *JobRegistry) Register(key string, h JobHandler) { r.handlers[key] = h }
func (r *JobRegistry) Get(key string) (JobHandler, bool) { h, ok := r.handlers[key]; return h, ok }

// Scheduler — owns the per-tick lifecycle.
type Scheduler struct {
	DB           *db.Pool
	Sched        *store.SchedulerStore
	Notifs       *store.NotificationStore
	Registry     *JobRegistry
	TickInterval time.Duration
	Logger       *slog.Logger
	cronParser   cron.Parser
}

func NewScheduler(d *db.Pool, sched *store.SchedulerStore, notifs *store.NotificationStore, reg *JobRegistry, l *slog.Logger) *Scheduler {
	return &Scheduler{
		DB: d, Sched: sched, Notifs: notifs, Registry: reg, Logger: l,
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	tick := s.TickInterval
	if tick <= 0 {
		tick = 60 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	s.Logger.Info("scheduler started", "tick_seconds", tick.Seconds())
	for {
		select {
		case <-ctx.Done():
			s.Logger.Info("scheduler stopped")
			return
		case now := <-t.C:
			s.processOnce(ctx, now)
		}
	}
}

func (s *Scheduler) processOnce(ctx context.Context, now time.Time) {
	var due []domain.ScheduledJob
	err := s.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		due, err = s.Sched.ListDueAcrossTenantsTx(ctx, tx, now)
		return err
	})
	if err != nil {
		s.Logger.Warn("scheduler: list due failed", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	for _, j := range due {
		s.runOne(ctx, j)
	}
}

func (s *Scheduler) runOne(ctx context.Context, job domain.ScheduledJob) {
	logger := s.Logger.With("job_key", job.JobKey, "tenant", job.TenantID)
	handler, ok := s.Registry.Get(job.JobKey)
	if !ok {
		logger.Warn("scheduler: no handler registered — skipping")
		// Still bump next_run_at so we don't tight-loop.
		s.bumpSchedule(ctx, job)
		return
	}
	scheduledFor := time.Now()
	if job.NextRunAt != nil {
		scheduledFor = *job.NextRunAt
	}

	// Open a run row first so failures get an entry.
	var runID uuid.UUID
	err := s.DB.WithTenantTx(ctx, job.TenantID, func(tx pgx.Tx) error {
		var err error
		runID, err = s.Sched.CreateRunTx(ctx, tx, &job, scheduledFor)
		return err
	})
	if err != nil {
		logger.Warn("scheduler: create run failed", "err", err)
		s.bumpSchedule(ctx, job)
		return
	}

	processed, failed, runErr := handler(ctx, s.DB, job.TenantID, &job)
	status := "succeeded"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
		logger.Error("scheduler: job failed", "err", runErr)
	} else {
		logger.Info("scheduler: job complete", "processed", processed, "failed", failed)
	}
	_ = s.DB.WithTenantTx(ctx, job.TenantID, func(tx pgx.Tx) error {
		return s.Sched.FinishRunTx(ctx, tx, runID, processed, failed, status, errMsg)
	})
	s.bumpSchedule(ctx, job)
}

func (s *Scheduler) bumpSchedule(ctx context.Context, job domain.ScheduledJob) {
	next, err := s.NextFiring(job.CronExpr, time.Now())
	if err != nil {
		s.Logger.Warn("scheduler: parse cron failed", "expr", job.CronExpr, "err", err)
		// Push next_run_at out an hour so we don't tight-loop on a bad cron.
		next = time.Now().Add(time.Hour)
	}
	_ = s.DB.WithTenantTx(ctx, job.TenantID, func(tx pgx.Tx) error {
		return s.Sched.MarkRanTx(ctx, tx, job.ID, time.Now(), next)
	})
}

// NextFiring computes the next time a 5-field cron expression fires
// strictly after `after`. Exposed for the admin UI to preview the next
// run when editing a job's cron expression.
func (s *Scheduler) NextFiring(expr string, after time.Time) (time.Time, error) {
	sched, err := s.cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron %q: %w", expr, err)
	}
	return sched.Next(after), nil
}

var ErrUnknownJob = errors.New("scheduler: unknown job_key")
