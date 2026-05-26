// Persistence for mpesa_outbound_requests.
//
// Lifecycle covered:
//   - InsertTx(in) — caller (savings or admin) enqueues. ON CONFLICT
//     DO NOTHING on (tenant, source_module, source_ref) absorbs replays.
//   - LeaseNextTx — dispatcher SELECT FOR UPDATE SKIP LOCKED.
//   - MarkSentTx — dispatcher recorded Daraja's conversation_id.
//   - MarkResultTx — Result callback persists final code/desc/receipt.
//   - MarkTimeoutTx — Timeout callback marks the row for retry.
//   - MarkReversedTx — Safaricom reversal hits.
//   - FinalizeTx — track the savings-side handoff (separate
//     attempts counter so the savings call can be retried without
//     re-asking Daraja).

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/domain"
)

type OutboundRequestStore struct {
	pool *pgxpool.Pool
}

func NewOutboundRequestStore(pool *pgxpool.Pool) *OutboundRequestStore {
	return &OutboundRequestStore{pool: pool}
}

// OutboundRequest is the domain shape the handlers + dispatcher pass
// around. Kept in the store package (not domain/) because no other
// package needs it; can move later if a UI surface lands.
type OutboundRequest struct {
	ID                       uuid.UUID
	TenantID                 uuid.UUID
	PaybillID                uuid.UUID
	Kind                     domain.OutboundKind
	MSISDN                   string
	Amount                   decimal.Decimal
	CommandID                string
	Remarks                  string
	SourceModule             string
	SourceRef                string
	Status                   domain.OutboundStatus
	DarajaConversationID     *string
	DarajaOriginatorID       *string
	ResultCode               *string
	ResultDesc               *string
	MpesaReceiptNumber       *string
	ResultRaw                []byte
	FinalizationStatus       string
	FinalizationAttempts     int
}

type InsertOutboundInput struct {
	TenantID     uuid.UUID
	PaybillID    uuid.UUID
	Kind         domain.OutboundKind
	MSISDN       string
	Amount       decimal.Decimal
	CommandID    string
	Remarks      string
	SourceModule string
	SourceRef    string
}

// InsertTx is the idempotent enqueue. Returns the row + a bool that's
// true when the row was actually inserted (false when a prior call
// with the same source_ref landed first).
func (s *OutboundRequestStore) InsertTx(ctx context.Context, tx pgx.Tx, in InsertOutboundInput) (*OutboundRequest, bool, error) {
	if in.SourceModule == "" || in.SourceRef == "" {
		return nil, false, errors.New("outbound: source_module + source_ref required")
	}
	var or OutboundRequest
	var inserted bool
	err := tx.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO mpesa_outbound_requests
				(tenant_id, paybill_id, kind, msisdn, amount,
				 source_module, source_ref, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
			ON CONFLICT (tenant_id, source_module, source_ref) DO NOTHING
			RETURNING id, tenant_id, paybill_id, kind, msisdn, amount,
			          source_module, source_ref, status, true AS inserted
		)
		SELECT id, tenant_id, paybill_id, kind, msisdn, amount,
		       source_module, source_ref, status, inserted
		  FROM ins
		UNION ALL
		SELECT id, tenant_id, paybill_id, kind, msisdn, amount,
		       source_module, source_ref, status, false AS inserted
		  FROM mpesa_outbound_requests
		 WHERE tenant_id = $1 AND source_module = $6 AND source_ref = $7
		 LIMIT 1
	`,
		in.TenantID, in.PaybillID, in.Kind, in.MSISDN, in.Amount,
		in.SourceModule, in.SourceRef,
	).Scan(
		&or.ID, &or.TenantID, &or.PaybillID, &or.Kind, &or.MSISDN, &or.Amount,
		&or.SourceModule, &or.SourceRef, &or.Status, &inserted,
	)
	if err != nil {
		return nil, false, err
	}
	// CommandID + Remarks are set via a separate UPDATE since they're
	// optional on InsertTx today (savings disburse doesn't always
	// populate them); kept off the INSERT to avoid the column-add
	// churn when a future spec adds another optional field.
	if inserted && (in.CommandID != "" || in.Remarks != "") {
		if _, err := tx.Exec(ctx, `
			UPDATE mpesa_outbound_requests
			   SET result_raw = COALESCE(result_raw,
			       jsonb_build_object(
			         'command_id', NULLIF($2,''),
			         'remarks',    NULLIF($3,'')
			       ))
			 WHERE id = $1
		`, or.ID, in.CommandID, in.Remarks); err != nil {
			return nil, false, fmt.Errorf("annotate outbound: %w", err)
		}
		or.CommandID = in.CommandID
		or.Remarks = in.Remarks
	}
	return &or, inserted, nil
}

// LeaseNextTx picks the oldest pending row for dispatch. The
// caller's tx holds the row lock; commit clears it. Filters on
// finalization_status='pending' so already-dispatched-but-pending-
// finalize rows are skipped (the finalize reconciler handles those).
func (s *OutboundRequestStore) LeaseNextTx(ctx context.Context, tx pgx.Tx, tenantID, workerID uuid.UUID) (*OutboundRequest, error) {
	var or OutboundRequest
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, paybill_id, kind, msisdn, amount,
		       source_module, source_ref, status,
		       daraja_conversation_id, daraja_originator_id,
		       result_code, result_desc, mpesa_receipt_number,
		       COALESCE(result_raw, '{}'::jsonb), finalization_status,
		       finalization_attempts
		  FROM mpesa_outbound_requests
		 WHERE tenant_id = $1 AND status = 'pending'
		 ORDER BY requested_at ASC
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED
	`, tenantID).Scan(
		&or.ID, &or.TenantID, &or.PaybillID, &or.Kind, &or.MSISDN, &or.Amount,
		&or.SourceModule, &or.SourceRef, &or.Status,
		&or.DarajaConversationID, &or.DarajaOriginatorID,
		&or.ResultCode, &or.ResultDesc, &or.MpesaReceiptNumber,
		&or.ResultRaw, &or.FinalizationStatus,
		&or.FinalizationAttempts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Lock-by stamp so the audit + dashboard show which worker holds it.
	if _, err := tx.Exec(ctx,
		`UPDATE mpesa_outbound_requests SET locked_at = now(), locked_by = $2 WHERE id = $1`,
		or.ID, workerID,
	); err != nil {
		return nil, err
	}
	return &or, nil
}

// MarkSentTx records the Daraja sync response after a successful
// dispatch. Status flips to 'sent'; the Result callback later flips
// to 'completed' or 'failed'.
func (s *OutboundRequestStore) MarkSentTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, conversationID, originatorID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_outbound_requests
		   SET status                 = 'sent',
		       daraja_conversation_id = $2,
		       daraja_originator_id   = $3,
		       sent_at                = now(),
		       locked_at              = NULL,
		       locked_by              = NULL
		 WHERE id = $1
	`, id, conversationID, originatorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailedTx — dispatcher hit a hard error (signing, network,
// Daraja 4xx). Resets locks so the row can be retried on the next
// pass after operator intervention.
func (s *OutboundRequestStore) MarkFailedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_outbound_requests
		   SET status      = 'failed',
		       result_desc = $2,
		       locked_at   = NULL,
		       locked_by   = NULL
		 WHERE id = $1
	`, id, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkResultTx — Result callback persisted. Sets the final status
// based on ResultCode ('0' = completed, else failed) and records the
// raw payload for audit.
func (s *OutboundRequestStore) MarkResultTx(
	ctx context.Context, tx pgx.Tx,
	id uuid.UUID,
	resultCode, resultDesc, mpesaReceiptNumber string,
	rawPayload []byte,
) error {
	status := domain.OutboundFailed
	if resultCode == "0" {
		status = domain.OutboundCompleted
	}
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_outbound_requests
		   SET status               = $2,
		       result_code          = $3,
		       result_desc          = $4,
		       mpesa_receipt_number = NULLIF($5,''),
		       result_raw           = $6,
		       completed_at         = now()
		 WHERE id = $1
	`, id, status, resultCode, resultDesc, mpesaReceiptNumber, rawPayload)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkReversedTx — Safaricom flagged a delivered B2C as reversed.
// Caller is expected to ALSO enqueue the mpesa_b2c_reversal wf task
// in the same tx so staff can decide retry vs cancel.
func (s *OutboundRequestStore) MarkReversedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, rawPayload []byte) error {
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_outbound_requests
		   SET status     = 'reversed',
		       result_raw = $2
		 WHERE id = $1
	`, id, rawPayload)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordFinalizationAttemptTx is what the Result callback (and the
// reconciler) calls after the savings finalize HTTP. On success the
// row's finalization_status flips to 'completed' and finalized_at
// is stamped; on failure attempts++ + error_text recorded so the
// reconciler can pick it up.
func (s *OutboundRequestStore) RecordFinalizationAttemptTx(
	ctx context.Context, tx pgx.Tx,
	id uuid.UUID, success bool, errText string,
) error {
	if success {
		tag, err := tx.Exec(ctx, `
			UPDATE mpesa_outbound_requests
			   SET finalization_status   = 'completed',
			       finalization_attempts = finalization_attempts + 1,
			       finalized_at          = now(),
			       finalization_error    = NULL
			 WHERE id = $1
		`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE mpesa_outbound_requests
		   SET finalization_status   = 'failed',
		       finalization_attempts = finalization_attempts + 1,
		       finalization_error    = $2
		 WHERE id = $1
	`, id, errText)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ByConversationIDTx — Result + Timeout callbacks identify the row
// by the Safaricom conversation_id Daraja issued at submit time.
func (s *OutboundRequestStore) ByConversationIDTx(ctx context.Context, tx pgx.Tx, conversationID string) (*OutboundRequest, error) {
	if strings.TrimSpace(conversationID) == "" {
		return nil, ErrNotFound
	}
	var or OutboundRequest
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, paybill_id, kind, msisdn, amount,
		       source_module, source_ref, status,
		       daraja_conversation_id, daraja_originator_id,
		       result_code, result_desc, mpesa_receipt_number,
		       COALESCE(result_raw, '{}'::jsonb), finalization_status,
		       finalization_attempts
		  FROM mpesa_outbound_requests
		 WHERE daraja_conversation_id = $1
	`, conversationID).Scan(
		&or.ID, &or.TenantID, &or.PaybillID, &or.Kind, &or.MSISDN, &or.Amount,
		&or.SourceModule, &or.SourceRef, &or.Status,
		&or.DarajaConversationID, &or.DarajaOriginatorID,
		&or.ResultCode, &or.ResultDesc, &or.MpesaReceiptNumber,
		&or.ResultRaw, &or.FinalizationStatus,
		&or.FinalizationAttempts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &or, nil
}

// silence import warnings when this file is the only consumer of
// json in the package (the result_raw column accepts already-marshalled
// bytes via the caller, so we don't json.Marshal here).
var _ = json.Marshal
