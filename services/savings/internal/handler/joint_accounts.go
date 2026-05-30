// DSID Phase 2.2 — joint account admin + consent handlers.
//
// Officer-facing endpoints (JWT, members:edit):
//   GET    /v1/deposit-accounts/{account_id}/joint-owners
//   POST   /v1/deposit-accounts/{account_id}/joint-owners
//   DELETE /v1/deposit-accounts/{account_id}/joint-owners/{counterparty_id}
//   PUT    /v1/deposit-accounts/{account_id}/joint-config
//        body: {is_joint, required_signers}
//   GET    /v1/deposit-accounts/{account_id}/pending-withdrawals
//        list of withdrawal_authorisations + per-signer status
//
// Public consent (token-driven, no auth):
//   GET    /p/joint-withdrawal/{token}
//   POST   /p/joint-withdrawal/{token}/respond   {decision: 'approved'|'rejected'}

package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type JointAccountsHandler struct {
	DB              *db.Pool
	Joint           *store.JointAccountStore
	Notifier        *notifier.Client
	DefaultExpiryHr int
}

// ─────────── GET /v1/deposit-accounts/{account_id}/joint-owners ───────────

func (h *JointAccountsHandler) ListOwners(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var owners []store.JointOwner
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.Joint.ListOwnersTx(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		owners = l
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": owners})
}

// ─────────── POST /v1/deposit-accounts/{account_id}/joint-owners ───────────

type addOwnerReq struct {
	CounterpartyID uuid.UUID `json:"counterparty_id"`
	SigningRole    string    `json:"signing_role,omitempty"`
}

func (h *JointAccountsHandler) AddOwner(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in addOwnerReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CounterpartyID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var out *store.JointOwner
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		o, err := h.Joint.AddOwnerTx(r.Context(), tx, store.AddOwnerInput{
			TenantID:       tid,
			AccountID:      accountID,
			CounterpartyID: in.CounterpartyID,
			SigningRole:    in.SigningRole,
			AddedBy:        uid,
		})
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── DELETE /v1/deposit-accounts/{account_id}/joint-owners/{counterparty_id} ───────────

func (h *JointAccountsHandler) RemoveOwner(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	cpID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Joint.RemoveOwnerTx(r.Context(), tx, accountID, cpID, uid)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── PUT /v1/deposit-accounts/{account_id}/joint-config ───────────

type jointConfigReq struct {
	IsJoint         bool `json:"is_joint"`
	RequiredSigners int  `json:"required_signers"`
}

func (h *JointAccountsHandler) PutConfig(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in jointConfigReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.RequiredSigners < 1 {
		in.RequiredSigners = 1
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Joint.SetIsJointTx(r.Context(), tx, accountID, in.IsJoint, in.RequiredSigners)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"updated": true})
}

// ─────────── GET /v1/deposit-accounts/{account_id}/pending-withdrawals ───────────

type pendingWithdrawalDTO struct {
	*store.PendingWithdrawal
	Signers []store.JointSigner `json:"signers"`
}

func (h *JointAccountsHandler) ListPendingWithdrawals(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	out := []pendingWithdrawalDTO{}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id FROM withdrawal_authorisations
			 WHERE account_id = $1 AND status = 'pending_joint_authorisation'
			 ORDER BY created_at DESC LIMIT 50
		`, accountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		for _, id := range ids {
			pw, perr := h.Joint.LockPendingTx(r.Context(), tx, id)
			if perr != nil {
				continue
			}
			signers, serr := h.Joint.ListSignersTx(r.Context(), tx, id)
			if serr != nil {
				return serr
			}
			out = append(out, pendingWithdrawalDTO{PendingWithdrawal: pw, Signers: signers})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out})
}

// ─────────── Public consent: GET /p/joint-withdrawal/{token} ───────────

type publicConsentView struct {
	Token       string          `json:"token"`
	Status      string          `json:"status"`
	Amount      string          `json:"amount"`
	AccountNo   string          `json:"account_no"`
	InitiatedBy string          `json:"initiated_by_name"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Expired     bool            `json:"expired"`
}

func (h *JointAccountsHandler) PublicGet(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing token"))
		return
	}
	var view publicConsentView
	view.Token = token
	err := h.DB.WithTenantTx(r.Context(), uuid.Nil, func(tx pgx.Tx) error {
		signer, err := h.Joint.SignerByTokenTx(r.Context(), tx, token)
		if err != nil {
			return err
		}
		pending, err := h.Joint.LockPendingTx(r.Context(), tx, signer.WithdrawalRequestID)
		if err != nil {
			return err
		}
		view.Status = signer.SignerStatus
		view.Amount = pending.Amount.StringFixed(2)
		view.ExpiresAt = pending.ExpiresAt
		view.Expired = time.Now().After(pending.ExpiresAt)
		// Fetch account_no for context.
		_ = tx.QueryRow(r.Context(),
			`SELECT account_no FROM deposit_accounts WHERE id = $1`, pending.AccountID,
		).Scan(&view.AccountNo)
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, view)
}

// ─────────── Public consent: POST /p/joint-withdrawal/{token}/respond ───────────

type jointRespondReq struct {
	Decision string `json:"decision"` // 'approved' | 'rejected'
}

func (h *JointAccountsHandler) PublicRespond(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing token"))
		return
	}
	var in jointRespondReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Decision != "approved" && in.Decision != "rejected" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decision must be 'approved' or 'rejected'"))
		return
	}
	var finalStatus string
	err := h.DB.WithTenantTx(r.Context(), uuid.Nil, func(tx pgx.Tx) error {
		signer, err := h.Joint.SignerByTokenTx(r.Context(), tx, token)
		if err != nil {
			return err
		}
		if signer.SignerStatus != "pending" {
			return httpx.ErrConflict("already responded")
		}
		// Lock parent.
		pending, err := h.Joint.LockPendingTx(r.Context(), tx, signer.WithdrawalRequestID)
		if err != nil {
			return err
		}
		if pending.Status != "pending_joint_authorisation" {
			return httpx.ErrConflict("withdrawal is no longer pending (current: " + pending.Status + ")")
		}
		if time.Now().After(pending.ExpiresAt) {
			_ = h.Joint.MarkPendingStatusTx(r.Context(), tx, pending.ID, "expired", nil, "consent token expired")
			return httpx.ErrConflict("authorisation window has expired")
		}
		// Record this signer's response.
		if err := h.Joint.MarkSignerStatusTx(r.Context(), tx, signer.ID, in.Decision, "sms_otp"); err != nil {
			return err
		}
		approved, rejected, _, err := h.Joint.CountSignerStatusesTx(r.Context(), tx, pending.ID)
		if err != nil {
			return err
		}
		if rejected > 0 {
			if err := h.Joint.MarkPendingStatusTx(r.Context(), tx, pending.ID, "rejected", nil, "rejected by signer"); err != nil {
				return err
			}
			finalStatus = "rejected"
			return nil
		}
		if approved >= pending.RequiredSigners {
			// Quorum met. We mark as 'approved' so a downstream
			// "execute pending withdrawals" path (separate cmd worker
			// or admin endpoint) can post the ledger entries. Posting
			// from inside this public-tx without a tenant context is
			// not safe — defer to the officer-side actuator.
			if err := h.Joint.MarkPendingStatusTx(r.Context(), tx, pending.ID, "approved", nil, ""); err != nil {
				return err
			}
			finalStatus = "approved"
			return nil
		}
		finalStatus = "still_pending"
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"status": finalStatus})
}

// ─────────── JointBridge implementation (deposit.go consumes) ───────────

// ListOwnersOfAccountTx satisfies the JointBridge interface declared
// on DepositHandler.
func (h *JointAccountsHandler) ListOwnersOfAccountTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) ([]store.JointOwner, error) {
	return h.Joint.ListOwnersTx(ctx, tx, accountID)
}

// CreateJointPendingTx satisfies the JointBridge interface declared
// on DepositHandler. Delegates to CreateJointWithdrawalTx.
func (h *JointAccountsHandler) CreateJointPendingTx(ctx context.Context, tx pgx.Tx, in JointPendingInput) (uuid.UUID, error) {
	return h.CreateJointWithdrawalTx(ctx, tx, CreateJointPendingInput{
		TenantID:                  in.TenantID,
		AccountID:                 in.AccountID,
		InitiatedByCounterpartyID: in.InitiatedByCounterpartyID,
		InitiatedByUserID:         in.InitiatedByUserID,
		Amount:                    in.Amount,
		Channel:                   in.Channel,
		Narration:                 in.Narration,
		RequiredSigners:           in.RequiredSigners,
		Owners:                    in.Owners,
	})
}

// CreateJointPendingInput is the local shape (mirrors deposit.go's
// JointPendingInput but lives in this package). Keep both forms so the
// bridge interface in deposit.go can be defined without importing
// this handler's package.
type CreateJointPendingInput struct {
	TenantID                  uuid.UUID
	AccountID                 uuid.UUID
	InitiatedByCounterpartyID uuid.UUID
	InitiatedByUserID         uuid.UUID
	Amount                    decimal.Decimal
	Channel                   string
	Narration                 string
	RequiredSigners           int
	Owners                    []store.JointOwner
}

// CreateJointWithdrawalTx — spins up the pending request + N signer
// rows + SMS each owner. Returns the parent row id.
func (h *JointAccountsHandler) CreateJointWithdrawalTx(ctx context.Context, tx pgx.Tx, in CreateJointPendingInput) (uuid.UUID, error) {
	expiry := h.DefaultExpiryHr
	if expiry <= 0 {
		expiry = 72
	}
	pending, err := h.Joint.CreatePendingWithdrawalTx(ctx, tx, store.CreatePendingWithdrawalInput{
		TenantID:                  in.TenantID,
		AccountID:                 in.AccountID,
		InitiatedByCounterpartyID: in.InitiatedByCounterpartyID,
		InitiatedByUserID:         in.InitiatedByUserID,
		Amount:                    in.Amount,
		Channel:                   in.Channel,
		Narration:                 in.Narration,
		RequiredSigners:           in.RequiredSigners,
		ExpiresAt:                 time.Now().Add(time.Duration(expiry) * time.Hour),
	})
	if err != nil {
		return uuid.Nil, err
	}
	// Insert one signer row per active joint owner (excluding the
	// initiator — they're presumed to be the one starting the
	// withdrawal, so their consent is implicit).
	for _, o := range in.Owners {
		if o.RemovedAt != nil {
			continue
		}
		if o.CounterpartyID == in.InitiatedByCounterpartyID {
			continue
		}
		// Look up MSISDN best-effort.
		var msisdn string
		_ = tx.QueryRow(ctx, `
			SELECT m.msisdn FROM members m
			 JOIN counterparty_directory cd ON cd.member_id = m.id
			 WHERE cd.counterparty_id = $1
		`, o.CounterpartyID).Scan(&msisdn)
		js, err := h.Joint.AddSignerTx(ctx, tx, store.AddSignerInput{
			TenantID:             in.TenantID,
			WithdrawalRequestID:  pending.ID,
			SignerCounterpartyID: o.CounterpartyID,
			SignerMSISDN:         msisdn,
		})
		if err != nil {
			return uuid.Nil, err
		}
		fireJointConsentSMS(ctx, h.Notifier, in.TenantID, o.CounterpartyID, msisdn, in.Amount.StringFixed(2), js.SignerToken, pending.ExpiresAt)
	}
	return pending.ID, nil
}

func fireJointConsentSMS(ctx context.Context, n *notifier.Client, tenantID, counterparty uuid.UUID, msisdn, amount, token string, expiresAt time.Time) {
	if n == nil {
		return
	}
	cp := counterparty
	n.Notify(ctx, notifier.Request{
		TenantID:          tenantID,
		EventCode:         "JOINT_WITHDRAWAL_CONSENT_REQUESTED",
		Channels:          []notifier.Channel{notifier.ChannelSMS},
		RecipientMemberID: &cp,
		RecipientPhone:    nilIfEmptyJ(msisdn),
		Payload: map[string]any{
			"amount":     amount,
			"token":      token,
			"expires_at": expiresAt.UTC().Format(time.RFC3339),
		},
	})
}

func nilIfEmptyJ(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
