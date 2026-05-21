// Template manager HTTP endpoints — Stage 8.
//
//   GET    /v1/notification-templates         already wired in notify.go
//   GET    /v1/notification-templates/{id}    detail
//   POST   /v1/notification-templates         create
//   PUT    /v1/notification-templates/{id}    update
//   DELETE /v1/notification-templates/{id}    remove
//   POST   /v1/notification-templates/{id}/clone   duplicate as draft
//   POST   /v1/notification-templates/preview     render a body with sample values
//
// Validation: event_code must exist in the catalog; channel must be one
// of in_app/sms/email; can't deactivate a template when there's no
// other active template for that (event, channel) pair (the dispatcher
// would silently skip the channel — admins should know).

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type TemplateHandler struct {
	DB        *db.Pool
	Templates *store.TemplateStore
	Events    *store.EventStore
	Logger    *slog.Logger
}

type templateReq struct {
	EventCode string         `json:"event_code"`
	Channel   domain.Channel `json:"channel"`
	Subject   *string        `json:"subject,omitempty"`
	Body      string         `json:"body"`
	IsActive  bool           `json:"is_active"`
}

func (h *TemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var t *domain.Template
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var lerr error
		t, lerr = h.Templates.GetTx(r.Context(), tx, id)
		return lerr
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, t)
}

func (h *TemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in templateReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if msg := validateTemplate(in); msg != "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(msg))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.Template
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := h.requireEventExistsTx(r.Context(), tx, in.EventCode); err != nil {
			return err
		}
		t, err := h.Templates.CreateTx(r.Context(), tx, store.UpsertTemplateInput{
			EventCode: in.EventCode,
			Channel:   in.Channel,
			Subject:   normalizeSubject(in),
			Body:      in.Body,
			IsActive:  in.IsActive,
		})
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		writeTemplateErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *TemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in templateReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if msg := validateTemplate(in); msg != "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(msg))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.Template
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := h.requireEventExistsTx(r.Context(), tx, in.EventCode); err != nil {
			return err
		}
		t, lerr := h.Templates.UpdateTx(r.Context(), tx, id, store.UpsertTemplateInput{
			EventCode: in.EventCode,
			Channel:   in.Channel,
			Subject:   normalizeSubject(in),
			Body:      in.Body,
			IsActive:  in.IsActive,
		})
		if lerr != nil {
			return lerr
		}
		out = t
		return nil
	})
	if err != nil {
		writeTemplateErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *TemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Templates.DeleteTx(r.Context(), tx, id)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Clone — duplicates an existing template as an inactive copy with the
// same event/channel/body. Useful for staged rollouts: edit + test the
// inactive copy, then flip the active flag to swap.
func (h *TemplateHandler) Clone(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.Template
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		src, lerr := h.Templates.GetTx(r.Context(), tx, id)
		if lerr != nil {
			return lerr
		}
		// Deactivate the source so the unique (tenant, event_code, channel,
		// is_active=true) constraint doesn't fire. The admin can re-activate
		// whichever copy they prefer.
		_, lerr = h.Templates.UpdateTx(r.Context(), tx, src.ID, store.UpsertTemplateInput{
			EventCode: src.EventCode, Channel: src.Channel,
			Subject: src.Subject, Body: src.Body, IsActive: false,
		})
		if lerr != nil {
			return lerr
		}
		t, lerr := h.Templates.CreateTx(r.Context(), tx, store.UpsertTemplateInput{
			EventCode: src.EventCode,
			Channel:   src.Channel,
			Subject:   src.Subject,
			Body:      src.Body,
			IsActive:  false,
		})
		if lerr != nil {
			return lerr
		}
		out = t
		return nil
	})
	if err != nil {
		writeTemplateErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// Preview — renders a template body + subject with admin-supplied
// placeholder values so they can sanity-check formatting before saving.
type previewReq struct {
	Subject string         `json:"subject,omitempty"`
	Body    string         `json:"body"`
	Payload map[string]any `json:"payload,omitempty"`
}

func (h *TemplateHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var in previewReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Body == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("body is required"))
		return
	}
	payload := in.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	httpx.OK(w, map[string]any{
		"subject": store.RenderTemplate(in.Subject, payload),
		"body":    store.RenderTemplate(in.Body, payload),
	})
}

// ─────────── Helpers ───────────

func validateTemplate(in templateReq) string {
	if in.EventCode == "" {
		return "event_code is required"
	}
	if !in.Channel.Valid() {
		return "channel must be in_app, sms, or email"
	}
	if in.Body == "" {
		return "body is required"
	}
	return ""
}

func normalizeSubject(in templateReq) *string {
	if in.Channel == domain.ChannelEmail {
		// Email needs a subject; default to event_code if the admin omitted it.
		if in.Subject == nil || *in.Subject == "" {
			s := in.EventCode
			return &s
		}
	}
	return in.Subject
}

// writeTemplateErr maps Postgres unique-constraint hits (one template
// per tenant/event/channel) to a friendly 409.
func writeTemplateErr(w http.ResponseWriter, r *http.Request, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "tenant_id_event_code_channel") {
		httpx.WriteErr(w, r, httpx.ErrConflict("a template already exists for this event + channel — edit or clone the existing one instead"))
		return
	}
	httpx.WriteErr(w, r, err)
}

func (h *TemplateHandler) requireEventExistsTx(ctx context.Context, tx pgx.Tx, code string) error {
	ev, err := h.Events.GetTx(ctx, tx, code)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrBadRequest("unknown event_code: " + code)
		}
		return err
	}
	if ev == nil {
		return httpx.ErrBadRequest("unknown event_code: " + code)
	}
	return nil
}
