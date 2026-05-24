// Persistence for fiscal_year_close_proposals — the workflow-gate row
// created at submit-for-close time. Lifecycle: pending → applied |
// rejected | cancelled. See migration 0009 for the table shape.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FiscalYearProposalStore struct {
	pool *pgxpool.Pool
}

func NewFiscalYearProposalStore(pool *pgxpool.Pool) *FiscalYearProposalStore {
	return &FiscalYearProposalStore{pool: pool}
}

type FiscalYearCloseProposal struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	Year               int
	WorkflowInstanceID uuid.UUID
	Notes              *string
	SubmittedBy        uuid.UUID
	SubmittedAt        time.Time
	Status             string // pending | approved | rejected | cancelled | applied
	AppliedCloseID     *uuid.UUID
	ResolvedAt         *time.Time
	ResolvedNote       *string
}

const fyProposalCols = `
	id, tenant_id, year, workflow_instance_id, notes,
	submitted_by, submitted_at, status, applied_close_id, resolved_at, resolved_note
`

func scanFYProposal(row pgx.Row) (*FiscalYearCloseProposal, error) {
	var p FiscalYearCloseProposal
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Year, &p.WorkflowInstanceID, &p.Notes,
		&p.SubmittedBy, &p.SubmittedAt, &p.Status, &p.AppliedCloseID, &p.ResolvedAt, &p.ResolvedNote,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *FiscalYearProposalStore) CreateTx(
	ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, year int, wfID uuid.UUID, notes string, submittedBy uuid.UUID,
) (*FiscalYearCloseProposal, error) {
	var notesP *string
	if notes != "" {
		notesP = &notes
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO fiscal_year_close_proposals (tenant_id, year, workflow_instance_id, notes, submitted_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+fyProposalCols,
		tenantID, year, wfID, notesP, submittedBy)
	return scanFYProposal(row)
}

// GetByIDTx returns the proposal or ErrNotFound.
func (s *FiscalYearProposalStore) GetByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*FiscalYearCloseProposal, error) {
	row := tx.QueryRow(ctx, `SELECT `+fyProposalCols+` FROM fiscal_year_close_proposals WHERE id = $1`, id)
	p, err := scanFYProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PendingForYearTx returns the in-flight proposal for (tenant, year)
// if one exists. Used by SubmitForClose to be idempotent — re-clicking
// the CTA returns the existing proposal instead of trying to create a
// second one (the partial unique index would reject anyway).
func (s *FiscalYearProposalStore) PendingForYearTx(ctx context.Context, tx pgx.Tx, year int) (*FiscalYearCloseProposal, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+fyProposalCols+` FROM fiscal_year_close_proposals WHERE year = $1 AND status = 'pending' LIMIT 1`, year)
	p, err := scanFYProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SetAppliedTx flips the proposal to 'applied' + back-links the
// fiscal_year_closes row that the resolve handler just created.
func (s *FiscalYearProposalStore) SetAppliedTx(ctx context.Context, tx pgx.Tx, id, closeID uuid.UUID, note string) error {
	var notep *string
	if note != "" {
		notep = &note
	}
	_, err := tx.Exec(ctx, `
		UPDATE fiscal_year_close_proposals
		SET status = 'applied', applied_close_id = $2, resolved_at = now(), resolved_note = $3
		WHERE id = $1`, id, closeID, notep)
	return err
}

// SetTerminalTx flips to a non-applied terminal status (rejected,
// cancelled). For approved-but-failed cases the operator gets a
// dedicated note.
func (s *FiscalYearProposalStore) SetTerminalTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status, note string) error {
	var notep *string
	if note != "" {
		notep = &note
	}
	_, err := tx.Exec(ctx, `
		UPDATE fiscal_year_close_proposals
		SET status = $2, resolved_at = now(), resolved_note = $3
		WHERE id = $1`, id, status, notep)
	return err
}
