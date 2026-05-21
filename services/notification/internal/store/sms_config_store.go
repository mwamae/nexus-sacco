// SMS configuration storage. Mirrors smtp_config_store — encrypted
// columns are sealed at the store boundary so callers always work in
// plaintext.

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

type SMSConfigStore struct {
	pool      *pgxpool.Pool
	cryptoKey []byte
}

func NewSMSConfigStore(pool *pgxpool.Pool, cryptoKey []byte) *SMSConfigStore {
	return &SMSConfigStore{pool: pool, cryptoKey: cryptoKey}
}

const smsCols = `
	tenant_id, provider, username, api_key_enc, sender_id,
	rate_per_minute, webhook_secret_enc, is_active, updated_at
`

func (s *SMSConfigStore) scan(row pgx.Row) (*domain.SMSConfig, error) {
	var c domain.SMSConfig
	var provider, apiKeyEnc, webhookEnc string
	err := row.Scan(
		&c.TenantID, &provider, &c.Username, &apiKeyEnc, &c.SenderID,
		&c.RatePerMinute, &webhookEnc, &c.IsActive, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Provider = domain.SMSProvider(provider)
	if apiKeyEnc != "" {
		k, derr := crypto.Decrypt(s.cryptoKey, apiKeyEnc)
		if derr != nil {
			return nil, derr
		}
		c.APIKey = k
	}
	if webhookEnc != "" {
		w, derr := crypto.Decrypt(s.cryptoKey, webhookEnc)
		if derr != nil {
			return nil, derr
		}
		c.WebhookSecret = w
	}
	return &c, nil
}

func (s *SMSConfigStore) GetTx(ctx context.Context, tx pgx.Tx) (*domain.SMSConfig, error) {
	row := tx.QueryRow(ctx, `SELECT `+smsCols+` FROM notification_sms_configs LIMIT 1`)
	c, err := s.scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (s *SMSConfigStore) GetByTenantTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.SMSConfig, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+smsCols+` FROM notification_sms_configs WHERE tenant_id = $1`,
		tenantID,
	)
	c, err := s.scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// Tenant lookup by webhook secret — used by the AT delivery report
// handler to figure out which tenant a callback belongs to. AT's
// payload doesn't carry tenant context, so we either route by URL
// path (per-tenant webhook URL) or by matching the provider_message_id
// across all rows. The latter is what we use; this helper is here for
// when stage 4's per-tenant signed webhooks land.
func (s *SMSConfigStore) ResolveTenantByMessageIDTx(ctx context.Context, tx pgx.Tx, providerMessageID string) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT tenant_id FROM notification_deliveries
		WHERE channel = 'sms' AND provider_message_id = $1
		LIMIT 1
	`, providerMessageID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return tenantID, err
}

type UpsertSMSInput struct {
	Provider      domain.SMSProvider
	Username      string
	APIKey        string // plaintext; "" leaves existing unchanged
	SenderID      string
	RatePerMinute int
	WebhookSecret string // plaintext; "" leaves existing unchanged
	IsActive      bool
}

func (s *SMSConfigStore) UpsertTx(ctx context.Context, tx pgx.Tx, in UpsertSMSInput) (*domain.SMSConfig, error) {
	keepAPI := in.APIKey == ""
	keepHook := in.WebhookSecret == ""

	var apiKeyEnc, webhookEnc string
	if !keepAPI {
		enc, err := crypto.Encrypt(s.cryptoKey, in.APIKey)
		if err != nil {
			return nil, err
		}
		apiKeyEnc = enc
	}
	if !keepHook {
		enc, err := crypto.Encrypt(s.cryptoKey, in.WebhookSecret)
		if err != nil {
			return nil, err
		}
		webhookEnc = enc
	}
	if in.RatePerMinute <= 0 {
		in.RatePerMinute = 600
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_sms_configs (
			tenant_id, provider, username, api_key_enc, sender_id,
			rate_per_minute, webhook_secret_enc, is_active, updated_at
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5, $6, $7, now()
		)
		ON CONFLICT (tenant_id) DO UPDATE SET
			provider           = EXCLUDED.provider,
			username           = EXCLUDED.username,
			api_key_enc        = CASE WHEN $8::boolean
				THEN notification_sms_configs.api_key_enc
				ELSE EXCLUDED.api_key_enc END,
			sender_id          = EXCLUDED.sender_id,
			rate_per_minute    = EXCLUDED.rate_per_minute,
			webhook_secret_enc = CASE WHEN $9::boolean
				THEN notification_sms_configs.webhook_secret_enc
				ELSE EXCLUDED.webhook_secret_enc END,
			is_active          = EXCLUDED.is_active,
			updated_at         = now()
		RETURNING `+smsCols,
		string(in.Provider), in.Username, apiKeyEnc, in.SenderID,
		in.RatePerMinute, webhookEnc, in.IsActive,
		keepAPI, keepHook,
	)
	return s.scan(row)
}
