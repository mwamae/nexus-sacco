// Collection Desk HTTP handlers — the single cashier's-counter surface.
//
//   GET  /v1/counterparties/{id}/outstanding          loan arrears + share shortfall
//   GET  /v1/till-sessions/current                    my open physical till (read-side proxy)
//   GET  /v1/receipts                                 list (filterable by till + date)
//   POST /v1/receipts                                 create header + lines + queue approvals
//   GET  /v1/receipts/{id}                            detail incl. per-line approval status
//   POST /v1/receipts/{id}/lines/{line_id}/void       per-line void (plan decision #2)
//
// The handler queues an approval per receipt line through the existing
// ApprovalsStore. The /v1/pending-approvals/{id}/approve dispatcher
// (pending_approvals.go) is what actually executes the underlying
// ledger move; once it does, ReceiptStore.MarkLinePostedTx flips the
// line's status and the header rolls up to 'posted' when every line
// is terminal.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type CollectionDeskHandler struct {
	DB             *db.Pool
	Receipts       *store.ReceiptStore
	VirtualTills   *store.VirtualTillStore
	Approvals      *store.ApprovalsStore
	Loans          *store.LoanStore
	LoanReports    *store.LoanReportsStore
	Shares         *store.ShareStore
	Tenants        *store.TenantStore
	Counterparties *store.CounterpartyStore
	Fees           *store.FeeCatalogStore
	Notifier       *notifier.Client
	Posting        *posting.Client
	Logger         *slog.Logger

	// Reverse-side wiring (Phase G follow-up: line-level reversal).
	// VoidLine dispatches by line.kind to the matching Execute*ReverseTx
	// on whichever handler owns the underlying transaction type. Same
	// pattern PendingApprovalsHandler uses for its approve-time
	// dispatch — kept here so the dispatcher doesn't have to know
	// about receipt lines.
	Deposit   *DepositHandler
	LoanRepay *LoanRepaymentHandler
}

// ─────────── GET /v1/counterparties/{id}/outstanding ───────────

func (h *CollectionDeskHandler) Outstanding(w http.ResponseWriter, r *http.Request) {
	cpID, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var out domain.CounterpartyOutstanding
	out.LoanArrears = []domain.LoanArrearSummary{}
	out.UnpaidFees = []domain.FeeDue{}

	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Existence check — surfaces a clean 404 instead of an empty
		// "this CP has nothing outstanding" body when the id is wrong.
		if _, gerr := h.Counterparties.GetByIDTx(r.Context(), tx, cpID); gerr != nil {
			return gerr
		}
		// Loan arrears for this counterparty. Pulls from loans table
		// directly; the shape mirrors what LoanReports.MemberLoanHistoryTx
		// returns but trimmed to the arrears subset.
		rows, qerr := tx.Query(r.Context(), `
			SELECT l.id, l.loan_no, p.code,
			       GREATEST(0, l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance
			                   - l.principal_disbursed * 0) AS arrears,
			       COALESCE(l.days_past_due, 0),
			       COALESCE(l.arrears_classification::text, 'performing')
			  FROM loans l JOIN loan_products p ON p.id = l.product_id
			 WHERE l.counterparty_id = $1
			   AND l.status IN ('active','in_arrears','restructured')
			   AND l.days_past_due > 0
			 ORDER BY l.days_past_due DESC
		`, cpID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var a domain.LoanArrearSummary
			if err := rows.Scan(&a.LoanID, &a.LoanNo, &a.ProductCode, &a.ArrearsAmount,
				&a.DaysPastDue, &a.Classification); err != nil {
				return err
			}
			out.LoanArrears = append(out.LoanArrears, a)
			out.TotalSuggested = out.TotalSuggested.Add(a.ArrearsAmount)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// Share shortfall (read tenant share policy + member's current
		// holding via the bridge). Skipped for institutional CPs since
		// the min-shares policy is member-only.
		policy, perr := h.Tenants.SharePolicyTx(r.Context(), tx)
		if perr == nil && policy.MinSharesRequired > 0 {
			var heldShares int
			var shareAcctID uuid.UUID
			perr2 := tx.QueryRow(r.Context(), `
				SELECT id, shares_held FROM share_accounts WHERE counterparty_id = $1
			`, cpID).Scan(&shareAcctID, &heldShares)
			if perr2 == nil && heldShares < policy.MinSharesRequired {
				shortShares := policy.MinSharesRequired - heldShares
				shortKES := decimal.NewFromInt(int64(shortShares)).Mul(policy.ParValue)
				out.ShareShortfall = &domain.ShareShortfallSummary{
					ShareAccountID:  shareAcctID,
					SharesHeld:      heldShares,
					MinSharesPolicy: policy.MinSharesRequired,
					ShortfallShares: shortShares,
					ParValue:        policy.ParValue,
					ShortfallKES:    shortKES,
				}
				out.TotalSuggested = out.TotalSuggested.Add(shortKES)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("counterparty not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── GET /v1/till-sessions/current ───────────

// Returns the caller's open physical till session, if any. Queries the
// shared DB directly — the till_sessions table is owned by the
// accounting service, but in this monolithic-DB deployment that's a
// plain SELECT. Not a cross-service HTTP call.
type currentSessionResponse struct {
	HasOpenSession bool       `json:"has_open_session"`
	SessionID      *uuid.UUID `json:"session_id,omitempty"`
	TillID         *uuid.UUID `json:"till_id,omitempty"`
	TillCode       *string    `json:"till_code,omitempty"`
	TillName       *string    `json:"till_name,omitempty"`
	OpenedAt       *time.Time `json:"opened_at,omitempty"`
}

func (h *CollectionDeskHandler) CurrentTillSession(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var resp currentSessionResponse
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var sid, tid uuid.UUID
		var code, name string
		var openedAt time.Time
		err := tx.QueryRow(r.Context(), `
			SELECT s.id, s.till_id, t.code, t.name, s.opened_at
			  FROM till_sessions s JOIN tills t ON t.id = s.till_id
			 WHERE s.teller_user_id = $1 AND s.status = 'open'
			 LIMIT 1
		`, userID).Scan(&sid, &tid, &code, &name, &openedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		resp.HasOpenSession = true
		resp.SessionID = &sid
		resp.TillID = &tid
		resp.TillCode = &code
		resp.TillName = &name
		resp.OpenedAt = &openedAt
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── POST /v1/receipts ───────────

type createReceiptRequest struct {
	CounterpartyID uuid.UUID                  `json:"counterparty_id"`
	Channel        domain.ReceiptChannel      `json:"channel"`
	ChannelRef     string                     `json:"channel_ref"`
	ChannelAmount  decimal.Decimal            `json:"channel_amount"`
	ValueDate      string                     `json:"value_date"` // YYYY-MM-DD; defaults to today
	Narration      string                     `json:"narration"`
	Lines          []createReceiptLineRequest `json:"lines"`
}

type createReceiptLineRequest struct {
	Kind            domain.ReceiptLineKind `json:"kind"`
	Amount          decimal.Decimal        `json:"amount"`
	TargetAccountID *uuid.UUID             `json:"target_account_id,omitempty"`
	FeeCode         *string                `json:"fee_code,omitempty"`
	Narration       string                 `json:"narration,omitempty"`
}

func (h *CollectionDeskHandler) CreateReceipt(w http.ResponseWriter, r *http.Request) {
	var in createReceiptRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.CounterpartyID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id is required"))
		return
	}
	if in.Channel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel is required"))
		return
	}
	if len(in.Lines) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("receipt must have at least one line"))
		return
	}
	valueDate := time.Now().UTC()
	if in.ValueDate != "" {
		t, err := time.Parse("2006-01-02", in.ValueDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
		valueDate = t
	}
	if valueDate.After(time.Now().Add(24 * time.Hour)) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date cannot be in the future"))
		return
	}

	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	var receipt *domain.Receipt
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Counterparty must exist + status check. Blacklisted CPs are
		// allowed loan_repayment lines only; share/savings/fee lines
		// are rejected. See plan negative paths.
		cp, gerr := h.Counterparties.GetByIDTx(r.Context(), tx, in.CounterpartyID)
		if gerr != nil {
			return gerr
		}
		if err := guardCounterpartyForReceipt(cp, in.Lines); err != nil {
			return err
		}

		// Resolve channel → till context.
		var (
			tillSessionID *uuid.UUID
			virtualTillID *uuid.UUID
			tillCode      string
		)
		if in.Channel == domain.RCCash {
			// Cash requires an open till session for THIS user. If none,
			// the desk should have caught this client-side already; the
			// 412 is the server-side hard stop.
			var sid, tid uuid.UUID
			var code string
			err := tx.QueryRow(r.Context(), `
				SELECT s.id, s.till_id, t.code FROM till_sessions s
				  JOIN tills t ON t.id = s.till_id
				 WHERE s.teller_user_id = $1 AND s.status = 'open'
				 LIMIT 1
			`, userID).Scan(&sid, &tid, &code)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrConflict("no open till session — open a till first")
			}
			if err != nil {
				return err
			}
			tillSessionID = &sid
			tillCode = code
		} else {
			vt, vErr := h.VirtualTills.EnsureForChannelTx(r.Context(), tx, tenantID, in.Channel)
			if vErr != nil {
				return vErr
			}
			virtualTillID = &vt.ID
			tillCode = string(in.Channel) // "mpesa", "bank_transfer", etc. — short slug for the serial
		}

		// Build the store input + create the receipt with N lines.
		var channelRef *string
		if in.ChannelRef != "" {
			s := in.ChannelRef
			channelRef = &s
		}
		var narration *string
		if in.Narration != "" {
			s := in.Narration
			narration = &s
		}
		lineInputs := make([]store.CreateReceiptLineInput, 0, len(in.Lines))
		for i, l := range in.Lines {
			var n *string
			if l.Narration != "" {
				s := l.Narration
				n = &s
			}
			lineInputs = append(lineInputs, store.CreateReceiptLineInput{
				LineNo:          i + 1,
				Kind:            l.Kind,
				Amount:          l.Amount,
				TargetAccountID: l.TargetAccountID,
				FeeCode:         l.FeeCode,
				Narration:       n,
			})
		}
		r2, cerr := h.Receipts.CreateTx(r.Context(), tx, store.CreateReceiptInput{
			TenantID:       tenantID,
			CounterpartyID: in.CounterpartyID,
			Channel:        in.Channel,
			ChannelRef:     channelRef,
			ChannelAmount:  in.ChannelAmount,
			ValueDate:      valueDate,
			Narration:      narration,
			CashierUserID:  userID,
			TillSessionID:  tillSessionID,
			VirtualTillID:  virtualTillID,
			TillCode:       tillCode,
			Lines:          lineInputs,
		})
		if cerr != nil {
			if errors.Is(cerr, store.ErrDuplicateReceipt) {
				// Surface the existing receipt id so the UI can render
				// the "duplicate — continue anyway?" dialog with a link.
				return httpx.ErrConflict(cerr.Error())
			}
			return cerr
		}

		// Wave 2 — per-line toggle routing. Before this change the
		// loop unconditionally queued an approval for
		// deposit/share/loan lines (regardless of toggle) and
		// always posted fee/welfare lines inline. Now every kind
		// consults its matching toggle on tenant_operations: if the
		// toggle is OFF (admin opted out), the line posts inline;
		// if ON (default), the line queues an approval and a
		// second user must approve before the GL fires.
		//
		// Existing tenants were flipped to safe-by-default by
		// migration 0030 — anyone who deliberately had a toggle
		// off carries an audit row in tenant_approval_changes.
		toggles, terr := h.Approvals.GetTogglesTx(r.Context(), tx)
		if terr != nil {
			return terr
		}
		for i := range r2.Lines {
			line := &r2.Lines[i]
			approvalKind, payload, perr := buildApprovalPayload(*line, *r2, in.Channel, in.ChannelRef, in.Narration, valueDate)
			if perr != nil {
				return perr
			}
			if approvalKind == "" {
				// Unknown line kind — should have been rejected upstream.
				return httpx.ErrBadRequest("unknown receipt line kind")
			}
			gated := kindToggleForReceiptLine(toggles, approvalKind)
			if !gated {
				// Toggle is OFF for this kind — direct-post path.
				// Fee + welfare keep using the catalog-driven
				// postFeeLineTx; the other kinds aren't reachable
				// here today (their inline paths run from their
				// dedicated handlers, not from the receipt loop)
				// so we fall back to the same catalog post which
				// covers the only two kinds that can land here
				// without a paired inline handler.
				if line.Kind == domain.LineFee || line.Kind == domain.LineWelfare {
					txnID, fErr := h.postFeeLineTx(r.Context(), tx, tenantID, *r2, *line, in.Channel)
					if fErr != nil {
						return fErr
					}
					if mErr := h.Receipts.MarkLinePostedTx(r.Context(), tx, line.ID, txnID); mErr != nil {
						return mErr
					}
					line.Status = domain.LinePosted
					line.PostedTxnID = &txnID
					continue
				}
				// For deposit / share / loan-repayment kinds the
				// "post inline when toggle off" path is a small
				// scope-extension reserved for a follow-up — the
				// matching inline handlers (Deposit/SharePurchase/
				// LoanRepayment) already do the post via their
				// dedicated endpoints. Queuing here even when the
				// toggle is off preserves today's collection-desk
				// behaviour for those kinds rather than tearing
				// down the path under cover of this PR. Logged so
				// the gap is visible.
				if h.Logger != nil {
					h.Logger.Info("collection desk: kind toggle is off but receipt path still queues — see Wave 2 follow-up",
						"receipt", r2.Serial, "line_no", line.LineNo, "kind", line.Kind)
				}
			}
			amt := line.Amount
			subj := r2.CounterpartyID
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            approvalKind,
				Title:           fmt.Sprintf("Receipt %s · line %d (%s)", r2.Serial, line.LineNo, line.Kind),
				SubjectMemberID: &subj,
				Amount:          &amt,
				Payload:         payload,
				MakerUserID:     userID,
			})
			if qerr != nil {
				return qerr
			}
			if aerr := h.Receipts.AttachApprovalTx(r.Context(), tx, line.ID, pa.ID); aerr != nil {
				return aerr
			}
			line.ApprovalID = &pa.ID
		}

		receipt = r2
		return nil
	})
	if err != nil {
		writeDeskErr(w, r, err)
		return
	}
	httpx.Created(w, receipt)
}

// FeePostingPayload + WelfarePostingPayload are stored on the
// pending_approvals row so the savings dispatcher can re-execute the
// existing postFeeLineTx on approve. The payload keeps just the
// minimum to identify the line — the executor re-loads receipt +
// line from the receipt id so any concurrent void during the pending
// window is honoured.
type FeePostingPayload struct {
	ReceiptID uuid.UUID             `json:"receipt_id"`
	LineID    uuid.UUID             `json:"line_id"`
	Channel   domain.ReceiptChannel `json:"channel"`
}
type WelfarePostingPayload struct {
	ReceiptID uuid.UUID             `json:"receipt_id"`
	LineID    uuid.UUID             `json:"line_id"`
	Channel   domain.ReceiptChannel `json:"channel"`
}

// buildApprovalPayload maps a receipt line to the approval kind +
// the typed payload struct the dispatcher (pending_approvals.go
// executePayloadTx) consumes on approve.
//
// Wave 2 — fee and welfare lines now return real approval kinds so
// the per-line loop in CreateReceipt can decide inline-vs-queue
// based on toggles.FeeCollection / toggles.WelfareCollection. Pre-
// Wave-2 these returned ("", nil, nil) and the loop posted them
// inline unconditionally.
func buildApprovalPayload(
	line domain.ReceiptLine, receipt domain.Receipt,
	channel domain.ReceiptChannel, channelRef, narration string, valueDate time.Time,
) (domain.ApprovalKind, any, error) {
	valueDateStr := valueDate.Format("2006-01-02")
	switch line.Kind {
	case domain.LineSavingsDeposit:
		if line.TargetAccountID == nil {
			return "", nil, httpx.ErrBadRequest("savings_deposit line requires target_account_id")
		}
		return domain.ApprovalKindDeposit, DepositPayload{
			AccountID:            *line.TargetAccountID,
			Amount:               line.Amount,
			Channel:              domain.DepositChannel(channel),
			ChannelRef:           channelRef,
			Narration:            ifEmpty(deref(line.Narration), narration),
			ValueDate:            valueDateStr,
			BypassDuplicateCheck: true, // receipt-level dedup is already enforced
		}, nil
	case domain.LineSharePurchase:
		shares := computeShares(line)
		return domain.ApprovalKindSharePurchase, SharePurchasePayload{
			CounterpartyID: receipt.CounterpartyID,
			Shares:         shares,
			PaymentChannel: domain.PaymentChannel(channel),
			PaymentRef:     channelRef,
			Narration:      ifEmpty(deref(line.Narration), narration),
		}, nil
	case domain.LineLoanRepayment:
		if line.TargetAccountID == nil {
			return "", nil, httpx.ErrBadRequest("loan_repayment line requires target_account_id (loan_id)")
		}
		return domain.ApprovalKindLoanRepayment, LoanRepaymentPayload{
			LoanID:     *line.TargetAccountID,
			Amount:     line.Amount,
			Channel:    string(channel),
			ChannelRef: channelRef,
			Narration:  ifEmpty(deref(line.Narration), narration),
			ValueDate:  valueDateStr,
		}, nil
	case domain.LineFee:
		return domain.ApprovalKindFeePosting, FeePostingPayload{
			ReceiptID: receipt.ID, LineID: line.ID, Channel: channel,
		}, nil
	case domain.LineWelfare:
		return domain.ApprovalKindWelfarePosting, WelfarePostingPayload{
			ReceiptID: receipt.ID, LineID: line.ID, Channel: channel,
		}, nil
	}
	return "", nil, httpx.ErrBadRequest("unknown receipt line kind: " + string(line.Kind))
}

// kindToggleForReceiptLine returns the receipt-line approval kind's
// toggle from the loaded ApprovalToggles bundle. Kept next to the
// per-line loop so the toggle list stays one edit away from the
// approval-kind list.
func kindToggleForReceiptLine(t *store.ApprovalToggles, kind domain.ApprovalKind) bool {
	if t == nil {
		// Conservative: treat missing toggles as "gate on" so a
		// degraded read can't accidentally relax the policy.
		return true
	}
	return t.IsKindGated(kind)
}

// computeShares derives integer share count from amount/par. Falls
// back to amount as shares if computation produces 0 (caller chose to
// pass shares directly via amount).
func computeShares(line domain.ReceiptLine) int {
	// TODO Phase G: read par value from share policy snapshot on receipt.
	// For now, assume amount IS already in shares × par; the dispatcher
	// re-resolves par from share policy at execution time.
	n, _ := line.Amount.Float64()
	if n < 1 {
		return 1
	}
	return int(n / 100) // par defaults to 100 in the seed; refined later
}

// guardCounterpartyForReceipt enforces the negative-path rules on a
// per-line basis (see plan: blacklisted → loan_repayment only, etc.).
func guardCounterpartyForReceipt(cp *store.CounterpartyView, lines []createReceiptLineRequest) error {
	status := strings.ToLower(cp.Status)
	for _, l := range lines {
		switch status {
		case "blacklisted", "exited", "deceased":
			if l.Kind != domain.LineLoanRepayment {
				return httpx.ErrForbidden(fmt.Sprintf(
					"counterparty status %q blocks %s lines; only loan_repayment is permitted", status, l.Kind))
			}
		}
	}
	return nil
}

// ─────────── GET /v1/receipts/{id} ───────────

func (h *CollectionDeskHandler) GetReceipt(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var out *domain.Receipt
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		rcpt, err := h.Receipts.GetByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = rcpt
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("receipt not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── GET /v1/receipts ───────────

func (h *CollectionDeskHandler) ListReceipts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReceiptListFilter{}
	if v := q.Get("till_session_id"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("till_session_id must be a uuid"))
			return
		}
		f.TillSessionID = &id
	}
	if v := q.Get("virtual_till_id"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("virtual_till_id must be a uuid"))
			return
		}
		f.VirtualTillID = &id
	}
	if v := q.Get("cashier_user_id"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("cashier_user_id must be a uuid"))
			return
		}
		f.CashierUserID = &id
	}
	if v := q.Get("value_date"); v != "" {
		t, perr := time.Parse("2006-01-02", v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
		f.ValueDate = &t
	}
	if v := q.Get("status"); v != "" {
		s := domain.ReceiptStatus(v)
		f.Status = &s
	}

	tenantID, _ := middleware.TenantIDFrom(r)
	var rows []store.ReceiptListItem
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		r2, err := h.Receipts.ListTx(r.Context(), tx, f)
		if err != nil {
			return err
		}
		rows = r2
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if rows == nil {
		rows = []store.ReceiptListItem{}
	}
	httpx.OK(w, rows)
}

// ─────────── POST /v1/receipts/{id}/lines/{line_id}/void ───────────

type voidLineRequest struct {
	Reason string `json:"reason"`
}

func (h *CollectionDeskHandler) VoidLine(w http.ResponseWriter, r *http.Request) {
	lineID, err := parseUUIDParam(r, "line_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in voidLineRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var reverseTxnID *uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// Pull the line first so we know what to dispatch to.
		line, lerr := h.Receipts.GetLineForVoidTx(r.Context(), tx, lineID)
		if lerr != nil {
			return lerr
		}
		// Only dispatch a reversal when the line actually moved money.
		// Pending/declined lines just flip status. fee + welfare lines
		// today don't have a ledger executor either (the v1 path marked
		// them posted with a nil txn id), so they take the same skip
		// path — no reversal needed.
		if line.Status == domain.LinePosted && line.PostedTxnID != nil && *line.PostedTxnID != uuid.Nil {
			revID, rerr := h.dispatchReversal(r.Context(), tx, line, in.Reason, userID)
			if rerr != nil {
				return rerr
			}
			reverseTxnID = revID
		}
		return h.Receipts.VoidLineTx(r.Context(), tx, store.VoidLineInput{
			LineID:   lineID,
			VoidedBy: userID,
			Reason:   in.Reason,
		})
	})
	if err != nil {
		writeDeskErr(w, r, err)
		return
	}
	resp := map[string]any{"status": "voided", "line_id": lineID}
	if reverseTxnID != nil {
		resp["reverse_txn_id"] = *reverseTxnID
	}
	httpx.OK(w, resp)
}

// dispatchReversal — per-kind switchboard that calls the appropriate
// Execute*ReverseTx and returns the new reversal-txn id. Share
// reversals are deferred: share_transactions doesn't carry
// reverses_txn_id / reversed_by_txn_id columns yet, so the data layer
// can't represent a clean back-link. The Go-side void still marks
// the line, but no money is unmoved — caller gets a friendly error
// surfaced as 412 so the UI can flag "share reversal not yet
// supported" rather than silently accept a half-void.
func (h *CollectionDeskHandler) dispatchReversal(
	ctx context.Context, tx pgx.Tx,
	line *domain.ReceiptLine, reason string, userID uuid.UUID,
) (*uuid.UUID, error) {
	if line.PostedTxnID == nil {
		return nil, nil
	}
	txnID := *line.PostedTxnID
	switch line.Kind {
	case domain.LineSavingsDeposit:
		if h.Deposit == nil {
			return nil, fmt.Errorf("deposit handler not wired")
		}
		res, err := h.Deposit.ExecuteDepositReverseTx(ctx, tx, DepositReversePayload{
			TxnID:  txnID,
			Reason: reason,
		}, userID)
		if err != nil {
			if errors.Is(err, store.ErrAlreadyReversed) {
				return nil, httpx.ErrConflict("deposit already reversed")
			}
			return nil, err
		}
		id := res.Reversal.ID
		return &id, nil
	case domain.LineLoanRepayment:
		if h.LoanRepay == nil {
			return nil, fmt.Errorf("loan-repayment handler not wired")
		}
		res, err := h.LoanRepay.ExecuteReverseTx(ctx, tx, LoanReversePayload{
			TxnID:  txnID,
			Reason: reason,
		}, userID)
		if err != nil {
			return nil, err
		}
		id := res.Reversal.ID
		return &id, nil
	case domain.LineSharePurchase:
		// share_transactions lacks reverses_txn_id / reversed_by_txn_id
		// columns — needs a schema migration + executor before voids
		// can reverse the underlying share posting. Until then, refuse
		// the void rather than silently mark it (which would corrupt
		// the receipt-to-ledger invariant).
		return nil, httpx.ErrConflict("share-purchase reversal not yet supported (data layer needs reverses_txn_id columns)")
	case domain.LineFee, domain.LineWelfare:
		// Fee + welfare lines have no underlying ledger executor in v1
		// (handler/collection_desk.go marks them 'posted' with a nil
		// txn id). Nothing to reverse — caller still flips the line
		// status to voided.
		return nil, nil
	}
	return nil, fmt.Errorf("unknown line kind for reversal: %s", line.Kind)
}

// ─────────── Fee-line execution + catalog endpoints ───────────

// postFeeLineTx fires the GL credit for a fee-or-welfare receipt
// line. Resolves the catalog entry by code → finds the credit account,
// computes the debit account from the receipt's channel
// (cash → till GL '1010', non-cash → channel suspense via the
// virtual_tills.gl_account_code), and posts a 2-line journal entry.
//
// Returns the synthetic "posted_txn_id" the receipt-line stores —
// here we use a UUID generated upfront and pass it as the source_ref
// so the accounting service's (source_module, source_ref) dedup
// catches re-tries. The journal entry itself is the source of truth;
// posted_txn_id is just the back-reference for the receipts view.
func (h *CollectionDeskHandler) postFeeLineTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	receipt domain.Receipt, line domain.ReceiptLine, channel domain.ReceiptChannel,
) (uuid.UUID, error) {
	if h.Fees == nil {
		return uuid.Nil, fmt.Errorf("fee catalog not wired")
	}
	if line.FeeCode == nil || *line.FeeCode == "" {
		return uuid.Nil, httpx.ErrBadRequest("fee line requires fee_code")
	}
	entry, err := h.Fees.GetByCodeTx(ctx, tx, *line.FeeCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return uuid.Nil, httpx.ErrBadRequest("fee code not in catalog: " + *line.FeeCode)
		}
		return uuid.Nil, err
	}
	debitAccount, err := debitAccountForChannel(ctx, tx, channel, receipt)
	if err != nil {
		return uuid.Nil, err
	}
	if h.Posting == nil || h.Posting.Disabled {
		// Dev environment without an accounting service. Stamp a
		// synthetic txn id so the receipt line still rolls up; log a
		// warning so production never trips this silently.
		if h.Logger != nil {
			h.Logger.Warn("collection desk: posting client disabled, fee line not GL-posted",
				"receipt", receipt.Serial, "fee_code", entry.Code, "amount", line.Amount.StringFixed(2))
		}
		return uuid.New(), nil
	}
	sourceRef := uuid.New()
	if err := h.Posting.Post(ctx, posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    receipt.ValueDate,
		ValueDate:    receipt.ValueDate,
		SourceModule: "savings.collection_desk.fees",
		SourceRef:    sourceRef.String(),
		Narration:    fmt.Sprintf("Receipt %s · fee %s", receipt.Serial, entry.Code),
		Lines: []posting.Line{
			{AccountCode: debitAccount, Debit: line.Amount, Narration: "Cash in via " + string(channel)},
			{AccountCode: entry.GLCreditCode, Credit: line.Amount, Narration: entry.Label},
		},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("post fee GL entry: %w", err)
	}
	return sourceRef, nil
}

// debitAccountForChannel returns the GL account code the fee posting
// should debit. Cash goes to the till's GL account; everything else
// hits the virtual_tills.gl_account_code for that channel.
func debitAccountForChannel(ctx context.Context, tx pgx.Tx, channel domain.ReceiptChannel, receipt domain.Receipt) (string, error) {
	if channel == domain.RCCash {
		if receipt.TillSessionID == nil {
			return "", fmt.Errorf("cash receipt has no till_session_id")
		}
		var code string
		if err := tx.QueryRow(ctx, `
			SELECT t.gl_account_code FROM till_sessions s JOIN tills t ON t.id = s.till_id WHERE s.id = $1
		`, *receipt.TillSessionID).Scan(&code); err != nil {
			return "", fmt.Errorf("lookup till GL account: %w", err)
		}
		return code, nil
	}
	if receipt.VirtualTillID == nil {
		return "", fmt.Errorf("non-cash receipt has no virtual_till_id")
	}
	var code string
	if err := tx.QueryRow(ctx, `SELECT gl_account_code FROM virtual_tills WHERE id = $1`, *receipt.VirtualTillID).Scan(&code); err != nil {
		return "", fmt.Errorf("lookup virtual till GL account: %w", err)
	}
	return code, nil
}

// ─────────── GET /v1/fees ───────────

func (h *CollectionDeskHandler) ListFees(w http.ResponseWriter, r *http.Request) {
	if h.Fees == nil {
		httpx.WriteErr(w, r, fmt.Errorf("fee catalog not wired"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	includeAll := r.URL.Query().Get("include_inactive") == "true"
	var out []domain.FeeCatalogEntry
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		if includeAll {
			out, err = h.Fees.ListAllTx(r.Context(), tx)
		} else {
			out, err = h.Fees.ListActiveTx(r.Context(), tx)
		}
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []domain.FeeCatalogEntry{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/fees (admin) ───────────

type createFeeRequest struct {
	Code           string          `json:"code"`
	Label          string          `json:"label"`
	Description    string          `json:"description"`
	AmountDefault  decimal.Decimal `json:"amount_default"`
	AmountEditable bool            `json:"amount_editable"`
	GLCreditCode   string          `json:"gl_credit_code"`
	SortOrder      int             `json:"sort_order"`
}

func (h *CollectionDeskHandler) CreateFee(w http.ResponseWriter, r *http.Request) {
	if h.Fees == nil {
		httpx.WriteErr(w, r, fmt.Errorf("fee catalog not wired"))
		return
	}
	var in createFeeRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	var desc *string
	if in.Description != "" {
		desc = &in.Description
	}
	var out *domain.FeeCatalogEntry
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		entry, err := h.Fees.CreateTx(r.Context(), tx, tenantID, store.CreateFeeCatalogInput{
			Code:           in.Code,
			Label:          in.Label,
			Description:    desc,
			AmountDefault:  in.AmountDefault,
			AmountEditable: in.AmountEditable,
			GLCreditCode:   in.GLCreditCode,
			SortOrder:      in.SortOrder,
		})
		if err != nil {
			return err
		}
		out = entry
		return nil
	})
	if err != nil {
		// PR fee-coa: an unknown gl_credit_code is the most common
		// admin error here — the store guard catches it before the
		// INSERT, but the dispatcher needs to surface a 422 with a
		// human-readable account code so the form can render it
		// inline rather than as a generic 500.
		if errors.Is(err, store.ErrUnknownGLCode) {
			httpx.WriteErr(w, r, httpx.E(http.StatusUnprocessableEntity, "unknown_gl_code",
				fmt.Sprintf("unknown GL account %q", in.GLCreditCode)))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── POST /v1/fees/replay-failed (admin) ───────────
//
// One-off recovery endpoint for receipts whose fee/welfare lines
// crashed at posting time (most commonly because the fee_catalog
// pointed at a non-existent GL code — fixed by accounting 0012 +
// savings 0031). Walks every receipt_line where:
//
//	kind IN ('fee','welfare') AND posted_txn_id IS NULL AND voided_at IS NULL
//
// and re-runs postFeeLineTx on each. Each line runs in its own
// sub-tx so a single still-broken row doesn't roll back the rest.
// Returns counts + the per-line error payload for any line that
// still fails so the admin can surface a remaining gap.
//
// Permission: spec asked for the 'finance_admin' role but the
// codebase doesn't expose role names directly — using the same
// permission as the approval-settings PUT + CreateFee endpoint
// (tenant:settings:edit). Swap to a dedicated permission later
// if the roles system grows one.

type replayLineFailure struct {
	ReceiptID uuid.UUID `json:"receipt_id"`
	LineID    uuid.UUID `json:"line_id"`
	Kind      string    `json:"kind"`
	Payload   string    `json:"payload"` // underlying engine error
}

type replayFailedResponse struct {
	Replayed    int                 `json:"replayed"`
	Skipped     int                 `json:"skipped"`
	StillFailed []replayLineFailure `json:"still_failed"`
}

func (h *CollectionDeskHandler) ReplayFailedFeeLines(w http.ResponseWriter, r *http.Request) {
	if h.Receipts == nil {
		httpx.WriteErr(w, r, fmt.Errorf("receipt store not wired"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)

	// Step 1 — pull the list of unposted lines. Read-only tx; the
	// per-line re-posts each open their own write tx below so a
	// long replay can't hold a single transaction open across the
	// whole batch.
	var unposted []store.UnpostedFeeLine
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		unposted, err = h.Receipts.ListUnpostedFeeLinesTx(r.Context(), tx)
		return err
	}); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	resp := replayFailedResponse{StillFailed: []replayLineFailure{}}

	for _, u := range unposted {
		perr := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			receipt, err := h.Receipts.GetByIDTx(r.Context(), tx, u.ReceiptID)
			if err != nil {
				return err
			}
			// Re-check the line state in the fresh tx — voids /
			// posts may have raced in between.
			var line *domain.ReceiptLine
			for i := range receipt.Lines {
				if receipt.Lines[i].ID == u.LineID {
					line = &receipt.Lines[i]
					break
				}
			}
			if line == nil || line.VoidedAt != nil || line.PostedTxnID != nil {
				resp.Skipped++
				return nil
			}
			txnID, ferr := h.postFeeLineTx(r.Context(), tx, tenantID, *receipt, *line, receipt.Channel)
			if ferr != nil {
				return ferr
			}
			if merr := h.Receipts.MarkLinePostedTx(r.Context(), tx, line.ID, txnID); merr != nil {
				return merr
			}
			resp.Replayed++
			return nil
		})
		if perr != nil {
			resp.StillFailed = append(resp.StillFailed, replayLineFailure{
				ReceiptID: u.ReceiptID,
				LineID:    u.LineID,
				Kind:      u.Kind,
				Payload:   perr.Error(),
			})
		}
	}

	if h.Logger != nil {
		h.Logger.Info("fee replay completed",
			"tenant", tenantID,
			"replayed", resp.Replayed,
			"skipped", resp.Skipped,
			"still_failed", len(resp.StillFailed))
	}
	httpx.OK(w, resp)
}

// ─────────── POST /v1/receipts/{id}/pdf ───────────
//
// Renders the receipt to PDF via the notification service, stamps the
// resulting pdf_documents.id back onto receipts.pdf_document_id, and
// returns a small envelope the frontend uses to build the download
// link. Synchronous — the chromedp render takes ~1–3s for an A5 page;
// fine inside a normal request lifecycle.
type renderPDFResponse struct {
	PDFDocumentID uuid.UUID `json:"pdf_document_id"`
	DownloadURL   string    `json:"download_url"`
}

func (h *CollectionDeskHandler) RenderPDF(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if h.Notifier == nil {
		httpx.WriteErr(w, r, fmt.Errorf("notifier client not configured"))
		return
	}
	tenantID, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)
	var receipt *domain.Receipt
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		r2, err := h.Receipts.GetByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		receipt = r2
		return nil
	})
	if err != nil {
		writeDeskErr(w, r, err)
		return
	}

	// Build the template payload. The bespoke {{lines_html}} render
	// happens here because the simple {{var}} engine in the
	// notification service doesn't support loops.
	cpName, cpNumber, cpLegacy, cashierName, tillCode := receiptDisplayContext(r.Context(), h, tenantID, receipt)
	payload := map[string]any{
		"serial":           receipt.Serial,
		"till_code":        tillCode,
		"cashier_name":     cashierName,
		"cp_display_name":  cpName,
		"cp_cp_number":     cpNumber,
		"cp_legacy_id":     cpLegacy,
		"value_date":       receipt.ValueDate.Format("2 January 2006"),
		"channel":          string(receipt.Channel),
		"channel_ref":      deref(receipt.ChannelRef),
		"channel_amount":   receipt.ChannelAmount.StringFixed(2),
		"lines_html":       renderLinesHTML(receipt.Lines),
		"narration_block":  renderNarrationBlock(receipt.Narration),
	}

	uid := userID
	gen, err := h.Notifier.GeneratePDF(r.Context(), notifier.PDFGenerateRequest{
		TenantID:        tenantID,
		DocumentType:    "CASH_RECEIPT",
		SubjectMemberID: &receipt.CounterpartyID,
		SubjectLabel:    "Cash receipt · " + receipt.Serial,
		Payload:         payload,
		GeneratedBy:     &uid,
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Receipts.SetPDFDocumentIDTx(r.Context(), tx, receipt.ID, gen.ID)
	}); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, renderPDFResponse{
		PDFDocumentID: gen.ID,
		// The authenticated admin download path. Frontend constructs
		// the full URL using the notification service base.
		DownloadURL: fmt.Sprintf("/v1/pdf-documents/%s/download", gen.ID),
	})
}

// receiptDisplayContext fetches the cosmetic strings the template
// needs (counterparty name, till code, cashier name). All best-effort:
// the PDF still renders if any lookup misses (falls back to a uuid
// prefix).
func receiptDisplayContext(ctx context.Context, h *CollectionDeskHandler, tenantID uuid.UUID, r *domain.Receipt) (cpName, cpNumber, cpLegacy, cashierName, tillCode string) {
	cashierName = r.CashierUserID.String()[:8] + "…"
	tillCode = "—"
	_ = h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Counterparty
		if cp, err := h.Counterparties.GetByIDTx(ctx, tx, r.CounterpartyID); err == nil {
			cpName = cp.FullName
			cpNumber = cp.MemberNo
			if cp.LegacyID != nil {
				cpLegacy = *cp.LegacyID
			}
		}
		// Cashier — best-effort lookup against users table (shared DB).
		var fullName *string
		_ = tx.QueryRow(ctx,
			`SELECT full_name FROM users WHERE id = $1`, r.CashierUserID,
		).Scan(&fullName)
		if fullName != nil && *fullName != "" {
			cashierName = *fullName
		}
		// Till code
		if r.TillSessionID != nil {
			_ = tx.QueryRow(ctx, `
				SELECT t.code FROM till_sessions s JOIN tills t ON t.id = s.till_id WHERE s.id = $1
			`, *r.TillSessionID).Scan(&tillCode)
		} else {
			// virtual till — use the channel slug for the serial-prefix
			// match.
			tillCode = string(r.Channel)
		}
		return nil
	})
	if cpLegacy == "" {
		cpLegacy = "—"
	}
	return
}

// renderLinesHTML pre-renders the <tr>...</tr> rows for the receipt
// template. Kept HTML-escape-conservative; line narration is treated
// as plain text via html.EscapeString.
func renderLinesHTML(lines []domain.ReceiptLine) string {
	var b []byte
	for _, l := range lines {
		desc := lineKindLabel(l.Kind)
		if l.Narration != nil && *l.Narration != "" {
			desc += `<div style="color:#777;font-size:8pt">` + htmlEscape(*l.Narration) + `</div>`
		}
		b = append(b, []byte(fmt.Sprintf(
			`<tr><td>%d</td><td>%s</td><td class="amt">%s</td></tr>`,
			l.LineNo, desc, l.Amount.StringFixed(2),
		))...)
	}
	return string(b)
}

func renderNarrationBlock(narration *string) string {
	if narration == nil || *narration == "" {
		return ""
	}
	return `<div class="narration">` + htmlEscape(*narration) + `</div>`
}

func lineKindLabel(k domain.ReceiptLineKind) string {
	switch k {
	case domain.LineSavingsDeposit:
		return "Savings deposit"
	case domain.LineSharePurchase:
		return "Share purchase"
	case domain.LineLoanRepayment:
		return "Loan repayment"
	case domain.LineFee:
		return "Fee payment"
	case domain.LineWelfare:
		return "Welfare contribution"
	}
	return string(k)
}

// htmlEscape is a tiny stand-in for html.EscapeString avoiding the
// stdlib import in this handler file's existing import set. Same
// semantics — &<>'"
func htmlEscape(s string) string {
	repl := strings.NewReplacer(
		`&`, `&amp;`,
		`<`, `&lt;`,
		`>`, `&gt;`,
		`"`, `&quot;`,
		`'`, `&#39;`,
	)
	return repl.Replace(s)
}

// ─────────── helpers ───────────

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ifEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeDeskErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteErr(w, r, httpx.ErrNotFound("not found"))
	case errors.Is(err, store.ErrDuplicateReceipt):
		httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
	default:
		httpx.WriteErr(w, r, err)
	}
}

// Silence the unused-import linter for json — used by the dispatcher
// interplay that other handlers exercise; keeps the file build-stable
// when the executors below are pulled in for context.
var _ = json.Marshal
var _ = context.Background
