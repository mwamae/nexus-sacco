// DSID Phase 2.2 — admin CRUD for per-product recurring fees.
//
//   GET    /v1/deposit-products/{product_id}/recurring-fees
//   POST   /v1/deposit-products/{product_id}/recurring-fees
//   PATCH  /v1/deposit-product-recurring-fees/{id}
//   DELETE /v1/deposit-product-recurring-fees/{id}

package handler

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type RecurringFeesHandler struct {
	DB   *db.Pool
	Fees *store.RecurringFeeStore
}

func (h *RecurringFeesHandler) List(w http.ResponseWriter, r *http.Request) {
	productID, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.RecurringFee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.Fees.ListByProductTx(r.Context(), tx, productID)
		if err != nil {
			return err
		}
		items = l
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

type createFeeReq struct {
	FeeKind      string          `json:"fee_kind"`
	Amount       decimal.Decimal `json:"amount"`
	Frequency    string          `json:"frequency"`
	GLCreditCode string          `json:"gl_credit_code"`
	StartsOn     string          `json:"starts_on,omitempty"`
	EndsOn       string          `json:"ends_on,omitempty"`
	Notes        string          `json:"notes,omitempty"`
}

func (h *RecurringFeesHandler) Create(w http.ResponseWriter, r *http.Request) {
	productID, err := parseUUIDParam(r, "product_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in createFeeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.FeeKind == "" || in.Amount.LessThanOrEqual(decimal.Zero) || in.GLCreditCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fee_kind, amount, gl_credit_code required"))
		return
	}
	if !validRecurringFreq(in.Frequency) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("frequency must be monthly|quarterly|annual"))
		return
	}
	startsOn := time.Now().UTC()
	if in.StartsOn != "" {
		t, perr := time.Parse("2006-01-02", in.StartsOn)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("starts_on must be YYYY-MM-DD"))
			return
		}
		startsOn = t
	}
	var endsOn *time.Time
	if in.EndsOn != "" {
		t, perr := time.Parse("2006-01-02", in.EndsOn)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("ends_on must be YYYY-MM-DD"))
			return
		}
		endsOn = &t
	}

	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var out *store.RecurringFee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		f, err := h.Fees.CreateTx(r.Context(), tx, store.CreateRecurringFeeInput{
			TenantID:     tid,
			ProductID:    productID,
			FeeKind:      in.FeeKind,
			Amount:       in.Amount,
			Frequency:    in.Frequency,
			GLCreditCode: in.GLCreditCode,
			StartsOn:     startsOn,
			EndsOn:       endsOn,
			Notes:        in.Notes,
			CreatedBy:    uid,
		})
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

type patchFeeReq struct {
	Amount       *decimal.Decimal `json:"amount,omitempty"`
	GLCreditCode *string          `json:"gl_credit_code,omitempty"`
	Active       *bool            `json:"active,omitempty"`
	EndsOn       *string          `json:"ends_on,omitempty"`
	Notes        *string          `json:"notes,omitempty"`
}

func (h *RecurringFeesHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in patchFeeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	upd := store.UpdateRecurringFeeInput{
		ID:           id,
		Amount:       in.Amount,
		GLCreditCode: in.GLCreditCode,
		Active:       in.Active,
		Notes:        in.Notes,
	}
	if in.EndsOn != nil {
		if *in.EndsOn == "" {
			var zero time.Time
			upd.EndsOn = &zero
		} else {
			t, perr := time.Parse("2006-01-02", *in.EndsOn)
			if perr != nil {
				httpx.WriteErr(w, r, httpx.ErrBadRequest("ends_on must be YYYY-MM-DD"))
				return
			}
			upd.EndsOn = &t
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.RecurringFee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		f, err := h.Fees.UpdateTx(r.Context(), tx, upd)
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *RecurringFeesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Fees.DeleteTx(r.Context(), tx, id)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func validRecurringFreq(s string) bool {
	switch s {
	case "monthly", "quarterly", "annual":
		return true
	}
	return false
}
