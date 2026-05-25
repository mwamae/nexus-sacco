// Interest engine HTTP handlers.
//
// Endpoints orchestrate the FY interest-declaration lifecycle:
//   POST   /v1/interest-runs                    create draft
//   GET    /v1/interest-runs                    list
//   GET    /v1/interest-runs/{id}               fetch run + summary
//   PATCH  /v1/interest-runs/{id}               edit draft details
//   POST   /v1/interest-runs/{id}/compute       compute preview lines
//   GET    /v1/interest-runs/{id}/lines         list lines (preview)
//   PATCH  /v1/interest-run-lines/{id}          per-member payout override
//   POST   /v1/interest-runs/{id}/submit        create workflow instance
//   POST   /v1/interest-runs/{id}/approve       direct-approve (no workflow)
//   POST   /v1/interest-runs/{id}/post          execute posting
//   POST   /v1/interest-runs/{id}/lock          final lock
//   POST   /v1/interest-runs/{id}/cancel        abandon
//   GET    /v1/wht-schedule?fy=...              tenant remittance schedule
//   GET    /v1/wht-certificate/{counterparty_id}?fy=  member tax certificate
//   POST   /v1/interest-runs/callback           workflow service → us

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type InterestHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	Products       *store.DepositProductStore
	Deposits       *store.DepositStore
	Shares         *store.ShareStore
	Interest       *store.InterestStore
	Notifier       *notifier.Client
	Posting        *posting.Client
	Logger         *slog.Logger

	// Workflow integration
	WorkflowURL         string
	SavingsSelfURL      string
	WorkflowProcessKind string
	HTTP                *http.Client
}

// ─────────── Helpers ───────────

func (h *InterestHandler) processKind() string {
	if h.WorkflowProcessKind != "" {
		return h.WorkflowProcessKind
	}
	// "interest_run" matches the workflow definition seeded by
	// services/workflow/.../0003_seed_process_kinds. Earlier code
	// used "interest_run_approval"; tenants on that older default
	// can keep it via the configurable WorkflowProcessKind field.
	return "interest_run"
}

func (h *InterestHandler) http() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// loadTenantInterestConfig fetches FY config, default payout, WHT rate.
type tenantInterestCfg struct {
	FYStartMonth int
	FYStartDay   int
	DefaultPayout domain.InterestPayoutMethod
	WHTRatePct   decimal.Decimal
}

func (h *InterestHandler) loadTenantCfgTx(ctx context.Context, tx pgx.Tx) (*tenantInterestCfg, error) {
	var c tenantInterestCfg
	var payoutStr string
	err := tx.QueryRow(ctx, `
		SELECT fy_start_month, fy_start_day, default_interest_payout, dividend_wht_rate
		FROM tenant_operations
	`).Scan(&c.FYStartMonth, &c.FYStartDay, &payoutStr, &c.WHTRatePct)
	if err != nil {
		return nil, err
	}
	c.DefaultPayout = domain.InterestPayoutMethod(payoutStr)
	return &c, nil
}

// ─────────── Create draft ───────────

type createRunReq struct {
	FinancialYearLabel string          `json:"financial_year_label"`
	FYStart            string          `json:"fy_start"`           // YYYY-MM-DD
	FYEnd              string          `json:"fy_end"`             // YYYY-MM-DD
	AGMRatePct         decimal.Decimal `json:"agm_rate_pct"`
	AGMResolutionRef   string          `json:"agm_resolution_ref"`
	AGMResolutionDate  string          `json:"agm_resolution_date"` // YYYY-MM-DD
	WHTRatePct         *decimal.Decimal `json:"wht_rate_pct,omitempty"` // optional override; defaults to tenant
	ProductIDs         []uuid.UUID     `json:"product_ids"`
	Notes              string          `json:"notes"`
}

func (h *InterestHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var in createRunReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.AGMRatePct.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("agm_rate_pct must be > 0"))
		return
	}
	if in.AGMResolutionRef == "" || in.AGMResolutionDate == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("agm_resolution_ref and agm_resolution_date are required"))
		return
	}
	fyStart, err := time.Parse("2006-01-02", in.FYStart)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy_start must be YYYY-MM-DD"))
		return
	}
	fyEnd, err := time.Parse("2006-01-02", in.FYEnd)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy_end must be YYYY-MM-DD"))
		return
	}
	if !fyEnd.After(fyStart) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy_end must be after fy_start"))
		return
	}
	agmDate, err := time.Parse("2006-01-02", in.AGMResolutionDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("agm_resolution_date must be YYYY-MM-DD"))
		return
	}
	if len(in.ProductIDs) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("product_ids must contain at least one product"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	if in.FinancialYearLabel == "" {
		in.FinancialYearLabel = domain.FYLabel(fyStart, fyEnd)
	}

	var notes *string
	if in.Notes != "" {
		notes = &in.Notes
	}

	var run *domain.InterestRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		cfg, err := h.loadTenantCfgTx(r.Context(), tx)
		if err != nil {
			return err
		}
		whtRate := cfg.WHTRatePct
		if in.WHTRatePct != nil {
			whtRate = *in.WHTRatePct
		}
		// Verify products exist + are interest-eligible.
		if err := h.validateProductsTx(r.Context(), tx, in.ProductIDs); err != nil {
			return err
		}
		run, err = h.Interest.CreateRunTx(r.Context(), tx, domain.InterestRun{
			TenantID:           tid,
			FinancialYearLabel: in.FinancialYearLabel,
			FYStart:            fyStart,
			FYEnd:              fyEnd,
			AGMRatePct:         in.AGMRatePct,
			AGMResolutionRef:   in.AGMResolutionRef,
			AGMResolutionDate:  agmDate,
			WHTRatePct:         whtRate,
			ProductIDs:         in.ProductIDs,
			Notes:              notes,
			CreatedBy:          userID,
		})
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.Created(w, run)
}

func (h *InterestHandler) validateProductsTx(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	rows, err := tx.Query(ctx, `
		SELECT id, interest_eligible, is_active
		FROM deposit_products
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		var eligible, active bool
		if err := rows.Scan(&id, &eligible, &active); err != nil {
			return err
		}
		seen[id] = true
		if !active {
			return httpx.ErrBadRequest("product is not active: " + id.String())
		}
		if !eligible {
			return httpx.ErrBadRequest("product is not interest-eligible: " + id.String())
		}
	}
	for _, id := range ids {
		if !seen[id] {
			return httpx.ErrBadRequest("product not found: " + id.String())
		}
	}
	return nil
}

// ─────────── Read endpoints ───────────

func (h *InterestHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.InterestRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		run, err = h.Interest.GetRunTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

func (h *InterestHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.RunListFilter{
		Status: q.Get("status"),
		FYLike: q.Get("fy"),
		Limit:  limit, Offset: offset,
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.InterestRun
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Interest.ListRunsTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []domain.InterestRun{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

type runDetailResp struct {
	Run   domain.InterestRun       `json:"run"`
	Lines []domain.InterestRunLine `json:"lines"`
}

func (h *InterestHandler) GetRunWithLines(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp runDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		lines, err := h.Interest.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp = runDetailResp{Run: *run, Lines: lines}
		return nil
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	if resp.Lines == nil {
		resp.Lines = []domain.InterestRunLine{}
	}
	httpx.OK(w, resp)
}

// ─────────── Compute ───────────

func (h *InterestHandler) Compute(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp runDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateRunForTransition(run, domain.RunPreview); err != nil {
			return err
		}
		// Compute lines (read-only).
		lines, err := h.Interest.ComputeLinesTx(r.Context(), tx, run)
		if err != nil {
			return err
		}
		// Persist + aggregates.
		if err := h.Interest.ReplaceLinesTx(r.Context(), tx, run.ID, lines); err != nil {
			return err
		}
		mc, twb, tg, tw, tn := domain.SumLines(lines)
		updated, err := h.Interest.UpdateStatusTx(r.Context(), tx, run.ID, store.StatusTransition{
			To: domain.RunPreview,
			By: userID,
			Aggregates: &store.RunAggregates{
				MemberCount:          mc,
				TotalWeightedBalance: twb,
				TotalGrossInterest:   tg,
				TotalWHT:             tw,
				TotalNetInterest:     tn,
			},
		})
		if err != nil {
			return err
		}
		// Refresh lines (they got persisted with ids).
		freshLines, err := h.Interest.LinesByRunTx(r.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		resp = runDetailResp{Run: *updated, Lines: freshLines}
		return nil
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── Update payout per line ───────────

type updateLinePayoutReq struct {
	PayoutMethod          domain.InterestPayoutMethod `json:"payout_method"`
	PayoutTargetAccountID *uuid.UUID                  `json:"payout_target_account_id,omitempty"`
	PayoutExternalChannel *string                     `json:"payout_external_channel,omitempty"`
	PayoutExternalRef     *string                     `json:"payout_external_ref,omitempty"`
}

func (h *InterestHandler) UpdateLinePayout(w http.ResponseWriter, r *http.Request) {
	lineID, err := parseUUIDParam(r, "line_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in updateLinePayoutReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !in.PayoutMethod.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid payout_method"))
		return
	}
	switch in.PayoutMethod {
	case domain.PayoutCreditSavings:
		if in.PayoutTargetAccountID == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("payout_target_account_id is required for credit_savings"))
			return
		}
	case domain.PayoutExternal:
		if in.PayoutExternalChannel == nil || *in.PayoutExternalChannel == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("payout_external_channel is required for external"))
			return
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Interest.UpdateLinePayoutTx(r.Context(), tx,
			lineID, in.PayoutMethod, in.PayoutTargetAccountID,
			in.PayoutExternalChannel, in.PayoutExternalRef)
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Submit for approval (workflow) ───────────

func (h *InterestHandler) Submit(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var run *domain.InterestRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		run, err = h.Interest.GetRunTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	if run.Status != domain.RunPreview {
		httpx.WriteErr(w, r, httpx.ErrConflict("run is not in 'preview' state"))
		return
	}
	if run.WorkflowInstanceID != nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("run already submitted to workflow"))
		return
	}

	wfID, err := h.createWorkflowInstance(r, tid, run, userID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Interest.UpdateWorkflowIDTx(r.Context(), tx, run.ID, wfID, userID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"workflow_instance_id": wfID, "status": "preview"})
}

// Direct approval (no workflow) — for tenants without compliance gating.
type approveReq struct {
	Comment string `json:"comment"`
}

func (h *InterestHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in approveReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	_ = in
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.InterestRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateRunForTransition(existing, domain.RunApproved); err != nil {
			return err
		}
		run, err = h.Interest.UpdateStatusTx(r.Context(), tx, id, store.StatusTransition{
			To: domain.RunApproved,
			By: userID,
		})
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

// ─────────── Posting ───────────

func (h *InterestHandler) Post(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var out runDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if run.Status != domain.RunApproved {
			return domain.ErrRunNotPostable
		}
		// Move to 'posting'
		if _, err := h.Interest.UpdateStatusTx(r.Context(), tx, id, store.StatusTransition{
			To: domain.RunPosting, By: userID,
		}); err != nil {
			return err
		}
		// Fetch lines + tenant share policy snapshot (for buy_shares path).
		lines, err := h.Interest.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		// Iterate + post.
		for i := range lines {
			if err := h.postLine(r.Context(), tx, run, &lines[i], policy, userID); err != nil {
				return err
			}
		}
		// Batched GL journal entry for the whole run. One JE per AGM
		// declaration keeps journal_entries readable for runs with
		// thousands of members; the (source_module='savings.interest',
		// source_ref=run.id) join recovers the per-line audit chain
		// through deposit_transactions / share_transactions.
		//
		// In-tx outbox INSERT — failure rolls back every postLine
		// write above, the 'posting' status flip, and the run stays
		// at 'approved'. Safe to retry /post.
		if err := h.postBatchedRunGLTx(r.Context(), tx, tid, run, lines, policy); err != nil {
			return err
		}
		// Promote to 'posted'.
		final, err := h.Interest.UpdateStatusTx(r.Context(), tx, id, store.StatusTransition{
			To: domain.RunPosted, By: userID,
		})
		if err != nil {
			return err
		}
		refreshed, err := h.Interest.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = runDetailResp{Run: *final, Lines: refreshed}
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// postBatchedRunGLTx aggregates the run's posted lines into one
// journal entry and queues it on the outbox.
//
// Aggregation:
//
//   DR 5000 Interest on Member Deposits  = Σ gross_interest
//   CR 2200 Withholding Tax Payable      = Σ wht_amount
//   CR per-product liability code        = Σ net amounts credited
//                                          back to savings (the
//                                          credit_savings path and
//                                          the buy_shares residual)
//   CR 3000 Member Share Capital         = Σ shares_purchased * par
//                                          on buy_shares lines
//   CR 2230 Other Payables               = Σ net_interest on
//                                          external lines
//
// Zero-net lines are skipped from the JE so the aggregation matches
// what postLine actually wrote — those lines also skip the
// tax_payable_ledger row, so including their gross / WHT here would
// double-count vs. the WHT ledger.
//
// Suppresses the post entirely when there is no expense to record
// (every line zero-net) or when the Posting client is disabled
// (dev). The run.ID doubles as the synthetic JE handle stamped on
// interest_runs.journal_entry_id — recover the actual
// journal_entries row via (source_module='savings.interest',
// source_ref=run.id).
func (h *InterestHandler) postBatchedRunGLTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	run *domain.InterestRun, lines []domain.InterestRunLine,
	policy *store.SharePolicy,
) error {
	if h.Posting == nil || h.Posting.Disabled {
		return nil
	}

	var (
		drInterestExpense decimal.Decimal
		crWHT             decimal.Decimal
		crSharesCapital   decimal.Decimal
		crOtherPayables   decimal.Decimal
	)
	crLiabilityByCode := map[string]decimal.Decimal{}

	// Resolve product → liability code once per distinct product so a
	// run with thousands of members doesn't fan into a N-row product
	// lookup.
	liabByProduct := map[uuid.UUID]string{}
	for _, l := range lines {
		if _, ok := liabByProduct[l.ProductID]; ok {
			continue
		}
		p, err := h.Products.GetTx(ctx, tx, l.ProductID)
		if err != nil {
			return fmt.Errorf("load product %s for GL aggregation: %w", l.ProductID, err)
		}
		liabByProduct[l.ProductID] = depositLiabilityCode(p.Segment, p.ProductType)
	}

	par := decimal.Zero
	if policy != nil {
		par = policy.ParValue
	}

	for _, line := range lines {
		if line.NetInterest.LessThanOrEqual(decimal.Zero) {
			continue
		}
		drInterestExpense = drInterestExpense.Add(line.GrossInterest)
		crWHT = crWHT.Add(line.WHTAmount)

		switch line.PayoutMethod {
		case domain.PayoutCreditSavings:
			code := liabByProduct[line.ProductID]
			crLiabilityByCode[code] = crLiabilityByCode[code].Add(line.NetInterest)
		case domain.PayoutBuyShares:
			sharesPortion := decimal.Zero
			residual := line.NetInterest
			if par.GreaterThan(decimal.Zero) {
				qty := line.NetInterest.Div(par).Floor()
				sharesPortion = par.Mul(qty)
				residual = line.NetInterest.Sub(sharesPortion)
			}
			if sharesPortion.GreaterThan(decimal.Zero) {
				crSharesCapital = crSharesCapital.Add(sharesPortion)
			}
			if residual.GreaterThan(decimal.Zero) {
				code := liabByProduct[line.ProductID]
				crLiabilityByCode[code] = crLiabilityByCode[code].Add(residual)
			}
		case domain.PayoutExternal:
			crOtherPayables = crOtherPayables.Add(line.NetInterest)
		}
	}

	if drInterestExpense.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	jeLines := []posting.Line{
		{AccountCode: "5000", Debit: drInterestExpense, Narration: "Interest expense · " + run.RunNo},
	}
	if crWHT.GreaterThan(decimal.Zero) {
		jeLines = append(jeLines, posting.Line{AccountCode: "2200", Credit: crWHT, Narration: "Withholding tax payable"})
	}
	if crSharesCapital.GreaterThan(decimal.Zero) {
		jeLines = append(jeLines, posting.Line{AccountCode: "3000", Credit: crSharesCapital, Narration: "Interest applied to shares"})
	}
	if crOtherPayables.GreaterThan(decimal.Zero) {
		jeLines = append(jeLines, posting.Line{AccountCode: "2230", Credit: crOtherPayables, Narration: "External payout owed to members"})
	}
	// Deterministic per-product liability order — keeps test assertions and
	// audit reads stable across runs.
	codes := make([]string, 0, len(crLiabilityByCode))
	for c := range crLiabilityByCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		jeLines = append(jeLines, posting.Line{
			AccountCode: c, Credit: crLiabilityByCode[c],
			Narration: "Interest credited to member savings (" + c + ")",
		})
	}

	if err := h.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.interest",
		SourceRef:    run.ID.String(),
		Narration:    "Interest run " + run.RunNo + " · " + run.FinancialYearLabel,
		Lines:        jeLines,
	}); err != nil {
		return err
	}
	return h.Interest.UpdateJournalEntryIDTx(ctx, tx, run.ID, run.ID)
}

// postLine executes the per-line money movement, writes the
// tax_payable_ledger row, and marks the line posted.
func (h *InterestHandler) postLine(
	ctx context.Context, tx pgx.Tx,
	run *domain.InterestRun, line *domain.InterestRunLine,
	sharePolicy *store.SharePolicy,
	userID uuid.UUID,
) error {
	if line.PostedAt != nil {
		return nil // idempotent
	}
	// Skip zero-net lines (e.g. a member whose gross fully rounds to WHT).
	if line.NetInterest.LessThanOrEqual(decimal.Zero) {
		return h.Interest.MarkLinePostedTx(ctx, tx, line.ID, nil, nil)
	}
	var depositTxnID, shareTxnID *uuid.UUID

	switch line.PayoutMethod {
	case domain.PayoutCreditSavings:
		if line.PayoutTargetAccountID == nil {
			// Fall back to any active ordinary-savings account owned by the member.
			fallback, err := h.findFallbackSavingsTx(ctx, tx, line.CounterpartyID)
			if err != nil {
				return err
			}
			if fallback == nil {
				return httpx.ErrConflict("no savings account available for credit; set payout_target_account_id on line " + line.ID.String())
			}
			line.PayoutTargetAccountID = &fallback.ID
		}
		acct, err := h.Deposits.GetAccountTx(ctx, tx, *line.PayoutTargetAccountID)
		if err != nil {
			return err
		}
		narration := "Interest credit · " + run.RunNo + " · " + run.FinancialYearLabel
		txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnInterestCredit,
			Amount:      line.NetInterest,
			Channel:     ptrChannel(domain.DepChannelInternal),
			Narration:   &narration,
			InitiatedBy: userID,
		})
		if err != nil {
			return err
		}
		depositTxnID = &txn.ID

	case domain.PayoutBuyShares:
		// floor(net / par) shares; any remainder credited to fallback savings.
		par := sharePolicy.ParValue
		if par.LessThanOrEqual(decimal.Zero) {
			return httpx.ErrConflict("share par value must be > 0 to buy_shares")
		}
		sharesQty := line.NetInterest.Div(par).Floor()
		n := int(sharesQty.IntPart())
		if n > 0 {
			acct, err := h.Shares.EnsureAccountTx(ctx, tx, line.CounterpartyID, par)
			if err != nil {
				return err
			}
			internal := domain.ChannelInternal
			narration := "Interest applied to shares · " + run.RunNo
			st, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
				Account:        acct,
				TxnType:        domain.TxnPurchase,
				SharesDelta:    n,
				ParValueAtTxn:  par,
				PaymentChannel: &internal,
				Narration:      &narration,
				InitiatedBy:    userID,
			})
			if err != nil {
				return err
			}
			shareTxnID = &st.ID
			// Issue a new certificate reflecting the post-purchase balance.
			updated, err := h.Shares.GetAccountTx(ctx, tx, acct.ID)
			if err != nil {
				return err
			}
			if _, err := h.Shares.IssueCertificateTx(ctx, tx, acct.ID, line.CounterpartyID, userID,
				updated.SharesHeld, par, sharePolicy.CertificatePrefix); err != nil {
				return err
			}
		}
		// Credit any remainder to the member's fallback savings account.
		remainder := line.NetInterest.Sub(par.Mul(decimal.NewFromInt(int64(n))))
		if remainder.GreaterThan(decimal.Zero) {
			fallback, err := h.findFallbackSavingsTx(ctx, tx, line.CounterpartyID)
			if err != nil {
				return err
			}
			if fallback != nil {
				narration := "Interest residual · " + run.RunNo
				txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
					Account:     fallback,
					TxnType:     domain.TxnInterestCredit,
					Amount:      remainder,
					Channel:     ptrChannel(domain.DepChannelInternal),
					Narration:   &narration,
					InitiatedBy: userID,
				})
				if err != nil {
					return err
				}
				depositTxnID = &txn.ID
			}
		}

	case domain.PayoutExternal:
		if line.PayoutExternalChannel == nil || *line.PayoutExternalChannel == "" {
			return httpx.ErrConflict("payout_external_channel is required for external payout (line " + line.ID.String() + ")")
		}
		// No ledger txn — the accounting team disburses externally. We
		// record the intent on the line itself, and the WHT row still
		// gets written below.
	}

	// Look up member identity for the tax_payable row.
	member, err := h.Counterparties.GetByIDTx(ctx, tx, line.CounterpartyID)
	if err != nil {
		return err
	}
	runID := run.ID
	if err := h.Interest.InsertTaxPayableTx(ctx, tx, &domain.TaxPayableEntry{
		SourceKind:  "interest_run",
		SourceID:    &runID,
		CounterpartyID:    line.CounterpartyID,
		MemberNo:    member.MemberNo,
		MemberName:  member.FullName,
		FYLabel:     run.FinancialYearLabel,
		GrossAmount: line.GrossInterest,
		WHTRatePct:  line.WHTRatePct,
		WHTAmount:   line.WHTAmount,
		PostedBy:    userID,
	}); err != nil {
		return err
	}

	return h.Interest.MarkLinePostedTx(ctx, tx, line.ID, depositTxnID, shareTxnID)
}

func ptrChannel(c domain.DepositChannel) *domain.DepositChannel { return &c }

// findFallbackSavingsTx picks an ordinary-savings account for the member,
// falling back to any active deposit account if none match.
func (h *InterestHandler) findFallbackSavingsTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*domain.DepositAccount, error) {
	row := tx.QueryRow(ctx, `
		SELECT id FROM deposit_accounts
		WHERE counterparty_id = $1 AND status = 'active'
		ORDER BY
		  CASE WHEN product_id IN (SELECT id FROM deposit_products WHERE product_type = 'ordinary') THEN 0 ELSE 1 END,
		  current_balance DESC
		LIMIT 1
	`, memberID)
	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return h.Deposits.GetAccountTx(ctx, tx, id)
}

// ─────────── Lock / cancel ───────────

func (h *InterestHandler) Lock(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.InterestRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateRunForTransition(existing, domain.RunLocked); err != nil {
			return err
		}
		run, err = h.Interest.UpdateStatusTx(r.Context(), tx, id, store.StatusTransition{To: domain.RunLocked, By: userID})
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

type cancelReq struct {
	Reason string `json:"reason"`
}

func (h *InterestHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in cancelReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required to cancel"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.InterestRun
	reason := in.Reason
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Interest.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !domain.CanTransition(existing.Status, domain.RunCancelled) {
			return domain.ErrInvalidStatusTxn
		}
		run, err = h.Interest.UpdateStatusTx(r.Context(), tx, id, store.StatusTransition{
			To: domain.RunCancelled, By: userID, CancelReason: &reason,
		})
		return err
	})
	if err != nil {
		writeInterestErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

// ─────────── WHT reports ───────────

func (h *InterestHandler) WHTSchedule(w http.ResponseWriter, r *http.Request) {
	fy := r.URL.Query().Get("fy")
	if fy == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy query parameter required (e.g. fy=FY%202025)"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []store.WHTScheduleRow
	var total decimal.Decimal
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, total, err = h.Interest.WHTScheduleForFYTx(r.Context(), tx, fy)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []store.WHTScheduleRow{}
	}
	httpx.OK(w, map[string]any{"fy_label": fy, "rows": rows, "total_wht": total})
}

func (h *InterestHandler) WHTCertificate(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	fy := r.URL.Query().Get("fy")
	if fy == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy query parameter required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var entries []domain.TaxPayableEntry
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		entries, err = h.Interest.PerMemberCertificateTx(r.Context(), tx, memberID, fy)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []domain.TaxPayableEntry{}
	}
	var totGross, totWHT decimal.Decimal
	for _, e := range entries {
		totGross = totGross.Add(e.GrossAmount)
		totWHT = totWHT.Add(e.WHTAmount)
	}
	httpx.OK(w, map[string]any{
		"counterparty_id": memberID,
		"fy_label":  fy,
		"entries":   entries,
		"totals": map[string]any{
			"gross_amount": totGross,
			"wht_amount":   totWHT,
			"net_amount":   totGross.Sub(totWHT),
		},
	})
}

// ─────────── Workflow integration ───────────

func (h *InterestHandler) createWorkflowInstance(r *http.Request, tenantID uuid.UUID, run *domain.InterestRun, actorID uuid.UUID) (uuid.UUID, error) {
	if h.WorkflowURL == "" {
		return uuid.Nil, httpx.ErrConflict("workflow service not configured (WORKFLOW_SERVICE_URL)")
	}
	_ = tenantID
	callback := ""
	if h.SavingsSelfURL != "" {
		callback = strings.TrimRight(h.SavingsSelfURL, "/") + "/v1/interest-runs/callback"
	}
	payload := map[string]any{
		"process_kind": h.processKind(),
		"subject_kind": "interest_run",
		"subject_id":   run.ID.String(),
		"context": map[string]any{
			"run_id":               run.ID,
			"run_no":               run.RunNo,
			"financial_year_label": run.FinancialYearLabel,
			"fy_start":             run.FYStart.Format("2006-01-02"),
			"fy_end":               run.FYEnd.Format("2006-01-02"),
			"agm_rate_pct":         run.AGMRatePct,
			"agm_resolution_ref":   run.AGMResolutionRef,
			"member_count":         run.MemberCount,
			"total_gross_interest": run.TotalGrossInterest,
			"total_wht":            run.TotalWHT,
			"total_net_interest":   run.TotalNetInterest,
		},
		"callback_url": callback,
		"initiator_id": actorID,
		// Unified Inbox (PR #6): one-line summary + deep-link back to
		// the source page. Powers the Inbox card + the "open source"
		// affordance in the detail pane.
		"summary":    fmt.Sprintf("Interest run %s — %s · KES %s net", run.RunNo, run.FinancialYearLabel, run.TotalNetInterest.StringFixed(2)),
		"source_url": fmt.Sprintf("/interest-runs/%s", run.ID),
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(h.WorkflowURL, "/")+"/v1/workflow-instances",
		bytes.NewReader(body))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}
	req.Host = r.Host
	resp, err := h.http().Do(req)
	if err != nil {
		return uuid.Nil, httpx.ErrConflict("workflow service unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, httpx.ErrConflict("workflow service rejected the instance: " + string(b))
	}
	var envelope struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return uuid.Nil, err
	}
	if envelope.Data.ID == uuid.Nil {
		return uuid.Nil, httpx.ErrConflict("workflow service returned no instance id")
	}
	return envelope.Data.ID, nil
}

// WorkflowCallback is hit by the workflow engine on terminal state.
// Body contains the full instance with an "outcome" field ("approved"
// | "rejected"). We move the run accordingly.
type wfInstanceCallback struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Instance struct {
		ID         uuid.UUID `json:"id"`
		SubjectID  uuid.UUID `json:"subject_id"`
		Outcome    string    `json:"outcome"`
		State      string    `json:"state"`
	} `json:"instance"`
}

func (h *InterestHandler) WorkflowCallback(w http.ResponseWriter, r *http.Request) {
	var cb wfInstanceCallback
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&cb); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid callback body: "+err.Error()))
		return
	}
	if cb.Instance.ID == uuid.Nil || cb.TenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing tenant_id or instance.id"))
		return
	}
	err := h.DB.WithTenantTx(r.Context(), cb.TenantID, func(tx pgx.Tx) error {
		// Look up the run by workflow_instance_id.
		var runID uuid.UUID
		err := tx.QueryRow(r.Context(), `
			SELECT id FROM interest_runs WHERE workflow_instance_id = $1
		`, cb.Instance.ID).Scan(&runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Not ours — silently ack.
				return nil
			}
			return err
		}
		run, err := h.Interest.GetRunTx(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if cb.Instance.Outcome == "approved" {
			if !domain.CanTransition(run.Status, domain.RunApproved) {
				return nil // already past approval; ignore
			}
			actor := run.CreatedBy
			if run.SubmittedBy != nil && *run.SubmittedBy != uuid.Nil {
				actor = *run.SubmittedBy
			}
			_, err = h.Interest.UpdateStatusTx(r.Context(), tx, runID, store.StatusTransition{
				To: domain.RunApproved, By: actor,
			})
			return err
		}
		if cb.Instance.Outcome == "rejected" {
			reason := "Rejected by approval workflow"
			_, err = h.Interest.UpdateStatusTx(r.Context(), tx, runID, store.StatusTransition{
				To: domain.RunCancelled, By: run.CreatedBy, CancelReason: &reason,
			})
			return err
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"status": "ok"})
}

// ─────────── Error mapping ───────────

func writeInterestErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrAGMGateMissing),
		errors.Is(err, domain.ErrInvalidStatusTxn),
		errors.Is(err, domain.ErrNoProductsInScope),
		errors.Is(err, domain.ErrFYInvalid),
		errors.Is(err, domain.ErrRunNotPostable),
		errors.Is(err, domain.ErrLineAlreadyPosted),
		errors.Is(err, domain.ErrPayoutTargetMissing),
		errors.Is(err, domain.ErrPayoutChannelMissing):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}

// chi reference silencer
var _ = chi.URLParam
