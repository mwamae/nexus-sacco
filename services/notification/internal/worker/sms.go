// SMS worker — mirror of email worker, with two differences:
//   1. Throttling. Each tenant declares max-per-minute on its
//      notification_sms_configs row; the worker claims at most
//      ceil(rate_per_minute / 6) rows per 10s tick.
//   2. Mock provider. When provider='mock' the worker simulates a
//      successful send (no network), and the audit-log shows
//      status=sent with a MOCK-* message id.

package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/sms"
	"github.com/nexussacco/notification/internal/store"
)

type SMSWorker struct {
	DB           *db.Pool
	Notifs       *store.NotificationStore
	SMSStore     *store.SMSConfigStore
	HTTPClient   *http.Client
	TickInterval time.Duration
	Logger       *slog.Logger
}

func (w *SMSWorker) Run(ctx context.Context) {
	tick := w.TickInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	if w.HTTPClient == nil {
		w.HTTPClient = sms.DefaultClient()
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	w.Logger.Info("sms worker started", "tick_seconds", tick.Seconds())
	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("sms worker stopped")
			return
		case <-t.C:
			w.processOnce(ctx)
		}
	}
}

var errNoSMSConfig = errors.New("no active sms config")

func (w *SMSWorker) processOnce(ctx context.Context) {
	var tenantIDs []uuid.UUID
	err := w.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		tenantIDs, err = w.Notifs.AllActiveTenantsTx(ctx, tx)
		return err
	})
	if err != nil {
		w.Logger.Warn("sms worker: list tenants failed", "err", err)
		return
	}
	for _, tid := range tenantIDs {
		w.processTenant(ctx, tid)
	}
}

func (w *SMSWorker) processTenant(ctx context.Context, tenantID uuid.UUID) {
	var batch []store.DueSMSDelivery
	var cfg *domain.SMSConfig
	tick := w.TickInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, err := w.SMSStore.GetByTenantTx(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		cfg = c
		if cfg == nil || !cfg.IsActive {
			return errNoSMSConfig
		}
		// Per-tick budget derived from the tenant's per-minute cap.
		ticksPerMinute := math.Max(1, 60.0/tick.Seconds())
		batchCap := int(math.Ceil(float64(cfg.RatePerMinute) / ticksPerMinute))
		if batchCap < 1 {
			batchCap = 1
		}
		var err2 error
		batch, err2 = w.Notifs.ClaimDueSMSForTenantTx(ctx, tx, batchCap)
		return err2
	})
	if err == errNoSMSConfig {
		return
	}
	if err != nil {
		w.Logger.Warn("sms worker: claim failed", "tenant", tenantID, "err", err)
		return
	}
	if len(batch) == 0 {
		return
	}
	for _, d := range batch {
		w.deliver(ctx, tenantID, cfg, d)
	}
}

func (w *SMSWorker) deliver(ctx context.Context, tenantID uuid.UUID, cfg *domain.SMSConfig, d store.DueSMSDelivery) {
	if d.RecipientPhone == "" {
		w.finalize(ctx, tenantID, d.DeliveryID,
			fmt.Sprintf("recipient_phone missing on notification %s", d.NotificationID))
		return
	}
	res, err := sms.Send(w.HTTPClient, cfg, sms.Message{
		To:   d.RecipientPhone,
		From: cfg.SenderID,
		Body: d.Body,
	})
	if err == nil {
		w.markSent(ctx, tenantID, d.DeliveryID, res.ProviderMessageID, cfg.Provider)
		return
	}
	w.handleFailure(ctx, tenantID, d, err)
}

func (w *SMSWorker) markSent(
	ctx context.Context, tenantID, deliveryID uuid.UUID,
	providerMessageID string, provider domain.SMSProvider,
) {
	id := providerMessageID
	pmid := &id
	if id == "" {
		pmid = nil
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := w.Notifs.MarkSentTx(ctx, tx, deliveryID, pmid); err != nil {
			return err
		}
		// Mock provider has no delivery-report callback, so promote
		// straight from sent → delivered to give the audit log a clean
		// terminal state.
		if provider == domain.SMSProviderMock {
			return w.Notifs.MarkDeliveredTx(ctx, tx, deliveryID)
		}
		return nil
	})
	if err != nil {
		w.Logger.Warn("sms worker: mark sent failed", "delivery", deliveryID, "err", err)
		return
	}
	w.Logger.Info("sms sent", "delivery", deliveryID, "provider", provider, "provider_msg_id", providerMessageID)
}

func (w *SMSWorker) handleFailure(ctx context.Context, tenantID uuid.UUID, d store.DueSMSDelivery, sendErr error) {
	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	retriesUsed := d.AttemptCount - 1
	if retriesUsed >= 3 {
		w.finalize(ctx, tenantID, d.DeliveryID, sendErr.Error())
		return
	}
	delay, ok := retryBackoff(retriesUsed + 1)
	if !ok {
		w.finalize(ctx, tenantID, d.DeliveryID, sendErr.Error())
		return
	}
	next := time.Now().Add(delay)
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkFailedRetryableTx(ctx, tx, d.DeliveryID, sendErr.Error(), next)
	})
	if err != nil {
		w.Logger.Warn("sms worker: schedule retry failed", "delivery", d.DeliveryID, "err", err)
		return
	}
	w.Logger.Info("sms send failed — retry scheduled",
		"delivery", d.DeliveryID, "attempt", d.AttemptCount,
		"retry_at", next.Format(time.RFC3339), "reason", sendErr)
}

func (w *SMSWorker) finalize(ctx context.Context, tenantID, deliveryID uuid.UUID, reason string) {
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkFailedFinalTx(ctx, tx, deliveryID, reason)
	})
	if err != nil {
		w.Logger.Warn("sms worker: finalize failed", "delivery", deliveryID, "err", err)
		return
	}
	w.Logger.Error("sms delivery permanently failed", "delivery", deliveryID, "reason", reason)
}
