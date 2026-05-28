// Loans Phase 2 — SASRA quarterly extract handler.
//
// Endpoints:
//
//   GET  /v1/loans/reports/sasra?period=YYYY-Qn&format=csv|pdf|json
//        Generates the SASRA quarterly portfolio extract. Defaults to
//        CSV. DRAFT watermark applied until the tenant admin has
//        verified the column layout against the current SASRA form.
//
//   POST /v1/loans/reports/sasra/verify
//        Body: {"period":"2026-Q1","verified_form_version":"…","note":"…"}
//        Tenant admin marks the column layout verified for the
//        period. Clears the DRAFT watermark.
//
//   GET  /v1/tenant/sasra-column-overrides
//   PUT  /v1/tenant/sasra-column-overrides
//        Per-tenant escape hatch for SASRA-changes-the-form-mid-release.
//        PUT body matches sasra_columns.json shape. Recorded with
//        provenance (updated_by, updated_at, form_version).
//
// Permission: loans:sasra (sensitive — granted to sacco_admin +
// auditor only per identity/0033). loans:reports:export gates the
// CSV/PDF download formats; JSON read is just loans:sasra.

package handler

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jung-kurt/gofpdf"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

//go:embed sasra_columns.json
var defaultSASRAColumnsJSON []byte

type SASRAColumnLayout struct {
	FormVersion              string                        `json:"form_version"`
	Description              string                        `json:"description"`
	HeaderMetadata           []string                      `json:"header_metadata"`
	Columns                  []SASRAColumn                 `json:"columns"`
	ClassificationThresholds map[string]ClassificationRule `json:"classification_thresholds"`
}

type SASRAColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type ClassificationRule struct {
	DPDMin       int     `json:"dpd_min"`
	DPDMax       *int    `json:"dpd_max"`
	Code         int     `json:"code"`
	ProvisionPct float64 `json:"provision_pct"`
}

type SASRAHandler struct {
	DB *db.Pool
}

// ─────────── GET /v1/loans/reports/sasra ───────────

func (h *SASRAHandler) Generate(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = currentQuarter()
	}
	if !validQuarter(period) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("period must be YYYY-Qn (e.g. 2026-Q1)"))
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "csv"
	}

	layout, err := h.loadLayout(r.Context(), tenantID)
	if err != nil {
		httpx.WriteErr(w, r, fmt.Errorf("load layout: %w", err))
		return
	}

	verified, verification, err := h.getVerification(r.Context(), tenantID, period)
	if err != nil {
		httpx.WriteErr(w, r, fmt.Errorf("verification lookup: %w", err))
		return
	}

	rows, headerMeta, err := h.computeRows(r.Context(), tenantID, period, layout)
	if err != nil {
		httpx.WriteErr(w, r, fmt.Errorf("compute extract: %w", err))
		return
	}

	switch format {
	case "csv":
		writeSASRACSV(w, layout, headerMeta, rows, verified, period, verification)
	case "pdf":
		writeSASRAPDF(w, layout, headerMeta, rows, verified, period, verification)
	default:
		// JSON — useful for the UI's preview before download.
		httpx.OK(w, map[string]any{
			"period":          period,
			"format_version":  layout.FormVersion,
			"verified":        verified,
			"verification":    verification,
			"header_metadata": headerMeta,
			"columns":         layout.Columns,
			"rows":            rows,
			"draft_watermark": !verified,
		})
	}
}

// ─────────── POST /v1/loans/reports/sasra/verify ───────────

type verifyReq struct {
	Period              string `json:"period"`
	VerifiedFormVersion string `json:"verified_form_version"`
	Note                string `json:"note"`
}

func (h *SASRAHandler) Verify(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	if actor == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}
	var in verifyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !validQuarter(in.Period) || strings.TrimSpace(in.VerifiedFormVersion) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("period (YYYY-Qn) + verified_form_version required"))
		return
	}
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO sasra_extract_verifications
			    (tenant_id, period, verified_by, verified_form_version, note)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, period) DO UPDATE SET
			    verified_by = EXCLUDED.verified_by,
			    verified_at = now(),
			    verified_form_version = EXCLUDED.verified_form_version,
			    note = EXCLUDED.note
		`, tenantID, in.Period, actor, in.VerifiedFormVersion, in.Note)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"period": in.Period, "verified": true})
}

// ─────────── GET/PUT /v1/tenant/sasra-column-overrides ───────────

func (h *SASRAHandler) GetOverride(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	var layout *SASRAColumnLayout
	var meta map[string]any
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var raw []byte
		var formVersion string
		var updatedBy uuid.UUID
		var updatedAt time.Time
		err := tx.QueryRow(r.Context(), `
			SELECT layout, form_version, updated_by, updated_at
			  FROM tenant_sasra_column_overrides WHERE tenant_id = $1
		`, tenantID).Scan(&raw, &formVersion, &updatedBy, &updatedAt)
		if err == pgx.ErrNoRows {
			return nil // no override — return defaults
		}
		if err != nil {
			return err
		}
		l := &SASRAColumnLayout{}
		if err := json.Unmarshal(raw, l); err != nil {
			return err
		}
		layout = l
		meta = map[string]any{
			"form_version": formVersion, "updated_by": updatedBy, "updated_at": updatedAt,
			"source": "override",
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if layout == nil {
		def, _ := h.loadDefaultLayout()
		httpx.OK(w, map[string]any{"layout": def, "source": "default"})
		return
	}
	httpx.OK(w, map[string]any{"layout": layout, "meta": meta})
}

type putOverrideReq struct {
	Layout      *SASRAColumnLayout `json:"layout"`
	FormVersion string             `json:"form_version"`
}

func (h *SASRAHandler) PutOverride(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	actor, _ := middleware.UserIDFrom(r)
	if actor == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized(""))
		return
	}
	var in putOverrideReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Layout == nil || len(in.Layout.Columns) == 0 || strings.TrimSpace(in.FormVersion) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("layout (with columns) + form_version required"))
		return
	}
	raw, err := json.Marshal(in.Layout)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO tenant_sasra_column_overrides (tenant_id, layout, updated_by, form_version)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id) DO UPDATE SET
			    layout = EXCLUDED.layout,
			    updated_by = EXCLUDED.updated_by,
			    updated_at = now(),
			    form_version = EXCLUDED.form_version
		`, tenantID, raw, actor, in.FormVersion)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"updated": true, "form_version": in.FormVersion})
}

// ─────────── compute ───────────

type sasraRow map[string]any

func (h *SASRAHandler) computeRows(ctx context.Context, tenantID uuid.UUID, period string, layout *SASRAColumnLayout) ([]sasraRow, map[string]string, error) {
	from, to, err := periodBounds(period)
	if err != nil {
		return nil, nil, err
	}
	headerMeta := map[string]string{}
	var rows []sasraRow
	err = h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Header metadata — tenant name + registration + period.
		var tenantName, regNo string
		_ = tx.QueryRow(ctx, `SELECT name, COALESCE(registration_no, '') FROM tenants WHERE id = $1`, tenantID).Scan(&tenantName, &regNo)
		headerMeta["tenant_name"] = tenantName
		headerMeta["tenant_registration_no"] = regNo
		headerMeta["reporting_period"] = period + fmt.Sprintf(" (%s to %s)", from.Format("2006-01-02"), to.Format("2006-01-02"))
		headerMeta["submission_date"] = time.Now().UTC().Format("2006-01-02")
		// Officer attribution from JWT claims.
		if c := middleware.ClaimsFrom(nil); c != nil {
			headerMeta["preparing_officer_name"] = c.FullName
		}

		// Per-loan data: include every active+restructured+written_off
		// loan whose status was open during the quarter (SASRA wants
		// the picture as-of period end).
		pgRows, err := tx.Query(ctx, `
			SELECT l.loan_no,
			       COALESCE(cd.cp_number, ''),
			       '' AS member_id_no,
			       COALESCE(cd.full_name, ''),
			       p.code, p.name,
			       l.principal::text,
			       to_char(l.disbursed_at, 'YYYY-MM-DD'),
			       COALESCE(to_char(l.disbursed_at + (l.term_months || ' months')::interval, 'YYYY-MM-DD'), ''),
			       l.interest_rate_pct::text,
			       l.term_months,
			       l.principal_balance::text,
			       l.interest_balance::text,
			       l.fees_balance::text,
			       l.penalty_balance::text,
			       (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance)::text AS total_out,
			       GREATEST(0, ($2::date - l.next_installment_due_at))::int AS dpd,
			       l.status::text
			  FROM loans l
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			  LEFT JOIN loan_products p ON p.id = l.product_id
			 WHERE l.disbursed_at <= $1
			   AND l.status IN ('active','in_arrears','restructured','defaulted','written_off')
			 ORDER BY l.loan_no
		`, to, to)
		if err != nil {
			return err
		}
		defer pgRows.Close()
		for pgRows.Next() {
			var loanNo, memberNo, memberIDNo, memberName, productCode, productName string
			var principal, disbursementDate, maturityDate, intRate, principalBal, interestBal, feesBal, penaltyBal, totalOut, status string
			var termMonths, dpd int
			if err := pgRows.Scan(&loanNo, &memberNo, &memberIDNo, &memberName,
				&productCode, &productName, &principal, &disbursementDate, &maturityDate,
				&intRate, &termMonths, &principalBal, &interestBal, &feesBal, &penaltyBal, &totalOut, &dpd, &status); err != nil {
				return err
			}
			classCode, classLabel, provisionPct := classify(dpd, status, layout.ClassificationThresholds)
			totalOutFloat, _ := strconv.ParseFloat(totalOut, 64)
			provisionRequired := totalOutFloat * provisionPct / 100.0
			rows = append(rows, sasraRow{
				"loan_no":                      loanNo,
				"member_no":                    memberNo,
				"member_id_no":                 memberIDNo,
				"member_name":                  memberName,
				"product_code":                 productCode,
				"product_name":                 productName,
				"original_disbursement_amount": principal,
				"disbursement_date":            disbursementDate,
				"maturity_date":                maturityDate,
				"interest_rate_pct":            intRate,
				"term_months":                  termMonths,
				"principal_outstanding":        principalBal,
				"interest_outstanding":         interestBal,
				"fees_outstanding":             feesBal,
				"penalty_outstanding":          penaltyBal,
				"total_outstanding":            totalOut,
				"days_in_arrears":              dpd,
				"classification_code":          classCode,
				"classification_label":         classLabel,
				"provision_required":           fmt.Sprintf("%.2f", provisionRequired),
				"provision_held":               "0.00",      // Phase 1 — not separately tracked
				"security_type":                "guarantor", // default
				"security_value":               "0.00",
				"is_insider":                   "N",
			})
		}
		return pgRows.Err()
	})
	return rows, headerMeta, err
}

func classify(dpd int, status string, thresholds map[string]ClassificationRule) (code int, label string, provisionPct float64) {
	if status == "written_off" {
		if r, ok := thresholds["loss"]; ok {
			return r.Code, "loss", r.ProvisionPct
		}
		return 5, "loss", 100
	}
	for _, name := range []string{"loss", "doubtful", "substandard", "watch", "normal"} {
		r, ok := thresholds[name]
		if !ok {
			continue
		}
		if dpd >= r.DPDMin && (r.DPDMax == nil || dpd <= *r.DPDMax) {
			return r.Code, name, r.ProvisionPct
		}
	}
	return 1, "normal", 0
}

// ─────────── Layout + verification helpers ───────────

func (h *SASRAHandler) loadLayout(ctx context.Context, tenantID uuid.UUID) (*SASRAColumnLayout, error) {
	// Per-tenant override wins.
	var raw []byte
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT layout FROM tenant_sasra_column_overrides WHERE tenant_id = $1`, tenantID).Scan(&raw)
	})
	if err == nil {
		l := &SASRAColumnLayout{}
		if err := json.Unmarshal(raw, l); err != nil {
			return nil, err
		}
		return l, nil
	}
	// No override — return defaults.
	return h.loadDefaultLayout()
}

func (h *SASRAHandler) loadDefaultLayout() (*SASRAColumnLayout, error) {
	l := &SASRAColumnLayout{}
	if err := json.Unmarshal(defaultSASRAColumnsJSON, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (h *SASRAHandler) getVerification(ctx context.Context, tenantID uuid.UUID, period string) (bool, map[string]any, error) {
	verified := false
	out := map[string]any{}
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var by uuid.UUID
		var at time.Time
		var version string
		var note *string
		err := tx.QueryRow(ctx, `
			SELECT verified_by, verified_at, verified_form_version, note
			  FROM sasra_extract_verifications
			 WHERE tenant_id = $1 AND period = $2
		`, tenantID, period).Scan(&by, &at, &version, &note)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		verified = true
		out = map[string]any{
			"verified_by": by, "verified_at": at, "verified_form_version": version,
		}
		if note != nil {
			out["note"] = *note
		}
		return nil
	})
	return verified, out, err
}

// ─────────── CSV + PDF writers ───────────

func writeSASRACSV(w http.ResponseWriter, layout *SASRAColumnLayout, headerMeta map[string]string, rows []sasraRow, verified bool, period string, _ map[string]any) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"sasra_%s.csv\"", period))
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if !verified {
		_ = cw.Write([]string{"# DRAFT — verify column layout against current SASRA Form D before submission"})
		_ = cw.Write([]string{"# Layout source: " + layout.FormVersion})
		_ = cw.Write([]string{})
	}
	// Header metadata block.
	for _, k := range layout.HeaderMetadata {
		_ = cw.Write([]string{"# " + k, headerMeta[k]})
	}
	_ = cw.Write([]string{})

	// Column labels.
	labels := make([]string, len(layout.Columns))
	for i, c := range layout.Columns {
		labels[i] = c.Label
	}
	_ = cw.Write(labels)

	// Data rows.
	for _, row := range rows {
		vals := make([]string, len(layout.Columns))
		for i, c := range layout.Columns {
			vals[i] = fmt.Sprintf("%v", row[c.Key])
		}
		_ = cw.Write(vals)
	}
}

func writeSASRAPDF(w http.ResponseWriter, layout *SASRAColumnLayout, headerMeta map[string]string, rows []sasraRow, verified bool, period string, _ map[string]any) {
	pdf := gofpdf.New("L", "mm", "A4", "")
	pdf.SetHeaderFunc(func() {
		if !verified {
			// DRAFT watermark on every page.
			pdf.SetTextColor(220, 60, 60)
			pdf.SetFont("Helvetica", "B", 32)
			pdf.SetXY(0, 5)
			pdf.CellFormat(297, 10, "DRAFT — pending SASRA form verification", "", 0, "C", false, 0, "")
			pdf.SetTextColor(0, 0, 0)
		}
		pdf.SetFont("Helvetica", "B", 14)
		pdf.SetXY(10, 18)
		pdf.Cell(0, 8, "SASRA Quarterly Loan Portfolio Extract")
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetXY(10, 26)
		pdf.Cell(0, 6, headerMeta["tenant_name"]+" · Period: "+period)
		pdf.Ln(10)
	})

	pdf.AddPage()
	pdf.SetY(40)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Header metadata")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 9)
	for _, k := range layout.HeaderMetadata {
		pdf.Cell(60, 5, k)
		pdf.Cell(0, 5, headerMeta[k])
		pdf.Ln(5)
	}
	pdf.Ln(4)

	// Summary table — count by classification.
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Classification summary")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 9)
	cls := map[string]int{}
	totalOut := map[string]float64{}
	for _, row := range rows {
		l := fmt.Sprintf("%v", row["classification_label"])
		cls[l]++
		if f, ok := row["total_outstanding"].(string); ok {
			v, _ := strconv.ParseFloat(f, 64)
			totalOut[l] += v
		}
	}
	pdf.Cell(60, 6, "Classification")
	pdf.Cell(40, 6, "Count")
	pdf.Cell(60, 6, "Total outstanding")
	pdf.Ln(6)
	for _, name := range []string{"normal", "watch", "substandard", "doubtful", "loss"} {
		if n, ok := cls[name]; ok && n > 0 {
			pdf.Cell(60, 5, name)
			pdf.Cell(40, 5, strconv.Itoa(n))
			pdf.Cell(60, 5, fmt.Sprintf("%.2f", totalOut[name]))
			pdf.Ln(5)
		}
	}
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.MultiCell(0, 4,
		"This is a MANAGEMENT REPORT generated by nexusSacco. For the line-by-line "+
			"data SACCOs upload to SASRA's portal, use the CSV export from the same page.",
		"", "L", false)

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"sasra_%s.pdf\"", period))
	if err := pdf.Output(w); err != nil {
		httpx.WriteErr(w, nil, err)
		return
	}
}

// ─────────── Helpers ───────────

func currentQuarter() string {
	now := time.Now().UTC()
	q := (int(now.Month())-1)/3 + 1
	return fmt.Sprintf("%d-Q%d", now.Year(), q)
}

func validQuarter(s string) bool {
	if len(s) != 7 || s[4] != '-' || s[5] != 'Q' {
		return false
	}
	_, err := strconv.Atoi(s[:4])
	if err != nil {
		return false
	}
	q, err := strconv.Atoi(s[6:])
	if err != nil || q < 1 || q > 4 {
		return false
	}
	return true
}

func periodBounds(period string) (time.Time, time.Time, error) {
	if !validQuarter(period) {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid period")
	}
	year, _ := strconv.Atoi(period[:4])
	q, _ := strconv.Atoi(period[6:])
	from := time.Date(year, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 3, 0).Add(-time.Nanosecond)
	return from, to, nil
}
