package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type TemplateStore struct {
	pool *pgxpool.Pool
}

func NewTemplateStore(pool *pgxpool.Pool) *TemplateStore {
	return &TemplateStore{pool: pool}
}

const templateCols = `
	id, tenant_id, event_code, channel, subject, body, is_active, created_at, updated_at
`

func scanTemplate(row pgx.Row) (*domain.Template, error) {
	var t domain.Template
	var channel string
	err := row.Scan(
		&t.ID, &t.TenantID, &t.EventCode, &channel,
		&t.Subject, &t.Body, &t.IsActive, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.Channel = domain.Channel(channel)
	return &t, nil
}

// ActiveByEventChannelTx finds the active template for the given
// (event, channel) combination in the current tenant context. Returns
// (nil, nil) when no template is configured — caller decides whether
// to skip the channel or fall back to a default.
func (s *TemplateStore) ActiveByEventChannelTx(
	ctx context.Context, tx pgx.Tx,
	eventCode string, channel domain.Channel,
) (*domain.Template, error) {
	row := tx.QueryRow(ctx, `
		SELECT `+templateCols+` FROM notification_templates
		WHERE event_code = $1 AND channel = $2 AND is_active = true
	`, eventCode, string(channel))
	t, err := scanTemplate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *TemplateStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.Template, error) {
	rows, err := tx.Query(ctx, `SELECT `+templateCols+` FROM notification_templates ORDER BY event_code, channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}
