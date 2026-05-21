// Platform-admin endpoints for tenant credit management.
// All routes here run with no tenant context (the user is on the
// platform subdomain and carries the platform_admin claim); we set
// `app.tenant_id` per query when needed so RLS still works.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type PlatformCreditsHandler struct {
	DB          *db.Pool
	Credits     *store.CreditStore
	Topups      *store.TopupRequestStore
	Pricing     *store.PricingStore
	Adjustments *store.AdjustmentStore
	Tenants     *store.TenantStore
	Logger      *slog.Logger
}

// ListTenants returns every tenant's two balances + last-topup info,
// plus enough metadata for the platform overview screen. Iterates per
// tenant rather than doing one cross-tenant SELECT because the worker
// DB connection runs under the nexus_app role (RLS enforced) — a
// cross-tenant select would return zero rows without an explicit
// per-tenant `app.tenant_id` context.
func (h *PlatformCreditsHandler) ListTenants(w http.ResponseWriter, r *http.Request) {
	type tenantRow struct {
		TenantID uuid.UUID              `json:"tenant_id"`
		Slug     string                 `json:"slug"`
		Name     string                 `json:"name"`
		Balances []domain.CreditBalance `json:"balances"`
	}
	rows := []tenantRow{}

	// Step 1 — list tenants (RLS-free table).
	type tenantHead struct {
		id        uuid.UUID
		slug, nme string
	}
	var heads []tenantHead
	err := h.DB.WithTenantTx(r.Context(), uuid.Nil, func(tx pgx.Tx) error {
		results, err := tx.Query(r.Context(), `SELECT id, slug, name FROM tenants ORDER BY slug`)
		if err != nil {
			return err
		}
		defer results.Close()
		for results.Next() {
			var th tenantHead
			if err := results.Scan(&th.id, &th.slug, &th.nme); err != nil {
				return err
			}
			heads = append(heads, th)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Step 2 — fetch each tenant's balances under its own RLS context.
	for _, th := range heads {
		var bs []domain.CreditBalance
		err := h.DB.WithTenantTx(r.Context(), th.id, func(tx pgx.Tx) error {
			var lerr error
			bs, lerr = h.Credits.AllBalancesTx(r.Context(), tx)
			return lerr
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		rows = append(rows, tenantRow{
			TenantID: th.id, Slug: th.slug, Name: th.nme, Balances: bs,
		})
	}
	httpx.OK(w, map[string]any{"items": rows})
}

func (h *PlatformCreditsHandler) TenantDetail(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	type out struct {
		Balances []domain.CreditBalance `json:"balances"`
		Pricing  []domain.CreditPricing `json:"pricing"`
	}
	var res out
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
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
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, res)
}

// ─────────── Top-up (immediate) ───────────

type topupReq struct {
	Channel   domain.Channel `json:"channel"`
	Credits   int            `json:"credits"`
	Reference string         `json:"reference,omitempty"`
	Notes     string         `json:"notes,omitempty"`
}

func (h *PlatformCreditsHandler) Topup(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	var in topupReq
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
	actor, _ := middleware.UserIDFrom(r)
	var newBalance int
	var ledgerID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		newBalance, ledgerID, err = h.Credits.TopupTx(r.Context(), tx, store.TopupInput{
			Channel: in.Channel, Credits: in.Credits,
			Reference: in.Reference, ActionedBy: actor, Notes: in.Notes,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"channel":     in.Channel,
		"credits":     in.Credits,
		"new_balance": newBalance,
		"ledger_id":   ledgerID,
	})
}

func (h *PlatformCreditsHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
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
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
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

// ─────────── Pricing ───────────

func (h *PlatformCreditsHandler) GetPricing(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	var rows []domain.CreditPricing
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Pricing.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": rows})
}

type updatePricingReq struct {
	Channel        domain.Channel `json:"channel"`
	PricePerCredit string         `json:"price_per_credit"`
	CurrencyCode   string         `json:"currency_code,omitempty"`
}

func (h *PlatformCreditsHandler) UpdatePricing(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	var in updatePricingReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel != domain.ChannelSMS && in.Channel != domain.ChannelEmail {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel must be sms or email"))
		return
	}
	if in.PricePerCredit == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("price_per_credit is required"))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Pricing.UpsertTx(r.Context(), tx, in.Channel, in.PricePerCredit, in.CurrencyCode, actor)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Top-up request fulfillment ───────────

func (h *PlatformCreditsHandler) ListTopupRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantFilter := q.Get("tenant_id")
	status := q.Get("status")
	if status == "" {
		status = "pending"
	}
	type itemOut struct {
		domain.TopupRequest
		TenantSlug string `json:"tenant_slug,omitempty"`
		TenantName string `json:"tenant_name,omitempty"`
	}
	rows := []itemOut{}
	heads, err := h.listTenantHeadsCtx(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	for _, th := range heads {
		if tenantFilter != "" && th.ID.String() != tenantFilter {
			continue
		}
		err := h.DB.WithTenantTx(r.Context(), th.ID, func(tx pgx.Tx) error {
			items, _, err := h.Topups.ListTx(r.Context(), tx, store.TopupListFilter{Status: status, Limit: 200})
			if err != nil {
				return err
			}
			for _, t := range items {
				rows = append(rows, itemOut{
					TopupRequest: t,
					TenantSlug:   th.Slug,
					TenantName:   th.Name,
				})
			}
			return nil
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	httpx.OK(w, map[string]any{"items": rows})
}

type fulfillReq struct {
	Reference string `json:"reference,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

func (h *PlatformCreditsHandler) FulfillTopupRequest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in fulfillReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	tenantID, topup, err := h.findTopupRequestTenant(r.Context(), id)
	if err != nil {
		writeCreditAdminErr(w, r, err, "topup request")
		return
	}
	if topup.Status != domain.TopupStatusPending {
		httpx.WriteErr(w, r, httpx.ErrConflict("topup request is "+string(topup.Status)))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	var newBalance int
	var ledgerID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		newBalance, ledgerID, err = h.Credits.TopupTx(r.Context(), tx, store.TopupInput{
			Channel: topup.Channel, Credits: topup.CreditsRequested,
			Reference: in.Reference, ActionedBy: actor, Notes: in.Notes,
		})
		if err != nil {
			return err
		}
		return h.Topups.MarkFulfilledTx(r.Context(), tx, id, actor, ledgerID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"new_balance": newBalance,
		"ledger_id":   ledgerID,
	})
}

type rejectReq struct {
	Reason string `json:"reason"`
}

func (h *PlatformCreditsHandler) RejectTopupRequest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in rejectReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	tenantID, _, err := h.findTopupRequestTenant(r.Context(), id)
	if err != nil {
		writeCreditAdminErr(w, r, err, "topup request")
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Topups.RejectTx(r.Context(), tx, id, actor, in.Reason)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Adjustments (maker/checker) ───────────

type adjustmentReq struct {
	Channel domain.Channel `json:"channel"`
	Credits int            `json:"credits"`
	Reason  string         `json:"reason"`
}

func (h *PlatformCreditsHandler) RequestAdjustment(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	var in adjustmentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel != domain.ChannelSMS && in.Channel != domain.ChannelEmail {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel must be sms or email"))
		return
	}
	if in.Credits == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("credits must be non-zero"))
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	var out *domain.CreditAdjustment
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Adjustments.CreateTx(r.Context(), tx, store.CreateAdjustmentInput{
			Channel: in.Channel, Credits: in.Credits, Reason: in.Reason, RequestedBy: actor,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *PlatformCreditsHandler) ListAdjustments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantFilter := q.Get("tenant_id")
	status := q.Get("status")
	if status == "" {
		status = "pending_approval"
	}
	type itemOut struct {
		domain.CreditAdjustment
		TenantSlug string `json:"tenant_slug,omitempty"`
		TenantName string `json:"tenant_name,omitempty"`
	}
	rows := []itemOut{}
	// Iterate per tenant — the nexus_app role enforces RLS so an
	// unscoped SELECT returns no rows.
	heads, err := h.listTenantHeadsCtx(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	for _, th := range heads {
		if tenantFilter != "" && th.ID.String() != tenantFilter {
			continue
		}
		err := h.DB.WithTenantTx(r.Context(), th.ID, func(tx pgx.Tx) error {
			items, _, err := h.Adjustments.ListTx(r.Context(), tx, store.AdjustmentListFilter{Status: status, Limit: 200})
			if err != nil {
				return err
			}
			for _, a := range items {
				rows = append(rows, itemOut{
					CreditAdjustment: a,
					TenantSlug:       th.Slug,
					TenantName:       th.Name,
				})
			}
			return nil
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	httpx.OK(w, map[string]any{"items": rows})
}

// tenantHead is a lightweight (id, slug, name) tuple for iterating
// tenants from platform-admin handlers. The tenants table has no RLS.
type tenantHead struct {
	ID   uuid.UUID
	Slug string
	Name string
}

func (h *PlatformCreditsHandler) listTenantHeadsCtx(ctx context.Context) ([]tenantHead, error) {
	var heads []tenantHead
	err := h.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, slug, name FROM tenants ORDER BY slug`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var th tenantHead
			if err := rows.Scan(&th.ID, &th.Slug, &th.Name); err != nil {
				return err
			}
			heads = append(heads, th)
		}
		return nil
	})
	return heads, err
}

// findAdjustmentTenant walks every tenant's RLS-scoped adjustments
// looking for the given row, returning the owning tenant ID + the
// adjustment itself. Needed because platform-admin endpoints
// (approve/reject) receive only the adjustment ID — they don't know
// which tenant context to set on the DB connection.
func (h *PlatformCreditsHandler) findAdjustmentTenant(ctx context.Context, id uuid.UUID) (uuid.UUID, *domain.CreditAdjustment, error) {
	heads, err := h.listTenantHeadsCtx(ctx)
	if err != nil {
		return uuid.Nil, nil, err
	}
	for _, th := range heads {
		var found *domain.CreditAdjustment
		_ = h.DB.WithTenantTx(ctx, th.ID, func(tx pgx.Tx) error {
			a, gerr := h.Adjustments.GetTx(ctx, tx, id)
			if gerr == nil {
				found = a
			}
			return gerr
		})
		if found != nil {
			return th.ID, found, nil
		}
	}
	return uuid.Nil, nil, store.ErrNotFound
}

// findTopupRequestTenant is the topup-request equivalent.
func (h *PlatformCreditsHandler) findTopupRequestTenant(ctx context.Context, id uuid.UUID) (uuid.UUID, *domain.TopupRequest, error) {
	heads, err := h.listTenantHeadsCtx(ctx)
	if err != nil {
		return uuid.Nil, nil, err
	}
	for _, th := range heads {
		var found *domain.TopupRequest
		_ = h.DB.WithTenantTx(ctx, th.ID, func(tx pgx.Tx) error {
			t, gerr := h.Topups.GetTx(ctx, tx, id)
			if gerr == nil {
				found = t
			}
			return gerr
		})
		if found != nil {
			return th.ID, found, nil
		}
	}
	return uuid.Nil, nil, store.ErrNotFound
}

func (h *PlatformCreditsHandler) ApproveAdjustment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tenantID, adj, err := h.findAdjustmentTenant(r.Context(), id)
	if err != nil {
		writeCreditAdminErr(w, r, err, "adjustment")
		return
	}
	if adj.Status != domain.AdjustmentPending {
		httpx.WriteErr(w, r, httpx.ErrConflict("adjustment is "+string(adj.Status)))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	if actor == adj.RequestedBy {
		httpx.WriteErr(w, r, httpx.ErrConflict("approver must differ from requester (maker/checker)"))
		return
	}
	var ledgerID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, lid, err := h.Credits.ApplyAdjustmentTx(r.Context(), tx, store.ApplyAdjustmentInput{
			Channel: adj.Channel, Credits: adj.Credits, Reason: adj.Reason, ActionedBy: actor,
		})
		if err != nil {
			return err
		}
		ledgerID = lid
		return h.Adjustments.MarkApprovedTx(r.Context(), tx, id, actor, ledgerID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"ledger_id": ledgerID})
}

func (h *PlatformCreditsHandler) RejectAdjustment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in rejectReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	tenantID, _, err := h.findAdjustmentTenant(r.Context(), id)
	if err != nil {
		writeCreditAdminErr(w, r, err, "adjustment")
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Adjustments.MarkRejectedTx(r.Context(), tx, id, actor, in.Reason)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// writeCreditAdminErr maps the lookup errors from the per-tenant
// scan helpers into clean HTTP statuses. ErrNotFound → 404; anything
// else → 500.
func writeCreditAdminErr(w http.ResponseWriter, r *http.Request, err error, what string) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrNotFound(what+" not found"))
		return
	}
	httpx.WriteErr(w, r, err)
}

// ─────────── Usage summary ───────────
//
// Iterates per-tenant because the worker DB connection runs under the
// nexus_app role (RLS enforced). A single cross-tenant aggregate
// SELECT would return zero rows without an explicit `app.tenant_id`
// context per query.

func (h *PlatformCreditsHandler) UsageSummary(w http.ResponseWriter, r *http.Request) {
	type totalRow struct {
		Channel       string `json:"channel"`
		TotalSold     int    `json:"total_sold"`
		TotalConsumed int    `json:"total_consumed"`
	}
	type zeroRow struct {
		TenantID uuid.UUID `json:"tenant_id"`
		Slug     string    `json:"slug"`
		Channel  string    `json:"channel"`
		Balance  int       `json:"balance"`
	}
	type out struct {
		Totals             []totalRow `json:"totals"`
		ZeroBalanceTenants []zeroRow  `json:"zero_balance_tenants"`
	}
	res := out{Totals: []totalRow{}, ZeroBalanceTenants: []zeroRow{}}

	// Step 1 — list tenants (RLS-free table).
	type tenantHead struct {
		id        uuid.UUID
		slug, nme string
	}
	var heads []tenantHead
	err := h.DB.WithTenantTx(r.Context(), uuid.Nil, func(tx pgx.Tx) error {
		results, err := tx.Query(r.Context(), `SELECT id, slug, name FROM tenants ORDER BY slug`)
		if err != nil {
			return err
		}
		defer results.Close()
		for results.Next() {
			var th tenantHead
			if err := results.Scan(&th.id, &th.slug, &th.nme); err != nil {
				return err
			}
			heads = append(heads, th)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Step 2 — accumulate per channel + collect zero-balance rows.
	totals := map[string]*totalRow{}
	for _, th := range heads {
		err := h.DB.WithTenantTx(r.Context(), th.id, func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(), `
				SELECT channel,
				       COALESCE(SUM(CASE WHEN credits > 0 THEN credits END), 0) AS sold,
				       COALESCE(SUM(CASE WHEN credits < 0 THEN -credits END), 0) AS consumed
				FROM notification_credit_ledger
				GROUP BY channel
			`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ch string
				var sold, consumed int
				if err := rows.Scan(&ch, &sold, &consumed); err != nil {
					return err
				}
				agg, ok := totals[ch]
				if !ok {
					agg = &totalRow{Channel: ch}
					totals[ch] = agg
				}
				agg.TotalSold += sold
				agg.TotalConsumed += consumed
			}
			zRows, err := tx.Query(r.Context(), `
				SELECT channel, balance
				FROM notification_credit_balances WHERE balance < 1
				ORDER BY channel
			`)
			if err != nil {
				return err
			}
			defer zRows.Close()
			for zRows.Next() {
				var ch string
				var bal int
				if err := zRows.Scan(&ch, &bal); err != nil {
					return err
				}
				res.ZeroBalanceTenants = append(res.ZeroBalanceTenants, zeroRow{
					TenantID: th.id, Slug: th.slug, Channel: ch, Balance: bal,
				})
			}
			return nil
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	for _, t := range totals {
		res.Totals = append(res.Totals, *t)
	}
	httpx.OK(w, res)
}
