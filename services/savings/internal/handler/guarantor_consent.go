// Guarantor consent endpoints — admin-captured + portal self-service.
//
//   POST /v1/loan-guarantees/{guarantee_id}/respond-with-proof
//        Admin-captured consent. Multipart form:
//          accept           = "true" | "false"
//          decline_reason   = string (required when accept=false)
//          file             = the signed-consent document (optional
//                              for declines; recommended for accepts)
//          note             = free-form text (optional)
//        File is stored under <tenant>/guarantor_consent/<uuid><ext>,
//        a loan_documents row is created with kind='guarantor_consent_proof'
//        and tied to the guarantee's application_id, and loan_guarantees
//        gets proof_document_id + responded_by stamped.
//        Permission: loans:guarantee:admin (new; granted to credit_officer
//                                            + above)
//
//   GET  /v1/portal/guarantorships
//   POST /v1/portal/guarantorships/{guarantee_id}/respond
//        Member-portal self-service. Body for POST:
//          {accept: bool, decline_reason?: string}
//        The JWT's user is bridged to a counterparty; the endpoint
//        refuses any guarantee that doesn't name the bridged
//        counterparty as the guarantor.
//        Permission: portal:self
//
// Loan_documents helper is shared with the existing GenerateLetter
// path (Phase 4) — same table, same shape.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/filestore"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type GuarantorConsentHandler struct {
	DB         *db.Pool
	Guarantees *store.LoanGuaranteeStore
	Files      *filestore.Store
	Logger     *slog.Logger
}

// ─────────── Admin: respond-with-proof ───────────

func (h *GuarantorConsentHandler) AdminRespond(w http.ResponseWriter, r *http.Request) {
	gID, err := uuid.Parse(chi.URLParam(r, "guarantee_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid guarantee_id"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	// 5 MB cap — consent proofs are typically a phone-camera shot or
	// a small PDF. Larger uploads almost certainly indicate the
	// wrong file was selected.
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("multipart parse failed: "+err.Error()))
		return
	}
	acceptStr := strings.ToLower(strings.TrimSpace(r.FormValue("accept")))
	if acceptStr != "true" && acceptStr != "false" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("accept must be 'true' or 'false'"))
		return
	}
	accept := acceptStr == "true"
	declineReason := strings.TrimSpace(r.FormValue("decline_reason"))
	note := strings.TrimSpace(r.FormValue("note"))

	if !accept && declineReason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decline_reason is required when accept=false"))
		return
	}

	// File is optional but strongly recommended for accepts. We
	// allow accept without file in dev / for tenants that record
	// consent verbally on call (the `note` field captures that).
	var docID *uuid.UUID
	file, fileHeader, ferr := r.FormFile("file")
	if ferr == nil {
		defer file.Close()
		if fileHeader.Size > 5<<20 {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("file too large; 5 MB max"))
			return
		}
		// Need the guarantee's application_id to tie the document
		// row to the right application. Pre-load.
		var appID uuid.UUID
		err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(),
				`SELECT application_id FROM loan_guarantees WHERE id = $1`, gID,
			).Scan(&appID)
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteErr(w, r, httpx.ErrNotFound("guarantee not found"))
				return
			}
			httpx.WriteErr(w, r, err)
			return
		}
		saved, err := h.Files.Save(tid, "guarantor_consent",
			fileHeader.Filename, fileHeader.Header.Get("Content-Type"), file)
		if err != nil {
			h.Logger.Error("save consent proof", "guarantee_id", gID, "err", err)
			httpx.WriteErr(w, r, httpx.ErrInternal())
			return
		}
		// Persist loan_documents row.
		err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			desc := "Guarantor consent proof"
			if note != "" {
				desc += ": " + note
			}
			var newDocID uuid.UUID
			if err := tx.QueryRow(r.Context(), `
				INSERT INTO loan_documents (
				  tenant_id, application_id, kind, description,
				  storage_path, mime, size_bytes, uploaded_by
				) VALUES (
				  current_tenant_id(), $1, 'guarantor_consent_proof'::loan_doc_kind, $2,
				  $3, $4, $5, $6
				)
				RETURNING id
			`, appID, desc, saved.StoragePath, saved.MimeType, saved.Size, uid).Scan(&newDocID); err != nil {
				return err
			}
			docID = &newDocID
			return nil
		})
		if err != nil {
			httpx.WriteErr(w, r, err); return
		}
	}

	// Run the actual respond (separate tx so the file/doc insert above
	// is durable even if the respond fails — admins can retry without
	// re-uploading).
	var declinePtr *string
	if !accept && declineReason != "" {
		declinePtr = &declineReason
	}
	var resp *domain.LoanGuarantee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		resp, err = h.Guarantees.RespondTx(r.Context(), tx, gID, accept, declinePtr, docID, uid)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, resp)
}

// ─────────── Member portal: list + respond ───────────

func (h *GuarantorConsentHandler) PortalList(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	cpID, err := h.bridgedCounterpartyID(r)
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	type row struct {
		ID                uuid.UUID `json:"id"`
		ApplicationID     uuid.UUID `json:"application_id"`
		ApplicationNo     string    `json:"application_no"`
		BorrowerName      string    `json:"borrower_name"`
		BorrowerMemberNo  string    `json:"borrower_member_no"`
		AmountGuaranteed  string    `json:"amount_guaranteed"`
		RequestedAmount   string    `json:"requested_amount"`
		ProductName       string    `json:"product_name"`
		Status            string    `json:"status"`
		RequestedAt       string    `json:"requested_at"`
		RespondedAt       *string   `json:"responded_at,omitempty"`
		DeclineReason     *string   `json:"decline_reason,omitempty"`
	}
	var out []row
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT g.id, g.application_id, a.application_no,
			       cd.full_name, cd.member_no,
			       g.amount_guaranteed::text, a.requested_amount::text,
			       p.name, g.status::text,
			       g.requested_at::text, g.responded_at::text, g.decline_reason
			  FROM loan_guarantees g
			  JOIN loan_applications a ON a.id = g.application_id
			  JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			  JOIN loan_products p ON p.id = a.product_id
			 WHERE g.guarantor_counterparty_id = $1
			 ORDER BY (g.status = 'pending_consent') DESC, g.requested_at DESC
		`, cpID)
		if err != nil { return err }
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(
				&rr.ID, &rr.ApplicationID, &rr.ApplicationNo,
				&rr.BorrowerName, &rr.BorrowerMemberNo,
				&rr.AmountGuaranteed, &rr.RequestedAmount,
				&rr.ProductName, &rr.Status,
				&rr.RequestedAt, &rr.RespondedAt, &rr.DeclineReason,
			); err != nil {
				return err
			}
			out = append(out, rr)
		}
		return rows.Err()
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}

type portalRespondReq struct {
	Accept        bool   `json:"accept"`
	DeclineReason string `json:"decline_reason"`
}

func (h *GuarantorConsentHandler) PortalRespond(w http.ResponseWriter, r *http.Request) {
	gID, err := uuid.Parse(chi.URLParam(r, "guarantee_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid guarantee_id")); return
	}
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	cpID, err := h.bridgedCounterpartyID(r)
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	var in portalRespondReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if !in.Accept && strings.TrimSpace(in.DeclineReason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decline_reason is required when accept=false"))
		return
	}

	// Authorisation check: the guarantee must name the bridged
	// counterparty. Without this, a logged-in member could respond
	// to anyone's guarantee.
	var owner uuid.UUID
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(),
			`SELECT guarantor_counterparty_id FROM loan_guarantees WHERE id = $1`, gID,
		).Scan(&owner)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("guarantee not found"))
			return
		}
		httpx.WriteErr(w, r, err); return
	}
	if owner != cpID {
		httpx.WriteErr(w, r, httpx.ErrForbidden("this guarantee is not addressed to you"))
		return
	}

	var declinePtr *string
	if !in.Accept {
		dr := strings.TrimSpace(in.DeclineReason)
		declinePtr = &dr
	}
	var resp *domain.LoanGuarantee
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		resp, err = h.Guarantees.RespondTx(r.Context(), tx, gID, in.Accept, declinePtr, nil, uid)
		return err
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, resp)
}

// bridgedCounterpartyID returns the counterparty id the authenticated
// user maps to via the email/phone bridge. Returns 403 when the user
// can't be mapped — institutional logins, staff accounts that aren't
// members, etc. (this endpoint is for borrowers only).
//
// The bridge is the same pattern insider-detection uses.
func (h *GuarantorConsentHandler) bridgedCounterpartyID(r *http.Request) (uuid.UUID, error) {
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		return uuid.Nil, httpx.ErrUnauthorized("user identity required")
	}
	tid, _ := middleware.TenantIDFrom(r)
	var cpID uuid.UUID
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT m.counterparty_id
			  FROM users u
			  JOIN members m ON m.tenant_id = u.tenant_id
			   AND (m.email = u.email OR (m.phone IS NOT NULL AND m.phone = u.phone))
			 WHERE u.id = $1
			 LIMIT 1
		`, uid).Scan(&cpID)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, httpx.ErrForbidden("your account is not linked to a member profile")
		}
		return uuid.Nil, fmt.Errorf("bridge user→member: %w", err)
	}
	return cpID, nil
}

