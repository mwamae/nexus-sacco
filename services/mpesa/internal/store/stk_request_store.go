// Persistence for mpesa_stk_requests (Phase 2.2 — STK Push).

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type STKRequestStore struct {
	pool *pgxpool.Pool
}

func NewSTKRequestStore(pool *pgxpool.Pool) *STKRequestStore {
	return &STKRequestStore{pool: pool}
}

type STKRequest struct {
	ID                       uuid.UUID
	TenantID                 uuid.UUID
	PaybillID                uuid.UUID
	MSISDN                   string
	Amount                   decimal.Decimal
	AccountReference         string
	TransactionDesc          string
	SourceModule             string
	SourceRef                string
	OriginatorConversationID *string
	MerchantRequestID        *string
	CheckoutRequestID        *string
	Status                   string
	ResponseCode             *string
	ResponseDescription      *string
	ResultCode               *string
	ResultDesc               *string
	MpesaReceiptNumber       *string
	InitiatedAt              time.Time
	CompletedAt              *time.Time
}

type InsertSTKRequestInput struct {
	TenantID         uuid.UUID
	PaybillID        uuid.UUID
	MSISDN           string
	Amount           decimal.Decimal
	AccountReference string
	TransactionDesc  string
	SourceModule     string
	SourceRef        string
}

func (s *STKRequestStore) InsertPendingTx(ctx context.Context, tx pgx.Tx, in InsertSTKRequestInput) (*STKRequest, error) {
	var r STKRequest
	err := tx.QueryRow(ctx, `
		INSERT INTO mpesa_stk_requests
		    (tenant_id, paybill_id, msisdn, amount,
		     account_reference, transaction_desc,
		     source_module, source_ref, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		RETURNING id, tenant_id, paybill_id, msisdn, amount,
		          account_reference, transaction_desc,
		          source_module, source_ref,
		          originator_conversation_id, merchant_request_id,
		          checkout_request_id, status,
		          response_code, response_description,
		          result_code, result_desc, mpesa_receipt_number,
		          initiated_at, completed_at
	`,
		in.TenantID, in.PaybillID, in.MSISDN, in.Amount,
		in.AccountReference, in.TransactionDesc,
		in.SourceModule, in.SourceRef,
	).Scan(
		&r.ID, &r.TenantID, &r.PaybillID, &r.MSISDN, &r.Amount,
		&r.AccountReference, &r.TransactionDesc,
		&r.SourceModule, &r.SourceRef,
		&r.OriginatorConversationID, &r.MerchantRequestID,
		&r.CheckoutRequestID, &r.Status,
		&r.ResponseCode, &r.ResponseDescription,
		&r.ResultCode, &r.ResultDesc, &r.MpesaReceiptNumber,
		&r.InitiatedAt, &r.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type MarkInitiatedInput struct {
	ID                       uuid.UUID
	OriginatorConversationID string
	MerchantRequestID        string
	CheckoutRequestID        string
	ResponseCode             string
	ResponseDescription      string
	Status                   string // 'sent' or 'failed'
	RawInitiateResponse      json.RawMessage
}

func (s *STKRequestStore) MarkInitiatedTx(ctx context.Context, tx pgx.Tx, in MarkInitiatedInput) error {
	_, err := tx.Exec(ctx, `
		UPDATE mpesa_stk_requests
		   SET originator_conversation_id = $2,
		       merchant_request_id        = $3,
		       checkout_request_id        = $4,
		       response_code              = $5,
		       response_description       = $6,
		       status                     = $7::stk_request_status,
		       raw_initiate_response      = $8
		 WHERE id = $1
	`, in.ID, nullIfEmpty(in.OriginatorConversationID), nullIfEmpty(in.MerchantRequestID),
		nullIfEmpty(in.CheckoutRequestID), nullIfEmpty(in.ResponseCode), nullIfEmpty(in.ResponseDescription),
		in.Status, in.RawInitiateResponse)
	return err
}

type ApplyCallbackInput struct {
	CheckoutRequestID  string
	ResultCode         string
	ResultDesc         string
	MpesaReceiptNumber string
	Status             string // 'completed' | 'failed' | 'cancelled'
	RawCallback        json.RawMessage
	InboundEventID     *uuid.UUID
}

func (s *STKRequestStore) ApplyCallbackTx(ctx context.Context, tx pgx.Tx, in ApplyCallbackInput) (*STKRequest, error) {
	var r STKRequest
	err := tx.QueryRow(ctx, `
		UPDATE mpesa_stk_requests
		   SET result_code         = $2,
		       result_desc         = $3,
		       mpesa_receipt_number = NULLIF($4, ''),
		       status              = $5::stk_request_status,
		       raw_callback        = $6,
		       inbound_event_id    = $7,
		       completed_at        = now()
		 WHERE checkout_request_id = $1
		 RETURNING id, tenant_id, paybill_id, msisdn, amount,
		           account_reference, transaction_desc,
		           source_module, source_ref,
		           originator_conversation_id, merchant_request_id,
		           checkout_request_id, status,
		           response_code, response_description,
		           result_code, result_desc, mpesa_receipt_number,
		           initiated_at, completed_at
	`,
		in.CheckoutRequestID, in.ResultCode, in.ResultDesc,
		in.MpesaReceiptNumber, in.Status, in.RawCallback, in.InboundEventID,
	).Scan(
		&r.ID, &r.TenantID, &r.PaybillID, &r.MSISDN, &r.Amount,
		&r.AccountReference, &r.TransactionDesc,
		&r.SourceModule, &r.SourceRef,
		&r.OriginatorConversationID, &r.MerchantRequestID,
		&r.CheckoutRequestID, &r.Status,
		&r.ResponseCode, &r.ResponseDescription,
		&r.ResultCode, &r.ResultDesc, &r.MpesaReceiptNumber,
		&r.InitiatedAt, &r.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// ByCheckoutTx — used by the savings-side poller (standing-order
// processor) to learn the outcome of a previously initiated STK push.
func (s *STKRequestStore) ByCheckoutTx(ctx context.Context, tx pgx.Tx, checkoutID string) (*STKRequest, error) {
	var r STKRequest
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, paybill_id, msisdn, amount,
		       account_reference, transaction_desc,
		       source_module, source_ref,
		       originator_conversation_id, merchant_request_id,
		       checkout_request_id, status,
		       response_code, response_description,
		       result_code, result_desc, mpesa_receipt_number,
		       initiated_at, completed_at
		  FROM mpesa_stk_requests
		 WHERE checkout_request_id = $1
	`, checkoutID).Scan(
		&r.ID, &r.TenantID, &r.PaybillID, &r.MSISDN, &r.Amount,
		&r.AccountReference, &r.TransactionDesc,
		&r.SourceModule, &r.SourceRef,
		&r.OriginatorConversationID, &r.MerchantRequestID,
		&r.CheckoutRequestID, &r.Status,
		&r.ResponseCode, &r.ResponseDescription,
		&r.ResultCode, &r.ResultDesc, &r.MpesaReceiptNumber,
		&r.InitiatedAt, &r.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}
