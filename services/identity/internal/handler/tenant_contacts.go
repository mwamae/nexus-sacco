// Platform-admin endpoints for managing tenant contact records + the
// staff (users) inside a tenant from the platform side. These let a
// platform super admin add/edit/remove contact people on a tenant
// record and invite/list staff users for any tenant without having
// to log in on that tenant's subdomain.
//
// Routes registered in routes.go:
//
//   POST   /v1/platform/tenants/{id}/contacts                 — add a contact
//   PATCH  /v1/platform/tenants/{id}/contacts/{contact_id}    — edit a contact
//   DELETE /v1/platform/tenants/{id}/contacts/{contact_id}    — remove a contact
//
//   GET    /v1/platform/tenants/{id}/users                    — list staff users
//   POST   /v1/platform/tenants/{id}/users/invite             — invite a user

package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

// ─────────── Contacts ───────────

type contactRequest struct {
	FullName        string   `json:"full_name"`
	Title           string   `json:"title,omitempty"`
	Email           string   `json:"email,omitempty"`
	Phone           string   `json:"phone,omitempty"`
	// Provision the contact as a tenant-side user with the listed
	// roles (defaults to tenant_owner). Email becomes required when
	// this is true.
	ProvisionAsUser bool     `json:"provision_as_user,omitempty"`
	RoleCodes       []string `json:"role_codes,omitempty"`
}

func (in contactRequest) normalised() store.ContactInput {
	return store.ContactInput{
		FullName: strings.TrimSpace(in.FullName),
		Title:    strings.TrimSpace(in.Title),
		Email:    strings.ToLower(strings.TrimSpace(in.Email)),
		Phone:    strings.TrimSpace(in.Phone),
	}
}

func (h *TenantHandler) AddContact(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	var in contactRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	c := in.normalised()
	if c.FullName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("full_name is required"))
		return
	}
	if in.ProvisionAsUser && c.Email == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("email is required when provision_as_user=true"))
		return
	}
	tenant, err := h.Tenants.ByID(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	roleCodes := in.RoleCodes
	if in.ProvisionAsUser && len(roleCodes) == 0 {
		roleCodes = []string{"tenant_owner"}
	}

	var (
		created      *domain.TenantContact
		createdUser  *domain.User
		rawInvite    string
		actorID, _   = middleware.UserIDFrom(r)
	)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var lerr error
		created, lerr = h.Tenants.AddContactTx(r.Context(), tx, tenantID, c)
		if lerr != nil {
			return lerr
		}
		if !in.ProvisionAsUser {
			return nil
		}
		// Provision the contact as a tenant-side user with the chosen
		// roles. Pending status + invite, no password — same flow as
		// the InviteUser endpoint, kept inline here so a contact + user
		// land in the same tx (rolls back atomically if either fails).
		roleIDs := make([]uuid.UUID, 0, len(roleCodes))
		for _, code := range roleCodes {
			code = strings.ToLower(strings.TrimSpace(code))
			if code == "platform_admin" {
				return httpx.ErrBadRequest("platform_admin cannot be granted via contact provisioning")
			}
			role, rerr := h.UserH.lookupAssignableRole(r.Context(), tx, tenantID, code)
			if rerr != nil {
				return rerr
			}
			roleIDs = append(roleIDs, role.ID)
		}
		u, uerr := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID: tenantID,
			Email:    c.Email,
			Phone:    c.Phone,
			FullName: c.FullName,
			Status:   domain.UserStatusPending,
		})
		if uerr != nil {
			return uerr
		}
		for _, rid := range roleIDs {
			if aerr := h.Roles.AssignTx(r.Context(), tx, u.ID, rid, nonZero(actorID)); aerr != nil {
				return aerr
			}
		}
		raw, hash, terr := store.NewInviteToken()
		if terr != nil {
			return terr
		}
		if ierr := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
			TenantID:  tenantID,
			UserID:    u.ID,
			TokenHash: hash,
			InvitedBy: nonZero(actorID),
			ExpiresAt: time.Now().Add(h.InviteTTL),
		}); ierr != nil {
			return ierr
		}
		createdUser = u
		rawInvite = raw
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

	// Fire the invite email out-of-band.
	if rawInvite != "" && createdUser != nil && h.UserH != nil {
		claims := middleware.ClaimsFrom(r)
		inviterName := ""
		if claims != nil {
			inviterName = claims.FullName
		}
		h.UserH.sendInviteEmail(tenant, createdUser, inviterName, rawInvite)
	}

	httpx.Created(w, map[string]any{
		"contact": created,
		"user":    createdUser, // null when not provisioned
	})
}

func (h *TenantHandler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	contactID, err := uuid.Parse(chi.URLParam(r, "contact_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid contact id"))
		return
	}
	var in contactRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	c := in.normalised()
	if c.FullName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("full_name is required"))
		return
	}
	var updated *domain.TenantContact
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var lerr error
		updated, lerr = h.Tenants.UpdateContactTx(r.Context(), tx, contactID, c)
		return lerr
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("contact not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

func (h *TenantHandler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	contactID, err := uuid.Parse(chi.URLParam(r, "contact_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid contact id"))
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Tenants.DeleteContactTx(r.Context(), tx, contactID)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("contact not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Users (platform admin acting on a specific tenant) ───────────
//
// These are thin wrappers around UserHandler.List + Invite that take
// the tenant from the URL path instead of from the request's effective
// tenant. Platform admins use these to manage staff across tenants
// without having to context-switch to each tenant's subdomain.

func (h *TenantHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	if _, err := h.Tenants.ByID(r.Context(), tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	var out []userWithRoles
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Page once with a generous limit — the tenant-side List uses
		// real pagination via query params, but the platform-side
		// management screen wants the full roster at a glance.
		res, err := h.Users.ListTx(r.Context(), tx, 500, 0)
		if err != nil {
			return err
		}
		for _, u := range res.Users {
			roles, err := h.Roles.RolesForUserDetailedTx(r.Context(), tx, u.ID)
			if err != nil {
				return err
			}
			out = append(out, userWithRoles{User: u, Roles: roles})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []userWithRoles{}
	}
	httpx.OK(w, map[string]any{"users": out, "total": len(out)})
}

type inviteUserToTenantReq struct {
	Email     string   `json:"email"`
	Phone     string   `json:"phone,omitempty"`
	FullName  string   `json:"full_name"`
	RoleCodes []string `json:"role_codes"`
}

func (h *TenantHandler) InviteUser(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return
	}
	tenant, err := h.Tenants.ByID(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("tenant not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	var req inviteUserToTenantReq
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
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least one role_code is required"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	var (
		created  *domain.User
		rawToken string
	)
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		// Resolve every requested role before any insert. Reuses the
		// helper on UserHandler so the rules stay consistent with the
		// tenant-side invite flow.
		roleIDs := make([]uuid.UUID, 0, len(req.RoleCodes))
		for _, code := range req.RoleCodes {
			code = strings.ToLower(strings.TrimSpace(code))
			if code == "platform_admin" {
				return httpx.ErrBadRequest("platform_admin can only be granted on the platform host")
			}
			role, lerr := h.UserH.lookupAssignableRole(r.Context(), tx, tenant.ID, code)
			if lerr != nil {
				return lerr
			}
			roleIDs = append(roleIDs, role.ID)
		}
		u, lerr := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID: tenant.ID,
			Email:    req.Email,
			Phone:    req.Phone,
			FullName: req.FullName,
			Status:   domain.UserStatusPending,
		})
		if lerr != nil {
			return lerr
		}
		for _, rid := range roleIDs {
			if rerr := h.Roles.AssignTx(r.Context(), tx, u.ID, rid, nonZero(actorID)); rerr != nil {
				return rerr
			}
		}
		raw, hash, terr := store.NewInviteToken()
		if terr != nil {
			return terr
		}
		if cerr := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
			TenantID:  tenant.ID,
			UserID:    u.ID,
			TokenHash: hash,
			InvitedBy: nonZero(actorID),
			ExpiresAt: time.Now().Add(h.InviteTTL),
		}); cerr != nil {
			return cerr
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
	// Fire the invite email outside the transaction. The notifier
	// failure mode is "log and continue" so a bad SMTP doesn't block
	// the user record creation — the admin can resend the invite from
	// the staff list.
	claims := middleware.ClaimsFrom(r)
	inviterName := ""
	if claims != nil {
		inviterName = claims.FullName
	}
	h.UserH.sendInviteEmail(tenant, created, inviterName, rawToken)

	httpx.Created(w, map[string]any{
		"user":           created,
		"invite_expires": time.Now().Add(h.InviteTTL),
	})
}

// userWithRoles mirrors the shape returned by the in-tenant List
// endpoint so the same frontend code can consume both.
type userWithRoles struct {
	User  *domain.User   `json:"user"`
	Roles []*domain.Role `json:"roles"`
}
