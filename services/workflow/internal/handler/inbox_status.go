// GET /v1/inbox-status — small endpoint the Unified Inbox frontend
// hits to know whether the current tenant has been switched into
// consolidated mode. Drives the /cash-approvals deprecation banner
// + the "Open in Inbox →" link on legacy queue rows.
//
// Read-only; no body. Returns:
//
//   { "unified_inbox_enabled": true|false }

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
}

func (h *InboxStatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var enabled bool
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(),
			`SELECT COALESCE(unified_inbox_enabled, false) FROM tenants WHERE id = $1`,
			tenantID).Scan(&enabled)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, inboxStatusResponse{UnifiedInboxEnabled: enabled})
}
