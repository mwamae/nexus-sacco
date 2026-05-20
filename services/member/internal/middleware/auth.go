// JWT bearer auth + permission gating. Same shape as the identity
// service — platform-admin tokens can operate on any tenant subdomain;
// other tokens must match the resolved tenant.

package middleware

import (
	"net/http"
	"strings"

	"github.com/nexussacco/member/internal/auth"
	"github.com/nexussacco/member/internal/httpx"
)

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
			if !claims.IsPlatformAdmin {
				tid, ok := TenantIDFrom(r)
				if !ok {
					httpx.WriteErr(w, r, httpx.ErrForbidden("token requires tenant context"))
					return
				}
				if claims.TenantID != tid.String() {
					httpx.WriteErr(w, r, httpx.ErrForbidden("token does not belong to this tenant"))
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}

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
