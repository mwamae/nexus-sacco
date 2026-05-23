// Share-account HTTP handlers.
//
// Every mutating call goes through ShareStore inside a tenant-bound
// pgx.Tx. Business rules (min/max holding, lien constraints, member
// status eligibility) are enforced here before posting.

package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

type ShareHandler struct {
	DB        *db.Pool
	Tenants   *store.TenantStore
	Members   *store.MemberStore
	Shares    *store.ShareStore
	Approvals *store.ApprovalsStore
	Notifier  *notifier.Client
	Posting   *posting.Client
	Logger    *slog.Logger
}

// ─────────── Helpers ───────────

// memberEligible blocks share operations for statuses where a member
// is not legally able to act on their equity (blacklisted, exited,
// deceased, rejected). pending + active + dormant + suspended are
// allowed — suspended members can still hold shares; dormant ones can
// reactivate via share top-up.
func memberEligible(status string) bool {
	switch status {
	case "pending", "active", "dormant", "suspended":
		return true
	}
	return false
}

func parseUUIDParam(r *http.Request, key string) (uuid.UUID, error) {
	raw := chi.URLParam(r, key)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, httpx.ErrBadRequest("invalid " + key + ": " + err.Error())
	}
	return id, nil
}

// loadContext fetches the policy, member, and (optionally creates) account
// inside a single transaction. The caller continues mutations on the same tx.
func (h *ShareHandler) loadContext(ctx context.Context, tx pgx.Tx, memberID uuid.UUID, ensure bool) (*store.SharePolicy, *store.MemberLite, *domain.ShareAccount, error) {
	policy, err := h.Tenants.SharePolicyTx(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	member, err := h.Members.GetTx(ctx, tx, memberID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, httpx.ErrNotFound("member not found")
		}
		return nil, nil, nil, err
	}
	// Eligibility gates WRITES only — viewing equity must always work so
	// admins can see what's there during the exit workflow (where the
	// shares must be transferred to another active member before exit
	// can be finalised). Per-handler write checks live in
	// requireWriteEligible.
	var account *domain.ShareAccount
	if ensure {
		account, err = h.Shares.EnsureAccountTx(ctx, tx, memberID, policy.ParValue)
	} else {
		account, err = h.Shares.GetAccountByMemberTx(ctx, tx, memberID)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, httpx.ErrNotFound("share account not found")
		}
		return nil, nil, nil, err
	}
	return policy, member, account, nil
}

// requireWriteEligible returns an error when the member's status
// forbids the requested operation. Transfer-out is explicitly allowed
// for blacklisted/exited members so an exiting member can move their
// shares to another active member before the exit workflow finalises
// — share capital is equity and cannot be redeemed for cash, so the
// only legitimate way to clear an exiting balance is via transfer.
func requireWriteEligible(member *store.MemberLite, op string) error {
	if memberEligible(member.Status) {
		return nil
	}
	if op == "transfer" {
		switch member.Status {
		case "blacklisted", "exited":
			return nil
		}
	}
	return httpx.ErrForbidden("member status '" + member.Status + "' does not permit '" + op + "' on shares")
}

// ─────────── Policy ───────────

type policyDTO struct {
	ParValue              decimal.Decimal `json:"par_value"`
	MinSharesRequired     int             `json:"min_shares_required"`
	MaxSharesPctOfCapital decimal.Decimal `json:"max_shares_pct_of_capital"`
	CertificatePrefix     string          `json:"certificate_prefix"`
}

func (h *ShareHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var dto policyDTO
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		p, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		dto = policyDTO{
			ParValue:              p.ParValue,
			MinSharesRequired:     p.MinSharesRequired,
			MaxSharesPctOfCapital: p.MaxSharesPctOfCapital,
			CertificatePrefix:     p.CertificatePrefix,
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, dto)
}

func (h *ShareHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var in policyDTO
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ParValue.LessThanOrEqual(decimal.Zero) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("par_value must be positive"))
		return
	}
	if in.MinSharesRequired < 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("min_shares_required must be >= 0"))
		return
	}
	if in.MaxSharesPctOfCapital.LessThan(decimal.Zero) || in.MaxSharesPctOfCapital.GreaterThan(decimal.NewFromInt(100)) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("max_shares_pct_of_capital must be between 0 and 100"))
		return
	}
	if in.CertificatePrefix == "" {
		in.CertificatePrefix = "SC"
	}

	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE tenant_operations
			   SET share_par_value = $1,
			       min_shares_required = $2,
			       max_shares_pct_of_capital = $3,
			       share_certificate_prefix = $4
		`, in.ParValue, in.MinSharesRequired, in.MaxSharesPctOfCapital, in.CertificatePrefix)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, in)
}

// ─────────── Account reads ───────────

type accountDTO struct {
	Account     domain.ShareAccount `json:"account"`
	Member      store.MemberLite    `json:"member"`
	Liens       []domain.ShareLien  `json:"active_liens"`
	Certificate *domain.ShareCertificate `json:"current_certificate,omitempty"`
	Policy      policyDTO           `json:"policy"`
}

func (h *ShareHandler) GetByMember(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var dto accountDTO
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		policy, member, acct, err := h.loadContext(r.Context(), tx, memberID, true)
		if err != nil {
			return err
		}
		liens, err := h.Shares.LiensForAccountTx(r.Context(), tx, acct.ID, true)
		if err != nil {
			return err
		}
		if liens == nil {
			liens = []domain.ShareLien{}
		}
		cert, err := h.Shares.CurrentCertificateTx(r.Context(), tx, acct.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		dto = accountDTO{
			Account:     *acct,
			Member:      *member,
			Liens:       liens,
			Certificate: cert,
			Policy: policyDTO{
				ParValue:              policy.ParValue,
				MinSharesRequired:     policy.MinSharesRequired,
				MaxSharesPctOfCapital: policy.MaxSharesPctOfCapital,
				CertificatePrefix:     policy.CertificatePrefix,
			},
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, dto)
}

func (h *ShareHandler) HistoryByMember(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	tid, _ := middleware.TenantIDFrom(r)

	var out []domain.ShareTransaction
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		acct, err := h.Shares.GetAccountByMemberTx(r.Context(), tx, memberID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out = nil
				return nil
			}
			return err
		}
		out, err = h.Shares.HistoryByAccountTx(r.Context(), tx, acct.ID, limit, offset)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.ShareTransaction{}
	}
	httpx.OK(w, out)
}

func (h *ShareHandler) CurrentCertificate(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var cert *domain.ShareCertificate
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		acct, err := h.Shares.GetAccountByMemberTx(r.Context(), tx, memberID)
		if err != nil {
			return err
		}
		cert, err = h.Shares.CurrentCertificateTx(r.Context(), tx, acct.ID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("no current certificate"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, cert)
}

// ─────────── Purchase ───────────

type purchaseReq struct {
	Shares         int                    `json:"shares"`
	PaymentChannel domain.PaymentChannel  `json:"payment_channel"`
	PaymentRef     string                 `json:"payment_ref"`
	Narration      string                 `json:"narration"`
}

type postResp struct {
	Transaction domain.ShareTransaction  `json:"transaction"`
	Account     domain.ShareAccount      `json:"account"`
	Certificate *domain.ShareCertificate `json:"certificate,omitempty"`
}

func (h *ShareHandler) Purchase(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in purchaseReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Shares <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("shares must be a positive integer"))
		return
	}
	if in.PaymentChannel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("payment_channel is required"))
		return
	}
	if !in.PaymentChannel.Valid() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid payment_channel"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := SharePurchasePayload{
		CounterpartyID:       memberID,
		Shares:         in.Shares,
		PaymentChannel: in.PaymentChannel,
		PaymentRef:     in.PaymentRef,
		Narration:      in.Narration,
	}
	var result *SharePostResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.SharePurchase {
			policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
			if err != nil {
				return err
			}
			amount := policy.ParValue.Mul(decimal.NewFromInt(int64(in.Shares)))
			m := memberID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindSharePurchase,
				Title:           fmt.Sprintf("Buy %d shares", in.Shares),
				SubjectMemberID: &m,
				Amount:          &amount,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteSharePurchaseTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	// Auto-post the GL entry:
	//   Debit  Cash / M-Pesa / Bank / Savings  (per payment channel)
	//   Credit Member Share Capital (equity)
	h.postSharePurchaseToGL(r, tid, result, in.PaymentChannel)
	h.emitSharePurchase(r, tid, userID, result)
	httpx.Created(w, result)
}

// shareChannelCashAccount picks the debit-side CoA code for the
// channel a share purchase came in through. "internal" means the cost
// was deducted from the member's savings — debit the savings
// liability (member's savings goes down).
func shareChannelCashAccount(ch domain.PaymentChannel) string {
	switch ch {
	case domain.ChannelMpesa:
		return "1030"
	case domain.ChannelAirtelMoney:
		return "1040"
	case domain.ChannelBankTransfer, domain.ChannelPayroll, domain.ChannelStandingOrder:
		return "1020"
	case domain.ChannelInternal:
		return "2000"
	default:
		return "1000"
	}
}

func (h *ShareHandler) postSharePurchaseToGL(r *http.Request, tenantID uuid.UUID, result *SharePostResult, ch domain.PaymentChannel) {
	if h.Posting == nil || result == nil {
		return
	}
	amount := result.Transaction.Amount
	if amount.IsZero() {
		return
	}
	cashAcct := shareChannelCashAccount(ch)
	narration := fmt.Sprintf("Share purchase %d shares · %s",
		result.Transaction.SharesDelta, result.Account.AccountNo)
	err := h.Posting.Post(r.Context(), posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.shares.purchase",
		SourceRef:    result.Transaction.ID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: cashAcct, Debit: amount, Narration: "Payment received"},
			{AccountCode: "3000", Credit: amount, Narration: "Member share capital"},
		},
	})
	if err != nil && !errors.Is(err, posting.ErrPostingDisabled) {
		h.Logger.Error("auto-post share purchase failed",
			"tenant", tenantID, "tx", result.Transaction.ID, "err", err)
	}
}

// ─────────── Transfer ───────────

type transferReq struct {
	Shares         int       `json:"shares"`
	ToMemberID     uuid.UUID `json:"to_member_id"`
	Narration      string    `json:"narration"`
	Reason         string    `json:"reason"`
}

type transferResp struct {
	From postResp `json:"from"`
	To   postResp `json:"to"`
}

func (h *ShareHandler) Transfer(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in transferReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Shares <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("shares must be a positive integer"))
		return
	}
	if in.ToMemberID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("to_member_id is required"))
		return
	}
	if in.ToMemberID == memberID {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("cannot transfer shares to the same member"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := ShareTransferPayload{
		FromMemberID: memberID,
		ToMemberID:   in.ToMemberID,
		Shares:       in.Shares,
		Narration:    in.Narration,
		Reason:       in.Reason,
	}
	var result *ShareTransferResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.ShareTransfer {
			policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
			if err != nil {
				return err
			}
			amount := policy.ParValue.Mul(decimal.NewFromInt(int64(in.Shares)))
			m := memberID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindShareTransfer,
				Title:           fmt.Sprintf("Transfer %d shares between members", in.Shares),
				SubjectMemberID: &m,
				Amount:          &amount,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteShareTransferTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	h.emitShareTransfer(r, tid, userID, result)
	httpx.Created(w, result)
}

// emitSharePurchase fires SHARE_PURCHASE_CONFIRMED for the member who
// bought, plus SHARE_CERTIFICATE_ISSUED if the post produced a new
// certificate.
func (h *ShareHandler) emitSharePurchase(r *http.Request, tenantID, actorID uuid.UUID, result *SharePostResult) {
	if h.Notifier == nil || result == nil {
		return
	}
	var member *store.MemberLite
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Members.GetByCounterpartyTx(r.Context(), tx, result.Account.CounterpartyID)
		return err
	})
	if member == nil {
		return
	}
	sourceModule := "savings.shares"
	recordID := result.Transaction.ID
	deepLink := "/shares?member=" + member.ID.String()
	mid := member.ID
	h.Notifier.Notify(r.Context(), notifier.Request{
		TenantID:          tenantID,
		EventCode:         "SHARE_PURCHASE_CONFIRMED",
		RecipientMemberID: &mid,
		RecipientName:     member.FullName,
		RecipientPhone:    strNilIfEmpty(member.Phone),
		RecipientEmail:    strNilIfEmpty(member.Email),
		SourceModule:      &sourceModule,
		SourceRecordID:    &recordID,
		DeepLink:          &deepLink,
		InitiatedBy:       nonZeroUUID(actorID),
		Payload: map[string]any{
			"member_no":   member.MemberNo,
			"full_name":   member.FullName,
			"shares":      result.Transaction.SharesDelta,
			"par_value":   result.Transaction.Amount.String(),
			"total_held":  result.Account.SharesHeld,
		},
	})
	if result.Certificate != nil {
		certID := result.Certificate.ID
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tenantID,
			EventCode:         "SHARE_CERTIFICATE_ISSUED",
			RecipientMemberID: &mid,
			RecipientName:     member.FullName,
			RecipientPhone:    strNilIfEmpty(member.Phone),
			RecipientEmail:    strNilIfEmpty(member.Email),
			SourceModule:      &sourceModule,
			SourceRecordID:    &certID,
			DeepLink:          &deepLink,
			InitiatedBy:       nonZeroUUID(actorID),
			Payload: map[string]any{
				"member_no":          member.MemberNo,
				"full_name":          member.FullName,
				"certificate_no":     result.Certificate.CertificateNo,
				"shares_certified":   result.Certificate.SharesCovered,
			},
		})
	}
}

// emitShareTransfer fires SHARE_TRANSFER_COMPLETED twice — once for
// the sender, once for the receiver. They each see their own balance
// change.
func (h *ShareHandler) emitShareTransfer(r *http.Request, tenantID, actorID uuid.UUID, result *ShareTransferResult) {
	if h.Notifier == nil || result == nil {
		return
	}
	sourceModule := "savings.shares"
	recordID := result.From.Transaction.ID
	for _, side := range []struct {
		role string
		r    SharePostResult
	}{
		{"sender", result.From},
		{"receiver", result.To},
	} {
		var member *store.MemberLite
		_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			var err error
			member, err = h.Members.GetByCounterpartyTx(r.Context(), tx, side.r.Account.CounterpartyID)
			return err
		})
		if member == nil {
			continue
		}
		mid := member.ID
		deepLink := "/shares?member=" + member.ID.String()
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tenantID,
			EventCode:         "SHARE_TRANSFER_COMPLETED",
			RecipientMemberID: &mid,
			RecipientName:     member.FullName,
			RecipientPhone:    strNilIfEmpty(member.Phone),
			RecipientEmail:    strNilIfEmpty(member.Email),
			SourceModule:      &sourceModule,
			SourceRecordID:    &recordID,
			DeepLink:          &deepLink,
			InitiatedBy:       nonZeroUUID(actorID),
			Payload: map[string]any{
				"member_no":  member.MemberNo,
				"full_name":  member.FullName,
				"role":       side.role,
				"shares":     side.r.Transaction.SharesDelta,
				"total_held": side.r.Account.SharesHeld,
			},
		})
	}
}

// NOTE: Share redemption is intentionally NOT supported. Share
// capital is equity per the Cooperative Societies Act + SASRA
// prudential framework — it cannot be bought back by the SACCO on
// member demand. An exiting member must transfer their share balance
// to another active member; see the Transfer handler above and the
// member-service exit guard which blocks 'exited' status until
// shares_held = 0.

// ─────────── Adjustment ───────────

type adjustReq struct {
	SharesDelta int    `json:"shares_delta"` // signed
	Reason      string `json:"reason"`
}

func (h *ShareHandler) Adjust(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in adjustReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.SharesDelta == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("shares_delta must be non-zero"))
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required for adjustment"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var resp postResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		policy, member, acct, err := h.loadContext(r.Context(), tx, memberID, true)
		if err != nil {
			return err
		}
		// Debits must respect available shares; credits respect max cap.
		if in.SharesDelta < 0 {
			if domain.AvailableShares(acct) < -in.SharesDelta {
				if acct.SharesPledged > 0 {
					return domain.ErrLienBlocksAction
				}
				return domain.ErrInsufficientShares
			}
		} else if policy.MaxSharesPctOfCapital.GreaterThan(decimal.Zero) {
			total, err := h.Shares.TotalSharesIssuedTx(r.Context(), tx)
			if err != nil {
				return err
			}
			newMember := acct.SharesHeld + in.SharesDelta
			newTotal := total + in.SharesDelta
			if newTotal > 0 {
				pct := decimal.NewFromInt(int64(newMember)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(newTotal)))
				if pct.GreaterThan(policy.MaxSharesPctOfCapital) {
					return domain.ErrExceedsMaxHolding
				}
			}
		}
		internal := domain.ChannelInternal
		reason := in.Reason
		txn, err := h.Shares.PostTxnTx(r.Context(), tx, store.PostInput{
			Account:             acct,
			TxnType:             domain.TxnAdjustment,
			SharesDelta:         in.SharesDelta,
			ParValueAtTxn:       policy.ParValue,
			PaymentChannel:      &internal,
			Narration:           &reason,
			InitiatedBy:         userID,
			AuthorizedBy:        &userID,
			AuthorizationReason: &reason,
		})
		if err != nil {
			return err
		}
		updated, err := h.Shares.GetAccountTx(r.Context(), tx, acct.ID)
		if err != nil {
			return err
		}
		cert, err := h.Shares.IssueCertificateTx(r.Context(), tx, acct.ID, member.ID, userID,
			updated.SharesHeld, policy.ParValue, policy.CertificatePrefix)
		if err != nil {
			return err
		}
		resp = postResp{Transaction: *txn, Account: *updated, Certificate: cert}
		return nil
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	httpx.Created(w, resp)
}

// ─────────── Liens ───────────

type placeLienReq struct {
	Shares        int    `json:"shares"`
	Reason        string `json:"reason"`
	ReferenceKind string `json:"reference_kind"`
	ReferenceID   string `json:"reference_id"`
}

func (h *ShareHandler) PlaceLien(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in placeLienReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Shares <= 0 || in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("shares (>0) and reason are required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := ShareLienPayload{
		CounterpartyID:      memberID,
		Shares:        in.Shares,
		Reason:        in.Reason,
		ReferenceKind: in.ReferenceKind,
		ReferenceID:   in.ReferenceID,
	}
	var lien *domain.ShareLien
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.ShareLien {
			m := memberID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindShareLien,
				Title:           fmt.Sprintf("Place lien on %d shares", in.Shares),
				SubjectMemberID: &m,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		out, err := h.ExecuteShareLienTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		lien = out
		return nil
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.Created(w, lien)
}

type releaseLienReq struct {
	Reason string `json:"reason"`
}

func (h *ShareHandler) ReleaseLien(w http.ResponseWriter, r *http.Request) {
	lienID, err := parseUUIDParam(r, "lien_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in releaseLienReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var lien *domain.ShareLien
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		lien, err = h.Shares.ReleaseLienTx(r.Context(), tx, lienID, userID, in.Reason)
		return err
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	httpx.OK(w, lien)
}

// ─────────── Bonus issue (tenant-wide) ───────────

type bonusIssueReq struct {
	// PctOfHolding is a percent like "5.0" meaning each member gets
	// floor(holding * 0.05) bonus shares. Whichever members hold zero
	// are skipped. Min one bonus share is awarded to anyone with > 0.
	PctOfHolding decimal.Decimal `json:"pct_of_holding"`
	Reason       string          `json:"reason"`
}

type bonusIssueResp struct {
	IssuedToCount    int             `json:"issued_to_count"`
	TotalBonusShares int             `json:"total_bonus_shares"`
	PctApplied       decimal.Decimal `json:"pct_applied"`
}

func (h *ShareHandler) BonusIssue(w http.ResponseWriter, r *http.Request) {
	var in bonusIssueReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.PctOfHolding.LessThanOrEqual(decimal.Zero) || in.PctOfHolding.GreaterThan(decimal.NewFromInt(100)) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("pct_of_holding must be > 0 and <= 100"))
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required (AGM resolution reference)"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := ShareBonusPayload{
		PctOfHolding: in.PctOfHolding,
		Reason:       in.Reason,
	}
	var resp bonusIssueResp
	resp.PctApplied = in.PctOfHolding
	var pending *domain.PendingApproval
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.ShareBonus {
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:        domain.ApprovalKindShareBonus,
				Title:       fmt.Sprintf("Bonus issue %s%% to all active accounts", in.PctOfHolding.String()),
				Payload:     payload,
				MakerUserID: userID,
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		out, err := h.ExecuteShareBonusTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		resp.IssuedToCount = out.IssuedToCount
		resp.TotalBonusShares = out.TotalBonusShares
		resp.PctApplied = out.PctApplied
		return nil
	})
	if err != nil {
		writeBusinessErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.Created(w, resp)
}

// ─────────── Register / Summary ───────────

type listResp struct {
	Items []store.AccountListItem `json:"items"`
	Total int                     `json:"total"`
}

func (h *ShareHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter := store.ListFilter{
		Status: q.Get("status"),
		Q:      q.Get("q"),
		Limit:  limit, Offset: offset,
	}
	if q.Get("below_min") == "1" || q.Get("below_min") == "true" {
		filter.MinBelow = true
	}

	var out listResp
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		items, total, err := h.Shares.ListAccountsTx(r.Context(), tx, filter, policy.MinSharesRequired)
		if err != nil {
			return err
		}
		if items == nil {
			items = []store.AccountListItem{}
		}
		out = listResp{Items: items, Total: total}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *ShareHandler) Summary(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var sum *store.Summary
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		sum, err = h.Shares.SummaryTx(r.Context(), tx, policy)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, sum)
}

// ─────────── Helpers ───────────

func strNilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// writeBusinessErr maps domain errors to HTTP error responses.
func writeBusinessErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInsufficientShares),
		errors.Is(err, domain.ErrLienBlocksAction),
		errors.Is(err, domain.ErrBelowMinHolding),
		errors.Is(err, domain.ErrExceedsMaxHolding),
		errors.Is(err, domain.ErrAccountClosed),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrSameMemberTransfer):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound(""))
	default:
		httpx.WriteErr(w, r, err)
	}
}
