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
	"github.com/nexussacco/savings/internal/store"
)

type LoanHandler struct {
	DB           *db.Pool
	Tenants      *store.TenantStore
	Members      *store.MemberStore
	LoanProducts *store.LoanProductStore
	Applications *store.LoanApplicationStore
	Guarantees   *store.LoanGuaranteeStore
	Loans        *store.LoanStore
	Deposits     *store.DepositStore
	Approvals    *store.ApprovalsStore
	Notifier     *notifier.Client
	Logger       *slog.Logger
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
			MemberID:          app.MemberID,
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
			memberID := loan.MemberID
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
		res, err := h.ExecuteDisbursementTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		writeLoanAppErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	// Notify borrower that the loan was disbursed.
	if h.Notifier != nil && result != nil {
		var member *store.MemberLite
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			var lerr error
			member, lerr = h.Members.GetTx(r.Context(), tx, result.Loan.MemberID)
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
	httpx.Created(w, result)
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
	if v := q.Get("member_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.MemberID = &id
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
		SELECT id, tenant_id, loan_id, member_id, txn_no, txn_type,
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
			&t.ID, &t.TenantID, &t.LoanID, &t.MemberID, &t.TxnNo, &t.TxnType,
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
