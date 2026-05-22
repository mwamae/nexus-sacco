// Organisation onboarding handlers. Mirrors the individual-member
// onboarding surface but with the six domains (profile / documents /
// officials / signatories / banking / contacts) we need for non-individual
// KYC.

package handler

import (
	"context"
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

	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/notifier"
	"github.com/nexussacco/member/internal/storage"
	"github.com/nexussacco/member/internal/store"
)

type OrgHandler struct {
	DB             *db.Pool
	Orgs           *store.OrgMemberStore
	Documents      *store.OrgDocumentStore
	Officials      *store.OrgOfficialStore
	Signatories    *store.OrgSignatoryStore
	Banking        *store.OrgBankingStore
	Contacts       *store.OrgContactStore
	Audit          *store.AuditStore
	Counterparties *store.CounterpartyStore // Phase A dual-target mirror
	Storage        storage.Storage
	MaxUpload      int64
	Logger         *slog.Logger
	Notifier    *notifier.Client
}

func (h *OrgHandler) audit(r *http.Request, tenantID, orgID uuid.UUID, action string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenantID, ActorID: nonZero(actorID),
		Action: action, TargetKind: "org", TargetID: orgID.String(),
		UserAgent: r.UserAgent(), Metadata: meta,
	})
}

// ─────────── POST /v1/orgs ───────────
//
// Creates the org + officials + signatories + banking + contacts in
// one atomic transaction so the wizard's final submit is one POST.

type officialDTO struct {
	FullName          string   `json:"full_name"`
	IDDocKind         string   `json:"id_doc_kind"`
	IDDocNumber       string   `json:"id_doc_number"`
	KraPIN            string   `json:"kra_pin"`
	DateOfBirth       string   `json:"date_of_birth"`
	Gender            string   `json:"gender"`
	Nationality       string   `json:"nationality"`
	Phone             string   `json:"phone"`
	Email             string   `json:"email"`
	PhysicalAddress   string   `json:"physical_address"`
	Occupation        string   `json:"occupation"`
	Position          string   `json:"position"`
	PositionLabel     string   `json:"position_label"`
	AppointedOn       string   `json:"appointed_on"`
	IsPEP             bool     `json:"is_pep"`
	PEPNote           string   `json:"pep_note"`
	IsBeneficialOwner bool     `json:"is_beneficial_owner"`
	OwnershipPercent  *float64 `json:"ownership_percent"`
	// Signatory shorthand: when present, a signatory row is created
	// referencing this official.
	Signatory *struct {
		Class        string   `json:"class"`
		SigningOrder int      `json:"signing_order"`
		TxnLimit     *float64 `json:"txn_limit"`
	} `json:"signatory"`
}

type contactDTO struct {
	Kind     string `json:"kind"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
}

type createOrgRequest struct {
	RegisteredName     string  `json:"registered_name"`
	TradingName        string  `json:"trading_name"`
	Kind               string  `json:"kind"`
	RegistrationNo     string  `json:"registration_no"`
	DateOfRegistration string  `json:"date_of_registration"`
	DateOfOperation    string  `json:"date_of_operation"`
	Industry           string  `json:"industry"`
	NatureOfBusiness   string  `json:"nature_of_business"`
	MemberCount        *int    `json:"member_count"`
	EmployeeCount      *int    `json:"employee_count"`

	PhysicalAddress string     `json:"physical_address"`
	PostalAddress   string     `json:"postal_address"`
	County          string     `json:"county"`
	SubCounty       string     `json:"sub_county"`
	Ward            string     `json:"ward"`
	GPSLat          *float64   `json:"gps_lat"`
	GPSLng          *float64   `json:"gps_lng"`
	BranchID        *uuid.UUID `json:"branch_id"`

	RiskCategory string `json:"risk_category"`

	Officials []officialDTO  `json:"officials"`
	Banking   *domain.Banking `json:"banking"`
	Contacts  []contactDTO   `json:"contacts"`
	Mandate   map[string]any `json:"mandate"`
}

func (h *OrgHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var req createOrgRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.RegisteredName = strings.TrimSpace(req.RegisteredName)
	if req.RegisteredName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("registered_name is required"))
		return
	}
	kind, err := parseOrgKind(req.Kind)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	dor, err := parseDate(req.DateOfRegistration)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	doo, err := parseDate(req.DateOfOperation)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	risk := domain.RiskCategory(strings.ToLower(strings.TrimSpace(req.RiskCategory)))
	switch risk {
	case "", domain.RiskLow, domain.RiskMedium, domain.RiskHigh:
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid risk_category"))
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	var created *domain.Org
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		orgNo, err := h.Orgs.NextOrgNoTx(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		o, err := h.Orgs.CreateTx(r.Context(), tx, store.CreateOrgInput{
			TenantID:           tenantID,
			RegisteredName:     req.RegisteredName,
			TradingName:        strings.TrimSpace(req.TradingName),
			Kind:               kind,
			RegistrationNo:     strings.TrimSpace(req.RegistrationNo),
			DateOfRegistration: dor,
			DateOfOperation:    doo,
			Industry:           strings.TrimSpace(req.Industry),
			NatureOfBusiness:   strings.TrimSpace(req.NatureOfBusiness),
			MemberCount:        req.MemberCount,
			EmployeeCount:      req.EmployeeCount,
			PhysicalAddress:    strings.TrimSpace(req.PhysicalAddress),
			PostalAddress:      strings.TrimSpace(req.PostalAddress),
			County:             strings.TrimSpace(req.County),
			SubCounty:          strings.TrimSpace(req.SubCounty),
			Ward:               strings.TrimSpace(req.Ward),
			GPSLat:             req.GPSLat,
			GPSLng:             req.GPSLng,
			BranchID:           req.BranchID,
			RiskCategory:       risk,
			CreatedBy:          nonZero(actorID),
		}, orgNo)
		if err != nil {
			return err
		}

		// Officials + signatories (signatory rows reference the official ids
		// we just created, so build the list as we go).
		var sigs []store.SignatoryInput
		for i, od := range req.Officials {
			if strings.TrimSpace(od.FullName) == "" || strings.TrimSpace(od.IDDocNumber) == "" {
				continue
			}
			official, err := h.buildOfficial(r.Context(), tx, tenantID, o.ID, od, i)
			if err != nil {
				return err
			}
			if od.Signatory != nil {
				class, err := parseSignatoryClass(od.Signatory.Class)
				if err != nil {
					return err
				}
				sigs = append(sigs, store.SignatoryInput{
					OfficialID:   official.ID,
					Class:        class,
					SigningOrder: od.Signatory.SigningOrder,
					TxnLimit:     od.Signatory.TxnLimit,
				})
			}
		}
		if len(sigs) > 0 {
			if err := h.Signatories.ReplaceTx(r.Context(), tx, tenantID, o.ID, sigs); err != nil {
				return err
			}
		}
		if len(req.Mandate) > 0 {
			if err := h.Signatories.UpsertMandateTx(r.Context(), tx, tenantID, o.ID, req.Mandate); err != nil {
				return err
			}
		}

		// Banking (1:1, only if any field present).
		if req.Banking != nil && !bankingEmpty(*req.Banking) {
			b := *req.Banking
			b.OrgID = o.ID
			if _, err := h.Banking.UpsertTx(r.Context(), tx, tenantID, b); err != nil {
				return err
			}
		}

		// Contacts.
		var contacts []store.ContactInput
		for _, c := range req.Contacts {
			ck, err := parseContactKind(c.Kind)
			if err != nil {
				return err
			}
			contacts = append(contacts, store.ContactInput{
				Kind: ck, FullName: c.FullName, Role: c.Role, Phone: c.Phone, Email: c.Email,
			})
		}
		if len(contacts) > 0 {
			if err := h.Contacts.ReplaceTx(r.Context(), tx, tenantID, o.ID, contacts); err != nil {
				return err
			}
		}

		created = o

		// Dual-target mirror — same shape as MemberHandler.Create.
		if h.Counterparties != nil {
			if err := mirrorOrgCreateToCounterpartyTx(
				r.Context(), tx, h.Counterparties, tenantID, o, actorID,
			); err != nil {
				return fmt.Errorf("mirror counterparty: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("an organisation with that key already exists"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, created.ID, "org.created", map[string]any{
		"org_no": created.OrgNo, "kind": string(created.Kind),
	})
	httpx.Created(w, created)
}

func (h *OrgHandler) buildOfficial(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID, od officialDTO, order int) (*domain.Official, error) {
	idKind, err := parseIDKind(od.IDDocKind)
	if err != nil {
		return nil, err
	}
	gender, err := parseGender(od.Gender)
	if err != nil {
		return nil, err
	}
	dob, err := parseDate(od.DateOfBirth)
	if err != nil {
		return nil, err
	}
	app, err := parseDate(od.AppointedOn)
	if err != nil {
		return nil, err
	}
	pos, err := parseOfficialPosition(od.Position)
	if err != nil {
		return nil, err
	}
	return h.Officials.CreateTx(ctx, tx, store.CreateOfficialInput{
		OrgID: orgID, TenantID: tenantID,
		FullName: strings.TrimSpace(od.FullName), IDDocKind: idKind, IDDocNumber: strings.TrimSpace(od.IDDocNumber),
		KraPIN: strings.ToUpper(strings.TrimSpace(od.KraPIN)), DateOfBirth: dob,
		Gender: gender, Nationality: strings.TrimSpace(od.Nationality),
		Phone: strings.TrimSpace(od.Phone), Email: strings.ToLower(strings.TrimSpace(od.Email)),
		PhysicalAddress: strings.TrimSpace(od.PhysicalAddress), Occupation: strings.TrimSpace(od.Occupation),
		Position: pos, PositionLabel: strings.TrimSpace(od.PositionLabel), AppointedOn: app,
		IsPEP: od.IsPEP, PEPNote: strings.TrimSpace(od.PEPNote),
		IsBeneficialOwner: od.IsBeneficialOwner, OwnershipPercent: od.OwnershipPercent,
		PositionOrder: order,
	})
}

// ─────────── GET /v1/orgs ───────────

func (h *OrgHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	in := store.ListOrgsInput{
		Status: domain.OrgStatus(strings.TrimSpace(q.Get("status"))),
		Kind:   domain.OrgKind(strings.TrimSpace(q.Get("kind"))),
		Query:  strings.TrimSpace(q.Get("q")),
		Limit:  limit, Offset: offset,
	}

	var result *store.ListOrgsResult
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		result, err = h.Orgs.ListTx(r.Context(), tx, in)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if result.Orgs == nil {
		result.Orgs = []*domain.Org{}
	}
	httpx.OK(w, result)
}

// ─────────── GET /v1/orgs/{id} ───────────

type orgDetail struct {
	*domain.Org
	Documents   []*domain.OrgDocument `json:"documents"`
	Officials   []*domain.Official    `json:"officials"`
	Signatories []*domain.Signatory   `json:"signatories"`
	Mandate     *domain.Mandate       `json:"mandate,omitempty"`
	Banking     *domain.Banking       `json:"banking,omitempty"`
	Contacts    []*domain.Contact     `json:"contacts"`
	// Phase B bridge fields. Same shape as memberDetail's so the FE
	// can render the CP-* + legacy id header consistently across the
	// individual + institutional detail pages.
	CPNumber       *string `json:"cp_number,omitempty"`
	CounterpartyID *string `json:"counterparty_id,omitempty"`
}

func (h *OrgHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var out orgDetail
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		o, err := h.Orgs.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out.Org = o
		if out.Documents, err = h.Documents.ListForOrgTx(r.Context(), tx, id); err != nil {
			return err
		}
		if out.Officials, err = h.Officials.ListForOrgTx(r.Context(), tx, id); err != nil {
			return err
		}
		if out.Signatories, err = h.Signatories.ListForOrgTx(r.Context(), tx, id); err != nil {
			return err
		}
		if out.Mandate, err = h.Signatories.GetMandateTx(r.Context(), tx, id); err != nil {
			return err
		}
		if out.Banking, err = h.Banking.ForOrgTx(r.Context(), tx, id); err != nil {
			return err
		}
		if out.Contacts, err = h.Contacts.ListForOrgTx(r.Context(), tx, id); err != nil {
			return err
		}
		// Same bridge LEFT JOIN as memberDetail.
		var cpID, cpNo *string
		_ = tx.QueryRow(r.Context(), `
			SELECT c.id::text, c.cp_number
			  FROM counterparties c
			  JOIN org_members o ON o.counterparty_id = c.id
			 WHERE o.id = $1
		`, id).Scan(&cpID, &cpNo)
		out.CounterpartyID = cpID
		out.CPNumber = cpNo
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("organisation not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if out.Documents == nil {
		out.Documents = []*domain.OrgDocument{}
	}
	if out.Officials == nil {
		out.Officials = []*domain.Official{}
	}
	if out.Signatories == nil {
		out.Signatories = []*domain.Signatory{}
	}
	if out.Contacts == nil {
		out.Contacts = []*domain.Contact{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/orgs/{id}/approve|reject|status ───────────

func (h *OrgHandler) Approve(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Orgs.ApproveTx(r.Context(), tx, id, actorID)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("org not found or not pending"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "org.approved", nil)
	httpx.NoContent(w)
}

type rejectOrgReq struct {
	Reason string `json:"reason"`
}

func (h *OrgHandler) Reject(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req rejectOrgReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Orgs.RejectTx(r.Context(), tx, id, actorID, req.Reason)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("org not found or not pending"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "org.rejected", map[string]any{"reason": req.Reason})
	httpx.NoContent(w)
}

type setOrgStatusReq struct {
	Status string `json:"status"`
}

func (h *OrgHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req setOrgStatusReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	next := domain.OrgStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	switch next {
	case domain.OrgActive, domain.OrgSuspended, domain.OrgClosed, domain.OrgDormant:
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be active/suspended/closed/dormant"))
		return
	}
	var fromStatus domain.OrgStatus
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		o, err := h.Orgs.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if o.Status == domain.OrgPending {
			return httpx.ErrBadRequest("pending orgs must use /approve or /reject")
		}
		if o.Status == next {
			return nil
		}
		fromStatus = o.Status
		return h.Orgs.SetStatusTx(r.Context(), tx, id, next)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("org not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if fromStatus != "" {
		h.audit(r, tenantID, id, "org.status_changed", map[string]any{"from": string(fromStatus), "to": string(next)})
	}
	httpx.NoContent(w)
}

type setKYCStatusReq struct {
	Status string `json:"status"`
}

func (h *OrgHandler) SetKYCStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req setKYCStatusReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	next := domain.KYCReviewStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	switch next {
	case domain.KYCNotStarted, domain.KYCInReview, domain.KYCVerified, domain.KYCRejected:
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid kyc status"))
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Orgs.SetKYCStatusTx(r.Context(), tx, id, next)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("org not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "org.kyc_status_changed", map[string]any{"to": string(next)})

	// KYC_APPROVED / KYC_REJECTED fire when an org reaches a terminal
	// KYC state. We notify the primary contact (if known) so the org
	// can begin / re-attempt onboarding.
	if h.Notifier != nil && (next == domain.KYCVerified || next == domain.KYCRejected) {
		eventCode := "KYC_APPROVED"
		if next == domain.KYCRejected {
			eventCode = "KYC_REJECTED"
		}
		var org *domain.Org
		_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			var err error
			org, err = h.Orgs.ByIDTx(r.Context(), tx, id)
			return err
		})
		if org != nil {
			actorID, _ := middleware.UserIDFrom(r)
			sourceModule := "member.org_kyc"
			recordID := org.ID
			deepLink := "/orgs/" + org.ID.String()
			h.Notifier.Notify(r.Context(), notifier.Request{
				TenantID:       tenantID,
				EventCode:      eventCode,
				RecipientName:  org.RegisteredName,
				SourceModule:   &sourceModule,
				SourceRecordID: &recordID,
				DeepLink:       &deepLink,
				InitiatedBy:    nonZero(actorID),
				Payload: map[string]any{
					"org_no":          org.OrgNo,
					"registered_name": org.RegisteredName,
					"kyc_status":      string(next),
				},
			})
		}
	}
	httpx.NoContent(w)
}

// ─────────── Documents ───────────

func (h *OrgHandler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	kind, err := parseOrgDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	q := r.URL.Query()
	issueDate, err := parseDate(q.Get("issue_date"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	expiryDate, err := parseDate(q.Get("expiry_date"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.MaxUpload+1024)
	if err := r.ParseMultipartForm(h.MaxUpload); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid upload: "+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing 'file' field"))
		return
	}
	defer file.Close()
	if header.Size > h.MaxUpload {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file too large"))
		return
	}
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	if !isAllowedOrgDocMIME(mime) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("disallowed file type"))
		return
	}

	// Confirm the org exists in this tenant before writing to storage.
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := h.Orgs.ByIDTx(r.Context(), tx, id)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("org not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	path, size, err := h.Storage.Save(tenantID, id, "orgs/docs/"+string(kind), mime, file, header.Size)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	var doc *domain.OrgDocument
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		d, err := h.Documents.UpsertTx(r.Context(), tx, store.CreateOrgDocumentInput{
			OrgID: id, TenantID: tenantID, Kind: kind,
			StoragePath: path, MIME: mime, SizeBytes: size,
			IssueDate: issueDate, ExpiryDate: expiryDate,
			UploadedBy: nonZero(actorID),
		})
		if err != nil {
			return err
		}
		doc = d
		return nil
	})
	if err != nil {
		_ = h.Storage.Delete(path)
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "org.document_uploaded", map[string]any{
		"kind": string(kind), "mime": mime, "size": size,
		"expiry": stringOrEmpty(expiryDate),
	})
	httpx.Created(w, doc)
}

func (h *OrgHandler) DownloadDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	kind, err := parseOrgDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var doc *domain.OrgDocument
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		d, err := h.Documents.ByKindTx(r.Context(), tx, id, kind)
		if err != nil {
			return err
		}
		doc = d
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("document not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	f, err := h.Storage.Open(doc.StoragePath)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", doc.MIME)
	w.Header().Set("Content-Length", strconv.FormatInt(doc.SizeBytes, 10))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, f)
}

type verifyDocReq struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

func (h *OrgHandler) VerifyDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	kind, err := parseOrgDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var req verifyDocReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	status := domain.DocVerification(strings.ToLower(strings.TrimSpace(req.Status)))
	switch status {
	case domain.VerifyPending, domain.VerifyVerified, domain.VerifyRejected:
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be pending/verified/rejected"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		doc, err := h.Documents.ByKindTx(r.Context(), tx, id, kind)
		if err != nil {
			return err
		}
		return h.Documents.SetVerificationTx(r.Context(), tx, doc.ID, status, actorID, req.Note)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("document not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "org.document_verified", map[string]any{
		"kind": string(kind), "status": string(status), "note": req.Note,
	})
	httpx.NoContent(w)
}

// ─────────── Officials sub-routes ───────────

func (h *OrgHandler) AddOfficial(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var od officialDTO
	if err := httpx.DecodeJSON(r, &od); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(od.FullName) == "" || strings.TrimSpace(od.IDDocNumber) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("full_name and id_doc_number are required"))
		return
	}
	var official *domain.Official
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		existing, err := h.Officials.ListForOrgTx(r.Context(), tx, orgID)
		if err != nil {
			return err
		}
		official, err = h.buildOfficial(r.Context(), tx, tenantID, orgID, od, len(existing))
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.official_added", map[string]any{
		"official_id": official.ID, "name": official.FullName, "position": string(official.Position),
	})
	httpx.Created(w, official)
}

func (h *OrgHandler) DeleteOfficial(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	officialID, err := uuid.Parse(chi.URLParam(r, "official_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid official id"))
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Officials.DeleteTx(r.Context(), tx, officialID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.official_removed", map[string]any{"official_id": officialID})
	httpx.NoContent(w)
}

type sanctionsReq struct {
	Hit  bool   `json:"hit"`
	Note string `json:"note"`
}

func (h *OrgHandler) ScreenOfficial(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	officialID, err := uuid.Parse(chi.URLParam(r, "official_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid official id"))
		return
	}
	var req sanctionsReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Officials.SetSanctionsScreenedTx(r.Context(), tx, officialID, actorID, req.Hit, req.Note)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("official not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.official_sanctions_screened", map[string]any{
		"official_id": officialID, "hit": req.Hit, "note": req.Note,
	})
	httpx.NoContent(w)
}

// POST /v1/orgs/{id}/officials/{official_id}/files/{kind}
//
// Uploads one of the per-official files (passport_photo / signature /
// id_copy / kra_pin_certificate). Storage path is derived by convention;
// metadata is persisted into the official's `files` JSONB.

func (h *OrgHandler) UploadOfficialFile(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	officialID, err := uuid.Parse(chi.URLParam(r, "official_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid official id"))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	switch kind {
	case "passport_photo", "signature", "id_copy", "kra_pin_certificate":
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file kind must be passport_photo, signature, id_copy, or kra_pin_certificate"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.MaxUpload+1024)
	if err := r.ParseMultipartForm(h.MaxUpload); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid upload: "+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing 'file' field"))
		return
	}
	defer file.Close()
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	if !isAllowedOrgDocMIME(mime) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("disallowed file type"))
		return
	}

	path, size, err := h.Storage.Save(tenantID, orgID, "officials/"+officialID.String()+"/"+kind, mime, file, header.Size)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	var updated *domain.Official
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		o, err := h.Officials.ByIDTx(r.Context(), tx, officialID)
		if err != nil {
			return err
		}
		if o.Files == nil {
			o.Files = domain.OfficialFiles{}
		}
		o.Files[kind] = struct {
			MIME      string    `json:"mime"`
			Size      int64     `json:"size"`
			UpdatedAt time.Time `json:"updated_at"`
		}{MIME: mime, Size: size, UpdatedAt: time.Now().UTC()}
		if err := h.Officials.SetFilesTx(r.Context(), tx, officialID, o.Files); err != nil {
			return err
		}
		updated = o
		return nil
	})
	if err != nil {
		_ = h.Storage.Delete(path)
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.official_file_uploaded", map[string]any{
		"official_id": officialID, "kind": kind, "mime": mime, "size": size,
	})
	httpx.Created(w, updated)
}

func (h *OrgHandler) DownloadOfficialFile(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	officialID, err := uuid.Parse(chi.URLParam(r, "official_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid official id"))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	var off *domain.Official
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		off, err = h.Officials.ByIDTx(r.Context(), tx, officialID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("official not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	meta, ok := off.Files[kind]
	if !ok {
		httpx.WriteErr(w, r, httpx.ErrNotFound("file not found"))
		return
	}
	// Derive the storage path the same way Save() did.
	path := tenantID.String() + "/" + orgID.String() + "/officials/" + officialID.String() + "/" + kind + extFromMIMEv2(meta.MIME)
	f, err := h.Storage.Open(path)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", meta.MIME)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, f)
	_ = orgID // referenced via path
}

// ─────────── Signatories + Mandate ───────────

type signatoryReplaceReq struct {
	Signatories []struct {
		OfficialID   uuid.UUID `json:"official_id"`
		Class        string    `json:"class"`
		SigningOrder int       `json:"signing_order"`
		TxnLimit     *float64  `json:"txn_limit"`
	} `json:"signatories"`
}

func (h *OrgHandler) ReplaceSignatories(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req signatoryReplaceReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	inputs := make([]store.SignatoryInput, 0, len(req.Signatories))
	for i, s := range req.Signatories {
		class, err := parseSignatoryClass(s.Class)
		if err != nil {
			return
		}
		_ = i
		inputs = append(inputs, store.SignatoryInput{
			OfficialID:   s.OfficialID,
			Class:        class,
			SigningOrder: s.SigningOrder,
			TxnLimit:     s.TxnLimit,
		})
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Signatories.ReplaceTx(r.Context(), tx, tenantID, orgID, inputs)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.signatories_replaced", map[string]any{"count": len(inputs)})
	httpx.NoContent(w)
}

type mandateReq struct {
	Rules map[string]any `json:"rules"`
}

func (h *OrgHandler) SetMandate(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req mandateReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Signatories.UpsertMandateTx(r.Context(), tx, tenantID, orgID, req.Rules)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.mandate_updated", nil)
	httpx.NoContent(w)
}

// ─────────── Banking ───────────

func (h *OrgHandler) UpsertBanking(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var b domain.Banking
	if err := httpx.DecodeJSON(r, &b); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	b.OrgID = orgID
	var out *domain.Banking
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = h.Banking.UpsertTx(r.Context(), tx, tenantID, b)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.banking_updated", nil)
	httpx.OK(w, out)
}

// ─────────── Contacts ───────────

type contactsReplaceReq struct {
	Contacts []contactDTO `json:"contacts"`
}

func (h *OrgHandler) ReplaceContacts(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid org id"))
		return
	}
	var req contactsReplaceReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	inputs := make([]store.ContactInput, 0, len(req.Contacts))
	for _, c := range req.Contacts {
		ck, err := parseContactKind(c.Kind)
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		inputs = append(inputs, store.ContactInput{
			Kind: ck, FullName: c.FullName, Role: c.Role, Phone: c.Phone, Email: c.Email,
		})
	}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Contacts.ReplaceTx(r.Context(), tx, tenantID, orgID, inputs)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, orgID, "org.contacts_updated", map[string]any{"count": len(inputs)})
	httpx.NoContent(w)
}

// ─────────── parsers ───────────

func parseOrgKind(s string) (domain.OrgKind, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", httpx.ErrBadRequest("kind is required")
	}
	switch domain.OrgKind(s) {
	case domain.OrgGroup, domain.OrgChama, domain.OrgLtd, domain.OrgSoleProp,
		domain.OrgNGO, domain.OrgChurch, domain.OrgSacco, domain.OrgCooperative, domain.OrgSchool:
		return domain.OrgKind(s), nil
	}
	return "", httpx.ErrBadRequest("invalid kind")
}

func parseOrgDocKind(s string) (domain.OrgDocKind, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch domain.OrgDocKind(s) {
	case domain.DocRegistrationCertificate, domain.DocCR12, domain.DocKRAPINCertificate,
		domain.DocMemorandumArticles, domain.DocConstitutionBylaws, domain.DocBusinessPermit,
		domain.DocTaxComplianceCertificate, domain.DocVATCertificate, domain.DocNGOCertificate,
		domain.DocCooperativeCertificate, domain.DocProofOfAddress, domain.DocAuditedFinancials,
		domain.DocBankStatement, domain.DocBoardResolution,
		domain.DocSignatoryAppointmentResol, domain.DocBeneficialOwnershipDecl:
		return domain.OrgDocKind(s), nil
	}
	return "", httpx.ErrBadRequest("invalid document kind: " + s)
}

func parseContactKind(s string) (domain.ContactKind, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch domain.ContactKind(s) {
	case domain.ContactPrimary, domain.ContactFinance, domain.ContactHRPayroll, domain.ContactCompliance:
		return domain.ContactKind(s), nil
	}
	return "", httpx.ErrBadRequest("invalid contact kind: " + s)
}

func parseSignatoryClass(s string) (domain.SignatoryClass, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return domain.SigMandatory, nil
	}
	switch domain.SignatoryClass(s) {
	case domain.SigMandatory, domain.SigOptional, domain.SigAlternate:
		return domain.SignatoryClass(s), nil
	}
	return "", httpx.ErrBadRequest("invalid signatory class: " + s)
}

func parseOfficialPosition(s string) (domain.OfficialPosition, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return domain.PosDirector, nil
	}
	switch domain.OfficialPosition(s) {
	case domain.PosChairperson, domain.PosViceChairperson, domain.PosTreasurer, domain.PosSecretary,
		domain.PosDirector, domain.PosTrustee, domain.PosPrincipal, domain.PosPastor, domain.PosOther:
		return domain.OfficialPosition(s), nil
	}
	return "", httpx.ErrBadRequest("invalid position: " + s)
}

func bankingEmpty(b domain.Banking) bool {
	return b.BankName == "" && b.BankBranch == "" && b.BankCode == "" && b.SwiftCode == "" &&
		b.AccountName == "" && b.AccountNumber == "" && b.Paybill == "" && b.TillNumber == "" &&
		b.MobileMoneyPhones == "" && b.MobileSettlementAccount == "" && b.PreferredDisbursement == "" &&
		b.PreferredRepayment == "" && b.StandingOrderDetails == "" && b.CheckoffArrangement == ""
}

func stringOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// isAllowedOrgDocMIME accepts the common document image + PDF formats.
func isAllowedOrgDocMIME(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/svg+xml",
		"application/pdf":
		return true
	}
	return false
}

// extFromMIMEv2 mirrors storage.extFromMIME (which is unexported) for
// reconstructing the on-disk filename of an official's file.
func extFromMIMEv2(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	}
	return ".bin"
}
