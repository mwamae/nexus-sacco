// Request-scoped context keys for tenant + auth claims.

package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/nexussacco/notification/internal/auth"
)

type ctxKey int

const (
	keyTenantID ctxKey = iota
	keyTenantSlug
	keyClaims
	keyRequestID
)

func WithTenant(ctx context.Context, id uuid.UUID, slug string) context.Context {
	ctx = context.WithValue(ctx, keyTenantID, id)
	ctx = context.WithValue(ctx, keyTenantSlug, slug)
	return ctx
}

func TenantIDFrom(r *http.Request) (uuid.UUID, bool) {
	v, ok := r.Context().Value(keyTenantID).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

func TenantSlugFrom(r *http.Request) string {
	v, _ := r.Context().Value(keyTenantSlug).(string)
	return v
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
