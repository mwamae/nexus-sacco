// User management within a tenant: list, invite (create with temp password),
// and roles enumeration.

package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type UserHandler struct {
	DB     *db.Pool
	Users  *store.UserStore
	Roles  *store.RoleStore
	Audit  *store.AuditStore
	Logger *slog.Logger
}

// ─────────── GET /v1/users ───────────

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	var result *store.ListUsersResult
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		res, err := h.Users.ListTx(r.Context(), tx, limit, offset)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"users":  result.Users,
		"total":  result.Total,
		"limit":  limit,
		"offset": offset,
	})
}

// ─────────── POST /v1/users ───────────
//
// Invite (create) a user. In this skeleton the password is supplied
// directly; replace with email-invite flow once notification service exists.

type inviteUserRequest struct {
	Email     string   `json:"email"`
	Phone     string   `json:"phone"`
	FullName  string   `json:"full_name"`
	Password  string   `json:"password"`
	RoleCodes []string `json:"role_codes"`
}

func (h *UserHandler) Invite(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var req inviteUserRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.FullName == "" || req.Password == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("email, full_name, and password are required"))
		return
	}
	if len(req.Password) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("password must be at least 12 characters"))
		return
	}
	if len(req.RoleCodes) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least one role_code required"))
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	var created *domain.User
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		u, err := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID:     tenant.ID,
			Email:        req.Email,
			Phone:        req.Phone,
			FullName:     req.FullName,
			PasswordHash: hash,
			Status:       domain.UserStatusActive,
		})
		if err != nil {
			return err
		}
		// Resolve role codes against this tenant's visible roles.
		for _, code := range req.RoleCodes {
			if code == "platform_admin" {
				return httpx.ErrForbidden("platform_admin can only be granted by platform admins")
			}
			// SystemRoleByCode looks at tenant_id IS NULL; tenant-custom roles aren't supported yet here.
			role, err := h.Roles.SystemRoleByCode(r.Context(), code)
			if err != nil {
				return httpx.ErrBadRequest("unknown role: " + code)
			}
			if err := h.Roles.AssignTx(r.Context(), tx, u.ID, role.ID, &actorID); err != nil {
				return err
			}
		}
		created = u
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

// ─────────── GET /v1/roles ───────────

func (h *UserHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	var roles []*domain.Role
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		list, err := h.Roles.ListVisibleTx(r.Context(), tx)
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

func nonZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
