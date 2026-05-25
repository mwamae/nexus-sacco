// /v1/finance/posting-outbox — read + replay surface for the
// posting outbox. Operators use this to spot rows where the
// dispatcher has retried >= 3 times without landing, and to
// re-arm any row whose underlying cause has been fixed.

package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type PostingOutboxHandler struct {
	DB     *db.Pool
	Outbox *store.PostingOutboxStore
}

// ListStuck handles GET /v1/finance/posting-outbox?status=stuck&limit=N.
// Only the "stuck" status is implemented today — it's the actionable
// view. A future iteration may add ?status=all for a forensic
// browse, but for ops use stuck-first is the right starting shape.
func (h *PostingOutboxHandler) ListStuck(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "stuck" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("only status=stuck is supported"))
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out []store.PostingOutboxRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Outbox.ListStuckTx(r.Context(), tx, limit)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []store.PostingOutboxRow{}
	}
	httpx.OK(w, map[string]any{"rows": out})
}

// Replay handles POST /v1/finance/posting-outbox/{id}/replay. Resets
// the row's retry counter so the dispatcher picks it up on the next
// poll. Idempotent: replaying a never-failed row is a no-op +
// returns the row unchanged (well, attempts is already 0). Replaying
// an already-dispatched row returns 409.
func (h *PostingOutboxHandler) Replay(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.PostingOutboxRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		row, rerr := h.Outbox.ReplayTx(r.Context(), tx, id)
		if rerr != nil {
			return rerr
		}
		out = row
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrConflict("row not found or already dispatched"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}
