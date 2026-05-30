// DSID Phase 2.2 — joint deposit accounts.
//
// Three tables touched:
//   deposit_account_joint_owners
//   withdrawal_authorisations
//   joint_withdrawal_authorisations
//
// The Withdraw handler peeks at deposit_accounts.is_joint; when true,
// it creates the parent + per-owner rows here instead of executing.

package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type JointAccountStore struct {
	pool *pgxpool.Pool
}

func NewJointAccountStore(pool *pgxpool.Pool) *JointAccountStore {
	return &JointAccountStore{pool: pool}
}

// ─────────── Joint owners ───────────

type JointOwner struct {
	ID             uuid.UUID  `json:"id"`
	AccountID      uuid.UUID  `json:"account_id"`
	CounterpartyID uuid.UUID  `json:"counterparty_id"`
	SigningRole    string     `json:"signing_role"`
	AddedAt        time.Time  `json:"added_at"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
}

type AddOwnerInput struct {
	TenantID       uuid.UUID
	AccountID      uuid.UUID
	CounterpartyID uuid.UUID
	SigningRole    string
	AddedBy        uuid.UUID
}

func (s *JointAccountStore) AddOwnerTx(ctx context.Context, tx pgx.Tx, in AddOwnerInput) (*JointOwner, error) {
	if in.SigningRole == "" {
		in.SigningRole = "co_owner"
	}
	var o JointOwner
	err := tx.QueryRow(ctx, `
		INSERT INTO deposit_account_joint_owners
		    (tenant_id, account_id, counterparty_id, signing_role, added_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, account_id, counterparty_id, signing_role, added_at, removed_at
	`, in.TenantID, in.AccountID, in.CounterpartyID, in.SigningRole, in.AddedBy).
		Scan(&o.ID, &o.AccountID, &o.CounterpartyID, &o.SigningRole, &o.AddedAt, &o.RemovedAt)
	return &o, err
}

func (s *JointAccountStore) RemoveOwnerTx(ctx context.Context, tx pgx.Tx, accountID, counterpartyID, removedBy uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE deposit_account_joint_owners
		   SET removed_at = now(), removed_by = $3
		 WHERE account_id = $1 AND counterparty_id = $2 AND removed_at IS NULL
	`, accountID, counterpartyID, removedBy)
	return err
}

func (s *JointAccountStore) ListOwnersTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) ([]JointOwner, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, account_id, counterparty_id, signing_role, added_at, removed_at
		  FROM deposit_account_joint_owners
		 WHERE account_id = $1
		 ORDER BY removed_at NULLS FIRST, added_at
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JointOwner{}
	for rows.Next() {
		var o JointOwner
		if err := rows.Scan(&o.ID, &o.AccountID, &o.CounterpartyID, &o.SigningRole, &o.AddedAt, &o.RemovedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *JointAccountStore) SetIsJointTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, isJoint bool, requiredSigners int) error {
	_, err := tx.Exec(ctx, `
		UPDATE deposit_accounts
		   SET is_joint = $2,
		       required_signers = $3,
		       updated_at = now()
		 WHERE id = $1
	`, accountID, isJoint, requiredSigners)
	return err
}

// ─────────── Pending withdrawal + signer rows ───────────

type PendingWithdrawal struct {
	ID                        uuid.UUID       `json:"id"`
	TenantID                  uuid.UUID       `json:"tenant_id"`
	AccountID                 uuid.UUID       `json:"account_id"`
	InitiatedByCounterpartyID uuid.UUID       `json:"initiated_by_counterparty_id"`
	InitiatedByUserID         *uuid.UUID      `json:"initiated_by_user_id,omitempty"`
	Amount                    decimal.Decimal `json:"amount"`
	Channel                   string          `json:"channel"`
	Narration                 *string         `json:"narration,omitempty"`
	RequiredSigners           int             `json:"required_signers"`
	Status                    string          `json:"status"`
	ExpiresAt                 time.Time       `json:"expires_at"`
	PostedTxnID               *uuid.UUID      `json:"posted_txn_id,omitempty"`
	PostedAt                  *time.Time      `json:"posted_at,omitempty"`
	CancellationReason        *string         `json:"cancellation_reason,omitempty"`
	CreatedAt                 time.Time       `json:"created_at"`
}

type CreatePendingWithdrawalInput struct {
	TenantID                  uuid.UUID
	AccountID                 uuid.UUID
	InitiatedByCounterpartyID uuid.UUID
	InitiatedByUserID         uuid.UUID
	Amount                    decimal.Decimal
	Channel                   string
	Narration                 string
	RequiredSigners           int
	ExpiresAt                 time.Time
}

func (s *JointAccountStore) CreatePendingWithdrawalTx(ctx context.Context, tx pgx.Tx, in CreatePendingWithdrawalInput) (*PendingWithdrawal, error) {
	var p PendingWithdrawal
	err := tx.QueryRow(ctx, `
		INSERT INTO withdrawal_authorisations
		    (tenant_id, account_id, initiated_by_counterparty_id, initiated_by_user_id,
		     amount, channel, narration, required_signers, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::deposit_channel, NULLIF($7, ''), $8, $9)
		RETURNING id, tenant_id, account_id, initiated_by_counterparty_id, initiated_by_user_id,
		          amount, channel::text, narration, required_signers, status, expires_at,
		          posted_txn_id, posted_at, cancellation_reason, created_at
	`,
		in.TenantID, in.AccountID, in.InitiatedByCounterpartyID, in.InitiatedByUserID,
		in.Amount, in.Channel, in.Narration, in.RequiredSigners, in.ExpiresAt,
	).Scan(
		&p.ID, &p.TenantID, &p.AccountID, &p.InitiatedByCounterpartyID, &p.InitiatedByUserID,
		&p.Amount, &p.Channel, &p.Narration, &p.RequiredSigners, &p.Status, &p.ExpiresAt,
		&p.PostedTxnID, &p.PostedAt, &p.CancellationReason, &p.CreatedAt,
	)
	return &p, err
}

// LockPendingTx — SELECT … FOR UPDATE on the parent row before quorum
// check. Prevents the simultaneous-approval race.
func (s *JointAccountStore) LockPendingTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*PendingWithdrawal, error) {
	var p PendingWithdrawal
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, account_id, initiated_by_counterparty_id, initiated_by_user_id,
		       amount, channel::text, narration, required_signers, status, expires_at,
		       posted_txn_id, posted_at, cancellation_reason, created_at
		  FROM withdrawal_authorisations
		 WHERE id = $1
		 FOR UPDATE
	`, id).Scan(
		&p.ID, &p.TenantID, &p.AccountID, &p.InitiatedByCounterpartyID, &p.InitiatedByUserID,
		&p.Amount, &p.Channel, &p.Narration, &p.RequiredSigners, &p.Status, &p.ExpiresAt,
		&p.PostedTxnID, &p.PostedAt, &p.CancellationReason, &p.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *JointAccountStore) MarkPendingStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, postedTxnID *uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE withdrawal_authorisations
		   SET status              = $2,
		       posted_txn_id       = $3,
		       posted_at           = CASE WHEN $2 IN ('posted','approved') THEN now() ELSE posted_at END,
		       cancellation_reason = NULLIF($4, '')
		 WHERE id = $1
	`, id, status, postedTxnID, reason)
	return err
}

// ─────────── Joint signer rows ───────────

type JointSigner struct {
	ID                   uuid.UUID  `json:"id"`
	WithdrawalRequestID  uuid.UUID  `json:"withdrawal_request_id"`
	SignerCounterpartyID uuid.UUID  `json:"signer_counterparty_id"`
	SignerMSISDN         *string    `json:"signer_msisdn,omitempty"`
	SignerToken          string     `json:"-"` // never surface the consent token to admin UIs
	SignerStatus         string     `json:"signer_status"`
	RespondedAt          *time.Time `json:"responded_at,omitempty"`
	SignatureMethod      *string    `json:"signature_method,omitempty"`
}

type AddSignerInput struct {
	TenantID              uuid.UUID
	WithdrawalRequestID   uuid.UUID
	SignerCounterpartyID  uuid.UUID
	SignerMSISDN          string
}

func (s *JointAccountStore) AddSignerTx(ctx context.Context, tx pgx.Tx, in AddSignerInput) (*JointSigner, error) {
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	var js JointSigner
	err = tx.QueryRow(ctx, `
		INSERT INTO joint_withdrawal_authorisations
		    (tenant_id, withdrawal_request_id, signer_counterparty_id, signer_msisdn, signer_token)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		RETURNING id, withdrawal_request_id, signer_counterparty_id,
		          signer_msisdn, signer_token, signer_status, responded_at, signature_method
	`,
		in.TenantID, in.WithdrawalRequestID, in.SignerCounterpartyID,
		in.SignerMSISDN, token,
	).Scan(
		&js.ID, &js.WithdrawalRequestID, &js.SignerCounterpartyID,
		&js.SignerMSISDN, &js.SignerToken, &js.SignerStatus, &js.RespondedAt, &js.SignatureMethod,
	)
	return &js, err
}

func (s *JointAccountStore) ListSignersTx(ctx context.Context, tx pgx.Tx, withdrawalRequestID uuid.UUID) ([]JointSigner, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, withdrawal_request_id, signer_counterparty_id,
		       signer_msisdn, signer_token, signer_status, responded_at, signature_method
		  FROM joint_withdrawal_authorisations
		 WHERE withdrawal_request_id = $1
		 ORDER BY id
	`, withdrawalRequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JointSigner{}
	for rows.Next() {
		var js JointSigner
		if err := rows.Scan(
			&js.ID, &js.WithdrawalRequestID, &js.SignerCounterpartyID,
			&js.SignerMSISDN, &js.SignerToken, &js.SignerStatus, &js.RespondedAt, &js.SignatureMethod,
		); err != nil {
			return nil, err
		}
		out = append(out, js)
	}
	return out, rows.Err()
}

func (s *JointAccountStore) SignerByTokenTx(ctx context.Context, tx pgx.Tx, token string) (*JointSigner, error) {
	// The public consent endpoint runs without a tenant context. RLS
	// would block this read; the migration grants a session-setting
	// escape hatch (app.public_token_lookup) that the handler sets
	// before this query. The setting is scoped to the tx.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.public_token_lookup', $1, true)`, token); err != nil {
		return nil, err
	}
	var js JointSigner
	err := tx.QueryRow(ctx, `
		SELECT id, withdrawal_request_id, signer_counterparty_id,
		       signer_msisdn, signer_token, signer_status, responded_at, signature_method
		  FROM joint_withdrawal_authorisations
		 WHERE signer_token = $1
	`, token).Scan(
		&js.ID, &js.WithdrawalRequestID, &js.SignerCounterpartyID,
		&js.SignerMSISDN, &js.SignerToken, &js.SignerStatus, &js.RespondedAt, &js.SignatureMethod,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &js, err
}

func (s *JointAccountStore) MarkSignerStatusTx(ctx context.Context, tx pgx.Tx, signerID uuid.UUID, status, method string) error {
	if status != "approved" && status != "rejected" {
		return fmt.Errorf("invalid signer status: %s", status)
	}
	_, err := tx.Exec(ctx, `
		UPDATE joint_withdrawal_authorisations
		   SET signer_status = $2,
		       responded_at = now(),
		       signature_method = NULLIF($3, '')
		 WHERE id = $1 AND signer_status = 'pending'
	`, signerID, status, method)
	return err
}

// CountSignerStatusesTx — used after a consent response to decide if
// quorum is reached or the request is cancelled.
func (s *JointAccountStore) CountSignerStatusesTx(ctx context.Context, tx pgx.Tx, withdrawalRequestID uuid.UUID) (approved, rejected, pending int, err error) {
	err = tx.QueryRow(ctx, `
		SELECT
		    COUNT(*) FILTER (WHERE signer_status = 'approved'),
		    COUNT(*) FILTER (WHERE signer_status = 'rejected'),
		    COUNT(*) FILTER (WHERE signer_status = 'pending')
		  FROM joint_withdrawal_authorisations
		 WHERE withdrawal_request_id = $1
	`, withdrawalRequestID).Scan(&approved, &rejected, &pending)
	return
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
