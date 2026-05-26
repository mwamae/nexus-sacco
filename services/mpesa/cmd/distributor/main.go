// services/mpesa/cmd/distributor — background worker that drains
// mpesa_inbound_events.status='received' through the distribution
// engine. Runs as its own binary so the HTTP service can scale
// independently of the worker.
//
// Concurrency model: one tx per event, leased via SELECT … FOR
// UPDATE SKIP LOCKED. Multiple instances of this binary are safe to
// run side by side — the skip-locked clause guarantees each event
// is processed exactly once.
//
// Polling cadence: 1s when work is found, 5s when the queue is
// empty. Tunable via MPESA_DISTRIBUTOR_POLL_INTERVAL_MS (the busy
// interval) + MPESA_DISTRIBUTOR_IDLE_INTERVAL_MS.
//
// One worker handles all tenants. The lease query iterates a
// tenant id at a time so a single noisy tenant can't starve the
// others; the loop walks tenants in round-robin.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/config"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/distribution"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

func main() {
	once := flag.Bool("once", false, "drain the queue once and exit (handy for cron-style deployments + tests)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel, cfg.Env)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	workerID := uuid.New()
	logger.Info("mpesa distributor starting",
		"worker_id", workerID, "env", cfg.Env, "once", *once)

	orchestrator := &distribution.Orchestrator{
		Events:              store.NewInboundEventStore(pool.Pool),
		Runs:                store.NewDistributionRunStore(pool.Pool),
		Balances:            store.NewDistributionBalances(pool.Pool),
		Audit:               store.NewAuditStore(pool.Pool),
		Workflow:            workflowclient.New(),
		Logger:              logger,
		CashAccountCode:     getEnv("MPESA_GL_CASH_ACCOUNT", "1030"),     // M-PESA receipts default
		ClearingAccountCode: getEnv("MPESA_GL_CLEARING_ACCOUNT", "1099"), // unallocated clearing default
	}

	busy := durationMs("MPESA_DISTRIBUTOR_POLL_INTERVAL_MS", 1000)
	idle := durationMs("MPESA_DISTRIBUTOR_IDLE_INTERVAL_MS", 5000)

	for {
		processed, err := drainOnce(ctx, pool, orchestrator, workerID, logger)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("drain pass", "err", err)
		}
		if *once {
			logger.Info("distributor --once complete", "processed", processed)
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("distributor shut down cleanly")
			return
		case <-time.After(pickInterval(processed, busy, idle)):
		}
	}
}

// drainOnce walks every tenant once and leases one event per
// tenant per pass. Returning early on a context cancellation
// surrenders any pending work to the next worker — never half-
// processes an event.
func drainOnce(
	ctx context.Context, pool *db.Pool, o *distribution.Orchestrator,
	workerID uuid.UUID, logger *slog.Logger,
) (int, error) {
	tenants, err := listTenantIDs(ctx, pool)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		ok, err := processOneEvent(ctx, pool, o, workerID, tenantID, logger)
		if err != nil {
			logger.Error("process one event", "tenant_id", tenantID, "err", err)
			continue
		}
		if ok {
			processed++
		}
	}
	return processed, nil
}

// processOneEvent leases ONE event for the tenant, runs the
// orchestrator, and commits — or rolls back + records the attempt
// failure in a fresh tx. Returns true when an event was processed
// (success or fail), false when the queue was empty.
func processOneEvent(
	ctx context.Context, pool *db.Pool, o *distribution.Orchestrator,
	workerID, tenantID uuid.UUID, logger *slog.Logger,
) (bool, error) {
	var (
		leasedEventID uuid.UUID
		processErr    error
	)
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		eventID, err := o.Runs.LeaseNextTx(ctx, tx, tenantID, workerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // empty queue
			}
			return err
		}
		leasedEventID = eventID
		if _, err := o.Process(ctx, tx, tenantID, eventID); err != nil {
			processErr = err
			return err
		}
		return nil
	})
	if leasedEventID == uuid.Nil {
		// Either the queue was empty or the lease query itself
		// errored before returning an id. The latter is logged
		// above.
		return false, nil
	}
	if processErr == nil && err == nil {
		logger.Info("event distributed", "event_id", leasedEventID, "tenant_id", tenantID)
		return true, nil
	}
	// Process failed → record the attempt in a NEW tx so we don't
	// lose the failure to the rolled-back business tx.
	fixErr := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return o.RecordFailure(ctx, tx, tenantID, leasedEventID,
			coalesceErr(processErr, err))
	})
	if fixErr != nil {
		logger.Error("record attempt failure", "event_id", leasedEventID, "err", fixErr)
	}
	return true, nil
}

func listTenantIDs(ctx context.Context, pool *db.Pool) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM tenants WHERE status = 'active'`)
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

func coalesceErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func pickInterval(processed int, busy, idle time.Duration) time.Duration {
	if processed > 0 {
		return busy
	}
	return idle
}

func durationMs(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if env == "development" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
