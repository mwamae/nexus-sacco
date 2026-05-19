// Context keys + helpers — keep them in one place so the rest of the
// codebase can pull tenant/user info out of *http.Request safely.

package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/domain"
)

type ctxKey int

const (
	keyTenant ctxKey = iota
	keyClaims
	keyRequestID
)

func WithTenant(ctx context.Context, t *domain.Tenant) context.Context {
	return context.WithValue(ctx, keyTenant, t)
}

func TenantFrom(r *http.Request) *domain.Tenant {
	v, _ := r.Context().Value(keyTenant).(*domain.Tenant)
	return v
}

// PlatformContextOnly returns true when the request came in on the
// reserved platform subdomain — used to gate platform admin routes.
func IsPlatformRequest(r *http.Request) bool {
	t := TenantFrom(r)
	return t == nil
}

func WithClaims(ctx context.Context, c *auth.AccessClaims) context.Context {
	return context.WithValue(ctx, keyClaims, c)
}

func ClaimsFrom(r *http.Request) *auth.AccessClaims {
	v, _ := r.Context().Value(keyClaims).(*auth.AccessClaims)
	return v
}

func UserIDFrom(r *http.Request) (uuid.UUID, bool) {
	c := ClaimsFrom(r)
	if c == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(c.UserID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

func RequestIDFrom(r *http.Request) string {
	v, _ := r.Context().Value(keyRequestID).(string)
	return v
}
