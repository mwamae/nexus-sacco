// notification_scheduled_jobs + notification_job_runs persistence.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type SchedulerStore struct {
	pool *pgxpool.Pool
}

func NewSchedulerStore(pool *pgxpool.Pool) *SchedulerStore {
	return &SchedulerStore{pool: pool}
}

const jobCols = `
	id, tenant_id, job_key, description, cron_expr, is_active, config,
	last_run_at, next_run_at, created_at, updated_at
`

func scanJob(row pgx.Row) (*domain.ScheduledJob, error) {
	var j domain.ScheduledJob
	var config []byte
	err := row.Scan(
		&j.ID, &j.TenantID, &j.JobKey, &j.Description, &j.CronExpr, &j.IsActive, &config,
		&j.LastRunAt, &j.NextRunAt, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	j.Config = config
	return &j, nil
}

// ListActiveJobsAcrossTenantsTx — used by the scheduler tick to find
// jobs that are due. Runs with no tenant context (RLS-aware); the
// caller iterates by tenant after.
func (s *SchedulerStore) ListDueAcrossTenantsTx(ctx context.Context, tx pgx.Tx, now time.Time) ([]domain.ScheduledJob, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+jobCols+` FROM notification_scheduled_jobs
		WHERE is_active = true AND next_run_at IS NOT NULL AND next_run_at <= $1
		ORDER BY next_run_at
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ScheduledJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *SchedulerStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.ScheduledJob, error) {
	rows, err := tx.Query(ctx, `SELECT `+jobCols+` FROM notification_scheduled_jobs ORDER BY job_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ScheduledJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *SchedulerStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.ScheduledJob, error) {
	row := tx.QueryRow(ctx, `SELECT `+jobCols+` FROM notification_scheduled_jobs WHERE id = $1`, id)
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

func (s *SchedulerStore) UpdateScheduleTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID,
	cronExpr string, isActive bool, nextRunAt time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_scheduled_jobs
		SET cron_expr = $2, is_active = $3, next_run_at = $4, updated_at = now()
		WHERE id = $1
	`, id, cronExpr, isActive, nextRunAt)
	return err
}

// MarkRanTx is called after a job tick: records last_run_at and the
// next scheduled run time.
func (s *SchedulerStore) MarkRanTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, ranAt, nextRunAt time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_scheduled_jobs
		SET last_run_at = $2, next_run_at = $3, updated_at = now()
		WHERE id = $1
	`, id, ranAt, nextRunAt)
	return err
}

// ─────────── Job runs ───────────

func (s *SchedulerStore) CreateRunTx(
	ctx context.Context, tx pgx.Tx,
	job *domain.ScheduledJob, scheduledFor time.Time,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO notification_job_runs (
			tenant_id, scheduled_job_id, job_key, scheduled_for
		) VALUES (current_tenant_id(), $1, $2, $3)
		RETURNING id
	`, job.ID, job.JobKey, scheduledFor).Scan(&id)
	return id, err
}

func (s *SchedulerStore) FinishRunTx(
	ctx context.Context, tx pgx.Tx, runID uuid.UUID,
	processed, failed int, status, errorMsg string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_job_runs
		SET finished_at = now(),
		    records_processed = $2,
		    records_failed = $3,
		    status = $4,
		    error_message = $5
		WHERE id = $1
	`, runID, processed, failed, status, nullIfEmpty(errorMsg))
	return err
}

func (s *SchedulerStore) ListRunsTx(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, limit int) ([]domain.JobRun, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, scheduled_job_id, job_key, scheduled_for,
		       started_at, finished_at, records_processed, records_failed, status, error_message
		FROM notification_job_runs
		WHERE scheduled_job_id = $1
		ORDER BY started_at DESC
		LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.JobRun{}
	for rows.Next() {
		var r domain.JobRun
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.ScheduledJobID, &r.JobKey, &r.ScheduledFor,
			&r.StartedAt, &r.FinishedAt, &r.RecordsProcessed, &r.RecordsFailed, &r.Status, &r.ErrorMessage,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
