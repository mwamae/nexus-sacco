// guarantor-reminder — hourly worker that resends SMS consent
// invites for guarantors who haven't responded yet.
//
// For every tenant: find consent tokens whose decision is still
// pending and whose age has crossed the configured first-reminder
// threshold (default 48h) or second-reminder threshold (default
// 6 days = 144h). Issue a fresh token (attempt_number + 1) inside
// a tx, then fire the SMS via the notification service.
//
// One token row per attempt — the old row stays around for audit;
// only the newest unused token in a chain can be redeemed (the
// older ones are still valid until they expire, but the SMS body
// only points to the newest one, and a member responding via an
// older link will see the same guarantee + accept/decline the same
// way, so this is benign).
//
// CLI flags:
//
//   --once    run one pass for every tenant and exit
//
// Default loop ticks every hour. The classifier-style "approximate"
// cadence is fine — the underlying SQL filter (attempt N created
// before now - threshold) guards against re-sending too often.

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
	"github.com/nexussacco/savings/internal/handler"
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
	hourlyInterval  = 1 * time.Hour
	perTenantBudget = 60 * time.Second
)

func main() {
	once := flag.Bool("once", false, "run one pass and exit (cron-style + test)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("guarantor-reminder: DATABASE_URL is required")
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
	consent := store.NewGuarantorConsentStore(dbPool.Pool)

	if !*once {
		go healthx.RunHeartbeatLoop(ctx, dbPool.Pool, "guarantor-reminder", workerVersion(), 30*time.Second, nil, logger)
	}

	for {
		if err := runOnePass(ctx, dbPool, consent, notif, logger); err != nil && ctx.Err() == nil {
			logger.Error("reminder pass failed", "err", err)
		}
		if *once {
			logger.Info("--once supplied; exiting")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-time.After(hourlyInterval):
		}
	}
}

func runOnePass(
	ctx context.Context, pool *db.Pool,
	consent *store.GuarantorConsentStore, notif *notifier.Client, logger *slog.Logger,
) error {
	tenants, err := listTenantIDs(ctx, pool.Pool)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	logger.Info("guarantor-reminder pass starting", "tenants", len(tenants))

	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		sent, err := remindTenant(tctx, pool, consent, notif, logger, t)
		tcancel()
		if err != nil {
			logger.Error("tenant reminder failed", "tenant", t, "err", err)
			continue
		}
		if sent > 0 {
			logger.Info("tenant reminders sent", "tenant", t, "reminders", sent)
		}
	}
	return nil
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

// remindTenant finds + sends due reminders for one tenant. Returns
// how many SMS dispatches it attempted.
func remindTenant(
	ctx context.Context, pool *db.Pool,
	consent *store.GuarantorConsentStore, notif *notifier.Client, logger *slog.Logger,
	tenantID uuid.UUID,
) (int, error) {
	var sent int
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		settings, err := handler.LoadConsentSettingsTx(ctx, tx, tenantID)
		if err != nil {
			return fmt.Errorf("load settings: %w", err)
		}
		if !settings.Enabled {
			return nil
		}

		// Defaults match the spec: 48h first, 144h (6 days) second.
		firstHours, secondHours := loadReminderHours(ctx, tx)

		targets, err := consent.FindDueRemindersTx(ctx, tx, firstHours, secondHours)
		if err != nil {
			return fmt.Errorf("find due: %w", err)
		}
		if len(targets) == 0 {
			return nil
		}

		for _, target := range targets {
			ctxRow, err := consent.LoadContextTx(ctx, tx, target.TokenID)
			if err != nil {
				// Expired/used tokens are not actionable — skip without
				// failing the tenant. Surface unexpected errors.
				if err == store.ErrConsentTokenExpired || err == store.ErrConsentTokenUsed {
					continue
				}
				logger.Warn("reminder load context skipped", "token", target.TokenID, "err", err)
				continue
			}
			if err := handler.IssueConsentForGuarantee(
				ctx, tx, consent, notif, logger,
				tenantID, ctxRow.Token.GuaranteeID, ctxRow.Token.TenantID,
				settings,
				ctxRow.ApplicantName, ctxRow.ProductName,
				ctxRow.RequestedAmount, ctxRow.AmountGuaranteed,
				target.NextAttempt,
			); err != nil {
				logger.Error("reminder issue failed", "token", target.TokenID, "err", err)
				continue
			}
			sent++
		}
		return nil
	})
	return sent, err
}

func loadReminderHours(ctx context.Context, tx pgx.Tx) (int, int) {
	var first, second int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(guarantor_reminder_hours_first, 48),
		       COALESCE(guarantor_reminder_hours_second, 144)
		  FROM tenant_operations
		 LIMIT 1
	`).Scan(&first, &second)
	if err != nil {
		return 48, 144
	}
	if first <= 0 {
		first = 48
	}
	if second <= 0 {
		second = 144
	}
	return first, second
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
