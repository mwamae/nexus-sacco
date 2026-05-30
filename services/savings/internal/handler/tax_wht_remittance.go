// DSID Phase 2.1 — WHT iTax remittance export.
//
//   GET /v1/tax/wht-remittance.csv?period=YYYY-MM&tax_type=interest|dividend|both
//   GET /v1/tax/wht-remittance.json?period=YYYY-MM&tax_type=...
//   GET /v1/tax/wht-remittance/history?limit=50
//
// Source of truth is tax_payable_ledger — rows are written at run-post
// time with the rate snapshot. The CSV matches the iTax column order
// SACCO accountants paste into the KRA portal.
//
// Tax-exempt: Phase 2.4 adds members.tax_exempt. Until then we treat
// every member as non-exempt (the column doesn't exist; the LEFT JOIN
// degrades silently).

package handler

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type WHTRemittanceHandler struct {
	DB             *db.Pool
	Logger         *slog.Logger
	taxExemptCheck *bool // detected at first call; cached
}

type whtRow struct {
	MemberNo     string  `json:"member_no"`
	FullName     string  `json:"full_name"`
	KRAPin       string  `json:"kra_pin"`
	GrossAmount  string  `json:"gross_amount"`
	WHTRatePct   string  `json:"wht_rate_pct"`
	WHTWithheld  string  `json:"wht_withheld"`
	NetAmount    string  `json:"net_amount"`
	RunNo        string  `json:"run_no"`
	PostedAt     string  `json:"posted_at"`
	SourceKind   string  `json:"source_kind"`
}

// ─────────── CSV ───────────

func (h *WHTRemittanceHandler) CSV(w http.ResponseWriter, r *http.Request) {
	rows, period, taxType, err := h.loadRows(r)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="wht-remittance-%s-%s.csv"`, taxType, period))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{
		"member_no", "full_name", "kra_pin",
		"gross_amount", "wht_rate_pct", "wht_withheld", "net_amount",
		"run_no", "posted_at", "source_kind",
	})
	for _, r := range rows {
		_ = cw.Write([]string{
			r.MemberNo, r.FullName, r.KRAPin,
			r.GrossAmount, r.WHTRatePct, r.WHTWithheld, r.NetAmount,
			r.RunNo, r.PostedAt, r.SourceKind,
		})
	}
}

// ─────────── JSON ───────────

func (h *WHTRemittanceHandler) JSON(w http.ResponseWriter, r *http.Request) {
	rows, period, taxType, err := h.loadRows(r)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Aggregate totals for the page header.
	var totalGross, totalWHT, totalNet float64
	for _, x := range rows {
		totalGross += parseFloat(x.GrossAmount)
		totalWHT += parseFloat(x.WHTWithheld)
		totalNet += parseFloat(x.NetAmount)
	}
	httpx.OK(w, map[string]any{
		"items":         rows,
		"total":         len(rows),
		"period":        period,
		"tax_type":      taxType,
		"total_gross":   fmt.Sprintf("%.2f", totalGross),
		"total_wht":     fmt.Sprintf("%.2f", totalWHT),
		"total_net":     fmt.Sprintf("%.2f", totalNet),
	})
}

// ─────────── shared loader ───────────

func (h *WHTRemittanceHandler) loadRows(r *http.Request) ([]whtRow, string, string, error) {
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	taxType := strings.TrimSpace(r.URL.Query().Get("tax_type"))
	if taxType == "" {
		taxType = "both"
	}
	switch taxType {
	case "interest", "dividend", "both":
	default:
		return nil, "", "", httpx.ErrBadRequest("tax_type must be interest | dividend | both")
	}
	if period == "" {
		return nil, "", "", httpx.ErrBadRequest("period (YYYY-MM) required")
	}
	monthStart, err := time.Parse("2006-01", period)
	if err != nil {
		return nil, "", "", httpx.ErrBadRequest("period must be YYYY-MM")
	}
	monthEnd := monthStart.AddDate(0, 1, 0)
	tid, _ := middleware.TenantIDFrom(r)

	var sourceFilter string
	switch taxType {
	case "interest":
		sourceFilter = "AND l.source_kind = 'interest_run'"
	case "dividend":
		sourceFilter = "AND l.source_kind = 'dividend_run'"
	default:
		sourceFilter = "AND l.source_kind IN ('interest_run','dividend_run')"
	}

	// Detect tax_exempt column once + cache. Phase 2.4 may add it; we
	// degrade gracefully when absent.
	if h.taxExemptCheck == nil {
		var present bool
		_ = h.DB.QueryRow(r.Context(), `
			SELECT EXISTS (
			  SELECT 1 FROM information_schema.columns
			   WHERE table_schema = 'public'
			     AND table_name = 'members'
			     AND column_name = 'tax_exempt'
			)
		`).Scan(&present)
		h.taxExemptCheck = &present
	}
	exemptJoin := ""
	exemptFilter := ""
	if h.taxExemptCheck != nil && *h.taxExemptCheck {
		exemptJoin = "LEFT JOIN members m ON m.id = l.member_id"
		exemptFilter = "AND COALESCE(m.tax_exempt, false) = false"
	}

	var rows []whtRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// kra_pin column on members may or may not exist depending on
		// schema rev. Use COALESCE(m.kra_pin, '') with a defensive
		// LEFT JOIN inside DBs that have it. For now, expose '' since
		// kra_pin isn't a guaranteed column.
		q := fmt.Sprintf(`
			SELECT l.member_no, l.member_name, '' AS kra_pin,
			       l.gross_amount::text, l.wht_rate_pct::text, l.wht_amount::text,
			       (l.gross_amount - l.wht_amount)::text AS net_amount,
			       COALESCE(ir.run_no, dr.run_no, '') AS run_no,
			       to_char(l.posted_at, 'YYYY-MM-DD') AS posted_at,
			       l.source_kind
			  FROM tax_payable_ledger l
			  LEFT JOIN interest_runs ir ON ir.id = l.source_id AND l.source_kind = 'interest_run'
			  LEFT JOIN dividend_runs dr ON dr.id = l.source_id AND l.source_kind = 'dividend_run'
			  %s
			 WHERE l.posted_at >= $1 AND l.posted_at < $2
			   %s
			   %s
			 ORDER BY l.posted_at, l.member_no
		`, exemptJoin, sourceFilter, exemptFilter)
		r2, qerr := tx.Query(r.Context(), q, monthStart, monthEnd)
		if qerr != nil {
			return qerr
		}
		defer r2.Close()
		for r2.Next() {
			var row whtRow
			if err := r2.Scan(
				&row.MemberNo, &row.FullName, &row.KRAPin,
				&row.GrossAmount, &row.WHTRatePct, &row.WHTWithheld, &row.NetAmount,
				&row.RunNo, &row.PostedAt, &row.SourceKind,
			); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return r2.Err()
	})
	if err != nil {
		return nil, "", "", err
	}
	return rows, period, taxType, nil
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
