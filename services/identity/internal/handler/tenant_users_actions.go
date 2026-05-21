// Platform-admin actions on individual users in a specific tenant.
// Routes mounted under /v1/platform/tenants/{id}/users/{user_id}/...
//
// Each action enforces the "last admin standing" rule when applicable
// — a tenant must always have at least one Active user holding the
// tenant_owner (Tenant Super Admin) role. Suspending, revoking, or
// removing that role from the only remaining admin returns 409.

package handler

import (
	"context"
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

// tenantSuperAdminRole is the code we treat as the canonical Tenant
// Super Admin. Mapped to tenant_owner — the highest-privileged tenant
// role in the system catalogue.
const tenantSuperAdminRole = "tenant_owner"

// ─────────── Resend invitation ───────────

func (h *TenantHandler) ResendUserInvite(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.parseTenantUser(w, r)
	if !ok {
		return
	}
	tenant, user, err := h.loadTenantUser(r.Context(), tenantID, userID)
	if err != nil {
		writeUserErr(w, r, err)
		return
	}
	if user.Status != domain.UserStatusPending {
		httpx.WriteErr(w, r, httpx.ErrConflict("user is not in pending state — they have already activated their account"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	var rawToken string
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Invalidate any outstanding tokens for this user — the spec
		// says a fresh invite replaces the previous one.
		if err := h.Invites.InvalidateOutstandingTx(r.Context(), tx, user.ID); err != nil {
			return err
		}
		raw, hash, terr := store.NewInviteToken()
		if terr != nil {
			return terr
		}
		if cerr := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
			TenantID:  tenantID,
			UserID:    user.ID,
			TokenHash: hash,
			InvitedBy: nonZero(actorID),
			ExpiresAt: time.Now().Add(h.InviteTTL),
		}); cerr != nil {
			return cerr
		}
		rawToken = raw
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if h.UserH != nil {
		claims := middleware.ClaimsFrom(r)
		inviterName := ""
		if claims != nil {
			inviterName = claims.FullName
		}
		h.UserH.sendInviteEmail(tenant, user, inviterName, rawToken)
	}
	httpx.OK(w, map[string]any{
		"user":           user,
		"invite_expires": time.Now().Add(h.InviteTTL),
	})
}

// ─────────── Suspend / Reactivate ───────────

type setStatusReq struct {
	Reason string `json:"reason,omitempty"`
}

func (h *TenantHandler) SuspendUser(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.parseTenantUser(w, r)
	if !ok {
		return
	}
	var in setStatusReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required when suspending a user"))
		return
	}
	_, _, err := h.loadTenantUser(r.Context(), tenantID, userID)
	if err != nil {
		writeUserErr(w, r, err)
		return
	}
	if blocked, msg := h.lastAdminGuard(r.Context(), tenantID, userID); blocked {
		httpx.WriteErr(w, r, httpx.ErrConflict(msg))
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Users.SetStatusTx(r.Context(), tx, userID, domain.UserStatusSuspended)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.auditUserAction(r, tenantID, userID, "user.suspended", map[string]any{"reason": in.Reason})
	httpx.NoContent(w)
}

func (h *TenantHandler) ReactivateUser(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.parseTenantUser(w, r)
	if !ok {
		return
	}
	_, user, err := h.loadTenantUser(r.Context(), tenantID, userID)
	if err != nil {
		writeUserErr(w, r, err)
		return
	}
	if user.Status == domain.UserStatusActive {
		httpx.WriteErr(w, r, httpx.ErrConflict("user is already active"))
		return
	}
	if user.Status == domain.UserStatusPending {
		httpx.WriteErr(w, r, httpx.ErrConflict("user has not activated yet — resend invite instead"))
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Users.SetStatusTx(r.Context(), tx, userID, domain.UserStatusActive)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.auditUserAction(r, tenantID, userID, "user.reactivated", nil)
	httpx.NoContent(w)
}

// ─────────── Revoke (permanent deactivate) ───────────

func (h *TenantHandler) RevokeUser(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.parseTenantUser(w, r)
	if !ok {
		return
	}
	var in setStatusReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required when revoking access"))
		return
	}
	if _, _, err := h.loadTenantUser(r.Context(), tenantID, userID); err != nil {
		writeUserErr(w, r, err)
		return
	}
	if blocked, msg := h.lastAdminGuard(r.Context(), tenantID, userID); blocked {
		httpx.WriteErr(w, r, httpx.ErrConflict(msg))
		return
	}
	// Use the "closed" status to mean "revoked" — the auth flow
	// already refuses login for closed accounts.
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Users.SetStatusTx(r.Context(), tx, userID, domain.UserStatusClosed)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.auditUserAction(r, tenantID, userID, "user.revoked", map[string]any{"reason": in.Reason})
	httpx.NoContent(w)
}

// ─────────── Force password reset ───────────
//
// Generates a single-use reset token and emails it. Existing
// session(s) are revoked so the user is logged out immediately.

func (h *TenantHandler) ForcePasswordReset(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.parseTenantUser(w, r)
	if !ok {
		return
	}
	tenant, user, err := h.loadTenantUser(r.Context(), tenantID, userID)
	if err != nil {
		writeUserErr(w, r, err)
		return
	}
	if h.AuthH == nil {
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	if err := h.AuthH.IssuePasswordResetFor(r.Context(), tenant, user); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.auditUserAction(r, tenantID, userID, "user.force_password_reset", nil)
	httpx.NoContent(w)
}

// ─────────── Helpers ───────────

func (h *TenantHandler) parseTenantUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant id"))
		return uuid.Nil, uuid.Nil, false
	}
	userID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid user id"))
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, userID, true
}

func (h *TenantHandler) loadTenantUser(ctx context.Context, tenantID, userID uuid.UUID) (*domain.Tenant, *domain.User, error) {
	tenant, err := h.Tenants.ByID(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	var user *domain.User
	err = h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		u, lerr := h.Users.ByIDTx(ctx, tx, userID)
		if lerr != nil {
			return lerr
		}
		if u.TenantID != tenantID {
			return store.ErrNotFound
		}
		user = u
		return nil
	})
	return tenant, user, err
}

// lastAdminGuard refuses an operation that would leave a tenant with
// zero Active users holding the Tenant Super Admin role. Returns
// (true, message) when the operation should be blocked.
func (h *TenantHandler) lastAdminGuard(ctx context.Context, tenantID, victimID uuid.UUID) (bool, string) {
	var willStrand bool
	_ = h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Count active admins other than the victim.
		var count int
		err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM users u
			JOIN user_roles ur ON ur.user_id = u.id
			JOIN roles r       ON r.id = ur.role_id
			WHERE u.tenant_id = $1
			  AND u.id <> $2
			  AND u.status = 'active'
			  AND r.code = $3
		`, tenantID, victimID, tenantSuperAdminRole).Scan(&count)
		if err != nil {
			return err
		}
		willStrand = count == 0
		return nil
	})
	if !willStrand {
		return false, ""
	}
	return true, fmt.Sprintf("cannot perform this action: the tenant would be left with no active %s", tenantSuperAdminRole)
}

func (h *TenantHandler) auditUserAction(r *http.Request, tenantID, userID uuid.UUID, action string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &tenantID,
		ActorID:    nonZero(actorID),
		Action:     action,
		TargetKind: "user",
		TargetID:   userID.String(),
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Metadata:   meta,
	})
}

func writeUserErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrNotFound("user not found in this tenant"))
		return
	}
	httpx.WriteErr(w, r, err)
}
