// Counterparty HTTP surface — Phase B routes per the unified-register spec.
//
//   GET   /v1/counterparties                  list + filter + search
//   GET   /v1/counterparties/{id}             detail
//   POST  /v1/counterparties                  create (kind-aware)
//   PATCH /v1/counterparties/{id}             partial update
//
// Status changes still route via the existing /members/{id}/status-change
// workflow surface — see prompt #3. Accounts + ledger aggregators link
// to the savings service via the existing /v1/deposit-accounts/by-member,
// /v1/share-accounts/by-member, /v1/loan-reports/by-member, and
// /v1/member-ledger endpoints (those still take counterparty_id since
// FK-rewriting is a Phase C concern).

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/store"
)

type CounterpartyHandler struct {
	DB             *db.Pool
	Counterparties *store.CounterpartyStore
	Logger         *slog.Logger
}

// ─────────── GET /v1/counterparties ───────────

func (h *CounterpartyHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()

	in := store.CPListInput{
		Query: q.Get("q"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			in.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			in.Offset = n
		}
	}
	// Repeating ?kind=individual&kind=chama supported; comma-list
	// also accepted for callers that prefer it.
	for _, k := range q["kind"] {
		for _, p := range splitCSV(k) {
			in.Kind = append(in.Kind, domain.CounterpartyKind(p))
		}
	}
	for _, s := range q["status"] {
		for _, p := range splitCSV(s) {
			in.Status = append(in.Status, domain.CounterpartyStatus(p))
		}
	}

	var res *store.CPListResult
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		res, err = h.Counterparties.ListTx(r.Context(), tx, in)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"counterparties": res.Counterparties,
		"total":          res.Total,
		"limit":          in.Limit,
		"offset":         in.Offset,
	})
}

// ─────────── GET /v1/counterparties/{id} ───────────

func (h *CounterpartyHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuidFromParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var c *domain.Counterparty
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		c, err = h.Counterparties.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, c)
}

// ─────────── POST /v1/counterparties ───────────

type createCounterpartyReq struct {
	Kind           domain.CounterpartyKind `json:"kind"`
	DisplayName    string                  `json:"display_name"`
	TradingAs      *string                 `json:"trading_as,omitempty"`
	LegacyID       *string                 `json:"legacy_id,omitempty"`
	RegistrationNo *string                 `json:"registration_no,omitempty"`
	Status         domain.CounterpartyStatus `json:"status"`
	KYCState       domain.CounterpartyKYCState `json:"kyc_state"`
	RiskBand       domain.CounterpartyRiskBand `json:"risk_band"`
	Individual     json.RawMessage         `json:"individual,omitempty"`
	Institution    json.RawMessage         `json:"institution,omitempty"`
	Contact        json.RawMessage         `json:"contact,omitempty"`
}

func (h *CounterpartyHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	actorID, _ := middleware.UserIDFrom(r)
	var req createCounterpartyReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.DisplayName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("display_name required"))
		return
	}
	if !req.Kind.IsIndividual() && !req.Kind.IsInstitutional() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid kind"))
		return
	}
	if req.Status == "" {
		req.Status = domain.CPStatusPending
	}

	in := store.CreateInput{
		TenantID:       tenantID,
		LegacyID:       req.LegacyID,
		Kind:           req.Kind,
		DisplayName:    req.DisplayName,
		TradingAs:      req.TradingAs,
		Status:         req.Status,
		KYCState:       req.KYCState,
		RiskBand:       req.RiskBand,
		RegistrationNo: req.RegistrationNo,
		Individual:     req.Individual,
		Institution:    req.Institution,
		Contact:        req.Contact,
		CreatedBy:      ptrUUID(actorID),
	}
	var out *domain.Counterparty
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = h.Counterparties.CreateTx(r.Context(), tx, in)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── PATCH /v1/counterparties/{id} ───────────

type updateCounterpartyReq struct {
	DisplayName    *string                       `json:"display_name,omitempty"`
	TradingAs      *string                       `json:"trading_as,omitempty"`
	Status         *domain.CounterpartyStatus    `json:"status,omitempty"`
	KYCState       *domain.CounterpartyKYCState  `json:"kyc_state,omitempty"`
	RiskBand       *domain.CounterpartyRiskBand  `json:"risk_band,omitempty"`
	RegistrationNo *string                       `json:"registration_no,omitempty"`
	Individual     *json.RawMessage              `json:"individual,omitempty"`
	Institution    *json.RawMessage              `json:"institution,omitempty"`
	Contact        *json.RawMessage              `json:"contact,omitempty"`
}

func (h *CounterpartyHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := uuidFromParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	actorID, _ := middleware.UserIDFrom(r)
	var req updateCounterpartyReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	p := store.UpdatePatch{
		DisplayName: req.DisplayName, TradingAs: req.TradingAs,
		Status: req.Status, KYCState: req.KYCState, RiskBand: req.RiskBand,
		RegistrationNo: req.RegistrationNo,
		Individual: req.Individual, Institution: req.Institution, Contact: req.Contact,
		UpdatedBy: ptrUUID(actorID),
	}
	var out *domain.Counterparty
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = h.Counterparties.UpdateTx(r.Context(), tx, id, p)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── helpers ───────────

func ptrUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil { return nil }
	return &u
}

func uuidFromParam(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.ErrBadRequest("invalid " + name)
	}
	return id, nil
}

func splitCSV(s string) []string {
	if s == "" { return nil }
	out := []string{}
	cur := ""
	for _, ch := range s {
		if ch == ',' {
			if cur != "" { out = append(out, cur); cur = "" }
		} else {
			cur += string(ch)
		}
	}
	if cur != "" { out = append(out, cur) }
	return out
}
