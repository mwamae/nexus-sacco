// Deposit account + transaction handlers.
//
// Endpoints orchestrate: load product + account + member, evaluate
// product rules, post the ledger row atomically.

package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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

type DepositHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	Products       *store.DepositProductStore
	Deposits       *store.DepositStore
	Approvals      *store.ApprovalsStore
	Notifier       *notifier.Client
	Posting        *posting.Client
	Logger         *slog.Logger

	// DuplicateLookback is how far back we look for a same-channel-ref
	// duplicate before flagging a deposit. Default 10 minutes.
	DuplicateLookback time.Duration
}

func (h *DepositHandler) lookback() time.Duration {
	if h.DuplicateLookback > 0 {
		return h.DuplicateLookback
	}
	return 10 * time.Minute
}

// ─────────── Helpers ───────────

func memberKind(_ *store.CounterpartyView) string {
	// Phase 3 individuals only. Once org/group support is wired through
	// the member service, infer from members.kind here.
	return "individual"
}

func (h *DepositHandler) loadProductAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (*domain.DepositProduct, *domain.DepositAccount, *store.CounterpartyView, error) {
	acct, err := h.Deposits.GetAccountTx(ctx, tx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, httpx.ErrNotFound("deposit account not found")
		}
		return nil, nil, nil, err
	}
	product, err := h.Products.GetTx(ctx, tx, acct.ProductID)
	if err != nil {
		return nil, nil, nil, err
	}
	member, err := h.Counterparties.GetByIDTx(ctx, tx, acct.CounterpartyID)
	if err != nil {
		return nil, nil, nil, err
	}
	return product, acct, member, nil
}

// ─────────── Account open ───────────

type openAcctReq struct {
	CounterpartyID             uuid.UUID                `json:"counterparty_id"`
	ProductID            uuid.UUID                `json:"product_id"`
	OpeningDeposit       decimal.Decimal          `json:"opening_deposit"`
	OpeningChannel       *domain.DepositChannel   `json:"opening_channel"`
	OpeningChannelRef    *string                  `json:"opening_channel_ref"`
	FixedTermMonths      *int                     `json:"fixed_term_months"`
	FixedInterestRatePct *decimal.Decimal         `json:"fixed_interest_rate_pct"`
	GoalTargetAmount     *decimal.Decimal         `json:"goal_target_amount"`
	GoalTargetDate       *string                  `json:"goal_target_date"`
	GoalDescription      *string                  `json:"goal_description"`
	GuardianMemberID     *uuid.UUID               `json:"guardian_member_id"`
	GroupOrgID           *uuid.UUID               `json:"group_org_id"`
}

type openAcctResp struct {
	Account     domain.DepositAccount      `json:"account"`
	Product     domain.DepositProduct      `json:"product"`
	OpeningTxn  *domain.DepositTransaction `json:"opening_transaction,omitempty"`
}

func (h *DepositHandler) Open(w http.ResponseWriter, r *http.Request) {
	var in openAcctReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CounterpartyID == uuid.Nil || in.ProductID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id and product_id are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var goalDate *time.Time
	if in.GoalTargetDate != nil && *in.GoalTargetDate != "" {
		d, err := time.Parse("2006-01-02", *in.GoalTargetDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("goal_target_date must be YYYY-MM-DD"))
			return
		}
		goalDate = &d
	}

	var resp openAcctResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, err := h.Products.GetTx(r.Context(), tx, in.ProductID)
		if err != nil {
			return err
		}
		if !product.IsActive {
			return domain.ErrProductInactive
		}
		member, err := h.Counterparties.GetByIDTx(r.Context(), tx, in.CounterpartyID)
		if err != nil {
			return err
		}
		if err := domain.EligibleForProduct(product, memberKind(member), member.Status); err != nil {
			return err
		}
		// Product-specific required fields.
		switch product.ProductType {
		case domain.ProductFixed:
			if in.FixedTermMonths == nil {
				v := 0
				if product.DefaultTermMonths != nil {
					v = *product.DefaultTermMonths
				}
				if v <= 0 {
					return httpx.ErrBadRequest("fixed deposit requires fixed_term_months")
				}
				in.FixedTermMonths = &v
			}
		case domain.ProductJunior:
			if in.GuardianMemberID == nil {
				return httpx.ErrBadRequest("junior account requires guardian_member_id")
			}
		case domain.ProductGoal:
			if in.GoalTargetAmount == nil || goalDate == nil {
				return httpx.ErrBadRequest("goal account requires goal_target_amount and goal_target_date")
			}
		case domain.ProductGroup:
			if in.GroupOrgID == nil {
				return httpx.ErrBadRequest("group account requires group_org_id")
			}
		}
		// Opening deposit ≥ min_opening_balance.
		if in.OpeningDeposit.LessThan(product.MinOpeningBalance) {
			return domain.ErrBelowMinOpeningBalance
		}
		if in.OpeningDeposit.GreaterThan(decimal.Zero) && in.OpeningChannel == nil {
			return httpx.ErrBadRequest("opening_channel is required when opening_deposit > 0")
		}

		accountNo, err := nextSeqExport(r.Context(), tx, "deposit_account", "DPA")
		if err != nil {
			return err
		}
		acct, openingTxn, err := h.Deposits.OpenAccountTx(r.Context(), tx, store.OpenInput{
			CounterpartyID:             in.CounterpartyID,
			ProductID:            in.ProductID,
			OpeningDeposit:       in.OpeningDeposit,
			OpeningChannel:       in.OpeningChannel,
			OpeningChannelRef:    in.OpeningChannelRef,
			FixedTermMonths:      in.FixedTermMonths,
			FixedInterestRatePct: in.FixedInterestRatePct,
			GoalTargetAmount:     in.GoalTargetAmount,
			GoalTargetDate:       goalDate,
			GoalDescription:      in.GoalDescription,
			GuardianMemberID:     in.GuardianMemberID,
			GroupOrgID:           in.GroupOrgID,
			CreatedBy:            userID,
		}, accountNo)
		if err != nil {
			return err
		}
		_ = h.Counterparties.TouchActivityTx(r.Context(), tx, in.CounterpartyID)
		resp = openAcctResp{Account: *acct, Product: *product, OpeningTxn: openingTxn}
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.Created(w, resp)
}

// nextSeqExport exposes the package-local nextSeq for handler-side use.
// Defined in shares store; replicated here to avoid an internal export.
func nextSeqExport(ctx context.Context, tx pgx.Tx, kind, prefix string) (string, error) {
	year := time.Now().UTC().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
		VALUES (current_tenant_id(), $1, $2, 1)
		ON CONFLICT (tenant_id, kind, year)
		DO UPDATE SET last_value = share_number_seq.last_value + 1
		RETURNING last_value
	`, kind, year).Scan(&next)
	if err != nil {
		return "", err
	}
	return formatSeq(prefix, year, next), nil
}

func formatSeq(prefix string, year, n int) string {
	// Mirror the share-store format: SHA-2026-00001 → DPA-2026-00001 etc.
	return prefix + "-" + strconvI(year) + "-" + zeroPad5(n)
}
func strconvI(n int) string  { return strconvItoa(n) }
func zeroPad5(n int) string {
	s := strconvItoa(n)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}
func strconvItoa(n int) string { return strconv.Itoa(n) }

// ─────────── Reads ───────────

type acctView struct {
	Account domain.DepositAccount   `json:"account"`
	Product domain.DepositProduct   `json:"product"`
	Member  store.CounterpartyView  `json:"member"`
}

func (h *DepositHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var v acctView
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, acct, member, err := h.loadProductAccount(r.Context(), tx, id)
		if err != nil {
			return err
		}
		v = acctView{Account: *acct, Product: *product, Member: *member}
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.OK(w, v)
}

type memberAcctItem struct {
	Account domain.DepositAccount `json:"account"`
	Product domain.DepositProduct `json:"product"`
}

func (h *DepositHandler) AccountsByMember(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	out := []memberAcctItem{}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		accts, err := h.Deposits.AccountsByMemberTx(r.Context(), tx, memberID)
		if err != nil {
			return err
		}
		for i := range accts {
			p, err := h.Products.GetTx(r.Context(), tx, accts[i].ProductID)
			if err != nil {
				return err
			}
			out = append(out, memberAcctItem{Account: accts[i], Product: *p})
		}
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *DepositHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.AcctListFilter{
		Status: q.Get("status"),
		Q:      q.Get("q"),
		Limit:  limit, Offset: offset,
	}
	if v := q.Get("product_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid product_id"))
			return
		}
		f.ProductID = &id
	}
	var items []store.AcctListItem
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Deposits.ListAccountsTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.AcctListItem{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

func (h *DepositHandler) Summary(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var sum *store.DepositsSummary
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		sum, err = h.Deposits.SummaryTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, sum)
}

// ─────────── Deposit ───────────

type depositReq struct {
	Amount     decimal.Decimal       `json:"amount"`
	Channel    domain.DepositChannel `json:"channel"`
	ChannelRef string                `json:"channel_ref"`
	Narration  string                `json:"narration"`
	ValueDate  string                `json:"value_date"` // YYYY-MM-DD
	BypassDuplicateCheck bool        `json:"bypass_duplicate_check"`
}

func (h *DepositHandler) Deposit(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in depositReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	if in.Channel == "" || !in.Channel.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("valid channel is required"))
		return
	}
	if in.ValueDate != "" {
		if _, err := time.Parse("2006-01-02", in.ValueDate); err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := DepositPayload{
		AccountID:            accountID,
		Amount:               in.Amount,
		Channel:              in.Channel,
		ChannelRef:           in.ChannelRef,
		Narration:            in.Narration,
		ValueDate:            in.ValueDate,
		BypassDuplicateCheck: in.BypassDuplicateCheck,
	}

	var result *DepositResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.Deposit {
			_, acct, _, lerr := h.loadProductAccount(r.Context(), tx, accountID)
			if lerr != nil {
				return lerr
			}
			memberID := acct.CounterpartyID
			amount := in.Amount
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:             domain.ApprovalKindDeposit,
				Title:            fmt.Sprintf("Deposit to a/c %s", acct.AccountNo),
				SubjectMemberID:  &memberID,
				SubjectAccountID: &accountID,
				Amount:           &amount,
				Payload:          payload,
				MakerUserID:      userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteDepositTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	h.postDepositToGL(r, tid, result, in.Channel)
	h.emitDeposit(r, tid, userID, result, "DEPOSIT_RECEIVED")
	httpx.Created(w, result)
}

// ─────────── Withdrawal ───────────

type withdrawReq struct {
	Amount     decimal.Decimal       `json:"amount"`
	Channel    domain.DepositChannel `json:"channel"`
	ChannelRef string                `json:"channel_ref"`
	Narration  string                `json:"narration"`
	Reason     string                `json:"reason"`
}

func (h *DepositHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in withdrawReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	if in.Channel == "" || !in.Channel.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("valid channel is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := WithdrawalPayload{
		AccountID:  accountID,
		Amount:     in.Amount,
		Channel:    in.Channel,
		ChannelRef: in.ChannelRef,
		Narration:  in.Narration,
		Reason:     in.Reason,
	}

	var result *WithdrawalResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// BOSA accounts must not drain via the normal withdraw path.
		// The loaded product carries the segment chip from PR 1; we
		// short-circuit before any approval-queueing or ledger work
		// so a misclicked withdraw on a BOSA account is rejected the
		// same way regardless of whether the toggles are on.
		product, _, _, lerr := h.loadProductAccount(r.Context(), tx, accountID)
		if lerr != nil {
			return lerr
		}
		if product.Segment == domain.SegmentBOSA {
			return domain.ErrBOSAWithdrawForbidden
		}
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.Withdrawal {
			_, acct, _, lerr := h.loadProductAccount(r.Context(), tx, accountID)
			if lerr != nil {
				return lerr
			}
			memberID := acct.CounterpartyID
			amount := in.Amount
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:             domain.ApprovalKindWithdrawal,
				Title:            fmt.Sprintf("Withdrawal from a/c %s", acct.AccountNo),
				SubjectMemberID:  &memberID,
				SubjectAccountID: &accountID,
				Amount:           &amount,
				Payload:          payload,
				MakerUserID:      userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteWithdrawalTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	h.postWithdrawalToGL(r, tid, result, in.Channel)
	h.emitWithdrawal(r, tid, userID, result, "WITHDRAWAL_PROCESSED", "")
	httpx.Created(w, result)
}

// channelCashAccount maps the payment channel on a deposit/withdrawal
// to the corresponding cash-side account in the default CoA. The
// member-side leg is always 2000 (Ordinary Savings Deposits) for now;
// product-aware mapping lands when posting_rules become product-scoped
// in a later phase.
func channelCashAccount(ch domain.DepositChannel) string {
	switch ch {
	case domain.DepChannelMpesa:
		return "1030" // M-Pesa Float
	case domain.DepChannelAirtelMoney:
		return "1040" // Airtel Money Float
	case domain.DepChannelBankTransfer:
		return "1020" // Bank Current Account
	default:
		return "1000" // Cash on Hand (cash, teller, standing order, fallback)
	}
}

// postDepositToGL fires the auto-post journal entry after a successful
// deposit. Spec rule:
//     Debit  cash/m-pesa/bank   (per channel)
//     Credit member savings     (liability)
// Posting failure is logged loudly — the deposit itself already
// committed, so we don't unwind. A follow-up reconciliation report
// surfaces unposted transactions; the accounting team can replay them
// via the manual journal entry path.
// resolveLiabilityAcct opens a short tenant-scoped read tx to look up
// the deposit account's product and map (segment, product_type) to a
// CoA liability code. Falls back to 2000 if the lookup fails so the
// GL post still succeeds (degrades to the old behaviour).
func (h *DepositHandler) resolveLiabilityAcct(r *http.Request, tenantID uuid.UUID, productID uuid.UUID) string {
	liab := "2000"
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		p, err := h.Products.GetTx(r.Context(), tx, productID)
		if err == nil && p != nil {
			liab = depositLiabilityCode(p.Segment, p.ProductType)
		}
		return nil
	})
	return liab
}

// depositLiabilityCode resolves the CoA liability account for a
// deposit product. Routing is segment-first (PR 3) — every BOSA
// product maps to 2050 regardless of the underlying product_type;
// FOSA products keep the pre-PR-3 product_type → 2000-range mapping.
//
// The product_type switch enumerates every value explicitly so a new
// type doesn't silently default to ordinary (the original 'group'
// fallthrough caused a quiet GL miscredit). Unknown types fall back
// to 2000 with a clear comment.
func depositLiabilityCode(segment domain.DepositSegment, productType domain.DepositProductType) string {
	if segment == domain.SegmentBOSA {
		// 2050 = Member Deposits (BOSA). Codes 2052–2059 reserved
		// for sub-classed BOSA products if a tenant wants them.
		return "2050"
	}
	switch productType {
	case domain.ProductOrdinary:
		return "2000"
	case domain.ProductHoliday:
		return "2010"
	case domain.ProductEmergency:
		return "2020"
	case domain.ProductGoal:
		return "2030"
	case domain.ProductJunior:
		return "2040"
	case domain.ProductFixed:
		return "2100"
	case domain.ProductGroup:
		// Group / chama savings are pooled FOSA; treat as ordinary
		// for GL purposes until a dedicated 2090 code is added.
		// (PR 3 correction #5 — this case used to fall through.)
		return "2000"
	case domain.ProductMemberDeposit:
		// member_deposit products are always BOSA by definition,
		// caught above. If we somehow get here with a mis-tagged
		// product (FOSA segment + member_deposit type), favour
		// 2050 over the FOSA default to keep the GL classification
		// safe — a misclassified deposit lands on the BOSA line,
		// which is easier to spot in reconciliation than the
		// other way around.
		return "2050"
	}
	return "2000"
}

func (h *DepositHandler) postDepositToGL(r *http.Request, tenantID uuid.UUID, result *DepositResult, ch domain.DepositChannel) {
	if h.Posting == nil || result == nil {
		return
	}
	amount := result.Transaction.Amount
	cashAcct := channelCashAccount(ch)
	// Resolve the product so we credit the right liability account
	// (e.g. fixed deposits go to 2100, not 2000).
	liabAcct := h.resolveLiabilityAcct(r, tenantID, result.Account.ProductID)
	narration := fmt.Sprintf("Deposit %s to a/c %s",
		amount.StringFixed(2), result.Account.AccountNo)
	err := h.Posting.Post(r.Context(), posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.deposits",
		SourceRef:    result.Transaction.ID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: cashAcct, Debit: amount, Narration: "Cash received"},
			{AccountCode: liabAcct, Credit: amount, Narration: "Member savings credited"},
		},
	})
	if err != nil && !errors.Is(err, posting.ErrPostingDisabled) {
		h.Logger.Error("auto-post deposit failed",
			"tenant", tenantID, "tx", result.Transaction.ID, "err", err)
	}
}

// postWithdrawalToGL fires the auto-post for a withdrawal. Spec rule:
//     Debit  member savings   (product-specific liability)
//     Credit cash/m-pesa      (per channel)
func (h *DepositHandler) postWithdrawalToGL(r *http.Request, tenantID uuid.UUID, result *WithdrawalResult, ch domain.DepositChannel) {
	if h.Posting == nil || result == nil {
		return
	}
	amount := result.Transaction.Amount
	cashAcct := channelCashAccount(ch)
	liabAcct := h.resolveLiabilityAcct(r, tenantID, result.Account.ProductID)
	narration := fmt.Sprintf("Withdrawal %s from a/c %s",
		amount.StringFixed(2), result.Account.AccountNo)
	err := h.Posting.Post(r.Context(), posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.deposits",
		SourceRef:    result.Transaction.ID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: liabAcct, Debit: amount, Narration: "Member savings debited"},
			{AccountCode: cashAcct, Credit: amount, Narration: "Cash paid out"},
		},
	})
	if err != nil && !errors.Is(err, posting.ErrPostingDisabled) {
		h.Logger.Error("auto-post withdrawal failed",
			"tenant", tenantID, "tx", result.Transaction.ID, "err", err)
	}
}

// emitDeposit / emitWithdrawal — fire notifications post-commit. We
// re-fetch the member contact info so SMS/email channels have what
// they need (the executor only returned account + transaction).
func (h *DepositHandler) emitDeposit(
	r *http.Request, tenantID, actorID uuid.UUID,
	result *DepositResult, eventCode string,
) {
	if h.Notifier == nil || result == nil {
		return
	}
	var member *store.CounterpartyView
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Counterparties.GetByIDTx(r.Context(), tx, result.Account.CounterpartyID)
		return err
	})
	if member == nil {
		return
	}
	sourceModule := "savings.deposits"
	recordID := result.Transaction.ID
	deepLink := "/deposits/" + result.Account.ID.String()
	memberID := member.ID
	h.Notifier.Notify(r.Context(), notifier.Request{
		TenantID:          tenantID,
		EventCode:         eventCode,
		RecipientMemberID: &memberID,
		RecipientName:     member.FullName,
		RecipientPhone:    strNilIfEmpty(member.Phone),
		RecipientEmail:    strNilIfEmpty(member.Email),
		SourceModule:      &sourceModule,
		SourceRecordID:    &recordID,
		DeepLink:          &deepLink,
		InitiatedBy:       nonZeroUUID(actorID),
		Payload: map[string]any{
			"member_no":      member.MemberNo,
			"full_name":      member.FullName,
			"account_no":     result.Account.AccountNo,
			"amount":         result.Transaction.Amount.String(),
			"new_balance":    result.Account.CurrentBalance.String(),
			"reference":      derefString(result.Transaction.ChannelRef),
			"value_date":     result.Transaction.ValueDate,
		},
	})
}

func (h *DepositHandler) emitWithdrawal(
	r *http.Request, tenantID, actorID uuid.UUID,
	result *WithdrawalResult, eventCode, rejectReason string,
) {
	if h.Notifier == nil || result == nil {
		return
	}
	var member *store.CounterpartyView
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Counterparties.GetByIDTx(r.Context(), tx, result.Account.CounterpartyID)
		return err
	})
	if member == nil {
		return
	}
	sourceModule := "savings.deposits"
	recordID := result.Transaction.ID
	deepLink := "/deposits/" + result.Account.ID.String()
	memberID := member.ID
	payload := map[string]any{
		"member_no":   member.MemberNo,
		"full_name":   member.FullName,
		"account_no":  result.Account.AccountNo,
		"amount":      result.Transaction.Amount.String(),
		"new_balance": result.Account.CurrentBalance.String(),
	}
	if rejectReason != "" {
		payload["rejection_reason"] = rejectReason
	}
	h.Notifier.Notify(r.Context(), notifier.Request{
		TenantID:          tenantID,
		EventCode:         eventCode,
		RecipientMemberID: &memberID,
		RecipientName:     member.FullName,
		RecipientPhone:    strNilIfEmpty(member.Phone),
		RecipientEmail:    strNilIfEmpty(member.Email),
		SourceModule:      &sourceModule,
		SourceRecordID:    &recordID,
		DeepLink:          &deepLink,
		InitiatedBy:       nonZeroUUID(actorID),
		Payload:           payload,
	})
}

// ─────────── Withdrawal notice ───────────

type noticeReq struct {
	Amount decimal.Decimal `json:"amount"`
}

func (h *DepositHandler) GiveWithdrawalNotice(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in noticeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, acct, _, err := h.loadProductAccount(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		if in.Amount.GreaterThan(acct.AvailableBalance) {
			return domain.ErrInsufficientBalance
		}
		return h.Deposits.SetWithdrawalNoticeTx(r.Context(), tx, accountID, in.Amount)
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"status": "ok"})
}

// ─────────── Transfer between own accounts ───────────

type depTransferReq struct {
	Amount             decimal.Decimal `json:"amount"`
	ToAccountID        uuid.UUID       `json:"to_account_id"`
	Narration          string          `json:"narration"`
}

func (h *DepositHandler) TransferBetweenOwn(w http.ResponseWriter, r *http.Request) {
	fromID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in depTransferReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ToAccountID == uuid.Nil || in.ToAccountID == fromID {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to_account_id must be a different account"))
		return
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := DepTransferPayload{
		FromAccountID: fromID,
		ToAccountID:   in.ToAccountID,
		Amount:        in.Amount,
		Narration:     in.Narration,
	}

	var result *DepTransferResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.DepositTransfer {
			_, fromAcct, fromMember, lerr := h.loadProductAccount(r.Context(), tx, fromID)
			if lerr != nil {
				return lerr
			}
			memberID := fromMember.ID
			amount := in.Amount
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:             domain.ApprovalKindDepositTransfer,
				Title:            fmt.Sprintf("Transfer from a/c %s", fromAcct.AccountNo),
				SubjectMemberID:  &memberID,
				SubjectAccountID: &fromID,
				Amount:           &amount,
				Payload:          payload,
				MakerUserID:      userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteDepTransferTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.Created(w, result)
}

// ─────────── Reversal ───────────

type reversalReq struct {
	TxnID  uuid.UUID `json:"txn_id"`
	Reason string    `json:"reason"`
}

func (h *DepositHandler) Reverse(w http.ResponseWriter, r *http.Request) {
	var in reversalReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TxnID == uuid.Nil || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("txn_id and reason are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var resp struct {
		Reversal domain.DepositTransaction `json:"reversal"`
		Account  domain.DepositAccount     `json:"account"`
	}
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		orig, err := h.Deposits.GetTxnTx(r.Context(), tx, in.TxnID)
		if err != nil {
			return err
		}
		if orig.TxnType == domain.TxnReversal {
			return domain.ErrCannotReverseReversal
		}
		if orig.ReversedByTxnID != nil {
			return domain.ErrAlreadyReversed
		}
		acct, err := h.Deposits.GetAccountTx(r.Context(), tx, orig.AccountID)
		if err != nil {
			return err
		}
		// Reversal amount is the inverse of the original signed amount.
		reversedAmount := orig.Amount.Neg()
		// Ensure the resulting balance doesn't go negative.
		if acct.CurrentBalance.Add(reversedAmount).LessThan(decimal.Zero) {
			return domain.ErrInsufficientBalance
		}
		internal := domain.DepChannelInternal
		reason := in.Reason
		reasonPtr := &reason
		narration := "Reversal of " + orig.TxnNo + ": " + in.Reason
		narrPtr := &narration
		rev, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:             acct,
			TxnType:             domain.TxnReversal,
			Amount:              reversedAmount,
			Channel:             &internal,
			Narration:           narrPtr,
			ReversesTxnID:       &orig.ID,
			ReversalReason:      reasonPtr,
			InitiatedBy:         userID,
			AuthorizedBy:        &userID,
			AuthorizationReason: reasonPtr,
		})
		if err != nil {
			return err
		}
		updated, err := h.Deposits.GetAccountTx(r.Context(), tx, acct.ID)
		if err != nil {
			return err
		}
		resp.Reversal = *rev
		resp.Account = *updated
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.Created(w, resp)
}

// ─────────── Adjustment ───────────

type depAdjustReq struct {
	Amount decimal.Decimal `json:"amount"` // signed: + credit, − debit
	Reason string          `json:"reason"`
}

func (h *DepositHandler) Adjust(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in depAdjustReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Amount.IsZero() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be non-zero (signed)"))
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required for adjustment"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var resp struct {
		Transaction domain.DepositTransaction `json:"transaction"`
		Account     domain.DepositAccount     `json:"account"`
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, acct, _, err := h.loadProductAccount(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		if acct.CurrentBalance.Add(in.Amount).LessThan(decimal.Zero) {
			return domain.ErrInsufficientBalance
		}
		internal := domain.DepChannelInternal
		reason := in.Reason
		reasonPtr := &reason
		txn, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:             acct,
			TxnType:             domain.TxnDepAdjustment,
			Amount:              in.Amount,
			Channel:             &internal,
			Narration:           reasonPtr,
			InitiatedBy:         userID,
			AuthorizedBy:        &userID,
			AuthorizationReason: reasonPtr,
		})
		if err != nil {
			return err
		}
		updated, err := h.Deposits.GetAccountTx(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		resp.Transaction = *txn
		resp.Account = *updated
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.Created(w, resp)
}

// ─────────── Statement ───────────

type statementResp struct {
	Account        domain.DepositAccount       `json:"account"`
	Product        domain.DepositProduct       `json:"product"`
	From           string                      `json:"from"`
	To             string                      `json:"to"`
	OpeningBalance decimal.Decimal             `json:"opening_balance"`
	ClosingBalance decimal.Decimal             `json:"closing_balance"`
	Transactions   []domain.DepositTransaction `json:"transactions"`
}

func (h *DepositHandler) Statement(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseUUIDParam(r, "account_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	q := r.URL.Query()
	from, to, err := parseDateRange(q.Get("from"), q.Get("to"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp statementResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, acct, _, err := h.loadProductAccount(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		txns, opening, err := h.Deposits.StatementTx(r.Context(), tx, accountID, from, to, 1000, 0)
		if err != nil {
			return err
		}
		closing := opening
		if len(txns) > 0 {
			closing = txns[len(txns)-1].BalanceAfter
		}
		resp = statementResp{
			Account:        *acct,
			Product:        *product,
			From:           from.Format("2006-01-02"),
			To:             to.Format("2006-01-02"),
			OpeningBalance: opening,
			ClosingBalance: closing,
			Transactions:   txns,
		}
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

func parseDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	if fromStr == "" || toStr == "" {
		// Default: last 90 days.
		to := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, 1)
		from := to.AddDate(0, 0, -90)
		return from, to, nil
	}
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("from must be YYYY-MM-DD")
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("to must be YYYY-MM-DD")
	}
	// Make `to` exclusive end-of-day.
	to = to.AddDate(0, 0, 1)
	if to.Before(from) {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("to must be on or after from")
	}
	return from, to, nil
}

// ─────────── Snapshot job ───────────

// RunDailySnapshot is invoked from the -run-snapshot CLI flag. Captures
// end-of-day balances for every non-closed account so the Phase 4
// interest engine has weighted-average inputs.
func RunDailySnapshot(ctx context.Context, h *DepositHandler, tenantID uuid.UUID, date time.Time) (int, error) {
	var n int
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		n, err = h.Deposits.SnapshotForDateTx(ctx, tx, date)
		return err
	})
	return n, err
}

// ─────────── Error mapping ───────────

func writeDepositErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	case errors.Is(err, domain.ErrBOSAWithdrawForbidden):
		// Spec-mandated literal code so callers (UI + tests) can
		// branch on it without having to inspect the message. 403
		// because the resource exists but the action is structurally
		// disallowed for this product segment.
		httpx.WriteErr(w, r, httpx.E(http.StatusForbidden, "BOSA_WITHDRAW_FORBIDDEN", err.Error()))
	case errors.Is(err, domain.ErrInsufficientBalance),
		errors.Is(err, domain.ErrBelowMinOpeningBalance),
		errors.Is(err, domain.ErrBelowMinDeposit),
		errors.Is(err, domain.ErrAboveMaxDeposit),
		errors.Is(err, domain.ErrBelowMinWithdrawal),
		errors.Is(err, domain.ErrAboveMaxWithdrawal),
		errors.Is(err, domain.ErrWouldBreachMinBalance),
		errors.Is(err, domain.ErrWouldExceedMaxBalance),
		errors.Is(err, domain.ErrLockInActive),
		errors.Is(err, domain.ErrOutsideWithdrawalWindow),
		errors.Is(err, domain.ErrPartialWithdrawalNotAllowed),
		errors.Is(err, domain.ErrFrequencyCapReached),
		errors.Is(err, domain.ErrNoticePeriodNotMet),
		errors.Is(err, domain.ErrDuplicateTransaction),
		errors.Is(err, domain.ErrCannotReverseReversal),
		errors.Is(err, domain.ErrAlreadyReversed),
		errors.Is(err, domain.ErrAccountNotActive),
		errors.Is(err, domain.ErrProductInactive),
		errors.Is(err, domain.ErrProductIneligible),
		errors.Is(err, domain.ErrMemberIneligibleStatus):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}
