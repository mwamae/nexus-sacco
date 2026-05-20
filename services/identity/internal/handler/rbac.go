// RBAC handler — permission catalog + role CRUD.
//
// Permissions are read-only (the catalog is developer-defined and tied
// to actual code-level checks). Roles are: system roles read-only, plus
// tenant-custom roles which admins with `roles:edit` can create/edit/delete.

package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type RBACHandler struct {
	DB             *db.Pool
	Roles          *store.RoleStore
	Permissions    *store.PermissionStore
	Audit          *store.AuditStore
	Logger         *slog.Logger
	PlatformTenant *domain.Tenant // fallback when on the platform host
}

// effectiveTenant picks the tenant the caller is operating on: a tenant
// subdomain wins; otherwise the platform pseudo-tenant.
func (h *RBACHandler) effectiveTenant(r *http.Request) *domain.Tenant {
	if t := middleware.TenantFrom(r); t != nil {
		return t
	}
	return h.PlatformTenant
}

// ─────────── GET /v1/permissions ───────────

func (h *RBACHandler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	perms, err := h.Permissions.List(r.Context())
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, perms)
}

// ─────────── GET /v1/roles ───────────

func (h *RBACHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	var roles []*domain.Role
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		list, err := h.Roles.ListVisibleWithPermissionsTx(r.Context(), tx)
		if err != nil {
			return err
		}
		roles = list
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, roles)
}

// ─────────── GET /v1/roles/{id} ───────────

func (h *RBACHandler) GetRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid role id"))
		return
	}
	var role *domain.Role
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		role, err = h.Roles.ByIDWithPermissionsTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("role not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, role)
}

// ─────────── POST /v1/roles ───────────

type createRoleRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

var roleCodeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,38}[a-z0-9]$`)

func (h *RBACHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	var req createRoleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Code = strings.ToLower(strings.TrimSpace(req.Code))
	req.Name = strings.TrimSpace(req.Name)
	if !roleCodeRE.MatchString(req.Code) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code must be lowercase letters/digits/underscores, 3-40 chars"))
		return
	}
	if req.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name is required"))
		return
	}
	// Reject codes that would collide with system roles.
	if existing, _ := h.Roles.SystemRoleByCode(r.Context(), req.Code); existing != nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("a system role with that code already exists"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	var created *domain.Role
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		role, err := h.Roles.CreateCustomTx(r.Context(), tx, store.CreateRoleInput{
			TenantID:    tenant.ID,
			Code:        req.Code,
			Name:        req.Name,
			Description: req.Description,
		})
		if err != nil {
			return err
		}
		if err := h.Roles.SetPermissionsTx(r.Context(), tx, role.ID, req.Permissions); err != nil {
			return err
		}
		role.Permissions = req.Permissions
		created = role
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a role with that code already exists in this tenant"))
			return
		}
		if db.IsForeignKeyViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("one or more permission codes are unknown"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "role.created", TargetKind: "role", TargetID: created.ID.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.Created(w, created)
}

// ─────────── PATCH /v1/roles/{id} ───────────

type updateRoleRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

func (h *RBACHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid role id"))
		return
	}
	var req updateRoleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	var updated *domain.Role
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		existing, err := h.Roles.ByIDWithPermissionsTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if existing.IsSystem {
			return httpx.ErrForbidden("system roles cannot be edited")
		}
		if req.Name != nil || req.Description != nil {
			name := existing.Name
			desc := existing.Description
			if req.Name != nil {
				name = strings.TrimSpace(*req.Name)
				if name == "" {
					return httpx.ErrBadRequest("name cannot be empty")
				}
			}
			if req.Description != nil {
				desc = *req.Description
			}
			if err := h.Roles.UpdateMetaTx(r.Context(), tx, id, name, desc); err != nil {
				return err
			}
			existing.Name = name
			existing.Description = desc
		}
		if req.Permissions != nil {
			if err := h.Roles.SetPermissionsTx(r.Context(), tx, id, *req.Permissions); err != nil {
				return err
			}
			existing.Permissions = *req.Permissions
		}
		updated = existing
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("role not found"))
			return
		}
		if db.IsForeignKeyViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("one or more permission codes are unknown"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "role.updated", TargetKind: "role", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, updated)
}

// ─────────── DELETE /v1/roles/{id} ───────────

func (h *RBACHandler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid role id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		existing, err := h.Roles.ByIDWithPermissionsTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if existing.IsSystem {
			return httpx.ErrForbidden("system roles cannot be deleted")
		}
		if existing.TenantID == nil || *existing.TenantID != tenant.ID {
			return httpx.ErrForbidden("role belongs to another tenant")
		}
		return h.Roles.DeleteTx(r.Context(), tx, id)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("role not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "role.deleted", TargetKind: "role", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.NoContent(w)
}
