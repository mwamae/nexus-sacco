// Persistence for mpesa_inbound_events.
//
// Two access patterns:
//   - The confirmation webhook handler does an idempotent insert.
//     Safaricom retries are absorbed via ON CONFLICT DO NOTHING on
//     the (tenant_id, transaction_id) UNIQUE constraint.
//   - The staff-facing GET /v1/mpesa/c2b/events lists rows with a
//     small but flexible filter set (paybill, msisdn, bill_ref,
//     status, date range) for the recent-traffic panel.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/mpesa/internal/domain"
)

type InboundEventStore struct {
	pool *pgxpool.Pool
}

func NewInboundEventStore(pool *pgxpool.Pool) *InboundEventStore {
	return &InboundEventStore{pool: pool}
}

type RecordInboundInput struct {
	TenantID        uuid.UUID
	PaybillID       uuid.UUID
	Shortcode       string
	TransactionID   string
	TransactionTime *time.Time
	Amount          string
	MSISDN          string
	BillRef         string
	RawPayload      json.RawMessage
}

// RecordTx persists the raw event. Returns the row and a bool that's
// true when the row was actually inserted (false on idempotent
// replay — same MpesaReceiptNumber landed before). The handler uses
// the bool to skip resolver + audit on dupes.
func (s *InboundEventStore) RecordTx(ctx context.Context, tx pgx.Tx, in RecordInboundInput) (*domain.InboundEvent, bool, error) {
	var e domain.InboundEvent
	var inserted bool
	err := tx.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO mpesa_inbound_events
				(tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
				 amount, msisdn, bill_ref, raw_payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (tenant_id, transaction_id) DO NOTHING
			RETURNING id, tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
			          amount::text, msisdn, bill_ref, raw_payload,
			          status, resolved_member_id, resolved_via,
			          workflow_instance_id, received_at, true AS inserted
		)
		SELECT * FROM ins
		UNION ALL
		SELECT id, tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
		       amount::text, msisdn, bill_ref, raw_payload,
		       status, resolved_member_id, resolved_via,
		       workflow_instance_id, received_at, false AS inserted
		  FROM mpesa_inbound_events
		 WHERE tenant_id = $1 AND transaction_id = $4
		 LIMIT 1
	`,
		in.TenantID, in.PaybillID, in.Shortcode, in.TransactionID, in.TransactionTime,
		in.Amount, nullIfEmpty(in.MSISDN), nullIfEmpty(in.BillRef), in.RawPayload,
	).Scan(
		&e.ID, &e.TenantID, &e.PaybillID, &e.Shortcode, &e.TransactionID, &e.TransactionTime,
		&e.Amount, &e.MSISDN, &e.BillRef, &e.RawPayload,
		&e.Status, &e.ResolvedMemberID, &e.ResolvedVia,
		&e.WorkflowInstanceID, &e.ReceivedAt, &inserted,
	)
	if err != nil {
		return nil, false, err
	}
	return &e, inserted, nil
}

type UpdateResolutionInput struct {
	ID                 uuid.UUID
	ResolvedMemberID   *uuid.UUID
	ResolvedVia        domain.ResolvedVia
	WorkflowInstanceID *uuid.UUID
}

func (s *InboundEventStore) UpdateResolutionTx(ctx context.Context, tx pgx.Tx, in UpdateResolutionInput) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_inbound_events
		   SET resolved_member_id   = $2,
		       resolved_via         = $3,
		       workflow_instance_id = $4,
		       resolved_at          = now()
		 WHERE id = $1
	`, in.ID, in.ResolvedMemberID, in.ResolvedVia, in.WorkflowInstanceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type ListInboundInput struct {
	PaybillID *uuid.UUID
	MSISDN    string
	BillRef   string
	Status    domain.InboundStatus
	From, To  *time.Time
	Limit     int
	Offset    int
}

type ListInboundResult struct {
	Events []*domain.InboundEvent
	Total  int
}

func (s *InboundEventStore) ListTx(ctx context.Context, tx pgx.Tx, in ListInboundInput) (*ListInboundResult, error) {
	if in.Limit <= 0 || in.Limit > 200 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	where := []string{}
	args := []any{}
	if in.PaybillID != nil {
		args = append(args, *in.PaybillID)
		where = append(where, fmt.Sprintf("paybill_id = $%d", len(args)))
	}
	if in.MSISDN != "" {
		args = append(args, in.MSISDN)
		where = append(where, fmt.Sprintf("msisdn = $%d", len(args)))
	}
	if in.BillRef != "" {
		args = append(args, in.BillRef)
		where = append(where, fmt.Sprintf("bill_ref = $%d", len(args)))
	}
	if in.Status != "" {
		args = append(args, in.Status)
		where = append(where, fmt.Sprintf("status = $%d::mpesa_inbound_status", len(args)))
	}
	if in.From != nil {
		args = append(args, *in.From)
		where = append(where, fmt.Sprintf("received_at >= $%d", len(args)))
	}
	if in.To != nil {
		args = append(args, *in.To)
		where = append(where, fmt.Sprintf("received_at <  $%d", len(args)))
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM mpesa_inbound_events"+whereSQL, args...,
	).Scan(&total); err != nil {
		return nil, err
	}

	args = append(args, in.Limit, in.Offset)
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
		       amount::text, msisdn, bill_ref, raw_payload,
		       status, resolved_member_id, resolved_via,
		       workflow_instance_id, received_at
		  FROM mpesa_inbound_events`+whereSQL+`
		 ORDER BY received_at DESC
		 LIMIT $`+sqlIdx(len(args)-1)+` OFFSET $`+sqlIdx(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ListInboundResult{Total: total, Events: []*domain.InboundEvent{}}
	for rows.Next() {
		var e domain.InboundEvent
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.PaybillID, &e.Shortcode, &e.TransactionID, &e.TransactionTime,
			&e.Amount, &e.MSISDN, &e.BillRef, &e.RawPayload,
			&e.Status, &e.ResolvedMemberID, &e.ResolvedVia,
			&e.WorkflowInstanceID, &e.ReceivedAt,
		); err != nil {
			return nil, err
		}
		out.Events = append(out.Events, &e)
	}
	return out, rows.Err()
}

// ByIDTx is a single-row read by id, used by tests + the staff
// reconciliation endpoint (phase 3).
func (s *InboundEventStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.InboundEvent, error) {
	var e domain.InboundEvent
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
		       amount::text, msisdn, bill_ref, raw_payload,
		       status, resolved_member_id, resolved_via,
		       workflow_instance_id, received_at
		  FROM mpesa_inbound_events WHERE id = $1
	`, id).Scan(
		&e.ID, &e.TenantID, &e.PaybillID, &e.Shortcode, &e.TransactionID, &e.TransactionTime,
		&e.Amount, &e.MSISDN, &e.BillRef, &e.RawPayload,
		&e.Status, &e.ResolvedMemberID, &e.ResolvedVia,
		&e.WorkflowInstanceID, &e.ReceivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// nullIfEmpty turns "" into a SQL NULL so non-required text columns
// store actual NULLs rather than empty strings.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sqlIdx renders a small int as a base-10 string for $N placeholder
// construction. Avoids pulling strconv into a hot path.
func sqlIdx(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	// up to 99 is plenty for our limit/offset slots.
	return string(digits[n/10]) + string(digits[n%10])
}
