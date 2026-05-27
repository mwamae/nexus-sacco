// Admin HTTP surface for paybills + credentials + the sandbox auth
// round-trip used by the Settings UI to confirm a paybill is wired
// correctly.
//
// Plaintext credentials enter the service ONCE — in the body of
// /credentials. They are encrypted before the DB write and never
// echoed back, logged, or returned in error messages. Logs use
// structured fields and explicitly omit the body of the credential
// request.

package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/daraja"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/httpx"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
)

// DarajaAuthenticator narrows the Daraja Client surface to the one
// method these handlers use. Splitting it out lets paybill_test.go
// inject a deterministic mock without standing up an httptest server
// for every case.
type DarajaAuthenticator interface {
	Authenticate(ctx context.Context, consumerKey, consumerSecret string) (daraja.Token, error)
}

type PaybillHandler struct {
	DB          *db.Pool
	Paybills    *store.PaybillStore
	Credentials *store.CredentialStore
	Sealer      *crypto.Sealer
	Daraja      DarajaAuthenticator
	Logger      *slog.Logger
}

// ─────────── POST /v1/mpesa/paybills ───────────

type createPaybillReq struct {
	Label       string   `json:"label"`
	Shortcode   string   `json:"shortcode"`
	Purpose     string   `json:"purpose"`
	Scope       []string `json:"scope"`
	Environment string   `json:"environment"`
}

func (h *PaybillHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var req createPaybillReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	purpose, err := parsePurpose(req.Purpose)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	env, err := parseEnvironment(req.Environment)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Label == "" || req.Shortcode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("label and shortcode are required"))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	var out *domain.Paybill
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		p, err := h.Paybills.CreateTx(r.Context(), tx, store.CreatePaybillInput{
			TenantID:    tenantID,
			Label:       req.Label,
			Shortcode:   req.Shortcode,
			Purpose:     purpose,
			Scope:       req.Scope,
			Environment: env,
			CreatedBy:   nonZeroUUID(actor),
		})
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── GET /v1/mpesa/paybills ───────────

// List returns every paybill registered for the current tenant.
// Permission gate (tenant:settings:view) is applied at the router.
// The response intentionally includes the webhook_token: the Settings
// UI's "copy Daraja URLs" panel needs it to compose the per-paybill
// callback URLs the operator pastes into the Safaricom portal. The
// same tenant scope that lets a user view this list lets them rotate
// the token, so visibility is gated consistently.
func (h *PaybillHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var out []domain.Paybill
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		list, err := h.Paybills.ListByTenantTx(r.Context(), tx)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.Paybill{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/mpesa/paybills/{id}/credentials ───────────

type putCredentialReq struct {
	Kind      string `json:"kind"`
	Plaintext string `json:"plaintext"`
}

func (h *PaybillHandler) PutCredential(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	paybillID, err := uuidFromParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var req putCredentialReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	kind, err := parseCredentialKind(req.Kind)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Plaintext == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("plaintext is required"))
		return
	}
	ciphertext, err := h.Sealer.Encrypt([]byte(req.Plaintext))
	if err != nil {
		// Don't surface the underlying crypto error — operators don't
		// need it and including it makes accidental logging risky.
		h.Logger.Error("envelope encrypt failed", "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	// Drop the plaintext from memory as soon as it's been sealed. Go
	// can't actually clear an immutable string but zeroing the bytes
	// the cipher held is the most we can do at this layer.
	req.Plaintext = ""

	actor, _ := middleware.UserIDFrom(r)
	var meta *domain.CredentialMetadata
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Confirm the paybill exists in this tenant before writing
		// the credential (RLS would block a cross-tenant id but we'd
		// rather return 404 than a confused FK violation).
		if _, err := h.Paybills.ByIDTx(r.Context(), tx, paybillID); err != nil {
			return err
		}
		m, err := h.Credentials.PutTx(r.Context(), tx, store.PutCredentialInput{
			TenantID:   tenantID,
			PaybillID:  paybillID,
			Kind:       kind,
			KeyID:      h.Sealer.ActiveID,
			Ciphertext: ciphertext,
			CreatedBy:  nonZeroUUID(actor),
		})
		if err != nil {
			return err
		}
		meta = m
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("paybill not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.Logger.Info("mpesa credential stored",
		"paybill_id", paybillID, "kind", string(kind), "key_id", h.Sealer.ActiveID)
	httpx.Created(w, meta)
}

// ─────────── GET /v1/mpesa/paybills/{id}/test-auth ───────────
//
// Pulls the consumer key + secret out of the store, decrypts them,
// asks Daraja for an OAuth token, and reports back {ok, expires_at}
// (or {ok:false, error}). Used by the Settings UI to confirm a
// paybill's credentials are correct before committing the operator
// to a real cash-flow webhook.

type testAuthResp struct {
	OK        bool      `json:"ok"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (h *PaybillHandler) TestAuth(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	paybillID, err := uuidFromParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var key, secret []byte
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		if _, err := h.Paybills.ByIDTx(r.Context(), tx, paybillID); err != nil {
			return err
		}
		_, ck, err := h.Credentials.ReadTx(r.Context(), tx, paybillID, domain.CredConsumerKey)
		if err != nil {
			return fmt.Errorf("consumer_key: %w", err)
		}
		_, cs, err := h.Credentials.ReadTx(r.Context(), tx, paybillID, domain.CredConsumerSecret)
		if err != nil {
			return fmt.Errorf("consumer_secret: %w", err)
		}
		ckPlain, err := h.Sealer.Decrypt(ck)
		if err != nil {
			return fmt.Errorf("decrypt consumer_key: %w", err)
		}
		csPlain, err := h.Sealer.Decrypt(cs)
		if err != nil {
			return fmt.Errorf("decrypt consumer_secret: %w", err)
		}
		key, secret = ckPlain, csPlain
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Could be paybill missing or credentials missing —
			// surface as a soft "ok=false" so the UI can present
			// "Configure credentials first" rather than 404.
			httpx.OK(w, testAuthResp{OK: false, Error: "missing credentials: " + err.Error()})
			return
		}
		httpx.OK(w, testAuthResp{OK: false, Error: err.Error()})
		return
	}

	tok, err := h.Daraja.Authenticate(r.Context(), string(key), string(secret))
	// Zero out the plaintext slices now that Daraja's signed the
	// header — no point holding them in memory once the round-trip
	// is complete.
	for i := range key {
		key[i] = 0
	}
	for i := range secret {
		secret[i] = 0
	}
	if err != nil {
		httpx.OK(w, testAuthResp{OK: false, Error: err.Error()})
		return
	}
	httpx.OK(w, testAuthResp{OK: true, ExpiresAt: tok.ExpiresAt})
}

// ─────────── helpers ───────────

func parsePurpose(s string) (domain.PaybillPurpose, error) {
	switch domain.PaybillPurpose(s) {
	case domain.PurposeCollection, domain.PurposeDisbursement, domain.PurposeBoth:
		return domain.PaybillPurpose(s), nil
	}
	return "", httpx.ErrBadRequest("purpose must be collection, disbursement, or both")
}

func parseEnvironment(s string) (domain.Environment, error) {
	switch domain.Environment(s) {
	case domain.EnvSandbox, domain.EnvProduction:
		return domain.Environment(s), nil
	}
	return "", httpx.ErrBadRequest("environment must be sandbox or production")
}

func parseCredentialKind(s string) (domain.CredentialKind, error) {
	switch domain.CredentialKind(s) {
	case domain.CredConsumerKey, domain.CredConsumerSecret, domain.CredPasskey,
		domain.CredInitiatorName, domain.CredInitiatorPassword:
		return domain.CredentialKind(s), nil
	}
	return "", httpx.ErrBadRequest("kind must be one of consumer_key, consumer_secret, passkey, initiator_name, initiator_password")
}

func uuidFromParam(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.ErrBadRequest("invalid " + name)
	}
	return id, nil
}

func nonZeroUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
