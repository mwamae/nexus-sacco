// Auth handlers: login, refresh, logout, /me, MFA challenge + verify,
// MFA enable / disable.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/email"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

const (
	mfaChallengeTTL = 10 * time.Minute
	mfaMaxAttempts  = 5
)

type AuthHandler struct {
	DB             *db.Pool
	Users          *store.UserStore
	Roles          *store.RoleStore
	Sessions       *store.SessionStore
	MFA            *store.MFAStore
	PasswordResets *store.PasswordResetStore
	Invites        *store.InviteStore
	Audit          *store.AuditStore
	Issuer         *auth.TokenIssuer
	RefreshTTL     time.Duration
	Logger         *slog.Logger
	Email          email.Sender

	// PasswordResetTTL controls how long a reset link is valid.
	PasswordResetTTL time.Duration
	// WebBaseURL template with {slug} placeholder, used in reset emails.
	WebBaseURL string

	// PlatformTenant is the pseudo-tenant that owns platform admins.
	// Resolved at startup; used when a login/refresh comes in on the
	// platform host (no tenant subdomain).
	PlatformTenant *domain.Tenant
}

// resolveLoginContext picks the tenant under which to authenticate.
// On a tenant subdomain we use that tenant; on the platform host we
// use the platform pseudo-tenant.
func (h *AuthHandler) resolveLoginContext(r *http.Request) (*domain.Tenant, bool) {
	if t := middleware.TenantFrom(r); t != nil {
		return t, false
	}
	if h.PlatformTenant == nil {
		return nil, false
	}
	return h.PlatformTenant, true
}

// ─────────── POST /v1/auth/login ───────────

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	AccessToken    string         `json:"access_token"`
	TokenType      string         `json:"token_type"`
	ExpiresAt      time.Time      `json:"expires_at"`
	RefreshToken   string         `json:"refresh_token"`
	RefreshExpires time.Time      `json:"refresh_expires_at"`
	User           *domain.User   `json:"user"`
	Tenant         *domain.Tenant `json:"tenant,omitempty"`
}

type mfaRequiredResponse struct {
	MFARequired   bool      `json:"mfa_required"`
	MFAToken      string    `json:"mfa_token"`
	MFAExpiresAt  time.Time `json:"mfa_expires_at"`
	Method        string    `json:"method"`
	DeliveryHint  string    `json:"delivery_hint"` // partially-masked destination
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	tenant, isPlatform := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("login must be performed on a tenant subdomain or platform host"))
		return
	}
	if tenant.Restrictions.UsersLocked {
		httpx.WriteErr(w, r, httpx.ErrForbidden("user logins are locked for this tenant"))
		return
	}

	var req loginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("email and password are required"))
		return
	}

	var (
		issued      *tokenResponse
		mfaChall    *mfaRequiredResponse
		failedUser  uuid.UUID
		successUser uuid.UUID
	)
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		uid, hash, err := h.Users.PasswordHashByEmailTx(r.Context(), tx, req.Email)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				_ = auth.VerifyPassword(req.Password, dummyHash)
				return httpx.ErrUnauthorized("invalid email or password")
			}
			return err
		}

		locked, err := h.Users.IsLockedTx(r.Context(), tx, uid)
		if err != nil {
			return err
		}
		if locked {
			return httpx.ErrForbidden("account locked due to repeated failed logins")
		}

		if err := auth.VerifyPassword(req.Password, hash); err != nil {
			failedUser = uid
			_ = h.Users.RecordFailedLoginTx(r.Context(), tx, uid)
			return httpx.ErrUnauthorized("invalid email or password")
		}

		user, err := h.Users.ByIDTx(r.Context(), tx, uid)
		if err != nil {
			return err
		}
		if user.Status != domain.UserStatusActive {
			return httpx.ErrForbidden("user is " + string(user.Status))
		}
		if isPlatform && !user.IsPlatformAdmin {
			return httpx.ErrForbidden("platform admin required")
		}

		mfaEnabled, mfaMethod, err := h.Users.MFAInfoTx(r.Context(), tx, user.ID)
		if err != nil {
			return err
		}

		if mfaEnabled && mfaMethod == "email" {
			// First factor passed; issue an MFA challenge instead of tokens.
			rawTok, tokHash, err := store.NewMFAToken()
			if err != nil {
				return err
			}
			code, codeHash, err := store.NewOTPCode()
			if err != nil {
				return err
			}
			expires := time.Now().Add(mfaChallengeTTL)
			if _, err := h.MFA.CreateTx(r.Context(), tx, store.CreateChallengeInput{
				TenantID:     user.TenantID,
				UserID:       user.ID,
				Purpose:      "login",
				MFATokenHash: tokHash,
				CodeHash:     codeHash,
				ExpiresAt:    expires,
				IP:           clientIP(r),
				UserAgent:    r.UserAgent(),
			}); err != nil {
				return err
			}

			// Send the code outside the transaction so SMTP latency
			// doesn't hold the row lock. Capture details now.
			mfaChall = &mfaRequiredResponse{
				MFARequired:  true,
				MFAToken:     rawTok,
				MFAExpiresAt: expires,
				Method:       "email",
				DeliveryHint: maskEmail(user.Email),
			}
			// Use closure capture; send happens after Commit below.
			go h.sendOTPEmail(user.Email, user.FullName, code, "login")
			successUser = user.ID
			return nil
		}

		// No MFA → issue tokens directly.
		issuedResp, err := h.issueTokensTxFull(r.Context(), tx, user, tenant, nil, r.UserAgent(), clientIP(r))
		if err != nil {
			return err
		}
		if err := h.Users.RecordLoginTx(r.Context(), tx, user.ID); err != nil {
			return err
		}
		issued = issuedResp
		successUser = user.ID
		return nil
	})
	if err != nil {
		if failedUser != uuid.Nil {
			_ = h.Audit.Write(r.Context(), store.AuditEntry{
				TenantID: &tenant.ID, ActorID: &failedUser,
				Action: "user.login.failed", IP: clientIP(r), UserAgent: r.UserAgent(),
			})
		}
		httpx.WriteErr(w, r, err)
		return
	}

	if mfaChall != nil {
		_ = h.Audit.Write(r.Context(), store.AuditEntry{
			TenantID: &tenant.ID, ActorID: &successUser,
			Action: "user.login.mfa_required", IP: clientIP(r), UserAgent: r.UserAgent(),
		})
		httpx.OK(w, mfaChall)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &issued.User.ID,
		Action: "user.login.success", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, issued)
}

// ─────────── POST /v1/auth/mfa/verify ───────────

type mfaVerifyRequest struct {
	MFAToken string `json:"mfa_token"`
	Code     string `json:"code"`
}

func (h *AuthHandler) MFAVerify(w http.ResponseWriter, r *http.Request) {
	tenant, _ := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("must be performed on a tenant or platform host"))
		return
	}

	var req mfaVerifyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.MFAToken == "" || req.Code == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("mfa_token and code are required"))
		return
	}

	var issued *tokenResponse
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		challenge, err := h.MFA.ByMFATokenHashTx(r.Context(), tx, store.HashMFAToken(req.MFAToken))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized("invalid mfa_token")
			}
			return err
		}
		if challenge.Purpose != "login" {
			return httpx.ErrUnauthorized("mfa_token has wrong purpose")
		}
		if challenge.UsedAt != nil {
			return httpx.ErrUnauthorized("mfa_token already used")
		}
		if challenge.ExpiresAt.Before(time.Now()) {
			return httpx.ErrUnauthorized("mfa_token expired")
		}
		if challenge.Attempts >= mfaMaxAttempts {
			return httpx.ErrUnauthorized("too many failed attempts")
		}

		expected := challenge.CodeHash
		got := store.HashOTPCode(req.Code)
		if !constantTimeEqual(expected, got) {
			n, _ := h.MFA.IncrementAttemptsTx(r.Context(), tx, challenge.ID)
			if n >= mfaMaxAttempts {
				return httpx.ErrUnauthorized("too many failed attempts; restart sign-in")
			}
			return httpx.ErrUnauthorized("invalid code")
		}

		if err := h.MFA.MarkUsedTx(r.Context(), tx, challenge.ID); err != nil {
			return err
		}

		user, err := h.Users.ByIDTx(r.Context(), tx, challenge.UserID)
		if err != nil {
			return err
		}
		if user.Status != domain.UserStatusActive {
			return httpx.ErrForbidden("user is " + string(user.Status))
		}

		issuedResp, err := h.issueTokensTxFull(r.Context(), tx, user, tenant, nil, r.UserAgent(), clientIP(r))
		if err != nil {
			return err
		}
		if err := h.Users.RecordLoginTx(r.Context(), tx, user.ID); err != nil {
			return err
		}
		issued = issuedResp
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &issued.User.ID,
		Action: "user.login.mfa_verified", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, issued)
}

// ─────────── POST /v1/auth/mfa/email/enable ───────────
//
// Sends a confirmation code to the user's email. The user submits it
// to /v1/auth/mfa/email/enable/confirm to flip mfa_enabled = true.

func (h *AuthHandler) MFAEnableStart(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	if tenant == nil {
		tenant = h.PlatformTenant
	}
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, ok := middleware.UserIDFrom(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}

	var resp mfaRequiredResponse
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		user, err := h.Users.ByIDTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		if user.MFAEnabled {
			return httpx.ErrConflict("MFA already enabled")
		}
		rawTok, tokHash, err := store.NewMFAToken()
		if err != nil {
			return err
		}
		code, codeHash, err := store.NewOTPCode()
		if err != nil {
			return err
		}
		expires := time.Now().Add(mfaChallengeTTL)
		if _, err := h.MFA.CreateTx(r.Context(), tx, store.CreateChallengeInput{
			TenantID:     user.TenantID,
			UserID:       user.ID,
			Purpose:      "enable_mfa",
			MFATokenHash: tokHash,
			CodeHash:     codeHash,
			ExpiresAt:    expires,
			IP:           clientIP(r),
			UserAgent:    r.UserAgent(),
		}); err != nil {
			return err
		}
		go h.sendOTPEmail(user.Email, user.FullName, code, "enable_mfa")
		resp = mfaRequiredResponse{
			MFARequired:  true,
			MFAToken:     rawTok,
			MFAExpiresAt: expires,
			Method:       "email",
			DeliveryHint: maskEmail(user.Email),
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── POST /v1/auth/mfa/email/enable/confirm ───────────

func (h *AuthHandler) MFAEnableConfirm(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	if tenant == nil {
		tenant = h.PlatformTenant
	}
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, ok := middleware.UserIDFrom(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}

	var req mfaVerifyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.MFAToken == "" || req.Code == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("mfa_token and code are required"))
		return
	}

	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		challenge, err := h.MFA.ByMFATokenHashTx(r.Context(), tx, store.HashMFAToken(req.MFAToken))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized("invalid mfa_token")
			}
			return err
		}
		if challenge.Purpose != "enable_mfa" || challenge.UserID != userID {
			return httpx.ErrUnauthorized("mfa_token does not match this user / purpose")
		}
		if challenge.UsedAt != nil {
			return httpx.ErrUnauthorized("mfa_token already used")
		}
		if challenge.ExpiresAt.Before(time.Now()) {
			return httpx.ErrUnauthorized("mfa_token expired")
		}
		if challenge.Attempts >= mfaMaxAttempts {
			return httpx.ErrUnauthorized("too many failed attempts")
		}

		got := store.HashOTPCode(req.Code)
		if !constantTimeEqual(challenge.CodeHash, got) {
			n, _ := h.MFA.IncrementAttemptsTx(r.Context(), tx, challenge.ID)
			if n >= mfaMaxAttempts {
				return httpx.ErrUnauthorized("too many failed attempts; restart")
			}
			return httpx.ErrUnauthorized("invalid code")
		}

		if err := h.MFA.MarkUsedTx(r.Context(), tx, challenge.ID); err != nil {
			return err
		}
		return h.Users.SetMFAEnabledTx(r.Context(), tx, userID, true, "email")
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &userID,
		Action: "user.mfa.enabled", IP: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"method": "email"},
	})
	httpx.OK(w, map[string]any{"mfa_enabled": true, "method": "email"})
}

// ─────────── POST /v1/auth/mfa/disable ───────────

type mfaDisableRequest struct {
	Password string `json:"password"`
}

func (h *AuthHandler) MFADisable(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	if tenant == nil {
		tenant = h.PlatformTenant
	}
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, ok := middleware.UserIDFrom(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}

	var req mfaDisableRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Password == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("password required to disable MFA"))
		return
	}

	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		hash, err := h.Users.PasswordHashByIDTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		if err := auth.VerifyPassword(req.Password, hash); err != nil {
			return httpx.ErrUnauthorized("invalid password")
		}
		return h.Users.SetMFAEnabledTx(r.Context(), tx, userID, false, "")
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &userID,
		Action: "user.mfa.disabled", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, map[string]any{"mfa_enabled": false})
}

// ─────────── POST /v1/auth/refresh ───────────

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	tenant, _ := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("refresh must be performed on a tenant subdomain or platform host"))
		return
	}
	if tenant.Restrictions.UsersLocked {
		httpx.WriteErr(w, r, httpx.ErrForbidden("user logins are locked for this tenant"))
		return
	}

	var req refreshRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.RefreshToken == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("refresh_token required"))
		return
	}

	hashed := auth.HashRefreshToken(req.RefreshToken)
	var issued *tokenResponse
	var (
		reuseDetected bool
		reuseRootID   uuid.UUID
		reuseUserID   uuid.UUID
	)

	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		rt, err := h.Sessions.ByHashTx(r.Context(), tx, hashed)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized("invalid refresh token")
			}
			return err
		}
		if rt.RevokedAt != nil {
			reuseDetected = true
			reuseRootID = rt.ID
			reuseUserID = rt.UserID
			return nil
		}
		if rt.ExpiresAt.Before(time.Now()) {
			return httpx.ErrUnauthorized("refresh token expired")
		}
		if rt.TenantID != tenant.ID {
			return httpx.ErrUnauthorized("refresh token tenant mismatch")
		}

		user, err := h.Users.ByIDTx(r.Context(), tx, rt.UserID)
		if err != nil {
			return err
		}
		if user.Status != domain.UserStatusActive {
			return httpx.ErrForbidden("user is " + string(user.Status))
		}
		issuedResp, err := h.issueTokensTxFull(r.Context(), tx, user, tenant, &rt.ID, r.UserAgent(), clientIP(r))
		if err != nil {
			return err
		}
		if err := h.Sessions.RevokeTx(r.Context(), tx, rt.ID, "rotated"); err != nil {
			return err
		}
		issued = issuedResp
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if reuseDetected {
		_ = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
			return h.Sessions.RevokeChainTx(r.Context(), tx, reuseRootID, "reuse_detected")
		})
		h.Logger.Warn("refresh token reuse — chain revoked",
			"user_id", reuseUserID, "tenant_id", tenant.ID, "token_id", reuseRootID,
		)
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("refresh token replay detected; all sessions revoked"))
		return
	}
	httpx.OK(w, issued)
}

// ─────────── POST /v1/auth/logout ───────────

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	tenant, _ := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.NoContent(w)
		return
	}
	var req refreshRequest
	_ = httpx.DecodeJSON(r, &req)
	if req.RefreshToken != "" {
		hashed := auth.HashRefreshToken(req.RefreshToken)
		_ = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
			rt, err := h.Sessions.ByHashTx(r.Context(), tx, hashed)
			if err != nil {
				return nil
			}
			return h.Sessions.RevokeTx(r.Context(), tx, rt.ID, "logout")
		})
	}
	httpx.NoContent(w)
}

// ─────────── GET /v1/auth/me ───────────

type meResponse struct {
	User        *domain.User   `json:"user"`
	Tenant      *domain.Tenant `json:"tenant,omitempty"`
	Roles       []string       `json:"roles"`
	Permissions []string       `json:"permissions"`
	// FeatureFlags / feature_flags removed in the Phase D drop —
	// reintroduce a flag bag here when the next per-tenant toggle
	// is needed.
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFrom(r)
	if claims == nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("malformed user id in token"))
		return
	}

	tenant := middleware.TenantFrom(r)
	platformContext := tenant == nil
	if tenant == nil {
		tenant = h.PlatformTenant
	}
	if tenant == nil {
		httpx.OK(w, meResponse{
			User: &domain.User{
				ID: userID, Email: claims.Email, FullName: claims.FullName,
				IsPlatformAdmin: claims.IsPlatformAdmin,
			},
			Roles:       claims.Roles,
			Permissions: claims.Permissions,
		})
		return
	}

	var resp meResponse
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		user, err := h.Users.ByIDTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		roles, err := h.Roles.RolesForUserTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		perms, err := h.Roles.PermissionsForUserTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		respTenant := tenant
		if claims.IsPlatformAdmin && platformContext {
			respTenant = nil
		}
		resp = meResponse{User: user, Tenant: respTenant, Roles: roles, Permissions: perms}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── POST /v1/auth/password/forgot ───────────
//
// Always returns 204, regardless of whether the email belongs to a real
// user, to avoid leaking account existence. Real users receive an email
// with a reset link.

type forgotRequest struct {
	Email string `json:"email"`
}

func (h *AuthHandler) PasswordForgot(w http.ResponseWriter, r *http.Request) {
	tenant, isPlatform := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("must be performed on a tenant subdomain or platform host"))
		return
	}

	var req forgotRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("email is required"))
		return
	}

	// Resolve user; if missing or not eligible, still return 204 silently.
	var (
		user  *domain.User
		token string
	)
	_ = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		u, err := h.Users.ByEmailTx(r.Context(), tx, req.Email)
		if err != nil {
			return nil
		}
		if u.Status != domain.UserStatusActive {
			return nil
		}
		// On the platform host only platform admins can reset.
		if isPlatform && !u.IsPlatformAdmin {
			return nil
		}
		raw, hash, err := store.NewResetToken()
		if err != nil {
			return err
		}
		if err := h.PasswordResets.CreateTx(r.Context(), tx, store.CreatePasswordResetInput{
			TenantID:  u.TenantID,
			UserID:    u.ID,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(h.PasswordResetTTL),
		}); err != nil {
			return err
		}
		user = u
		token = raw
		return nil
	})

	if user != nil && token != "" {
		slug := tenant.Slug
		// Build the reset URL outside the transaction.
		resetURL := strings.ReplaceAll(h.WebBaseURL, "{slug}", slug) + "/reset?token=" + token
		go h.sendPasswordResetEmail(user.Email, user.FullName, resetURL)
		_ = h.Audit.Write(r.Context(), store.AuditEntry{
			TenantID: &tenant.ID, ActorID: &user.ID,
			Action: "user.password.reset_requested", IP: clientIP(r), UserAgent: r.UserAgent(),
		})
	}

	httpx.NoContent(w)
}

// IssuePasswordResetFor generates a fresh password-reset token for an
// arbitrary user (no email lookup), stores it, revokes the user's
// active sessions, and emails the reset link. Used by the
// platform-admin "force password reset" action — the platform admin
// never sees the token, the user follows the link from their email.
func (h *AuthHandler) IssuePasswordResetFor(ctx context.Context, tenant *domain.Tenant, user *domain.User) error {
	var rawToken string
	err := h.DB.WithTenantTx(ctx, tenant.ID, func(tx pgx.Tx) error {
		raw, hash, err := store.NewResetToken()
		if err != nil {
			return err
		}
		if err := h.PasswordResets.CreateTx(ctx, tx, store.CreatePasswordResetInput{
			TenantID:  user.TenantID,
			UserID:    user.ID,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(h.PasswordResetTTL),
		}); err != nil {
			return err
		}
		// Invalidate active sessions so any existing browser tab is
		// kicked out immediately — spec requires this on force reset.
		if h.Sessions != nil {
			if err := h.Sessions.RevokeAllForUserTx(ctx, tx, user.ID, "force_password_reset"); err != nil {
				return err
			}
		}
		rawToken = raw
		return nil
	})
	if err != nil {
		return err
	}
	resetURL := strings.ReplaceAll(h.WebBaseURL, "{slug}", tenant.Slug) + "/reset?token=" + rawToken
	go h.sendPasswordResetEmail(user.Email, user.FullName, resetURL)
	return nil
}

// ─────────── POST /v1/auth/password/reset ───────────

type resetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (h *AuthHandler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	tenant, _ := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("must be performed on a tenant subdomain or platform host"))
		return
	}

	var req resetRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Token == "" || req.NewPassword == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("token and new_password are required"))
		return
	}
	if len(req.NewPassword) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_password must be at least 12 characters"))
		return
	}

	var userID uuid.UUID
	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		pr, err := h.PasswordResets.ByTokenHashTx(r.Context(), tx, store.HashResetToken(req.Token))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized("invalid or expired reset token")
			}
			return err
		}
		if pr.UsedAt != nil {
			return httpx.ErrUnauthorized("reset token already used")
		}
		if pr.ExpiresAt.Before(time.Now()) {
			return httpx.ErrUnauthorized("reset token expired")
		}
		if pr.TenantID != tenant.ID {
			return httpx.ErrUnauthorized("reset token tenant mismatch")
		}

		hash, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			return err
		}
		if err := h.Users.UpdatePasswordHashTx(r.Context(), tx, pr.UserID, hash); err != nil {
			return err
		}
		if err := h.PasswordResets.MarkUsedTx(r.Context(), tx, pr.ID); err != nil {
			return err
		}
		// Burn any other outstanding reset links for this user.
		if err := h.PasswordResets.InvalidateOutstandingTx(r.Context(), tx, pr.UserID); err != nil {
			return err
		}
		// Revoke every active refresh token — known sessions are kicked.
		if err := h.Sessions.RevokeAllForUserTx(r.Context(), tx, pr.UserID, "password_reset"); err != nil {
			return err
		}
		userID = pr.UserID
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &userID,
		Action: "user.password.reset_completed", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, map[string]any{"ok": true})
}

// ─────────── POST /v1/auth/password/change ───────────
//
// Authenticated. Requires the current password as a re-confirmation.
// All other sessions are revoked on success.

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *AuthHandler) PasswordChange(w http.ResponseWriter, r *http.Request) {
	tenant := middleware.TenantFrom(r)
	if tenant == nil {
		tenant = h.PlatformTenant
	}
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("no tenant context"))
		return
	}
	userID, ok := middleware.UserIDFrom(r)
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("current_password and new_password are required"))
		return
	}
	if len(req.NewPassword) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_password must be at least 12 characters"))
		return
	}
	if req.CurrentPassword == req.NewPassword {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_password must differ from current_password"))
		return
	}

	err := h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		current, err := h.Users.PasswordHashByIDTx(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		if err := auth.VerifyPassword(req.CurrentPassword, current); err != nil {
			return httpx.ErrUnauthorized("current password is incorrect")
		}
		newHash, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			return err
		}
		if err := h.Users.UpdatePasswordHashTx(r.Context(), tx, userID, newHash); err != nil {
			return err
		}
		return h.Sessions.RevokeAllForUserTx(r.Context(), tx, userID, "password_changed")
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &userID,
		Action: "user.password.changed", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, map[string]any{"ok": true})
}

// ─────────── POST /v1/auth/invite/accept ───────────
//
// Public. Consumes a single-use invite token, sets the user's password,
// and flips them from pending → active. Replays are rejected.

type inviteAcceptRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (h *AuthHandler) InviteAccept(w http.ResponseWriter, r *http.Request) {
	// The invite link lives at {slug}.nexussacco.local/invite/accept?token=…
	// so the subdomain in the request identifies the tenant. Platform admin
	// invites go to platform.nexussacco.local which resolves to the
	// platform pseudo-tenant.
	tenant, _ := h.resolveLoginContext(r)
	if tenant == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("must be performed on a tenant subdomain or platform host"))
		return
	}

	var req inviteAcceptRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if req.Token == "" || req.NewPassword == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("token and new_password are required"))
		return
	}
	if len(req.NewPassword) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("new_password must be at least 12 characters"))
		return
	}

	pwHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Single tx: validate invite → activate user → mark invite used →
	// invalidate other outstanding invites → issue session tokens. The
	// auto-login means the frontend doesn't need a separate login step
	// after the user sets their password.
	var (
		userID uuid.UUID
		tokens *tokenResponse
	)
	err = h.DB.WithTenantTx(r.Context(), tenant.ID, func(tx pgx.Tx) error {
		inv, err := h.Invites.ByTokenHashTx(r.Context(), tx, store.HashInviteToken(req.Token))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized("invalid or expired invite")
			}
			return err
		}
		if inv.AcceptedAt != nil {
			return httpx.ErrUnauthorized("invite already accepted")
		}
		if inv.ExpiresAt.Before(time.Now()) {
			return httpx.ErrUnauthorized("invite expired")
		}
		if inv.TenantID != tenant.ID {
			return httpx.ErrUnauthorized("invite tenant mismatch")
		}
		if err := h.Users.ActivateWithPasswordTx(r.Context(), tx, inv.UserID, pwHash); err != nil {
			return err
		}
		if err := h.Invites.MarkAcceptedTx(r.Context(), tx, inv.ID); err != nil {
			return err
		}
		if err := h.Invites.InvalidateOutstandingTx(r.Context(), tx, inv.UserID); err != nil {
			return err
		}
		userID = inv.UserID
		// Load the freshly-activated user and issue tokens in the same tx.
		u, err := h.Users.ByIDTx(r.Context(), tx, inv.UserID)
		if err != nil {
			return err
		}
		t, err := h.issueTokensTxFull(r.Context(), tx, u, tenant, nil, r.UserAgent(), clientIP(r))
		if err != nil {
			return err
		}
		tokens = t
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenant.ID, ActorID: &userID,
		Action: "user.invite_accepted", IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	httpx.OK(w, map[string]any{
		"ok":     true,
		"tokens": tokens,
		"redirect": "/", // dashboard — the frontend can override based on first-login UX
	})
}

// ─────────── Helpers ───────────

func (h *AuthHandler) sendPasswordResetEmail(to, displayName, resetURL string) {
	if h.Email == nil || !h.Email.Enabled() {
		h.Logger.Warn("SMTP not configured — password reset URL not sent", "to", to, "url", resetURL)
		return
	}
	msg := email.PasswordResetMessage(to, displayName, resetURL, int(h.PasswordResetTTL/time.Minute))
	if err := h.Email.Send(msg); err != nil {
		h.Logger.Error("send password reset email", "to", to, "err", err)
	}
}

func (h *AuthHandler) issueTokensTx(ctx context.Context, tx pgx.Tx, user *domain.User, tenant *domain.Tenant) (*tokenResponse, error) {
	return h.issueTokensTxFull(ctx, tx, user, tenant, nil, "", "")
}

func (h *AuthHandler) issueTokensTxWithParent(ctx context.Context, tx pgx.Tx, user *domain.User, tenant *domain.Tenant, parent *uuid.UUID) (*tokenResponse, error) {
	return h.issueTokensTxFull(ctx, tx, user, tenant, parent, "", "")
}

func (h *AuthHandler) issueTokensTxFull(ctx context.Context, tx pgx.Tx, user *domain.User, tenant *domain.Tenant, parent *uuid.UUID, ua, ip string) (*tokenResponse, error) {
	roles, err := h.Roles.RolesForUserTx(ctx, tx, user.ID)
	if err != nil {
		return nil, err
	}
	perms, err := h.Roles.PermissionsForUserTx(ctx, tx, user.ID)
	if err != nil {
		return nil, err
	}
	access, accessExp, err := h.Issuer.Issue(auth.AccessClaims{
		TenantID:        user.TenantID.String(),
		TenantSlug:      tenant.Slug,
		UserID:          user.ID.String(),
		Email:           user.Email,
		FullName:        user.FullName,
		Roles:           roles,
		Permissions:     perms,
		IsPlatformAdmin: user.IsPlatformAdmin,
	})
	if err != nil {
		return nil, err
	}
	rawRefresh, hashed, err := auth.NewRefreshToken()
	if err != nil {
		return nil, err
	}
	refreshExp := time.Now().Add(h.RefreshTTL)
	if _, err := h.Sessions.CreateTx(ctx, tx, store.CreateRefreshInput{
		TenantID:  user.TenantID,
		UserID:    user.ID,
		TokenHash: hashed,
		ParentID:  parent,
		UserAgent: ua,
		IP:        ip,
		ExpiresAt: refreshExp,
	}); err != nil {
		return nil, err
	}
	return &tokenResponse{
		AccessToken:    access,
		TokenType:      "Bearer",
		ExpiresAt:      accessExp,
		RefreshToken:   rawRefresh,
		RefreshExpires: refreshExp,
		User:           user,
		Tenant:         tenant,
	}, nil
}

func (h *AuthHandler) sendOTPEmail(to, displayName, code, purpose string) {
	if h.Email == nil || !h.Email.Enabled() {
		// Fallback for dev: log the code so the operator can read it
		// when SMTP isn't configured. Never enable in prod.
		h.Logger.Warn("SMTP not configured — OTP not sent", "to", to, "code", code, "purpose", purpose)
		return
	}
	msg := email.OTPMessage(to, displayName, code, purpose, int(mfaChallengeTTL/time.Minute))
	if err := h.Email.Send(msg); err != nil {
		h.Logger.Error("send OTP email", "to", to, "purpose", purpose, "err", err)
	}
}

// constantTimeEqual is a small wrapper to keep the comparison readable.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// maskEmail returns a privacy-safe hint like "ow****@tujenge.test".
func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return "***"
	}
	user := e[:at]
	if len(user) <= 2 {
		return user[:1] + "*" + e[at:]
	}
	return user[:2] + strings.Repeat("*", min(4, len(user)-2)) + e[at:]
}

// A stable, well-formed argon2id hash used to spend CPU on misses.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$Mzc4NjJiMzEwNzgyMjFkOQ$JtQ7gqDz8z7+5XmW2bn+9xtq6h6vTzCp+y8Xp1IIv2k"

func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		first := h
		if i := strings.Index(h, ","); i != -1 {
			first = h[:i]
		}
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip.String()
		}
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		if ip := net.ParseIP(strings.TrimSpace(h)); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}
