// Loans Phase 5 — group (org-as-borrower) loan applications.
//
//   POST /v1/loan-applications/group
//        {counterparty_id, product_id, requested_amount, requested_term_months,
//         group_income_source, officers: [{member_id, position}],
//         apportionment: [{member_id, share_pct}], purpose_note?}
//
//   POST /v1/loan-applications/{app_id}/group-officers/{consent_id}/respond
//        {decision: 'consented' | 'declined', decline_reason?}
//
//   GET  /v1/loan-applications/{app_id}/group-officers
//        Lists the officer consent rows.
//
//   GET  /v1/loans/{loan_id}/group-apportionment
//        Lists the apportionment for a disbursed group loan.
//
// Validation:
//   - counterparty_id must reference a non-individual counterparty.
//   - officers must contain at least one chair.
//   - apportionment share_pct must sum to exactly 100.00.
//   - Each apportionment member_id must be a real member.
//
// The existing CreateTx handles the schedule + scoring path; group
// apps just set applicant_kind='group' and rely on the trigger to
// enforce the apportionment sum at insert time.

package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type groupOfficer struct {
	MemberID uuid.UUID `json:"member_id"`
	Position string    `json:"position"` // chair | treasurer | secretary | signatory
}

type groupApportionment struct {
	MemberID uuid.UUID `json:"member_id"`
	SharePct string    `json:"share_pct"` // decimal as string for precision
}

type groupAppReq struct {
	CounterpartyID      uuid.UUID            `json:"counterparty_id"`
	ProductID           uuid.UUID            `json:"product_id"`
	RequestedAmount     string               `json:"requested_amount"`
	RequestedTermMonths int                  `json:"requested_term_months"`
	GroupIncomeSource   string               `json:"group_income_source"`
	PurposeNote         string               `json:"purpose_note"`
	Officers            []groupOfficer       `json:"officers"`
	Apportionment       []groupApportionment `json:"apportionment"`
}

func (h *LoanApplicationHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var in groupAppReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if in.CounterpartyID == uuid.Nil || in.ProductID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("counterparty_id and product_id required")); return
	}
	amt, err := decimal.NewFromString(in.RequestedAmount)
	if err != nil || !amt.IsPositive() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_amount must be positive")); return
	}
	if in.RequestedTermMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("requested_term_months must be > 0")); return
	}
	if len(in.Officers) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least one officer required")); return
	}
	hasChair := false
	for _, o := range in.Officers {
		if o.Position == "chair" { hasChair = true; break }
	}
	if !hasChair {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("officers must include at least one with position='chair'")); return
	}
	if len(in.Apportionment) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("apportionment list must contain at least one member")); return
	}
	var apportionmentSum decimal.Decimal
	for _, a := range in.Apportionment {
		v, err := decimal.NewFromString(a.SharePct)
		if err != nil || !v.IsPositive() || v.GreaterThan(decimal.NewFromInt(100)) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("each share_pct must be a positive decimal <= 100")); return
		}
		apportionmentSum = apportionmentSum.Add(v)
	}
	if !apportionmentSum.Equal(decimal.NewFromInt(100)) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("apportionment shares must sum to exactly 100.00 (got "+apportionmentSum.String()+")")); return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)

	// We stash the apportionment in the app's notes (json) AND emit
	// the officer consent rows. The trigger validates the sum on the
	// final loan_group_apportionment rows (inserted at disbursement
	// time — when a loan_id exists).
	var created *domain.LoanApplication
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Validate counterparty is institutional.
		var kind string
		if err := tx.QueryRow(r.Context(),
			`SELECT kind::text FROM counterparties WHERE id = $1`, in.CounterpartyID,
		).Scan(&kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrBadRequest("counterparty not found")
			}
			return err
		}
		if kind == "individual" {
			return httpx.ErrBadRequest("counterparty is individual; use POST /v1/loan-applications for individual borrowers")
		}

		// Encode apportionment as a JSON note so the disbursement-time
		// insert into loan_group_apportionment has a deterministic source.
		// Officers also stashed there.
		apportionmentNote, _ := encodeGroupExtras(in.Officers, in.Apportionment)
		notePtr := optStr(in.PurposeNote)
		groupSrc := optStr(in.GroupIncomeSource)

		app := &domain.LoanApplication{
			CounterpartyID:         in.CounterpartyID,
			ProductID:              in.ProductID,
			Status:                 domain.AppPendingScoring,
			RequestedAmount:        amt,
			RequestedTermMonths:    in.RequestedTermMonths,
			PurposeNote:            notePtr,
			ApplicantKind:          "group",
			BorrowerCounterpartyID: &in.CounterpartyID,
			GroupIncomeSource:      groupSrc,
			Notes:                  apportionmentNote,
			CreatedBy:              userID,
		}
		c, err := h.Applications.CreateTx(r.Context(), tx, app)
		if err != nil {
			return err
		}
		created = c
		// Emit officer consents.
		for _, o := range in.Officers {
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO loan_group_officer_consents (
				  tenant_id, application_id, officer_member_id, position
				) VALUES (current_tenant_id(), $1, $2, $3)
			`, created.ID, o.MemberID, o.Position); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil { writeApplicationErr(w, r, err); return }
	httpx.Created(w, created)
}

// encodeGroupExtras packages officers + apportionment into a tagged JSON
// string we stash in loan_applications.notes for later. We can't add
// real columns mid-PR without another migration; this is the pragmatic
// shape until Phase 6 splits them out.
func encodeGroupExtras(officers []groupOfficer, app []groupApportionment) (*string, error) {
	// Sentinel-prefixed JSON so callers can detect + parse without
	// confusing this with a free-form note.
	type payload struct {
		Officers      []groupOfficer       `json:"officers"`
		Apportionment []groupApportionment `json:"apportionment"`
	}
	b, err := json.Marshal(payload{Officers: officers, Apportionment: app})
	if err != nil {
		return nil, err
	}
	s := "[group-extras] " + string(b)
	return &s, nil
}

// ─────────── Officer consent response ───────────

type officerRespondReq struct {
	Decision      string `json:"decision"` // consented | declined
	DeclineReason string `json:"decline_reason"`
}

func (h *LoanApplicationHandler) RespondGroupOfficer(w http.ResponseWriter, r *http.Request) {
	consentID, err := uuid.Parse(chi.URLParam(r, "consent_id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid consent_id")); return }
	var in officerRespondReq
	if err := httpx.DecodeJSON(r, &in); err != nil { httpx.WriteErr(w, r, err); return }
	if in.Decision != "consented" && in.Decision != "declined" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decision must be consented or declined")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var declineReason *string
		if in.Decision == "declined" {
			r := in.DeclineReason
			if r == "" { r = "no reason provided" }
			declineReason = &r
		}
		tag, err := tx.Exec(r.Context(), `
			UPDATE loan_group_officer_consents
			   SET status = $2, responded_at = now(), decline_reason = $3
			 WHERE id = $1 AND status = 'pending_consent'
		`, consentID, in.Decision, declineReason)
		if err != nil { return err }
		if tag.RowsAffected() == 0 {
			return httpx.ErrConflict("consent row not found or already responded")
		}
		return nil
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	w.WriteHeader(http.StatusNoContent)
}

func (h *LoanApplicationHandler) ListGroupOfficers(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid app_id")); return }
	tid, _ := middleware.TenantIDFrom(r)
	type officerRow struct {
		ID             uuid.UUID  `json:"id"`
		OfficerMemberID uuid.UUID `json:"officer_member_id"`
		Position       string     `json:"position"`
		Status         string     `json:"status"`
		RespondedAt    *string    `json:"responded_at"`
		DeclineReason  *string    `json:"decline_reason"`
	}
	var out []officerRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, officer_member_id, position, status,
			       responded_at::text, decline_reason
			  FROM loan_group_officer_consents
			 WHERE application_id = $1
			 ORDER BY position, officer_member_id
		`, appID)
		if err != nil { return err }
		defer rows.Close()
		for rows.Next() {
			var o officerRow
			if err := rows.Scan(&o.ID, &o.OfficerMemberID, &o.Position, &o.Status,
				&o.RespondedAt, &o.DeclineReason); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

// ─────────── Group apportionment read ───────────

func (h *LoanApplicationHandler) GetGroupApportionment(w http.ResponseWriter, r *http.Request) {
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil { httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id")); return }
	tid, _ := middleware.TenantIDFrom(r)
	type apportionRow struct {
		MemberID uuid.UUID `json:"member_id"`
		SharePct string    `json:"share_pct"`
	}
	var out []apportionRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT member_id, share_pct::text
			  FROM loan_group_apportionment
			 WHERE loan_id = $1
			 ORDER BY share_pct DESC
		`, loanID)
		if err != nil { return err }
		defer rows.Close()
		for rows.Next() {
			var a apportionRow
			if err := rows.Scan(&a.MemberID, &a.SharePct); err != nil { return err }
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

