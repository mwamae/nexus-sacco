// Excel export for the financial reports.
//
// One generic endpoint dispatches by report name:
//   GET /v1/exports/balance-sheet.xlsx?as_of=YYYY-MM-DD
//   GET /v1/exports/income-statement.xlsx?from=...&to=...
//   GET /v1/exports/trial-balance.xlsx?from=...&to=...
//   GET /v1/exports/cash-flow.xlsx?from=...&to=...
//   GET /v1/exports/changes-in-equity.xlsx?from=...&to=...
//   GET /v1/exports/sasra-return.xlsx?as_of=...
//
// Each builder reuses the same data layer the JSON endpoints call —
// the only thing different is the serialization to .xlsx via excelize.
//
// Excel conventions:
//   • Row 1: report title (merged, bold, larger)
//   • Row 2: tenant name + as-of date (italic, muted)
//   • Row 3: blank
//   • Row 4: column headers (bold, filled)
//   • Data rows follow; money columns use #,##0.00 format
//   • Subtotal rows are bold + lightly filled

package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

type ExportHandler struct {
	DB      *db.Pool
	Reports *store.ReportStore
	Budgets *store.BudgetStore
	Tenants *store.TenantStore
	Logger  *slog.Logger
}

func (h *ExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	report := chi.URLParam(r, "report")
	tid, _ := middleware.TenantIDFrom(r)
	if tid == [16]byte{} {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("tenant context required"))
		return
	}
	tenant, err := h.Tenants.GetByID(r.Context(), tid)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantName := tenant.Name

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	switch strings.TrimSuffix(report, ".xlsx") {
	case "balance-sheet":
		err = h.buildBalanceSheet(r.Context(), f, tid, tenantName, r)
	case "income-statement":
		err = h.buildIncomeStatement(r.Context(), f, tid, tenantName, r)
	case "trial-balance":
		err = h.buildTrialBalance(r.Context(), f, tid, tenantName, r)
	case "cash-flow":
		err = h.buildCashFlow(r.Context(), f, tid, tenantName, r)
	case "changes-in-equity":
		err = h.buildChangesInEquity(r.Context(), f, tid, tenantName, r)
	case "sasra-return":
		err = h.buildSASRAReturn(r.Context(), f, tid, tenantName, r)
	case "fees-summary":
		err = h.buildFeesSummary(r.Context(), f, tid, tenantName, r)
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("unknown report: "+report))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	filename := fmt.Sprintf("%s-%s-%s.xlsx",
		strings.TrimSuffix(report, ".xlsx"),
		strings.ReplaceAll(strings.ToLower(tenantName), " ", "-"),
		time.Now().UTC().Format("20060102"),
	)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if err := f.Write(w); err != nil {
		h.Logger.Error("write xlsx", "err", err)
	}
}

// ─────────── Styling helpers ───────────

type sheetStyles struct {
	Title    int
	Meta     int
	Header   int
	Money    int
	Subtotal int
	SubtotalMoney int
}

func styles(f *excelize.File) sheetStyles {
	title, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 14},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	meta, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Italic: true, Color: "707070"},
	})
	header, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"3B6AB8"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	money, _ := f.NewStyle(&excelize.Style{
		NumFmt:    4, // #,##0.00;(#,##0.00)
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	subtotal, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"EEEEEE"}, Pattern: 1},
	})
	subtotalMoney, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"EEEEEE"}, Pattern: 1},
		NumFmt:    4,
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	return sheetStyles{Title: title, Meta: meta, Header: header, Money: money, Subtotal: subtotal, SubtotalMoney: subtotalMoney}
}

// writeHeader writes rows 1-3 (title + meta + blank) and returns the
// next free row.
func writeHeader(f *excelize.File, sheet, title, subtitle string, st sheetStyles, cols int) int {
	_ = f.SetCellValue(sheet, "A1", title)
	_ = f.SetCellStyle(sheet, "A1", "A1", st.Title)
	if cols > 1 {
		colLetter, _ := excelize.ColumnNumberToName(cols)
		_ = f.MergeCell(sheet, "A1", colLetter+"1")
	}
	_ = f.SetCellValue(sheet, "A2", subtitle)
	_ = f.SetCellStyle(sheet, "A2", "A2", st.Meta)
	_ = f.SetRowHeight(sheet, 1, 22)
	return 4 // row index for column-header row
}

func decStr(d decimal.Decimal) float64 {
	v, _ := d.Float64()
	return v
}

// ─────────── Balance Sheet ───────────

func (h *ExportHandler) buildBalanceSheet(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	asOf := time.Now()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return httpx.ErrBadRequest("as_of must be YYYY-MM-DD")
		}
		asOf = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	}

	var rows []store.BalanceSheetRow
	var netSurplus decimal.Decimal
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.BalanceSheetTx(ctx, tx, asOf)
		if err != nil {
			return err
		}
		netSurplus, err = h.Reports.NetSurplusTx(ctx, tx, asOf)
		return err
	})
	if err != nil {
		return err
	}

	sheet := "Balance Sheet"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "Statement of Financial Position", tenantName+" · As of "+asOf.Format("2006-01-02"), st, 4)

	// Column headers.
	headers := []string{"Code", "Account", "Class", "Amount"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A4", "D4", st.Header)
	row++

	// Group by class for readability.
	classes := []string{"asset", "liability", "equity"}
	classLabel := map[string]string{"asset": "ASSETS", "liability": "LIABILITIES", "equity": "EQUITY"}
	totals := map[string]decimal.Decimal{}
	for _, cls := range classes {
		sectionStart := row
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), classLabel[cls])
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("D%d", row), st.Subtotal)
		row++
		for _, r := range rows {
			if string(r.Class) != cls {
				continue
			}
			amt := r.Amount
			if r.IsContra {
				amt = amt.Neg()
			}
			totals[cls] = totals[cls].Add(amt)
			_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.AccountCode)
			_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), r.AccountName)
			_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), string(r.Class))
			_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(amt))
			_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), st.Money)
			row++
		}
		// Unclosed P&L into equity.
		if cls == "equity" && !netSurplus.IsZero() {
			_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "3999")
			_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Current period earnings (unclosed)")
			_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), "equity")
			_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(netSurplus))
			_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), st.Money)
			totals[cls] = totals[cls].Add(netSurplus)
			row++
		}
		// Subtotal.
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Total "+classLabel[cls])
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(totals[cls]))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("C%d", row), st.Subtotal)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), st.SubtotalMoney)
		row++
		row++ // blank spacer
		_ = sectionStart
	}

	// Grand reconciliation: Assets = Liabilities + Equity
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Liabilities + Equity")
	combined := totals["liability"].Add(totals["equity"])
	_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(combined))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("C%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), st.SubtotalMoney)
	row++
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Balanced?")
	balanced := totals["asset"].Equal(combined)
	label := "✓ Yes"
	if !balanced {
		label = "✗ NO — investigate"
	}
	_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), label)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("D%d", row), st.Subtotal)

	_ = f.SetColWidth(sheet, "A", "A", 10)
	_ = f.SetColWidth(sheet, "B", "B", 36)
	_ = f.SetColWidth(sheet, "C", "C", 12)
	_ = f.SetColWidth(sheet, "D", "D", 16)
	return nil
}

// ─────────── Income Statement ───────────

func (h *ExportHandler) buildIncomeStatement(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	from, to, ok := parseDateRangeFromQuery(r)
	if !ok {
		return httpx.ErrBadRequest("from and to are required as YYYY-MM-DD")
	}
	var rows []store.IncomeStatementRow
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.IncomeStatementTx(ctx, tx, from, to)
		return err
	})
	if err != nil {
		return err
	}
	sheet := "Income Statement"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "Income Statement",
		tenantName+" · "+from.Format("2006-01-02")+" → "+to.Format("2006-01-02"), st, 3)

	headers := []string{"Code", "Account", "Amount"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A4", "C4", st.Header)
	row++

	var totalIncome, totalExpense decimal.Decimal
	for _, label := range []string{"income", "expense"} {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), strings.ToUpper(label))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("C%d", row), st.Subtotal)
		row++
		for _, r := range rows {
			if string(r.Class) != label {
				continue
			}
			_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.AccountCode)
			_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), r.AccountName)
			_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(r.Amount))
			_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("C%d", row), st.Money)
			if label == "income" {
				totalIncome = totalIncome.Add(r.Amount)
			} else {
				totalExpense = totalExpense.Add(r.Amount)
			}
			row++
		}
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Total "+label)
		t := totalIncome
		if label == "expense" {
			t = totalExpense
		}
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(t))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Subtotal)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("C%d", row), st.SubtotalMoney)
		row += 2
	}

	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Net surplus / (deficit)")
	_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(totalIncome.Sub(totalExpense)))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("C%d", row), st.SubtotalMoney)

	_ = f.SetColWidth(sheet, "A", "A", 10)
	_ = f.SetColWidth(sheet, "B", "B", 36)
	_ = f.SetColWidth(sheet, "C", "C", 18)
	return nil
}

// ─────────── Trial Balance ───────────

func (h *ExportHandler) buildTrialBalance(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	from, to, ok := parseDateRangeFromQuery(r)
	if !ok {
		return httpx.ErrBadRequest("from and to are required as YYYY-MM-DD")
	}
	var rows []domainTrialBalanceRow
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		src, err := h.Reports.TrialBalanceTx(ctx, tx, from, to)
		if err != nil {
			return err
		}
		for _, s := range src {
			rows = append(rows, domainTrialBalanceRow{
				Code: s.AccountCode, Name: s.AccountName, Class: string(s.Class),
				OpeningDebit: s.OpeningDebit, OpeningCredit: s.OpeningCredit,
				PeriodDebits: s.PeriodDebits, PeriodCredits: s.PeriodCredits,
				ClosingDebit: s.ClosingDebit, ClosingCredit: s.ClosingCredit,
			})
		}
		return nil
	})
	if err != nil {
		return err
	}

	sheet := "Trial Balance"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "Trial Balance",
		tenantName+" · "+from.Format("2006-01-02")+" → "+to.Format("2006-01-02"), st, 7)

	headers := []string{"Code", "Account", "Class", "Period Debits", "Period Credits", "Closing Debit", "Closing Credit"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A4", "G4", st.Header)
	row++

	var td, tc decimal.Decimal
	for _, r := range rows {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.Code)
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), r.Name)
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), r.Class)
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(r.PeriodDebits))
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), decStr(r.PeriodCredits))
		_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(r.ClosingDebit))
		_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", row), decStr(r.ClosingCredit))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", row), fmt.Sprintf("G%d", row), st.Money)
		td = td.Add(r.ClosingDebit)
		tc = tc.Add(r.ClosingCredit)
		row++
	}
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "TOTALS")
	_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(td))
	_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", row), decStr(tc))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("E%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("F%d", row), fmt.Sprintf("G%d", row), st.SubtotalMoney)
	row++
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Balanced?")
	balanced := td.Equal(tc)
	if balanced {
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), "✓ Yes")
	} else {
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), "✗ NO")
	}

	for _, c := range []string{"A", "C"} {
		_ = f.SetColWidth(sheet, c, c, 12)
	}
	_ = f.SetColWidth(sheet, "B", "B", 36)
	_ = f.SetColWidth(sheet, "D", "G", 16)
	return nil
}

type domainTrialBalanceRow struct {
	Code, Name, Class                          string
	OpeningDebit, OpeningCredit, PeriodDebits  decimal.Decimal
	PeriodCredits, ClosingDebit, ClosingCredit decimal.Decimal
}

// ─────────── Cash Flow ───────────

func (h *ExportHandler) buildCashFlow(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	from, to, ok := parseDateRangeFromQuery(r)
	if !ok {
		return httpx.ErrBadRequest("from and to are required as YYYY-MM-DD")
	}
	var report *store.CashFlowReport
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		report, err = h.Reports.CashFlowTx(ctx, tx, from, to)
		return err
	})
	if err != nil {
		return err
	}

	sheet := "Cash Flow"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "Cash Flow Statement (indirect method)",
		tenantName+" · "+from.Format("2006-01-02")+" → "+to.Format("2006-01-02"), st, 2)

	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Description")
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Amount")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Header)
	row++

	for _, sec := range report.Sections {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), strings.ToUpper(sec.Name))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Subtotal)
		row++
		for _, ln := range sec.Rows {
			_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), ln.Label)
			_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(ln.Amount))
			_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.Money)
			row++
		}
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Net cash from "+strings.ToLower(sec.Name))
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(sec.Subtotal))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), st.Subtotal)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.SubtotalMoney)
		row += 2
	}

	// Reconciliation footer.
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Net change in cash (computed)")
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(report.NetChangeInCash))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.SubtotalMoney)
	row++
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Opening cash")
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(report.OpeningCash))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.Money)
	row++
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Closing cash")
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(report.ClosingCash))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.Money)
	row++
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "Reconciles?")
	if report.Reconciles {
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "✓ Yes")
	} else {
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "✗ Investigate")
	}

	_ = f.SetColWidth(sheet, "A", "A", 50)
	_ = f.SetColWidth(sheet, "B", "B", 18)
	return nil
}

// ─────────── Changes in Equity ───────────

func (h *ExportHandler) buildChangesInEquity(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	from, to, ok := parseDateRangeFromQuery(r)
	if !ok {
		return httpx.ErrBadRequest("from and to are required as YYYY-MM-DD")
	}
	var rows []store.ChangesInEquityRow
	var netSurplus decimal.Decimal
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		rows, err = h.Reports.ChangesInEquityTx(ctx, tx, from, to)
		if err != nil {
			return err
		}
		netSurplus, err = h.Reports.NetSurplusInWindowTx(ctx, tx, from, to)
		return err
	})
	if err != nil {
		return err
	}

	sheet := "Changes in Equity"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "Statement of Changes in Equity",
		tenantName+" · "+from.Format("2006-01-02")+" → "+to.Format("2006-01-02"), st, 6)

	headers := []string{"Code", "Account", "Opening", "Increase", "Decrease", "Closing"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A4", "F4", st.Header)
	row++

	var to1, ti, td2, tcls decimal.Decimal
	for _, r := range rows {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.AccountCode)
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), r.AccountName)
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(r.Opening))
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(r.Increase))
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), decStr(r.Decrease))
		_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(r.Closing))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("F%d", row), st.Money)
		to1 = to1.Add(r.Opening); ti = ti.Add(r.Increase); td2 = td2.Add(r.Decrease); tcls = tcls.Add(r.Closing)
		row++
	}
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Total (closed equity accounts)")
	_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(to1))
	_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(ti))
	_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), decStr(td2))
	_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(tcls))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("F%d", row), st.SubtotalMoney)
	row += 2
	_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), "Net surplus / (deficit) for period (unclosed)")
	_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(netSurplus))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("E%d", row), st.Subtotal)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("F%d", row), fmt.Sprintf("F%d", row), st.SubtotalMoney)

	_ = f.SetColWidth(sheet, "A", "A", 10)
	_ = f.SetColWidth(sheet, "B", "B", 36)
	_ = f.SetColWidth(sheet, "C", "F", 14)
	return nil
}

// ─────────── SASRA Return ───────────

func (h *ExportHandler) buildSASRAReturn(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	asOf := time.Now()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return httpx.ErrBadRequest("as_of must be YYYY-MM-DD")
		}
		asOf = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	}
	var report *store.SASRAReturn
	err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		report, err = h.Reports.SASRAReturnTx(ctx, tx, asOf)
		return err
	})
	if err != nil {
		return err
	}

	sheet := "SASRA Return"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	row := writeHeader(f, sheet, "SASRA Quarterly Return",
		tenantName+" · As of "+asOf.Format("2006-01-02"), st, 6)

	// Section: Position
	row = writeSection(f, sheet, row, st, "Statement of Financial Position", [][]any{
		{"Total assets", decStr(report.Position.TotalAssets)},
		{"Total liabilities", decStr(report.Position.TotalLiabilities)},
		{"Total equity", decStr(report.Position.TotalEquity)},
	})

	row = writeSection(f, sheet, row, st, "Income (YTD)", [][]any{
		{"Total income", decStr(report.IncomeStatement.TotalIncome)},
		{"Total expense", decStr(report.IncomeStatement.TotalExpense)},
		{"Net surplus", decStr(report.IncomeStatement.NetSurplus)},
	})

	row = writeSection(f, sheet, row, st, "Capital", [][]any{
		{"Share capital (3000)", decStr(report.Capital.ShareCapital)},
		{"Retained earnings + unclosed P&L", decStr(report.Capital.RetainedEarnings)},
		{"Statutory reserve (3020)", decStr(report.Capital.StatutoryReserve)},
		{"General reserves (3030)", decStr(report.Capital.GeneralReserves)},
		{"Institutional capital (3050)", decStr(report.Capital.InstitutionalCapital)},
		{"Less: Intangible assets", decStr(report.Capital.IntangibleAssets)},
		{"Core capital", decStr(report.Capital.CoreCapital)},
		{"Institutional capital total", decStr(report.Capital.InstitutionalCapTotal)},
	})

	row = writeSection(f, sheet, row, st, "Loan portfolio", [][]any{
		{"Gross loans (1100)", decStr(report.LoanPortfolio.GrossLoans)},
		{"Interest receivable", decStr(report.LoanPortfolio.InterestRecv)},
		{"Provisions (1120)", decStr(report.LoanPortfolio.Provisions)},
		{"Net loans", decStr(report.LoanPortfolio.NetLoans)},
	})

	row = writeSection(f, sheet, row, st, "Deposits", [][]any{
		// PR 4: BOSA / FOSA segmentation surfaces as two distinct
		// regulator-facing lines. Fixed deposits (2100) roll up
		// into FOSA after the PR 3 reclassification — the prior
		// "Fixed deposits" line stops being meaningful.
		{"Member deposits (BOSA, non-withdrawable)", decStr(report.Deposits.MemberDepositsBOSA)},
		{"Member savings (FOSA, withdrawable)", decStr(report.Deposits.MemberSavingsFOSA)},
		{"Total deposits", decStr(report.Deposits.Total)},
	})

	// Ratios table.
	row += 1
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "SASRA Prudential Ratios")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Subtotal)
	row++
	headers := []string{"Ratio", "Numerator", "Denominator", "Result %", "Threshold", "Compliant?"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Header)
	row++
	for _, r := range report.Ratios {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.Label)
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), decStr(r.Numerator))
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(r.Denominator))
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(r.Ratio))
		op := "≥"
		if r.Operator == "max" {
			op = "≤"
		}
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), op+" "+r.Threshold.String()+"%")
		if r.Compliant {
			_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), "✓")
		} else {
			_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), "✗ breach")
		}
		_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("D%d", row), st.Money)
		row++
	}

	_ = f.SetColWidth(sheet, "A", "A", 40)
	_ = f.SetColWidth(sheet, "B", "F", 16)
	return nil
}

// writeSection renders one labelled section block — a header row +
// 2-column key/value rows. Returns the next free row.
func writeSection(f *excelize.File, sheet string, row int, st sheetStyles, title string, rows [][]any) int {
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), title)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), st.Subtotal)
	row++
	for _, r := range rows {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r[0])
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), r[1])
		_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.Money)
		row++
	}
	return row + 1
}

// ─────────── Fees & Collections Summary ───────────
//
// XLSX twin of services/savings/internal/handler/fees_summary.go::Summary.
// Same SQL aggregation, written here so the existing
// /v1/exports/{report}.xlsx surface keeps working from the
// downloadReport() frontend helper. The query joins savings-owned
// tables (receipts, receipt_lines, fee_catalog) — shared-DB pattern,
// already done by buildSASRAReturn et al.
func (h *ExportHandler) buildFeesSummary(ctx context.Context, f *excelize.File, tid [16]byte, tenantName string, r *http.Request) error {
	from, to, ok := parseDateRangeFromQuery(r)
	if !ok {
		return httpx.ErrBadRequest("from and to are required (YYYY-MM-DD)")
	}
	channel := r.URL.Query().Get("channel")
	feeCode := r.URL.Query().Get("fee_code")
	counterpartyID := r.URL.Query().Get("counterparty_id")

	type feeCodeRow struct {
		FeeCode      string
		FeeLabel     string
		GLCreditCode string
		Count        int
		Total        decimal.Decimal
		Voided       decimal.Decimal
	}
	type channelRow struct {
		Channel string
		Count   int
		Total   decimal.Decimal
		Voided  decimal.Decimal
	}
	var (
		totalAmt, totalVoid decimal.Decimal
		feeRows             []feeCodeRow
		chRows              []channelRow
	)

	if err := h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		conds := []string{
			"rl.kind IN ('fee','welfare')",
			"r.status = 'posted'",
			"r.value_date BETWEEN $1 AND $2",
		}
		args := []any{from, to}
		idx := 3
		if channel != "" {
			conds = append(conds, fmt.Sprintf("r.channel = $%d", idx))
			args = append(args, channel)
			idx++
		}
		if feeCode != "" {
			conds = append(conds, fmt.Sprintf("rl.fee_code = $%d", idx))
			args = append(args, feeCode)
			idx++
		}
		if counterpartyID != "" {
			conds = append(conds, fmt.Sprintf("r.counterparty_id = $%d", idx))
			args = append(args, counterpartyID)
			idx++
		}
		where := strings.Join(conds, " AND ")

		// Totals
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(rl.amount), 0),
			       COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0)
			  FROM receipts r
			  JOIN receipt_lines rl ON rl.receipt_id = r.id
			 WHERE `+where, args...).Scan(&totalAmt, &totalVoid); err != nil {
			return fmt.Errorf("fees totals: %w", err)
		}
		// By fee code
		fr, err := tx.Query(ctx, `
			SELECT COALESCE(rl.fee_code, ''),
			       COALESCE(fc.label, rl.fee_code, ''),
			       COALESCE(fc.gl_credit_code, ''),
			       COUNT(*),
			       COALESCE(SUM(rl.amount), 0),
			       COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0)
			  FROM receipts r
			  JOIN receipt_lines rl    ON rl.receipt_id = r.id
			  LEFT JOIN fee_catalog fc ON fc.tenant_id = r.tenant_id AND fc.code = rl.fee_code
			 WHERE `+where+`
			 GROUP BY rl.fee_code, COALESCE(fc.label, rl.fee_code, ''), COALESCE(fc.gl_credit_code, '')
			 ORDER BY 5 DESC
		`, args...)
		if err != nil {
			return err
		}
		for fr.Next() {
			var row feeCodeRow
			if err := fr.Scan(&row.FeeCode, &row.FeeLabel, &row.GLCreditCode, &row.Count, &row.Total, &row.Voided); err != nil {
				fr.Close()
				return err
			}
			feeRows = append(feeRows, row)
		}
		fr.Close()
		// By channel
		cr, err := tx.Query(ctx, `
			SELECT r.channel::text,
			       COUNT(*),
			       COALESCE(SUM(rl.amount), 0),
			       COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0)
			  FROM receipts r
			  JOIN receipt_lines rl ON rl.receipt_id = r.id
			 WHERE `+where+`
			 GROUP BY r.channel
			 ORDER BY 3 DESC
		`, args...)
		if err != nil {
			return err
		}
		for cr.Next() {
			var row channelRow
			if err := cr.Scan(&row.Channel, &row.Count, &row.Total, &row.Voided); err != nil {
				cr.Close()
				return err
			}
			chRows = append(chRows, row)
		}
		cr.Close()
		return nil
	}); err != nil {
		return err
	}

	sheet := "Fees Summary"
	_, _ = f.NewSheet(sheet)
	_ = f.DeleteSheet("Sheet1")
	st := styles(f)
	subtitle := fmt.Sprintf("%s · %s → %s", tenantName,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	row := writeHeader(f, sheet, "Fees & Collections Summary", subtitle, st, 6)

	// ── Totals block ──
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "TOTALS")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Subtotal)
	row++
	for _, kv := range [][2]any{
		{"Total amount", decStr(totalAmt)},
		{"Voided amount", decStr(totalVoid)},
		{"Net amount", decStr(totalAmt.Sub(totalVoid))},
	} {
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), kv[0])
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), kv[1])
		_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.Money)
		row++
	}
	row++

	// ── By fee code ──
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "BY FEE CODE")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Subtotal)
	row++
	feeHeaders := []string{"Code", "Label", "GL credit", "Count", "Total", "Net"}
	for i, h := range feeHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Header)
	row++
	for _, fr := range feeRows {
		net := fr.Total.Sub(fr.Voided)
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), fr.FeeCode)
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), fr.FeeLabel)
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), fr.GLCreditCode)
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), fr.Count)
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), decStr(fr.Total))
		_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", row), decStr(net))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("E%d", row), fmt.Sprintf("F%d", row), st.Money)
		row++
	}
	row++

	// ── By channel ──
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "BY CHANNEL")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("F%d", row), st.Subtotal)
	row++
	chHeaders := []string{"Channel", "Count", "Total", "Voided", "Net"}
	for i, h := range chHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", row), fmt.Sprintf("E%d", row), st.Header)
	row++
	for _, cr := range chRows {
		net := cr.Total.Sub(cr.Voided)
		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", row), cr.Channel)
		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", row), cr.Count)
		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", row), decStr(cr.Total))
		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", row), decStr(cr.Voided))
		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", row), decStr(net))
		_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", row), fmt.Sprintf("E%d", row), st.Money)
		row++
	}

	// Reasonable column widths so labels read at a glance.
	_ = f.SetColWidth(sheet, "A", "A", 22)
	_ = f.SetColWidth(sheet, "B", "B", 32)
	_ = f.SetColWidth(sheet, "C", "F", 14)
	return nil
}

// parseDateRangeFromQuery is the standalone version of the existing
// reports.parseDateRange — needed here because the report handler's
// helper writes to ResponseWriter and we already have our own error
// path.
func parseDateRangeFromQuery(r *http.Request) (from, to time.Time, ok bool) {
	fv := r.URL.Query().Get("from")
	tv := r.URL.Query().Get("to")
	if fv == "" || tv == "" {
		return
	}
	ft, err := time.Parse("2006-01-02", fv)
	if err != nil {
		return
	}
	tt, err := time.Parse("2006-01-02", tv)
	if err != nil {
		return
	}
	return ft, time.Date(tt.Year(), tt.Month(), tt.Day(), 23, 59, 59, 0, time.UTC), true
}
