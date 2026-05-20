// Audit-log lookup.
//
//   GET /v1/audit/by-target/{kind}/{id}?limit=N
//
// Returns audit entries for (target_kind, target_id), newest first.
// Permission: audit:view. Tenant filtering is applied automatically —
// non-platform tokens see only their own tenant's events.

package handler

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type AuditHandler struct {
	Audit  *store.AuditStore
	Logger *slog.Logger
}

func (h *AuditHandler) ByTarget(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	rawID, err := url.PathUnescape(chi.URLParam(r, "id"))
	if err != nil || rawID == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid target id"))
		return
	}
	if kind == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind is required"))
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	// Tenant-scope the query. Platform admins (no tenant in context, or
	// IsPlatformAdmin claim) get cross-tenant visibility; everyone else
	// only sees their own tenant's events.
	var tenantFilter *uuid.UUID
	if t := middleware.TenantFrom(r); t != nil {
		id := t.ID
		tenantFilter = &id
	}
	claims := middleware.ClaimsFrom(r)
	if claims != nil && claims.IsPlatformAdmin {
		tenantFilter = nil
	}

	entries, err := h.Audit.ByTarget(r.Context(), tenantFilter, kind, rawID, limit)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []*store.AuditEntryRead{}
	}
	httpx.OK(w, map[string]any{
		"entries":     entries,
		"target_kind": kind,
		"target_id":   rawID,
		"limit":       limit,
	})
}
