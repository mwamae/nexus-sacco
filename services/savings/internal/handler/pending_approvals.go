// Maker-checker HTTP endpoints (Phase 7b).
//
//   GET   /v1/pending-approvals                       queue with filters
//   GET   /v1/pending-approvals/{id}                  detail (incl. payload)
//   POST  /v1/pending-approvals/{id}/approve          execute the queued action
//   POST  /v1/pending-approvals/{id}/decline          reject (no ledger move)
//   POST  /v1/pending-approvals/{id}/cancel           maker withdraws their own pending
//
//   GET   /v1/approval-settings                       per-kind toggles
//   PUT   /v1/approval-settings                       update toggles
//
// The Approve endpoint dispatches to the matching executor based on
// `kind`. Each cash-handling handler in the savings service registers
// an executor that re-runs the action using the stored payload.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

var _ = errors.New

// writeApprovalErr maps domain errors to friendly HTTP statuses.
func writeApprovalErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrApprovalSelfDenied):
		httpx.WriteErr(w, r, httpx.ErrForbidden("the maker cannot approve their own submission; ask a different user to approve"))
	case errors.Is(err, domain.ErrApprovalNotPending):
		httpx.WriteErr(w, r, httpx.ErrConflict("approval is not in pending state"))
	case errors.Is(err, domain.ErrApprovalKindUnknown):
		httpx.WriteErr(w, r, httpx.ErrBadRequest("approval kind is not recognised"))
	default:
		httpx.WriteErr(w, r, err)
	}
}

type PendingApprovalsHandler struct {
	DB        *db.Pool
	Approvals *store.ApprovalsStore

	// Each cash handler that supports queued approvals supplies its
	// executor here. The dispatcher uses kind → executor lookup.
	Deposit     *DepositHandler
	Share       *ShareHandler
	Loan        *LoanHandler
	LoanRepay   *LoanRepaymentHandler
	LoanCollect *LoanCollectionsHandler
	LoanReports *LoanReportsHandler

	// Receipts is optional. When wired, every approve/decline checks
	// whether the approval was spawned by a Collection Desk receipt
	// line (via store.GetLineByApprovalIDTx) and mirrors the terminal
	// status back onto the line. Without it the dispatcher still works
	// — it just leaves receipts uncoordinated, which is fine for
	// approvals coming from the legacy per-panel buttons (no receipt
	// linkage to begin with).
	Receipts *store.ReceiptStore

	// Shared secret the workflow service includes on its terminal-
	// status callback POSTs. When empty (dev fallback) we rely on the
	// User-Agent prefix check instead. See ResolveFromWorkflow.
	WorkflowInternalToken string

	Logger *slog.Logger
}

// ─────────── Helpers used by other handlers ───────────

// writePendingResponse is the common 202 response shape used by every
// handler that may queue an action.
func writePendingResponse(w http.ResponseWriter, _ *http.Request, p *domain.PendingApproval) {
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"status":  "pending_approval",
			"pending": p,
		},
	})
}

// ─────────── List / get ───────────

func (h *PendingApprovalsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.ApprovalListFilter{
		Status:        q.Get("status"),
		Kind:          q.Get("kind"),
		IncludeClosed: q.Get("include_closed") == "1",
		Limit:         limit,
		Offset:        offset,
	}
	if v := q.Get("counterparty_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.CounterpartyID = &id
		}
	}
	if v := q.Get("maker_user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.MakerUserID = &id
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.PendingApproval
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Approvals.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []domain.PendingApproval{}
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

func (h *PendingApprovalsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "approval_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Approvals.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Approve / decline / cancel ───────────

type decisionReq struct {
	Note string `json:"note"`
}

func (h *PendingApprovalsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "approval_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in decisionReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	checkerID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var out *domain.PendingApproval
	var executed any
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		pa, err := h.Approvals.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if pa.Status != domain.ApprovalStatusPending {
			return domain.ErrApprovalNotPending
		}
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if pa.MakerUserID == checkerID && !toggles.AllowSelf {
			return domain.ErrApprovalSelfDenied
		}

		// Execute.
		result, txnID, execErr := h.executePayloadTx(r.Context(), tx, pa)
		if execErr != nil {
			// Record the error on the row but DO NOT swallow it — return
			// it to the client so they can correct + resubmit.
			_ = h.Approvals.MarkExecErrorTx(r.Context(), tx, id, execErr.Error())
			return execErr
		}
		executed = result

		note := strNilIfEmpty(in.Note)
		updated, err := h.Approvals.MarkApprovedTx(r.Context(), tx, id, checkerID, note, txnID)
		if err != nil {
			return err
		}
		// Phase G hookup: if this approval was queued by the Collection
		// Desk, propagate the post back onto the receipt line. Lines
		// flip to 'posted' + the header rolls up to 'posted' once every
		// line is terminal. No-op for approvals queued via the legacy
		// per-panel buttons (no backing receipt_line row).
		if err := h.propagateToReceiptLine(r.Context(), tx, id, txnID, true); err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		writeApprovalErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"approval": out,
		"result":   executed,
	})
}

// propagateToReceiptLine mirrors an approval's terminal status back
// onto the receipt line that spawned it. posted=true ⇒ MarkLinePostedTx;
// posted=false ⇒ MarkLineDeclinedTx. Silently skips when the
// dispatcher has no Receipts dependency wired (older test rigs) or
// when the approval has no backing receipt line (per-panel-direct
// approvals).
func (h *PendingApprovalsHandler) propagateToReceiptLine(
	ctx context.Context, tx pgx.Tx, approvalID uuid.UUID, txnID *uuid.UUID, posted bool,
) error {
	if h.Receipts == nil {
		return nil
	}
	line, err := h.Receipts.GetLineByApprovalIDTx(ctx, tx, approvalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if posted {
		// txnID is the result of the per-kind Execute*Tx. For deposit /
		// loan-repay it's the ledger txn id; for share purchase it's
		// the share-transaction id. Either way, it's the canonical
		// "what got posted" reference the receipt line stores.
		var t uuid.UUID
		if txnID != nil {
			t = *txnID
		}
		return h.Receipts.MarkLinePostedTx(ctx, tx, line.ID, t)
	}
	return h.Receipts.MarkLineDeclinedTx(ctx, tx, line.ID)
}

func (h *PendingApprovalsHandler) Decline(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "approval_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in decisionReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	if in.Note == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("a note is required to decline"))
		return
	}
	checkerID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		pa, err := h.Approvals.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if pa.Status != domain.ApprovalStatusPending {
			return domain.ErrApprovalNotPending
		}
		note := strNilIfEmpty(in.Note)
		out, err = h.Approvals.MarkDeclinedTx(r.Context(), tx, id, checkerID, note)
		if err != nil {
			return err
		}
		// Mirror the decline onto the receipt line (if any). Causes
		// the receipt header to roll up to 'posted' once every other
		// line is also terminal — matches the plan note that a
		// declined line doesn't block the rest.
		return h.propagateToReceiptLine(r.Context(), tx, id, nil, false)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *PendingApprovalsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "approval_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in decisionReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		pa, err := h.Approvals.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if pa.Status != domain.ApprovalStatusPending {
			return domain.ErrApprovalNotPending
		}
		if pa.MakerUserID != userID {
			return httpx.ErrForbidden("only the maker can cancel a pending approval")
		}
		note := strNilIfEmpty(in.Note)
		out, err = h.Approvals.MarkCancelledTx(r.Context(), tx, id, userID, note)
		if err != nil {
			return err
		}
		// A maker-cancel collapses to "declined" on the receipt-line
		// side: the line is terminal but unpost-able; receipt header
		// rolls up the same way it would for an officer decline.
		return h.propagateToReceiptLine(r.Context(), tx, id, nil, false)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Settings ───────────

func (h *PendingApprovalsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.ApprovalToggles
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Approvals.GetTogglesTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

type updateTogglesReq struct {
	Deposit                *bool `json:"deposit,omitempty"`
	Withdrawal             *bool `json:"withdrawal,omitempty"`
	DepositTransfer        *bool `json:"deposit_transfer,omitempty"`
	SharePurchase          *bool `json:"share_purchase,omitempty"`
	ShareTransfer          *bool `json:"share_transfer,omitempty"`
	ShareBonus             *bool `json:"share_bonus,omitempty"`
	ShareLien              *bool `json:"share_lien,omitempty"`
	LoanDisbursement       *bool `json:"loan_disbursement,omitempty"`
	LoanRepayment          *bool `json:"loan_repayment,omitempty"`
	LoanSettle             *bool `json:"loan_settle,omitempty"`
	LoanReverse            *bool `json:"loan_reverse,omitempty"`
	LoanWriteoff           *bool `json:"loan_writeoff,omitempty"`
	LoanReschedule         *bool `json:"loan_reschedule,omitempty"`
	LoanMoratorium         *bool `json:"loan_moratorium,omitempty"`
	LoanSettlementDiscount *bool `json:"loan_settlement_discount,omitempty"`
	AllowSelf              *bool `json:"allow_self,omitempty"`
}

func (h *PendingApprovalsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in updateTogglesReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.ApprovalToggles
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Approvals.UpdateTogglesTx(r.Context(), tx, store.UpdateTogglesInput{
			Deposit:                in.Deposit,
			Withdrawal:             in.Withdrawal,
			DepositTransfer:        in.DepositTransfer,
			SharePurchase:          in.SharePurchase,
			ShareTransfer:          in.ShareTransfer,
			ShareBonus:             in.ShareBonus,
			ShareLien:              in.ShareLien,
			LoanDisbursement:       in.LoanDisbursement,
			LoanRepayment:          in.LoanRepayment,
			LoanSettle:             in.LoanSettle,
			LoanReverse:            in.LoanReverse,
			LoanWriteoff:           in.LoanWriteoff,
			LoanReschedule:         in.LoanReschedule,
			LoanMoratorium:         in.LoanMoratorium,
			LoanSettlementDiscount: in.LoanSettlementDiscount,
			AllowSelf:              in.AllowSelf,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Dispatcher ───────────

// executePayloadTx looks up the executor for the kind, unmarshals the
// stored payload, and runs it. Returns the result (for embedding in the
// approval response) and the resulting transaction id (recorded on the
// approval row).
func (h *PendingApprovalsHandler) executePayloadTx(
	ctx context.Context, tx pgx.Tx, pa *domain.PendingApproval,
) (any, *uuid.UUID, error) {
	makerID := pa.MakerUserID
	switch pa.Kind {
	case domain.ApprovalKindDeposit:
		if h.Deposit == nil {
			return nil, nil, errors.New("deposit handler is not wired")
		}
		p, err := store.UnmarshalPayload[DepositPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Deposit.ExecuteDepositTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindWithdrawal:
		if h.Deposit == nil {
			return nil, nil, errors.New("deposit handler is not wired")
		}
		p, err := store.UnmarshalPayload[WithdrawalPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Deposit.ExecuteWithdrawalTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindDepositTransfer:
		if h.Deposit == nil {
			return nil, nil, errors.New("deposit handler is not wired")
		}
		p, err := store.UnmarshalPayload[DepTransferPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Deposit.ExecuteDepTransferTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.From.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindSharePurchase:
		if h.Share == nil {
			return nil, nil, errors.New("share handler is not wired")
		}
		p, err := store.UnmarshalPayload[SharePurchasePayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Share.ExecuteSharePurchaseTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindShareTransfer:
		if h.Share == nil {
			return nil, nil, errors.New("share handler is not wired")
		}
		p, err := store.UnmarshalPayload[ShareTransferPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Share.ExecuteShareTransferTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.From.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindShareBonus:
		if h.Share == nil {
			return nil, nil, errors.New("share handler is not wired")
		}
		p, err := store.UnmarshalPayload[ShareBonusPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Share.ExecuteShareBonusTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		// Bonus issue affects many ledger rows — no single result_txn_id.
		return res, nil, nil

	case domain.ApprovalKindShareLien:
		if h.Share == nil {
			return nil, nil, errors.New("share handler is not wired")
		}
		p, err := store.UnmarshalPayload[ShareLienPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		lien, err := h.Share.ExecuteShareLienTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		// Lien is not a ledger txn either.
		return lien, nil, nil

	case domain.ApprovalKindLoanDisbursement:
		if h.Loan == nil {
			return nil, nil, errors.New("loan handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanDisbursementPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.Loan.ExecuteDisbursementTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Disbursement.ID
		return res, &txnID, nil

	case domain.ApprovalKindLoanRepayment:
		if h.LoanRepay == nil {
			return nil, nil, errors.New("loan repayment handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanRepaymentPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanRepay.ExecuteRepaymentTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindLoanSettle:
		if h.LoanRepay == nil {
			return nil, nil, errors.New("loan repayment handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanSettlePayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanRepay.ExecuteSettleTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Transaction.ID
		return res, &txnID, nil

	case domain.ApprovalKindLoanReverse:
		if h.LoanRepay == nil {
			return nil, nil, errors.New("loan repayment handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanReversePayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanRepay.ExecuteReverseTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		txnID := res.Reversal.ID
		return res, &txnID, nil

	case domain.ApprovalKindLoanWriteoff:
		if h.LoanReports == nil {
			return nil, nil, errors.New("loan reports handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanWriteoffPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanReports.ExecuteWriteoffTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		var txnID *uuid.UUID
		if res.Writeoff.WriteoffTxnID != nil {
			id := *res.Writeoff.WriteoffTxnID
			txnID = &id
		}
		return res, txnID, nil

	case domain.ApprovalKindLoanReschedule:
		if h.LoanCollect == nil {
			return nil, nil, errors.New("loan collections handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanReschedulePayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanCollect.ExecuteRescheduleTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		return res, nil, nil

	case domain.ApprovalKindLoanMoratorium:
		if h.LoanCollect == nil {
			return nil, nil, errors.New("loan collections handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanMoratoriumPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanCollect.ExecuteMoratoriumTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		return res, nil, nil

	case domain.ApprovalKindLoanSettlementDiscount:
		if h.LoanCollect == nil {
			return nil, nil, errors.New("loan collections handler is not wired")
		}
		p, err := store.UnmarshalPayload[LoanSettlementDiscountPayload](pa.Payload)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.LoanCollect.ExecuteSettlementDiscountTx(ctx, tx, p, makerID)
		if err != nil {
			return nil, nil, err
		}
		var txnID *uuid.UUID
		if res.Restructuring.DiscountWriteoffTxnID != nil {
			id := *res.Restructuring.DiscountWriteoffTxnID
			txnID = &id
		}
		return res, txnID, nil
	}
	return nil, nil, domain.ErrApprovalKindUnknown
}

// ─────────── POST /internal/v1/pending-approvals/{id}/resolve ───────────
//
// Webhook target for the workflow service's terminal-status
// callback. Triggered by the Unified Inbox consolidation
// (PR #3): once a workflow_instance reaches approved/rejected/
// cancelled, the engine POSTs here so this row mirrors the
// decision + (on approve) fires the existing executePayloadTx
// dispatcher.
//
// Idempotency: pending_approvals already in a terminal status
// short-circuit to 200 + no-op so a redelivered callback
// can't double-post a transaction. The same is true for
// already-mirrored rows whose result_txn_id is set.
//
// Auth: this endpoint lives under /internal/v1/... and is NOT
// JWT-protected. It checks the configured WORKFLOW_INTERNAL_TOKEN
// against the X-Internal-Token header. When the env var is empty
// (dev only) we fall back to a User-Agent prefix check so
// localhost workflow → savings still works without manual token
// wiring.

type workflowCallbackEnvelope struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	Event     string    `json:"event"` // "approved" | "rejected" | "cancelled"
	Instance  struct {
		ID        uuid.UUID `json:"id"`
		Context   map[string]any `json:"context"`
	} `json:"instance"`
}

func (h *PendingApprovalsHandler) ResolveFromWorkflow(w http.ResponseWriter, r *http.Request) {
	// Trust gate — match WorkflowInternalToken, else fall back to
	// the User-Agent prefix the workflow service always sends.
	expected := h.WorkflowInternalToken
	got := r.Header.Get("X-Internal-Token")
	if expected != "" {
		if got != expected {
			httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
			return
		}
	} else if !strings.HasPrefix(r.Header.Get("User-Agent"), "nexus-workflow") {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("workflow callback expected"))
		return
	}

	id, err := parseUUIDParam(r, "approval_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var env workflowCallbackEnvelope
	if err := httpx.DecodeJSON(r, &env); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if env.TenantID == uuid.Nil || env.Event == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id and event required"))
		return
	}

	var out *domain.PendingApproval
	var executed any
	err = h.DB.WithTenantTx(r.Context(), env.TenantID, func(tx pgx.Tx) error {
		pa, err := h.Approvals.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Idempotent: already terminal → no-op, return current state.
		// The workflow service may redeliver on transient transport
		// failures; the resolve must be safe to receive twice.
		if pa.Status != domain.ApprovalStatusPending {
			out = pa
			return nil
		}
		switch env.Event {
		case "approved":
			result, txnID, execErr := h.executePayloadTx(r.Context(), tx, pa)
			if execErr != nil {
				_ = h.Approvals.MarkExecErrorTx(r.Context(), tx, id, execErr.Error())
				return execErr
			}
			executed = result
			// Use the workflow's initiator (stored as approval maker)
			// as the checker attribution — the actual checker identity
			// is captured in the workflow instance's action audit.
			updated, err := h.Approvals.MarkApprovedTx(r.Context(), tx, id, pa.MakerUserID, nil, txnID)
			if err != nil {
				return err
			}
			out = updated
		case "rejected", "cancelled":
			updated, err := h.Approvals.MarkDeclinedTx(r.Context(), tx, id, pa.MakerUserID, nil)
			if err != nil {
				return err
			}
			out = updated
		default:
			return httpx.ErrBadRequest("unsupported event: " + env.Event)
		}
		return nil
	})
	if err != nil {
		writeApprovalErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"approval": out,
		"result":   executed,
	})
}
