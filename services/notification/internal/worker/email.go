// Background worker that drains queued email deliveries.
//
// Each tick the worker iterates every active tenant, claims up to
// BatchSize due rows (atomically, with FOR UPDATE SKIP LOCKED so
// concurrent workers don't collide), loads that tenant's SMTP
// config, and attempts to send each email. Failures are rescheduled
// using the platform retry table (1m, 5m, 15m); after the configured
// max_attempts is exhausted the row is marked failed-final and an
// operations alert is logged via slog.

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/pdf"
	"github.com/nexussacco/notification/internal/smtp"
	"github.com/nexussacco/notification/internal/store"
)

type EmailWorker struct {
	DB           *db.Pool
	Notifs       *store.NotificationStore
	PlatformSMTP *store.PlatformSMTPStore
	Credits      *store.CreditStore
	PDFStorage   *pdf.Storage // for reading attachments off disk
	TickInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
}

// retryBackoff returns the delay before the Nth attempt (1-indexed
// after the initial attempt). Platform spec:
//   attempt 1 (first retry): +1 min
//   attempt 2 (second retry): +5 min
//   attempt 3 (third retry): +15 min
// Anything beyond 3 retries is terminal failure.
func retryBackoff(attemptCount int) (time.Duration, bool) {
	switch attemptCount {
	case 1:
		return 1 * time.Minute, true
	case 2:
		return 5 * time.Minute, true
	case 3:
		return 15 * time.Minute, true
	}
	return 0, false
}

func (w *EmailWorker) Run(ctx context.Context) {
	tick := w.TickInterval
	if tick <= 0 {
		tick = 10 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	w.Logger.Info("email worker started", "tick_seconds", tick.Seconds())
	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("email worker stopped")
			return
		case <-t.C:
			w.processOnce(ctx)
		}
	}
}

func (w *EmailWorker) processOnce(ctx context.Context) {
	// Load the shared platform SMTP config once per tick. No tenant
	// context required — the table is a singleton (id=1).
	platformCfg, err := w.PlatformSMTP.Get(ctx)
	if err != nil {
		w.Logger.Warn("email worker: load platform SMTP failed", "err", err)
		return
	}
	if platformCfg == nil || !platformCfg.IsEnabled {
		return
	}
	cfg := platformSMTPToTenantShape(platformCfg)

	var tenantIDs []uuid.UUID
	err = w.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		tenantIDs, err = w.Notifs.AllActiveTenantsTx(ctx, tx)
		return err
	})
	if err != nil {
		w.Logger.Warn("email worker: list tenants failed", "err", err)
		return
	}
	for _, tid := range tenantIDs {
		w.processTenant(ctx, tid, cfg)
	}
}

func (w *EmailWorker) processTenant(ctx context.Context, tenantID uuid.UUID, cfg *domain.SMTPConfig) {
	// Claim a batch within the tenant context (so RLS applies + the
	// next_retry_at index is hit). The claim already flips rows to
	// 'sent' optimistically; we'll roll forward to confirmed sent,
	// back to retryable failure, or to blocked if credits are out.
	var batch []store.DueEmailDelivery
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		batch, err = w.Notifs.ClaimDueEmailsForTenantTx(ctx, tx, w.BatchSize)
		return err
	})
	if err != nil {
		w.Logger.Warn("email worker: claim failed", "tenant", tenantID, "err", err)
		return
	}
	if len(batch) == 0 {
		return
	}

	for _, d := range batch {
		w.deliver(ctx, tenantID, cfg, d)
	}
}

// platformSMTPToTenantShape adapts the singleton platform config into
// the per-tenant struct shape that smtp.Send already understands.
func platformSMTPToTenantShape(p *domain.PlatformSMTPConfig) *domain.SMTPConfig {
	return &domain.SMTPConfig{
		Host:        p.Host,
		Port:        p.Port,
		Username:    p.Username,
		Password:    p.Password,
		Encryption:  domain.SMTPEncryption(p.Encryption),
		FromAddress: p.FromAddress,
		FromName:    p.FromName,
		IsActive:    p.IsEnabled,
	}
}

func (w *EmailWorker) deliver(ctx context.Context, tenantID uuid.UUID, cfg *domain.SMTPConfig, d store.DueEmailDelivery) {
	if d.RecipientEmail == "" {
		// Can never succeed — finalize.
		w.finalize(ctx, tenantID, d.DeliveryID,
			fmt.Sprintf("recipient_email missing on notification %s", d.NotificationID))
		return
	}
	// Credit pre-check. See sms worker for the race-window discussion.
	balance, balErr := w.readBalance(ctx, tenantID)
	if balErr != nil {
		w.Logger.Warn("email worker: balance read failed", "tenant", tenantID, "err", balErr)
		w.handleFailure(ctx, tenantID, d, balErr)
		return
	}
	if balance < 1 {
		w.markBlocked(ctx, tenantID, d.DeliveryID, "insufficient_credits")
		return
	}

	from := cfg.FromAddress
	if cfg.FromName != "" {
		from = cfg.FromName + " <" + cfg.FromAddress + ">"
	}
	to := d.RecipientEmail
	if d.RecipientName != "" {
		to = d.RecipientName + " <" + d.RecipientEmail + ">"
	}
	atts := w.loadAttachments(d.AttachmentPaths)
	msgID, err := smtp.Send(cfg, smtp.Message{
		From:        from,
		To:          to,
		Subject:     d.Subject,
		PlainBody:   d.Body,
		Attachments: atts,
	})
	if err == nil {
		w.commitDeductAndMarkSent(ctx, tenantID, d, msgID)
		return
	}
	w.handleFailure(ctx, tenantID, d, err)
}

func (w *EmailWorker) readBalance(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var balance int
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		b, err := w.Credits.BalanceTx(ctx, tx, domain.ChannelEmail)
		if err != nil {
			return err
		}
		balance = b.Balance
		return nil
	})
	return balance, err
}

func (w *EmailWorker) markBlocked(ctx context.Context, tenantID, deliveryID uuid.UUID, reason string) {
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkBlockedTx(ctx, tx, deliveryID, reason)
	})
	if err != nil {
		w.Logger.Warn("email worker: mark blocked failed", "delivery", deliveryID, "err", err)
		return
	}
	w.Logger.Info("email blocked", "delivery", deliveryID, "reason", reason)
}

// commitDeductAndMarkSent — atomic post-send finalisation. Debit
// happens in the same tx that flips status to 'sent', so a DB error
// during either step rolls both back. See SMS worker for the same
// rationale.
func (w *EmailWorker) commitDeductAndMarkSent(ctx context.Context, tenantID uuid.UUID, d store.DueEmailDelivery, providerMsgID string) {
	pmid := &providerMsgID
	if providerMsgID == "" {
		pmid = nil
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		notifID := d.NotificationID
		delivID := d.DeliveryID
		if _, derr := w.Credits.DeductTx(ctx, tx, store.DeductInput{
			Channel:        domain.ChannelEmail,
			Amount:         1,
			NotificationID: &notifID,
			DeliveryID:     &delivID,
		}); derr != nil {
			if errors.Is(derr, store.ErrInsufficientCredits) {
				w.Logger.Warn("email worker: balance drained between pre-check and post-send debit — sending without debit",
					"tenant", tenantID, "delivery", d.DeliveryID)
			} else {
				return derr
			}
		}
		return w.Notifs.MarkSentTx(ctx, tx, d.DeliveryID, pmid)
	})
	if err != nil {
		w.Logger.Warn("email worker: mark sent failed", "delivery", d.DeliveryID, "err", err)
		return
	}
	w.Logger.Info("email sent", "delivery", d.DeliveryID, "provider_msg_id", providerMsgID)
}

func (w *EmailWorker) loadAttachments(paths []string) []smtp.Attachment {
	if w.PDFStorage == nil || len(paths) == 0 {
		return nil
	}
	out := make([]smtp.Attachment, 0, len(paths))
	for _, p := range paths {
		f, err := w.PDFStorage.Open(p)
		if err != nil {
			w.Logger.Warn("email worker: open attachment failed", "path", p, "err", err)
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			w.Logger.Warn("email worker: read attachment failed", "path", p, "err", err)
			continue
		}
		out = append(out, smtp.Attachment{
			Filename: filepath.Base(p),
			Data:     data,
			MimeType: "application/pdf",
		})
	}
	return out
}

func (w *EmailWorker) markSent(ctx context.Context, tenantID, deliveryID uuid.UUID, providerMsgID string) {
	id := providerMsgID
	pmid := &id
	if id == "" {
		pmid = nil
	}
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkSentTx(ctx, tx, deliveryID, pmid)
	})
	if err != nil {
		w.Logger.Warn("email worker: mark sent failed", "delivery", deliveryID, "err", err)
		return
	}
	w.Logger.Info("email sent", "delivery", deliveryID, "provider_msg_id", providerMsgID)
}

func (w *EmailWorker) handleFailure(ctx context.Context, tenantID uuid.UUID, d store.DueEmailDelivery, sendErr error) {
	// AttemptCount on the row already reflects this attempt (incremented
	// by the claim CTE). Decide whether more retries are allowed.
	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}
	// d.AttemptCount is the NEW attempt_count after the claim. The
	// number of retries already used is attemptCount-1.
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
		w.Logger.Warn("email worker: schedule retry failed", "delivery", d.DeliveryID, "err", err)
		return
	}
	w.Logger.Info("email send failed — retry scheduled",
		"delivery", d.DeliveryID, "attempt", d.AttemptCount,
		"retry_at", next.Format(time.RFC3339), "reason", sendErr)
}

func (w *EmailWorker) finalize(ctx context.Context, tenantID, deliveryID uuid.UUID, reason string) {
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return w.Notifs.MarkFailedFinalTx(ctx, tx, deliveryID, reason)
	})
	if err != nil {
		w.Logger.Warn("email worker: finalize failed", "delivery", deliveryID, "err", err)
		return
	}
	// System alert — Stage 8 will surface this as a notification of
	// its own; for now slog.Error is the operational signal.
	w.Logger.Error("email delivery permanently failed", "delivery", deliveryID, "reason", reason)
}
