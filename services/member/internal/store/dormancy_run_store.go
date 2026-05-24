// Persistence for dormancy_runs — the per-run tracking row added by
// PR #6 to gate the bulk dormancy detector behind a workflow
// approval. See migration 0015 for the table shape.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DormancyRunStore struct {
	pool *pgxpool.Pool
}

func NewDormancyRunStore(pool *pgxpool.Pool) *DormancyRunStore {
	return &DormancyRunStore{pool: pool}
}

type DormancyRun struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	WorkflowInstanceID uuid.UUID
	ThresholdDays      int
	Snapshot           []DormancyCandidate
	CandidateCount     int
	Status             string // pending | approved | rejected | cancelled | applied
	SubmittedBy        uuid.UUID
	SubmittedAt        time.Time
	ResolvedAt         *time.Time
	AppliedAt          *time.Time
	ResolvedNote       *string
	ApplyOutcomes      []DormancyApplyOutcome
}

type DormancyApplyOutcome struct {
	CounterpartyID uuid.UUID `json:"counterparty_id"`
	MemberNo       string    `json:"member_no"`
	Outcome        string    `json:"outcome"` // "applied" | "skipped:<reason>"
}

func (s *DormancyRunStore) CreateTx(
	ctx context.Context, tx pgx.Tx,
	tenantID, workflowID, submittedBy uuid.UUID,
	thresholdDays int, candidates []*DormancyCandidate,
) (*DormancyRun, error) {
	// Snapshot is stored as jsonb — flatten pointers so the
	// serialization is the same shape DormancyCandidatesTx returns.
	flat := make([]DormancyCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c != nil {
			flat = append(flat, *c)
		}
	}
	snap, _ := json.Marshal(flat)
	var run DormancyRun
	err := tx.QueryRow(ctx, `
		INSERT INTO dormancy_runs (
		  tenant_id, workflow_instance_id, threshold_days, snapshot, candidate_count, submitted_by
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, tenant_id, workflow_instance_id, threshold_days, candidate_count, status,
		          submitted_by, submitted_at, resolved_at, applied_at, resolved_note
	`, tenantID, workflowID, thresholdDays, snap, len(flat), submittedBy).
		Scan(&run.ID, &run.TenantID, &run.WorkflowInstanceID, &run.ThresholdDays,
			&run.CandidateCount, &run.Status,
			&run.SubmittedBy, &run.SubmittedAt, &run.ResolvedAt, &run.AppliedAt, &run.ResolvedNote)
	if err != nil {
		return nil, err
	}
	run.Snapshot = flat
	return &run, nil
}

// GetByIDTx returns the run with its snapshot decoded. ErrNotFound
// when no row matches.
func (s *DormancyRunStore) GetByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*DormancyRun, error) {
	return s.scanOne(ctx, tx, `WHERE id = $1`, id)
}

// ByWorkflowInstanceTx is the reverse lookup the resolve callback
// uses: workflow says "instance X is now approved", we find the
// dormancy_runs row that owns it.
func (s *DormancyRunStore) ByWorkflowInstanceTx(ctx context.Context, tx pgx.Tx, wfID uuid.UUID) (*DormancyRun, error) {
	return s.scanOne(ctx, tx, `WHERE workflow_instance_id = $1`, wfID)
}

func (s *DormancyRunStore) scanOne(ctx context.Context, tx pgx.Tx, where string, args ...any) (*DormancyRun, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, workflow_instance_id, threshold_days, snapshot, candidate_count, status,
		       submitted_by, submitted_at, resolved_at, applied_at, resolved_note, apply_outcomes
		  FROM dormancy_runs `+where+` LIMIT 1`, args...)
	var run DormancyRun
	var snapRaw, outRaw []byte
	err := row.Scan(&run.ID, &run.TenantID, &run.WorkflowInstanceID, &run.ThresholdDays,
		&snapRaw, &run.CandidateCount, &run.Status,
		&run.SubmittedBy, &run.SubmittedAt, &run.ResolvedAt, &run.AppliedAt, &run.ResolvedNote, &outRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(snapRaw, &run.Snapshot)
	_ = json.Unmarshal(outRaw, &run.ApplyOutcomes)
	return &run, nil
}

// MarkAppliedTx flips the run to status='applied', stamps applied_at,
// and writes the per-counterparty outcomes the resolve handler
// collected while iterating.
func (s *DormancyRunStore) MarkAppliedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, outcomes []DormancyApplyOutcome) error {
	outRaw, _ := json.Marshal(outcomes)
	_, err := tx.Exec(ctx, `
		UPDATE dormancy_runs
		SET status = 'applied', applied_at = now(), resolved_at = now(), apply_outcomes = $2
		WHERE id = $1`, id, outRaw)
	return err
}

func (s *DormancyRunStore) MarkTerminalTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status, note string) error {
	var notep *string
	if note != "" {
		notep = &note
	}
	_, err := tx.Exec(ctx, `
		UPDATE dormancy_runs
		SET status = $2, resolved_at = now(), resolved_note = $3
		WHERE id = $1`, id, status, notep)
	return err
}
