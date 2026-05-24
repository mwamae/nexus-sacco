// Virtual-till read endpoints.
//
//   GET /v1/virtual-tills          list every virtual till for the tenant
//
// Used by the admin frontend's TillLabel resolver — there are at most a
// handful of virtual tills per tenant (one per non-cash channel), so a
// single list + module-level cache is cheaper than a per-id getter.

package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type VirtualTillHandler struct {
	DB    *db.Pool
	Tills *store.VirtualTillStore
}

func (h *VirtualTillHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out []domain.VirtualTill
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		v, err := h.Tills.ListTx(r.Context(), tx)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.VirtualTill{}
	}
	httpx.OK(w, map[string]any{"items": out})
}
