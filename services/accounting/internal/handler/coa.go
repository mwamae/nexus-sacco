// Chart of Accounts HTTP endpoints.
//
//   GET    /v1/coa             list (optionally active-only)
//   POST   /v1/coa             create
//   GET    /v1/coa/{id}        detail
//   PATCH  /v1/coa/{id}        update (name/type/parent/active/desc)
//
// System-locked accounts are read-only — the store rejects updates
// with ErrSystemLocked which we map to 409 here.

package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

type CoAHandler struct {
	DB     *db.Pool
	CoA    *store.CoAStore
	Logger *slog.Logger
}

func (h *CoAHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	activeOnly := r.URL.Query().Get("active") == "1" || r.URL.Query().Get("active") == "true"
	var items []domain.Account
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.CoA.ListTx(r.Context(), tx, activeOnly)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *CoAHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var a *domain.Account
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		a, err = h.CoA.GetTx(r.Context(), tx, id)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrNotFound("account not found"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, a)
}

type createAccountReq struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Class         string  `json:"class"`
	Type          string  `json:"type"`
	ParentCode    string  `json:"parent_code,omitempty"` // resolved to parent_id server-side
	NormalBalance string  `json:"normal_balance"`
	CurrencyCode  string  `json:"currency_code,omitempty"`
	IsActive      bool    `json:"is_active"`
	Description   *string `json:"description,omitempty"`
}

func (h *CoAHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createAccountReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Code == "" || in.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code and name are required"))
		return
	}
	class := domain.AccountClass(in.Class)
	if !class.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("class must be asset, liability, equity, income, or expense"))
		return
	}
	nb := domain.NormalBalance(in.NormalBalance)
	if nb != domain.NormalDebit && nb != domain.NormalCredit {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("normal_balance must be debit or credit"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.Account
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var parentID *uuid.UUID
		if in.ParentCode != "" {
			p, err := h.CoA.GetByCodeTx(r.Context(), tx, in.ParentCode)
			if err != nil {
				return httpx.ErrBadRequest("parent_code not found in chart of accounts")
			}
			parentID = &p.ID
		}
		a, err := h.CoA.CreateTx(r.Context(), tx, store.CreateAccountInput{
			Code: in.Code, Name: in.Name, Class: class, Type: in.Type,
			ParentID: parentID, NormalBalance: nb, CurrencyCode: in.CurrencyCode,
			IsActive: in.IsActive, Description: in.Description,
		})
		if err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

type updateAccountReq struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	ParentCode  string  `json:"parent_code,omitempty"`
	IsActive    bool    `json:"is_active"`
	Description *string `json:"description,omitempty"`
}

func (h *CoAHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in updateAccountReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.Account
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var parentID *uuid.UUID
		if in.ParentCode != "" {
			p, lerr := h.CoA.GetByCodeTx(r.Context(), tx, in.ParentCode)
			if lerr != nil {
				return httpx.ErrBadRequest("parent_code not found in chart of accounts")
			}
			parentID = &p.ID
		}
		a, lerr := h.CoA.UpdateTx(r.Context(), tx, id, store.UpdateAccountInput{
			Name: in.Name, Type: in.Type, ParentID: parentID,
			IsActive: in.IsActive, Description: in.Description,
		})
		if lerr != nil {
			return lerr
		}
		out = a
		return nil
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound("account not found"))
	case errors.Is(err, store.ErrSystemLocked):
		httpx.WriteErr(w, r, httpx.ErrConflict("system-locked account; cannot edit"))
	case err != nil:
		httpx.WriteErr(w, r, err)
	default:
		httpx.OK(w, out)
	}
}
