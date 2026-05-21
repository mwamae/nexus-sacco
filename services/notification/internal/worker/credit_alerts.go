// Credit-balance alert worker (Stage 9).
//
// Polls every tenant's SMS + email credit balance. Fires:
//
//   * CREDIT_LOW_BALANCE   when balance crosses below the tenant's
//     configured low_balance_threshold (skipped if threshold = 0)
//   * CREDIT_ZERO_BALANCE  when balance is < 1
//
// Alerts are idempotent: the _alerted_at columns on the balance row
// gate re-fire. A top-up clears both columns (see credit_store.TopupTx)
// so the next drain triggers a fresh alert.
//
// Alerts themselves are in-app only — they don't consume credits.
// Otherwise a tenant with zero email credits could never be told that
// their email credits are zero. Per the user's clarification the
// audience is "all tenant-admin users", but we don't yet have a
// per-tenant admin-user resolver wired in. For now the alert lands as
// a tenant-wide in-app notification with no specific recipient_user_id
// — it shows up in the dashboard's notification feed and the
// persistent banner picks it up too.

package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

type CreditAlertWorker struct {
	DB           *db.Pool
	Tenants      *store.TenantStore
	Credits      *store.CreditStore
	Notifs       *store.NotificationStore
	TickInterval time.Duration
	Logger       *slog.Logger
}

const (
	eventCreditLow  = "CREDIT_LOW_BALANCE"
	eventCreditZero = "CREDIT_ZERO_BALANCE"
)

func (w *CreditAlertWorker) Run(ctx context.Context) {
	tick := w.TickInterval
	if tick <= 0 {
		tick = 60 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	w.Logger.Info("credit alerts worker started", "tick_seconds", tick.Seconds())
	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("credit alerts worker stopped")
			return
		case <-t.C:
			w.processOnce(ctx)
		}
	}
}

func (w *CreditAlertWorker) processOnce(ctx context.Context) {
	// Discover tenants (RLS-free table).
	type tenantHead struct {
		id   uuid.UUID
		slug string
	}
	var heads []tenantHead
	err := w.DB.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, slug FROM tenants ORDER BY slug`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h tenantHead
			if err := rows.Scan(&h.id, &h.slug); err != nil {
				return err
			}
			heads = append(heads, h)
		}
		return nil
	})
	if err != nil {
		w.Logger.Warn("credit alerts: list tenants failed", "err", err)
		return
	}
	for _, h := range heads {
		w.processTenant(ctx, h.id, h.slug)
	}
}

func (w *CreditAlertWorker) processTenant(ctx context.Context, tenantID uuid.UUID, slug string) {
	var balances []domain.CreditBalance
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		balances, err = w.Credits.AllBalancesTx(ctx, tx)
		return err
	})
	if err != nil {
		w.Logger.Warn("credit alerts: load balances failed", "tenant", tenantID, "err", err)
		return
	}
	for _, b := range balances {
		w.evaluate(ctx, tenantID, slug, b)
	}
}

func (w *CreditAlertWorker) evaluate(ctx context.Context, tenantID uuid.UUID, slug string, b domain.CreditBalance) {
	// Zero-balance crossing: only fire if balance is <1 AND we haven't
	// already alerted at this level. Cleared by the next top-up.
	if b.Balance < 1 && b.ZeroBalanceAlertedAt == nil {
		w.fire(ctx, tenantID, slug, b, eventCreditZero, "zero")
		return
	}
	// Low-balance crossing: tenant has a non-zero threshold, balance
	// is at or below it, but still > 0 (zero gets its own event above).
	if b.LowBalanceThreshold > 0 &&
		b.Balance > 0 && b.Balance <= b.LowBalanceThreshold &&
		b.LowBalanceAlertedAt == nil {
		w.fire(ctx, tenantID, slug, b, eventCreditLow, "low")
	}
}

func (w *CreditAlertWorker) fire(
	ctx context.Context,
	tenantID uuid.UUID, slug string, b domain.CreditBalance,
	eventCode string, kind string,
) {
	channelLabel := "SMS"
	if b.Channel == domain.ChannelEmail {
		channelLabel = "Email"
	}
	deepLink := "/credits"
	sourceModule := "notification.credits"
	priority := domain.PriorityWarning
	if kind == "zero" {
		priority = domain.PriorityError
	}
	recipientName := "Tenant administrators"
	err := w.DB.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		notif, err := w.Notifs.CreateTx(ctx, tx, store.CreateInput{
			EventCode:     eventCode,
			Priority:      priority,
			RecipientName: recipientName,
			SourceModule:  &sourceModule,
			DeepLink:      &deepLink,
			Payload: map[string]any{
				"channel":             string(b.Channel),
				"channel_label":       channelLabel,
				"balance":             b.Balance,
				"low_balance_threshold": b.LowBalanceThreshold,
				"tenant_slug":         slug,
			},
		})
		if err != nil {
			return err
		}
		// Render a fallback body inline — these alerts shouldn't depend
		// on the template manager having a row configured for the new
		// CREDIT_* events. Future: surface in the template manager so
		// SACCOs can customise the wording.
		body := channelLabel + " credit balance is low (" + intToStr(b.Balance) + " remaining)."
		if kind == "zero" {
			body = channelLabel + " credits are exhausted. " + channelLabel +
				" notifications are currently suspended; contact your platform admin to top up."
		}
		if _, err := w.Notifs.CreateDeliveryTx(ctx, tx, store.CreateDeliveryInput{
			NotificationID: notif.ID,
			Channel:        domain.ChannelInApp,
			Body:           body,
		}); err != nil {
			return err
		}
		return w.Credits.MarkAlertedTx(ctx, tx, b.Channel, kind)
	})
	if err != nil {
		w.Logger.Warn("credit alerts: fire failed",
			"tenant", tenantID, "channel", b.Channel, "kind", kind, "err", err)
		return
	}
	w.Logger.Info("credit alert fired",
		"tenant", slug, "channel", b.Channel, "kind", kind, "balance", b.Balance)
}

// intToStr — tiny helper to avoid pulling in strconv for one int.
// We use it once per alert; readability trumps efficiency.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
