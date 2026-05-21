// Tenants submit a top-up request from inside their admin UI; the
// platform admin sees the queue and fulfils it by calling
// CreditStore.TopupTx — at which point the request row gets marked
// fulfilled and pointed at the ledger entry that recorded the credits.

package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type TopupRequestStore struct {
	pool *pgxpool.Pool
}

func NewTopupRequestStore(pool *pgxpool.Pool) *TopupRequestStore {
	return &TopupRequestStore{pool: pool}
}

const topupCols = `
	id, tenant_id, channel, credits_requested, status,
	requested_by, requested_at, fulfilled_by, fulfilled_at,
	fulfillment_ledger_id, notes, rejection_reason
`

func scanTopup(row pgx.Row) (*domain.TopupRequest, error) {
	var t domain.TopupRequest
	var channel, status string
	err := row.Scan(
		&t.ID, &t.TenantID, &channel, &t.CreditsRequested, &status,
		&t.RequestedBy, &t.RequestedAt, &t.FulfilledBy, &t.FulfilledAt,
		&t.FulfillmentLedgerID, &t.Notes, &t.RejectionReason,
	)
	if err != nil {
		return nil, err
	}
	t.Channel = domain.Channel(channel)
	t.Status = domain.TopupStatus(status)
	return &t, nil
}

type CreateTopupInput struct {
	Channel     domain.Channel
	Credits     int
	RequestedBy *uuid.UUID
	Notes       string
}

func (s *TopupRequestStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateTopupInput) (*domain.TopupRequest, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_credit_topup_requests
		    (tenant_id, channel, credits_requested, requested_by, notes)
		VALUES (current_tenant_id(), $1, $2, $3, $4)
		RETURNING `+topupCols,
		string(in.Channel), in.Credits, in.RequestedBy, nullIfEmpty(in.Notes),
	)
	return scanTopup(row)
}

func (s *TopupRequestStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.TopupRequest, error) {
	row := tx.QueryRow(ctx, `SELECT `+topupCols+` FROM notification_credit_topup_requests WHERE id = $1`, id)
	t, err := scanTopup(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

type TopupListFilter struct {
	Status  string
	Channel string
	Limit   int
	Offset  int
}

func (s *TopupRequestStore) ListTx(ctx context.Context, tx pgx.Tx, f TopupListFilter) ([]domain.TopupRequest, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + strconv.Itoa(idx)
		args = append(args, f.Status)
		idx++
	}
	if f.Channel != "" {
		where += " AND channel = $" + strconv.Itoa(idx)
		args = append(args, f.Channel)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notification_credit_topup_requests `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	rows, err := tx.Query(ctx, `
		SELECT `+topupCols+` FROM notification_credit_topup_requests `+where+`
		ORDER BY requested_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.TopupRequest{}
	for rows.Next() {
		t, err := scanTopup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

func (s *TopupRequestStore) MarkFulfilledTx(ctx context.Context, tx pgx.Tx, id, fulfilledBy, ledgerID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_credit_topup_requests
		SET status = 'fulfilled', fulfilled_by = $2, fulfilled_at = now(),
		    fulfillment_ledger_id = $3
		WHERE id = $1 AND status = 'pending'
	`, id, fulfilledBy, ledgerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *TopupRequestStore) RejectTx(ctx context.Context, tx pgx.Tx, id, rejectedBy uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_credit_topup_requests
		SET status = 'rejected', fulfilled_by = $2, fulfilled_at = now(),
		    rejection_reason = $3
		WHERE id = $1 AND status = 'pending'
	`, id, rejectedBy, nullIfEmpty(reason))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelTx — tenant cancels their own pending request.
func (s *TopupRequestStore) CancelTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_credit_topup_requests
		SET status = 'cancelled', fulfilled_at = now()
		WHERE id = $1 AND status = 'pending'
	`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────── Pricing ───────────

type PricingStore struct {
	pool *pgxpool.Pool
}

func NewPricingStore(pool *pgxpool.Pool) *PricingStore {
	return &PricingStore{pool: pool}
}

func (s *PricingStore) GetTx(ctx context.Context, tx pgx.Tx, channel domain.Channel) (*domain.CreditPricing, error) {
	var p domain.CreditPricing
	var ch string
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, channel, price_per_credit::text, currency_code, updated_at, updated_by
		FROM notification_credit_pricing WHERE channel = $1
	`, string(channel)).Scan(&p.TenantID, &ch, &p.PricePerCredit, &p.CurrencyCode, &p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Channel = domain.Channel(ch)
	return &p, nil
}

func (s *PricingStore) ListTx(ctx context.Context, tx pgx.Tx) ([]domain.CreditPricing, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, channel, price_per_credit::text, currency_code, updated_at, updated_by
		FROM notification_credit_pricing ORDER BY channel
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CreditPricing{}
	for rows.Next() {
		var p domain.CreditPricing
		var ch string
		if err := rows.Scan(&p.TenantID, &ch, &p.PricePerCredit, &p.CurrencyCode, &p.UpdatedAt, &p.UpdatedBy); err != nil {
			return nil, err
		}
		p.Channel = domain.Channel(ch)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PricingStore) UpsertTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, pricePerCredit string, currencyCode string, by uuid.UUID) error {
	if currencyCode == "" {
		currencyCode = "KES"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_credit_pricing (tenant_id, channel, price_per_credit, currency_code, updated_by)
		VALUES (current_tenant_id(), $1, $2::numeric, $3, $4)
		ON CONFLICT (tenant_id, channel) DO UPDATE SET
		    price_per_credit = EXCLUDED.price_per_credit,
		    currency_code = EXCLUDED.currency_code,
		    updated_at = now(),
		    updated_by = EXCLUDED.updated_by
	`, string(channel), pricePerCredit, currencyCode, by)
	return err
}

// ─────────── Adjustments (maker/checker) ───────────

type AdjustmentStore struct {
	pool *pgxpool.Pool
}

func NewAdjustmentStore(pool *pgxpool.Pool) *AdjustmentStore {
	return &AdjustmentStore{pool: pool}
}

const adjCols = `
	id, tenant_id, channel, credits, reason, status,
	requested_by, requested_at, approved_by, approved_at,
	rejected_by, rejected_at, rejection_reason, applied_ledger_id
`

func scanAdj(row pgx.Row) (*domain.CreditAdjustment, error) {
	var a domain.CreditAdjustment
	var channel, status string
	err := row.Scan(
		&a.ID, &a.TenantID, &channel, &a.Credits, &a.Reason, &status,
		&a.RequestedBy, &a.RequestedAt, &a.ApprovedBy, &a.ApprovedAt,
		&a.RejectedBy, &a.RejectedAt, &a.RejectionReason, &a.AppliedLedgerID,
	)
	if err != nil {
		return nil, err
	}
	a.Channel = domain.Channel(channel)
	a.Status = domain.AdjustmentStatus(status)
	return &a, nil
}

type CreateAdjustmentInput struct {
	Channel     domain.Channel
	Credits     int
	Reason      string
	RequestedBy uuid.UUID
}

func (s *AdjustmentStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateAdjustmentInput) (*domain.CreditAdjustment, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO notification_credit_adjustments
		    (tenant_id, channel, credits, reason, requested_by)
		VALUES (current_tenant_id(), $1, $2, $3, $4)
		RETURNING `+adjCols,
		string(in.Channel), in.Credits, in.Reason, in.RequestedBy,
	)
	return scanAdj(row)
}

func (s *AdjustmentStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.CreditAdjustment, error) {
	row := tx.QueryRow(ctx, `SELECT `+adjCols+` FROM notification_credit_adjustments WHERE id = $1`, id)
	a, err := scanAdj(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

type AdjustmentListFilter struct {
	Status string
	Limit  int
	Offset int
}

func (s *AdjustmentStore) ListTx(ctx context.Context, tx pgx.Tx, f AdjustmentListFilter) ([]domain.CreditAdjustment, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += " AND status = $" + strconv.Itoa(idx)
		args = append(args, f.Status)
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notification_credit_adjustments `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	rows, err := tx.Query(ctx, `
		SELECT `+adjCols+` FROM notification_credit_adjustments `+where+`
		ORDER BY requested_at DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.CreditAdjustment{}
	for rows.Next() {
		a, err := scanAdj(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

func (s *AdjustmentStore) MarkApprovedTx(ctx context.Context, tx pgx.Tx, id, approvedBy, ledgerID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_credit_adjustments
		SET status = 'approved', approved_by = $2, approved_at = now(),
		    applied_ledger_id = $3
		WHERE id = $1 AND status = 'pending_approval'
	`, id, approvedBy, ledgerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *AdjustmentStore) MarkRejectedTx(ctx context.Context, tx pgx.Tx, id, rejectedBy uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_credit_adjustments
		SET status = 'rejected', rejected_by = $2, rejected_at = now(),
		    rejection_reason = $3
		WHERE id = $1 AND status = 'pending_approval'
	`, id, rejectedBy, nullIfEmpty(reason))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
