// JWT authentication + permission gates.

package middleware

import (
	"net/http"
	"strings"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/httpx"
)

// Authenticated requires a valid Bearer access token and binds its
// claims onto the request context. It also verifies that the token's
// tenant matches the subdomain-resolved tenant (or that the token
// belongs to a platform admin on a platform route).
func Authenticated(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
			if raw == "" {
				httpx.WriteErr(w, r, httpx.ErrUnauthorized("missing bearer token"))
				return
			}
			claims, err := issuer.Parse(raw)
			if err != nil {
				httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid token: "+err.Error()))
				return
			}
			// Tenant binding: if the request resolved a tenant, the token
			// must be for that tenant. If it didn't (platform host), the
			// token must be a platform-admin token.
			if t := TenantFrom(r); t != nil {
				if claims.TenantID != t.ID.String() {
					httpx.WriteErr(w, r, httpx.ErrForbidden("token does not belong to this tenant"))
					return
				}
			} else if !claims.IsPlatformAdmin {
				httpx.WriteErr(w, r, httpx.ErrForbidden("platform admin required"))
				return
			}
			ctx := WithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePermission gates a handler on a permission code carried in
// the access token. Platform admins bypass.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := ClaimsFrom(r)
			if c == nil {
				httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
				return
			}
			if c.IsPlatformAdmin {
				next.ServeHTTP(w, r)
				return
			}
			for _, p := range c.Permissions {
				if p == perm {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.WriteErr(w, r, httpx.ErrForbidden("missing permission: "+perm))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
