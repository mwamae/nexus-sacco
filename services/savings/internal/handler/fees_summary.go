// GET /v1/reports/fees-summary — Fees & Collections Summary.
//
// Operational report for the finance team: what fees were collected,
// by code and by channel, in a window, and which GL income accounts
// they hit. The same endpoint backs the Member Profile → Fees tab
// (via ?counterparty_id=).
//
// Filters (all optional except from/to):
//   from, to               YYYY-MM-DD inclusive window on receipts.value_date
//   channel                receipt_channel ('cash','mpesa','airtel_money',…)
//   fee_code               filter to a single fee_catalog code
//   counterparty_id        scope to one member (Member Profile tab)
//
// "Unposted" surfaces drift independently of the window — see the
// store-layer comment.

package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type FeesSummaryHandler struct {
	DB    *db.Pool
	Store *store.FeesSummaryStore
}

func (h *FeesSummaryHandler) Summary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")
	if fromStr == "" || toStr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("from and to are required (YYYY-MM-DD)"))
		return
	}
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("from must be YYYY-MM-DD"))
		return
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to must be YYYY-MM-DD"))
		return
	}
	if to.Before(from) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to must be on or after from"))
		return
	}
	f := store.FeesSummaryFilter{
		From:    from,
		To:      to,
		Channel: q.Get("channel"),
		FeeCode: q.Get("fee_code"),
	}
	if v := q.Get("counterparty_id"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id must be uuid"))
			return
		}
		f.CounterpartyID = id
	}

	tid, _ := middleware.TenantIDFrom(r)
	var out *store.FeesSummary
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var serr error
		out, serr = h.Store.SummaryTx(r.Context(), tx, f)
		return serr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}
