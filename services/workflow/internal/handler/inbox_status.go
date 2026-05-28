// GET /v1/inbox-status — small endpoint the Unified Inbox frontend
// hits to know whether the current tenant has been switched into
// consolidated mode. Drives the /cash-approvals deprecation banner
// + the "Open in Inbox →" link on legacy queue rows.
//
// Read-only; no body. Returns:
//
//   { "unified_inbox_enabled": true|false, "bosa_fosa_enabled": true|false }
//
// The bosa_fosa_enabled flag was bolted on rather than given its own
// endpoint to avoid an extra round-trip on app boot — the frontend
// already fetches inbox status on cold start, and adding more
// per-flag endpoints would multiply that cost. Reads from the
// matching boolean columns on `tenants` (identity migrations 0020
// and 0021).

package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/workflow/internal/db"
	"github.com/nexussacco/workflow/internal/httpx"
	"github.com/nexussacco/workflow/internal/middleware"
)

type InboxStatusHandler struct {
	DB *db.Pool
}

type inboxStatusResponse struct {
	UnifiedInboxEnabled bool `json:"unified_inbox_enabled"`
	BOSAFOSAEnabled     bool `json:"bosa_fosa_enabled"`
}

func (h *InboxStatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// unified_inbox_enabled is hard-pinned true post the unified-
	// approvals migration: every cash kind routes through the workflow
	// engine unconditionally now, and every approval surface in the UI
	// behaves as if the flag is on. The column on `tenants` is
	// preserved one release for a panic-revert path (set the column
	// to false on a single tenant to ride the legacy code branches
	// that still exist in the per-service handlers), and is removed
	// in the next major release together with those dead branches.
	//
	// bosa_fosa_enabled remains a per-tenant read because that flag
	// gates a separate rollout (deposit accounts segmentation) and
	// hasn't been universally turned on yet.
	var bosaFosaEnabled bool
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(),
			`SELECT COALESCE(bosa_fosa_enabled, false) FROM tenants WHERE id = $1`,
			tenantID).Scan(&bosaFosaEnabled)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, inboxStatusResponse{
		UnifiedInboxEnabled: true,
		BOSAFOSAEnabled:     bosaFosaEnabled,
	})
}
