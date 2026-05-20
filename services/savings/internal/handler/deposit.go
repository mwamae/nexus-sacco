// Deposit account + transaction handlers.
//
// Endpoints orchestrate: load product + account + member, evaluate
// product rules, post the ledger row atomically.

package handler

import (
	"context"
	"errors"
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
	"github.com/nexussacco/savings/internal/store"
)

type DepositHandler struct {
	DB       *db.Pool
	Tenants  *store.TenantStore
	Members  *store.MemberStore
	Products *store.DepositProductStore
	Deposits *store.DepositStore
	Logger   *slog.Logger

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

func memberKind(_ *store.MemberLite) string {
	// Phase 3 individuals only. Once org/group support is wired through
	// the member service, infer from members.kind here.
	return "individual"
}

func (h *DepositHandler) loadProductAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) (*domain.DepositProduct, *domain.DepositAccount, *store.MemberLite, error) {
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
	member, err := h.Members.GetTx(ctx, tx, acct.MemberID)
	if err != nil {
		return nil, nil, nil, err
	}
	return product, acct, member, nil
}

// ─────────── Account open ───────────

type openAcctReq struct {
	MemberID             uuid.UUID                `json:"member_id"`
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
	if in.MemberID == uuid.Nil || in.ProductID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("member_id and product_id are required"))
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
		member, err := h.Members.GetTx(r.Context(), tx, in.MemberID)
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
			MemberID:             in.MemberID,
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
		_ = h.Members.TouchActivityTx(r.Context(), tx, in.MemberID)
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
	Account domain.DepositAccount `json:"account"`
	Product domain.DepositProduct `json:"product"`
	Member  store.MemberLite      `json:"member"`
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
	memberID, err := parseUUIDParam(r, "member_id")
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
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var vd *time.Time
	if in.ValueDate != "" {
		d, err := time.Parse("2006-01-02", in.ValueDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
		vd = &d
	}

	var resp struct {
		Transaction domain.DepositTransaction `json:"transaction"`
		Account     domain.DepositAccount     `json:"account"`
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, acct, _, err := h.loadProductAccount(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		if err := domain.EvaluateDeposit(product, acct, in.Amount); err != nil {
			return err
		}
		if in.ChannelRef != "" && !in.BypassDuplicateCheck {
			dup, err := h.Deposits.DuplicateExistsTx(r.Context(), tx, accountID, in.Amount, in.ChannelRef, h.lookback())
			if err != nil {
				return err
			}
			if dup {
				return domain.ErrDuplicateTransaction
			}
		}
		ch := in.Channel
		ref := strNilIfEmpty(in.ChannelRef)
		narr := strNilIfEmpty(in.Narration)
		txn, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:     acct,
			TxnType:     domain.TxnDeposit,
			Amount:      in.Amount,
			ValueDate:   vd,
			Channel:     &ch,
			ChannelRef:  ref,
			Narration:   narr,
			InitiatedBy: userID,
		})
		if err != nil {
			return err
		}
		updated, err := h.Deposits.GetAccountTx(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		_ = h.Members.TouchActivityTx(r.Context(), tx, acct.MemberID)
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

	var resp struct {
		Transaction       domain.DepositTransaction `json:"transaction"`
		Account           domain.DepositAccount     `json:"account"`
		RequiresApproval  bool                      `json:"requires_approval"`
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		product, acct, _, err := h.loadProductAccount(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		monthly, err := h.Deposits.WithdrawalCountThisMonthTx(r.Context(), tx, accountID, time.Now())
		if err != nil {
			return err
		}
		if err := domain.EvaluateWithdrawal(product, acct, in.Amount, time.Now(), monthly); err != nil {
			return err
		}
		// Large-withdrawal hint — Phase 4 will gate via workflow. For now,
		// flag it in the response and require an authorization reason.
		if domain.IsLargeWithdrawal(product, in.Amount) {
			if in.Reason == "" {
				return httpx.ErrConflict("withdrawal exceeds the product's large-withdrawal threshold; reason is required (workflow gating ships in Phase 4)")
			}
			resp.RequiresApproval = true
		}
		ch := in.Channel
		ref := strNilIfEmpty(in.ChannelRef)
		narr := strNilIfEmpty(in.Narration)
		reason := strNilIfEmpty(in.Reason)
		txn, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:             acct,
			TxnType:             domain.TxnWithdrawal,
			Amount:              in.Amount,
			Channel:             &ch,
			ChannelRef:          ref,
			Narration:           narr,
			InitiatedBy:         userID,
			AuthorizedBy:        &userID,
			AuthorizationReason: reason,
		})
		if err != nil {
			return err
		}
		// Clear withdrawal-notice on successful withdrawal.
		_ = h.Deposits.ClearWithdrawalNoticeTx(r.Context(), tx, accountID)
		updated, err := h.Deposits.GetAccountTx(r.Context(), tx, accountID)
		if err != nil {
			return err
		}
		_ = h.Members.TouchActivityTx(r.Context(), tx, acct.MemberID)
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

	var out struct {
		From struct {
			Transaction domain.DepositTransaction `json:"transaction"`
			Account     domain.DepositAccount     `json:"account"`
		} `json:"from"`
		To struct {
			Transaction domain.DepositTransaction `json:"transaction"`
			Account     domain.DepositAccount     `json:"account"`
		} `json:"to"`
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		fromProduct, fromAcct, fromMember, err := h.loadProductAccount(r.Context(), tx, fromID)
		if err != nil {
			return err
		}
		toProduct, toAcct, toMember, err := h.loadProductAccount(r.Context(), tx, in.ToAccountID)
		if err != nil {
			return err
		}
		// Same-member check.
		if fromMember.ID != toMember.ID {
			return httpx.ErrConflict("transfer endpoints are restricted to the same member's accounts")
		}
		monthly, err := h.Deposits.WithdrawalCountThisMonthTx(r.Context(), tx, fromID, time.Now())
		if err != nil {
			return err
		}
		if err := domain.EvaluateWithdrawal(fromProduct, fromAcct, in.Amount, time.Now(), monthly); err != nil {
			return err
		}
		if err := domain.EvaluateDeposit(toProduct, toAcct, in.Amount); err != nil {
			return err
		}
		internal := domain.DepChannelInternal
		narration := in.Narration
		if narration == "" {
			narration = "Transfer between own deposit accounts"
		}
		narrPtr := &narration
		outTxn, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:             fromAcct,
			TxnType:             domain.TxnDepTransferOut,
			Amount:              in.Amount,
			Channel:             &internal,
			Narration:           narrPtr,
			CounterpartyAccount: toAcct,
			InitiatedBy:         userID,
		})
		if err != nil {
			return err
		}
		fromAcct, err = h.Deposits.GetAccountTx(r.Context(), tx, fromID)
		if err != nil {
			return err
		}
		inTxn, err := h.Deposits.PostTxnTx(r.Context(), tx, store.PostDepInput{
			Account:             toAcct,
			TxnType:             domain.TxnDepTransferIn,
			Amount:              in.Amount,
			Channel:             &internal,
			Narration:           narrPtr,
			CounterpartyAccount: fromAcct,
			CounterpartyTxnID:   &outTxn.ID,
			InitiatedBy:         userID,
		})
		if err != nil {
			return err
		}
		if err := h.Deposits.LinkCounterpartyTxnTx(r.Context(), tx, outTxn.ID, inTxn.ID); err != nil {
			return err
		}
		toAcct, err = h.Deposits.GetAccountTx(r.Context(), tx, in.ToAccountID)
		if err != nil {
			return err
		}
		out.From.Transaction = *outTxn
		out.From.Account = *fromAcct
		out.To.Transaction = *inTxn
		out.To.Account = *toAcct
		return nil
	})
	if err != nil {
		writeDepositErr(w, r, err)
		return
	}
	httpx.Created(w, out)
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
