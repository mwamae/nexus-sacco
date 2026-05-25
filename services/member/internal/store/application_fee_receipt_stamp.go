// Stamp application fee payments onto the new counterparty's
// ledger as posted_outside_till receipts.
//
// The receipts + receipt_lines tables are owned by the savings
// service, but we INSERT into them directly from the member service
// — same pattern as PostOpeningContributionsTx, which also reaches
// across into share_accounts / deposit_accounts. Direct shared-DB
// access keeps the materialise transaction atomic: a halfway
// failure on the receipt stamp rolls back alongside the rest of
// materialise, no saga / compensation logic required.
//
// Idempotency: each receipt carries application_payment_id (the
// FK column added in savings migration 0029). The UNIQUE partial
// index on that column means re-running the stamper either
// silently skips or returns the existing receipt id. We also do a
// SELECT-first to avoid noisy constraint-violation log lines.

package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type ApplicationFeeReceiptStamper struct {
	pool *pgxpool.Pool
}

func NewApplicationFeeReceiptStamper(pool *pgxpool.Pool) *ApplicationFeeReceiptStamper {
	return &ApplicationFeeReceiptStamper{pool: pool}
}

// FeeReceiptStampResult — what the caller logs after each stamp.
// Holds counts (created vs already-present) + the per-payment receipt
// ids so the admin restamp endpoint can echo them back.
type FeeReceiptStampResult struct {
	Created       int
	AlreadyExists int
	Receipts      []FeeReceiptStamped
}

type FeeReceiptStamped struct {
	PaymentID uuid.UUID `json:"payment_id"`
	ReceiptID uuid.UUID `json:"receipt_id"`
	Serial    string    `json:"serial"`
	Existing  bool      `json:"existing"`
}

// FeeReceiptStampInput is what the stamper needs to know about
// the parent application. Materialise has all of this in hand by
// the time it gets here.
type FeeReceiptStampInput struct {
	TenantID                uuid.UUID
	ApplicationID           uuid.UUID
	ApplicationNo           string
	MaterializedCounterpartyID uuid.UUID
}

// StampTx is the entry point. Reads every live + posted payment
// row for the application, then per row either INSERTs a synthetic
// receipt + line or skips when one already exists. Order is
// posted_at ASC so the per-application serial seq matches what the
// backfill migration produced.
//
// All inserts run inside the caller's tx — the materialise handler
// wraps this so a failure rolls back the whole materialise.
func (s *ApplicationFeeReceiptStamper) StampTx(
	ctx context.Context, tx pgx.Tx, in FeeReceiptStampInput,
) (*FeeReceiptStampResult, error) {
	out := &FeeReceiptStampResult{}

	// Pull the eligible payments in deterministic order.
	rows, err := tx.Query(ctx, `
		SELECT id, amount, channel, channel_reference, value_date,
		       posted_at, journal_entry_id, created_by, note
		  FROM application_fee_payments
		 WHERE application_id = $1
		   AND voided_at IS NULL
		   AND journal_entry_id IS NOT NULL
		 ORDER BY posted_at ASC, created_at ASC
	`, in.ApplicationID)
	if err != nil {
		return nil, fmt.Errorf("list eligible payments: %w", err)
	}
	type pay struct {
		ID               uuid.UUID
		Amount           decimal.Decimal
		Channel          string
		ChannelReference *string
		ValueDate        time.Time
		PostedAt         time.Time
		JournalEntryID   uuid.UUID
		CreatedBy        uuid.UUID
		Note             *string
	}
	var payments []pay
	for rows.Next() {
		var p pay
		if err := rows.Scan(
			&p.ID, &p.Amount, &p.Channel, &p.ChannelReference, &p.ValueDate,
			&p.PostedAt, &p.JournalEntryID, &p.CreatedBy, &p.Note,
		); err != nil {
			rows.Close()
			return nil, err
		}
		payments = append(payments, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, p := range payments {
		seq := i + 1
		// Idempotency check (SELECT-first to keep error logs quiet
		// when the UNIQUE index would otherwise be the gate).
		var existingID uuid.UUID
		var existingSerial string
		err := tx.QueryRow(ctx,
			`SELECT id, serial FROM receipts WHERE application_payment_id = $1`,
			p.ID,
		).Scan(&existingID, &existingSerial)
		if err == nil {
			out.AlreadyExists++
			out.Receipts = append(out.Receipts, FeeReceiptStamped{
				PaymentID: p.ID, ReceiptID: existingID, Serial: existingSerial, Existing: true,
			})
			continue
		}
		// Spec rule: cash payments get channel_ref nulled on the
		// receipt; the partial UNIQUE on (tenant, channel, channel_ref)
		// and the existing "cash has no ref" semantics both demand it.
		// The original ref stays on application_fee_payments for audit.
		var ref *string
		if p.Channel != "cash" {
			ref = p.ChannelReference
		}
		// application_no is "APP-YYYY-NNNNNN"; strip the redundant
		// leading "APP-" before splicing into the receipt serial so
		// we get R-APP-2026-000007-01 (per spec example) rather
		// than R-APP-APP-2026-000007-01.
		serial := fmt.Sprintf("R-APP-%s-%02d", strings.TrimPrefix(in.ApplicationNo, "APP-"), seq)
		narration := strings.TrimSpace("Application fee · " + in.ApplicationNo)

		var receiptID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO receipts (
			  tenant_id, serial, counterparty_id, channel, channel_ref,
			  channel_amount, value_date, narration, cashier_user_id,
			  till_session_id, virtual_till_id, status,
			  posted_at, posted_outside_till,
			  application_id, application_payment_id
			) VALUES (
			  $1, $2, $3, $4::receipt_channel, $5,
			  $6, $7, $8, $9,
			  NULL, NULL, 'posted'::receipt_status,
			  $10, true,
			  $11, $12
			)
			RETURNING id
		`,
			in.TenantID, serial, in.MaterializedCounterpartyID, p.Channel, ref,
			p.Amount, p.ValueDate, narration, p.CreatedBy,
			p.PostedAt,
			in.ApplicationID, p.ID,
		).Scan(&receiptID)
		if err != nil {
			return nil, fmt.Errorf("insert receipt for payment %s: %w", p.ID, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO receipt_lines (
			  receipt_id, line_no, kind, amount, fee_code,
			  narration, posted_txn_id, status, posted_at
			) VALUES (
			  $1, 1, 'fee'::receipt_line_kind, $2, 'membership_registration',
			  $3, $4, 'posted'::receipt_line_status, $5
			)
		`, receiptID, p.Amount, p.Note, p.JournalEntryID, p.PostedAt); err != nil {
			return nil, fmt.Errorf("insert receipt line for payment %s: %w", p.ID, err)
		}

		out.Created++
		out.Receipts = append(out.Receipts, FeeReceiptStamped{
			PaymentID: p.ID, ReceiptID: receiptID, Serial: serial, Existing: false,
		})
	}
	return out, nil
}
