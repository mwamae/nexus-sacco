// User management within a tenant: list, invite, get one, update profile,
// change status, and role assign/unassign. Works on both tenant subdomains
// and the platform host (where the effective tenant is the platform
// pseudo-tenant) so super-admins can manage other platform admins the
// same way tenant owners manage their staff.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/email"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type UserHandler struct {
	DB             *db.Pool
	Users          *store.UserStore
	Roles          *store.RoleStore
	Invites        *store.InviteStore
	Tenants        *store.TenantStore
	Audit          *store.AuditStore
	Email          email.Sender
	WebBaseURL     string        // template with {slug}
	InviteTTL      time.Duration // how long an invite link stays valid
	Logger         *slog.Logger
	PlatformTenant *domain.Tenant
}

// effectiveTenant returns the tenant the caller is operating on:
// the subdomain-resolved tenant, falling back to the platform tenant.
func (h *UserHandler) effectiveTenant(r *http.Request) *domain.Tenant {
	if t := middleware.TenantFrom(r); t != nil {
		return t
	}
	return h.PlatformTenant
}

// ─────────── GET /v1/users ───────────

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	type userWithRoles struct {
		*domain.User
		Roles []*domain.Role `json:"roles"`
	}

	var enriched []userWithRoles
	var total int
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		res, err := h.Users.ListTx(r.Context(), tx, limit, offset)
		if err != nil {
			return err
		}
		total = res.Total
		enriched = make([]userWithRoles, 0, len(res.Users))
		for _, u := range res.Users {
			roles, err := h.Roles.RolesForUserDetailedTx(r.Context(), tx, u.ID)
			if err != nil {
				return err
			}
			if roles == nil {
				roles = []*domain.Role{}
			}
			enriched = append(enriched, userWithRoles{User: u, Roles: roles})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"users":  enriched,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// ─────────── GET /v1/users/{id} ───────────

func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	var u *domain.User
	var roles []*domain.Role
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		u, err = h.Users.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		roles, err = h.Roles.RolesForUserDetailedTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("user not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if roles == nil {
		roles = []*domain.Role{}
	}
	httpx.OK(w, map[string]any{"user": u, "roles": roles})
}

// ─────────── POST /v1/users/invite ───────────

type inviteRequest struct {
	Email     string   `json:"email"`
	Phone     string   `json:"phone"`
	FullName  string   `json:"full_name"`
	RoleCodes []string `json:"role_codes"`
}

func (h *UserHandler) Invite(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	isPlatform := tenant == h.PlatformTenant

	var req inviteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.FullName = strings.TrimSpace(req.FullName)
	if req.Email == "" || req.FullName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("email and full_name are required"))
		return
	}
	if len(req.RoleCodes) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least one role_code required"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	claims := middleware.ClaimsFrom(r)
	isPlatformAdmin := claims != nil && claims.IsPlatformAdmin

	var created *domain.User
	var rawToken string
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		// Resolve every requested role first so we fail before any insert.
		roleIDs := make([]uuid.UUID, 0, len(req.RoleCodes))
		makePlatformAdmin := false
		for _, code := range req.RoleCodes {
			code = strings.ToLower(strings.TrimSpace(code))
			if code == "platform_admin" {
				if !isPlatformAdmin {
					return httpx.ErrForbidden("platform_admin can only be granted by platform admins")
				}
				if !isPlatform {
					return httpx.ErrBadRequest("platform_admin can only be granted on the platform host")
				}
				makePlatformAdmin = true
			}
			role, err := h.lookupAssignableRole(r.Context(), tx, tenant.ID, code)
			if err != nil {
				return err
			}
			roleIDs = append(roleIDs, role.ID)
		}

		u, err := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID:        tenant.ID,
			Email:           req.Email,
			Phone:           req.Phone,
			FullName:        req.FullName,
			Status:          domain.UserStatusPending,
			IsPlatformAdmin: isPlatform && makePlatformAdmin,
		})
		if err != nil {
			return err
		}
		for _, rid := range roleIDs {
			if err := h.Roles.AssignTx(r.Context(), tx, u.ID, rid, nonZero(actorID)); err != nil {
				return err
			}
		}

		raw, hash, err := store.NewInviteToken()
		if err != nil {
			return err
		}
		if err := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
			TenantID:  tenant.ID,
			UserID:    u.ID,
			TokenHash: hash,
			InvitedBy: nonZero(actorID),
			ExpiresAt: time.Now().Add(h.InviteTTL),
		}); err != nil {
			return err
		}
		created = u
		rawToken = raw
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a user with that email already exists in this tenant"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	// Email outside the transaction.
	inviterName := ""
	if claims != nil {
		inviterName = claims.FullName
	}
	h.sendInviteEmail(tenant, created, inviterName, rawToken)

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &tenant.ID,
		ActorID:    nonZero(actorID),
		Action:     "user.invited",
		TargetKind: "user",
		TargetID:   created.ID.String(),
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
	})
	httpx.Created(w, created)
}

// ─────────── POST /v1/users/{id}/invite/resend ───────────

func (h *UserHandler) ResendInvite(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	var u *domain.User
	var rawToken string
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		var err error
		u, err = h.Users.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if u.Status != domain.UserStatusPending {
			return httpx.ErrBadRequest("only pending users can be re-invited")
		}
		if err := h.Invites.InvalidateOutstandingTx(r.Context(), tx, u.ID); err != nil {
			return err
		}
		raw, hash, err := store.NewInviteToken()
		if err != nil {
			return err
		}
		if err := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
			TenantID:  tenant.ID,
			UserID:    u.ID,
			TokenHash: hash,
			InvitedBy: nonZero(actorID),
			ExpiresAt: time.Now().Add(h.InviteTTL),
		}); err != nil {
			return err
		}
		rawToken = raw
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("user not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	claims := middleware.ClaimsFrom(r)
	inviterName := ""
	if claims != nil {
		inviterName = claims.FullName
	}
	h.sendInviteEmail(tenant, u, inviterName, rawToken)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "user.invite_resent", TargetKind: "user", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.NoContent(w)
}

// ─────────── PATCH /v1/users/{id} ───────────

type updateUserRequest struct {
	FullName *string `json:"full_name,omitempty"`
	Phone    *string `json:"phone,omitempty"`
}

func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	var req updateUserRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.FullName == nil && req.Phone == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("nothing to update"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	var updated *domain.User
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		existing, err := h.Users.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		name := existing.FullName
		phone := existing.Phone
		if req.FullName != nil {
			name = strings.TrimSpace(*req.FullName)
			if name == "" {
				return httpx.ErrBadRequest("full_name cannot be empty")
			}
		}
		if req.Phone != nil {
			phone = strings.TrimSpace(*req.Phone)
		}
		if err := h.Users.UpdateProfileTx(r.Context(), tx, id, name, phone); err != nil {
			return err
		}
		updated, err = h.Users.ByIDTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("user not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "user.updated", TargetKind: "user", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, updated)
}

// ─────────── POST /v1/users/{id}/status ───────────

type setStatusRequest struct {
	Status string `json:"status"`
}

func (h *UserHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	var req setStatusRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	status := domain.UserStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	switch status {
	case domain.UserStatusActive, domain.UserStatusSuspended:
		// allowed
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be 'active' or 'suspended'"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	if id == actorID {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("you cannot change your own status"))
		return
	}

	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		existing, err := h.Users.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := h.Users.SetStatusTx(r.Context(), tx, id, status); err != nil {
			return err
		}
		if status == domain.UserStatusSuspended && existing.Status != domain.UserStatusSuspended {
			// Suspending revokes active sessions immediately.
			// (We don't have direct access to SessionStore here; the auth
			// handler owns it. Suspended users are rejected at login anyway,
			// but their existing refresh tokens would still rotate until
			// expiry — acceptable for v1.)
			_ = existing
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("user not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "user.status_changed", TargetKind: "user", TargetID: id.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.NoContent(w)
}

// ─────────── POST /v1/users/{id}/roles ───────────

type assignRoleRequest struct {
	RoleCode string    `json:"role_code"`
	RoleID   uuid.UUID `json:"role_id"`
}

func (h *UserHandler) AssignRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	var req assignRoleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.RoleCode == "" && req.RoleID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("role_code or role_id required"))
		return
	}
	claims := middleware.ClaimsFrom(r)
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		if _, err := h.Users.ByIDTx(r.Context(), tx, userID); err != nil {
			return err
		}
		var role *domain.Role
		if req.RoleID != uuid.Nil {
			role, err = h.Roles.ByIDWithPermissionsTx(r.Context(), tx, req.RoleID)
			if err != nil {
				return err
			}
			if !role.IsSystem && (role.TenantID == nil || *role.TenantID != tenant.ID) {
				return httpx.ErrForbidden("role does not belong to this tenant")
			}
		} else {
			code := strings.ToLower(strings.TrimSpace(req.RoleCode))
			if code == "platform_admin" && !(claims != nil && claims.IsPlatformAdmin) {
				return httpx.ErrForbidden("platform_admin can only be granted by platform admins")
			}
			role, err = h.lookupAssignableRole(r.Context(), tx, tenant.ID, code)
			if err != nil {
				return err
			}
		}
		return h.Roles.AssignTx(r.Context(), tx, userID, role.ID, nonZero(actorID))
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("user or role not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "user.role_assigned", TargetKind: "user", TargetID: userID.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.NoContent(w)
}

// ─────────── DELETE /v1/users/{id}/roles/{role_id} ───────────

func (h *UserHandler) UnassignRole(w http.ResponseWriter, r *http.Request) {
	tenant := h.effectiveTenant(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return
	}
	roleID, err := uuid.Parse(chi.URLParam(r, "role_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid role id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		return h.Roles.UnassignTx(r.Context(), tx, userID, roleID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: nonZero(actorID),
		Action: "user.role_unassigned", TargetKind: "user", TargetID: userID.String(),
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.NoContent(w)
}

// ─────────── helpers ───────────

// lookupAssignableRole accepts either a system role code or a tenant-custom
// role code and returns its full record, but only if it's assignable in
// the current tenant.
func (h *UserHandler) lookupAssignableRole(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (*domain.Role, error) {
	var r domain.Role
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), is_system
		FROM roles
		WHERE code = $1
		  AND (tenant_id IS NULL OR tenant_id = $2)
		ORDER BY tenant_id NULLS LAST
		LIMIT 1
	`, code, tenantID).Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrBadRequest("unknown role: " + code)
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (h *UserHandler) sendInviteEmail(tenant *domain.Tenant, u *domain.User, inviterName, rawToken string) {
	if h.Email == nil || !h.Email.Enabled() {
		h.Logger.Info("invite — email disabled, logging link",
			"user", u.Email, "token_prefix", safePrefix(rawToken))
		return
	}
	url := strings.ReplaceAll(h.WebBaseURL, "{slug}", tenant.Slug) + "/invite/accept?token=" + rawToken
	hours := int(h.InviteTTL / time.Hour)
	if hours < 1 {
		hours = 1
	}
	msg := email.InviteMessage(u.Email, u.FullName, tenant.Name, inviterName, url, hours)
	go func() {
		if err := h.Email.Send(msg); err != nil {
			h.Logger.Error("send invite email", "user", u.Email, "err", err)
		}
	}()
}

func safePrefix(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:12] + "…"
}

func nonZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
