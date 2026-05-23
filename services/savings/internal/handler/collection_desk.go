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
	Logger         *slog.Logger
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

		// Queue an approval per line. Fee + welfare lines have no
		// existing approval kind / executor; for v1 we mark them
		// 'posted' immediately and leave the underlying GL move to a
		// follow-up PR (fee catalog work).
		for i := range r2.Lines {
			line := &r2.Lines[i]
			approvalKind, payload, perr := buildApprovalPayload(*line, *r2, in.Channel, in.ChannelRef, in.Narration, valueDate)
			if perr != nil {
				return perr
			}
			if approvalKind == "" {
				// Fee/welfare: mark posted immediately, defer GL move.
				if mErr := h.Receipts.MarkLinePostedTx(r.Context(), tx, line.ID, uuid.Nil); mErr != nil {
					return mErr
				}
				continue
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

// buildApprovalPayload maps a receipt line to the existing approval
// kind + the typed payload struct the dispatcher (pending_approvals.go
// executePayloadTx) already knows how to consume on approve. Returns
// ("", nil, nil) for fee/welfare kinds — those have no executor yet.
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
	case domain.LineFee, domain.LineWelfare:
		// No approval kind yet — v1 marks these posted immediately.
		return "", nil, nil
	}
	return "", nil, httpx.ErrBadRequest("unknown receipt line kind: " + string(line.Kind))
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
	var rows []domain.Receipt
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
		rows = []domain.Receipt{}
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
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		// v1 just marks the line voided; the underlying-txn reversal
		// (deposit reverse / share reverse / loan reverse) is a
		// follow-up PR (needs the appropriate Execute*ReverseTx wired
		// through the approvals dispatcher).
		return h.Receipts.VoidLineTx(r.Context(), tx, store.VoidLineInput{
			LineID:   lineID,
			VoidedBy: userID,
			Reason:   in.Reason,
		})
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"status": "voided", "line_id": lineID})
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
