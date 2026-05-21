// Tenant-side credit endpoints. Tenants can:
//   * see their SMS + email balances and per-channel low-balance
//     threshold (configurable from this UI)
//   * browse the ledger (every credit movement)
//   * submit a top-up request that the platform admin fulfils
//   * list blocked deliveries from the last 24h and retry them
//     once credits are back in the balance

package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type CreditsHandler struct {
	DB      *db.Pool
	Credits *store.CreditStore
	Topups  *store.TopupRequestStore
	Pricing *store.PricingStore
	Notifs  *store.NotificationStore
	Tenants *store.TenantStore
	Logger  *slog.Logger
}

// Overview — single call returns the two balances + pricing + pending
// top-up requests so the dashboard can render in one round-trip.
func (h *CreditsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	type out struct {
		Balances        []domain.CreditBalance `json:"balances"`
		Pricing         []domain.CreditPricing `json:"pricing"`
		PendingTopups   []domain.TopupRequest  `json:"pending_topups"`
	}
	var res out
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		bs, err := h.Credits.AllBalancesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		res.Balances = bs
		ps, err := h.Pricing.ListTx(r.Context(), tx)
		if err != nil {
			return err
		}
		res.Pricing = ps
		items, _, err := h.Topups.ListTx(r.Context(), tx, store.TopupListFilter{Status: "pending", Limit: 20})
		if err != nil {
			return err
		}
		res.PendingTopups = items
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, res)
}

func (h *CreditsHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.LedgerFilter{
		Channel:      q.Get("channel"),
		MovementType: q.Get("movement_type"),
		Limit:        limit,
		Offset:       offset,
	}
	var items []domain.CreditLedgerEntry
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Credits.ListLedgerTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// SetLowBalanceThreshold — tenant-configurable warning level per
// channel. Resets the alerted-at flags so the next dispatch re-evaluates
// against the new threshold.
type setThresholdReq struct {
	Threshold int `json:"threshold"`
}

func (h *CreditsHandler) SetLowBalanceThreshold(w http.ResponseWriter, r *http.Request) {
	channel := domain.Channel(chi.URLParam(r, "channel"))
	if channel != domain.ChannelSMS && channel != domain.ChannelEmail {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel must be sms or email"))
		return
	}
	var in setThresholdReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Credits.SetLowBalanceThresholdTx(r.Context(), tx, channel, in.Threshold)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Top-up requests (tenant submits) ───────────

type createTopupReq struct {
	Channel domain.Channel `json:"channel"`
	Credits int            `json:"credits"`
	Notes   string         `json:"notes,omitempty"`
}

func (h *CreditsHandler) CreateTopupRequest(w http.ResponseWriter, r *http.Request) {
	var in createTopupReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel != domain.ChannelSMS && in.Channel != domain.ChannelEmail {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel must be sms or email"))
		return
	}
	if in.Credits <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("credits must be > 0"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	var out *domain.TopupRequest
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		var by *uuid.UUID
		if userID != uuid.Nil {
			id := userID
			by = &id
		}
		out, err = h.Topups.CreateTx(r.Context(), tx, store.CreateTopupInput{
			Channel: in.Channel, Credits: in.Credits,
			RequestedBy: by, Notes: in.Notes,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *CreditsHandler) ListTopupRequests(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.TopupListFilter{
		Status:  q.Get("status"),
		Channel: q.Get("channel"),
		Limit:   limit,
		Offset:  offset,
	}
	var items []domain.TopupRequest
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Topups.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

func (h *CreditsHandler) CancelTopupRequest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Topups.CancelTx(r.Context(), tx, id)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Blocked deliveries replay ───────────

const blockedReplayWindow = 24 * time.Hour

// ListBlocked returns deliveries blocked due to insufficient credits
// within the last 24h — eligible for replay once credits are restored.
func (h *CreditsHandler) ListBlocked(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	channel := r.URL.Query().Get("channel")
	if channel != "sms" && channel != "email" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel must be sms or email"))
		return
	}
	var ids []uuid.UUID
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		ids, err = h.Notifs.ListBlockedSinceTx(r.Context(), tx, channel, time.Now().Add(-blockedReplayWindow))
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": ids})
}

// RetryBlocked flips a single blocked delivery back to pending so the
// channel worker picks it up on the next tick. The worker re-runs the
// credit check at that point.
func (h *CreditsHandler) RetryBlocked(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Notifs.RequeueBlockedTx(r.Context(), tx, id)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}
