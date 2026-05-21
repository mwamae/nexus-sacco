// Fiscal year close — posts the closing journal that zeros all P&L
// accounts into Retained Earnings (3010) and locks every monthly
// period in the year.
//
// Fiscal year for now = calendar year. The closing journal:
//   • DR every income account by its closing credit balance → 0
//   • CR every expense account by its closing debit balance → 0
//   • The net (income − expense) is the period surplus:
//       - Surplus  → CR Retained Earnings 3010
//       - Deficit  → DR Retained Earnings 3010
//
// Posted as one balanced entry with entry_type='adjustment' (we don't
// have a dedicated 'closing' enum value yet; the narration tags it).
//
// After posting, all 12 monthly periods of the year flip to 'closed'.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

type FiscalYearHandler struct {
	DB          *db.Pool
	FY          *store.FiscalYearStore
	Periods     *store.PeriodStore
	Engine      *posting.Engine
	Logger      *slog.Logger
}

func (h *FiscalYearHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.FiscalYearClose
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.FY.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type closeYearReq struct {
	Notes string `json:"notes,omitempty"`
}

func (h *FiscalYearHandler) Close(w http.ResponseWriter, r *http.Request) {
	yearStr := chi.URLParam(r, "year")
	year, err := strconv.Atoi(yearStr)
	if err != nil || year < 2000 || year > 2999 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("year must be a 4-digit number"))
		return
	}
	var in closeYearReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	fyStart := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	fyEnd := time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)

	var result *store.FiscalYearClose
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		alreadyClosed, err := h.FY.IsClosedTx(r.Context(), tx, year)
		if err != nil {
			return err
		}
		if alreadyClosed {
			return store.ErrYearAlreadyClosed
		}

		// Open Dec period if not yet — the posting engine will refuse
		// otherwise. The year-end close posts dated Dec 31.
		if _, err := h.Periods.EnsureOpenForDateTx(r.Context(), tx, fyEnd); err != nil {
			return fmt.Errorf("december period: %w", err)
		}

		// Read every income/expense account's closing balance.
		closing, err := h.FY.PLBalancesAsOfTx(r.Context(), tx, fyEnd)
		if err != nil {
			return fmt.Errorf("read P&L balances: %w", err)
		}
		if len(closing) == 0 {
			return httpx.ErrBadRequest("no income or expense activity in " + yearStr + " — nothing to close")
		}

		var totalIncome, totalExpense decimal.Decimal
		var incomeAcctCount, expenseAcctCount int
		lines := make([]posting.Line, 0, len(closing)+1)
		for _, cl := range closing {
			if cl.Class == "income" {
				totalIncome = totalIncome.Add(cl.Balance)
				incomeAcctCount++
				// Income has a credit balance — debit it to zero.
				lines = append(lines, posting.Line{
					AccountCode: cl.AccountCode,
					Debit:       cl.Balance,
					Narration:   "Close " + cl.AccountName,
				})
			} else {
				totalExpense = totalExpense.Add(cl.Balance)
				expenseAcctCount++
				// Expense has a debit balance — credit it to zero.
				lines = append(lines, posting.Line{
					AccountCode: cl.AccountCode,
					Credit:      cl.Balance,
					Narration:   "Close " + cl.AccountName,
				})
			}
		}

		netSurplus := totalIncome.Sub(totalExpense)
		// Plug to Retained Earnings.
		if netSurplus.IsPositive() {
			lines = append(lines, posting.Line{
				AccountCode: "3010",
				Credit:      netSurplus,
				Narration:   "FY " + yearStr + " net surplus → retained earnings",
			})
		} else if netSurplus.IsNegative() {
			lines = append(lines, posting.Line{
				AccountCode: "3010",
				Debit:       netSurplus.Neg(),
				Narration:   "FY " + yearStr + " net deficit ← retained earnings",
			})
		}
		// netSurplus == 0 is fine — no retained earnings movement needed.

		// Post the closing entry.
		entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryDate:    fyEnd,
			ValueDate:    fyEnd,
			EntryType:    domain.TypeAdjustment,
			SourceModule: "accounting.fiscal-year-close",
			SourceRef:    yearStr,
			Narration:    "Year-end closing entry for FY " + yearStr,
			Lines:        lines,
			PostedBy:     &userID,
		})
		if err != nil {
			return fmt.Errorf("post closing entry: %w", err)
		}

		// Record the audit row.
		recorded, err := h.FY.RecordCloseTx(r.Context(), tx, store.FiscalYearClose{
			Year:            year,
			FYStart:         fyStart,
			FYEnd:           fyEnd,
			ClosingEntryID:  entry.ID,
			TotalIncome:     totalIncome,
			TotalExpense:    totalExpense,
			NetSurplus:      netSurplus,
			IncomeAccounts:  incomeAcctCount,
			ExpenseAccounts: expenseAcctCount,
			ClosedBy:        userID,
			Notes:           strPtrIfNotEmpty(in.Notes),
		})
		if err != nil {
			return fmt.Errorf("record close: %w", err)
		}

		// Lock all 12 monthly periods of the year. Done AFTER posting so
		// the closing entry itself wasn't blocked.
		if err := h.FY.LockAllPeriodsInYearTx(r.Context(), tx, year, userID); err != nil {
			return fmt.Errorf("lock periods: %w", err)
		}

		result = recorded
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrYearAlreadyClosed) {
			httpx.WriteErr(w, r, httpx.ErrConflict("fiscal year "+yearStr+" is already closed"))
			return
		}
		h.Logger.Error("close fiscal year", "year", year, "err", err)
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, result)
}

func strPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
