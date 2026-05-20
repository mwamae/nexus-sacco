// Platform-admin tenant lifecycle + offboarding endpoints.
//
// These complement TenantHandler.Create/List/Current:
//   GET    /v1/platform/tenants/{id}            — full record + branches + contacts
//   POST   /v1/platform/tenants/{id}/status     — change lifecycle status
//   POST   /v1/platform/tenants/{id}/restrictions — flip operational toggles
//   POST   /v1/platform/tenants/{id}/archive    — convenience: archived + all locks on
//   GET    /v1/platform/tenants/{id}/export     — JSON bundle (operational data)
//   GET    /v1/platform/tenants/{id}/backup     — JSON bundle (full incl. roles/audit refs)

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

// ─────────── GET /v1/platform/tenants/{id} ───────────

type tenantDetail struct {
	*domain.Tenant
	Branches []*domain.TenantBranch  `json:"branches"`
	Contacts []*domain.TenantContact `json:"contacts"`
}

func (h *TenantHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	t, err := h.Tenants.ByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	var branches []*domain.TenantBranch
	var contacts []*domain.TenantContact
	err = h.DB.WithTenantTx(r.Context(), t.ID, func(tx pgx.Tx) error {
		var err error
		if branches, err = h.Tenants.BranchesForTenantTx(r.Context(), tx, t.ID); err != nil {
			return err
		}
		if contacts, err = h.Tenants.ContactsForTenantTx(r.Context(), tx, t.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if branches == nil {
		branches = []*domain.TenantBranch{}
	}
	if contacts == nil {
		contacts = []*domain.TenantContact{}
	}
	httpx.OK(w, tenantDetail{Tenant: t, Branches: branches, Contacts: contacts})
}

// ─────────── POST /v1/platform/tenants/{id}/status ───────────

type setTenantStatusRequest struct {
	Status string `json:"status"`
}

func (h *TenantHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	var req setTenantStatusRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	status := domain.TenantStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	switch status {
	case domain.TenantStatusActive, domain.TenantStatusTrial, domain.TenantStatusSuspended,
		domain.TenantStatusExpired, domain.TenantStatusPendingSetup:
		// allowed via this endpoint
	case domain.TenantStatusArchived:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("use /archive to archive a tenant"))
		return
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid status"))
		return
	}

	current, err := h.Tenants.ByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if current.Status == domain.TenantStatusArchived {
		httpx.WriteErr(w, r, httpx.ErrForbidden("archived tenants cannot be re-activated via this endpoint"))
		return
	}
	if current.Slug == platformTenantSlug {
		httpx.WriteErr(w, r, httpx.ErrForbidden("the platform pseudo-tenant cannot change status"))
		return
	}
	if err := h.Tenants.SetStatus(r.Context(), id, status); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actor, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &id, ActorID: ptr(actor),
		Action: "tenant.status_changed", TargetKind: "tenant", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"from": string(current.Status), "to": string(status)},
	})
	httpx.NoContent(w)
}

// ─────────── POST /v1/platform/tenants/{id}/restrictions ───────────

type setRestrictionsRequest struct {
	OperationsFrozen     *bool `json:"operations_frozen"`
	UsersLocked          *bool `json:"users_locked"`
	TransactionsDisabled *bool `json:"transactions_disabled"`
}

func (h *TenantHandler) SetRestrictions(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	var req setRestrictionsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.OperationsFrozen == nil && req.UsersLocked == nil && req.TransactionsDisabled == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("nothing to update"))
		return
	}

	current, err := h.Tenants.ByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if current.Slug == platformTenantSlug {
		httpx.WriteErr(w, r, httpx.ErrForbidden("the platform pseudo-tenant cannot be restricted"))
		return
	}
	if err := h.Tenants.SetRestrictions(r.Context(), id, req.OperationsFrozen, req.UsersLocked, req.TransactionsDisabled); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actor, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &id, ActorID: ptr(actor),
		Action: "tenant.restrictions_changed", TargetKind: "tenant", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"operations_frozen":     boolp(req.OperationsFrozen),
			"users_locked":          boolp(req.UsersLocked),
			"transactions_disabled": boolp(req.TransactionsDisabled),
		},
	})
	httpx.NoContent(w)
}

// ─────────── POST /v1/platform/tenants/{id}/archive ───────────

func (h *TenantHandler) Archive(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	current, err := h.Tenants.ByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if current.Slug == platformTenantSlug {
		httpx.WriteErr(w, r, httpx.ErrForbidden("the platform pseudo-tenant cannot be archived"))
		return
	}
	if current.Status == domain.TenantStatusArchived {
		httpx.NoContent(w)
		return
	}
	if err := h.Tenants.Archive(r.Context(), id); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &id, ActorID: ptr(actor),
		Action: "tenant.archived", TargetKind: "tenant", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"previous_status": string(current.Status)},
	})
	httpx.NoContent(w)
}

// ─────────── GET /v1/platform/tenants/{id}/export and /backup ───────────
//
// Both produce a JSON bundle, served as a downloadable attachment.
//   export — operational data only (no roles, no audit metadata)
//   backup — adds user→role assignments and current restriction flags
// Both audit-logged under distinct actions so platform support can prove
// what was generated, when, and by whom.

func (h *TenantHandler) Export(w http.ResponseWriter, r *http.Request) {
	h.bundle(w, r, "export")
}

func (h *TenantHandler) Backup(w http.ResponseWriter, r *http.Request) {
	h.bundle(w, r, "backup")
}

type bundleHeader struct {
	GeneratedAt time.Time `json:"generated_at"`
	Kind        string    `json:"kind"`
	Generator   string    `json:"generator"`
}

func (h *TenantHandler) bundle(w http.ResponseWriter, r *http.Request, kind string) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	t, err := h.Tenants.ByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	out := map[string]any{
		"_bundle": bundleHeader{
			GeneratedAt: time.Now().UTC(),
			Kind:        kind,
			Generator:   "nexus-identity",
		},
		"tenant": t,
	}
	err = h.DB.WithTenantTx(r.Context(), t.ID, func(tx pgx.Tx) error {
		branches, err := h.Tenants.BranchesForTenantTx(r.Context(), tx, t.ID)
		if err != nil {
			return err
		}
		contacts, err := h.Tenants.ContactsForTenantTx(r.Context(), tx, t.ID)
		if err != nil {
			return err
		}
		users, err := h.Users.ListTx(r.Context(), tx, 10000, 0)
		if err != nil {
			return err
		}
		out["branches"] = branches
		out["contacts"] = contacts
		out["users"] = users.Users

		if kind == "backup" {
			// Backup adds per-user role assignments.
			type userRoles struct {
				UserID uuid.UUID `json:"user_id"`
				Roles  []string  `json:"role_codes"`
			}
			roleRows := make([]userRoles, 0, len(users.Users))
			for _, u := range users.Users {
				codes, err := h.Roles.RolesForUserTx(r.Context(), tx, u.ID)
				if err != nil {
					return err
				}
				roleRows = append(roleRows, userRoles{UserID: u.ID, Roles: codes})
			}
			out["user_roles"] = roleRows
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actor, _ := middleware.UserIDFrom(r)
	action := "tenant.exported"
	if kind == "backup" {
		action = "tenant.backed_up"
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &id, ActorID: ptr(actor),
		Action: action, TargetKind: "tenant", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})

	filename := fmt.Sprintf("%s-%s-%s.json", t.Slug, kind, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ─────────── helpers ───────────

const platformTenantSlug = "platform"

func boolp(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
