// Phase-1 follow-up — Comments tab on the loan application + loan
// detail pages.
//
// Hybrid model: visibility 'internal' is officer-only; 'external' fires
// an SMS to the borrower with a link to /m/c/{reply_token} on the
// admin SPA. The public reply route lets the borrower respond — the
// reply lands as an author_member_id comment on the same thread.

package handler

import (
	"context"
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
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type LoanCommentsHandler struct {
	DB       *db.Pool
	Comments *store.LoanCommentsStore
	Notifier *notifier.Client
	Logger   *slog.Logger

	// PublicBaseURL — base for the member-side reply link
	// (https://{tenant}.nexussacco.local). Empty falls back to localhost.
	PublicBaseURL string
}

// ─────────── helpers ───────────

func (h *LoanCommentsHandler) publicBase(tenantSlug string) string {
	base := strings.TrimRight(h.PublicBaseURL, "/")
	if base == "" {
		base = "http://localhost:5173"
	}
	if tenantSlug == "" || !strings.Contains(base, "{slug}") {
		return base
	}
	return strings.ReplaceAll(base, "{slug}", tenantSlug)
}

// resolveBodyFromTemplate — when template_id is supplied, fetch the
// template and use its body. Visibility from the template must match
// the request's visibility (so a tenant can't accidentally send an
// external-shaped body internally).
func (h *LoanCommentsHandler) resolveBodyFromTemplate(ctx context.Context, tx pgx.Tx, templateID *uuid.UUID, visibility string, supplied string) (string, error) {
	if templateID == nil {
		if strings.TrimSpace(supplied) == "" {
			return "", httpx.ErrBadRequest("body is required when no template_id is supplied")
		}
		return supplied, nil
	}
	t, err := h.Comments.GetTemplateTx(ctx, tx, *templateID)
	if err != nil {
		if errors.Is(err, store.ErrCommentTemplateBad) {
			return "", httpx.ErrBadRequest("template not found")
		}
		return "", err
	}
	if t.Visibility != visibility {
		return "", httpx.ErrBadRequest("template visibility does not match the requested visibility")
	}
	body := t.Body
	if strings.TrimSpace(supplied) != "" {
		body = supplied // caller may have edited the template before posting
	}
	return body, nil
}

// ─────────── Post ───────────

type postCommentReq struct {
	Visibility      string    `json:"visibility"`
	Body            string    `json:"body"`
	ParentID        *string   `json:"parent_id,omitempty"`
	AttachmentPaths []string  `json:"attachment_paths,omitempty"`
	TemplateID      *string   `json:"template_id,omitempty"`
}

func (h *LoanCommentsHandler) PostForApplication(w http.ResponseWriter, r *http.Request) {
	h.post(w, r, true)
}

func (h *LoanCommentsHandler) PostForLoan(w http.ResponseWriter, r *http.Request) {
	h.post(w, r, false)
}

func (h *LoanCommentsHandler) post(w http.ResponseWriter, r *http.Request, forApp bool) {
	idParam := "loan_id"
	if forApp {
		idParam = "app_id"
	}
	targetID, err := uuid.Parse(chi.URLParam(r, idParam))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid "+idParam))
		return
	}
	var in postCommentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Visibility != "internal" && in.Visibility != "external" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("visibility must be internal | external"))
		return
	}
	var parentID *uuid.UUID
	if in.ParentID != nil && *in.ParentID != "" {
		p, perr := uuid.Parse(*in.ParentID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid parent_id"))
			return
		}
		parentID = &p
	}
	var templateID *uuid.UUID
	if in.TemplateID != nil && *in.TemplateID != "" {
		t, perr := uuid.Parse(*in.TemplateID)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid template_id"))
			return
		}
		templateID = &t
	}

	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	tenantSlug := middleware.TenantSlugFrom(r)
	var replyToken *uuid.UUID
	if in.Visibility == "external" {
		t := uuid.New()
		replyToken = &t
	}

	var (
		created       *domain.LoanComment
		applicantPhone string
		applicantName  string
		applicantMemberID *uuid.UUID
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		body, berr := h.resolveBodyFromTemplate(r.Context(), tx, templateID, in.Visibility, in.Body)
		if berr != nil {
			return berr
		}

		// Interpolate the small set of placeholders for external bodies.
		if in.Visibility == "external" {
			body, applicantName, applicantPhone, applicantMemberID = h.interpolatePlaceholdersTx(r.Context(), tx, forApp, targetID, body)
		}

		uidCopy := uid
		postInput := store.PostCommentInput{
			ParentID:      parentID,
			Visibility:    in.Visibility,
			Body:          body,
			Attachments:   in.AttachmentPaths,
			AuthorUserID:  &uidCopy,
			ReplyToken:    replyToken,
		}
		if forApp {
			postInput.ApplicationID = &targetID
		} else {
			postInput.LoanID = &targetID
		}
		c, perr := h.Comments.PostTx(r.Context(), tx, postInput)
		if perr != nil {
			return perr
		}
		created = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// External path: fire the SMS post-commit. Best-effort; the
	// notifier client never errors fatally.
	if in.Visibility == "external" && created != nil && h.Notifier != nil && applicantPhone != "" {
		tenantName := tenantSlug
		shortRef := created.ID.String()[:8]
		base := h.publicBase(tenantSlug)
		shortURL := fmt.Sprintf("%s/m/c/%s", strings.TrimRight(base, "/"), created.ReplyToken)
		bodyClip := created.Body
		if len(bodyClip) > 120 {
			bodyClip = bodyClip[:120] + "…"
		}
		sms := fmt.Sprintf("Hi %s, %s sent you a message about your loan: \"%s\". Reply via %s. Ref: %s",
			applicantName, tenantName, bodyClip, shortURL, shortRef,
		)
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tid,
			EventCode:         "GUARANTOR_CONSENT_REQUEST", // reuse passthrough event
			Channels:          []notifier.Channel{notifier.ChannelSMS},
			RecipientMemberID: applicantMemberID,
			RecipientName:     applicantName,
			RecipientPhone:    &applicantPhone,
			Payload:           map[string]any{"body": sms},
			InitiatedBy:       &uid,
		})
	}
	httpx.OK(w, created)
}

// interpolatePlaceholdersTx — substitutes {member_name} + a couple of
// other handy placeholders on the body. Best-effort: if a lookup
// fails the placeholder stays literal.
//
// Returns (rendered body, applicant_name, applicant_phone, applicant_member_id).
func (h *LoanCommentsHandler) interpolatePlaceholdersTx(
	ctx context.Context, tx pgx.Tx, forApp bool, targetID uuid.UUID, body string,
) (string, string, string, *uuid.UUID) {
	var fullName, phone string
	var memberID *uuid.UUID
	if forApp {
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(cd.full_name, ''), COALESCE(m.phone, ''), m.id
			  FROM loan_applications a
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			  LEFT JOIN members m ON m.id = cd.member_id
			 WHERE a.id = $1
		`, targetID).Scan(&fullName, &phone, &memberID)
	} else {
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(cd.full_name, ''), COALESCE(m.phone, ''), m.id
			  FROM loans l
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			  LEFT JOIN members m ON m.id = cd.member_id
			 WHERE l.id = $1
		`, targetID).Scan(&fullName, &phone, &memberID)
	}
	out := body
	out = strings.ReplaceAll(out, "{member_name}", fullName)
	return out, fullName, phone, memberID
}

// ─────────── List ───────────

func (h *LoanCommentsHandler) ListForApplication(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, true)
}

func (h *LoanCommentsHandler) ListForLoan(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, false)
}

func (h *LoanCommentsHandler) list(w http.ResponseWriter, r *http.Request, forApp bool) {
	idParam := "loan_id"
	if forApp {
		idParam = "app_id"
	}
	targetID, err := uuid.Parse(chi.URLParam(r, idParam))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid "+idParam))
		return
	}
	includeExternal := r.URL.Query().Get("include_external") != "false"
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanComment
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var lerr error
		if forApp {
			items, lerr = h.Comments.ListByApplicationTx(r.Context(), tx, targetID, includeExternal)
		} else {
			items, lerr = h.Comments.ListByLoanTx(r.Context(), tx, targetID, includeExternal)
		}
		return lerr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Edit / Pin / Delete ───────────

type editCommentReq struct {
	Body string `json:"body"`
}

func (h *LoanCommentsHandler) Edit(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in editCommentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("body required"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var updated *domain.LoanComment
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Comments.EditTx(r.Context(), tx, id, uid, in.Body)
		if err != nil {
			return err
		}
		updated = c
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrCommentForbidden) {
			httpx.WriteErr(w, r, httpx.ErrForbidden("only the author may edit"))
			return
		}
		if errors.Is(err, store.ErrCommentNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("comment not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

type pinCommentReq struct {
	Pinned bool `json:"pinned"`
}

func (h *LoanCommentsHandler) Pin(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in pinCommentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var updated *domain.LoanComment
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Comments.PinTx(r.Context(), tx, id, in.Pinned)
		if err != nil {
			return err
		}
		updated = c
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrCommentNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("comment not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

func (h *LoanCommentsHandler) SoftDelete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Comments.SoftDeleteTx(r.Context(), tx, id, uid)
	})
	if err != nil {
		if errors.Is(err, store.ErrCommentForbidden) {
			httpx.WriteErr(w, r, httpx.ErrForbidden("only the author may delete"))
			return
		}
		if errors.Is(err, store.ErrCommentNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("comment not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Templates + search ───────────

func (h *LoanCommentsHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanCommentTemplate
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var lerr error
		items, lerr = h.Comments.ListTemplatesTx(r.Context(), tx)
		return lerr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *LoanCommentsHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("q is required"))
		return
	}
	var appIDPtr, loanIDPtr *uuid.UUID
	if s := r.URL.Query().Get("application_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid application_id"))
			return
		}
		appIDPtr = &id
	}
	if s := r.URL.Query().Get("loan_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid loan_id"))
			return
		}
		loanIDPtr = &id
	}
	if (appIDPtr == nil) == (loanIDPtr == nil) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("supply exactly one of application_id or loan_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanComment
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var serr error
		items, serr = h.Comments.SearchTx(r.Context(), tx, appIDPtr, loanIDPtr, q)
		return serr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Public reply route ───────────
//
// GET  /p/comments/{token} — load the thread + mark external comments read.
// POST /p/comments/{token}/reply — post a member-authored reply.

type PublicCommentsHandler struct {
	DB       *db.Pool
	Comments *store.LoanCommentsStore
	Logger   *slog.Logger
}

func (h *PublicCommentsHandler) withTokenContext(r *http.Request, fn func(ctx context.Context, tx pgx.Tx, commentID, tenantID uuid.UUID) error) error {
	raw := chi.URLParam(r, "token")
	if raw == "" {
		return httpx.ErrBadRequest("missing token")
	}
	tok, err := uuid.Parse(raw)
	if err != nil {
		return httpx.ErrBadRequest("invalid token")
	}
	commentID, tenantID, err := h.Comments.FindTenantByToken(r.Context(), tok)
	if err != nil {
		if errors.Is(err, store.ErrCommentNotFound) {
			return httpx.ErrNotFound("link not found")
		}
		return err
	}
	return h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		return fn(r.Context(), tx, commentID, tenantID)
	})
}

func (h *PublicCommentsHandler) Get(w http.ResponseWriter, r *http.Request) {
	var resp map[string]any
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, commentID, tid uuid.UUID) error {
		root, err := h.Comments.GetTx(ctx, tx, commentID)
		if err != nil {
			return err
		}
		var items []domain.LoanComment
		if root.ApplicationID != nil {
			items, err = h.Comments.ListByApplicationTx(ctx, tx, *root.ApplicationID, true)
		} else if root.LoanID != nil {
			items, err = h.Comments.ListByLoanTx(ctx, tx, *root.LoanID, true)
		}
		if err != nil {
			return err
		}
		// Member only sees the external slice.
		external := items[:0]
		for _, c := range items {
			if c.Visibility == "external" {
				external = append(external, c)
			}
		}
		resp = map[string]any{
			"items": external,
			"total": len(external),
		}
		// Mark every external comment in the thread as read.
		if root.ReplyToken != nil {
			_, _ = h.Comments.MarkMemberReadByTokenTx(ctx, tx, *root.ReplyToken)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

type publicReplyReq struct {
	Body string `json:"body"`
}

func (h *PublicCommentsHandler) Reply(w http.ResponseWriter, r *http.Request) {
	var in publicReplyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("body is required"))
		return
	}
	var created *domain.LoanComment
	err := h.withTokenContext(r, func(ctx context.Context, tx pgx.Tx, commentID, tid uuid.UUID) error {
		root, err := h.Comments.GetTx(ctx, tx, commentID)
		if err != nil {
			return err
		}
		// Find the member-id from the application / loan target so the
		// comment row carries author_member_id.
		var memberID uuid.UUID
		if root.ApplicationID != nil {
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(m.id, '00000000-0000-0000-0000-000000000000'::uuid)
				  FROM loan_applications a
				  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
				  LEFT JOIN members m ON m.id = cd.member_id
				 WHERE a.id = $1
			`, *root.ApplicationID).Scan(&memberID); err != nil {
				return err
			}
		} else if root.LoanID != nil {
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(m.id, '00000000-0000-0000-0000-000000000000'::uuid)
				  FROM loans l
				  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
				  LEFT JOIN members m ON m.id = cd.member_id
				 WHERE l.id = $1
			`, *root.LoanID).Scan(&memberID); err != nil {
				return err
			}
		}
		if memberID == uuid.Nil {
			return httpx.ErrConflict("cannot identify the member from this thread; ask SACCO staff to follow up")
		}
		in := store.PostCommentInput{
			ApplicationID:  root.ApplicationID,
			LoanID:         root.LoanID,
			ParentID:       &commentID,
			Visibility:     "external",
			Body:           body,
			AuthorMemberID: &memberID,
		}
		c, perr := h.Comments.PostTx(ctx, tx, in)
		if perr != nil {
			return perr
		}
		created = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, created)
}
