// Dividend engine HTTP handlers.
//
// Mirror interest engine endpoints (same lifecycle, same gating):
//   POST   /v1/dividend-runs                       create draft
//   GET    /v1/dividend-runs                       list
//   GET    /v1/dividend-runs/{id}                  fetch run + lines
//   POST   /v1/dividend-runs/{id}/compute          compute preview lines
//   PATCH  /v1/dividend-run-lines/{id}             per-line payout override
//   POST   /v1/dividend-runs/{id}/submit           create workflow instance
//   POST   /v1/dividend-runs/{id}/approve          direct-approve
//   POST   /v1/dividend-runs/{id}/post             execute posting
//   POST   /v1/dividend-runs/{id}/lock             final lock
//   POST   /v1/dividend-runs/{id}/cancel           abandon
//   POST   /v1/dividend-runs/callback              workflow service → us

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type DividendHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	Deposits       *store.DepositStore
	Shares         *store.ShareStore
	Dividends      *store.DividendStore
	Notifier       *notifier.Client
	Logger         *slog.Logger

	WorkflowURL         string
	SavingsSelfURL      string
	WorkflowProcessKind string
	HTTP                *http.Client
}

func (h *DividendHandler) processKind() string {
	if h.WorkflowProcessKind != "" {
		return h.WorkflowProcessKind
	}
	return "dividend_run_approval"
}

func (h *DividendHandler) http() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ─────────── Create ───────────

type createDivRunReq struct {
	FinancialYearLabel string                     `json:"financial_year_label"`
	FYStart            string                     `json:"fy_start"`
	FYEnd              string                     `json:"fy_end"`
	CalcMethod         domain.DividendCalcMethod  `json:"calc_method"`
	AGMRatePct         decimal.Decimal            `json:"agm_rate_pct"`
	AGMResolutionRef   string                     `json:"agm_resolution_ref"`
	AGMResolutionDate  string                     `json:"agm_resolution_date"`
	WHTRatePct         *decimal.Decimal           `json:"wht_rate_pct,omitempty"`
	Notes              string                     `json:"notes"`
}

func (h *DividendHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var in createDivRunReq
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
	if in.CalcMethod == "" {
		in.CalcMethod = domain.CalcClosingBalance
	}
	if !in.CalcMethod.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid calc_method"))
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

	var run *domain.DividendRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var whtRate decimal.Decimal
		err := tx.QueryRow(r.Context(), `SELECT dividend_wht_rate FROM tenant_operations`).Scan(&whtRate)
		if err != nil {
			return err
		}
		if in.WHTRatePct != nil {
			whtRate = *in.WHTRatePct
		}
		run, err = h.Dividends.CreateRunTx(r.Context(), tx, domain.DividendRun{
			TenantID:           tid,
			FinancialYearLabel: in.FinancialYearLabel,
			FYStart:            fyStart,
			FYEnd:              fyEnd,
			CalcMethod:         in.CalcMethod,
			AGMRatePct:         in.AGMRatePct,
			AGMResolutionRef:   in.AGMResolutionRef,
			AGMResolutionDate:  agmDate,
			WHTRatePct:         whtRate,
			Notes:              notes,
			CreatedBy:          userID,
		})
		return err
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.Created(w, run)
}

// ─────────── Reads ───────────

func (h *DividendHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.DivRunListFilter{
		Status: q.Get("status"), FYLike: q.Get("fy"),
		Limit: limit, Offset: offset,
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.DividendRun
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Dividends.ListRunsTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []domain.DividendRun{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

type divRunDetailResp struct {
	Run   domain.DividendRun       `json:"run"`
	Lines []domain.DividendRunLine `json:"lines"`
}

func (h *DividendHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp divRunDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		lines, err := h.Dividends.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp = divRunDetailResp{Run: *run, Lines: lines}
		return nil
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	if resp.Lines == nil {
		resp.Lines = []domain.DividendRunLine{}
	}
	httpx.OK(w, resp)
}

// ─────────── Compute ───────────

func (h *DividendHandler) Compute(w http.ResponseWriter, r *http.Request) {
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
	var resp divRunDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateDividendRunForTransition(run, domain.DivPreview); err != nil {
			return err
		}
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		// Compute per-account basis under the requested method.
		basis, err := h.Dividends.ComputeBasisTx(r.Context(), tx, run.CalcMethod, run.FYStart, run.FYEnd)
		if err != nil {
			return err
		}
		daysInFY := domain.DaysInFY(run.FYStart, run.FYEnd)
		var lines []domain.DividendRunLine
		for _, b := range basis {
			line := domain.DivCalcLine(domain.DivCalcInputs{
				ShareAccountID: b.AccountID,
				CounterpartyID:       b.CounterpartyID,
				CalcMethod:     run.CalcMethod,
				SharesBasis:    b.SharesBasis,
				ParValueAtRun:  policy.ParValue,
				DaysInFY:       daysInFY,
				DaysHeldInFY:   b.DaysHeldInFY,
			}, run.AGMRatePct, run.WHTRatePct)
			line.RunID = run.ID
			line.TenantID = run.TenantID
			lines = append(lines, line)
		}
		if err := h.Dividends.ReplaceLinesTx(r.Context(), tx, run.ID, lines); err != nil {
			return err
		}
		mc, tb, tg, tw, tn := domain.SumDivLines(lines)
		updated, err := h.Dividends.UpdateStatusTx(r.Context(), tx, run.ID, store.DivStatusTransition{
			To: domain.DivPreview, By: userID,
			Aggregates: &store.DivRunAggregates{
				MemberCount: mc, TotalShareBasis: tb,
				TotalGrossDividend: tg, TotalWHT: tw, TotalNetDividend: tn,
			},
		})
		if err != nil {
			return err
		}
		freshLines, err := h.Dividends.LinesByRunTx(r.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		resp = divRunDetailResp{Run: *updated, Lines: freshLines}
		return nil
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── Payout edit ───────────

type updateDivLineReq struct {
	PayoutMethod          domain.InterestPayoutMethod `json:"payout_method"`
	PayoutTargetAccountID *uuid.UUID                  `json:"payout_target_account_id,omitempty"`
	PayoutExternalChannel *string                     `json:"payout_external_channel,omitempty"`
	PayoutExternalRef     *string                     `json:"payout_external_ref,omitempty"`
}

func (h *DividendHandler) UpdateLinePayout(w http.ResponseWriter, r *http.Request) {
	lineID, err := parseUUIDParam(r, "line_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in updateDivLineReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !in.PayoutMethod.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid payout_method"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Dividends.UpdateLinePayoutTx(r.Context(), tx,
			lineID, in.PayoutMethod, in.PayoutTargetAccountID,
			in.PayoutExternalChannel, in.PayoutExternalRef)
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Submit / Approve ───────────

func (h *DividendHandler) Submit(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.DividendRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		run, err = h.Dividends.GetRunTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	if run.Status != domain.DivPreview {
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
		return h.Dividends.UpdateWorkflowIDTx(r.Context(), tx, run.ID, wfID, userID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"workflow_instance_id": wfID, "status": "preview"})
}

type divApproveReq struct {
	Comment string `json:"comment"`
}

func (h *DividendHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in divApproveReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	_ = in
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.DividendRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateDividendRunForTransition(existing, domain.DivApproved); err != nil {
			return err
		}
		run, err = h.Dividends.UpdateStatusTx(r.Context(), tx, id, store.DivStatusTransition{To: domain.DivApproved, By: userID})
		return err
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

// ─────────── Post ───────────

func (h *DividendHandler) Post(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out divRunDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if run.Status != domain.DivApproved {
			return domain.ErrDivRunNotPostable
		}
		if _, err := h.Dividends.UpdateStatusTx(r.Context(), tx, id, store.DivStatusTransition{To: domain.DivPosting, By: userID}); err != nil {
			return err
		}
		lines, err := h.Dividends.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		interestStore := newInterestSubset(tx)
		for i := range lines {
			if err := h.postDivLine(r.Context(), tx, run, &lines[i], policy, userID, interestStore); err != nil {
				return err
			}
		}
		final, err := h.Dividends.UpdateStatusTx(r.Context(), tx, id, store.DivStatusTransition{To: domain.DivPosted, By: userID})
		if err != nil {
			return err
		}
		refreshed, err := h.Dividends.LinesByRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = divRunDetailResp{Run: *final, Lines: refreshed}
		return nil
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// newInterestSubset is a closure facade so postDivLine can write to
// tax_payable_ledger without depending on InterestStore directly. We
// just need the InsertTaxPayableTx behaviour.
type taxWriter struct {
	tx pgx.Tx
}

func newInterestSubset(tx pgx.Tx) *taxWriter { return &taxWriter{tx: tx} }

func (tw *taxWriter) writeTaxPayable(ctx context.Context, e *domain.TaxPayableEntry) error {
	_, err := tw.tx.Exec(ctx, `
		INSERT INTO tax_payable_ledger (
			tenant_id, source_kind, source_id, counterparty_id, member_no, member_name,
			fy_label, gross_amount, wht_rate_pct, wht_amount, posted_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`,
		e.SourceKind, e.SourceID, e.CounterpartyID, e.MemberNo, e.MemberName,
		e.FYLabel, e.GrossAmount, e.WHTRatePct, e.WHTAmount, e.PostedBy,
	)
	return err
}

// postDivLine executes the per-line money movement + WHT bookkeeping.
func (h *DividendHandler) postDivLine(
	ctx context.Context, tx pgx.Tx,
	run *domain.DividendRun, line *domain.DividendRunLine,
	sharePolicy *store.SharePolicy,
	userID uuid.UUID,
	tw *taxWriter,
) error {
	if line.PostedAt != nil {
		return nil
	}
	if line.NetDividend.LessThanOrEqual(decimal.Zero) {
		return h.Dividends.MarkLinePostedTx(ctx, tx, line.ID, nil, nil)
	}
	var depositTxnID, shareTxnID *uuid.UUID

	switch line.PayoutMethod {
	case domain.PayoutCreditSavings:
		if line.PayoutTargetAccountID == nil {
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
		narration := "Dividend · " + run.RunNo + " · " + run.FinancialYearLabel
		internal := domain.DepChannelInternal
		txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnInterestCredit, // accountants treat dividend credits as the same ledger move
			Amount:      line.NetDividend,
			Channel:     &internal,
			Narration:   &narration,
			InitiatedBy: userID,
		})
		if err != nil {
			return err
		}
		depositTxnID = &txn.ID

	case domain.PayoutBuyShares:
		par := sharePolicy.ParValue
		if par.LessThanOrEqual(decimal.Zero) {
			return httpx.ErrConflict("share par value must be > 0 to buy_shares")
		}
		sharesQty := line.NetDividend.Div(par).Floor()
		n := int(sharesQty.IntPart())
		if n > 0 {
			acct, err := h.Shares.EnsureAccountTx(ctx, tx, line.CounterpartyID, par)
			if err != nil {
				return err
			}
			internalCh := domain.ChannelInternal
			narration := "Dividend re-invested · " + run.RunNo
			st, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
				Account:        acct,
				TxnType:        domain.TxnBonusIssue,
				SharesDelta:    n,
				ParValueAtTxn:  par,
				PaymentChannel: &internalCh,
				Narration:      &narration,
				InitiatedBy:    userID,
			})
			if err != nil {
				return err
			}
			shareTxnID = &st.ID
			updated, err := h.Shares.GetAccountTx(ctx, tx, acct.ID)
			if err != nil {
				return err
			}
			if _, err := h.Shares.IssueCertificateTx(ctx, tx, acct.ID, line.CounterpartyID, userID,
				updated.SharesHeld, par, sharePolicy.CertificatePrefix); err != nil {
				return err
			}
		}
		// Residual to fallback savings.
		residual := line.NetDividend.Sub(par.Mul(decimal.NewFromInt(int64(n))))
		if residual.GreaterThan(decimal.Zero) {
			fallback, err := h.findFallbackSavingsTx(ctx, tx, line.CounterpartyID)
			if err != nil {
				return err
			}
			if fallback != nil {
				narration := "Dividend residual · " + run.RunNo
				internal := domain.DepChannelInternal
				txn, err := h.Deposits.PostTxnTx(ctx, tx, store.PostDepInput{
					Account:     fallback,
					TxnType:     domain.TxnInterestCredit,
					Amount:      residual,
					Channel:     &internal,
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
	}

	member, err := h.Counterparties.GetByIDTx(ctx, tx, line.CounterpartyID)
	if err != nil {
		return err
	}
	runID := run.ID
	if err := tw.writeTaxPayable(ctx, &domain.TaxPayableEntry{
		SourceKind:  "dividend_run",
		SourceID:    &runID,
		CounterpartyID:    line.CounterpartyID,
		MemberNo:    member.MemberNo,
		MemberName:  member.FullName,
		FYLabel:     run.FinancialYearLabel,
		GrossAmount: line.GrossDividend,
		WHTRatePct:  line.WHTRatePct,
		WHTAmount:   line.WHTAmount,
		PostedBy:    userID,
	}); err != nil {
		return err
	}
	return h.Dividends.MarkLinePostedTx(ctx, tx, line.ID, depositTxnID, shareTxnID)
}

func (h *DividendHandler) findFallbackSavingsTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (*domain.DepositAccount, error) {
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

func (h *DividendHandler) Lock(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "run_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.DividendRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidateDividendRunForTransition(existing, domain.DivLocked); err != nil {
			return err
		}
		run, err = h.Dividends.UpdateStatusTx(r.Context(), tx, id, store.DivStatusTransition{To: domain.DivLocked, By: userID})
		return err
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

func (h *DividendHandler) Cancel(w http.ResponseWriter, r *http.Request) {
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
	reason := in.Reason
	var run *domain.DividendRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		existing, err := h.Dividends.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !domain.CanTransitionDividend(existing.Status, domain.DivCancelled) {
			return domain.ErrDivInvalidStatusTxn
		}
		run, err = h.Dividends.UpdateStatusTx(r.Context(), tx, id, store.DivStatusTransition{
			To: domain.DivCancelled, By: userID, CancelReason: &reason,
		})
		return err
	})
	if err != nil {
		writeDivErr(w, r, err)
		return
	}
	httpx.OK(w, run)
}

// ─────────── Workflow integration ───────────

func (h *DividendHandler) createWorkflowInstance(r *http.Request, _ uuid.UUID, run *domain.DividendRun, actorID uuid.UUID) (uuid.UUID, error) {
	if h.WorkflowURL == "" {
		return uuid.Nil, httpx.ErrConflict("workflow service not configured")
	}
	callback := ""
	if h.SavingsSelfURL != "" {
		callback = strings.TrimRight(h.SavingsSelfURL, "/") + "/v1/dividend-runs/callback"
	}
	payload := map[string]any{
		"process_kind": h.processKind(),
		"subject_kind": "dividend_run",
		"subject_id":   run.ID.String(),
		"context": map[string]any{
			"run_id":                run.ID,
			"run_no":                run.RunNo,
			"financial_year_label":  run.FinancialYearLabel,
			"fy_start":              run.FYStart.Format("2006-01-02"),
			"fy_end":                run.FYEnd.Format("2006-01-02"),
			"calc_method":           string(run.CalcMethod),
			"agm_rate_pct":          run.AGMRatePct,
			"agm_resolution_ref":    run.AGMResolutionRef,
			"member_count":          run.MemberCount,
			"total_gross_dividend":  run.TotalGrossDividend,
			"total_wht":             run.TotalWHT,
			"total_net_dividend":    run.TotalNetDividend,
		},
		"callback_url": callback,
		"initiator_id": actorID,
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
		return uuid.Nil, httpx.ErrConflict("workflow rejected: " + string(b))
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
		return uuid.Nil, httpx.ErrConflict("workflow returned no instance id")
	}
	return envelope.Data.ID, nil
}

type divWfCallback struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Instance struct {
		ID      uuid.UUID `json:"id"`
		Outcome string    `json:"outcome"`
		State   string    `json:"state"`
	} `json:"instance"`
}

func (h *DividendHandler) WorkflowCallback(w http.ResponseWriter, r *http.Request) {
	var cb divWfCallback
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
		var runID uuid.UUID
		err := tx.QueryRow(r.Context(), `SELECT id FROM dividend_runs WHERE workflow_instance_id = $1`, cb.Instance.ID).Scan(&runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		run, err := h.Dividends.GetRunTx(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if cb.Instance.Outcome == "approved" {
			if !domain.CanTransitionDividend(run.Status, domain.DivApproved) {
				return nil
			}
			actor := run.CreatedBy
			if run.SubmittedBy != nil && *run.SubmittedBy != uuid.Nil {
				actor = *run.SubmittedBy
			}
			_, err = h.Dividends.UpdateStatusTx(r.Context(), tx, runID, store.DivStatusTransition{To: domain.DivApproved, By: actor})
			return err
		}
		if cb.Instance.Outcome == "rejected" {
			reason := "Rejected by approval workflow"
			_, err = h.Dividends.UpdateStatusTx(r.Context(), tx, runID, store.DivStatusTransition{
				To: domain.DivCancelled, By: run.CreatedBy, CancelReason: &reason,
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

func writeDivErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrDivAGMGateMissing),
		errors.Is(err, domain.ErrDivInvalidStatusTxn),
		errors.Is(err, domain.ErrDivFYInvalid),
		errors.Is(err, domain.ErrDivRunNotPostable),
		errors.Is(err, domain.ErrDivInvalidCalcMethod):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
