// GET /v1/loans/dashboard — Phase 1 aggregator for the Loans →
// Dashboard page. Returns every KPI the page renders in one round
// trip so the dashboard doesn't fire ten parallel requests.
//
// All numbers come from existing tables; this handler does NOT
// introduce new business logic. The Phase 3 DPD engine will replace
// the inline "next_due_date < now()" heuristic for at_risk_count;
// the Phase 4 PTPs work will fill promises_due_this_week_count.
//
// Caching: 30s in-process cache keyed by tenant. A dashboard refresh
// hits cache for 29 of every 30 seconds; only the first request after
// the TTL expires runs the underlying queries. Cache misses are not
// deduped (the queries are cheap — total ~6 indexed counts + 3 sums
// + 1 product breakdown — so a thundering herd costs ~10ms per
// duplicate caller).
//
// Permission: loans:view. Mounted in routes.go under the standard
// authenticated + tenant-required group.

package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type LoanDashboardHandler struct {
	DB *db.Pool

	mu    sync.Mutex
	cache map[uuid.UUID]cachedDashboard
}

type cachedDashboard struct {
	at   time.Time
	data *LoanDashboardResponse
}

const loanDashboardCacheTTL = 30 * time.Second

// LoanDashboardResponse is the on-the-wire shape the LoansDashboard
// page consumes. Field names match the prompt's specified shape so
// the React component can read them verbatim.
type LoanDashboardResponse struct {
	AsOf                         time.Time            `json:"as_of"`
	TotalOutstanding             TotalOutstandingKPIs `json:"total_outstanding"`
	ByProduct                    []ProductOutstanding `json:"by_product"`
	ByStatus                     map[string]int       `json:"by_status"`
	DisbursedThisMonth           string               `json:"disbursed_this_month"`
	CollectedThisMonth           string               `json:"collected_this_month"`
	ApplicationsByStatus         map[string]int       `json:"applications_by_status"`
	ApproachingDisbursementCount int                  `json:"approaching_disbursement_count"`
	AtRiskCount                  int                  `json:"at_risk_count"`
	PromisesDueThisWeekCount     int                  `json:"promises_due_this_week_count"`
}

type TotalOutstandingKPIs struct {
	PrincipalBalance string `json:"principal_balance"`
	InterestBalance  string `json:"interest_balance"`
	FeesBalance      string `json:"fees_balance"`
	PenaltyBalance   string `json:"penalty_balance"`
	ActiveCount      int    `json:"active_count"`
}

type ProductOutstanding struct {
	ProductID   uuid.UUID `json:"product_id"`
	ProductName string    `json:"product_name"`
	Outstanding string    `json:"outstanding"`
	ActiveCount int       `json:"active_count"`
}

// Get is the HTTP entry point. Permission gating is applied in routes.go.
func (h *LoanDashboardHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	if tenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant context required"))
		return
	}
	resp, err := h.snapshot(r.Context(), tenantID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// snapshot returns the cached dashboard or runs a fresh fan-out.
// Per-tenant cache; concurrent first-callers may produce duplicate
// queries (acceptable — see header).
func (h *LoanDashboardHandler) snapshot(ctx context.Context, tenantID uuid.UUID) (*LoanDashboardResponse, error) {
	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[uuid.UUID]cachedDashboard{}
	}
	if c, ok := h.cache[tenantID]; ok && time.Since(c.at) < loanDashboardCacheTTL {
		h.mu.Unlock()
		return c.data, nil
	}
	h.mu.Unlock()

	data, err := h.compute(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.cache[tenantID] = cachedDashboard{at: time.Now(), data: data}
	h.mu.Unlock()
	return data, nil
}

func (h *LoanDashboardHandler) compute(ctx context.Context, tenantID uuid.UUID) (*LoanDashboardResponse, error) {
	resp := &LoanDashboardResponse{
		AsOf:                 time.Now().UTC(),
		ByStatus:             map[string]int{},
		ApplicationsByStatus: map[string]int{},
		ByProduct:            []ProductOutstanding{},
	}
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// 1. Total outstanding + active count.
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(principal_balance),0)::text,
			       COALESCE(SUM(interest_balance),0)::text,
			       COALESCE(SUM(fees_balance),0)::text,
			       COALESCE(SUM(penalty_balance),0)::text,
			       count(*) FILTER (WHERE status IN ('active','in_arrears','restructured'))
			  FROM loans
			 WHERE status IN ('active','in_arrears','restructured','pending_disbursement')
		`).Scan(
			&resp.TotalOutstanding.PrincipalBalance,
			&resp.TotalOutstanding.InterestBalance,
			&resp.TotalOutstanding.FeesBalance,
			&resp.TotalOutstanding.PenaltyBalance,
			&resp.TotalOutstanding.ActiveCount,
		); err != nil {
			return err
		}

		// 2. By-product breakdown — one row per active product.
		rows, err := tx.Query(ctx, `
			SELECT l.product_id, p.name,
			       COALESCE(SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance),0)::text,
			       count(*) FILTER (WHERE l.status IN ('active','in_arrears','restructured'))
			  FROM loans l
			  JOIN loan_products p ON p.id = l.product_id
			 WHERE l.status IN ('active','in_arrears','restructured')
			 GROUP BY l.product_id, p.name
			 ORDER BY p.name
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr ProductOutstanding
			if err := rows.Scan(&pr.ProductID, &pr.ProductName, &pr.Outstanding, &pr.ActiveCount); err != nil {
				return err
			}
			resp.ByProduct = append(resp.ByProduct, pr)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// 3. By-status counts. Initialise every known status to 0
		// so the donut renders empty buckets cleanly (no missing-key
		// branching in the React layer).
		for _, k := range []string{"pending_disbursement", "active", "in_arrears", "restructured", "defaulted", "settled", "written_off", "closed"} {
			resp.ByStatus[k] = 0
		}
		statusRows, err := tx.Query(ctx, `
			SELECT status::text, count(*) FROM loans GROUP BY status
		`)
		if err != nil {
			return err
		}
		defer statusRows.Close()
		for statusRows.Next() {
			var s string
			var n int
			if err := statusRows.Scan(&s, &n); err != nil {
				return err
			}
			resp.ByStatus[s] = n
		}

		// 4. Disbursed this month — sum of disbursement-typed txns
		// posted on or after the first of the current month.
		var disbursed decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount),0)
			  FROM loan_transactions
			 WHERE txn_type = 'disbursement'
			   AND posted_at >= date_trunc('month', now())
		`).Scan(&disbursed); err != nil {
			return err
		}
		resp.DisbursedThisMonth = disbursed.StringFixed(2)

		// 5. Collected this month — sum of repayment-typed txns this
		// month. Reversals are negative-amount rows in this codebase;
		// summing the lot gives net collected. Matches what the
		// existing /loans page already displays.
		var collected decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount),0)
			  FROM loan_transactions
			 WHERE txn_type IN ('repayment','reversal')
			   AND posted_at >= date_trunc('month', now())
		`).Scan(&collected); err != nil {
			return err
		}
		resp.CollectedThisMonth = collected.StringFixed(2)

		// 6. Applications by status — every known enum value.
		for _, k := range []string{
			"draft", "pending_validation", "pending_guarantor", "pending_scoring",
			"pending_approval", "approved", "approved_with_conditions", "declined",
			"returned_for_info", "offer_sent", "offer_accepted", "offer_declined",
			"expired", "cancelled", "disbursed",
		} {
			resp.ApplicationsByStatus[k] = 0
		}
		appRows, err := tx.Query(ctx, `
			SELECT status::text, count(*) FROM loan_applications GROUP BY status
		`)
		if err != nil {
			return err
		}
		defer appRows.Close()
		for appRows.Next() {
			var s string
			var n int
			if err := appRows.Scan(&s, &n); err != nil {
				return err
			}
			resp.ApplicationsByStatus[s] = n
		}

		// 7. Approaching-disbursement count — loans that have passed
		// approval/offer-accepted and are waiting for the disburse
		// step. The loans row exists with status='pending_disbursement'
		// once the application is approved+offer_accepted; counting
		// those gives the same number the dashboard list will show.
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM loans WHERE status = 'pending_disbursement'
		`).Scan(&resp.ApproachingDisbursementCount); err != nil {
			return err
		}

		// 8. At-risk count — Phase 1 heuristic: any active/in_arrears
		// loan with next_installment_due_at in the past. The Phase 3
		// DPD engine replaces this with arrears_classification IN
		// ('substandard','doubtful','loss') once that's wired.
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM loans
			 WHERE status IN ('active','in_arrears')
			   AND principal_balance > 0
			   AND next_installment_due_at IS NOT NULL
			   AND next_installment_due_at < CURRENT_DATE
		`).Scan(&resp.AtRiskCount); err != nil {
			return err
		}

		// 9. Promises due this week — Phase 4 wires PTPs. Phase 1
		// returns 0 so the card renders with a "Phase 4" hint in the
		// UI instead of a misleading number.
		resp.PromisesDueThisWeekCount = 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}
