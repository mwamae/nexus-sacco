// Campaign worker — drains scheduled campaigns whose scheduled_for
// has elapsed. Iterates each due campaign's audience, fires one
// notification per recipient through the regular pipeline (which in
// turn creates in_app deliveries immediately and queues sms/email
// rows for the existing channel workers).

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

type CampaignWorker struct {
	DB            *db.Pool
	Notifs        *store.NotificationStore
	Templates     *store.TemplateStore
	Campaigns     *store.CampaignStore
	Audience      *store.AudienceStore
	TickInterval  time.Duration
	Logger        *slog.Logger
}

func (w *CampaignWorker) Run(ctx context.Context) {
	tick := w.TickInterval
	if tick <= 0 {
		tick = 15 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	w.Logger.Info("campaign worker started", "tick_seconds", tick.Seconds())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.processOnce(ctx)
		}
	}
}

func (w *CampaignWorker) processOnce(ctx context.Context) {
	var tenantIDs []uuid.UUID
	err := w.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		tenantIDs, err = w.Notifs.AllActiveTenantsTx(ctx, tx)
		return err
	})
	if err != nil {
		w.Logger.Warn("campaign worker: list tenants failed", "err", err)
		return
	}
	for _, tid := range tenantIDs {
		w.processTenant(ctx, tid)
	}
}

func (w *CampaignWorker) processTenant(ctx context.Context, tenantID uuid.UUID) {
	for {
		var camp *domain.Campaign
		err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			c, err := w.Campaigns.ClaimNextScheduledTx(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			camp = c
			return nil
		})
		if err != nil {
			w.Logger.Warn("campaign worker: claim failed", "tenant", tenantID, "err", err)
			return
		}
		if camp == nil {
			return // nothing due
		}
		w.dispatch(ctx, tenantID, camp)
	}
}

func (w *CampaignWorker) dispatch(ctx context.Context, tenantID uuid.UUID, camp *domain.Campaign) {
	logger := w.Logger.With("campaign", camp.ID, "tenant", tenantID)
	logger.Info("campaign dispatch started", "name", camp.Name, "scheduled_for", camp.ScheduledFor)

	// Resolve audience.
	filter, err := store.ParseAudience(camp.Audience)
	if err != nil {
		w.finalize(ctx, tenantID, camp.ID, 0, 0, domain.CampaignFailed, err.Error())
		logger.Error("campaign audience parse failed", "err", err)
		return
	}
	var recipients []store.AudienceRecipient
	err = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		recipients, err = w.Audience.ResolveTx(ctx, tx, filter)
		return err
	})
	if err != nil {
		w.finalize(ctx, tenantID, camp.ID, 0, 0, domain.CampaignFailed, err.Error())
		logger.Error("campaign audience resolve failed", "err", err)
		return
	}
	logger.Info("campaign audience resolved", "recipients", len(recipients))

	// Open a run row.
	var runID uuid.UUID
	_ = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r, err := w.Campaigns.CreateRunTx(ctx, tx, camp.ID, len(recipients))
		if err != nil {
			return err
		}
		runID = r.ID
		return nil
	})

	// Update total_recipients on the campaign row.
	_ = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE notification_campaigns SET total_recipients = $2 WHERE id = $1`,
			camp.ID, len(recipients))
		return err
	})

	// Parse base payload once.
	var basePayload map[string]any
	_ = json.Unmarshal(camp.Payload, &basePayload)
	if basePayload == nil {
		basePayload = map[string]any{}
	}

	dispatched := 0
	failed := 0
	for _, r := range recipients {
		if err := w.dispatchOne(ctx, tenantID, camp, basePayload, r); err != nil {
			failed++
			logger.Warn("campaign recipient failed", "member", r.MemberID, "err", err)
			_ = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
				return w.Campaigns.IncrementProgressTx(ctx, tx, camp.ID, 0, 1)
			})
			continue
		}
		dispatched++
		_ = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			return w.Campaigns.IncrementProgressTx(ctx, tx, camp.ID, 1, 0)
		})
	}

	// Finalize.
	_ = w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Campaigns.FinishRunTx(ctx, tx, runID, dispatched, failed, "")
	})
	w.finalize(ctx, tenantID, camp.ID, dispatched, failed, domain.CampaignSent, "")
	logger.Info("campaign dispatch complete", "dispatched", dispatched, "failed", failed)
}

// dispatchOne — creates one notification + per-channel delivery rows
// for a single recipient. Same persistence pattern as the Notify HTTP
// handler; the SMS/email workers pick up the queued rows.
func (w *CampaignWorker) dispatchOne(
	ctx context.Context, tenantID uuid.UUID,
	camp *domain.Campaign, basePayload map[string]any, r store.AudienceRecipient,
) error {
	return w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Per-recipient payload: base + auto-injected member vars.
		payload := make(map[string]any, len(basePayload)+4)
		for k, v := range basePayload {
			payload[k] = v
		}
		payload["member_no"] = r.MemberNo
		payload["full_name"] = r.FullName
		payload["recipient_name"] = r.FullName

		memberID := r.MemberID
		sourceModule := "notification.campaign"
		sourceRecord := camp.ID
		notif, err := w.Notifs.CreateTx(ctx, tx, store.CreateInput{
			EventCode:         camp.EventCode,
			Priority:          domain.PriorityInfo,
			RecipientMemberID: &memberID,
			RecipientName:     r.FullName,
			RecipientPhone:    r.Phone,
			RecipientEmail:    r.Email,
			SourceModule:      &sourceModule,
			SourceRecordID:    &sourceRecord,
			Payload:           payload,
		})
		if err != nil {
			return err
		}
		for _, ch := range camp.Channels {
			if !ch.Valid() {
				continue
			}
			tpl, err := w.Templates.ActiveByEventChannelTx(ctx, tx, camp.EventCode, ch)
			if err != nil {
				return err
			}
			body := camp.EventCode + ": " + r.FullName
			var subject *string
			var templateID *uuid.UUID
			if tpl != nil {
				body = store.RenderTemplate(tpl.Body, payload)
				if tpl.Subject != nil {
					rendered := store.RenderTemplate(*tpl.Subject, payload)
					subject = &rendered
				}
				id := tpl.ID
				templateID = &id
			}
			if _, err := w.Notifs.CreateDeliveryTx(ctx, tx, store.CreateDeliveryInput{
				NotificationID: notif.ID,
				Channel:        ch,
				TemplateID:     templateID,
				Subject:        subject,
				Body:           body,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (w *CampaignWorker) finalize(
	ctx context.Context, tenantID, campaignID uuid.UUID,
	dispatched, failed int, status domain.CampaignStatus, reason string,
) {
	updates := map[string]any{
		"sent_at": time.Now(),
	}
	if status == domain.CampaignFailed {
		updates["failure_reason"] = reason
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Campaigns.UpdateStatusTx(ctx, tx, campaignID, status, updates)
	})
	if err != nil {
		w.Logger.Warn("campaign worker: finalize failed", "campaign", campaignID, "err", err)
	}
	_ = dispatched
	_ = failed
	_ = fmt.Sprint("")
}
