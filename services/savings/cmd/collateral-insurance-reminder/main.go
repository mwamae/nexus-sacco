// collateral-insurance-reminder — daily worker. For every tenant,
// scan collateral_insurance_policies.is_current rows whose effective_to
// lands inside the warning window (default 30 days), fire an SMS to
// the borrower + an event so the slide-over surfaces it, and flip
// expired policies to status='expired'.
//
// Idempotent: one reminder per item per 30 days.
//
// CLI flags: --once.

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
		logger.Error("collateral-insurance-reminder: DATABASE_URL required")
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
		go healthx.RunHeartbeatLoop(ctx, dbPool.Pool, "collateral-insurance-reminder", workerVersion(), 30*time.Second, nil, logger)
	}

	for {
		if err := runOnePass(ctx, dbPool, collStore, notif, logger); err != nil && ctx.Err() == nil {
			logger.Error("insurance reminder pass failed", "err", err)
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
		return err
	}
	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		sent, expired, err := remindTenant(tctx, pool, collStore, notif, logger, t)
		tcancel()
		if err != nil {
			logger.Error("tenant pass failed", "tenant", t, "err", err)
			continue
		}
		if sent > 0 || expired > 0 {
			logger.Info("insurance reminder pass",
				"tenant", t, "reminders_sent", sent, "policies_expired", expired)
		}
	}
	return nil
}

func remindTenant(
	ctx context.Context, pool *db.Pool, collStore *store.CollateralStore,
	notif *notifier.Client, logger *slog.Logger, tenantID uuid.UUID,
) (int, int, error) {
	var sent, expired int
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Tenant warning window. Default 30 days.
		var warningDays int
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(collateral_insurance_warning_days, 30) FROM tenant_operations LIMIT 1
		`).Scan(&warningDays)
		if warningDays <= 0 {
			warningDays = 30
		}

		// 1. Flip expired-but-still-active policies to 'expired'.
		tag, err := tx.Exec(ctx, `
			UPDATE collateral_insurance_policies
			   SET status = 'expired'
			 WHERE is_current = true AND status = 'active' AND effective_to < CURRENT_DATE
		`)
		if err != nil {
			return err
		}
		expired = int(tag.RowsAffected())

		// 2. Send reminders for policies expiring soon.
		rows, err := tx.Query(ctx, `
			SELECT p.collateral_id, c.kind::text, c.description,
			       p.effective_to, p.provider_name, p.policy_no,
			       COALESCE(cd.full_name, ''), COALESCE(m.phone, '')
			  FROM collateral_insurance_policies p
			  JOIN loan_collateral c ON c.id = p.collateral_id
			  JOIN loan_applications a ON a.id = c.application_id
			  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
			  LEFT JOIN members m ON m.id = cd.member_id
			 WHERE p.is_current = true AND p.status = 'active'
			   AND p.effective_to BETWEEN CURRENT_DATE AND CURRENT_DATE + ($1 || ' days')::interval
			   AND NOT EXISTS (
			     SELECT 1 FROM loan_collateral_events e
			      WHERE e.collateral_id = p.collateral_id
			        AND e.kind = 'documents_attached'
			        AND e.details->>'action' = 'insurance_reminder'
			        AND e.occurred_at > now() - interval '30 days'
			   )
		`, fmt.Sprintf("%d", warningDays))
		if err != nil {
			return err
		}
		defer rows.Close()
		type target struct {
			CollateralID uuid.UUID
			Kind, Description, Provider, PolicyNo, MemberName, Phone string
			EffectiveTo time.Time
		}
		var targets []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.CollateralID, &t.Kind, &t.Description, &t.EffectiveTo,
				&t.Provider, &t.PolicyNo, &t.MemberName, &t.Phone); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, tg := range targets {
			daysToExpiry := int(time.Until(tg.EffectiveTo).Hours() / 24)
			body := fmt.Sprintf(
				"Insurance for %s (%s) expires in %d days. Renew with %s (policy %s) to keep your loan in good standing.",
				tg.Kind, tg.Description, daysToExpiry, tg.Provider, tg.PolicyNo,
			)
			if notif != nil {
				phone := tg.Phone
				notif.Notify(ctx, notifier.Request{
					TenantID:       tenantID,
					EventCode:      "GUARANTOR_CONSENT_REQUEST",
					Channels:       []notifier.Channel{notifier.ChannelSMS},
					RecipientName:  tg.MemberName,
					RecipientPhone: ptrIfNonEmpty(phone),
					Payload:        map[string]any{"body": body},
				})
			}
			if err := collStore.AppendEventTx(ctx, tx, store.AppendEventInput{
				CollateralID: tg.CollateralID,
				Kind:         "documents_attached",
				Details: map[string]interface{}{
					"action":         "insurance_reminder",
					"days_to_expiry": daysToExpiry,
					"provider":       tg.Provider,
				},
			}); err != nil {
				return err
			}
			sent++
		}
		return nil
	})
	return sent, expired, err
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

func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
