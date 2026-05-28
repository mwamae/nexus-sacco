// Loans Phase 2 — 10 reporting endpoints + caching + CSV.
//
//   GET /v1/loans/reports/par
//   GET /v1/loans/reports/par/history
//   GET /v1/loans/reports/aging-buckets
//   GET /v1/loans/reports/vintage?from=YYYY-MM&to=YYYY-MM
//   GET /v1/loans/reports/officers?from=&to=
//   GET /v1/loans/reports/disbursements?from=&to=&product_id=&channel=
//   GET /v1/loans/reports/repayments?from=&to=&product_id=&channel=
//   GET /v1/loans/reports/guarantor-exposure?member_id=...
//   GET /v1/loans/reports/top-n?metric=outstanding|disbursed|collected&limit=50
//   GET /v1/loans/reports/portfolio/history?days=90
//
// Every endpoint accepts `format=json|csv`. CSV is generated inline
// (no extra dep — encoding/csv handles it). Each tab in the UI calls
// the JSON form for rendering + offers a CSV button for download.
//
// Caching: per-endpoint TTL per the prompt's matrix. X-Cache: HIT
// header on cache hit so the UI can show "Last computed Ns ago".

package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type LoanReportsPhase2Handler struct {
	DB    *db.Pool
	Store *store.LoanReportsStore

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at   time.Time
	data any
}

// Per-endpoint TTL matrix — mirrors the prompt's spec.
const (
	ttlAging      = 30 * time.Second
	ttlVintage    = 1 * time.Hour
	ttlOfficers   = 1 * time.Minute
	ttlTopN       = 1 * time.Minute
	ttlGuarantors = 5 * time.Minute
	ttlHistory    = 5 * time.Minute
)

// getCached pulls from cache if fresh; otherwise calls compute, stores,
// returns. The `hit` return tells the handler whether to set X-Cache: HIT.
func (h *LoanReportsPhase2Handler) getCached(key string, ttl time.Duration, compute func() (any, error)) (data any, hit bool, err error) {
	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[string]cacheEntry{}
	}
	if e, ok := h.cache[key]; ok && time.Since(e.at) < ttl {
		h.mu.Unlock()
		return e.data, true, nil
	}
	h.mu.Unlock()

	d, err := compute()
	if err != nil {
		return nil, false, err
	}
	h.mu.Lock()
	h.cache[key] = cacheEntry{at: time.Now(), data: d}
	h.mu.Unlock()
	return d, false, nil
}

// writeResponse routes between JSON and CSV based on ?format= query.
// Each CSV writer is responsible for its own header row + serialisation.
func (h *LoanReportsPhase2Handler) writeResponse(w http.ResponseWriter, r *http.Request, data any, hit bool, csvFn func(io *csv.Writer, data any) error) {
	if hit {
		w.Header().Set("X-Cache", "HIT")
	}
	if r.URL.Query().Get("format") == "csv" && csvFn != nil {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"report.csv\"")
		cw := csv.NewWriter(w)
		if err := csvFn(cw, data); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		cw.Flush()
		return
	}
	httpx.OK(w, data)
}

// parseReportDateRange reads from=, to= (RFC3339 dates or YYYY-MM-DD); defaults to
// last 30 days if missing.
func parseReportDateRange(r *http.Request) (time.Time, time.Time) {
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -30)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := parseDateOrTime(v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := parseDateOrTime(v); err == nil {
			to = t
		}
	}
	return from, to
}

func parseDateOrTime(v string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01", v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

// ─────────── PAR ───────────

func (h *LoanReportsPhase2Handler) PAR(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	// PAR is cheap — no cache.
	var data *store.PARSummary
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		d, err := h.Store.PARTx(r.Context(), tx)
		if err != nil {
			return err
		}
		data = d
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, false, parCSV)
}

func parCSV(cw *csv.Writer, data any) error {
	d := data.(*store.PARSummary)
	if err := cw.Write([]string{"metric", "value"}); err != nil {
		return err
	}
	rows := [][]string{
		{"total_principal", d.TotalPrincipal},
		{"total_outstanding", d.TotalOutstanding},
		{"par_1_principal", d.Par1Principal},
		{"par_30_principal", d.Par30Principal},
		{"par_90_principal", d.Par90Principal},
		{"par_1_pct", d.Par1Pct},
		{"par_30_pct", d.Par30Pct},
		{"par_90_pct", d.Par90Pct},
	}
	for _, r := range rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	for _, p := range d.ByProduct {
		_ = cw.Write([]string{"product:" + p.ProductName + ":principal", p.TotalPrincipal})
		_ = cw.Write([]string{"product:" + p.ProductName + ":par_30_pct", p.Par30Pct})
	}
	return nil
}

// ─────────── Aging buckets ───────────

func (h *LoanReportsPhase2Handler) Aging(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	key := "aging:" + tenantID.String()
	data, hit, err := h.getCached(key, ttlAging, func() (any, error) {
		var d *store.AgingBucketsReport
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.AgingBucketsTx(r.Context(), tx)
			if err != nil {
				return err
			}
			d = res
			return nil
		})
		return d, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, agingCSV)
}

func agingCSV(cw *csv.Writer, data any) error {
	d := data.(*store.AgingBucketsReport)
	_ = cw.Write([]string{"bucket", "dpd_min", "dpd_max", "count", "principal", "interest", "penalty", "total"})
	for _, b := range d.Buckets {
		hi := "open"
		if b.DPDMax != nil {
			hi = strconv.Itoa(*b.DPDMax)
		}
		_ = cw.Write([]string{b.Label, strconv.Itoa(b.DPDMin), hi, strconv.Itoa(b.Count), b.Principal, b.Interest, b.Penalty, b.Total})
	}
	return nil
}

// ─────────── Vintage ───────────

func (h *LoanReportsPhase2Handler) Vintage(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	from, to := parseReportDateRange(r)
	// Default vintage window: last 24 months.
	if from.IsZero() {
		from = time.Now().UTC().AddDate(-2, 0, 0)
	}
	key := fmt.Sprintf("vintage:%s:%s:%s", tenantID, from.Format("2006-01"), to.Format("2006-01"))
	data, hit, err := h.getCached(key, ttlVintage, func() (any, error) {
		var d *store.VintageReport
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.VintageTx(r.Context(), tx, from, to)
			if err != nil {
				return err
			}
			d = res
			return nil
		})
		return d, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, vintageCSV)
}

func vintageCSV(cw *csv.Writer, data any) error {
	d := data.(*store.VintageReport)
	_ = cw.Write([]string{"disbursement_month", "disbursed_count", "disbursed_amount", "months_on_book", "par_30_pct", "par_90_pct", "write_off_pct"})
	for _, c := range d.Cohorts {
		for _, p := range c.Performance {
			_ = cw.Write([]string{c.DisbursementMonth, strconv.Itoa(c.DisbursedCount), c.DisbursedAmount, strconv.Itoa(p.MonthsOnBook), p.Par30Pct, p.Par90Pct, p.WriteOffPct})
		}
	}
	return nil
}

// ─────────── Officers ───────────

func (h *LoanReportsPhase2Handler) Officers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	from, to := parseReportDateRange(r)
	key := fmt.Sprintf("officers:%s:%s:%s", tenantID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	data, hit, err := h.getCached(key, ttlOfficers, func() (any, error) {
		var rows []store.OfficerRow
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.OfficersTx(r.Context(), tx, from, to)
			if err != nil {
				return err
			}
			rows = res
			return nil
		})
		return map[string]any{"officers": rows, "from": from, "to": to}, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, officersCSV)
}

func officersCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	rows := m["officers"].([]store.OfficerRow)
	_ = cw.Write([]string{"officer", "disbursed_count", "disbursed_amount", "collected_amount", "par_30", "write_off_amount"})
	for _, o := range rows {
		_ = cw.Write([]string{o.UserName, strconv.Itoa(o.DisbursedCount), o.DisbursedAmount, o.CollectedAmount, o.Par30Pct, o.WriteOffAmount})
	}
	return nil
}

// ─────────── Disbursements ───────────

func (h *LoanReportsPhase2Handler) Disbursements(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	from, to := parseReportDateRange(r)
	var productID *uuid.UUID
	if v := r.URL.Query().Get("product_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			productID = &id
		}
	}
	channel := r.URL.Query().Get("channel")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	// No cache — register rows can move minute-to-minute.
	var rows []store.DisbursementRow
	var total int
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		res, n, err := h.Store.DisbursementsTx(r.Context(), tx, from, to, productID, channel, limit, offset)
		if err != nil {
			return err
		}
		rows = res
		total = n
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	data := map[string]any{
		"rows": rows, "total": total,
		"page_size": ifZero(limit, 100), "has_more": offset+len(rows) < total,
	}
	h.writeResponse(w, r, data, false, disbursementsCSV)
}

func disbursementsCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	rows := m["rows"].([]store.DisbursementRow)
	_ = cw.Write([]string{"loan_no", "member_no", "member_name", "product", "amount", "channel", "disbursed_at", "officer"})
	for _, r := range rows {
		ch := ""
		if r.Channel != nil {
			ch = *r.Channel
		}
		_ = cw.Write([]string{r.LoanNo, r.MemberNo, r.MemberName, r.Product, r.Amount, ch, r.DisbursedAt.Format(time.RFC3339), r.OfficerName})
	}
	return nil
}

// ─────────── Repayments ───────────

func (h *LoanReportsPhase2Handler) Repayments(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	from, to := parseReportDateRange(r)
	var productID *uuid.UUID
	if v := r.URL.Query().Get("product_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			productID = &id
		}
	}
	channel := r.URL.Query().Get("channel")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	var rows []store.RepaymentRow
	var total int
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		res, n, err := h.Store.RepaymentsTx(r.Context(), tx, from, to, productID, channel, limit, offset)
		if err != nil {
			return err
		}
		rows = res
		total = n
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	data := map[string]any{
		"rows": rows, "total": total,
		"page_size": ifZero(limit, 100), "has_more": offset+len(rows) < total,
	}
	h.writeResponse(w, r, data, false, repaymentsCSV)
}

func repaymentsCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	rows := m["rows"].([]store.RepaymentRow)
	_ = cw.Write([]string{"loan_no", "member_name", "amount", "channel", "principal", "interest", "fees", "penalty", "posted_at", "officer"})
	for _, r := range rows {
		ch := ""
		if r.Channel != nil {
			ch = *r.Channel
		}
		_ = cw.Write([]string{r.LoanNo, r.MemberName, r.Amount, ch, r.Principal, r.Interest, r.Fees, r.Penalty, r.PostedAt.Format(time.RFC3339), r.OfficerName})
	}
	return nil
}

// ─────────── Guarantor exposure ───────────

func (h *LoanReportsPhase2Handler) GuarantorExposure(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	var memberID *uuid.UUID
	if v := r.URL.Query().Get("member_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			memberID = &id
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	key := fmt.Sprintf("guarantor-exposure:%s:%v:%d", tenantID, memberID, limit)
	data, hit, err := h.getCached(key, ttlGuarantors, func() (any, error) {
		var rows []store.GuarantorExposureRow
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.GuarantorExposureTx(r.Context(), tx, memberID, limit)
			if err != nil {
				return err
			}
			rows = res
			return nil
		})
		return map[string]any{"rows": rows}, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, guarantorCSV)
}

func guarantorCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	rows := m["rows"].([]store.GuarantorExposureRow)
	_ = cw.Write([]string{"guarantor_no", "guarantor_name", "total_guaranteed", "active_count"})
	for _, g := range rows {
		_ = cw.Write([]string{g.GuarantorNo, g.GuarantorName, g.TotalGuaranteed, strconv.Itoa(g.ActiveCount)})
	}
	return nil
}

// ─────────── Top-N ───────────

func (h *LoanReportsPhase2Handler) TopN(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	metric := strings.ToLower(r.URL.Query().Get("metric"))
	if metric == "" {
		metric = "outstanding"
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	key := fmt.Sprintf("top-n:%s:%s:%d", tenantID, metric, limit)
	data, hit, err := h.getCached(key, ttlTopN, func() (any, error) {
		var rows []store.TopNRow
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.TopNTx(r.Context(), tx, metric, limit)
			if err != nil {
				return err
			}
			rows = res
			return nil
		})
		return map[string]any{"rows": rows, "metric": metric}, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, topNCSV)
}

func topNCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	rows := m["rows"].([]store.TopNRow)
	metric := m["metric"].(string)
	_ = cw.Write([]string{"member_no", "member_name", metric})
	for _, r := range rows {
		_ = cw.Write([]string{r.MemberNo, r.MemberName, r.Value})
	}
	return nil
}

// ─────────── PAR history (snapshot read) ───────────

func (h *LoanReportsPhase2Handler) PARHistory(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant required"))
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 90
	}
	key := fmt.Sprintf("par-history:%s:%d", tenantID, days)
	data, hit, err := h.getCached(key, ttlHistory, func() (any, error) {
		var pts []store.SnapshotPoint
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			res, err := h.Store.PARHistoryTx(r.Context(), tx, days)
			if err != nil {
				return err
			}
			pts = res
			return nil
		})
		return map[string]any{"points": pts, "days": days}, err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.writeResponse(w, r, data, hit, parHistoryCSV)
}

func parHistoryCSV(cw *csv.Writer, data any) error {
	m := data.(map[string]any)
	pts := m["points"].([]store.SnapshotPoint)
	_ = cw.Write([]string{"snapshot_date", "total_principal", "total_outstanding", "par_1", "par_30", "par_90", "active_count"})
	for _, p := range pts {
		_ = cw.Write([]string{p.SnapshotDate, p.TotalPrincipal, p.TotalOutstanding, p.Par1Pct, p.Par30Pct, p.Par90Pct, strconv.Itoa(p.ActiveCount)})
	}
	return nil
}

// ─────────── Portfolio history (same shape, kept separate for clarity) ───────────

func (h *LoanReportsPhase2Handler) PortfolioHistory(w http.ResponseWriter, r *http.Request) {
	// Same snapshot table; returns the same shape. Kept under its own
	// URL so a future Phase can diverge the response (e.g. add
	// per-product breakdown) without affecting PAR history.
	h.PARHistory(w, r)
}

func ifZero(n, def int) int {
	if n == 0 {
		return def
	}
	return n
}

// Silence unused-import warnings when a build flag prunes a branch.
var _ context.Context = context.Background()
var _ json.Marshaler
