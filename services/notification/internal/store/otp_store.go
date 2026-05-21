// otp_requests + otp_settings persistence.

package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type OTPStore struct {
	pool *pgxpool.Pool
}

func NewOTPStore(pool *pgxpool.Pool) *OTPStore {
	return &OTPStore{pool: pool}
}

const otpCols = `
	id, tenant_id, purpose,
	subject_user_id, subject_member_id, subject_identifier,
	channel, destination,
	code_hash, code_length,
	status, attempts_used, max_attempts,
	generated_at, expires_at, verified_at,
	host(ip_address), device_fingerprint,
	notification_id, created_by
`

func scanOTP(row pgx.Row) (*domain.OTPRequest, error) {
	var o domain.OTPRequest
	var purpose, channel, status string
	err := row.Scan(
		&o.ID, &o.TenantID, &purpose,
		&o.SubjectUserID, &o.SubjectMemberID, &o.SubjectIdentifier,
		&channel, &o.Destination,
		&o.CodeHash, &o.CodeLength,
		&status, &o.AttemptsUsed, &o.MaxAttempts,
		&o.GeneratedAt, &o.ExpiresAt, &o.VerifiedAt,
		&o.IPAddress, &o.DeviceFingerprint,
		&o.NotificationID, &o.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	o.Purpose = domain.OTPPurpose(purpose)
	o.Channel = domain.Channel(channel)
	o.Status = domain.OTPStatus(status)
	return &o, nil
}

// CreateOTPInput is the typed payload for inserting an OTP.
type CreateOTPInput struct {
	Purpose           domain.OTPPurpose
	SubjectUserID     *uuid.UUID
	SubjectMemberID   *uuid.UUID
	SubjectIdentifier *string
	Channel           domain.Channel
	Destination       string
	CodeHash          string
	CodeLength        int
	MaxAttempts       int
	ExpiresAt         time.Time
	IPAddress         *string
	DeviceFingerprint *string
	NotificationID    *uuid.UUID
	CreatedBy         *uuid.UUID
}

func (s *OTPStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateOTPInput) (*domain.OTPRequest, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO otp_requests (
			tenant_id, purpose,
			subject_user_id, subject_member_id, subject_identifier,
			channel, destination,
			code_hash, code_length,
			max_attempts, expires_at,
			ip_address, device_fingerprint,
			notification_id, created_by
		) VALUES (
			current_tenant_id(), $1,
			$2, $3, $4,
			$5, $6,
			$7, $8,
			$9, $10,
			$11::inet, $12,
			$13, $14
		)
		RETURNING `+otpCols,
		string(in.Purpose),
		in.SubjectUserID, in.SubjectMemberID, in.SubjectIdentifier,
		string(in.Channel), in.Destination,
		in.CodeHash, in.CodeLength,
		in.MaxAttempts, in.ExpiresAt,
		in.IPAddress, in.DeviceFingerprint,
		in.NotificationID, in.CreatedBy,
	)
	return scanOTP(row)
}

func (s *OTPStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.OTPRequest, error) {
	row := tx.QueryRow(ctx, `SELECT `+otpCols+` FROM otp_requests WHERE id = $1`, id)
	o, err := scanOTP(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// MostRecentForCooldownTx returns the timestamp of the most recent
// OTP request for the same (purpose, subject) so the caller can
// enforce the resend cooldown.
func (s *OTPStore) MostRecentForCooldownTx(
	ctx context.Context, tx pgx.Tx,
	purpose domain.OTPPurpose,
	subjectUserID, subjectMemberID *uuid.UUID,
	subjectIdentifier *string,
) (*time.Time, error) {
	row := tx.QueryRow(ctx, `
		SELECT MAX(generated_at) FROM otp_requests
		WHERE purpose = $1
		  AND (subject_user_id IS NOT DISTINCT FROM $2)
		  AND (subject_member_id IS NOT DISTINCT FROM $3)
		  AND (subject_identifier IS NOT DISTINCT FROM $4)
	`, string(purpose), subjectUserID, subjectMemberID, subjectIdentifier)
	var t *time.Time
	if err := row.Scan(&t); err != nil {
		return nil, err
	}
	return t, nil
}

// IncrementAttemptsTx is called on every verify attempt. Returns the
// new attempts_used value so the caller can decide whether to mark
// the OTP exhausted.
func (s *OTPStore) IncrementAttemptsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		UPDATE otp_requests
		SET attempts_used = attempts_used + 1
		WHERE id = $1
		RETURNING attempts_used
	`, id).Scan(&n)
	return n, err
}

func (s *OTPStore) MarkVerifiedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE otp_requests
		SET status = 'verified', verified_at = now()
		WHERE id = $1
	`, id)
	return err
}

func (s *OTPStore) MarkStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.OTPStatus) error {
	_, err := tx.Exec(ctx, `
		UPDATE otp_requests SET status = $2 WHERE id = $1 AND status = 'pending'
	`, id, string(status))
	return err
}

// AttachNotificationIDTx links the OTP to the notification that
// delivered it. Called after the notification has been created.
func (s *OTPStore) AttachNotificationIDTx(ctx context.Context, tx pgx.Tx, id, notificationID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE otp_requests SET notification_id = $2 WHERE id = $1`,
		id, notificationID,
	)
	return err
}

// ListTx — admin audit view.
type OTPListFilter struct {
	Status  string
	Purpose string
	Limit   int
	Offset  int
}

func (s *OTPStore) ListTx(ctx context.Context, tx pgx.Tx, f OTPListFilter) ([]domain.OTPRequest, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + strconv.Itoa(idx)
		args = append(args, f.Status)
		idx++
	}
	if f.Purpose != "" {
		where += " AND purpose = $" + strconv.Itoa(idx)
		args = append(args, f.Purpose)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM otp_requests `+where, args...).Scan(&total); err != nil {
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
	rows, err := tx.Query(ctx, `
		SELECT `+otpCols+` FROM otp_requests `+where+`
		ORDER BY generated_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.OTPRequest{}
	for rows.Next() {
		o, err := scanOTP(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *o)
	}
	return out, total, rows.Err()
}

// ─────────── Settings ───────────

type OTPSettingsStore struct {
	pool *pgxpool.Pool
}

func NewOTPSettingsStore(pool *pgxpool.Pool) *OTPSettingsStore {
	return &OTPSettingsStore{pool: pool}
}

const otpSettingsCols = `
	tenant_id, code_length, expiry_minutes, max_attempts,
	resend_cooldown_seconds, default_channel, updated_at
`

func scanOTPSettings(row pgx.Row) (*domain.OTPSettings, error) {
	var s domain.OTPSettings
	var ch string
	err := row.Scan(
		&s.TenantID, &s.CodeLength, &s.ExpiryMinutes, &s.MaxAttempts,
		&s.ResendCooldownSeconds, &ch, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.DefaultChannel = domain.Channel(ch)
	return &s, nil
}

// GetTx returns the current tenant's OTP policy. Auto-inserts the
// default row if missing — every tenant always has a policy.
func (s *OTPSettingsStore) GetTx(ctx context.Context, tx pgx.Tx) (*domain.OTPSettings, error) {
	row := tx.QueryRow(ctx, `SELECT `+otpSettingsCols+` FROM otp_settings LIMIT 1`)
	st, err := scanOTPSettings(row)
	if errors.Is(err, pgx.ErrNoRows) {
		row = tx.QueryRow(ctx, `
			INSERT INTO otp_settings (tenant_id) VALUES (current_tenant_id())
			RETURNING `+otpSettingsCols,
		)
		return scanOTPSettings(row)
	}
	return st, err
}

type UpsertOTPSettingsInput struct {
	CodeLength            int
	ExpiryMinutes         int
	MaxAttempts           int
	ResendCooldownSeconds int
	DefaultChannel        domain.Channel
}

func (s *OTPSettingsStore) UpsertTx(ctx context.Context, tx pgx.Tx, in UpsertOTPSettingsInput) (*domain.OTPSettings, error) {
	if in.CodeLength == 0 {
		in.CodeLength = 6
	}
	if in.ExpiryMinutes == 0 {
		in.ExpiryMinutes = 5
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 3
	}
	if in.ResendCooldownSeconds == 0 {
		in.ResendCooldownSeconds = 60
	}
	if in.DefaultChannel == "" {
		in.DefaultChannel = domain.ChannelSMS
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO otp_settings (
			tenant_id, code_length, expiry_minutes, max_attempts,
			resend_cooldown_seconds, default_channel
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5
		)
		ON CONFLICT (tenant_id) DO UPDATE SET
			code_length             = EXCLUDED.code_length,
			expiry_minutes          = EXCLUDED.expiry_minutes,
			max_attempts            = EXCLUDED.max_attempts,
			resend_cooldown_seconds = EXCLUDED.resend_cooldown_seconds,
			default_channel         = EXCLUDED.default_channel,
			updated_at              = now()
		RETURNING `+otpSettingsCols,
		in.CodeLength, in.ExpiryMinutes, in.MaxAttempts,
		in.ResendCooldownSeconds, string(in.DefaultChannel),
	)
	return scanOTPSettings(row)
}

