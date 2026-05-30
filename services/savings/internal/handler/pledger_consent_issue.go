// Phase 1.5b — third-party pledger consent token issuance + admin
// offline-record path.
//
// Reuses the guarantor-consent SMS template (tenant_operations.
// guarantor_sms_template) — pragmatic: the body is generic enough
// ("Hi {name}, {applicant} has asked you to … To respond: {link}")
// to cover either case, and SACCOs don't need separate copy.
//
// Token expiry, OTP attempts, public_base_url all read off the same
// tenant_operations columns set up in Phase 5 follow-up.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/filestore"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

// PledgerConsentHandler — admin-side endpoints: issue/resend SMS,
// record offline consent.
type PledgerConsentHandler struct {
	DB       *db.Pool
	Tokens   *store.PledgerConsentStore
	Files    *filestore.Store
	Notifier *notifier.Client
	Logger   *slog.Logger
}

// IssueForCollateral fires (or re-fires) a consent SMS to the
// pledger. Pre-condition: the collateral row's pledger_counterparty_id
// is set (i.e. it's a third-party pledge). Idempotent — re-calling
// creates a fresh token + new SMS.
//
// Mounted at POST /v1/collateral/{id}/pledger/issue (loans:apply perm).
func (h *PledgerConsentHandler) IssueForCollateral(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var attemptNo int
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var pledgerName, pledgerPhone, applicantName, productName, appNo string
		var pledgerCID uuid.UUID
		var requestedAmount, estimatedValue string
		var collateralKind, collateralDescription string
		if err := tx.QueryRow(r.Context(), `
			SELECT c.pledger_counterparty_id,
			       COALESCE(cd_p.full_name, ''),
			       COALESCE(m_p.phone, ''),
			       COALESCE(cd_a.full_name, ''),
			       p.name, a.application_no,
			       a.requested_amount::text,
			       c.estimated_value::text,
			       c.kind::text, c.description
			  FROM loan_collateral c
			  JOIN loan_applications a ON a.id = c.application_id
			  JOIN loan_products p ON p.id = a.product_id
			  LEFT JOIN counterparty_directory cd_p ON cd_p.counterparty_id = c.pledger_counterparty_id
			  LEFT JOIN members m_p ON m_p.id = cd_p.member_id
			  LEFT JOIN counterparty_directory cd_a ON cd_a.counterparty_id = a.counterparty_id
			 WHERE c.id = $1
		`, id).Scan(&pledgerCID, &pledgerName, &pledgerPhone, &applicantName,
			&productName, &appNo, &requestedAmount, &estimatedValue,
			&collateralKind, &collateralDescription); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound("collateral not found")
			}
			return err
		}
		if pledgerCID == uuid.Nil {
			return httpx.ErrBadRequest("collateral has no third-party pledger to notify")
		}

		// Load shared consent settings (same tenant_operations row that
		// powers the guarantor consent SMS flow).
		settings, serr := LoadConsentSettingsTx(r.Context(), tx, tid)
		if serr != nil {
			return serr
		}
		if !settings.Enabled {
			return httpx.ErrConflict("SMS consent is disabled for this tenant")
		}

		// Determine the next attempt number.
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(MAX(attempt_number), 0) + 1
			  FROM collateral_pledger_consent_tokens WHERE collateral_id = $1
		`, id).Scan(&attemptNo); err != nil {
			return err
		}

		plaintext, hash, terr := store.NewToken()
		if terr != nil {
			return terr
		}
		expiresAt := time.Now().UTC().Add(time.Duration(settings.ExpiryDays) * 24 * time.Hour)
		if _, terr := h.Tokens.CreateTx(r.Context(), tx, id, hash, expiresAt, attemptNo, uid); terr != nil {
			return terr
		}

		if pledgerPhone == "" {
			h.Logger.Warn("pledger SMS skipped: no phone on file", "collateral_id", id)
			return nil
		}
		publicBase := strings.TrimRight(settings.PublicBaseURL, "/")
		link := fmt.Sprintf("%s/g/pledger/%s", publicBase, plaintext)
		body := renderConsentTemplate(settings.Template, map[string]string{
			"guarantor_name":   pledgerName, // reuse template var names
			"applicant_name":   applicantName,
			"product_name":     productName,
			"amount":           estimatedValue,
			"requested_amount": requestedAmount,
			"link":             link,
			"tenant_name":      settings.TenantName,
			"expiry_days":      fmt.Sprintf("%d", settings.ExpiryDays),
			"token_short":      store.ShortRef(plaintext),
		})

		if h.Notifier != nil {
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:          tid,
				EventCode:         "GUARANTOR_CONSENT_REQUEST",
				Channels:          []notifier.Channel{notifier.ChannelSMS},
				RecipientMemberID: &pledgerCID,
				RecipientName:     pledgerName,
				RecipientPhone:    &pledgerPhone,
				SourceModule:      strPtrConsent("savings.loan_collateral.pledger"),
				SourceRecordID:    &id,
				Payload:           map[string]any{"body": body},
				InitiatedBy:       &uid,
			})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"attempt_number": attemptNo})
}

// AdminRecordOfflineConsent — multipart upload of a signed paper
// consent doc; flips pledger_consent_status → offline_consented.
// Mounted at POST /v1/collateral/{id}/pledger/offline-consent
// (loans:apply perm).
func (h *PledgerConsentHandler) AdminRecordOfflineConsent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("multipart parse failed: "+err.Error()))
		return
	}
	file, fileHeader, ferr := r.FormFile("file")
	if ferr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file is required"))
		return
	}
	defer file.Close()
	if fileHeader.Size > 5<<20 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file too large; 5 MB max"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	saved, err := h.Files.Save(tid, "pledger_consent",
		fileHeader.Filename, fileHeader.Header.Get("Content-Type"), file)
	if err != nil {
		h.Logger.Error("save pledger consent proof", "collateral_id", id, "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Tokens.AdminRecordOfflineConsentTx(r.Context(), tx, id, saved.StoragePath)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"status":   "offline_consented",
		"doc_path": saved.StoragePath,
	})
}
