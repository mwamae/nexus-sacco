// Loans Phase 6 — CRB pull endpoints.
//
//   POST /v1/loans/crb/pulls
//        {member_id, provider?, national_id?, application_id?,
//         consent_recorded, consent_signature_path?}
//
//        provider defaults to the tenant's active provider (the
//        most-recently-active row in crb_credentials). If no
//        credentials configured, falls back to the metropol stub so
//        sandbox tenants can exercise the flow.
//
//        Consent is mandatory: consent_recorded=false → 400.
//
//   GET  /v1/loans/crb/pulls?member_id=...
//        Lists recent pulls for a member; client-side decides whether
//        a recent (≤ 30d) pull is reusable.
//
// The pull writes the loan_applications.scoring_details JSONB if
// application_id is set so the existing /score endpoint can read the
// CRB report as one of its inputs.
//
// All Phase 6 adapters are stubs (services/savings/internal/crb).
// Real vendor calls are a per-vendor follow-up PR.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/crb"
	"github.com/nexussacco/savings/internal/cryptox"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type CRBHandler struct {
	DB     *db.Pool
	Sealer *cryptox.Sealer // nil → adapters always run with empty creds (stub-only)
	Logger *slog.Logger
}

// ─────────── Pull ───────────

type crbPullReq struct {
	MemberID              uuid.UUID  `json:"member_id"`
	Provider              string     `json:"provider"`       // metropol | transunion | crb_africa | "" for default
	ApplicationID         *uuid.UUID `json:"application_id"`
	NationalIDOverride    string     `json:"national_id"`    // optional; defaults to members.id_doc_number
	ConsentRecorded       bool       `json:"consent_recorded"`
	ConsentSignaturePath  string     `json:"consent_signature_path"`
	ForceFresh            bool       `json:"force_fresh"`    // bypass the 30-day reuse check
}

type crbPullResp struct {
	PullID    uuid.UUID   `json:"pull_id"`
	Provider  string      `json:"provider"`
	Score     int         `json:"score"`
	Rating    string      `json:"rating"`
	Listings  int         `json:"listings_count"`
	Enquiries int         `json:"enquiries_count"`
	Sandbox   bool        `json:"sandbox"`
	Reused    bool        `json:"reused"`        // true when an existing recent pull was returned
	Report    *crb.Report `json:"report"`
}

func (h *CRBHandler) Pull(w http.ResponseWriter, r *http.Request) {
	var in crbPullReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if in.MemberID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("member_id required")); return
	}
	if !in.ConsentRecorded {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("consent_recorded must be true; member consent is mandatory before a CRB pull")); return
	}
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required")); return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var out crbPullResp

	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Look up the member's national_id + full_name + phone.
		var nationalID, fullName, phone string
		if err := tx.QueryRow(r.Context(), `
			SELECT id_doc_number, full_name, COALESCE(phone, '')
			  FROM members WHERE id = $1
		`, in.MemberID).Scan(&nationalID, &fullName, &phone); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound("member not found")
			}
			return err
		}
		if in.NationalIDOverride != "" {
			nationalID = in.NationalIDOverride
		}

		providerCode := in.Provider
		if providerCode == "" {
			// Pick the tenant's most-recently-active provider; fall back
			// to metropol stub.
			err := tx.QueryRow(r.Context(), `
				SELECT provider::text FROM crb_credentials
				 WHERE active = true
				 ORDER BY effective_from DESC LIMIT 1
			`).Scan(&providerCode)
			if errors.Is(err, pgx.ErrNoRows) {
				providerCode = "metropol"
			} else if err != nil {
				return err
			}
		}

		// 30-day reuse check.
		if !in.ForceFresh {
			var existingID uuid.UUID
			err := tx.QueryRow(r.Context(), `
				SELECT id FROM crb_pulls
				 WHERE member_id = $1 AND provider = $2::crb_provider
				   AND pulled_at >= now() - interval '30 days'
				   AND status = 'success'
				 ORDER BY pulled_at DESC LIMIT 1
			`, in.MemberID, providerCode).Scan(&existingID)
			if err == nil {
				return loadExistingPullTx(r.Context(), tx, existingID, &out)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		// Resolve credentials (or use empty for stub-only).
		creds, err := loadCredsTx(r.Context(), tx, h.Sealer, providerCode)
		if err != nil {
			return err
		}
		provider, err := crb.NewProvider(providerCode, creds)
		if err != nil {
			return httpx.ErrBadRequest(err.Error())
		}

		// Fire the adapter.
		report, perr := provider.Pull(r.Context(), crb.PullInput{
			NationalID: nationalID, FullName: fullName, Phone: phone,
		})
		status := "success"
		errMsg := ""
		if perr != nil {
			status = "failed"
			errMsg = perr.Error()
		}

		// Persist the pull row.
		var rawResp []byte
		listingsCount := 0
		enquiriesCount := 0
		var score *int
		var rating *string
		var activeCredit, outstanding string = "0", "0"
		sandbox := false
		if report != nil {
			rawResp = report.RawResponse
			listingsCount = len(report.Listings)
			enquiriesCount = len(report.Enquiries)
			s := report.Score
			r := report.Rating
			score = &s
			rating = &r
			activeCredit = report.ActiveCredit.String()
			outstanding = report.OutstandingBalance.String()
			sandbox = report.Sandbox
		}
		if len(rawResp) == 0 {
			rawResp = []byte("{}")
		}

		var pullID uuid.UUID
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO crb_pulls (
			  tenant_id, provider, member_id, application_id, pulled_by,
			  consent_recorded, consent_signature_path,
			  request_payload, response_payload,
			  score, rating, listings_count, enquiries_count,
			  total_active_credit, outstanding_balance,
			  status, error_message, sandbox
			) VALUES (
			  current_tenant_id(), $1::crb_provider, $2, $3, $4,
			  $5, NULLIF($6, ''),
			  $7::jsonb, $8::jsonb,
			  $9, $10, $11, $12,
			  $13::numeric, $14::numeric,
			  $15, NULLIF($16, ''), $17
			)
			RETURNING id
		`,
			providerCode, in.MemberID, in.ApplicationID, uid,
			in.ConsentRecorded, in.ConsentSignaturePath,
			"{}", rawResp,
			score, rating, listingsCount, enquiriesCount,
			activeCredit, outstanding,
			status, errMsg, sandbox,
		).Scan(&pullID); err != nil {
			return err
		}

		// If linked to an application, stamp scoring_details + flags.
		if in.ApplicationID != nil && report != nil {
			scoringDetails := map[string]any{
				"crb_provider":   providerCode,
				"crb_score":      report.Score,
				"crb_rating":     report.Rating,
				"crb_listings":   len(report.Listings),
				"crb_enquiries":  len(report.Enquiries),
				"crb_pull_id":    pullID,
				"crb_pulled_at":  time.Now().UTC().Format(time.RFC3339),
				"crb_sandbox":    report.Sandbox,
			}
			scoringFlags := []string{}
			if len(report.Listings) > 0 {
				scoringFlags = append(scoringFlags, "adverse_listings")
			}
			if report.Rating == "D" || report.Rating == "E" {
				scoringFlags = append(scoringFlags, "low_rating")
			}
			detailsJSON, _ := json.Marshal(scoringDetails)
			flagsJSON, _ := json.Marshal(scoringFlags)
			if _, err := tx.Exec(r.Context(), `
				UPDATE loan_applications
				   SET scoring_details = COALESCE(scoring_details, '{}'::jsonb) || $2::jsonb,
				       scoring_flags   = $3::jsonb
				 WHERE id = $1
			`, *in.ApplicationID, detailsJSON, flagsJSON); err != nil {
				return err
			}
		}

		out.PullID = pullID
		out.Provider = providerCode
		out.Sandbox = sandbox
		out.Report = report
		if report != nil {
			out.Score = report.Score
			out.Rating = report.Rating
			out.Listings = len(report.Listings)
			out.Enquiries = len(report.Enquiries)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.Created(w, out)
}

func loadExistingPullTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, out *crbPullResp) error {
	var rawResp []byte
	var provider, rating string
	var score, listings, enquiries int
	var sandbox bool
	if err := tx.QueryRow(ctx, `
		SELECT provider::text, COALESCE(score, 0), COALESCE(rating, ''),
		       listings_count, enquiries_count, sandbox, response_payload
		  FROM crb_pulls WHERE id = $1
	`, id).Scan(&provider, &score, &rating, &listings, &enquiries, &sandbox, &rawResp); err != nil {
		return err
	}
	out.PullID = id
	out.Provider = provider
	out.Score = score
	out.Rating = rating
	out.Listings = listings
	out.Enquiries = enquiries
	out.Sandbox = sandbox
	out.Reused = true
	// Best-effort decode of raw response back into a Report shape.
	var rep crb.Report
	if err := json.Unmarshal(rawResp, &rep); err == nil {
		rep.Score = score
		rep.Rating = rating
		rep.Sandbox = sandbox
		out.Report = &rep
	}
	return nil
}

// loadCredsTx fetches the active credential row for a provider and
// decrypts it. Returns zero-value Creds when no row exists; stub
// adapters tolerate empty creds.
func loadCredsTx(ctx context.Context, tx pgx.Tx, sealer *cryptox.Sealer, provider string) (crb.Creds, error) {
	var ciphertext []byte
	var baseURL *string
	err := tx.QueryRow(ctx, `
		SELECT ciphertext, base_url FROM crb_credentials
		 WHERE provider = $1::crb_provider AND active = true
		 ORDER BY effective_from DESC LIMIT 1
	`, provider).Scan(&ciphertext, &baseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return crb.Creds{}, nil
	}
	if err != nil {
		return crb.Creds{}, err
	}
	if sealer == nil {
		// No sealer → can't decrypt; behave as if no creds (stub mode).
		c := crb.Creds{}
		if baseURL != nil {
			c.BaseURL = *baseURL
		}
		return c, nil
	}
	plaintext, err := sealer.Decrypt(ciphertext)
	if err != nil {
		return crb.Creds{}, err
	}
	var c crb.Creds
	if err := json.Unmarshal(plaintext, &c); err != nil {
		return crb.Creds{}, err
	}
	if baseURL != nil && c.BaseURL == "" {
		c.BaseURL = *baseURL
	}
	return c, nil
}

// ─────────── List ───────────

func (h *CRBHandler) ListByMember(w http.ResponseWriter, r *http.Request) {
	memberIDStr := r.URL.Query().Get("member_id")
	if memberIDStr == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("member_id query parameter required")); return
	}
	memberID, err := uuid.Parse(memberIDStr)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("member_id must be a UUID")); return
	}
	tid, _ := middleware.TenantIDFrom(r)
	type pullRow struct {
		ID             uuid.UUID `json:"id"`
		Provider       string    `json:"provider"`
		PulledAt       time.Time `json:"pulled_at"`
		Score          *int      `json:"score"`
		Rating         *string   `json:"rating"`
		ListingsCount  int       `json:"listings_count"`
		EnquiriesCount int       `json:"enquiries_count"`
		Status         string    `json:"status"`
		Sandbox        bool      `json:"sandbox"`
	}
	var out []pullRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, provider::text, pulled_at, score, rating,
			       listings_count, enquiries_count, status, sandbox
			  FROM crb_pulls
			 WHERE member_id = $1
			 ORDER BY pulled_at DESC
			 LIMIT 50
		`, memberID)
		if err != nil { return err }
		defer rows.Close()
		for rows.Next() {
			var p pullRow
			if err := rows.Scan(&p.ID, &p.Provider, &p.PulledAt, &p.Score, &p.Rating,
				&p.ListingsCount, &p.EnquiriesCount, &p.Status, &p.Sandbox); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil { httpx.WriteErr(w, r, err); return }
	httpx.OK(w, map[string]any{"items": out, "total": len(out)})
}
