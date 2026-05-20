// Tenant resolution from subdomain. Member service ALWAYS operates
// inside a tenant — no platform-only routes — so the middleware fails
// closed if it can't pick one.
//
// Resolution order:
//   1. Leftmost subdomain of Host (if not "platform"/"www"/"api"/etc)
//   2. X-Tenant-Slug header (handy for dev / scripts)
// The X-Tenant-Slug header is only honored for platform-admin tokens
// to prevent cross-tenant escapes from a non-admin caller; it's checked
// inside Authenticated below.

package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/store"
)

var reservedSubdomains = map[string]struct{}{
	"":         {},
	"www":      {},
	"api":      {},
	"platform": {},
	"admin":    {},
	"app":      {},
}

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
		return ""
	}
	sub := strings.TrimSuffix(host, suffix)
	if dot := strings.Index(sub, "."); dot != -1 {
		sub = sub[:dot]
	}
	if _, reserved := reservedSubdomains[sub]; reserved {
		return ""
	}
	return sub
}

func ResolveTenant(tenants *store.TenantStore, appDomain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := extractTenantSlug(r.Host, appDomain)
			if slug == "" {
				slug = strings.ToLower(strings.TrimSpace(r.Header.Get("X-Tenant-Slug")))
			}
			if slug == "" {
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
			if t.Status != "active" {
				httpx.WriteErr(w, r, httpx.E(http.StatusForbidden, httpx.CodeForbidden, "tenant is "+t.Status))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), t.ID, t.Slug)))
		})
	}
}

// RequireTenant ensures a tenant resolved for the request.
func RequireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := TenantIDFrom(r); !ok {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("this endpoint requires a tenant subdomain"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
