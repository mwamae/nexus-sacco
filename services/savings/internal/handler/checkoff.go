// Loans Phase 5 — salary check-off (payroll deduction) batch handler.
//
// Three endpoints + a list/get pair:
//
//   POST /v1/loans/checkoff/batches             upload + parse CSV
//   POST /v1/loans/checkoff/batches/{id}/validate    resolve rows
//   POST /v1/loans/checkoff/batches/{id}/post        post matched rows
//   POST /v1/loans/checkoff/batches/{id}/rows/{row_id}/resolve  manual fix
//   GET  /v1/loans/checkoff/batches              list
//   GET  /v1/loans/checkoff/batches/{id}         detail + rows
//
// CSV format (header optional; we sniff):
//
//   member_no,amount[,note]
//   M-12345,1500
//   M-67890,2200
//
// Idempotency: a posted row's `posted_txn_id` is set; re-posting the
// batch is a no-op for those rows. Per-row failures are captured in
// status='failed' + error_message; the batch finishes with status
// 'posted' (all matched succeeded), 'partial', or 'failed'.

package handler

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type CheckoffHandler struct {
	DB     *db.Pool
	Loans  *store.LoanStore
	Logger *slog.Logger
}

// ─────────── Upload ───────────

func (h *CheckoffHandler) Upload(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)

	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10 MB cap
		httpx.WriteErr(w, r, httpx.ErrBadRequest("multipart parse failed: "+err.Error())); return
	}
	employerName := strings.TrimSpace(r.FormValue("employer_name"))
	employerCode := strings.TrimSpace(r.FormValue("employer_code"))
	periodLabel := strings.TrimSpace(r.FormValue("period_label"))
	if employerName == "" || periodLabel == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("employer_name and period_label are required")); return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing 'file' field")); return
	}
	defer file.Close()
	if header.Size > 5<<20 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file too large; 5 MB max")); return
	}
	rows, parseErr := parseCheckoffCSV(file)
	if parseErr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("CSV parse failed: "+parseErr.Error())); return
	}
	if len(rows) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("CSV had no data rows")); return
	}

	var batchID uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO checkoff_batches (
			  tenant_id, employer_name, employer_code, period_label,
			  upload_filename, uploaded_by, row_count
			) VALUES (current_tenant_id(), $1, NULLIF($2, ''), $3, $4, $5, $6)
			RETURNING id
		`, employerName, employerCode, periodLabel, header.Filename, uid, len(rows)).Scan(&batchID); err != nil {
			return err
		}
		for i, row := range rows {
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO checkoff_batch_rows (batch_id, row_no, member_no_raw, amount_raw)
				VALUES ($1, $2, $3, $4)
			`, batchID, i+1, row.MemberNo, row.AmountRaw); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.Created(w, map[string]any{"batch_id": batchID, "row_count": len(rows)})
}

type parsedCheckoffRow struct {
	MemberNo  string
	AmountRaw string
}

// parseCheckoffCSV reads CSV with at least 2 columns (member_no,amount).
// Sniffs whether the first row is a header — if both fields in row 1
// don't parse as a numeric for the second column, treat as header.
func parseCheckoffCSV(r io.Reader) ([]parsedCheckoffRow, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate variable column counts
	cr.LazyQuotes = true
	all, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	// Header sniff.
	start := 0
	if len(all[0]) >= 2 {
		if _, err := decimal.NewFromString(strings.TrimSpace(all[0][1])); err != nil {
			start = 1
		}
	}
	var out []parsedCheckoffRow
	for i := start; i < len(all); i++ {
		rec := all[i]
		if len(rec) < 2 {
			continue
		}
		m := strings.TrimSpace(rec[0])
		a := strings.TrimSpace(rec[1])
		if m == "" || a == "" {
			continue
		}
		out = append(out, parsedCheckoffRow{MemberNo: m, AmountRaw: a})
	}
	return out, nil
}

// ─────────── Validate ───────────

func (h *CheckoffHandler) Validate(w http.ResponseWriter, r *http.Request) {
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid batch id")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var summary map[string]any
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		summary, err = validateCheckoffBatchTx(r.Context(), tx, batchID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, summary)
}

func validateCheckoffBatchTx(ctx context.Context, tx pgx.Tx, batchID uuid.UUID) (map[string]any, error) {
	// Status guard.
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM checkoff_batches WHERE id = $1`, batchID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.ErrNotFound("batch not found")
		}
		return nil, err
	}
	if status != "draft" && status != "validated" {
		return nil, httpx.ErrConflict("batch is " + status + "; cannot re-validate")
	}

	// Pull all rows.
	rows, err := tx.Query(ctx, `
		SELECT id, member_no_raw, amount_raw
		  FROM checkoff_batch_rows
		 WHERE batch_id = $1
		 ORDER BY row_no
	`, batchID)
	if err != nil {
		return nil, err
	}
	type rowState struct {
		id        uuid.UUID
		memberNo  string
		amountRaw string
	}
	var rs []rowState
	for rows.Next() {
		var r rowState
		if err := rows.Scan(&r.id, &r.memberNo, &r.amountRaw); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	matched, ambiguous, unmatched, unmatchedAmt := 0, 0, 0, decimal.Zero
	for _, row := range rs {
		amt, amtErr := decimal.NewFromString(row.amountRaw)
		if amtErr != nil || !amt.IsPositive() {
			if _, err := tx.Exec(ctx, `
				UPDATE checkoff_batch_rows
				   SET status='unmatched', error_message=$2
				 WHERE id = $1
			`, row.id, "amount could not be parsed"); err != nil {
				return nil, err
			}
			unmatched++
			continue
		}
		// Resolve member by member_no via counterparty_directory.
		var memberID uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT member_id FROM counterparty_directory
			 WHERE member_no = $1 AND member_id IS NOT NULL
			 LIMIT 1
		`, row.memberNo).Scan(&memberID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := tx.Exec(ctx, `
					UPDATE checkoff_batch_rows
					   SET status='unmatched', amount=$2, error_message=$3
					 WHERE id = $1
				`, row.id, amt, "member_no not found"); err != nil {
					return nil, err
				}
				unmatched++
				unmatchedAmt = unmatchedAmt.Add(amt)
				continue
			}
			return nil, err
		}
		// Find active loans for this member.
		loanRows, err := tx.Query(ctx, `
			SELECT l.id FROM loans l
			 JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			 WHERE cd.member_id = $1
			   AND l.status IN ('active','in_arrears','restructured')
		`, memberID)
		if err != nil {
			return nil, err
		}
		var loanIDs []uuid.UUID
		for loanRows.Next() {
			var id uuid.UUID
			if err := loanRows.Scan(&id); err != nil {
				loanRows.Close()
				return nil, err
			}
			loanIDs = append(loanIDs, id)
		}
		loanRows.Close()
		if err := loanRows.Err(); err != nil {
			return nil, err
		}

		switch len(loanIDs) {
		case 0:
			if _, err := tx.Exec(ctx, `
				UPDATE checkoff_batch_rows
				   SET status='unmatched', resolved_member_id=$2, amount=$3, error_message=$4
				 WHERE id = $1
			`, row.id, memberID, amt, "member has no active loans"); err != nil {
				return nil, err
			}
			unmatched++
			unmatchedAmt = unmatchedAmt.Add(amt)
		case 1:
			if _, err := tx.Exec(ctx, `
				UPDATE checkoff_batch_rows
				   SET status='matched', resolved_member_id=$2, resolved_loan_id=$3,
				       amount=$4, error_message=NULL
				 WHERE id = $1
			`, row.id, memberID, loanIDs[0], amt); err != nil {
				return nil, err
			}
			matched++
		default:
			if _, err := tx.Exec(ctx, `
				UPDATE checkoff_batch_rows
				   SET status='ambiguous', resolved_member_id=$2, amount=$3,
				       error_message=$4
				 WHERE id = $1
			`, row.id, memberID, amt, fmt.Sprintf("member has %d active loans — pick one", len(loanIDs))); err != nil {
				return nil, err
			}
			ambiguous++
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE checkoff_batches
		   SET status='validated',
		       matched_count=$2, unmatched_count=$3, unmatched_amount=$4
		 WHERE id = $1
	`, batchID, matched, unmatched+ambiguous, unmatchedAmt); err != nil {
		return nil, err
	}
	return map[string]any{
		"batch_id": batchID,
		"matched": matched,
		"ambiguous": ambiguous,
		"unmatched": unmatched,
		"unmatched_amount": unmatchedAmt.String(),
	}, nil
}

// ─────────── Manual row resolve ───────────

type resolveRowReq struct {
	LoanID *uuid.UUID `json:"loan_id"` // nil → mark skipped
	Skip   bool       `json:"skip"`
}

func (h *CheckoffHandler) ResolveRow(w http.ResponseWriter, r *http.Request) {
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid batch id")); return }
	rowID, err := uuid.Parse(chi.URLParam(r, "row_id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid row id")); return }
	var in resolveRowReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if in.Skip {
			_, err := tx.Exec(r.Context(), `
				UPDATE checkoff_batch_rows
				   SET status='skipped', error_message='manually skipped'
				 WHERE id = $1 AND batch_id = $2
			`, rowID, batchID)
			return err
		}
		if in.LoanID == nil {
			return httpx.ErrBadRequest("loan_id or skip=true required")
		}
		_, err := tx.Exec(r.Context(), `
			UPDATE checkoff_batch_rows
			   SET status='matched', resolved_loan_id=$2, error_message=NULL
			 WHERE id = $1 AND batch_id = $3
		`, rowID, *in.LoanID, batchID)
		return err
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Post ───────────

func (h *CheckoffHandler) Post(w http.ResponseWriter, r *http.Request) {
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid batch id")); return }
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil { httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return }
	tid, _ := middleware.TenantIDFrom(r)

	var result map[string]any
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Status guard.
		var status string
		if err := tx.QueryRow(r.Context(), `SELECT status FROM checkoff_batches WHERE id = $1`, batchID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) { return httpx.ErrNotFound("batch not found") }
			return err
		}
		if status != "validated" && status != "partial" {
			return httpx.ErrConflict("batch is " + status + "; only validated/partial batches can be posted")
		}

		// Collect matched + un-posted rows.
		rows, err := tx.Query(r.Context(), `
			SELECT id, resolved_loan_id, amount
			  FROM checkoff_batch_rows
			 WHERE batch_id = $1
			   AND status = 'matched'
			   AND posted_txn_id IS NULL
		`, batchID)
		if err != nil { return err }
		type row struct {
			id     uuid.UUID
			loanID uuid.UUID
			amt    decimal.Decimal
		}
		var todo []row
		for rows.Next() {
			var rr row
			var lid *uuid.UUID
			if err := rows.Scan(&rr.id, &lid, &rr.amt); err != nil {
				rows.Close()
				return err
			}
			if lid == nil { continue }
			rr.loanID = *lid
			todo = append(todo, rr)
		}
		rows.Close()
		if err := rows.Err(); err != nil { return err }

		waterfall, err := h.Loans.GetWaterfallTx(r.Context(), tx)
		if err != nil { return err }

		posted := 0
		failed := 0
		var postedAmount decimal.Decimal
		for _, rr := range todo {
			// Re-load loan inside the loop in case earlier rows changed it.
			loan, err := h.Loans.GetTx(r.Context(), tx, rr.loanID)
			if err != nil {
				markCheckoffRowFailed(r.Context(), tx, rr.id, "load loan: "+err.Error())
				failed++
				continue
			}
			if loan.Status != domain.LoanActive && loan.Status != domain.LoanInArrears && loan.Status != domain.LoanRestructured {
				markCheckoffRowFailed(r.Context(), tx, rr.id, "loan is "+string(loan.Status))
				failed++
				continue
			}
			channelRef := batchID.String()[:8]
			txn, _, err := h.Loans.PostRepaymentTx(r.Context(), tx, store.RepaymentInput{
				Loan: loan, Amount: rr.amt,
				Channel: "payroll", ChannelRef: channelRef,
				Narration: "Salary check-off · batch " + channelRef,
				ValueDate: time.Now(), InitiatedBy: uid,
			}, waterfall)
			if err != nil {
				markCheckoffRowFailed(r.Context(), tx, rr.id, err.Error())
				failed++
				continue
			}
			if _, err := tx.Exec(r.Context(), `
				UPDATE checkoff_batch_rows
				   SET status='posted', posted_txn_id=$2, error_message=NULL
				 WHERE id = $1
			`, rr.id, txn.ID); err != nil {
				return err
			}
			posted++
			postedAmount = postedAmount.Add(rr.amt)
		}

		// Final batch status.
		final := "posted"
		if posted == 0 {
			final = "failed"
		} else if failed > 0 || (posted < len(todo)) {
			final = "partial"
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE checkoff_batches
			   SET status=$2, posted_amount=posted_amount + $3,
			       posted_at=COALESCE(posted_at, now()),
			       posted_by=COALESCE(posted_by, $4)
			 WHERE id = $1
		`, batchID, final, postedAmount, uid); err != nil {
			return err
		}
		result = map[string]any{
			"batch_id":       batchID,
			"status":         final,
			"posted_rows":    posted,
			"failed_rows":    failed,
			"posted_amount":  postedAmount.String(),
		}
		return nil
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, result)
}

func markCheckoffRowFailed(ctx context.Context, tx pgx.Tx, rowID uuid.UUID, msg string) {
	if len(msg) > 500 { msg = msg[:500] }
	_, _ = tx.Exec(ctx, `
		UPDATE checkoff_batch_rows
		   SET status='failed', error_message=$2
		 WHERE id = $1
	`, rowID, msg)
}

// ─────────── Read ───────────

func (h *CheckoffHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit := 50
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 { limit = n }
	}
	type batchListRow struct {
		ID              uuid.UUID `json:"id"`
		EmployerName    string    `json:"employer_name"`
		PeriodLabel     string    `json:"period_label"`
		UploadFilename  string    `json:"upload_filename"`
		Status          string    `json:"status"`
		RowCount        int       `json:"row_count"`
		MatchedCount    int       `json:"matched_count"`
		UnmatchedCount  int       `json:"unmatched_count"`
		PostedAmount    string    `json:"posted_amount"`
		UploadedAt      time.Time `json:"uploaded_at"`
	}
	var items []batchListRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, employer_name, period_label, upload_filename, status,
			       row_count, matched_count, unmatched_count, posted_amount::text, uploaded_at
			  FROM checkoff_batches
			 ORDER BY uploaded_at DESC
			 LIMIT $1
		`, limit)
		if err != nil { return err }
		defer rows.Close()
		for rows.Next() {
			var b batchListRow
			if err := rows.Scan(&b.ID, &b.EmployerName, &b.PeriodLabel, &b.UploadFilename, &b.Status,
				&b.RowCount, &b.MatchedCount, &b.UnmatchedCount, &b.PostedAmount, &b.UploadedAt); err != nil {
				return err
			}
			items = append(items, b)
		}
		return rows.Err()
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *CheckoffHandler) Get(w http.ResponseWriter, r *http.Request) {
	batchID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid batch id")); return }
	tid, _ := middleware.TenantIDFrom(r)
	var resp map[string]any
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var batch map[string]any
		var employerName, periodLabel, filename, status string
		var rowCount, matched, unmatched int
		var postedAmount, unmatchedAmount string
		var uploadedAt time.Time
		var postedAt *time.Time
		if err := tx.QueryRow(r.Context(), `
			SELECT employer_name, period_label, upload_filename, status,
			       row_count, matched_count, unmatched_count,
			       posted_amount::text, unmatched_amount::text,
			       uploaded_at, posted_at
			  FROM checkoff_batches WHERE id = $1
		`, batchID).Scan(&employerName, &periodLabel, &filename, &status,
			&rowCount, &matched, &unmatched, &postedAmount, &unmatchedAmount,
			&uploadedAt, &postedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) { return httpx.ErrNotFound("batch not found") }
			return err
		}
		batch = map[string]any{
			"id": batchID, "employer_name": employerName, "period_label": periodLabel,
			"upload_filename": filename, "status": status,
			"row_count": rowCount, "matched_count": matched, "unmatched_count": unmatched,
			"posted_amount": postedAmount, "unmatched_amount": unmatchedAmount,
			"uploaded_at": uploadedAt, "posted_at": postedAt,
		}
		rows, err := tx.Query(r.Context(), `
			SELECT id, row_no, member_no_raw, amount_raw, resolved_member_id,
			       resolved_loan_id, amount::text, status, error_message, posted_txn_id
			  FROM checkoff_batch_rows
			 WHERE batch_id = $1
			 ORDER BY row_no
		`, batchID)
		if err != nil { return err }
		defer rows.Close()
		type rowOut struct {
			ID               uuid.UUID  `json:"id"`
			RowNo            int        `json:"row_no"`
			MemberNoRaw      string     `json:"member_no_raw"`
			AmountRaw        string     `json:"amount_raw"`
			ResolvedMemberID *uuid.UUID `json:"resolved_member_id"`
			ResolvedLoanID   *uuid.UUID `json:"resolved_loan_id"`
			Amount           *string    `json:"amount"`
			Status           string     `json:"status"`
			ErrorMessage     *string    `json:"error_message"`
			PostedTxnID      *uuid.UUID `json:"posted_txn_id"`
		}
		var rs []rowOut
		for rows.Next() {
			var ro rowOut
			if err := rows.Scan(&ro.ID, &ro.RowNo, &ro.MemberNoRaw, &ro.AmountRaw,
				&ro.ResolvedMemberID, &ro.ResolvedLoanID, &ro.Amount,
				&ro.Status, &ro.ErrorMessage, &ro.PostedTxnID); err != nil {
				return err
			}
			rs = append(rs, ro)
		}
		if err := rows.Err(); err != nil { return err }
		resp = map[string]any{"batch": batch, "rows": rs}
		return nil
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, resp)
}
