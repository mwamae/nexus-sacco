// Member 360° statement handler. Pulls the consolidated view of every
// financial relationship a member has with the SACCO.

package handler

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type MemberStatementHandler struct {
	DB         *db.Pool
	Statements *store.MemberStatementStore
	Logger     *slog.Logger
}

// Get — GET /v1/members/{member_id}/statement
func (h *MemberStatementHandler) Get(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "member_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var stmt *store.MemberStatement
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		stmt, err = h.Statements.BuildTx(r.Context(), tx, memberID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, stmt)
}
