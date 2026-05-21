// Orchestrator + HTTP entry points for the Notifications module.
//
// Internal-only endpoint (called by other services):
//   POST /internal/v1/notify           authenticate with X-Internal-Token
//
// User-facing endpoints (JWT auth via subdomain like other services):
//   GET  /v1/notifications             current user's inbox
//   GET  /v1/notifications/unread      unread count for bell badge
//   POST /v1/notifications/{id}/read   mark single notification as read
//   POST /v1/notifications/mark-all-read
//   GET  /v1/notifications/log         tenant-wide audit log (admin)
//   GET  /v1/notification-events       catalog
//   GET  /v1/notification-templates    list active templates

package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type Handler struct {
	DB            *db.Pool
	Events        *store.EventStore
	Templates     *store.TemplateStore
	Notifications *store.NotificationStore
	Tenants       *store.TenantStore
	InternalToken string
	Logger        *slog.Logger
}

// ─────────── Internal: POST /internal/v1/notify ───────────

type notifyReq struct {
	TenantID          uuid.UUID         `json:"tenant_id"`
	EventCode         string            `json:"event_code"`
	Priority          domain.Priority   `json:"priority,omitempty"`
	Channels          []domain.Channel  `json:"channels,omitempty"` // override default_channels
	RecipientMemberID *uuid.UUID        `json:"recipient_member_id,omitempty"`
	RecipientUserID   *uuid.UUID        `json:"recipient_user_id,omitempty"`
	RecipientName     string            `json:"recipient_name,omitempty"`
	RecipientPhone    *string           `json:"recipient_phone,omitempty"`
	RecipientEmail    *string           `json:"recipient_email,omitempty"`
	SourceModule      *string           `json:"source_module,omitempty"`
	SourceRecordID    *uuid.UUID        `json:"source_record_id,omitempty"`
	DeepLink          *string           `json:"deep_link,omitempty"`
	Payload           map[string]any    `json:"payload,omitempty"`
	InitiatedBy       *uuid.UUID        `json:"initiated_by,omitempty"`
}

type notifyResp struct {
	Notification domain.Notification `json:"notification"`
	Deliveries   []domain.Delivery   `json:"deliveries"`
}

// Notify is the single entry-point for every module in the platform.
func (h *Handler) Notify(w http.ResponseWriter, r *http.Request) {
	// Shared-secret check — only services on the cluster network call this.
	if h.InternalToken != "" {
		if got := r.Header.Get("X-Internal-Token"); got != h.InternalToken {
			httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
			return
		}
	}
	var in notifyReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id is required"))
		return
	}
	if in.EventCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("event_code is required"))
		return
	}
	if in.RecipientMemberID == nil && in.RecipientUserID == nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("recipient_member_id or recipient_user_id is required"))
		return
	}

	var out notifyResp
	err := h.DB.WithTenantTx(r.Context(), in.TenantID, func(tx pgx.Tx) error {
		event, err := h.Events.GetTx(r.Context(), tx, in.EventCode)
		if err != nil {
			return err
		}
		if !event.IsActive {
			return httpx.ErrConflict("event " + in.EventCode + " is not active")
		}

		// Resolve channel set: explicit override, else event defaults.
		channels := in.Channels
		if len(channels) == 0 {
			channels = event.DefaultChannels
		}

		priority := in.Priority
		if priority == "" {
			priority = event.DefaultPriority
		}

		// Insert the notification row first — deliveries reference it.
		n, err := h.Notifications.CreateTx(r.Context(), tx, store.CreateInput{
			EventCode:         in.EventCode,
			Priority:          priority,
			RecipientMemberID: in.RecipientMemberID,
			RecipientUserID:   in.RecipientUserID,
			RecipientName:     in.RecipientName,
			RecipientPhone:    in.RecipientPhone,
			RecipientEmail:    in.RecipientEmail,
			SourceModule:      in.SourceModule,
			SourceRecordID:    in.SourceRecordID,
			DeepLink:          in.DeepLink,
			Payload:           in.Payload,
			InitiatedBy:       in.InitiatedBy,
		})
		if err != nil {
			return err
		}

		// Render + persist a delivery row per channel.
		deliveries := []domain.Delivery{}
		for _, ch := range channels {
			if !ch.Valid() {
				continue
			}
			tpl, err := h.Templates.ActiveByEventChannelTx(r.Context(), tx, in.EventCode, ch)
			if err != nil {
				return err
			}
			// Fall-back body — never silently drop a notification just
			// because a template is missing.
			body := in.EventCode + ": " + n.RecipientName
			var subject *string
			var templateID *uuid.UUID
			if tpl != nil {
				body = store.RenderTemplate(tpl.Body, in.Payload)
				if tpl.Subject != nil {
					rendered := store.RenderTemplate(*tpl.Subject, in.Payload)
					subject = &rendered
				}
				id := tpl.ID
				templateID = &id
			}
			d, err := h.Notifications.CreateDeliveryTx(r.Context(), tx, store.CreateDeliveryInput{
				NotificationID: n.ID,
				Channel:        ch,
				TemplateID:     templateID,
				Subject:        subject,
				Body:           body,
			})
			if err != nil {
				return err
			}
			deliveries = append(deliveries, *d)
		}

		out.Notification = *n
		out.Deliveries = deliveries
		return nil
	})
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func writeNotifyErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("unknown event_code"))
		return
	}
	httpx.WriteErr(w, r, err)
}

// ─────────── User-facing: feed + read state ───────────

func (h *Handler) Feed(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	unread := q.Get("unread") == "1"

	var items []domain.FeedItem
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Notifications.FeedForRecipientTx(r.Context(), tx, store.FeedFilter{
			UserID:     &userID,
			UnreadOnly: unread,
			Limit:      limit,
			Offset:     offset,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

func (h *Handler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var n int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		n, err = h.Notifications.UnreadCountForUserTx(r.Context(), tx, userID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"unread": n})
}

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid notification id"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Notifications.MarkReadTx(r.Context(), tx, id, &userID)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var n int64
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		n, err = h.Notifications.MarkAllReadForUserTx(r.Context(), tx, userID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"marked": n})
}

// ─────────── Admin: audit log + catalog ───────────

func (h *Handler) Log(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	type row struct {
		domain.Notification
		Deliveries []domain.Delivery `json:"deliveries"`
	}
	var out []row
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM notifications`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT `+notificationColsForRead+`
			FROM notifications
			ORDER BY created_at DESC
			LIMIT $1 OFFSET $2
		`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		notifs := []domain.Notification{}
		for rows.Next() {
			var n domain.Notification
			var prio string
			var payload []byte
			if err := rows.Scan(
				&n.ID, &n.TenantID, &n.EventCode, &prio,
				&n.RecipientMemberID, &n.RecipientUserID, &n.RecipientName, &n.RecipientPhone, &n.RecipientEmail,
				&n.SourceModule, &n.SourceRecordID, &n.DeepLink,
				&payload, &n.InitiatedBy, &n.CreatedAt,
			); err != nil {
				return err
			}
			n.Priority = domain.Priority(prio)
			n.Payload = payload
			notifs = append(notifs, n)
		}
		out = make([]row, 0, len(notifs))
		for _, n := range notifs {
			ds, err := h.Notifications.DeliveriesByNotificationTx(r.Context(), tx, n.ID)
			if err != nil {
				return err
			}
			out = append(out, row{Notification: n, Deliveries: ds})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": out, "total": total})
}

const notificationColsForRead = `
	id, tenant_id, event_code, priority,
	recipient_member_id, recipient_user_id, recipient_name, recipient_phone, recipient_email,
	source_module, source_record_id, deep_link,
	payload, initiated_by, created_at
`

func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.Event
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Events.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.Template
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Templates.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}
