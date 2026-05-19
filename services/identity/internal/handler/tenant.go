// Platform-admin tenant endpoints + tenant self-info.

package handler

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type TenantHandler struct {
	DB      *db.Pool
	Tenants *store.TenantStore
	Users   *store.UserStore
	Roles   *store.RoleStore
	Audit   *store.AuditStore
	Logger  *slog.Logger
}

var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{1,38}[a-z0-9])?$`)

// ─────────── POST /v1/platform/tenants ───────────
//
// Creates a new tenant AND its initial owner user atomically.
// Requires a platform-admin token (enforced by middleware).

type createTenantRequest struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	LegalName    string `json:"legal_name"`
	Kind         string `json:"kind"`
	CountryCode  string `json:"country_code"`
	CurrencyCode string `json:"currency_code"`
	LicenseNo    string `json:"license_no"`
	OwnerEmail   string `json:"owner_email"`
	OwnerName    string `json:"owner_name"`
	OwnerPhone   string `json:"owner_phone"`
	OwnerPassword string `json:"owner_password"`
}

type createTenantResponse struct {
	Tenant *domain.Tenant `json:"tenant"`
	Owner  *domain.User   `json:"owner"`
}

func (h *TenantHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if !slugRE.MatchString(req.Slug) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("slug must be 3-40 chars, lowercase, [a-z0-9-]"))
		return
	}
	if req.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name is required"))
		return
	}
	if req.OwnerEmail == "" || req.OwnerPassword == "" || req.OwnerName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("owner_email, owner_name, and owner_password are required"))
		return
	}
	if len(req.OwnerPassword) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("owner_password must be at least 12 characters"))
		return
	}
	if req.Kind == "" {
		req.Kind = string(domain.TenantKindSACCO)
	}
	if req.CountryCode == "" {
		req.CountryCode = "KE"
	}
	if req.CurrencyCode == "" {
		req.CurrencyCode = "KES"
	}

	t, err := h.Tenants.Create(r.Context(), store.CreateTenantInput{
		Slug:         req.Slug,
		Name:         req.Name,
		LegalName:    req.LegalName,
		Kind:         domain.TenantKind(req.Kind),
		CountryCode:  strings.ToUpper(req.CountryCode),
		CurrencyCode: strings.ToUpper(req.CurrencyCode),
		LicenseNo:    req.LicenseNo,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("tenant slug already taken"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	ownerRole, err := h.Roles.SystemRoleByCode(r.Context(), "tenant_owner")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	hash, err := auth.HashPassword(req.OwnerPassword)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var owner *domain.User
	err = h.DB.WithTenantTx(r.Context(), t.ID, func(tx pgx.Tx) error {
		u, err := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID:     t.ID,
			Email:        req.OwnerEmail,
			Phone:        req.OwnerPhone,
			FullName:     req.OwnerName,
			PasswordHash: hash,
			Status:       domain.UserStatusActive,
		})
		if err != nil {
			return err
		}
		owner = u
		return h.Roles.AssignTx(r.Context(), tx, u.ID, ownerRole.ID, nil)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &t.ID,
		ActorID:    ptr(actorID),
		Action:     "tenant.created",
		TargetKind: "tenant",
		TargetID:   t.ID.String(),
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Metadata:   map[string]any{"slug": t.Slug, "owner_id": owner.ID.String()},
	})

	httpx.Created(w, createTenantResponse{Tenant: t, Owner: owner})
}

// ─────────── GET /v1/platform/tenants ───────────

func (h *TenantHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Tenants.List(r.Context(), 200)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, list)
}

// ─────────── GET /v1/tenant ───────────

func (h *TenantHandler) Current(w http.ResponseWriter, r *http.Request) {
	t := middleware.TenantFrom(r)
	if t == nil {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no tenant"))
		return
	}
	httpx.OK(w, t)
}

func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
