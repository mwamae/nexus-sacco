// Loans Phase 3 — per-tenant loans-policy admin surface.
//
// Mounted at /v1/loans/policy/* with permission loans:policy:write
// (read uses loans:view).
//
// Exposes:
//
//   GET  /v1/loans/policy
//        Returns thresholds + ECL matrix (latest effective_from row
//        per (sasra, stage)).
//
//   PUT  /v1/loans/policy/thresholds  {sasra_watch_dpd, dpd_substandard_days,
//                                       dpd_doubtful_days, dpd_loss_days,
//                                       ifrs9_stage2_dpd, ifrs9_stage3_dpd}
//        Updates the six DPD threshold columns on tenant_operations.
//
//   PUT  /v1/loans/policy/ecl-matrix  {rows: [...]}
//        Inserts a new effective_from=today row per (sasra, stage)
//        only when the rate differs from the current row. Old rows
//        stay so the audit trail of rate history is preserved.
//
// Classification timeline endpoint (loan detail tab):
//
//   GET  /v1/loans/{loan_id}/classification-history  loans:view

package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type LoanPolicyHandler struct {
	Pool   *db.Pool
	Logger *slog.Logger
}

type thresholdsPayload struct {
	SASRAWatchDPD       int `json:"sasra_watch_dpd"`
	DPDSubstandardDays  int `json:"dpd_substandard_days"`
	DPDDoubtfulDays     int `json:"dpd_doubtful_days"`
	DPDLossDays         int `json:"dpd_loss_days"`
	IFRS9Stage2DPD      int `json:"ifrs9_stage2_dpd"`
	IFRS9Stage3DPD      int `json:"ifrs9_stage3_dpd"`
}

type eclRow struct {
	ClassificationSASRA string          `json:"classification_sasra"`
	ClassificationStage int             `json:"classification_ifrs9_stage"`
	ECLRatePct          decimal.Decimal `json:"ecl_rate_pct"`
	EffectiveFrom       string          `json:"effective_from,omitempty"`
	Notes               *string         `json:"notes,omitempty"`
}

type policySnapshot struct {
	Thresholds           thresholdsPayload `json:"thresholds"`
	ECLMatrix            []eclRow          `json:"ecl_matrix"`
	DividendOffsetPolicy string            `json:"dividend_offset_policy"` // disabled | manual_preview | automatic
}

func (h *LoanPolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var snap policySnapshot
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			SELECT sasra_watch_dpd, dpd_substandard_days, dpd_doubtful_days,
			       dpd_loss_days, ifrs9_stage2_dpd, ifrs9_stage3_dpd,
			       dividend_offset_policy
			  FROM tenant_operations
		`).Scan(
			&snap.Thresholds.SASRAWatchDPD,
			&snap.Thresholds.DPDSubstandardDays,
			&snap.Thresholds.DPDDoubtfulDays,
			&snap.Thresholds.DPDLossDays,
			&snap.Thresholds.IFRS9Stage2DPD,
			&snap.Thresholds.IFRS9Stage3DPD,
			&snap.DividendOffsetPolicy,
		); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT DISTINCT ON (classification_sasra, classification_ifrs9_stage)
			       classification_sasra, classification_ifrs9_stage,
			       ecl_rate_pct, effective_from, notes
			  FROM ecl_rate_matrix
			 ORDER BY classification_sasra, classification_ifrs9_stage, effective_from DESC
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row eclRow
			var ef string
			if err := rows.Scan(&row.ClassificationSASRA, &row.ClassificationStage,
				&row.ECLRatePct, &ef, &row.Notes); err != nil {
				return err
			}
			row.EffectiveFrom = ef
			snap.ECLMatrix = append(snap.ECLMatrix, row)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, snap)
}

func (h *LoanPolicyHandler) UpdateThresholds(w http.ResponseWriter, r *http.Request) {
	var in thresholdsPayload
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	// Validate ordering — strict ascending within SASRA and within IFRS 9.
	if !(in.SASRAWatchDPD >= 1 && in.SASRAWatchDPD < in.DPDSubstandardDays &&
		in.DPDSubstandardDays < in.DPDDoubtfulDays &&
		in.DPDDoubtfulDays < in.DPDLossDays) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("SASRA thresholds must be 1 <= watch < substandard < doubtful < loss"))
		return
	}
	if !(in.IFRS9Stage2DPD < in.IFRS9Stage3DPD) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("ifrs9_stage2_dpd must be less than ifrs9_stage3_dpd"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE tenant_operations SET
			  sasra_watch_dpd      = $1,
			  dpd_substandard_days = $2,
			  dpd_doubtful_days    = $3,
			  dpd_loss_days        = $4,
			  ifrs9_stage2_dpd     = $5,
			  ifrs9_stage3_dpd     = $6,
			  updated_at           = now()
		`, in.SASRAWatchDPD, in.DPDSubstandardDays, in.DPDDoubtfulDays,
			in.DPDLossDays, in.IFRS9Stage2DPD, in.IFRS9Stage3DPD)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type updateMatrixReq struct {
	Rows []eclRow `json:"rows"`
}

func (h *LoanPolicyHandler) UpdateMatrix(w http.ResponseWriter, r *http.Request) {
	var in updateMatrixReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if len(in.Rows) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("rows is required"))
		return
	}
	for _, row := range in.Rows {
		if row.ECLRatePct.IsNegative() || row.ECLRatePct.GreaterThan(decimal.NewFromInt(1)) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("ecl_rate_pct must be between 0 and 1.0"))
			return
		}
	}

	tid, _ := middleware.TenantIDFrom(r)
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		for _, row := range in.Rows {
			// Only insert if the current row has a different rate —
			// avoids pointless audit churn when the form is saved
			// without changes.
			var currentRate decimal.Decimal
			err := tx.QueryRow(r.Context(), `
				SELECT ecl_rate_pct FROM ecl_rate_matrix
				 WHERE classification_sasra = $1
				   AND classification_ifrs9_stage = $2
				 ORDER BY effective_from DESC LIMIT 1
			`, row.ClassificationSASRA, row.ClassificationStage).Scan(&currentRate)
			if err == nil && currentRate.Equal(row.ECLRatePct) {
				continue
			}
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO ecl_rate_matrix (
				  tenant_id, classification_sasra, classification_ifrs9_stage,
				  ecl_rate_pct, effective_from, notes
				) VALUES ($1, $2, $3, $4, CURRENT_DATE, $5)
				ON CONFLICT (tenant_id, classification_sasra, classification_ifrs9_stage, effective_from)
				DO UPDATE SET ecl_rate_pct = EXCLUDED.ecl_rate_pct, notes = EXCLUDED.notes
			`, tid, row.ClassificationSASRA, row.ClassificationStage, row.ECLRatePct, row.Notes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Classification timeline ───────────

func (h *LoanPolicyHandler) LoanClassificationHistory(w http.ResponseWriter, r *http.Request) {
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	type historyRow struct {
		ChangedAt       string  `json:"changed_at"`
		PrevSASRA       *string `json:"prev_sasra,omitempty"`
		NewSASRA        string  `json:"new_sasra"`
		PrevIFRS9Stage  *int    `json:"prev_ifrs9_stage,omitempty"`
		NewIFRS9Stage   int     `json:"new_ifrs9_stage"`
		DPDDays         int     `json:"dpd_days"`
		TriggerSource   string  `json:"trigger_source"`
	}
	var out []historyRow
	err = h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT changed_at::text, prev_sasra, new_sasra,
			       prev_ifrs9_stage, new_ifrs9_stage, dpd_days, trigger_source
			  FROM loan_classification_history
			 WHERE loan_id = $1
			 ORDER BY changed_at DESC
			 LIMIT 200
		`, loanID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h historyRow
			if err := rows.Scan(&h.ChangedAt, &h.PrevSASRA, &h.NewSASRA,
				&h.PrevIFRS9Stage, &h.NewIFRS9Stage, &h.DPDDays, &h.TriggerSource); err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

// ─────────── Dividend offset policy ───────────

type dividendOffsetPolicyReq struct {
	Policy string `json:"policy"` // disabled | manual_preview | automatic
}

func (h *LoanPolicyHandler) UpdateDividendOffsetPolicy(w http.ResponseWriter, r *http.Request) {
	var in dividendOffsetPolicyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	switch in.Policy {
	case "disabled", "manual_preview", "automatic":
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("policy must be disabled | manual_preview | automatic"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err := h.Pool.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE tenant_operations SET
			  dividend_offset_policy = $1,
			  updated_at             = now()
		`, in.Policy)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
