package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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

func (s *TemplateStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Template, error) {
	row := tx.QueryRow(ctx, `SELECT `+templateCols+` FROM notification_templates WHERE id = $1`, id)
	t, err := scanTemplate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// CreateTx inserts a new template. The (tenant_id, event_code, channel)
// combination is unique among active templates — to roll out a new
// version, deactivate the existing one first or use UpdateTx.
type UpsertTemplateInput struct {
	EventCode string
	Channel   domain.Channel
	Subject   *string
	Body      string
	IsActive  bool
}

func (s *TemplateStore) CreateTx(ctx context.Context, tx pgx.Tx, in UpsertTemplateInput) (*domain.Template, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_templates (tenant_id, event_code, channel, subject, body, is_active)
		VALUES (current_tenant_id(), $1, $2, $3, $4, $5)
		RETURNING `+templateCols,
		in.EventCode, string(in.Channel), in.Subject, in.Body, in.IsActive,
	)
	return scanTemplate(row)
}

func (s *TemplateStore) UpdateTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, in UpsertTemplateInput) (*domain.Template, error) {
	row := tx.QueryRow(ctx, `
		UPDATE notification_templates
		SET event_code = $2, channel = $3, subject = $4, body = $5, is_active = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+templateCols,
		id, in.EventCode, string(in.Channel), in.Subject, in.Body, in.IsActive,
	)
	t, err := scanTemplate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *TemplateStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM notification_templates WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
