// Loan offer → acceptance → disbursement handlers (Phase 6c).
//
//   POST  /v1/loan-applications/{app_id}/send-offer    approved → offer_sent
//   POST  /v1/loan-applications/{app_id}/accept-offer  offer_sent → loan created + offer_accepted
//   POST  /v1/loans/{loan_id}/disburse                 active + amortisation schedule generated
//   GET   /v1/loans                                    list with filters
//   GET   /v1/loans/{loan_id}                          detail + schedule + transactions

package handler

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type LoanHandler struct {
	DB             *db.Pool
	Tenants        *store.TenantStore
	Members        *store.MemberStore
	Counterparties *store.CounterpartyStore
	LoanProducts   *store.LoanProductStore
	Applications   *store.LoanApplicationStore
	Guarantees     *store.LoanGuaranteeStore
	Loans          *store.LoanStore
	Deposits       *store.DepositStore
	// DepositProducts resolves the savings liability code when a loan
	// disburses to an internal target account (depositLiabilityCode
	// needs segment + product_type from the target's product).
	DepositProducts *store.DepositProductStore
	Approvals      *store.ApprovalsStore
	Notifier       *notifier.Client
	Posting        *posting.Client
	Logger         *slog.Logger
}

// ─────────── Send offer ───────────

type sendOfferReq struct {
	ExpiresAt *string `json:"expires_at,omitempty"`  // YYYY-MM-DD; default = +14 days
	LetterPath *string `json:"letter_path,omitempty"` // storage path for the signed offer letter
}

func (h *LoanHandler) SendOffer(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in sendOfferReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	expires := time.Now().AddDate(0, 0, 14)
	if in.ExpiresAt != nil && *in.ExpiresAt != "" {
		d, err := time.Parse("2006-01-02", *in.ExpiresAt)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("expires_at must be YYYY-MM-DD"))
			return
		}
		expires = d
	}

	var out *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if app.Status != domain.AppApproved && app.Status != domain.AppApprovedWithConditions {
			return domain.ErrAppNotOfferable
		}
		out, err = h.Applications.UpdateStatusTx(r.Context(), tx, id, store.AppTransition{
			To: domain.AppOfferSent, By: userID,
			OfferExpiresAt: &expires,
			OfferLetterPath: in.LetterPath,
		})
		return err
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Accept offer ───────────
//
// On acceptance we create the `loans` row with the approved terms
// snapshot. The schedule is *not* generated yet — that happens at
// disbursement so the first-due-date is anchored on the actual
// disbursement date.

type acceptOfferReq struct {
	// Optional: signed offer letter doc upload would be linked here in
	// the full UI; for now we accept just the boolean confirmation.
	Confirmed bool `json:"confirmed"`
}

type acceptOfferResp struct {
	Application domain.LoanApplication `json:"application"`
	Loan        domain.Loan            `json:"loan"`
}

func (h *LoanHandler) AcceptOffer(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "app_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in acceptOfferReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if !in.Confirmed {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("confirmed must be true"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)

	var resp acceptOfferResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		app, err := h.Applications.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if app.Status != domain.AppOfferSent {
			return domain.ErrAppNotAcceptable
		}
		// Validate expiry.
		if app.OfferExpiresAt != nil && app.OfferExpiresAt.Before(time.Now()) {
			return httpx.ErrConflict("offer has expired")
		}
		// Pull approved terms (mandatory at this point).
		if app.ApprovedAmount == nil || app.ApprovedTermMonths == nil || app.ApprovedInterestRatePct == nil {
			return httpx.ErrConflict("application has incomplete approval terms")
		}
		product, err := h.LoanProducts.GetTx(r.Context(), tx, app.ProductID)
		if err != nil {
			return err
		}
		// Move app to offer_accepted.
		updatedApp, err := h.Applications.UpdateStatusTx(r.Context(), tx, id, store.AppTransition{
			To: domain.AppOfferAccepted, By: userID,
		})
		if err != nil {
			return err
		}
		// Create the loan row.
		loan, err := h.Loans.CreateOnAcceptanceTx(r.Context(), tx, store.CreateLoanInput{
			ApplicationID:     app.ID,
			CounterpartyID:          app.CounterpartyID,
			ProductID:         app.ProductID,
			Principal:         *app.ApprovedAmount,
			InterestRatePct:   *app.ApprovedInterestRatePct,
			InterestMethod:    product.InterestMethod,
			RepaymentMethod:   product.RepaymentMethod,
			TermMonths:        *app.ApprovedTermMonths,
			GracePeriodMonths: product.GracePeriodMonths,
			InstallmentCount:  *app.ApprovedTermMonths, // monthly installments — equals term
		})
		if err != nil {
			return err
		}
		// Backfill loan_id on guarantees + collateral + documents.
		if err := h.Guarantees.BackfillLoanIDTx(r.Context(), tx, app.ID, loan.ID); err != nil {
			return err
		}
		resp = acceptOfferResp{Application: *updatedApp, Loan: *loan}
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	httpx.Created(w, resp)
}

// ─────────── Disburse ───────────

type disburseReq struct {
	Channel             string     `json:"channel"`               // 'internal' | 'mpesa' | 'bank_transfer' | 'wallet'
	TargetAccountID     *uuid.UUID `json:"target_account_id,omitempty"` // required when channel='internal'
	ExternalRef         *string    `json:"external_ref,omitempty"`
	ValueDate           *string    `json:"value_date,omitempty"`  // YYYY-MM-DD; default = today
}

type disburseResp struct {
	Loan        domain.Loan              `json:"loan"`
	Schedule    []domain.LoanInstallment `json:"schedule"`
	NetDisbursed decimal.Decimal         `json:"net_disbursed"`
	Fees        []domain.LoanTransaction `json:"fees"`
	Disbursement domain.LoanTransaction  `json:"disbursement"`
}

func (h *LoanHandler) Disburse(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in disburseReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Channel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("channel is required"))
		return
	}
	if in.Channel == "internal" && in.TargetAccountID == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("target_account_id is required when channel='internal'"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if in.ValueDate != nil && *in.ValueDate != "" {
		if _, err := time.Parse("2006-01-02", *in.ValueDate); err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
	}

	payload := LoanDisbursementPayload{
		LoanID:          loanID,
		Channel:         in.Channel,
		TargetAccountID: in.TargetAccountID,
		ExternalRef:     in.ExternalRef,
		ValueDate:       in.ValueDate,
	}

	var result *LoanDisbursementResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanDisbursement {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.CounterpartyID
			amount := loan.Principal
			pa, qerr := h.Approvals.QueueTx(r.Context(), tx, store.QueueInput{
				Kind:            domain.ApprovalKindLoanDisbursement,
				Title:           "Disburse loan " + loan.LoanNo,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
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
		// Phase-4 M-PESA branch. When channel='mpesa' the
		// disbursement is deferred until Daraja confirms cash has
		// actually left the paybill. Inside this tx we:
		//   1. Validate the loan is still in pending_disbursement.
		//   2. Resolve the member's MSISDN + the tenant's default
		//      B2C paybill.
		//   3. Insert an mpesa_outbound_requests row (shared-DB —
		//      this is exactly the precedent ApplicationFeeReceipt
		//      / PostOpeningContributions set in earlier phases).
		// The actual ExecuteDisbursementTx + GL post fire when
		// mpesa's b2c result handler hits POST /internal/v1/
		// loans/{id}/finalize-disbursement (FinalizeDisbursement
		// below).
		if strings.EqualFold(in.Channel, "mpesa") {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			if loan.Status != domain.LoanPendingDisbursement {
				return domain.ErrAppNotDisbursable
			}
			msisdn, err := mpesaMSISDNForCounterpartyTx(r.Context(), tx, loan.CounterpartyID)
			if err != nil {
				return fmt.Errorf("resolve msisdn: %w", err)
			}
			paybillID, err := defaultB2CPaybillTx(r.Context(), tx, tid)
			if err != nil {
				return fmt.Errorf("resolve default b2c paybill: %w", err)
			}
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO mpesa_outbound_requests
					(tenant_id, paybill_id, kind, msisdn, amount,
					 source_module, source_ref, status)
				VALUES ($1, $2, 'b2c_disbursement', $3, $4,
				        'loan.disbursement', $5, 'pending')
				ON CONFLICT (tenant_id, source_module, source_ref) DO NOTHING
			`, tid, paybillID, msisdn, loan.Principal, loan.ID.String()); err != nil {
				return fmt.Errorf("queue mpesa outbound: %w", err)
			}
			// Loan stays in pending_disbursement; no GL post yet.
			// result stays nil; the response below handles that.
			return nil
		}

		res, err := h.ExecuteDisbursementTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		// In-tx outbox post:
		//   Debit  Member Loans Receivable
		//   Credit Cash / M-Pesa / Bank   (per channel)
		// Failure here rolls back the disbursement.
		if perr := h.postLoanDisbursementToGLTx(r.Context(), tx, tid, res, in.Channel); perr != nil {
			return perr
		}
		result = res
		return nil
	})
	if err != nil {
		if errors.Is(err, posting.ErrOutboxInsert) {
			httpx.WriteErr(w, r, httpx.ErrGLPostFailed(err.Error()))
			return
		}
		writeLoanAppErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	// Notify borrower that the loan was disbursed.
	if h.Notifier != nil && result != nil {
		var member *store.CounterpartyView
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			var lerr error
			member, lerr = h.Counterparties.GetByIDTx(r.Context(), tx, result.Loan.CounterpartyID)
			return lerr
		})
		if member != nil {
			sourceModule := "savings.loans"
			recordID := result.Disbursement.ID
			deepLink := "/loans/" + result.Loan.ID.String()
			mid := member.ID
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          tid,
				EventCode:         "LOAN_DISBURSED",
				RecipientMemberID: &mid,
				RecipientName:     member.FullName,
				RecipientPhone:    strNilIfEmpty(member.Phone),
				RecipientEmail:    strNilIfEmpty(member.Email),
				SourceModule:      &sourceModule,
				SourceRecordID:    &recordID,
				DeepLink:          &deepLink,
				InitiatedBy:       nonZeroUUID(userID),
				Payload: map[string]any{
					"member_no":     member.MemberNo,
					"full_name":     member.FullName,
					"loan_no":       result.Loan.LoanNo,
					"principal":     result.Loan.Principal.String(),
					"net_disbursed": result.NetDisbursed.String(),
					"channel":       in.Channel,
				},
			})
		}
	}
	if result == nil {
		// M-PESA branch: outbound row was queued; the loan stays in
		// pending_disbursement until Daraja's Result callback hits
		// our finalize endpoint and runs ExecuteDisbursementTx.
		httpx.Created(w, map[string]any{
			"status":  "queued_for_mpesa",
			"loan_id": loanID,
			"message": "B2C payment queued; loan will move to active once Daraja confirms.",
		})
		return
	}
	httpx.Created(w, result)
}

// mpesaMSISDNForCounterpartyTx pulls the member's primary phone out
// of counterparties.contact and normalises it to the 254xxxxxxxxx
// shape Daraja's B2C expects. Returns an error when the field is
// missing — disbursement to a member without a phone number on file
// fails closed rather than silently dropping the cash.
func mpesaMSISDNForCounterpartyTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (string, error) {
	var raw string
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(contact->>'phone', '') FROM counterparties WHERE id = $1`,
		cpID,
	).Scan(&raw)
	if err != nil {
		return "", err
	}
	// Strip everything except digits + collapse the country-code
	// prefixes ("0xxx…", "+254xxx…", "254xxx…") down to a single
	// canonical "254xxxxxxxxx". Daraja rejects + or leading 0.
	digits := ""
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	if strings.HasPrefix(digits, "254") {
		// already canonical
	} else if strings.HasPrefix(digits, "0") {
		digits = "254" + digits[1:]
	} else if len(digits) == 9 {
		digits = "254" + digits
	}
	if len(digits) != 12 || !strings.HasPrefix(digits, "254") {
		return "", fmt.Errorf("counterparty %s has no usable msisdn (raw=%q normalised=%q)", cpID, raw, digits)
	}
	return digits, nil
}

// defaultB2CPaybillTx returns the tenant's preferred B2C paybill —
// active, purpose includes b2c, is_default=true. Falls back to the
// most-recently-updated active b2c paybill when no default is
// flagged. Errors when nothing matches; phase 4 expects every
// tenant to have at least one configured before any disbursement
// uses channel='mpesa'.
func defaultB2CPaybillTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM mpesa_paybills
		 WHERE tenant_id = $1
		   AND status   = 'active'
		   AND purpose  IN ('disbursement', 'both')
		 ORDER BY is_default DESC, updated_at DESC
		 LIMIT 1
	`, tenantID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("no active b2c-capable paybill configured for tenant %s", tenantID)
		}
		return uuid.Nil, err
	}
	return id, nil
}

// ─────────── Reads ───────────

type loanDetailResp struct {
	Loan         domain.Loan               `json:"loan"`
	Schedule     []domain.LoanInstallment  `json:"schedule"`
	Transactions []domain.LoanTransaction  `json:"transactions"`
	Guarantees   []domain.LoanGuarantee    `json:"guarantees"`
	Collateral   []domain.LoanCollateralItem `json:"collateral"`
}

func (h *LoanHandler) GetLoan(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out loanDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		loan, err := h.Loans.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		schedule, err := h.Loans.ScheduleByLoanTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		txns, err := loadLoanTxnsTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		guars, err := h.Guarantees.ByApplicationTx(r.Context(), tx, loan.ApplicationID)
		if err != nil {
			return err
		}
		coll, err := h.Guarantees.CollateralByApplicationTx(r.Context(), tx, loan.ApplicationID)
		if err != nil {
			return err
		}
		out = loanDetailResp{
			Loan: *loan, Schedule: schedule, Transactions: txns,
			Guarantees: guars, Collateral: coll,
		}
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if out.Schedule == nil { out.Schedule = []domain.LoanInstallment{} }
	if out.Transactions == nil { out.Transactions = []domain.LoanTransaction{} }
	if out.Guarantees == nil { out.Guarantees = []domain.LoanGuarantee{} }
	if out.Collateral == nil { out.Collateral = []domain.LoanCollateralItem{} }
	httpx.OK(w, out)
}

func (h *LoanHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.LoanListFilter{Status: q.Get("status"), Q: q.Get("q"), Limit: limit, Offset: offset}
	if v := q.Get("counterparty_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.CounterpartyID = &id
		}
	}
	if v := q.Get("product_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.ProductID = &id
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.LoanListItem
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Loans.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil { items = []store.LoanListItem{} }
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// loadLoanTxnsTx is a small helper for the detail endpoint.
func loadLoanTxnsTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) ([]domain.LoanTransaction, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, loan_id, counterparty_id, txn_no, txn_type,
		       amount, principal_component, interest_component, fee_component, penalty_component,
		       value_date, channel, channel_ref, narration,
		       reverses_txn_id, reversed_by_txn_id, installment_no,
		       posted_at, initiated_by, authorized_by
		FROM loan_transactions WHERE loan_id = $1 ORDER BY posted_at DESC
	`, loanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanTransaction
	for rows.Next() {
		var t domain.LoanTransaction
		if err := rows.Scan(
			&t.ID, &t.TenantID, &t.LoanID, &t.CounterpartyID, &t.TxnNo, &t.TxnType,
			&t.Amount, &t.PrincipalComponent, &t.InterestComponent, &t.FeeComponent, &t.PenaltyComponent,
			&t.ValueDate, &t.Channel, &t.ChannelRef, &t.Narration,
			&t.ReversesTxnID, &t.ReversedByTxnID, &t.InstallmentNo,
			&t.PostedAt, &t.InitiatedBy, &t.AuthorizedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

var _ = errors.New

// postLoanDisbursementToGLTx queues the batched disbursement GL entry
// onto the outbox INSIDE the caller's tx.
//
// Aggregation:
//
//   DR 1100 Member Loans Receivable        = principal
//   CR cash leg                            = net disbursed (= principal − upfront fees)
//   CR per-fee gl_credit_code (aggregated) = each fee bucket
//
// Cash leg resolves by channel:
//   • mpesa/bank/cash channels   → channel cash account (1030/1020/1000)
//   • internal channel            → segment-aware member savings liability
//                                  (depositLiabilityCode of the target
//                                  account's product) — credits the
//                                  right BOSA/FOSA code instead of
//                                  hardcoding 2000.
//
// Balance: DR(principal) = CR(net) + Σ CR(fees) = (principal − F) + F = principal.
//
// Failure (outbox INSERT error) rolls back the whole disbursement
// tx — schedule, loan_transactions, MarkDisbursedTx, application
// status flip all unwind. Handler surfaces 502 + gl_post_failed.
func (h *LoanHandler) postLoanDisbursementToGLTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, result *LoanDisbursementResult, channel string) error {
	if h.Posting == nil || result == nil {
		return nil
	}
	principal := result.Loan.Principal
	net := result.NetDisbursed
	if net.IsZero() && len(result.FeeGLLines) == 0 {
		// No fees deducted — net falls back to principal so the
		// cash leg still balances the DR.
		net = principal
	}

	cashAcct := h.resolveDisbursementCashAcctTx(ctx, tx, channel, result.Loan)
	narration := fmt.Sprintf("Loan %s disbursement via %s",
		result.Loan.LoanNo, channel)
	cashNarration := "Cash disbursed"
	if strings.ToLower(channel) == "internal" || strings.ToLower(channel) == "savings" {
		cashNarration = "Credited to member savings (" + cashAcct + ")"
	}

	lines := []posting.Line{
		{AccountCode: "1100", Debit: principal, Narration: "Loan receivable created"},
		{AccountCode: cashAcct, Credit: net, Narration: cashNarration},
	}
	// FeeGLLines is already aggregated by gl_credit_code and sorted by
	// code (see the executor), so we can append verbatim.
	lines = append(lines, result.FeeGLLines...)

	return h.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     tenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.disbursement",
		SourceRef:    result.Disbursement.ID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}

// resolveDisbursementCashAcctTx picks the credit-side account for the
// cash leg. Non-internal channels map to the channel cash account
// (1030 M-Pesa, 1020 Bank, 1000 Cash). Internal channels look up the
// target deposit account's product and use depositLiabilityCode so a
// BOSA target hits 2050 instead of the legacy 2000 default. Falls
// back to 2000 if the lookup fails so the GL post still commits.
func (h *LoanHandler) resolveDisbursementCashAcctTx(ctx context.Context, tx pgx.Tx, channel string, loan domain.Loan) string {
	ch := strings.ToLower(channel)
	if ch != "internal" && ch != "savings" {
		switch ch {
		case "mpesa":
			// Phase 4: B2C disbursements debit Loans Receivable
			// (1100) and credit the per-paybill cash account
			// 1015. The legacy 1030 (M-Pesa Float) is kept for
			// historical references; new disbursements land
			// against 1015 so the GL reflects "money has left the
			// Safaricom paybill".
			return "1015"
		case "bank", "bank_transfer":
			return "1020"
		default:
			return "1000"
		}
	}
	// Internal channel: resolve the target deposit account's product.
	if loan.DisbursementTargetAccountID == nil || h.Deposits == nil || h.DepositProducts == nil {
		return "2000"
	}
	acct, err := h.Deposits.GetAccountTx(ctx, tx, *loan.DisbursementTargetAccountID)
	if err != nil || acct == nil {
		return "2000"
	}
	p, err := h.DepositProducts.GetTx(ctx, tx, acct.ProductID)
	if err != nil || p == nil {
		return "2000"
	}
	return depositLiabilityCode(p.Segment, p.ProductType)
}
