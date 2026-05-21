// SMS worker — Stage 9 refactor.
//
// What changed:
//   • The Africa's Talking credentials live on a single platform-owned
//     row (platform_sms_config); the worker loads them once per tick
//     instead of fetching a per-tenant row.
//   • Before sending, the worker checks the tenant's prepaid SMS
//     credit balance. balance < 1 → delivery is marked `blocked`
//     with reason=insufficient_credits (NO retry — admins must top
//     up first). balance >= 1 → send proceeds.
//   • Credits are debited AFTER the provider returns success, in the
//     same tx that flips the delivery to `sent`. A transient provider
//     failure rolls back nothing (because no debit happened) and the
//     delivery is queued for retry per the existing backoff policy.
//   • Low/zero balance alerts are not fired here; that's a separate
//     polling worker that watches every balance change.

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
	PlatformSMS  *store.PlatformSMSStore
	Credits      *store.CreditStore
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

var errNoSMSConfig = errors.New("no active platform sms config")

// processOnce: load the platform config once per tick, then walk each
// tenant and drain its credit-permitted queue.
func (w *SMSWorker) processOnce(ctx context.Context) {
	platformCfg, err := w.PlatformSMS.Get(ctx)
	if err != nil {
		w.Logger.Warn("sms worker: load platform config failed", "err", err)
		return
	}
	if platformCfg == nil || !platformCfg.IsEnabled {
		return
	}
	// Build the per-tick batch cap from the platform's rate limit;
	// applied per-tenant (i.e. each tenant gets up to this many sends
	// per tick — simpler than fair-sharing in this iteration).
	tick := w.TickInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	ticksPerMinute := math.Max(1, 60.0/tick.Seconds())
	batchCap := int(math.Ceil(float64(platformCfg.RatePerMinute) / ticksPerMinute))
	if batchCap < 1 {
		batchCap = 1
	}

	var tenantIDs []uuid.UUID
	err = w.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		tenantIDs, err = w.Notifs.AllActiveTenantsTx(ctx, tx)
		return err
	})
	if err != nil {
		w.Logger.Warn("sms worker: list tenants failed", "err", err)
		return
	}
	smsCfg := platformConfigToTenantShape(platformCfg)
	for _, tid := range tenantIDs {
		w.processTenant(ctx, tid, smsCfg, batchCap)
	}
}

func (w *SMSWorker) processTenant(ctx context.Context, tenantID uuid.UUID, cfg *domain.SMSConfig, batchCap int) {
	var batch []store.DueSMSDelivery
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		batch, err = w.Notifs.ClaimDueSMSForTenantTx(ctx, tx, batchCap)
		return err
	})
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
	// Credit pre-check. Reads w/o lock; the real authoritative debit
	// happens post-send in the SELECT FOR UPDATE branch below. A racer
	// could drain between this check and the post-send debit, but
	// in-process worker tick batching makes that rare.
	balance, balErr := w.readBalance(ctx, tenantID)
	if balErr != nil {
		w.Logger.Warn("sms worker: balance read failed", "tenant", tenantID, "err", balErr)
		// Be conservative — schedule a retry rather than burning the send.
		w.handleFailure(ctx, tenantID, d, balErr)
		return
	}
	if balance < 1 {
		w.markBlocked(ctx, tenantID, d.DeliveryID, "insufficient_credits")
		return
	}

	res, sendErr := sms.Send(w.HTTPClient, cfg, sms.Message{
		To:   d.RecipientPhone,
		From: cfg.SenderID,
		Body: d.Body,
	})
	if sendErr != nil {
		w.handleFailure(ctx, tenantID, d, sendErr)
		return
	}
	w.commitDeductAndMarkSent(ctx, tenantID, d, res.ProviderMessageID, cfg.Provider)
}

// commitDeductAndMarkSent — runs the atomic "debit + flip to sent"
// step required by the spec. If the debit fails because credits
// drained between the pre-check and now (race), we still mark sent
// because the provider already accepted the message; we just log a
// warning so ops can chase the slight balance drift if it ever shows
// up.
func (w *SMSWorker) commitDeductAndMarkSent(
	ctx context.Context, tenantID uuid.UUID, d store.DueSMSDelivery,
	providerMsgID string, provider domain.SMSProvider,
) {
	pmid := &providerMsgID
	if providerMsgID == "" {
		pmid = nil
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		notifID := d.NotificationID
		delivID := d.DeliveryID
		if _, err := w.Credits.DeductTx(ctx, tx, store.DeductInput{
			Channel:        domain.ChannelSMS,
			Amount:         1,
			NotificationID: &notifID,
			DeliveryID:     &delivID,
		}); err != nil {
			if errors.Is(err, store.ErrInsufficientCredits) {
				w.Logger.Warn("sms worker: balance drained between pre-check and post-send debit — sending without debit",
					"tenant", tenantID, "delivery", d.DeliveryID)
				// Fall through and still mark sent.
			} else {
				return err
			}
		}
		if err := w.Notifs.MarkSentTx(ctx, tx, d.DeliveryID, pmid); err != nil {
			return err
		}
		if provider == domain.SMSProviderMock {
			return w.Notifs.MarkDeliveredTx(ctx, tx, d.DeliveryID)
		}
		return nil
	})
	if err != nil {
		w.Logger.Warn("sms worker: mark sent failed", "delivery", d.DeliveryID, "err", err)
		return
	}
	w.Logger.Info("sms sent", "delivery", d.DeliveryID, "provider", provider, "provider_msg_id", providerMsgID)
}

func (w *SMSWorker) readBalance(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var balance int
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		b, err := w.Credits.BalanceTx(ctx, tx, domain.ChannelSMS)
		if err != nil {
			return err
		}
		balance = b.Balance
		return nil
	})
	return balance, err
}

func (w *SMSWorker) markBlocked(ctx context.Context, tenantID, deliveryID uuid.UUID, reason string) {
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkBlockedTx(ctx, tx, deliveryID, reason)
	})
	if err != nil {
		w.Logger.Warn("sms worker: mark blocked failed", "delivery", deliveryID, "err", err)
		return
	}
	w.Logger.Info("sms blocked", "delivery", deliveryID, "reason", reason)
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

// platformConfigToTenantShape adapts the platform-level SMS config to
// the domain.SMSConfig shape that the sms.Send helper expects. We
// don't bother filling TenantID — the sender doesn't read it.
func platformConfigToTenantShape(p *domain.PlatformSMSConfig) *domain.SMSConfig {
	return &domain.SMSConfig{
		Provider:      p.Provider,
		Username:      p.Username,
		APIKey:        p.APIKey,
		SenderID:      p.SenderID,
		RatePerMinute: p.RatePerMinute,
		WebhookSecret: p.WebhookSecret,
		IsActive:      p.IsEnabled,
	}
}
