// Accounting period endpoints.
//
//   GET   /v1/periods         list
//   POST  /v1/periods/{id}/close   close a period
//   POST  /v1/periods/{id}/reopen  re-open a closed period (requires reason)

package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

type PeriodHandler struct {
	DB      *db.Pool
	Periods *store.PeriodStore
	Logger  *slog.Logger
}

func (h *PeriodHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.Period
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Periods.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

type closeReq struct {
	Notes string `json:"notes,omitempty"`
}

func (h *PeriodHandler) Close(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in closeReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Periods.CloseTx(r.Context(), tx, id, actor, in.Notes)
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrConflict("period not found or already closed"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

type reopenReq struct {
	Reason string `json:"reason"`
}

func (h *PeriodHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in reopenReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required when re-opening a closed period"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Periods.ReopenTx(r.Context(), tx, id, actor, in.Reason)
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrConflict("period not found or not closed"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}
