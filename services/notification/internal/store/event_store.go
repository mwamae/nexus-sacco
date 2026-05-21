package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type EventStore struct {
	pool *pgxpool.Pool
}

func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{pool: pool}
}

const eventCols = `
	code, category, default_priority, description,
	default_channels, allowed_variables, has_pdf_attachment, is_active, created_at
`

func scanEvent(row pgx.Row) (*domain.Event, error) {
	var e domain.Event
	var category, prio string
	var chans []string
	err := row.Scan(
		&e.Code, &category, &prio, &e.Description,
		&chans, &e.AllowedVariables, &e.HasPDFAttachment, &e.IsActive, &e.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	e.Category = domain.Category(category)
	e.DefaultPriority = domain.Priority(prio)
	for _, c := range chans {
		e.DefaultChannels = append(e.DefaultChannels, domain.Channel(c))
	}
	return &e, nil
}

func (s *EventStore) GetTx(ctx context.Context, tx pgx.Tx, code string) (*domain.Event, error) {
	row := tx.QueryRow(ctx, `SELECT `+eventCols+` FROM notification_events WHERE code = $1`, code)
	e, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (s *EventStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.Event, error) {
	rows, err := tx.Query(ctx, `SELECT `+eventCols+` FROM notification_events WHERE is_active = true ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ErrNotFound is the package-wide "no such record" sentinel.
var ErrNotFound = errors.New("notification: record not found")
