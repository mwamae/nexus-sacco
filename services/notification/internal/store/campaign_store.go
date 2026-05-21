// notification_campaigns + notification_campaign_runs +
// notification_campaign_settings persistence.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type CampaignStore struct {
	pool *pgxpool.Pool
}

func NewCampaignStore(pool *pgxpool.Pool) *CampaignStore {
	return &CampaignStore{pool: pool}
}

const campaignCols = `
	id, tenant_id, name, description, event_code, channels,
	audience, payload, status, scheduled_for,
	estimated_recipients, total_recipients, dispatched_count, failed_count,
	created_at, created_by, approved_at, approved_by,
	sent_at, cancelled_at, cancel_reason, failure_reason, updated_at
`

func scanCampaign(row pgx.Row) (*domain.Campaign, error) {
	var c domain.Campaign
	var status string
	var channels []string
	var audience, payload []byte
	err := row.Scan(
		&c.ID, &c.TenantID, &c.Name, &c.Description, &c.EventCode, &channels,
		&audience, &payload, &status, &c.ScheduledFor,
		&c.EstimatedRecipients, &c.TotalRecipients, &c.DispatchedCount, &c.FailedCount,
		&c.CreatedAt, &c.CreatedBy, &c.ApprovedAt, &c.ApprovedBy,
		&c.SentAt, &c.CancelledAt, &c.CancelReason, &c.FailureReason, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Status = domain.CampaignStatus(status)
	c.Channels = make([]domain.Channel, 0, len(channels))
	for _, ch := range channels {
		c.Channels = append(c.Channels, domain.Channel(ch))
	}
	c.Audience = audience
	c.Payload = payload
	return &c, nil
}

type CreateCampaignInput struct {
	Name                string
	Description         *string
	EventCode           string
	Channels            []domain.Channel
	Audience            map[string]any
	Payload             map[string]any
	ScheduledFor        *time.Time
	EstimatedRecipients int
	Status              domain.CampaignStatus
	CreatedBy           *uuid.UUID
}

func (s *CampaignStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateCampaignInput) (*domain.Campaign, error) {
	chans := make([]string, 0, len(in.Channels))
	for _, c := range in.Channels {
		chans = append(chans, string(c))
	}
	if len(chans) == 0 {
		chans = []string{string(domain.ChannelInApp)}
	}
	if in.Status == "" {
		in.Status = domain.CampaignDraft
	}
	audience, err := json.Marshal(in.Audience)
	if err != nil {
		return nil, err
	}
	if len(audience) == 0 || string(audience) == "null" {
		audience = []byte(`{"type":"all_members"}`)
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte("{}")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_campaigns (
			tenant_id, name, description, event_code, channels,
			audience, payload, status, scheduled_for,
			estimated_recipients, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5::jsonb, $6::jsonb, $7, $8,
			$9, $10
		)
		RETURNING `+campaignCols,
		in.Name, in.Description, in.EventCode, chans,
		audience, payload, string(in.Status), in.ScheduledFor,
		in.EstimatedRecipients, in.CreatedBy,
	)
	return scanCampaign(row)
}

func (s *CampaignStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Campaign, error) {
	row := tx.QueryRow(ctx, `SELECT `+campaignCols+` FROM notification_campaigns WHERE id = $1`, id)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

type CampaignListFilter struct {
	Status string
	Limit  int
	Offset int
}

func (s *CampaignStore) ListTx(ctx context.Context, tx pgx.Tx, f CampaignListFilter) ([]domain.Campaign, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + strconv.Itoa(idx)
		args = append(args, f.Status)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notification_campaigns `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	rows, err := tx.Query(ctx, `
		SELECT `+campaignCols+` FROM notification_campaigns `+where+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, rows.Err()
}

func (s *CampaignStore) UpdateStatusTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID,
	status domain.CampaignStatus, fields map[string]any,
) error {
	// Build a dynamic UPDATE — only fields supplied get set.
	sets := []string{"status = $2", "updated_at = now()"}
	args := []any{id, string(status)}
	idx := 3
	for k, v := range fields {
		sets = append(sets, k+" = $"+strconv.Itoa(idx))
		args = append(args, v)
		idx++
	}
	q := "UPDATE notification_campaigns SET " + join(sets, ", ") + " WHERE id = $1"
	_, err := tx.Exec(ctx, q, args...)
	return err
}

func (s *CampaignStore) ClaimNextScheduledTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.Campaign, error) {
	// Atomically pick the next due scheduled campaign for this tenant
	// and flip it to 'sending'. Uses FOR UPDATE SKIP LOCKED so multiple
	// workers don't collide.
	row := tx.QueryRow(ctx, `
		WITH due AS (
			SELECT id FROM notification_campaigns
			WHERE tenant_id = $1
			  AND status = 'scheduled'
			  AND scheduled_for <= now()
			ORDER BY scheduled_for
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE notification_campaigns c
		SET status = 'sending', updated_at = now()
		FROM due
		WHERE c.id = due.id
		RETURNING `+prefixCols(campaignCols, "c."), tenantID,
	)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// IncrementProgressTx bumps counters as the worker processes recipients.
func (s *CampaignStore) IncrementProgressTx(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, dispatchedDelta, failedDelta int,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_campaigns
		SET dispatched_count = dispatched_count + $2,
		    failed_count     = failed_count + $3,
		    updated_at       = now()
		WHERE id = $1
	`, id, dispatchedDelta, failedDelta)
	return err
}

// ─────────── Campaign runs ───────────

func (s *CampaignStore) CreateRunTx(
	ctx context.Context, tx pgx.Tx, campaignID uuid.UUID, total int,
) (*domain.JobRun, error) {
	// We re-use the JobRun struct shape for simplicity; the data lives
	// in notification_campaign_runs.
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_campaign_runs (tenant_id, campaign_id, recipients_total)
		VALUES (current_tenant_id(), $1, $2)
		RETURNING id
	`, campaignID, total)
	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	return &domain.JobRun{ID: id}, nil
}

func (s *CampaignStore) FinishRunTx(
	ctx context.Context, tx pgx.Tx, runID uuid.UUID,
	dispatched, failed int, notes string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_campaign_runs
		SET finished_at = now(),
		    dispatched_count = $2,
		    failed_count = $3,
		    notes = $4
		WHERE id = $1
	`, runID, dispatched, failed, nullIfEmpty(notes))
	return err
}

// ─────────── Settings ───────────

func (s *CampaignStore) GetSettingsTx(ctx context.Context, tx pgx.Tx) (*domain.CampaignSettings, error) {
	row := tx.QueryRow(ctx, `SELECT tenant_id, approval_recipient_threshold, updated_at FROM notification_campaign_settings LIMIT 1`)
	var c domain.CampaignSettings
	err := row.Scan(&c.TenantID, &c.ApprovalRecipientThreshold, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		row = tx.QueryRow(ctx, `
			INSERT INTO notification_campaign_settings (tenant_id) VALUES (current_tenant_id())
			RETURNING tenant_id, approval_recipient_threshold, updated_at`)
		err = row.Scan(&c.TenantID, &c.ApprovalRecipientThreshold, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *CampaignStore) UpdateSettingsTx(ctx context.Context, tx pgx.Tx, threshold int) (*domain.CampaignSettings, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_campaign_settings (tenant_id, approval_recipient_threshold)
		VALUES (current_tenant_id(), $1)
		ON CONFLICT (tenant_id) DO UPDATE SET
			approval_recipient_threshold = EXCLUDED.approval_recipient_threshold,
			updated_at                   = now()
		RETURNING tenant_id, approval_recipient_threshold, updated_at
	`, threshold)
	var c domain.CampaignSettings
	if err := row.Scan(&c.TenantID, &c.ApprovalRecipientThreshold, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// join — local; avoids importing strings just for one Join.
func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}

// prefixCols rewrites a comma-separated column list (with whitespace
// and newlines tolerated) so every bare column name is qualified with
// the given prefix. Used to disambiguate RETURNING after a self-join.
func prefixCols(cols, prefix string) string {
	out := ""
	for _, raw := range splitOnComma(cols) {
		col := trimSpace(raw)
		if col == "" {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += prefix + col
	}
	return out
}

func splitOnComma(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
