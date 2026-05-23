// Member ledger handler — unified timeline endpoint.
// Cursor-paginated by `before` (RFC3339 timestamp), default page size 50.

package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type MemberLedgerHandler struct {
	DB     *db.Pool
	Ledger *store.MemberLedgerStore
	Logger *slog.Logger
}

// Get — GET /v1/members/{counterparty_id}/ledger?limit=50&before=2026-05-22T00:00:00Z
func (h *MemberLedgerHandler) Get(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, perr := time.Parse(time.RFC3339Nano, v)
		if perr != nil {
			t, perr = time.Parse(time.RFC3339, v)
		}
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("before must be RFC3339"))
			return
		}
		before = t
	}

	tid, _ := middleware.TenantIDFrom(r)
	var page *store.LedgerPage
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var qerr error
		page, qerr = h.Ledger.ListMemberLedgerTx(r.Context(), tx, memberID, before, limit)
		return qerr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, page)
}
