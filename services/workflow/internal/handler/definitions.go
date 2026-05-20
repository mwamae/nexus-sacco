// Definition CRUD.

package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/workflow/internal/db"
	"github.com/nexussacco/workflow/internal/domain"
	"github.com/nexussacco/workflow/internal/httpx"
	"github.com/nexussacco/workflow/internal/middleware"
	"github.com/nexussacco/workflow/internal/store"
)

type DefinitionHandler struct {
	DB     *db.Pool
	Defs   *store.DefinitionStore
	Logger *slog.Logger
}

// ─────────── GET /v1/workflows ───────────

func (h *DefinitionHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	in := store.ListDefsInput{
		ProcessKind: strings.TrimSpace(q.Get("process_kind")),
		OnlyActive:  q.Get("only_active") == "1" || q.Get("only_active") == "true",
	}
	var out []*domain.Definition
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		defs, err := h.Defs.ListTx(r.Context(), tx, in)
		if err != nil {
			return err
		}
		out = defs
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []*domain.Definition{}
	}
	httpx.OK(w, out)
}

// ─────────── GET /v1/workflows/{id} ───────────

func (h *DefinitionHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var d *domain.Definition
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		d, err = h.Defs.ByIDTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("definition not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, d)
}

// ─────────── POST /v1/workflows ───────────
//
// Creates a fresh definition (new process_kind), OR — if a definition
// already exists for this process_kind — creates a new version of it.
// Pass active=false to keep the existing version live.

type createDefRequest struct {
	ProcessKind string            `json:"process_kind"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Active      *bool             `json:"active"`
	Levels      []domain.LevelDef `json:"levels"`
}

func (h *DefinitionHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var req createDefRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.ProcessKind = strings.ToLower(strings.TrimSpace(req.ProcessKind))
	req.Name = strings.TrimSpace(req.Name)
	if req.ProcessKind == "" || req.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("process_kind and name are required"))
		return
	}
	if len(req.Levels) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least one level is required"))
		return
	}
	// Validate levels minimally — order is implicit (we re-index on save).
	for i, l := range req.Levels {
		if strings.TrimSpace(l.Name) == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("level "+itoa(i+1)+": name is required"))
			return
		}
		if len(l.ApproverRoles) == 0 && len(l.ApproverUserIDs) == 0 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("level "+itoa(i+1)+": needs at least one approver_role or approver_user_id"))
			return
		}
		switch l.Quorum {
		case "", domain.QuorumAnyOne, domain.QuorumAll:
			// ok
		default:
			httpx.WriteErr(w, r, httpx.ErrBadRequest("level "+itoa(i+1)+": quorum must be any_one or all"))
			return
		}
	}
	actorID, _ := middleware.UserIDFrom(r)
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	var d *domain.Definition
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		d, err = h.Defs.CreateTx(r.Context(), tx, store.CreateDefinitionInput{
			TenantID:    tenantID,
			ProcessKind: req.ProcessKind,
			Name:        req.Name,
			Description: req.Description,
			Levels:      req.Levels,
			CreatedBy:   nonZero(actorID),
			Active:      active,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, d)
}

// ─────────── POST /v1/workflows/{id}/activation ───────────

type activationReq struct {
	Active bool `json:"active"`
}

func (h *DefinitionHandler) SetActivation(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var req activationReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Defs.SetActiveTx(r.Context(), tx, tenantID, id, req.Active)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("definition not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── helpers ───────────

func nonZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func itoa(i int) string { return strconv.Itoa(i) }
