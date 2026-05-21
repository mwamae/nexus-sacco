// Report endpoints — Trial Balance + GL detail per account.
//
//   GET /v1/reports/trial-balance?from=YYYY-MM-DD&to=YYYY-MM-DD
//   GET /v1/reports/gl-detail/{account_id}?from=YYYY-MM-DD&to=YYYY-MM-DD&limit=1000

package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
	"github.com/shopspring/decimal"
)

type ReportHandler struct {
	DB      *db.Pool
	Reports *store.ReportStore
	Logger  *slog.Logger
}

func (h *ReportHandler) TrialBalance(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []domain.TrialBalanceRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.TrialBalanceTx(r.Context(), tx, from, to)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Foot-totals — must be equal for the report to be valid.
	var totalDebits, totalCredits decimal.Decimal
	for _, r := range rows {
		totalDebits = totalDebits.Add(r.ClosingDebit)
		totalCredits = totalCredits.Add(r.ClosingCredit)
	}
	httpx.OK(w, map[string]any{
		"from":          from.Format("2006-01-02"),
		"to":            to.Format("2006-01-02"),
		"items":         rows,
		"total_debits":  totalDebits,
		"total_credits": totalCredits,
		"balanced":      totalDebits.Equal(totalCredits),
	})
}

// BalanceSheet — snapshot at the given as-of date. Defaults to today.
func (h *ReportHandler) BalanceSheet(w http.ResponseWriter, r *http.Request) {
	asOf := time.Now()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("as_of must be YYYY-MM-DD"))
			return
		}
		asOf = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.BalanceSheetRow
	var netSurplus decimal.Decimal
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.BalanceSheetTx(r.Context(), tx, asOf)
		if err != nil {
			return err
		}
		netSurplus, err = h.Reports.NetSurplusTx(r.Context(), tx, asOf)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Surface unclosed P&L as a derived equity line so the equation
	// balances before period close. Suppressed when zero.
	if !netSurplus.IsZero() {
		surplus := netSurplus
		isContra := surplus.IsNegative()
		if isContra {
			surplus = surplus.Neg()
		}
		rows = append(rows, store.BalanceSheetRow{
			AccountCode: "3999",
			AccountName: "Current period earnings (unclosed)",
			Class:       domain.ClassEquity,
			Amount:      surplus,
			IsContra:    isContra,
		})
	}

	var totalAssets, totalLiab, totalEquity decimal.Decimal
	for _, r := range rows {
		amt := r.Amount
		if r.IsContra {
			amt = amt.Neg()
		}
		switch r.Class {
		case domain.ClassAsset:
			totalAssets = totalAssets.Add(amt)
		case domain.ClassLiability:
			totalLiab = totalLiab.Add(amt)
		case domain.ClassEquity:
			totalEquity = totalEquity.Add(amt)
		}
	}
	httpx.OK(w, map[string]any{
		"as_of":             asOf.Format("2006-01-02"),
		"items":             rows,
		"total_assets":      totalAssets,
		"total_liabilities": totalLiab,
		"total_equity":      totalEquity,
		"balanced":          totalAssets.Equal(totalLiab.Add(totalEquity)),
	})
}

// IncomeStatement — income and expenses for a window, plus net surplus.
func (h *ReportHandler) IncomeStatement(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.IncomeStatementRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.IncomeStatementTx(r.Context(), tx, from, to)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var totalIncome, totalExpense decimal.Decimal
	for _, r := range rows {
		switch r.Class {
		case domain.ClassIncome:
			totalIncome = totalIncome.Add(r.Amount)
		case domain.ClassExpense:
			totalExpense = totalExpense.Add(r.Amount)
		}
	}
	httpx.OK(w, map[string]any{
		"from":          from.Format("2006-01-02"),
		"to":            to.Format("2006-01-02"),
		"items":         rows,
		"total_income":  totalIncome,
		"total_expense": totalExpense,
		"net_surplus":   totalIncome.Sub(totalExpense),
	})
}

// ChangesInEquity — per-equity-account opening/increase/decrease/
// closing over [from, to], plus the period's net surplus (income −
// expense) surfaced as a derived line that explains the movement in
// retained earnings before year-end close.
func (h *ReportHandler) ChangesInEquity(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.ChangesInEquityRow
	var netSurplus decimal.Decimal
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.ChangesInEquityTx(r.Context(), tx, from, to)
		if err != nil {
			return err
		}
		netSurplus, err = h.Reports.NetSurplusInWindowTx(r.Context(), tx, from, to)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var totalOpening, totalIncrease, totalDecrease, totalClosing decimal.Decimal
	for _, r := range rows {
		totalOpening = totalOpening.Add(r.Opening)
		totalIncrease = totalIncrease.Add(r.Increase)
		totalDecrease = totalDecrease.Add(r.Decrease)
		totalClosing = totalClosing.Add(r.Closing)
	}
	httpx.OK(w, map[string]any{
		"from":           from.Format("2006-01-02"),
		"to":             to.Format("2006-01-02"),
		"items":          rows,
		"total_opening":  totalOpening,
		"total_increase": totalIncrease,
		"total_decrease": totalDecrease,
		"total_closing":  totalClosing,
		"net_surplus":    netSurplus,
	})
}

// CashFlow — indirect-method statement of cash flows.
func (h *ReportHandler) CashFlow(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var report *store.CashFlowReport
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		report, err = h.Reports.CashFlowTx(r.Context(), tx, from, to)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, report)
}

func (h *ReportHandler) GLDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "account_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid account_id"))
		return
	}
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.GLDetailRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.GLDetailTx(r.Context(), tx, id, from, to, limit)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"account_id": id,
		"from":       from.Format("2006-01-02"),
		"to":         to.Format("2006-01-02"),
		"items":      rows,
	})
}

// parseDateRange — `from` defaults to start of the current month and
// `to` defaults to today. Both are inclusive.
func parseDateRange(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	q := r.URL.Query()
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("from must be YYYY-MM-DD"))
			return from, to, false
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("to must be YYYY-MM-DD"))
			return from, to, false
		}
		to = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	}
	if from.After(to) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("from must be on or before to"))
		return from, to, false
	}
	return from, to, true
}
