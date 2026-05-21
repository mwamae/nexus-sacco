// platform_smtp_config + platform_sms_config — singleton (id=1) tables
// holding the SHARED driver credentials owned by the platform. These
// supersede the per-tenant notification_smtp_configs / notification_sms_configs
// (kept around until a follow-up cleanup migration).

package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/crypto"
	"github.com/nexussacco/notification/internal/domain"
)

type PlatformSMTPStore struct {
	pool *pgxpool.Pool
	key  []byte // first 32 bytes of JWT_SECRET, used for AES-GCM password storage
}

func NewPlatformSMTPStore(pool *pgxpool.Pool, key []byte) *PlatformSMTPStore {
	return &PlatformSMTPStore{pool: pool, key: key}
}

func (s *PlatformSMTPStore) Get(ctx context.Context) (*domain.PlatformSMTPConfig, error) {
	var c domain.PlatformSMTPConfig
	var passwordEnc string
	err := s.pool.QueryRow(ctx, `
		SELECT host, port, encryption, username, password_enc,
		       from_address, from_name, is_enabled, updated_at, updated_by
		FROM platform_smtp_config WHERE id = 1
	`).Scan(&c.Host, &c.Port, &c.Encryption, &c.Username, &passwordEnc,
		&c.FromAddress, &c.FromName, &c.IsEnabled, &c.UpdatedAt, &c.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if passwordEnc != "" {
		pw, derr := crypto.Decrypt(s.key, passwordEnc)
		if derr == nil {
			c.Password = pw
		}
	}
	return &c, nil
}

type UpdatePlatformSMTPInput struct {
	Host        string
	Port        int
	Encryption  string
	Username    string
	Password    *string // nil = don't change; non-nil = encrypt & store
	FromAddress string
	FromName    string
	IsEnabled   bool
	UpdatedBy   uuid.UUID
}

func (s *PlatformSMTPStore) Update(ctx context.Context, in UpdatePlatformSMTPInput) (*domain.PlatformSMTPConfig, error) {
	if in.Encryption == "" {
		in.Encryption = "none"
	}
	if in.Port <= 0 {
		in.Port = 1025
	}
	var passwordEnc *string
	if in.Password != nil {
		enc, err := crypto.Encrypt(s.key, *in.Password)
		if err != nil {
			return nil, err
		}
		passwordEnc = &enc
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE platform_smtp_config SET
		    host = $1, port = $2, encryption = $3, username = $4,
		    password_enc = COALESCE($5, password_enc),
		    from_address = $6, from_name = $7, is_enabled = $8,
		    updated_at = now(), updated_by = $9
		WHERE id = 1
	`,
		in.Host, in.Port, in.Encryption, in.Username, passwordEnc,
		in.FromAddress, in.FromName, in.IsEnabled, in.UpdatedBy,
	)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx)
}

// ─────────── SMS ───────────

type PlatformSMSStore struct {
	pool *pgxpool.Pool
	key  []byte
}

func NewPlatformSMSStore(pool *pgxpool.Pool, key []byte) *PlatformSMSStore {
	return &PlatformSMSStore{pool: pool, key: key}
}

func (s *PlatformSMSStore) Get(ctx context.Context) (*domain.PlatformSMSConfig, error) {
	var c domain.PlatformSMSConfig
	var provider, apiKeyEnc, webhookSecretEnc string
	err := s.pool.QueryRow(ctx, `
		SELECT provider, username, api_key_enc, sender_id, rate_per_minute,
		       webhook_secret_enc, is_enabled, updated_at, updated_by
		FROM platform_sms_config WHERE id = 1
	`).Scan(&provider, &c.Username, &apiKeyEnc, &c.SenderID, &c.RatePerMinute,
		&webhookSecretEnc, &c.IsEnabled, &c.UpdatedAt, &c.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Provider = domain.SMSProvider(provider)
	if apiKeyEnc != "" {
		if k, derr := crypto.Decrypt(s.key, apiKeyEnc); derr == nil {
			c.APIKey = k
		}
	}
	if webhookSecretEnc != "" {
		if ws, derr := crypto.Decrypt(s.key, webhookSecretEnc); derr == nil {
			c.WebhookSecret = ws
		}
	}
	return &c, nil
}

type UpdatePlatformSMSInput struct {
	Provider      string
	Username      string
	APIKey        *string // nil = don't change
	SenderID      string
	RatePerMinute int
	WebhookSecret *string // nil = don't change
	IsEnabled     bool
	UpdatedBy     uuid.UUID
}

func (s *PlatformSMSStore) Update(ctx context.Context, in UpdatePlatformSMSInput) (*domain.PlatformSMSConfig, error) {
	if in.Provider == "" {
		in.Provider = "mock"
	}
	if in.RatePerMinute <= 0 {
		in.RatePerMinute = 600
	}
	var apiKeyEnc, webhookEnc *string
	if in.APIKey != nil {
		enc, err := crypto.Encrypt(s.key, *in.APIKey)
		if err != nil {
			return nil, err
		}
		apiKeyEnc = &enc
	}
	if in.WebhookSecret != nil {
		enc, err := crypto.Encrypt(s.key, *in.WebhookSecret)
		if err != nil {
			return nil, err
		}
		webhookEnc = &enc
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE platform_sms_config SET
		    provider = $1, username = $2,
		    api_key_enc = COALESCE($3, api_key_enc),
		    sender_id = $4, rate_per_minute = $5,
		    webhook_secret_enc = COALESCE($6, webhook_secret_enc),
		    is_enabled = $7, updated_at = now(), updated_by = $8
		WHERE id = 1
	`,
		in.Provider, in.Username, apiKeyEnc, in.SenderID, in.RatePerMinute,
		webhookEnc, in.IsEnabled, in.UpdatedBy,
	)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx)
}
