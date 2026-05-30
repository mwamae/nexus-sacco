// Phase 1.5b — collateral reports.
//
// Five endpoints mounted under /v1/loans/reports/collateral-*. Each
// returns a JSON payload + has a CSV sibling under /csv/.
//
//   GET /v1/loans/reports/collateral-exposure        — per-loan coverage gap
//   GET /v1/loans/reports/collateral-by-kind         — portfolio split by kind
//   GET /v1/loans/reports/collateral-valuations-expiring — revaluation queue
//   GET /v1/loans/reports/collateral-insurance-expiring  — insurance renewal queue
//   GET /v1/loans/reports/collateral-charge-status   — pledged-without-charge cleanup queue
//
// All five reuse the Phase 2 reports infrastructure pattern (tenant-
// scoped tx + httpx.OK or CSV writer; permission gated on loans:reports).

package handler

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type CollateralReportsHandler struct {
	DB     *db.Pool
	Logger *slog.Logger
}

// ─────────── 1. Exposure — under-secured loans ───────────

type collateralExposureRow struct {
	LoanID         uuid.UUID `json:"loan_id"`
	LoanNo         string    `json:"loan_no"`
	MemberName     string    `json:"member_name"`
	ProductName    string    `json:"product_name"`
	Outstanding    string    `json:"outstanding"`
	GuarantorCover string    `json:"guarantor_cover"`
	CollateralFSV  string    `json:"collateral_fsv"`
	SecurityModel  string    `json:"security_model"`
	Shortfall      string    `json:"shortfall"` // positive = under-secured
}

func (h *CollateralReportsHandler) Exposure(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	rows, err := h.loadExposure(r, tid)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": rows, "total": len(rows)})
}

func (h *CollateralReportsHandler) ExposureCSV(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	rows, err := h.loadExposure(r, tid)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="collateral-exposure.csv"`)
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"loan_no", "member_name", "product", "outstanding", "guarantor_cover", "collateral_fsv", "security_model", "shortfall"})
	for _, row := range rows {
		_ = cw.Write([]string{row.LoanNo, row.MemberName, row.ProductName,
			row.Outstanding, row.GuarantorCover, row.CollateralFSV,
			row.SecurityModel, row.Shortfall})
	}
}

func (h *CollateralReportsHandler) loadExposure(r *http.Request, tid uuid.UUID) ([]collateralExposureRow, error) {
	var out []collateralExposureRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT l.id, l.loan_no,
			       COALESCE(cd.full_name, ''),
			       p.name, p.security_model,
			       (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance)::text AS outstanding,
			       COALESCE((SELECT SUM(g.amount_guaranteed)
			                   FROM loan_guarantees g
			                  WHERE g.application_id = l.application_id
			                    AND g.status = 'accepted'), 0)::text AS guarantor_cover,
			       COALESCE((SELECT SUM(c.forced_sale_value)
			                   FROM loan_collateral c
			                  WHERE c.application_id = l.application_id
			                    AND c.status = 'pledged'
			                    AND c.forced_sale_value IS NOT NULL), 0)::text AS collateral_fsv
			  FROM loans l
			  JOIN loan_products p ON p.id = l.product_id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			 WHERE l.status IN ('active','in_arrears','restructured','defaulted')
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row collateralExposureRow
			if err := rows.Scan(&row.LoanID, &row.LoanNo, &row.MemberName,
				&row.ProductName, &row.SecurityModel,
				&row.Outstanding, &row.GuarantorCover, &row.CollateralFSV); err != nil {
				return err
			}
			// Shortfall = outstanding - (gCover + cFSV). Positive = under-secured.
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Compute shortfall + sort.
	for i := range out {
		os, _ := strconv.ParseFloat(out[i].Outstanding, 64)
		gc, _ := strconv.ParseFloat(out[i].GuarantorCover, 64)
		cf, _ := strconv.ParseFloat(out[i].CollateralFSV, 64)
		short := os - (gc + cf)
		out[i].Shortfall = fmt.Sprintf("%.2f", short)
	}
	// Sort descending by shortfall.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, _ := strconv.ParseFloat(out[i].Shortfall, 64)
			b, _ := strconv.ParseFloat(out[j].Shortfall, 64)
			if b > a {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// ─────────── 2. By-kind distribution ───────────

type byKindRow struct {
	Kind        string `json:"kind"`
	ItemCount   int    `json:"item_count"`
	TotalFSV    string `json:"total_fsv"`
}

func (h *CollateralReportsHandler) ByKind(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out []byKindRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT kind::text, COUNT(*),
			       COALESCE(SUM(forced_sale_value), 0)::text
			  FROM loan_collateral
			 WHERE status IN ('pledged','valued')
			 GROUP BY kind
			 ORDER BY SUM(forced_sale_value) DESC NULLS LAST
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row byKindRow
			if err := rows.Scan(&row.Kind, &row.ItemCount, &row.TotalFSV); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

// ─────────── 3. Valuations expiring ───────────

type valuationExpiringRow struct {
	CollateralID uuid.UUID `json:"collateral_id"`
	LoanNo       string    `json:"loan_no"`
	MemberName   string    `json:"member_name"`
	Kind         string    `json:"kind"`
	Description  string    `json:"description"`
	ExpiresAt    string    `json:"expires_at"`
	DaysToExpiry int       `json:"days_to_expiry"`
}

func (h *CollateralReportsHandler) ValuationsExpiring(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	days := parseDaysQuery(r, 90)
	var out []valuationExpiringRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT v.collateral_id, COALESCE(l.loan_no, a.application_no),
			       COALESCE(cd.full_name, ''),
			       c.kind::text, c.description,
			       v.expires_at::text,
			       (v.expires_at - CURRENT_DATE)::int
			  FROM collateral_valuations v
			  JOIN loan_collateral c ON c.id = v.collateral_id
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN loans l ON l.application_id = a.id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			 WHERE v.is_current = true
			   AND v.expires_at IS NOT NULL
			   AND v.expires_at <= CURRENT_DATE + ($1 || ' days')::interval
			 ORDER BY v.expires_at ASC
		`, fmt.Sprintf("%d", days))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row valuationExpiringRow
			if err := rows.Scan(&row.CollateralID, &row.LoanNo, &row.MemberName,
				&row.Kind, &row.Description, &row.ExpiresAt, &row.DaysToExpiry); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out), "window_days": days})
}

// ─────────── 4. Insurance expiring ───────────

type insuranceExpiringRow struct {
	CollateralID uuid.UUID `json:"collateral_id"`
	LoanNo       string    `json:"loan_no"`
	MemberName   string    `json:"member_name"`
	Kind         string    `json:"kind"`
	Description  string    `json:"description"`
	Provider     string    `json:"provider"`
	PolicyNo     string    `json:"policy_no"`
	ExpiresAt    string    `json:"expires_at"`
	DaysToExpiry int       `json:"days_to_expiry"`
	Status       string    `json:"status"`
}

func (h *CollateralReportsHandler) InsuranceExpiring(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	days := parseDaysQuery(r, 30)
	var out []insuranceExpiringRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT p.collateral_id, COALESCE(l.loan_no, a.application_no),
			       COALESCE(cd.full_name, ''),
			       c.kind::text, c.description,
			       p.provider_name, p.policy_no,
			       p.effective_to::text,
			       (p.effective_to - CURRENT_DATE)::int,
			       p.status
			  FROM collateral_insurance_policies p
			  JOIN loan_collateral c ON c.id = p.collateral_id
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN loans l ON l.application_id = a.id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			 WHERE p.is_current = true
			   AND p.effective_to <= CURRENT_DATE + ($1 || ' days')::interval
			 ORDER BY p.effective_to ASC
		`, fmt.Sprintf("%d", days))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row insuranceExpiringRow
			if err := rows.Scan(&row.CollateralID, &row.LoanNo, &row.MemberName,
				&row.Kind, &row.Description, &row.Provider, &row.PolicyNo,
				&row.ExpiresAt, &row.DaysToExpiry, &row.Status); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out), "window_days": days})
}

// ─────────── 5. Charge registration status ───────────

type chargeStatusRow struct {
	CollateralID    uuid.UUID `json:"collateral_id"`
	LoanNo          string    `json:"loan_no"`
	MemberName      string    `json:"member_name"`
	Kind            string    `json:"kind"`
	Description     string    `json:"description"`
	Status          string    `json:"status"`
	ChargeRequired  bool      `json:"charge_required"`
	ChargeRegistry  string    `json:"charge_registry"`
	ChargeRefNumber string    `json:"charge_reference"`
	ChargeRegistered bool     `json:"charge_registered"`
	DaysSincePledge int       `json:"days_since_pledge"`
}

func (h *CollateralReportsHandler) ChargeRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out []chargeStatusRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Tenant policy — which kinds require charge?
		var requiredKinds []string
		_ = tx.QueryRow(r.Context(), `
			SELECT COALESCE(collateral_charge_required_kinds, ARRAY[]::text[])
			  FROM tenant_operations LIMIT 1
		`).Scan(&requiredKinds)

		rows, err := tx.Query(r.Context(), `
			SELECT c.id, COALESCE(l.loan_no, a.application_no),
			       COALESCE(cd.full_name, ''),
			       c.kind::text, c.description, c.status,
			       COALESCE(c.charge_registry::text, ''),
			       COALESCE(c.charge_reference, ''),
			       c.charge_registered_at IS NOT NULL,
			       (CURRENT_DATE - c.pledged_at::date)::int
			  FROM loan_collateral c
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN loans l ON l.application_id = a.id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			 WHERE c.status = 'pledged'
			 ORDER BY c.pledged_at ASC
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row chargeStatusRow
			if err := rows.Scan(&row.CollateralID, &row.LoanNo, &row.MemberName,
				&row.Kind, &row.Description, &row.Status,
				&row.ChargeRegistry, &row.ChargeRefNumber, &row.ChargeRegistered,
				&row.DaysSincePledge); err != nil {
				return err
			}
			row.ChargeRequired = stringInSlice(row.Kind, requiredKinds)
			// Filter — only show rows that need attention: required but
			// not yet registered.
			if row.ChargeRequired && !row.ChargeRegistered {
				out = append(out, row)
			}
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

// ─────────── helpers ───────────

func parseDaysQuery(r *http.Request, fallback int) int {
	q := strings.TrimSpace(r.URL.Query().Get("days"))
	if q == "" {
		return fallback
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 1 || n > 365 {
		return fallback
	}
	return n
}

func stringInSlice(needle string, haystack []string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Force time import for any future date math here.
var _ = time.Now
