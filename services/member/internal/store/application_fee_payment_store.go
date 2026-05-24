// Store for late-fee captures on membership applications.
//
// Every Insert / Void path here is paired with a RecomputeAggregates
// call so the denormalised fee_* columns on membership_applications
// stay in lockstep with the rows below them. Callers are expected
// to run all three in a single WithTenantTx so the aggregates never
// reflect a partial write.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/member/internal/domain"
)

type ApplicationFeePaymentStore struct {
	pool *pgxpool.Pool
}

func NewApplicationFeePaymentStore(pool *pgxpool.Pool) *ApplicationFeePaymentStore {
	return &ApplicationFeePaymentStore{pool: pool}
}

const feePaymentCols = `
	id, tenant_id, application_id, amount, channel, channel_reference,
	value_date, proof_doc_path, note,
	journal_entry_id, posted_at,
	voided_at, void_reason, voided_by,
	created_at, created_by
`

func scanFeePayment(row pgx.Row) (*domain.ApplicationFeePayment, error) {
	var p domain.ApplicationFeePayment
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.ApplicationID, &p.Amount, &p.Channel, &p.ChannelReference,
		&p.ValueDate, &p.ProofDocPath, &p.Note,
		&p.JournalEntryID, &p.PostedAt,
		&p.VoidedAt, &p.VoidReason, &p.VoidedBy,
		&p.CreatedAt, &p.CreatedBy,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

type FeePaymentInsertInput struct {
	ApplicationID    uuid.UUID
	Amount           decimal.Decimal
	Channel          string
	ChannelReference *string
	ValueDate        time.Time
	ProofDocPath     *string
	Note             *string
	CreatedBy        uuid.UUID
}

// InsertTx writes a single payment row. The caller must follow with
// SetJournalEntryTx after the GL post succeeds, and finally
// RecomputeAggregatesTx in the same tx.
func (s *ApplicationFeePaymentStore) InsertTx(ctx context.Context, tx pgx.Tx, in FeePaymentInsertInput) (*domain.ApplicationFeePayment, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO application_fee_payments (
		  tenant_id, application_id, amount, channel, channel_reference,
		  value_date, proof_doc_path, note, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4,
		  $5, $6, $7, $8
		)
		RETURNING `+feePaymentCols,
		in.ApplicationID, in.Amount, in.Channel, in.ChannelReference,
		in.ValueDate, in.ProofDocPath, in.Note, in.CreatedBy,
	)
	return scanFeePayment(row)
}

// SetJournalEntryTx stamps the GL JE id + posted_at after the
// accounting service confirms the post. Skipped if the JE id is the
// nil uuid (dev / disabled posting path — see handler comments).
func (s *ApplicationFeePaymentStore) SetJournalEntryTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, jeID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE application_fee_payments
		   SET journal_entry_id = $2, posted_at = now()
		 WHERE id = $1
	`, paymentID, jeID)
	return err
}

// GetByIDTx returns one row by id.
func (s *ApplicationFeePaymentStore) GetByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.ApplicationFeePayment, error) {
	row := tx.QueryRow(ctx, `SELECT `+feePaymentCols+` FROM application_fee_payments WHERE id = $1`, id)
	p, err := scanFeePayment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrFeePaymentNotFound
	}
	return p, err
}

// ListByApplicationTx returns every payment for an application,
// newest first. Voided rows included so the UI can surface the void
// state alongside the live history.
func (s *ApplicationFeePaymentStore) ListByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.ApplicationFeePayment, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+feePaymentCols+`
		  FROM application_fee_payments
		 WHERE application_id = $1
		 ORDER BY COALESCE(posted_at, created_at) DESC, created_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ApplicationFeePayment{}
	for rows.Next() {
		p, err := scanFeePayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// FindLiveByChannelRefTx is the idempotency check. Returns the
// existing live (non-voided) row with the same (channel,
// channel_reference) — within the current tenant via RLS. Returns
// nil + nil when nothing matches. Cash with nil reference always
// returns nil + nil (nothing to dedup on).
func (s *ApplicationFeePaymentStore) FindLiveByChannelRefTx(ctx context.Context, tx pgx.Tx, channel string, channelReference *string) (*domain.ApplicationFeePayment, error) {
	if channelReference == nil || *channelReference == "" {
		return nil, nil
	}
	row := tx.QueryRow(ctx, `
		SELECT `+feePaymentCols+`
		  FROM application_fee_payments
		 WHERE channel = $1
		   AND channel_reference = $2
		   AND voided_at IS NULL
		 LIMIT 1
	`, channel, *channelReference)
	p, err := scanFeePayment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// FindLiveReceiptByChannelRefTx checks the Collection Desk's
// receipts table for a non-voided row with the same channel + ref.
// receipts.channel_ref is the only field name (no separate channel
// column on that table — the channel is part of the receipt). We
// match by channel_ref alone; the handler logs the receipt serial
// so the operator can investigate manually if needed.
//
// Returns the receipt serial when matched, empty string when not.
// receipts table lives in the savings service's schema; we share
// the DB so a read is fine.
func (s *ApplicationFeePaymentStore) FindLiveReceiptByChannelRefTx(ctx context.Context, tx pgx.Tx, channelReference *string) (string, error) {
	if channelReference == nil || *channelReference == "" {
		return "", nil
	}
	var serial string
	err := tx.QueryRow(ctx, `
		SELECT serial FROM receipts
		 WHERE channel_ref = $1
		   AND voided_at IS NULL
		 LIMIT 1
	`, *channelReference).Scan(&serial)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		// Receipts table absent in some dev setups; don't block the
		// fee-payment path on it.
		return "", nil
	}
	return serial, nil
}

// VoidTx marks the row voided. Aggregates must be recomputed
// afterwards in the same tx.
func (s *ApplicationFeePaymentStore) VoidTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID, voidedBy uuid.UUID, reason string) (*domain.ApplicationFeePayment, error) {
	row := tx.QueryRow(ctx, `
		UPDATE application_fee_payments
		   SET voided_at = now(), voided_by = $2, void_reason = $3
		 WHERE id = $1
		   AND voided_at IS NULL
		RETURNING `+feePaymentCols,
		paymentID, voidedBy, reason,
	)
	p, err := scanFeePayment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrFeePaymentAlreadyVoided
	}
	return p, err
}

// FeeAggregates is what RecomputeAggregatesTx writes back onto the
// parent application row. Exposed so the handler can include the
// recomputed values in its response without a second SELECT.
type FeeAggregates struct {
	AmountPaid        decimal.Decimal
	LatestChannel     *string
	LatestReference   *string
	LatestPaymentDate *time.Time
	Status            string
}

// RecomputeAggregatesTx rebuilds the denormalised fee_* columns on
// membership_applications from the rows in application_fee_payments,
// then UPDATEs the application row in-place. Returns the values it
// wrote so the handler doesn't have to re-fetch the application.
//
// Status derivation:
//
//	fee_amount_due = 0                              → not_required
//	paid >= due                                     → paid
//	paid > 0                                        → shortfall
//	paid == 0                                       → not_paid
//
// We deliberately don't touch refund_pending / refunded — those are
// flipped by the refund path and shouldn't be clobbered just
// because a stale payment was voided.
func (s *ApplicationFeePaymentStore) RecomputeAggregatesTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) (*FeeAggregates, error) {
	// Aggregate the live rows.
	var (
		paid              decimal.Decimal
		latestChannel     *string
		latestReference   *string
		latestPaymentDate *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(amount) FILTER (WHERE voided_at IS NULL), 0)
		FROM application_fee_payments
		WHERE application_id = $1
	`, appID).Scan(&paid); err != nil {
		return nil, fmt.Errorf("aggregate fee_amount_paid: %w", err)
	}
	// Latest non-voided row drives channel / reference / date. NULL
	// when there are no live payments.
	err := tx.QueryRow(ctx, `
		SELECT channel, channel_reference, value_date
		  FROM application_fee_payments
		 WHERE application_id = $1
		   AND voided_at IS NULL
		 ORDER BY COALESCE(posted_at, created_at) DESC, created_at DESC
		 LIMIT 1
	`, appID).Scan(&latestChannel, &latestReference, &latestPaymentDate)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read latest payment: %w", err)
	}

	// Derive status. Read fee_amount_due + current fee_status so we
	// can preserve refund_pending / refunded if the app is already
	// in the refund pipeline.
	var (
		feeDue        decimal.Decimal
		currentStatus string
	)
	if err := tx.QueryRow(ctx, `
		SELECT fee_amount_due, fee_status
		  FROM membership_applications
		 WHERE id = $1
	`, appID).Scan(&feeDue, &currentStatus); err != nil {
		return nil, fmt.Errorf("read application fee state: %w", err)
	}

	var status string
	switch {
	case currentStatus == "refund_pending" || currentStatus == "refunded":
		status = currentStatus
	case feeDue.IsZero():
		status = "not_required"
	case paid.GreaterThanOrEqual(feeDue):
		status = "paid"
	case paid.GreaterThan(decimal.Zero):
		status = "shortfall"
	default:
		status = "not_paid"
	}

	// Write back.
	if _, err := tx.Exec(ctx, `
		UPDATE membership_applications
		   SET fee_amount_paid       = $2,
		       fee_payment_channel   = $3,
		       fee_payment_reference = $4,
		       fee_payment_date      = $5,
		       fee_status            = $6,
		       updated_at            = now()
		 WHERE id = $1
	`, appID, paid, latestChannel, latestReference, latestPaymentDate, status); err != nil {
		return nil, fmt.Errorf("write aggregates: %w", err)
	}

	return &FeeAggregates{
		AmountPaid:        paid,
		LatestChannel:     latestChannel,
		LatestReference:   latestReference,
		LatestPaymentDate: latestPaymentDate,
		Status:            status,
	}, nil
}

// SumPostedTx returns the sum of journal_entry_id-stamped payments
// for an application. Used by the materialise path to decide
// whether the existing at-create-time fee post is still needed.
func (s *ApplicationFeePaymentStore) SumPostedTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) (decimal.Decimal, error) {
	var posted decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount) FILTER (
		  WHERE voided_at IS NULL AND journal_entry_id IS NOT NULL
		), 0)
		FROM application_fee_payments
		WHERE application_id = $1
	`, appID).Scan(&posted)
	return posted, err
}
