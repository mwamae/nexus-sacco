// Virtual-till store. Auto-provisions the per-(tenant, non-cash channel)
// reconciliation row on first use so the Collection Desk doesn't have
// to make tenants manually create one per channel.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/domain"
)

type VirtualTillStore struct {
	pool *pgxpool.Pool
}

func NewVirtualTillStore(pool *pgxpool.Pool) *VirtualTillStore {
	return &VirtualTillStore{pool: pool}
}

// virtualTillDefaults — keep in sync with the comment block in
// migration 0022_collection_desk.up.sql. New channels get an entry
// here AND in the migration's documentation.
var virtualTillDefaults = map[domain.ReceiptChannel]struct {
	GLCode      string
	DisplayName string
}{
	domain.RCMpesa:         {GLCode: "1020", DisplayName: "M-Pesa Suspense"},
	domain.RCAirtelMoney:   {GLCode: "1021", DisplayName: "Airtel Money Suspense"},
	domain.RCBankTransfer:  {GLCode: "1030", DisplayName: "Bank Transfer Suspense"},
	domain.RCCheque:        {GLCode: "1040", DisplayName: "Cheque Suspense"},
	domain.RCStandingOrder: {GLCode: "1050", DisplayName: "Standing Order Suspense"},
}

// EnsureForChannelTx returns the (tenant, channel) virtual till, creating
// it from defaults if it doesn't yet exist. Idempotent — concurrent
// inserts collapse via the UNIQUE (tenant_id, channel) constraint.
// Returns ErrInvalidArgument when called with the cash channel (cash
// flows through real till_sessions in the accounting service, never
// a virtual till).
func (s *VirtualTillStore) EnsureForChannelTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, channel domain.ReceiptChannel,
) (*domain.VirtualTill, error) {
	if channel == domain.RCCash {
		return nil, fmt.Errorf("virtual till requested for cash channel; cash uses till_sessions")
	}
	defaults, ok := virtualTillDefaults[channel]
	if !ok {
		return nil, fmt.Errorf("unknown channel for virtual till: %s", channel)
	}
	// Upsert-style: insert ignoring conflict, then read. One round-trip
	// for the create-first-time case, two for the already-exists case;
	// always-correct semantics either way.
	if _, err := tx.Exec(ctx, `
		INSERT INTO virtual_tills (tenant_id, channel, gl_account_code, display_name)
		VALUES ($1, $2::receipt_channel, $3, $4)
		ON CONFLICT (tenant_id, channel) DO NOTHING
	`, tenantID, string(channel), defaults.GLCode, defaults.DisplayName); err != nil {
		return nil, fmt.Errorf("ensure virtual till (%s): %w", channel, err)
	}
	return s.GetByChannelTx(ctx, tx, tenantID, channel)
}

func (s *VirtualTillStore) GetByChannelTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, channel domain.ReceiptChannel,
) (*domain.VirtualTill, error) {
	var v domain.VirtualTill
	var kindStr string
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, channel::text, gl_account_code, display_name, is_active,
		       created_at, updated_at
		  FROM virtual_tills WHERE tenant_id = $1 AND channel = $2::receipt_channel
	`, tenantID, string(channel)).Scan(
		&v.ID, &v.TenantID, &kindStr, &v.GLAccountCode, &v.DisplayName, &v.IsActive,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Channel = domain.ReceiptChannel(kindStr)
	return &v, nil
}

// ListTx — used by EOD reconciliation views; returns every virtual
// till in the tenant.
func (s *VirtualTillStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.VirtualTill, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, channel::text, gl_account_code, display_name, is_active,
		       created_at, updated_at
		  FROM virtual_tills
		 ORDER BY channel
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VirtualTill
	for rows.Next() {
		var v domain.VirtualTill
		var kindStr string
		if err := rows.Scan(&v.ID, &v.TenantID, &kindStr, &v.GLAccountCode,
			&v.DisplayName, &v.IsActive, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Channel = domain.ReceiptChannel(kindStr)
		out = append(out, v)
	}
	return out, rows.Err()
}
