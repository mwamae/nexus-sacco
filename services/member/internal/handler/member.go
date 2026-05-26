// HTTP handlers for member onboarding + lifecycle.

package handler

import (
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

type MemberHandler struct {
	DB             *db.Pool
	Members        *store.MemberStore
	Relations      *store.RelationStore
	Documents      *store.DocumentStore
	Audit          *store.AuditStore
	Counterparties *store.CounterpartyStore // Phase A dual-target mirror
	Storage        storage.Storage
	MaxUpload      int64
	Logger         *slog.Logger
	Notifier       *notifier.Client
}

func (h *MemberHandler) audit(r *http.Request, tenantID uuid.UUID, memberID uuid.UUID, action string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &tenantID,
		ActorID:    nonZero(actorID),
		Action:     action,
		TargetKind: "member",
		TargetID:   memberID.String(),
		IP:         "", // member service doesn't extract client IP yet
		UserAgent:  r.UserAgent(),
		Metadata:   meta,
	})
}

// ─────────── POST /v1/members ───────────

type relationDTO struct {
	Kind         string   `json:"kind"`
	FullName     string   `json:"full_name"`
	Relationship string   `json:"relationship"`
	Phone        string   `json:"phone"`
	Email        string   `json:"email"`
	IDDocNumber  string   `json:"id_doc_number"`
	SharePercent *float64 `json:"share_percent"`
}

type createMemberRequest struct {
	FullName    string `json:"full_name"`
	IDDocKind   string `json:"id_doc_kind"`
	IDDocNumber string `json:"id_doc_number"`
	KraPIN      string `json:"kra_pin"`
	Gender      string `json:"gender"`
	DateOfBirth string `json:"date_of_birth"` // YYYY-MM-DD

	Phone string `json:"phone"`
	Email string `json:"email"`

	County          string `json:"county"`
	SubCounty       string `json:"sub_county"`
	PhysicalAddress string `json:"physical_address"`

	EmploymentStatus string `json:"employment_status"`
	Employer         string `json:"employer"`
	PayrollNo        string `json:"payroll_no"`
	JobTitle         string `json:"job_title"`

	NextOfKin     *relationDTO  `json:"next_of_kin"`
	Beneficiaries []relationDTO `json:"beneficiaries"`
}

func (h *MemberHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var req createMemberRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.IDDocNumber = strings.TrimSpace(req.IDDocNumber)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.FullName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("full_name is required"))
		return
	}
	if req.IDDocNumber == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("id_doc_number is required"))
		return
	}

	idKind, err := parseIDKind(req.IDDocKind)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	gender, err := parseGender(req.Gender)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	dob, err := parseDate(req.DateOfBirth)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// Beneficiary share percentages must sum to 100 if any provided.
	if len(req.Beneficiaries) > 0 {
		var sum float64
		any := false
		for _, b := range req.Beneficiaries {
			if b.SharePercent != nil {
				sum += *b.SharePercent
				any = true
			}
		}
		if any && (sum < 99.999 || sum > 100.001) {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("beneficiary share_percent must sum to 100"))
			return
		}
	}

	actorID, _ := middleware.UserIDFrom(r)
	var created *domain.Member
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		memberNo, err := h.Members.NextMemberNoTx(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		m, err := h.Members.CreateTx(r.Context(), tx, store.CreateMemberInput{
			TenantID:         tenantID,
			FullName:         req.FullName,
			IDDocKind:        idKind,
			IDDocNumber:      req.IDDocNumber,
			KraPIN:           strings.ToUpper(strings.TrimSpace(req.KraPIN)),
			Gender:           gender,
			DateOfBirth:      dob,
			Phone:            strings.TrimSpace(req.Phone),
			Email:            req.Email,
			County:           strings.TrimSpace(req.County),
			SubCounty:        strings.TrimSpace(req.SubCounty),
			PhysicalAddress:  strings.TrimSpace(req.PhysicalAddress),
			EmploymentStatus: strings.TrimSpace(req.EmploymentStatus),
			Employer:         strings.TrimSpace(req.Employer),
			PayrollNo:        strings.TrimSpace(req.PayrollNo),
			JobTitle:         strings.TrimSpace(req.JobTitle),
			CreatedBy:        nonZero(actorID),
		}, memberNo)
		if err != nil {
			return err
		}

		// Phase E A: relations stores key off counterparty.id now.
		// We have a fresh members.id from CreateTx; the counterparty
		// hasn't been created yet (that happens further down), so we
		// have to defer relations writes until after the CP is stamped.
		var nokInput *store.RelationInput
		if req.NextOfKin != nil && req.NextOfKin.FullName != "" {
			nokInput = &store.RelationInput{
				Kind:         domain.RelNextOfKin,
				FullName:     req.NextOfKin.FullName,
				Relationship: req.NextOfKin.Relationship,
				Phone:        req.NextOfKin.Phone,
				Email:        req.NextOfKin.Email,
				IDDocNumber:  req.NextOfKin.IDDocNumber,
			}
		}
		var benefInput []store.RelationInput
		if len(req.Beneficiaries) > 0 {
			benefInput = make([]store.RelationInput, 0, len(req.Beneficiaries))
			for _, b := range req.Beneficiaries {
				if strings.TrimSpace(b.FullName) == "" {
					continue
				}
				benefInput = append(benefInput, store.RelationInput{
					Kind:         domain.RelBeneficiary,
					FullName:     b.FullName,
					Relationship: b.Relationship,
					Phone:        b.Phone,
					Email:        b.Email,
					IDDocNumber:  b.IDDocNumber,
					SharePercent: b.SharePercent,
				})
			}
		}
		created = m

		// Counterparty co-create — direct admin POST /v1/members
		// path. Creates the matching counterparty row + stamps the
		// bridge inside the same tx so the unified register sees
		// the new entity immediately. Application-approval has its
		// own co-create in ApplicationHandler.Approve.
		var cpID uuid.UUID
		if h.Counterparties != nil {
			id, err := createCounterpartyForMemberTx(
				r.Context(), tx, h.Counterparties, tenantID, m, actorID,
			)
			if err != nil {
				return fmt.Errorf("create counterparty: %w", err)
			}
			cpID = id
		}
		// Phase E A: write relations now that the counterparty bridge is
		// stamped. Deferred from above for the same reason
		// OpenDefaultIndividualAccountsTx is deferred in
		// application_store: dependent writes can't fire until the
		// bridge column is populated.
		if cpID != uuid.Nil {
			if nokInput != nil {
				if err := h.Relations.ReplaceTx(r.Context(), tx, tenantID, cpID, domain.RelNextOfKin, []store.RelationInput{*nokInput}); err != nil {
					return err
				}
			}
			if len(benefInput) > 0 {
				if err := h.Relations.ReplaceTx(r.Context(), tx, tenantID, cpID, domain.RelBeneficiary, benefInput); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("a member with that ID number already exists"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, created.ID, "member.created", map[string]any{
		"member_no": created.MemberNo, "id_doc_number": created.IDDocNumber,
	})

	// Notify the new member — welcomes them and confirms their member_no.
	// Non-blocking; success is what counts even if notification fails.
	if h.Notifier != nil {
		memberID := created.ID
		sourceModule := "member.onboarding"
		recordID := created.ID
		deepLink := "/members/" + created.ID.String()
		var phone, email *string
		if created.Phone != "" {
			p := created.Phone
			phone = &p
		}
		if created.Email != "" {
			e := created.Email
			email = &e
		}
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tenantID,
			EventCode:         "MEMBER_REGISTERED",
			RecipientMemberID: &memberID,
			RecipientName:     created.FullName,
			RecipientPhone:    phone,
			RecipientEmail:    email,
			SourceModule:      &sourceModule,
			SourceRecordID:    &recordID,
			DeepLink:          &deepLink,
			InitiatedBy:       nonZero(actorID),
			Payload: map[string]any{
				"member_no": created.MemberNo,
				"full_name": created.FullName,
			},
		})
	}
	httpx.Created(w, created)
}

// ─────────── GET /v1/members ───────────

func (h *MemberHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	status := domain.MemberStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	var result *store.ListResult
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		res, err := h.Members.ListTx(r.Context(), tx, store.ListInput{
			Status: status, Query: query, Limit: limit, Offset: offset,
		})
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"members": result.Members,
		"total":   result.Total,
		"limit":   limit,
		"offset":  offset,
	})
}

// ─────────── GET /v1/members/{id} ───────────

type memberDetail struct {
	*domain.Member
	NextOfKin     *domain.Relation   `json:"next_of_kin"`
	Beneficiaries []*domain.Relation `json:"beneficiaries"`
	Documents     []*domain.Document `json:"documents"`
	// Phase B — the bridge values. Populated when the member row has
	// been backfilled (which is every row in any tenant that ran
	// migration 0008). Frontend uses this to render the CP-* number
	// alongside the legacy M-* without a second round-trip.
	CPNumber       *string `json:"cp_number,omitempty"`
	CounterpartyID *string `json:"counterparty_id,omitempty"`
}

func (h *MemberHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid member id"))
		return
	}
	var out memberDetail
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		m, err := h.Members.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out.Member = m
		// Phase E A: relations + documents stores key off counterparty.id
		// now. Resolve at the boundary since this handler still serves
		// the legacy /members/{id} GET route (URL parameter is a
		// members.id).
		cpID, err := store.ResolveCounterpartyID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		rels, err := h.Relations.ListForCounterpartyTx(r.Context(), tx, cpID)
		if err != nil {
			return err
		}
		out.Beneficiaries = []*domain.Relation{}
		for _, r := range rels {
			switch r.Kind {
			case domain.RelNextOfKin:
				out.NextOfKin = r
			case domain.RelBeneficiary:
				out.Beneficiaries = append(out.Beneficiaries, r)
			}
		}
		docs, err := h.Documents.ListForCounterpartyTx(r.Context(), tx, cpID)
		if err != nil {
			return err
		}
		out.Documents = docs

		// Bridge to the unified register. cpID already resolved above;
		// fetch the cp_number for display.
		var cpIDStr, cpNo *string
		_ = tx.QueryRow(r.Context(), `
			SELECT id::text, cp_number FROM counterparties WHERE id = $1
		`, cpID).Scan(&cpIDStr, &cpNo)
		out.CounterpartyID = cpIDStr
		out.CPNumber = cpNo
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/members/{id}/status ───────────
//
// Transitions a member's lifecycle status. Pending → active/rejected
// must go through /approve|/reject; this endpoint covers the post-
// approval transitions (active ↔ suspended, → closed).

type setStatusRequest struct {
	Status string `json:"status"`
}

func (h *MemberHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid member id"))
		return
	}
	var req setStatusRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	next := domain.MemberStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	switch next {
	case domain.StatusActive, domain.StatusSuspended, domain.StatusClosed:
		// allowed
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be 'active', 'suspended', or 'closed'"))
		return
	}

	var fromStatus domain.MemberStatus
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		current, err := h.Members.ByIDTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// Block pending → active here so callers must go through /approve
		// (which also records approver + clears rejection_reason).
		if current.Status == domain.StatusPending {
			return httpx.ErrBadRequest("pending members must use /approve or /reject")
		}
		if current.Status == next {
			return nil
		}
		fromStatus = current.Status
		return h.Members.SetStatusTx(r.Context(), tx, id, next)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if fromStatus != "" {
		h.audit(r, tenantID, id, "member.status_changed", map[string]any{
			"from": string(fromStatus), "to": string(next),
		})
		h.fireStatusChanged(r, tenantID, id, fromStatus, next)
	}
	httpx.NoContent(w)
}

// fireStatusChanged emits MEMBER_STATUS_CHANGED. Re-fetches the member
// row to pick up contact info — the SetStatus path doesn't otherwise
// keep the full row in scope.
func (h *MemberHandler) fireStatusChanged(r *http.Request, tenantID, memberID uuid.UUID, from, to domain.MemberStatus) {
	if h.Notifier == nil {
		return
	}
	var m *domain.Member
	_ = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		m, err = h.Members.ByIDTx(r.Context(), tx, memberID)
		return err
	})
	if m == nil {
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	sourceModule := "member.lifecycle"
	recordID := m.ID
	mid := m.ID
	deepLink := "/members/" + m.ID.String()
	var phone, email *string
	if m.Phone != "" {
		p := m.Phone
		phone = &p
	}
	if m.Email != "" {
		e := m.Email
		email = &e
	}
	h.Notifier.Notify(r.Context(), notifier.Request{
		TenantID:          tenantID,
		EventCode:         "MEMBER_STATUS_CHANGED",
		RecipientMemberID: &mid,
		RecipientName:     m.FullName,
		RecipientPhone:    phone,
		RecipientEmail:    email,
		SourceModule:      &sourceModule,
		SourceRecordID:    &recordID,
		DeepLink:          &deepLink,
		InitiatedBy:       nonZero(actorID),
		Payload: map[string]any{
			"member_no":   m.MemberNo,
			"full_name":   m.FullName,
			"from_status": string(from),
			"to_status":   string(to),
		},
	})
}

// ─────────── POST /v1/members/{id}/approve ───────────

func (h *MemberHandler) Approve(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid member id"))
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return h.Members.ApproveTx(r.Context(), tx, id, actorID)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found or not in pending state"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "member.approved", nil)
	h.fireStatusChanged(r, tenantID, id, domain.StatusPending, domain.StatusActive)
	httpx.NoContent(w)
}

// ─────────── POST /v1/members/{id}/reject ───────────

type rejectRequest struct {
	Reason string `json:"reason"`
}

func (h *MemberHandler) Reject(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid member id"))
		return
	}
	var req rejectRequest
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
		return h.Members.RejectTx(r.Context(), tx, id, actorID, req.Reason)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found or not in pending state"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "member.rejected", map[string]any{"reason": req.Reason})
	httpx.NoContent(w)
}

// ─────────── POST /v1/members/{id}/documents/{kind} ───────────
//
// Multipart upload (field: "file"). Replaces any existing document for
// the (member, kind) pair.

func (h *MemberHandler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id (route is
	// /counterparties/{id}/documents/{kind}).
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	kind, err := parseDocKind(chi.URLParam(r, "kind"))
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
	if !isAllowedMIME(kind, mime) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("disallowed file type for "+string(kind)))
		return
	}

	issueDate, err := parseOptionalDate(r.URL.Query().Get("issue_date"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid issue_date (want YYYY-MM-DD)"))
		return
	}
	expiryDate, err := parseOptionalDate(r.URL.Query().Get("expiry_date"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid expiry_date (want YYYY-MM-DD)"))
		return
	}
	if issueDate != nil && expiryDate != nil && expiryDate.Before(*issueDate) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("expiry_date must be on or after issue_date"))
		return
	}

	// First check counterparty exists (RLS will hide cross-tenant ids).
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := h.Counterparties.GetTx(r.Context(), tx, id)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("counterparty not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	path, size, err := h.Storage.Save(tenantID, id, string(kind), mime, file, header.Size)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	var doc *domain.Document
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		d, err := h.Documents.UpsertTx(r.Context(), tx, store.CreateDocumentInput{
			CounterpartyID: id,
			TenantID:       tenantID,
			Kind:           kind,
			StoragePath:    path,
			MIME:           mime,
			SizeBytes:      size,
			IssueDate:      issueDate,
			ExpiryDate:     expiryDate,
			UploadedBy:     nonZero(actorID),
		})
		if err != nil {
			return err
		}
		doc = d
		return nil
	})
	if err != nil {
		// Try to clean up the on-disk file if metadata insert failed.
		_ = h.Storage.Delete(path)
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, id, "member.document_uploaded", map[string]any{
		"kind":        string(kind),
		"mime":        mime,
		"size":        size,
		"issue_date":  isoDate(issueDate),
		"expiry_date": isoDate(expiryDate),
	})
	httpx.Created(w, doc)
}

// ─────────── POST /v1/counterparties/{id}/documents/{kind}/verify ───────────
//
// Mirrors OrgHandler.VerifyDocument for the individual side. Permission
// is gated by routes.go (members:edit). Body is {status, note}.
func (h *MemberHandler) VerifyDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	kind, err := parseDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var req verifyMemberDocReq
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
	if status == domain.VerifyRejected && strings.TrimSpace(req.Note) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("note is required when rejecting"))
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
	h.audit(r, tenantID, id, "member.document_verified", map[string]any{
		"kind": string(kind), "status": string(status), "note": req.Note,
	})
	httpx.NoContent(w)
}

// ─────────── DELETE /v1/counterparties/{id}/documents/{kind} ───────────
//
// Removes the DB row inside an RLS-scoped transaction, then best-effort
// deletes the underlying storage blob. Storage failure is logged but
// does NOT roll back the DB delete — the row is the source of truth
// and a stranded blob is preferable to a row pointing at nothing.
func (h *MemberHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	kind, err := parseDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var storagePath string
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		doc, err := h.Documents.ByKindTx(r.Context(), tx, id, kind)
		if err != nil {
			return err
		}
		path, err := h.Documents.DeleteTx(r.Context(), tx, doc.ID)
		if err != nil {
			return err
		}
		storagePath = path
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
	if storagePath != "" {
		if err := h.Storage.Delete(storagePath); err != nil {
			slog.Warn("storage delete failed after member_documents row removal",
				"path", storagePath, "err", err)
		}
	}
	h.audit(r, tenantID, id, "member.document_removed", map[string]any{
		"kind": string(kind),
	})
	httpx.NoContent(w)
}

type verifyMemberDocReq struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// ─────────── GET /v1/members/{id}/documents/{kind} ───────────

func (h *MemberHandler) DownloadDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id.
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	kind, err := parseDocKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var doc *domain.Document
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

// ─────────── helpers ───────────

func parseIDKind(s string) (domain.IDDocKind, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return domain.IDNationalID, nil
	}
	switch s {
	case "national_id", "passport", "alien_id":
		return domain.IDDocKind(s), nil
	}
	return "", httpx.ErrBadRequest("invalid id_doc_kind")
}

func parseGender(s string) (domain.Gender, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return domain.GenderUndisclosed, nil
	}
	switch s {
	case "male", "female", "other", "undisclosed":
		return domain.Gender(s), nil
	}
	return "", httpx.ErrBadRequest("invalid gender")
}

func parseDocKind(s string) (domain.DocumentKind, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "signature", "passport_photo", "id_front", "id_back",
		"death_certificate", "exit_clearance", "blacklist_directive",
		"kra_pin_certificate", "proof_of_address", "bank_statement",
		"payslip", "employment_letter", "business_permit",
		"signed_application_form", "next_of_kin_id", "other":
		return domain.DocumentKind(s), nil
	}
	return "", httpx.ErrBadRequest("invalid document kind")
}

func parseDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, httpx.ErrBadRequest("invalid date_of_birth (want YYYY-MM-DD)")
	}
	return &t, nil
}

// parseOptionalDate returns nil on empty input (the caller supplies
// the field-specific error message — keeps the helper generic).
func parseOptionalDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func isoDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02")
}

func isAllowedMIME(kind domain.DocumentKind, mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch kind {
	case domain.DocSignature:
		return mime == "image/png" || mime == "image/jpeg" || mime == "image/jpg" || mime == "image/svg+xml"
	case domain.DocPassportPhoto, domain.DocIDFront, domain.DocIDBack,
		domain.DocNextOfKinID:
		return mime == "image/png" || mime == "image/jpeg" || mime == "image/jpg" || mime == "image/webp"
	}
	// Generic KYC / supporting documents: images + PDF + Word.
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/jpg" ||
		mime == "image/webp" || mime == "application/pdf" ||
		mime == "application/msword" ||
		mime == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}

func nonZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
