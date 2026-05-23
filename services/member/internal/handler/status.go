// Member status-change handler. Owns:
//   * POST   /v1/members/{id}/status-change           — propose or apply
//   * GET    /v1/members/{id}/status-history          — audit timeline
//   * GET    /v1/members/{id}/status-actions          — what the UI can do
//   * POST   /v1/members/{id}/status-supporting-doc   — multipart upload
//   * POST   /v1/members/dormancy/preview             — read-only
//   * POST   /v1/members/dormancy/run                 — applies dormant status
//   * POST   /v1/members/status/callback              — workflow → here
//   * GET    /v1/members/status/summary               — dashboard counts

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/httpx"
	"github.com/nexussacco/member/internal/middleware"
	"github.com/nexussacco/member/internal/notifier"
	"github.com/nexussacco/member/internal/storage"
	"github.com/nexussacco/member/internal/store"
)

type StatusHandler struct {
	DB             *db.Pool
	Members        *store.MemberStore
	Status         *store.StatusChangeStore
	Audit          *store.AuditStore
	Storage        storage.Storage
	MaxUpload      int64
	Logger         *slog.Logger
	WorkflowURL    string // e.g. http://localhost:8083
	MemberSelfURL  string // e.g. http://localhost:8082 — used as callback_url for the workflow
	HTTP           *http.Client
	DefaultDormancyDays int
	WorkflowProcessKind string // e.g. "member_status_change"
	Notifier       *notifier.Client
}

// ─────────── GET /v1/members/{id}/status-actions ───────────

type statusActionsResponse struct {
	Current        domain.MemberStatus            `json:"current"`
	SystemBehavior string                         `json:"system_behavior"`
	Visibility     domain.Visibility              `json:"visibility"`
	Transitions    []domain.Transition            `json:"transitions"`
	OpenProposals  []*domain.MemberStatusProposal `json:"open_proposals"`
	AllowedActions []allowedAction                `json:"allowed_actions"`
}

type allowedAction struct {
	Action  domain.Action `json:"action"`
	Allowed bool          `json:"allowed"`
}

func (h *StatusHandler) Actions(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id (route is
	// /counterparties/{id}/status-actions).
	cpID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	out := &statusActionsResponse{}
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		m, err := h.Members.ByCounterpartyTx(r.Context(), tx, cpID)
		if err != nil {
			return err
		}
		out.Current = m.Status
		out.SystemBehavior = domain.SystemBehavior(m.Status)
		out.Visibility = domain.VisibilityFor(m.Status)
		out.Transitions = domain.AllowedTransitionsFrom(m.Status)
		out.OpenProposals, err = h.Status.OpenProposalsForCounterpartyTx(r.Context(), tx, cpID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	for _, a := range []domain.Action{
		domain.ActionApplyLoan, domain.ActionDeposit, domain.ActionWithdraw,
		domain.ActionBuyShares, domain.ActionTransferShares, domain.ActionBecomeGuarantor,
		domain.ActionUpdateKYC, domain.ActionPortalLogin, domain.ActionReceiveDividend,
		domain.ActionLoanRepayment,
	} {
		out.AllowedActions = append(out.AllowedActions, allowedAction{
			Action: a, Allowed: domain.CanPerform(out.Current, a),
		})
	}
	if out.Transitions == nil {
		out.Transitions = []domain.Transition{}
	}
	if out.OpenProposals == nil {
		out.OpenProposals = []*domain.MemberStatusProposal{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/members/{id}/status-change ───────────

type statusChangeRequest struct {
	TargetStatus      string `json:"target_status"`
	ReasonCategory    string `json:"reason_category"`
	ReasonNote        string `json:"reason_note"`
	ReviewDate        string `json:"review_date"`
	SupportingDocPath string `json:"supporting_doc_path"`
	SupportingDocMIME string `json:"supporting_doc_mime"`
	// SkipWorkflow lets a platform-admin bypass workflow routing during
	// dev / recovery. Ignored unless caller is is_platform_admin.
	SkipWorkflow bool `json:"skip_workflow"`
}

type statusChangeResponse struct {
	Mode               string                       `json:"mode"` // "applied" | "proposed"
	Member             *domain.Member               `json:"member,omitempty"`
	StatusChange       *domain.MemberStatusChange   `json:"status_change,omitempty"`
	Proposal           *domain.MemberStatusProposal `json:"proposal,omitempty"`
	WorkflowInstanceID *uuid.UUID                   `json:"workflow_instance_id,omitempty"`
}

func (h *StatusHandler) Change(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id.
	cpID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	var req statusChangeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	target := domain.MemberStatus(strings.ToLower(strings.TrimSpace(req.TargetStatus)))
	reason := domain.StatusReason(strings.ToLower(strings.TrimSpace(req.ReasonCategory)))
	if reason == "" {
		reason = domain.ReasonAdminAction
	}
	if !isValidStatus(target) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid target_status"))
		return
	}
	if !isValidReason(reason) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid reason_category"))
		return
	}
	var reviewDate *time.Time
	if s := strings.TrimSpace(req.ReviewDate); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("review_date must be YYYY-MM-DD"))
			return
		}
		reviewDate = &t
	}
	actorID, _ := middleware.UserIDFrom(r)
	claims := middleware.ClaimsFrom(r)
	isAdmin := claims != nil && claims.IsPlatformAdmin

	// Load the current state to decide direct-apply vs workflow.
	var m *domain.Member
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		m, err = h.Members.ByCounterpartyTx(r.Context(), tx, cpID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("member not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if err := domain.ValidateTransition(m.Status, target); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(err.Error()))
		return
	}

	// Exit guard: share capital is equity and cannot be redeemed for
	// cash. An exiting member must have transferred their full share
	// balance to another active member before this transition is
	// allowed. Refuse the move if any shares are still held — the UI
	// surfaces this message verbatim in the exit workflow.
	if target == domain.StatusExited {
		var sharesHeld int
		err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(),
				`SELECT COALESCE(shares_held, 0) FROM share_accounts WHERE counterparty_id = $1`,
				cpID,
			).Scan(&sharesHeld)
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, store.ErrNotFound) {
			// share_accounts may not exist in some test contexts — treat
			// "table not found" as zero shares so the member service stays
			// usable independently. Real production has the table.
			if !isMissingShareAccountsTable(err) {
				httpx.WriteErr(w, r, err)
				return
			}
			sharesHeld = 0
		}
		if sharesHeld > 0 {
			httpx.WriteErr(w, r, httpx.ErrConflict(
				"Your share balance must be fully transferred to active members before your exit can be finalized. Share capital cannot be redeemed.",
			))
			return
		}
	}

	sensitive := domain.IsSensitive(m.Status, target) && !(req.SkipWorkflow && isAdmin)

	if !sensitive {
		// Direct apply.
		var change *domain.MemberStatusChange
		err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
			c, err := h.Status.ApplyTx(r.Context(), tx, store.ApplyInput{
				TenantID:          tenantID,
				CounterpartyID:    cpID,
				FromStatus:        m.Status,
				ToStatus:          target,
				ReasonCategory:    reason,
				ReasonNote:        req.ReasonNote,
				SupportingDocPath: req.SupportingDocPath,
				SupportingDocMIME: req.SupportingDocMIME,
				ChangedBy:         nonZero(actorID),
				ReviewDate:        reviewDate,
			})
			if err != nil {
				return err
			}
			change = c
			m, err = h.Members.ByIDTx(r.Context(), tx, m.ID)
			return err
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		h.auditChange(r, tenantID, m.ID, m.Status, target, reason, req.ReasonNote, nil)
		httpx.OK(w, statusChangeResponse{
			Mode: "applied", Member: m, StatusChange: change,
		})
		return
	}

	// Sensitive — route through workflow.
	wfInstanceID, err := h.createWorkflowInstance(r, tenantID, m, target, reason, req.ReasonNote, actorID)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var proposal *domain.MemberStatusProposal
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		proposal, err = h.Status.CreateProposalTx(r.Context(), tx, store.ProposalInput{
			TenantID:           tenantID,
			CounterpartyID:     cpID,
			WorkflowInstanceID: wfInstanceID,
			ProposedStatus:     target,
			ReasonCategory:     reason,
			ReasonNote:         req.ReasonNote,
			SupportingDocPath:  req.SupportingDocPath,
			SupportingDocMIME:  req.SupportingDocMIME,
			ReviewDate:         reviewDate,
			ProposedBy:         nonZero(actorID),
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.audit(r, tenantID, m.ID, "member.status_change_proposed", map[string]any{
		"target_status": string(target), "reason_category": string(reason),
		"workflow_instance_id": wfInstanceID,
	})

	w.Header().Set("Location", "/v1/workflow-instances/"+wfInstanceID.String())
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"data": statusChangeResponse{
			Mode: "proposed", Proposal: proposal, WorkflowInstanceID: &wfInstanceID,
		},
	})
}

// ─────────── GET /v1/counterparties/{id}/status-history ───────────

func (h *StatusHandler) History(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id.
	cpID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
		return
	}
	var out []*domain.MemberStatusChange
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = h.Status.HistoryTx(r.Context(), tx, cpID, 200)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out == nil {
		out = []*domain.MemberStatusChange{}
	}
	httpx.OK(w, out)
}

// ─────────── POST /v1/members/{id}/status-supporting-doc ───────────
//
// Multipart upload that returns a {storage_path, mime} reference the
// caller passes into the status-change request body. We don't insert a
// row in member_documents (that table is for the standard KYC docs);
// supporting docs for status changes live alongside the change row.

func (h *StatusHandler) UploadSupportingDoc(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	// Phase E A: URL parameter is now a counterparty.id.
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty id"))
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
	switch strings.ToLower(mime) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "application/pdf":
		// ok
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("supporting doc must be PNG, JPEG, WebP, or PDF"))
		return
	}
	// Verify the counterparty exists in this tenant.
	if err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		_, err := h.Members.ByCounterpartyTx(r.Context(), tx, id)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("counterparty not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	// Stamp the filename with a timestamp so multiple uploads don't clobber.
	kind := "status-supporting/" + time.Now().UTC().Format("20060102-150405")
	path, size, err := h.Storage.Save(tenantID, id, kind, mime, file, header.Size)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"storage_path": path, "mime": mime, "size_bytes": size,
	})
}

// ─────────── GET /v1/members/{id}/status-history/{change_id}/doc ───────────

func (h *StatusHandler) DownloadSupportingDoc(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	changeID, err := uuid.Parse(chi.URLParam(r, "change_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid change id"))
		return
	}
	var path, mime string
	err = h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(r.Context(), `
			SELECT COALESCE(supporting_doc_path,''), COALESCE(supporting_doc_mime,'application/octet-stream')
			FROM member_status_changes WHERE id = $1
		`, changeID)
		return row.Scan(&path, &mime)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("status change not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if path == "" {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no supporting document on this change"))
		return
	}
	f, err := h.Storage.Open(path)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, f)
}

// ─────────── Dormancy ───────────

type dormancyResponse struct {
	ThresholdDays int                          `json:"threshold_days"`
	Candidates    []*store.DormancyCandidate   `json:"candidates"`
	Applied       []*domain.MemberStatusChange `json:"applied,omitempty"`
}

func (h *StatusHandler) DormancyPreview(w http.ResponseWriter, r *http.Request) {
	h.dormancy(w, r, false)
}

func (h *StatusHandler) DormancyRun(w http.ResponseWriter, r *http.Request) {
	h.dormancy(w, r, true)
}

func (h *StatusHandler) dormancy(w http.ResponseWriter, r *http.Request, apply bool) {
	tenantID, _ := middleware.TenantIDFrom(r)
	threshold := h.tenantDormancyDays(r.Context(), tenantID)
	resp := &dormancyResponse{ThresholdDays: threshold}

	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		cands, err := h.Status.DormancyCandidatesTx(r.Context(), tx, threshold)
		if err != nil {
			return err
		}
		resp.Candidates = cands
		if !apply || len(cands) == 0 {
			return nil
		}
		// Mark each as dormant. Dormancy is non-sensitive, so applies
		// directly. The "changed_by" is whoever invoked the run.
		actorID, _ := middleware.UserIDFrom(r)
		for _, c := range cands {
			change, err := h.Status.ApplyTx(r.Context(), tx, store.ApplyInput{
				TenantID:       tenantID,
				CounterpartyID:       c.CounterpartyID,
				FromStatus:     domain.StatusActive,
				ToStatus:       domain.StatusDormant,
				ReasonCategory: domain.ReasonDormancyInactivity,
				ReasonNote:     "auto: inactive for " + dayString(c.DaysInactive),
				ChangedBy:      nonZero(actorID),
			})
			if err != nil {
				return err
			}
			resp.Applied = append(resp.Applied, change)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if resp.Candidates == nil {
		resp.Candidates = []*store.DormancyCandidate{}
	}
	if apply {
		h.audit(r, tenantID, uuid.Nil, "member.dormancy_run", map[string]any{
			"threshold_days": threshold, "applied": len(resp.Applied),
		})
	}
	httpx.OK(w, resp)
}

// ─────────── GET /v1/members/status/summary ───────────

// statusSummaryResponse is the dashboard payload. All count fields
// (by_status + the two totals) come from the same
// member_status_counts(tenant_id) source so they cannot disagree.
type statusSummaryResponse struct {
	ByStatus             map[domain.MemberStatus]int `json:"by_status"`
	TotalOnRegister      int                         `json:"total_on_register"`
	TotalActiveServicing int                         `json:"total_active_servicing"`
	DormancyPipeline     []*store.DormancyCandidate  `json:"dormancy_pipeline"`
	RecentChanges        []*store.RecentChange       `json:"recent_changes"`
	ThresholdDays        int                         `json:"dormancy_threshold_days"`
}

func (h *StatusHandler) Summary(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	threshold := h.tenantDormancyDays(r.Context(), tenantID)
	warn := 30
	out := &statusSummaryResponse{ThresholdDays: threshold}
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		counts, err := h.Status.MemberStatusCountsTx(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		out.ByStatus = counts.ByStatus()
		out.TotalOnRegister = counts.TotalOnRegister
		out.TotalActiveServicing = counts.TotalActiveServicing

		out.DormancyPipeline, err = h.Status.DormancyPipelineTx(r.Context(), tx, threshold, warn)
		if err != nil {
			return err
		}
		out.RecentChanges, err = h.Status.RecentChangesTx(r.Context(), tx, 20)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if out.ByStatus == nil {
		out.ByStatus = map[domain.MemberStatus]int{}
	}
	if out.DormancyPipeline == nil {
		out.DormancyPipeline = []*store.DormancyCandidate{}
	}
	if out.RecentChanges == nil {
		out.RecentChanges = []*store.RecentChange{}
	}
	httpx.OK(w, out)
}

// ─────────── GET /v1/members/status/counts ───────────
//
// Lean version of /status/summary for views that need the roll-call
// numbers but not the dormancy pipeline / recent-changes panels (e.g.
// the Members page KPI strip). Returns exactly the
// MemberStatusCounts shape so the Members page and the dashboard pull
// from the same source.

func (h *StatusHandler) Counts(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantIDFrom(r)
	var counts *store.MemberStatusCounts
	err := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		c, err := h.Status.MemberStatusCountsTx(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		counts = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, counts)
}

// ─────────── POST /v1/members/status/callback ───────────
//
// Workflow service POSTs here when a sensitive member-status-change
// instance reaches a terminal state. Payload is the InstanceCallback
// shape that the workflow engine uses.

type wfCallback struct {
	TenantID    uuid.UUID `json:"tenant_id"`
	Event       string    `json:"event"` // "approved" | "rejected" | "cancelled"
	DeliveredAt time.Time `json:"delivered_at"`
	Instance    struct {
		ID uuid.UUID `json:"id"`
	} `json:"instance"`
}

func (h *StatusHandler) WorkflowCallback(w http.ResponseWriter, r *http.Request) {
	var cb wfCallback
	// Don't use httpx.DecodeJSON here — it disallows unknown fields and
	// the workflow service POSTs the *full* instance object with many
	// fields we don't care about.
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&cb); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid callback body: "+err.Error()))
		return
	}
	if cb.Instance.ID == uuid.Nil || cb.TenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("missing tenant_id or instance.id"))
		return
	}
	err := h.DB.WithTenantTx(r.Context(), cb.TenantID, func(tx pgx.Tx) error {
		proposal, _, _, err := h.Status.ProposalByWorkflowTx(r.Context(), tx, cb.Instance.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Not ours — silently ack so the workflow doesn't retry.
				return nil
			}
			return err
		}
		if proposal.ResolvedAt != nil {
			// Already handled (duplicate delivery).
			return nil
		}
		switch cb.Event {
		case "approved":
			// proposal.CounterpartyID is a counterparty.id post-Phase E A.
			m, err := h.Members.ByCounterpartyTx(r.Context(), tx, proposal.CounterpartyID)
			if err != nil {
				return err
			}
			if err := domain.ValidateTransition(m.Status, proposal.ProposedStatus); err != nil {
				// State drifted since the proposal — record resolution but
				// don't blow up.
				_ = h.Status.ResolveProposalTx(r.Context(), tx, proposal.ID, "approved_but_invalid:"+err.Error())
				return nil
			}
			_, err = h.Status.ApplyTx(r.Context(), tx, store.ApplyInput{
				TenantID:           cb.TenantID,
				CounterpartyID:           proposal.CounterpartyID,
				FromStatus:         m.Status,
				ToStatus:           proposal.ProposedStatus,
				ReasonCategory:     proposal.ReasonCategory,
				ReasonNote:         proposal.ReasonNote,
				ChangedBy:          proposal.ProposedBy,
				WorkflowInstanceID: &cb.Instance.ID,
				ReviewDate:         proposal.ReviewDate,
			})
			if err != nil {
				return err
			}
			return h.Status.ResolveProposalTx(r.Context(), tx, proposal.ID, "approved")
		case "rejected":
			return h.Status.ResolveProposalTx(r.Context(), tx, proposal.ID, "rejected")
		case "cancelled":
			return h.Status.ResolveProposalTx(r.Context(), tx, proposal.ID, "cancelled")
		default:
			return nil
		}
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── helpers ───────────

func (h *StatusHandler) createWorkflowInstance(r *http.Request, tenantID uuid.UUID, m *domain.Member, target domain.MemberStatus, reason domain.StatusReason, note string, actorID uuid.UUID) (uuid.UUID, error) {
	if h.WorkflowURL == "" {
		return uuid.Nil, httpx.ErrBadRequest("workflow service not configured (WORKFLOW_SERVICE_URL)")
	}
	if h.WorkflowProcessKind == "" {
		h.WorkflowProcessKind = "member_status_change"
	}
	callback := strings.TrimRight(h.MemberSelfURL, "/") + "/v1/members/status/callback"
	if h.MemberSelfURL == "" {
		callback = "" // engine will skip delivery and operator must resolve manually
	}
	payload := map[string]any{
		"process_kind": h.WorkflowProcessKind,
		"subject_kind": "member",
		"subject_id":   m.ID.String(),
		"context": map[string]any{
			"counterparty_id":      m.ID,
			"member_no":      m.MemberNo,
			"from_status":    string(m.Status),
			"to_status":      string(target),
			"reason":         string(reason),
			"reason_note":    note,
			"member_name":    m.FullName,
		},
		"callback_url": callback,
		"initiator_id": actorID,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(h.WorkflowURL, "/")+"/v1/workflow-instances",
		bytes.NewReader(body))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Forward the caller's auth header so the workflow service sees the
	// initiator + tenant.
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}
	// Carry the Host header so the workflow service resolves the same tenant.
	req.Host = r.Host
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return uuid.Nil, httpx.ErrBadRequest("workflow service unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return uuid.Nil, httpx.ErrBadRequest("workflow service rejected the instance: " + string(b))
	}
	var envelope struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return uuid.Nil, err
	}
	if envelope.Data.ID == uuid.Nil {
		return uuid.Nil, httpx.ErrBadRequest("workflow service returned no instance id")
	}
	return envelope.Data.ID, nil
}

func (h *StatusHandler) tenantDormancyDays(ctx context.Context, tenantID uuid.UUID) int {
	var d int
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT dormancy_threshold_days FROM tenant_operations WHERE tenant_id = $1`, tenantID).Scan(&d)
	})
	if err != nil || d <= 0 {
		if h.DefaultDormancyDays > 0 {
			return h.DefaultDormancyDays
		}
		return 365
	}
	return d
}

func (h *StatusHandler) audit(r *http.Request, tenantID, memberID uuid.UUID, action string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	actorID, _ := middleware.UserIDFrom(r)
	target := memberID.String()
	kind := "member"
	if memberID == uuid.Nil {
		target = tenantID.String()
		kind = "tenant"
	}
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID: &tenantID, ActorID: nonZero(actorID),
		Action: action, TargetKind: kind, TargetID: target,
		UserAgent: r.UserAgent(), Metadata: meta,
	})
}

func (h *StatusHandler) auditChange(r *http.Request, tenantID, memberID uuid.UUID, from, to domain.MemberStatus, reason domain.StatusReason, note string, wfInstanceID *uuid.UUID) {
	h.audit(r, tenantID, memberID, "member.status_changed", map[string]any{
		"from": from, "to": to,
		"reason": reason, "note": note,
		"workflow_instance_id": wfInstanceID,
	})
}

func isValidStatus(s domain.MemberStatus) bool {
	switch s {
	case domain.StatusPending, domain.StatusActive, domain.StatusDormant,
		domain.StatusSuspended, domain.StatusBlacklisted, domain.StatusExited,
		domain.StatusDeceased, domain.StatusRejected:
		return true
	}
	return false
}

func isValidReason(r domain.StatusReason) bool {
	switch r {
	case domain.ReasonOnboardingApproval, domain.ReasonOnboardingRejection,
		domain.ReasonDormancyInactivity, domain.ReasonReactivationRequest,
		domain.ReasonLoanDefault, domain.ReasonComplianceHold,
		domain.ReasonDisciplinaryAction, domain.ReasonFraudInvestigation,
		domain.ReasonRegulatoryDirective, domain.ReasonMemberRequest,
		domain.ReasonAdminAction, domain.ReasonDeceasedNotification,
		domain.ReasonSystemCorrection, domain.ReasonOther:
		return true
	}
	return false
}

// RunDormancyForTenant is the entry point used by the -run-dormancy CLI
// flag. It returns the number of members transitioned to dormant.
func RunDormancyForTenant(ctx context.Context, h *StatusHandler, tenantID uuid.UUID) (int, error) {
	threshold := h.tenantDormancyDays(ctx, tenantID)
	applied := 0
	err := h.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cands, err := h.Status.DormancyCandidatesTx(ctx, tx, threshold)
		if err != nil {
			return err
		}
		for _, c := range cands {
			if _, err := h.Status.ApplyTx(ctx, tx, store.ApplyInput{
				TenantID:       tenantID,
				CounterpartyID:       c.CounterpartyID,
				FromStatus:     domain.StatusActive,
				ToStatus:       domain.StatusDormant,
				ReasonCategory: domain.ReasonDormancyInactivity,
				ReasonNote:     "auto: inactive for " + dayString(c.DaysInactive),
			}); err != nil {
				return err
			}
			applied++
		}
		return nil
	})
	return applied, err
}

func dayString(d int) string {
	if d == 1 {
		return "1 day"
	}
	return itoa(d) + " days"
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// isMissingShareAccountsTable returns true when the share_accounts
// table doesn't exist in the database. Lets the exit guard degrade
// gracefully in test environments that bring up the member service
// without savings migrations.
func isMissingShareAccountsTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}
