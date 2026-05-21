// Central notification persistence — creates the notifications row +
// per-channel notification_deliveries rows, and exposes the feed
// queries the admin / member inbox needs.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type NotificationStore struct {
	pool *pgxpool.Pool
}

func NewNotificationStore(pool *pgxpool.Pool) *NotificationStore {
	return &NotificationStore{pool: pool}
}

const notificationCols = `
	id, tenant_id, event_code, priority,
	recipient_member_id, recipient_user_id, recipient_name, recipient_phone, recipient_email,
	source_module, source_record_id, deep_link,
	payload, initiated_by, created_at
`

func scanNotification(row pgx.Row) (*domain.Notification, error) {
	var n domain.Notification
	var prio string
	var payload []byte
	err := row.Scan(
		&n.ID, &n.TenantID, &n.EventCode, &prio,
		&n.RecipientMemberID, &n.RecipientUserID, &n.RecipientName, &n.RecipientPhone, &n.RecipientEmail,
		&n.SourceModule, &n.SourceRecordID, &n.DeepLink,
		&payload, &n.InitiatedBy, &n.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	n.Priority = domain.Priority(prio)
	n.Payload = payload
	return &n, nil
}

// CreateInput is the typed payload for inserting a notification.
type CreateInput struct {
	EventCode         string
	Priority          domain.Priority
	RecipientMemberID *uuid.UUID
	RecipientUserID   *uuid.UUID
	RecipientName     string
	RecipientPhone    *string
	RecipientEmail    *string
	SourceModule      *string
	SourceRecordID    *uuid.UUID
	DeepLink          *string
	Payload           map[string]any
	InitiatedBy       *uuid.UUID
}

func (s *NotificationStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateInput) (*domain.Notification, error) {
	if in.Priority == "" {
		in.Priority = domain.PriorityInfo
	}
	payloadBytes, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	if len(payloadBytes) == 0 || string(payloadBytes) == "null" {
		payloadBytes = []byte("{}")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO notifications (
			tenant_id, event_code, priority,
			recipient_member_id, recipient_user_id, recipient_name, recipient_phone, recipient_email,
			source_module, source_record_id, deep_link,
			payload, initiated_by
		) VALUES (
			current_tenant_id(), $1, $2,
			$3, $4, $5, $6, $7,
			$8, $9, $10,
			$11::jsonb, $12
		)
		RETURNING `+notificationCols,
		in.EventCode, string(in.Priority),
		in.RecipientMemberID, in.RecipientUserID, in.RecipientName, in.RecipientPhone, in.RecipientEmail,
		in.SourceModule, in.SourceRecordID, in.DeepLink,
		payloadBytes, in.InitiatedBy,
	)
	return scanNotification(row)
}

func (s *NotificationStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Notification, error) {
	row := tx.QueryRow(ctx, `SELECT `+notificationCols+` FROM notifications WHERE id = $1`, id)
	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// CreateDeliveryInput is the per-channel rendered content + initial status.
type CreateDeliveryInput struct {
	NotificationID uuid.UUID
	Channel        domain.Channel
	TemplateID     *uuid.UUID
	Subject        *string
	Body           string
	Status         domain.Status // initial status
}

func (s *NotificationStore) CreateDeliveryTx(ctx context.Context, tx pgx.Tx, in CreateDeliveryInput) (*domain.Delivery, error) {
	// For in_app, the row IS the delivery — mark delivered immediately.
	// For sms/email, status starts as 'queued' and a worker will process.
	now := "now()"
	status := in.Status
	if status == "" {
		switch in.Channel {
		case domain.ChannelInApp:
			status = domain.StatusDelivered
		default:
			status = domain.StatusQueued
		}
	}
	deliveredAtExpr := "NULL"
	queuedAtExpr := "NULL"
	switch status {
	case domain.StatusDelivered:
		deliveredAtExpr = now
	case domain.StatusQueued:
		queuedAtExpr = now
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_deliveries (
			tenant_id, notification_id, channel, template_id,
			subject, body, status,
			queued_at, delivered_at
		) VALUES (
			current_tenant_id(), $1, $2, $3,
			$4, $5, $6,
			`+queuedAtExpr+`, `+deliveredAtExpr+`
		)
		RETURNING `+deliveryCols,
		in.NotificationID, string(in.Channel), in.TemplateID,
		in.Subject, in.Body, string(status),
	)
	return scanDelivery(row)
}

const deliveryCols = `
	id, tenant_id, notification_id, channel, template_id,
	subject, body, status, attempt_count,
	queued_at, sent_at, delivered_at, read_at, failed_at,
	failure_reason, provider_message_id, created_at, updated_at
`

func scanDelivery(row pgx.Row) (*domain.Delivery, error) {
	var d domain.Delivery
	var channel, status string
	err := row.Scan(
		&d.ID, &d.TenantID, &d.NotificationID, &channel, &d.TemplateID,
		&d.Subject, &d.Body, &status, &d.AttemptCount,
		&d.QueuedAt, &d.SentAt, &d.DeliveredAt, &d.ReadAt, &d.FailedAt,
		&d.FailureReason, &d.ProviderMessageID, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	d.Channel = domain.Channel(channel)
	d.Status = domain.Status(status)
	return &d, nil
}

func (s *NotificationStore) DeliveriesByNotificationTx(ctx context.Context, tx pgx.Tx, notificationID uuid.UUID) ([]domain.Delivery, error) {
	rows, err := tx.Query(ctx, `SELECT `+deliveryCols+` FROM notification_deliveries WHERE notification_id = $1 ORDER BY channel`, notificationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// MarkReadTx marks the in-app delivery row for a notification as read.
// Idempotent — calling it on a row already in 'read' is a no-op.
func (s *NotificationStore) MarkReadTx(ctx context.Context, tx pgx.Tx, notificationID uuid.UUID, recipientUserID *uuid.UUID) error {
	// Belt-and-suspenders: only the actual recipient can mark their own
	// notification read. RLS already isolates tenants; this stops a
	// staff user from poking sideways at another staff user's row.
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries d
		SET status = 'read', read_at = now(), updated_at = now()
		FROM notifications n
		WHERE d.notification_id = $1 AND d.channel = 'in_app' AND d.status <> 'read'
		  AND n.id = d.notification_id
		  AND ($2::uuid IS NULL OR n.recipient_user_id = $2::uuid)
	`, notificationID, recipientUserID)
	return err
}

func (s *NotificationStore) MarkAllReadForUserTx(ctx context.Context, tx pgx.Tx, recipientUserID uuid.UUID) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_deliveries d
		SET status = 'read', read_at = now(), updated_at = now()
		FROM notifications n
		WHERE n.id = d.notification_id AND d.channel = 'in_app' AND d.status <> 'read'
		  AND n.recipient_user_id = $1
	`, recipientUserID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// FeedFilter narrows the inbox listing.
type FeedFilter struct {
	UserID   *uuid.UUID
	MemberID *uuid.UUID
	UnreadOnly bool
	Limit    int
	Offset   int
}

// FeedForRecipientTx returns the in-app feed for a user or member.
// The query returns one row per notification with its in-app delivery
// embedded so the frontend doesn't have to join.
func (s *NotificationStore) FeedForRecipientTx(ctx context.Context, tx pgx.Tx, f FeedFilter) ([]domain.FeedItem, int, error) {
	if f.UserID == nil && f.MemberID == nil {
		return []domain.FeedItem{}, 0, nil
	}
	where := []string{"d.channel = 'in_app'"}
	args := []any{}
	idx := 1
	if f.UserID != nil {
		where = append(where, "n.recipient_user_id = $"+strconv.Itoa(idx))
		args = append(args, *f.UserID)
		idx++
	} else if f.MemberID != nil {
		where = append(where, "n.recipient_member_id = $"+strconv.Itoa(idx))
		args = append(args, *f.MemberID)
		idx++
	}
	if f.UnreadOnly {
		where = append(where, "d.status <> 'read'")
	}
	whereClause := "WHERE " + strings.Join(where, " AND ")

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM notifications n
		 JOIN notification_deliveries d ON d.notification_id = n.id `+whereClause,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)

	q := `SELECT
		` + prefixedCols(notificationCols, "n") + `,
		d.body, d.status, d.read_at
	FROM notifications n
	JOIN notification_deliveries d ON d.notification_id = n.id
	` + whereClause + `
	ORDER BY n.created_at DESC
	LIMIT $` + strconv.Itoa(idx) + ` OFFSET $` + strconv.Itoa(idx+1)

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.FeedItem{}
	for rows.Next() {
		var f domain.FeedItem
		var prio, status string
		var payload []byte
		if err := rows.Scan(
			&f.ID, &f.TenantID, &f.EventCode, &prio,
			&f.RecipientMemberID, &f.RecipientUserID, &f.RecipientName, &f.RecipientPhone, &f.RecipientEmail,
			&f.SourceModule, &f.SourceRecordID, &f.DeepLink,
			&payload, &f.InitiatedBy, &f.CreatedAt,
			&f.Body, &status, &f.ReadAt,
		); err != nil {
			return nil, 0, err
		}
		f.Priority = domain.Priority(prio)
		f.Payload = payload
		f.InAppStatus = domain.Status(status)
		out = append(out, f)
	}
	return out, total, rows.Err()
}

func (s *NotificationStore) UnreadCountForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications n
		JOIN notification_deliveries d ON d.notification_id = n.id
		WHERE n.recipient_user_id = $1 AND d.channel = 'in_app' AND d.status <> 'read'
	`, userID).Scan(&n)
	return n, err
}

// ─────────── Worker queries (Stage 2) ───────────

// DueEmailDelivery is the joined row the worker needs to send: the
// rendered email body plus the recipient address from the parent
// notification row.
type DueEmailDelivery struct {
	DeliveryID     uuid.UUID
	NotificationID uuid.UUID
	TenantID       uuid.UUID
	Subject        string
	Body           string
	RecipientName  string
	RecipientEmail string
	AttemptCount   int
	MaxAttempts    int
}

// ClaimDueEmailsForTenantTx atomically marks up to `limit` email
// deliveries as 'sent' (we'll roll them back to 'queued' or 'failed'
// after the SMTP attempt) and returns them. Using a CTE keeps the
// claim atomic so two worker ticks can't pick the same row.
//
// We claim only rows where: status='queued', channel='email', and
// either there's no scheduled retry yet OR the retry time has passed.
// Rows missing a recipient_email are skipped (returned to failed-final).
func (s *NotificationStore) ClaimDueEmailsForTenantTx(
	ctx context.Context, tx pgx.Tx, limit int,
) ([]DueEmailDelivery, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT d.id
			FROM notification_deliveries d
			WHERE d.channel = 'email'
			  AND d.status = 'queued'
			  AND (d.next_retry_at IS NULL OR d.next_retry_at <= now())
			ORDER BY d.created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		),
		claimed AS (
			UPDATE notification_deliveries d
			SET status = 'sent', attempt_count = attempt_count + 1, updated_at = now()
			FROM due
			WHERE d.id = due.id
			RETURNING d.id, d.notification_id, d.tenant_id, d.subject, d.body, d.attempt_count, d.max_attempts
		)
		SELECT c.id, c.notification_id, c.tenant_id,
		       COALESCE(c.subject, ''), c.body,
		       n.recipient_name, COALESCE(n.recipient_email, ''),
		       c.attempt_count, c.max_attempts
		FROM claimed c
		JOIN notifications n ON n.id = c.notification_id
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DueEmailDelivery{}
	for rows.Next() {
		var d DueEmailDelivery
		if err := rows.Scan(
			&d.DeliveryID, &d.NotificationID, &d.TenantID,
			&d.Subject, &d.Body, &d.RecipientName, &d.RecipientEmail,
			&d.AttemptCount, &d.MaxAttempts,
		); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkSentTx records a successful SMTP send. Provider message id is
// optional (some servers don't return one).
func (s *NotificationStore) MarkSentTx(
	ctx context.Context, tx pgx.Tx,
	deliveryID uuid.UUID, providerMessageID *string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'sent', sent_at = now(), updated_at = now(),
		    provider_message_id = COALESCE($2, provider_message_id),
		    failure_reason = NULL
		WHERE id = $1
	`, deliveryID, providerMessageID)
	return err
}

// MarkFailedRetryableTx pushes a delivery back to 'queued' and schedules
// the next attempt. Caller computes nextRetryAt from the attempt count
// using the platform retry table (1m, 5m, 15m).
func (s *NotificationStore) MarkFailedRetryableTx(
	ctx context.Context, tx pgx.Tx,
	deliveryID uuid.UUID, reason string, nextRetryAt time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'queued',
		    failure_reason = $2,
		    next_retry_at = $3,
		    updated_at = now()
		WHERE id = $1
	`, deliveryID, reason, nextRetryAt)
	return err
}

// MarkFailedFinalTx records the terminal failure after retries are
// exhausted. Triggers a system alert via the caller's logger.
func (s *NotificationStore) MarkFailedFinalTx(
	ctx context.Context, tx pgx.Tx,
	deliveryID uuid.UUID, reason string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'failed',
		    failed_at = now(),
		    failure_reason = $2,
		    next_retry_at = NULL,
		    updated_at = now()
		WHERE id = $1
	`, deliveryID, reason)
	return err
}

// DueSMSDelivery — SMS-side analogue of DueEmailDelivery.
type DueSMSDelivery struct {
	DeliveryID     uuid.UUID
	NotificationID uuid.UUID
	TenantID       uuid.UUID
	Body           string
	RecipientName  string
	RecipientPhone string
	AttemptCount   int
	MaxAttempts    int
}

// ClaimDueSMSForTenantTx mirrors ClaimDueEmailsForTenantTx for the
// 'sms' channel. Same atomic claim semantics (FOR UPDATE SKIP LOCKED).
func (s *NotificationStore) ClaimDueSMSForTenantTx(
	ctx context.Context, tx pgx.Tx, limit int,
) ([]DueSMSDelivery, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT d.id
			FROM notification_deliveries d
			WHERE d.channel = 'sms'
			  AND d.status = 'queued'
			  AND (d.next_retry_at IS NULL OR d.next_retry_at <= now())
			ORDER BY d.created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		),
		claimed AS (
			UPDATE notification_deliveries d
			SET status = 'sent', attempt_count = attempt_count + 1, updated_at = now()
			FROM due
			WHERE d.id = due.id
			RETURNING d.id, d.notification_id, d.tenant_id, d.body, d.attempt_count, d.max_attempts
		)
		SELECT c.id, c.notification_id, c.tenant_id,
		       c.body,
		       n.recipient_name, COALESCE(n.recipient_phone, ''),
		       c.attempt_count, c.max_attempts
		FROM claimed c
		JOIN notifications n ON n.id = c.notification_id
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DueSMSDelivery{}
	for rows.Next() {
		var d DueSMSDelivery
		if err := rows.Scan(
			&d.DeliveryID, &d.NotificationID, &d.TenantID,
			&d.Body, &d.RecipientName, &d.RecipientPhone,
			&d.AttemptCount, &d.MaxAttempts,
		); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDeliveredTx is the terminal-success transition used by the
// delivery-report webhook (SMS) and any future read-receipt webhooks.
func (s *NotificationStore) MarkDeliveredTx(
	ctx context.Context, tx pgx.Tx,
	deliveryID uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'delivered', delivered_at = now(), updated_at = now()
		WHERE id = $1
	`, deliveryID)
	return err
}

// FindByProviderMessageIDTx looks up a delivery row by its
// provider-side correlation id. Used by the SMS delivery-report
// webhook to resolve "who is this AT status callback for?".
func (s *NotificationStore) FindByProviderMessageIDTx(
	ctx context.Context, tx pgx.Tx, channel domain.Channel, providerMessageID string,
) (uuid.UUID, uuid.UUID, error) {
	var id, tenantID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id FROM notification_deliveries
		WHERE channel = $1 AND provider_message_id = $2
		LIMIT 1
	`, string(channel), providerMessageID).Scan(&id, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	return id, tenantID, err
}

// AllActiveTenantsTx returns tenant IDs the worker needs to scan.
// Lives here (not in TenantStore) because the worker pulls active
// tenants then iterates inside its own loop.
func (s *NotificationStore) AllActiveTenantsTx(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM tenants WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func prefixedCols(cols, alias string) string {
	parts := strings.Split(cols, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, alias+"."+strings.TrimSpace(p))
	}
	return strings.Join(out, ", ")
}
