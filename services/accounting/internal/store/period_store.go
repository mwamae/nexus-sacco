// accounting_periods persistence + the open-period guard used by the
// journal entry poster.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/accounting/internal/domain"
)

type PeriodStore struct {
	pool *pgxpool.Pool
}

func NewPeriodStore(pool *pgxpool.Pool) *PeriodStore {
	return &PeriodStore{pool: pool}
}

const periodCols = `
	id, tenant_id, year, month, status, opened_at, opened_by,
	closed_at, closed_by, notes, created_at, updated_at
`

func scanPeriod(row pgx.Row) (*domain.Period, error) {
	var p domain.Period
	var status string
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Year, &p.Month, &status,
		&p.OpenedAt, &p.OpenedBy, &p.ClosedAt, &p.ClosedBy, &p.Notes,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.Status = domain.PeriodStatus(status)
	return &p, nil
}

func (s *PeriodStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.Period, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+periodCols+` FROM accounting_periods ORDER BY year DESC, month DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Period{}
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *PeriodStore) ForDateTx(ctx context.Context, tx pgx.Tx, d time.Time) (*domain.Period, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+periodCols+` FROM accounting_periods WHERE year = $1 AND month = $2`,
		d.Year(), int(d.Month()),
	)
	p, err := scanPeriod(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// OpenOrCreateTx ensures a period row exists for the given (year, month)
// and is in the 'open' state. Returns the period. Used by the auto-poster
// so a transaction on the 1st of a new month doesn't bounce because no
// one's clicked "open period" yet.
func (s *PeriodStore) OpenOrCreateTx(ctx context.Context, tx pgx.Tx, year, month int) (*domain.Period, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO accounting_periods (tenant_id, year, month, status, opened_at)
		VALUES (current_tenant_id(), $1, $2, 'open', now())
		ON CONFLICT (tenant_id, year, month) DO UPDATE
		   SET updated_at = accounting_periods.updated_at
		RETURNING `+periodCols, year, month,
	)
	return scanPeriod(row)
}

// CloseTx flips a period to closed. The journal entry poster rejects
// any entry whose period_year/period_month is on a closed period.
func (s *PeriodStore) CloseTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, closedBy uuid.UUID, notes string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE accounting_periods
		SET status = 'closed', closed_at = now(), closed_by = $2, notes = NULLIF($3,''),
		    updated_at = now()
		WHERE id = $1 AND status = 'open'
	`, id, closedBy, notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PeriodStore) ReopenTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, openedBy uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE accounting_periods
		SET status = 'open', closed_at = NULL, closed_by = NULL,
		    opened_at = now(), opened_by = $2,
		    notes = CASE WHEN $3 = '' THEN notes ELSE COALESCE(notes,'') || E'\nRe-opened: ' || $3 END,
		    updated_at = now()
		WHERE id = $1 AND status = 'closed'
	`, id, openedBy, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureOpenForDateTx is what the posting engine calls before writing
// any entry. Returns ErrPeriodClosed if the period exists but is closed.
var ErrPeriodClosed = errors.New("accounting period is closed")

func (s *PeriodStore) EnsureOpenForDateTx(ctx context.Context, tx pgx.Tx, d time.Time) (*domain.Period, error) {
	p, err := s.OpenOrCreateTx(ctx, tx, d.Year(), int(d.Month()))
	if err != nil {
		return nil, err
	}
	if p.Status != domain.PeriodOpen {
		return nil, ErrPeriodClosed
	}
	return p, nil
}
