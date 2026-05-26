// Staff-facing read surface for the "recent inbound traffic" panel
// in the Settings UI. JWT-authed under /v1/mpesa/c2b/events; runs in
// the caller's tenant scope.
//
// Filter shape matches the directory page the UI is expected to grow
// later: paybill, msisdn (exact), bill_ref (exact), status, and an
// optional received_at window. Paging via limit/offset (page size
// capped at 200).

package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/httpx"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/store"
)

type InboundEventsHandler struct {
	DB     *db.Pool
	Events *store.InboundEventStore
}

func (h *InboundEventsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()

	in := store.ListInboundInput{
		MSISDN:  strings.TrimSpace(q.Get("msisdn")),
		BillRef: strings.TrimSpace(q.Get("bill_ref")),
	}
	if v := q.Get("paybill_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid paybill_id"))
			return
		}
		in.PaybillID = &id
	}
	if v := q.Get("status"); v != "" {
		switch domain.InboundStatus(v) {
		case domain.InboundReceived, domain.InboundDistributed, domain.InboundFailed:
			in.Status = domain.InboundStatus(v)
		default:
			httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be received, distributed, or failed"))
			return
		}
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("from must be RFC3339"))
			return
		}
		in.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("to must be RFC3339"))
			return
		}
		in.To = &t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("limit must be a positive integer"))
			return
		}
		in.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("offset must be a non-negative integer"))
			return
		}
		in.Offset = n
	}

	var res *store.ListInboundResult
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		res, err = h.Events.ListTx(r.Context(), tx, in)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"events": res.Events,
		"total":  res.Total,
		"limit":  in.Limit,
		"offset": in.Offset,
	})
}
