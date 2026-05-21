// Campaign HTTP endpoints.
//
//   GET  /v1/campaigns                  list with filter
//   POST /v1/campaigns                  create (status defaults to draft)
//   GET  /v1/campaigns/{id}             detail (with progress counters)
//   POST /v1/campaigns/{id}/preview     audience count + sample render
//   POST /v1/campaigns/{id}/schedule    set scheduled_for + status=scheduled
//   POST /v1/campaigns/{id}/send        send immediately (worker picks up
//                                       on the next tick)
//   POST /v1/campaigns/{id}/cancel      cancel a draft / scheduled campaign
//   GET  /v1/campaign-settings          maker/checker threshold
//   PUT  /v1/campaign-settings

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type CampaignHandler struct {
	DB        *db.Pool
	Campaigns *store.CampaignStore
	Audience  *store.AudienceStore
	Templates *store.TemplateStore
	Logger    *slog.Logger
}

type campaignReq struct {
	Name         string           `json:"name"`
	Description  *string          `json:"description,omitempty"`
	EventCode    string           `json:"event_code"`
	Channels     []domain.Channel `json:"channels"`
	Audience     map[string]any   `json:"audience"`
	Payload      map[string]any   `json:"payload,omitempty"`
	ScheduledFor *string          `json:"scheduled_for,omitempty"`
}

func (h *CampaignHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in campaignReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Name == "" || in.EventCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name and event_code are required"))
		return
	}
	var scheduledFor *time.Time
	if in.ScheduledFor != nil && *in.ScheduledFor != "" {
		t, err := time.Parse(time.RFC3339, *in.ScheduledFor)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("scheduled_for must be RFC3339"))
			return
		}
		scheduledFor = &t
	}

	tid, _ := middleware.TenantIDFrom(r)
	userID, _ := middleware.UserIDFrom(r)

	// Pre-compute the estimated recipient count so the row carries it
	// from creation. Lets the list view show "X recipients" without
	// re-resolving on every render.
	audienceJSON, _ := json.Marshal(in.Audience)
	filter, ferr := store.ParseAudience(audienceJSON)
	if ferr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(ferr.Error()))
		return
	}

	var (
		estimated int
		out       *domain.Campaign
	)
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		n, err := h.Audience.ResolveCountTx(r.Context(), tx, filter)
		if err != nil {
			return err
		}
		estimated = n
		c, err := h.Campaigns.CreateTx(r.Context(), tx, store.CreateCampaignInput{
			Name:                in.Name,
			Description:         in.Description,
			EventCode:           in.EventCode,
			Channels:            in.Channels,
			Audience:            in.Audience,
			Payload:             in.Payload,
			ScheduledFor:        scheduledFor,
			EstimatedRecipients: estimated,
			Status:              domain.CampaignDraft,
			CreatedBy:           uuidPtr(userID),
		})
		if err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *CampaignHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var c *domain.Campaign
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		c, err = h.Campaigns.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, c)
}

func (h *CampaignHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.CampaignListFilter{
		Status: q.Get("status"),
		Limit:  limit,
		Offset: offset,
	}
	var items []domain.Campaign
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Campaigns.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// ─────────── Preview ───────────

type previewSample struct {
	Channel domain.Channel `json:"channel"`
	Subject *string        `json:"subject,omitempty"`
	Body    string         `json:"body"`
}

func (h *CampaignHandler) Preview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var (
		count   int
		samples []previewSample
		camp    *domain.Campaign
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Campaigns.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		camp = c
		filter, err := store.ParseAudience(c.Audience)
		if err != nil {
			return err
		}
		count, err = h.Audience.ResolveCountTx(r.Context(), tx, filter)
		if err != nil {
			return err
		}
		// Render with placeholder member values so admins can sanity-check
		// the template before hitting send.
		samplePayload := map[string]any{
			"member_no":      "M-2026-PREVIEW",
			"full_name":      "Sample Member",
			"recipient_name": "Sample Member",
		}
		var basePayload map[string]any
		if len(c.Payload) > 0 {
			_ = json.Unmarshal(c.Payload, &basePayload)
		}
		for k, v := range basePayload {
			samplePayload[k] = v
		}
		for _, ch := range c.Channels {
			tpl, err := h.Templates.ActiveByEventChannelTx(r.Context(), tx, c.EventCode, ch)
			if err != nil {
				return err
			}
			if tpl == nil {
				continue
			}
			body := store.RenderTemplate(tpl.Body, samplePayload)
			var subj *string
			if tpl.Subject != nil {
				rendered := store.RenderTemplate(*tpl.Subject, samplePayload)
				subj = &rendered
			}
			samples = append(samples, previewSample{Channel: ch, Subject: subj, Body: body})
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"campaign_id":          camp.ID,
		"event_code":           camp.EventCode,
		"estimated_recipients": count,
		"samples":              samples,
	})
}

// ─────────── State transitions ───────────

type scheduleReq struct {
	ScheduledFor string `json:"scheduled_for"`
}

func (h *CampaignHandler) Schedule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in scheduleReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	t, perr := time.Parse(time.RFC3339, in.ScheduledFor)
	if perr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("scheduled_for must be RFC3339"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Campaigns.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !canTransitionFrom(c.Status) {
			return httpx.ErrConflict("campaign is " + string(c.Status) + "; cannot reschedule")
		}
		return h.Campaigns.UpdateStatusTx(r.Context(), tx, id, domain.CampaignScheduled, map[string]any{
			"scheduled_for": t,
		})
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Send — flips a draft to status=scheduled with scheduled_for=now so the
// CampaignWorker picks it up on the next tick. We avoid fanning out in
// the HTTP path: a campaign with 10k recipients would block the API
// call for minutes.
func (h *CampaignHandler) Send(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Campaigns.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !canTransitionFrom(c.Status) {
			return httpx.ErrConflict("campaign is " + string(c.Status) + "; cannot send")
		}
		return h.Campaigns.UpdateStatusTx(r.Context(), tx, id, domain.CampaignScheduled, map[string]any{
			"scheduled_for": time.Now(),
		})
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type cancelReq struct {
	Reason string `json:"reason,omitempty"`
}

func (h *CampaignHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	// Reason is optional — accept empty body without erroring.
	var in cancelReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, err := h.Campaigns.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if c.Status == domain.CampaignSending || c.Status == domain.CampaignSent {
			return httpx.ErrConflict("campaign already dispatched; cannot cancel")
		}
		fields := map[string]any{
			"cancelled_at": time.Now(),
		}
		if in.Reason != "" {
			fields["cancel_reason"] = in.Reason
		}
		return h.Campaigns.UpdateStatusTx(r.Context(), tx, id, domain.CampaignCancelled, fields)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Settings (maker/checker threshold) ───────────

func (h *CampaignHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var s *domain.CampaignSettings
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		s, err = h.Campaigns.GetSettingsTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, s)
}

type updateCampaignSettingsReq struct {
	ApprovalRecipientThreshold int `json:"approval_recipient_threshold"`
}

func (h *CampaignHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in updateCampaignSettingsReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.ApprovalRecipientThreshold < 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("threshold must be >= 0"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *domain.CampaignSettings
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Campaigns.UpdateSettingsTx(r.Context(), tx, in.ApprovalRecipientThreshold)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Helpers ───────────

func canTransitionFrom(s domain.CampaignStatus) bool {
	switch s {
	case domain.CampaignDraft, domain.CampaignAwaitingApproval, domain.CampaignScheduled:
		return true
	}
	return false
}

// uuidPtr returns nil if the UUID is zero (no user context) — keeps the
// created_by column NULL for system-initiated rows.
func uuidPtr(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}
