// Tenant resolution middleware.
//
// Extracts the leftmost subdomain of the request host and looks it up
// in the tenants table. The bare apex and the reserved "platform" /
// "www" / "api" subdomains are treated as "no tenant" (platform context).
// Suspended/closed tenants are rejected.

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/store"
)

// Reserved subdomains that should NOT be resolved as a tenant.
var reservedSubdomains = map[string]struct{}{
	"":         {},
	"www":      {},
	"api":      {},
	"platform": {},
	"admin":    {},
	"app":      {},
}

// ResolveTenant injects the tenant (or leaves it nil for platform routes).
func ResolveTenant(tenants *store.TenantStore, appDomain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := extractTenantSlug(r.Host, appDomain)

			if slug == "" {
				// Platform context.
				next.ServeHTTP(w, r)
				return
			}

			t, err := tenants.BySlug(r.Context(), slug)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					httpx.WriteErr(w, r, httpx.ErrNotFound("tenant "+slug+" not found"))
					return
				}
				httpx.WriteErr(w, r, err)
				return
			}
			if t.Status != domain.TenantStatusActive {
				httpx.WriteErr(w, r, httpx.E(http.StatusForbidden, httpx.CodeForbidden, "tenant is "+string(t.Status)))
				return
			}
			ctx := WithTenant(r.Context(), t)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractTenantSlug pulls the tenant slug out of a Host header. Handles:
//   nexussacco.local                  -> ""           (apex / platform)
//   tujenge.nexussacco.local          -> "tujenge"
//   tujenge.nexussacco.local:5173     -> "tujenge"    (port stripped)
//   platform.nexussacco.local         -> ""           (reserved)
//
// Falls back to "" for anything unparseable rather than 500ing.
func extractTenantSlug(host, appDomain string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	appDomain = strings.ToLower(strings.TrimSpace(appDomain))
	if host == "" || host == appDomain {
		return ""
	}
	suffix := "." + appDomain
	if !strings.HasSuffix(host, suffix) {
		// Allow direct ip / localhost in dev — treated as platform.
		return ""
	}
	sub := strings.TrimSuffix(host, suffix)
	// Only take the leftmost label if there are multiple subdomains.
	if dot := strings.Index(sub, "."); dot != -1 {
		sub = sub[:dot]
	}
	if _, reserved := reservedSubdomains[sub]; reserved {
		return ""
	}
	return sub
}

// RequireTenant is mounted on routes that must run within a tenant.
func RequireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TenantFrom(r) == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("this endpoint requires a tenant subdomain"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePlatform is mounted on routes only valid on the platform host.
func RequirePlatform(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TenantFrom(r) != nil {
			httpx.WriteErr(w, r, httpx.ErrNotFound(""))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Silence unused import in some builds.
var _ = context.Background
