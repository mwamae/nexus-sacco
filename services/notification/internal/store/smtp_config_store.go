// SMTP configuration storage. Password column holds AES-GCM
// ciphertext at all times; encrypt/decrypt happens at the store
// boundary so callers always work with plaintext domain objects.

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

type SMTPConfigStore struct {
	pool      *pgxpool.Pool
	cryptoKey []byte
}

func NewSMTPConfigStore(pool *pgxpool.Pool, cryptoKey []byte) *SMTPConfigStore {
	return &SMTPConfigStore{pool: pool, cryptoKey: cryptoKey}
}

const smtpCols = `
	tenant_id, host, port, username, password_enc,
	encryption, from_address, from_name, reply_to,
	is_active, updated_at
`

func (s *SMTPConfigStore) scan(row pgx.Row) (*domain.SMTPConfig, error) {
	var c domain.SMTPConfig
	var enc, passEnc string
	err := row.Scan(
		&c.TenantID, &c.Host, &c.Port, &c.Username, &passEnc,
		&enc, &c.FromAddress, &c.FromName, &c.ReplyTo,
		&c.IsActive, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Encryption = domain.SMTPEncryption(enc)
	if passEnc != "" {
		pw, derr := crypto.Decrypt(s.cryptoKey, passEnc)
		if derr != nil {
			return nil, derr
		}
		c.Password = pw
	}
	return &c, nil
}

// GetTx returns the active tenant's SMTP config, or (nil, nil) when
// no row exists (vs. nil, err for real failures).
func (s *SMTPConfigStore) GetTx(ctx context.Context, tx pgx.Tx) (*domain.SMTPConfig, error) {
	row := tx.QueryRow(ctx, `SELECT `+smtpCols+` FROM notification_smtp_configs LIMIT 1`)
	c, err := s.scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// GetByTenantTx is the worker-side accessor — supplies tenant id
// explicitly so it can run outside a tenant-scoped tx context. Callers
// MUST have set current_tenant_id() to the matching tenant first.
func (s *SMTPConfigStore) GetByTenantTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*domain.SMTPConfig, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+smtpCols+` FROM notification_smtp_configs WHERE tenant_id = $1`,
		tenantID,
	)
	c, err := s.scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// UpsertInput is what the admin endpoint sends. Password is plaintext
// here — the store encrypts before persisting.
type UpsertSMTPInput struct {
	Host        string
	Port        int
	Username    string
	Password    string // plaintext; "" leaves existing password unchanged
	Encryption  domain.SMTPEncryption
	FromAddress string
	FromName    string
	ReplyTo     *string
	IsActive    bool
}

func (s *SMTPConfigStore) UpsertTx(ctx context.Context, tx pgx.Tx, in UpsertSMTPInput) (*domain.SMTPConfig, error) {
	// Default to keeping the existing password ciphertext when the
	// admin form is submitted without typing a new password.
	keepExisting := in.Password == ""
	var pwEnc string
	if !keepExisting {
		enc, err := crypto.Encrypt(s.cryptoKey, in.Password)
		if err != nil {
			return nil, err
		}
		pwEnc = enc
	}
	// One row per tenant. UPDATE ... ELSE INSERT pattern via ON CONFLICT.
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_smtp_configs (
			tenant_id, host, port, username, password_enc,
			encryption, from_address, from_name, reply_to, is_active, updated_at
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, now()
		)
		ON CONFLICT (tenant_id) DO UPDATE SET
			host         = EXCLUDED.host,
			port         = EXCLUDED.port,
			username     = EXCLUDED.username,
			password_enc = CASE WHEN $10::boolean
				THEN notification_smtp_configs.password_enc
				ELSE EXCLUDED.password_enc END,
			encryption   = EXCLUDED.encryption,
			from_address = EXCLUDED.from_address,
			from_name    = EXCLUDED.from_name,
			reply_to     = EXCLUDED.reply_to,
			is_active    = EXCLUDED.is_active,
			updated_at   = now()
		RETURNING `+smtpCols,
		in.Host, in.Port, in.Username, pwEnc,
		string(in.Encryption), in.FromAddress, in.FromName, in.ReplyTo, in.IsActive,
		keepExisting,
	)
	return s.scan(row)
}
