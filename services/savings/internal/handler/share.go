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
	"github.com/nexussacco/savings/internal/postingops"
	"github.com/nexussacco/savings/internal/receiptops"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type ShareHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	Shares         *store.ShareStore
	Approvals      *store.ApprovalsStore
	// Receipts + VirtualTills drive the inline-panel receipt writes
	// added in the receiptops PR — every share-buy now leaves a row
	// in receipts/receipt_lines so Today's receipts sees the event.
	Receipts     *store.ReceiptStore
	VirtualTills *store.VirtualTillStore
	Notifier     *notifier.Client
	Posting      *posting.Client
	Logger       *slog.Logger

	// Workflow + SavingsSelfURL drive the new wf path. See
	// DepositHandler for the same fields.
	Workflow       *workflowclient.Client
	SavingsSelfURL string
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

// loadContext fetches the policy, counterparty's member view, and
// (optionally creates) share account inside a single transaction.
// Phase D sub-PR 3: the `cpID` parameter is now a counterparty.id
// (was a members.id with internal resolve).
func (h *ShareHandler) loadContext(ctx context.Context, tx pgx.Tx, cpID uuid.UUID, ensure bool) (*store.SharePolicy, *store.CounterpartyView, *domain.ShareAccount, error) {
	policy, err := h.Tenants.SharePolicyTx(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	member, err := h.Counterparties.GetByIDTx(ctx, tx, cpID)
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
		account, err = h.Shares.EnsureAccountTx(ctx, tx, cpID, policy.ParValue)
	} else {
		account, err = h.Shares.GetAccountByCounterpartyTx(ctx, tx, cpID)
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
func requireWriteEligible(member *store.CounterpartyView, op string) error {
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
	Account     domain.ShareAccount      `json:"account"`
	Member      store.CounterpartyView   `json:"member"`
	Liens       []domain.ShareLien       `json:"active_liens"`
	Certificate *domain.ShareCertificate `json:"current_certificate,omitempty"`
	Policy      policyDTO                `json:"policy"`
}

// GetAccountByID returns the bare share account by its uuid. Used by
// the AccountRef admin component to resolve share_account.id → display
// label without going through the heavier GetByMember envelope
// (which loads liens + cert + policy).
func (h *ShareHandler) GetAccountByID(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var acct *domain.ShareAccount
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		a, err := h.Shares.GetAccountTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		acct = a
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("share account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, acct)
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
		acct, err := h.Shares.GetAccountByCounterpartyTx(r.Context(), tx, memberID)
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
		acct, err := h.Shares.GetAccountByCounterpartyTx(r.Context(), tx, memberID)
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
	// Inline cash is blocked — telephone-style "type the amount and
	// submit" can't be reconciled against a teller's drawer. The UI
	// hard-blocks the Cash option in the modal; the 412 here is the
	// server-side hard stop for any direct API caller.
	if in.PaymentChannel == domain.ChannelCash {
		httpx.WriteErr(w, r, httpx.ErrCashInlineBlocked(memberID.String()))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	payload := SharePurchasePayload{
		CounterpartyID: memberID,
		Shares:         in.Shares,
		PaymentChannel: in.PaymentChannel,
		PaymentRef:     in.PaymentRef,
		Narration:      in.Narration,
	}
	var result *SharePostResult
	var pending *domain.PendingApproval
	var receipt *domain.Receipt
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		policy, err := h.Tenants.SharePolicyTx(r.Context(), tx)
		if err != nil {
			return err
		}
		amount := policy.ParValue.Mul(decimal.NewFromInt(int64(in.Shares)))
		if toggles.SharePurchase {
			m := memberID
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindSharePurchase,
				Title:           fmt.Sprintf("Buy %d shares", in.Shares),
				SubjectID:       memberID,
				SubjectMemberID: &m,
				Amount:          &amount,
				Payload:         payload,
				MakerUserID:     userID,
				SummarySuffix:   " — " + amount.StringFixed(2),
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			// Write a pending receipt + line so Today's receipts sees
			// the inline panel's queue immediately. The line's
			// approval_id is set from the just-queued pa; the line
			// flips to 'posted' when the approval executor fires.
			rec, rerr := h.writeInlineShareReceipt(r.Context(), tx, tid, memberID, userID,
				in.PaymentChannel, in.PaymentRef, amount, in.Narration,
				domain.ReceiptDraft, domain.LinePending, nil)
			if rerr != nil {
				return rerr
			}
			if len(rec.Lines) > 0 {
				if aerr := h.Receipts.AttachApprovalTx(r.Context(), tx, rec.Lines[0].ID, pa.ID); aerr != nil {
					return aerr
				}
			}
			receipt = rec
			return nil
		}
		res, err := h.ExecuteSharePurchaseTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		// In-tx outbox post:
		//   Debit  Cash / M-Pesa / Bank / Savings  (per payment channel)
		//   Credit Member Share Capital (equity)
		if perr := h.postSharePurchaseToGLTx(r.Context(), tx, tid, res, in.PaymentChannel); perr != nil {
			return perr
		}
		// Write a posted receipt + posted line so the inline path
		// is visible to Today's receipts alongside Collection Desk
		// receipts (the bug this PR closes).
		rec, rerr := h.writeInlineShareReceipt(r.Context(), tx, tid, memberID, userID,
			in.PaymentChannel, in.PaymentRef, res.Transaction.Amount, in.Narration,
			domain.ReceiptPosted, domain.LinePosted, &res.Transaction.ID)
		if rerr != nil {
			return rerr
		}
		receipt = rec
		result = res
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeBusinessErr(w, r, err)
		return
	}
	_ = receipt // surfaced via /v1/receipts; not in this endpoint's response shape
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
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

// postSharePurchaseToGLTx — thin wrapper over postingops.PostShareBuyTx.
// Body moved out so the approval executor (pending_approvals.go) can
// call the same JE logic when an approved purchase fires.
func (h *ShareHandler) postSharePurchaseToGLTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, result *SharePostResult, ch domain.PaymentChannel) error {
	if result == nil {
		return nil
	}
	if err := postingops.PostShareBuyTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
		Shares:  h.Shares,
	}, postingops.ShareBuyInput{
		TenantID:       tenantID,
		TxnID:          result.Transaction.ID,
		Amount:         result.Transaction.Amount,
		SharesDelta:    result.Transaction.SharesDelta,
		AccountNo:      result.Account.AccountNo,
		PaymentChannel: ch,
	}); err != nil {
		return err
	}
	// Mirror the in-memory journal_entry_id stamp so the HTTP response
	// reflects what postingops persisted to the row.
	if h.Posting != nil && !h.Posting.DryRun {
		jeID := result.Transaction.ID
		result.Transaction.JournalEntryID = &jeID
	}
	return nil
}

// writeInlineShareReceipt persists a single-line receipt for a
// share purchase fired from the Member 360 inline panel. Skipped
// silently when the receipt deps aren't wired (test setups,
// channels that can't ride the receipts table like 'internal' /
// 'payroll'). Returns the persisted receipt for caller-side
// AttachApprovalTx wiring when an approval was queued.
func (h *ShareHandler) writeInlineShareReceipt(
	ctx context.Context, tx pgx.Tx,
	tenantID, memberID, userID uuid.UUID,
	channel domain.PaymentChannel, paymentRef string,
	amount decimal.Decimal, narration string,
	headerStatus domain.ReceiptStatus, lineStatus domain.ReceiptLineStatus,
	postedTxnID *uuid.UUID,
) (*domain.Receipt, error) {
	if h.Receipts == nil || h.VirtualTills == nil {
		return &domain.Receipt{}, nil
	}
	rec, err := receiptops.WriteTx(ctx, tx, receiptops.Deps{
		Receipts:     h.Receipts,
		VirtualTills: h.VirtualTills,
	}, receiptops.WriteInput{
		TenantID:       tenantID,
		CounterpartyID: memberID,
		CashierUserID:  userID,
		Channel:        domain.ReceiptChannel(channel),
		ChannelRef:     paymentRef,
		ChannelAmount:  amount,
		Narration:      narration,
		Source:         "inline_share_purchase",
		HeaderStatus:   headerStatus,
		Lines: []receiptops.LineInput{{
			Kind:        domain.LineSharePurchase,
			Amount:      amount,
			Status:      lineStatus,
			PostedTxnID: postedTxnID,
		}},
	})
	if err != nil {
		// Channels like 'internal' / 'payroll' aren't representable on
		// the receipts table. Skip the receipt write silently rather
		// than fail the share purchase.
		if errors.Is(err, receiptops.ErrUnsupportedChannel) {
			return &domain.Receipt{}, nil
		}
		return nil, err
	}
	return rec, nil
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
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindShareTransfer,
				Title:           fmt.Sprintf("Transfer %d shares between members", in.Shares),
				SubjectID:       memberID,
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
	var member *store.CounterpartyView
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		member, err = h.Counterparties.GetByIDTx(r.Context(), tx, result.Account.CounterpartyID)
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
		var member *store.CounterpartyView
		_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			var err error
			member, err = h.Counterparties.GetByIDTx(r.Context(), tx, side.r.Account.CounterpartyID)
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
	SharesDelta           int    `json:"shares_delta"`            // signed
	Reason                string `json:"reason"`                  // adjustment_reason — required
	OffsettingAccountCode string `json:"offsetting_account_code"` // CoA code; must be equity or expense
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
	if in.OffsettingAccountCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("offsetting_account_code is required (must be an equity or expense CoA code, e.g. 3010 Retained Earnings)"))
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
		// Validate offsetting account BEFORE the share-side write so a
		// bad account fails fast without a wasted PostTxnTx.
		if _, verr := h.validateOffsettingAccountTx(r.Context(), tx, in.OffsettingAccountCode); verr != nil {
			return verr
		}
		policy, _, acct, err := h.loadContext(r.Context(), tx, memberID, true)
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
		// In-tx GL post — DR/CR polarity driven by the shares_delta
		// sign (increase = capital up, decrease = capital down).
		// Failure rolls back the share-side write + certificate.
		if perr := h.postShareAdjustmentToGLTx(r.Context(), tx, tid, txn, in.OffsettingAccountCode, reason); perr != nil {
			return perr
		}
		updated, err := h.Shares.GetAccountTx(r.Context(), tx, acct.ID)
		if err != nil {
			return err
		}
		cert, err := h.Shares.IssueCertificateTx(r.Context(), tx, acct.ID, acct.CounterpartyID, userID,
			updated.SharesHeld, policy.ParValue, policy.CertificatePrefix)
		if err != nil {
			return err
		}
		resp = postResp{Transaction: *txn, Account: *updated, Certificate: cert}
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
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
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindShareLien,
				Title:           fmt.Sprintf("Place lien on %d shares", in.Shares),
				SubjectID:       memberID,
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
			// Bonus issue is tenant-wide (no per-member subject).
			// Use tenant_id as the wf subject_id so the engine has
			// something non-nil to index on; the executor doesn't
			// read subject_id — it walks every active account.
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:    tid,
				Kind:        domain.ApprovalKindShareBonus,
				Title:       fmt.Sprintf("Bonus issue %s%% to all active accounts", in.PctOfHolding.String()),
				SubjectID:   tid,
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
		// Bonus issue is a stock dividend — same shape as the
		// buy_shares branch of the dividend run (R4) but issued
		// without first declaring cash. One batched JE for the
		// whole run; per-member share_transactions rows are
		// stamped with the same JE handle for audit. Failure
		// rolls back every PostTxnTx + certificate issued above.
		if perr := h.postBonusIssueToGLTx(r.Context(), tx, tid, out, payload.Reason); perr != nil {
			return perr
		}
		resp.IssuedToCount = out.IssuedToCount
		resp.TotalBonusShares = out.TotalBonusShares
		resp.PctApplied = out.PctApplied
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
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

// ─────────── GL helpers (bonus + adjust) ───────────

// postBonusIssueToGLTx queues one batched appropriation JE for a
// bonus issue. Bonus shares are a stock dividend — equity transfer
// from Retained Earnings into Member Share Capital, NO cash leg:
//
//   DR 3010 Retained Earnings    = total_bonus_shares × par
//   CR 3000 Member Share Capital = same
//
// One JE per bonus run regardless of the per-member share_transactions
// count — keeps journal_entries readable for runs with thousands of
// members. The generated jeID is stamped on every share_transactions
// row produced by the run so reconciliation by JE handle returns the
// full per-member breakdown.
//
// Suppressed when nothing to record (no holders → no bonus shares
// issued) or when the Posting client is disabled (dev). Failure
// (outbox INSERT error) rolls back every PostTxnTx + certificate
// issued by the executor.
func (h *ShareHandler) postBonusIssueToGLTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	result *ShareBonusResult, reason string,
) error {
	if result == nil {
		return nil
	}
	return postingops.PostShareBonusTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
		Shares:  h.Shares,
	}, postingops.ShareBonusInput{
		TenantID:         tenantID,
		ParValue:         result.ParValue,
		TotalBonusShares: result.TotalBonusShares,
		PctApplied:       result.PctApplied,
		Reason:           reason,
		TxnIDs:           result.TxnIDs,
	})
}

// validateOffsettingAccountTx confirms the operator-provided
// offsetting account exists, is active, and has a class compatible
// with a share-equity adjustment. Spec rule: must be 'equity' or
// 'expense'. Returns the account class so the caller can include it
// in the JE narration for audit clarity.
func (h *ShareHandler) validateOffsettingAccountTx(ctx context.Context, tx pgx.Tx, code string) (string, error) {
	if code == "" {
		return "", httpx.ErrBadRequest("offsetting_account_code is required for share adjustment")
	}
	var class string
	err := tx.QueryRow(ctx, `
		SELECT class FROM chart_of_accounts
		 WHERE tenant_id = current_tenant_id()
		   AND code = $1 AND is_active = true
	`, code).Scan(&class)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", httpx.E(http.StatusBadRequest, "unknown_account",
				"offsetting_account_code does not exist on this tenant: "+code)
		}
		return "", err
	}
	if class != "equity" && class != "expense" {
		return "", httpx.E(http.StatusBadRequest, "invalid_offsetting_account_class",
			fmt.Sprintf("offsetting account %s has class %q; must be 'equity' or 'expense'", code, class))
	}
	return class, nil
}

// postShareAdjustmentToGLTx queues the JE for an admin share-count
// correction. Polarity follows the shares_delta sign:
//
//   shares_delta > 0 (increase): DR <offsetting> / CR 3000  ← share capital up
//   shares_delta < 0 (decrease): DR 3000 / CR <offsetting>  ← share capital down
//
// The offsetting account is operator-chosen (validated upstream via
// validateOffsettingAccountTx — must be equity or expense). Amount =
// par × |shares_delta|. Source_ref = txn.ID so the share_transactions
// row's journal_entry_id == its own ID (single-txn-per-JE pattern,
// matches Purchase). Failure rolls back the share-side write.
func (h *ShareHandler) postShareAdjustmentToGLTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	txn *domain.ShareTransaction, offsettingCode, reason string,
) error {
	if h.Posting == nil || h.Posting.DryRun || txn == nil {
		return nil
	}
	amount := txn.Amount.Abs()
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	var debitCode, creditCode string
	if txn.SharesDelta > 0 {
		debitCode, creditCode = offsettingCode, "3000"
	} else {
		debitCode, creditCode = "3000", offsettingCode
	}
	narration := fmt.Sprintf("Share adjustment · %s · %d shares · %s",
		txn.TxnNo, txn.SharesDelta, reason)
	if err := h.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.shares.adjust",
		SourceRef:    txn.ID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: debitCode, Debit: amount, Narration: "Adjustment offset"},
			{AccountCode: creditCode, Credit: amount, Narration: "Member share capital adjustment"},
		},
	}); err != nil {
		return err
	}
	if err := h.Shares.UpdateJournalEntryIDTx(ctx, tx, txn.ID, txn.ID); err != nil {
		return err
	}
	// Mutate the in-memory txn so the HTTP response reflects the stamp.
	jeID := txn.ID
	txn.JournalEntryID = &jeID
	return nil
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
