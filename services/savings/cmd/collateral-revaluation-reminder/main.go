// collateral-revaluation-reminder — daily worker. For every tenant,
// scan collateral_valuations.is_current rows whose expires_at lands
// inside the warning window (default 60 days), and fire an SMS to
// the assigned credit officer + insert a 'revalued_due' event on the
// collateral row so the loan detail page flags it.
//
// Idempotency: re-uses the 30-day "one reminder per item" rule —
// don't fire if a 'revalued_due' event was emitted on the same
// collateral_id in the last 30 days.
//
// CLI flags:
//
//   --once   run one pass + exit (cron / test)
//
// Default loop ticks every 24h.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/shared/healthx"
)

var version string

func workerVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
}

const (
	dailyInterval   = 24 * time.Hour
	perTenantBudget = 90 * time.Second
)

func main() {
	once := flag.Bool("once", false, "run one pass and exit")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("collateral-revaluation-reminder: DATABASE_URL required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPool, err := db.New(ctx, dsn)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	notif := notifier.New(
		envOr("NOTIFICATION_SERVICE_URL", "http://localhost:8085"),
		os.Getenv("NOTIFICATION_INTERNAL_TOKEN"),
		logger,
	)
	collStore := store.NewCollateralStore(dbPool.Pool)

	if !*once {
		go healthx.RunHeartbeatLoop(ctx, dbPool.Pool, "collateral-revaluation-reminder", workerVersion(), 30*time.Second, nil, logger)
	}

	for {
		if err := runOnePass(ctx, dbPool, collStore, notif, logger); err != nil && ctx.Err() == nil {
			logger.Error("revaluation reminder pass failed", "err", err)
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(dailyInterval):
		}
	}
}

func runOnePass(
	ctx context.Context, pool *db.Pool, collStore *store.CollateralStore,
	notif *notifier.Client, logger *slog.Logger,
) error {
	tenants, err := listTenantIDs(ctx, pool.Pool)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	logger.Info("revaluation reminder pass starting", "tenants", len(tenants))
	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		sent, err := remindTenant(tctx, pool, collStore, notif, logger, t)
		tcancel()
		if err != nil {
			logger.Error("tenant pass failed", "tenant", t, "err", err)
			continue
		}
		if sent > 0 {
			logger.Info("revaluation reminders sent", "tenant", t, "count", sent)
		}
	}
	return nil
}

func remindTenant(
	ctx context.Context, pool *db.Pool, collStore *store.CollateralStore,
	notif *notifier.Client, logger *slog.Logger, tenantID uuid.UUID,
) (int, error) {
	var sent int
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Tenant-policy window. Defaults match the migration.
		var warningDays int
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(collateral_revaluation_warning_days, 60) FROM tenant_operations LIMIT 1
		`).Scan(&warningDays)
		if warningDays <= 0 {
			warningDays = 60
		}

		// Find due valuations. Filter out ones we already nudged about
		// in the last 30 days via the loan_collateral_events idempotency
		// gate.
		rows, err := tx.Query(ctx, `
			SELECT v.collateral_id, c.kind::text, c.description, v.expires_at,
			       c.application_id, a.application_no, COALESCE(cd.full_name, '')
			  FROM collateral_valuations v
			  JOIN loan_collateral c ON c.id = v.collateral_id
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			 WHERE v.is_current = true
			   AND v.expires_at IS NOT NULL
			   AND v.expires_at <= CURRENT_DATE + ($1 || ' days')::interval
			   AND c.status IN ('valued','pledged')
			   AND NOT EXISTS (
			     SELECT 1 FROM loan_collateral_events e
			      WHERE e.collateral_id = v.collateral_id
			        AND e.kind = 'revalued'
			        AND e.occurred_at > now() - interval '30 days'
			        AND e.details->>'reminder' = 'true'
			   )
		`, fmt.Sprintf("%d", warningDays))
		if err != nil {
			return err
		}
		defer rows.Close()

		type target struct {
			CollateralID, ApplicationID uuid.UUID
			Kind, Description, AppNo, MemberName string
			ExpiresAt time.Time
		}
		var targets []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.CollateralID, &t.Kind, &t.Description, &t.ExpiresAt,
				&t.ApplicationID, &t.AppNo, &t.MemberName); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, tg := range targets {
			daysToExpiry := int(time.Until(tg.ExpiresAt).Hours() / 24)
			body := fmt.Sprintf(
				"Revaluation due in %d days for %s on application %s (%s). Please schedule.",
				daysToExpiry, tg.Kind, tg.AppNo, tg.MemberName,
			)
			if notif != nil {
				notif.Notify(ctx, notifier.Request{
					TenantID:      tenantID,
					EventCode:     "GUARANTOR_CONSENT_REQUEST", // reuse passthrough event
					Channels:      []notifier.Channel{notifier.ChannelSMS},
					RecipientName: tg.MemberName,
					Payload:       map[string]any{"body": body},
				})
			}
			if err := collStore.AppendEventTx(ctx, tx, store.AppendEventInput{
				CollateralID: tg.CollateralID,
				Kind:         "revalued",
				Details: map[string]interface{}{
					"reminder":       "true",
					"days_to_expiry": daysToExpiry,
				},
			}); err != nil {
				return err
			}
			sent++
		}
		return nil
	})
	return sent, err
}

func listTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM tenants WHERE slug <> 'platform' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
