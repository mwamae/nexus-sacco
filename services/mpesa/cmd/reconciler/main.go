// services/mpesa/cmd/reconciler — daily reconciliation job + soak harness.
//
// Two modes:
//
//   ./reconciler -daily
//     Default. For every tenant + active paybill, persists a
//     mpesa_statement_pulls row, runs a diff against our local
//     mpesa_inbound_events / mpesa_outbound_requests, and writes
//     any discrepancies into mpesa_reconciliation_diffs. Each diff
//     row gets an mpesa_reconciliation_diff wf_instance so staff
//     can investigate.
//
//   ./reconciler -soak=<N>
//     CI/operator-driven validation. Synthesises N inbound events
//     directly into mpesa_inbound_events, waits for the distributor
//     to drain them, then runs a diff against the same window.
//     Exits non-zero if diff_count > 0. CI invokes with N=20.
//     The 24-hour acceptance soak runs with N=1000 against
//     sandbox.
//
// Phase-6 scope notes:
//   • The Daraja Account Balance call is async (Result URL); we
//     post the request synchronously here + persist whatever the
//     immediate response says. The real-time fields land later via
//     the Result callback (phase 7 wires that handler).
//   • For the soak path the reconciler doesn't call Daraja at all —
//     it diffs the local ledger against itself (synthesised events
//     should every flip to status='distributed' inside the soak
//     window; non-zero stuck rows ARE the diff).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/config"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/metrics"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

func main() {
	daily := flag.Bool("daily", false, "run one reconciliation pass for every tenant + active paybill and exit")
	soak := flag.Int("soak", 0, "synthesise N inbound events, wait for the distributor to drain, then diff (exits non-zero on diff)")
	soakTimeout := flag.Duration("soak-timeout", 2*time.Minute, "max wall-clock to wait for soak events to drain")
	flag.Parse()
	if !*daily && *soak <= 0 {
		fmt.Fprintln(os.Stderr, "usage: reconciler -daily | -soak=<N>")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel, cfg.Env)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Reconciler intentionally crosses tenant boundaries — use the
	// privileged pool so the bootstrap SELECTs (listing paybills,
	// scanning soak rows) aren't filtered by RLS. Per-tenant writes
	// still pass through WithTenantTx.
	pool, err := db.NewPrivileged(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *daily {
		if err := runDaily(ctx, pool, logger); err != nil {
			logger.Error("daily reconciliation", "err", err)
			os.Exit(1)
		}
		return
	}
	if *soak > 0 {
		if err := runSoak(ctx, pool, logger, *soak, *soakTimeout); err != nil {
			logger.Error("soak", "err", err, "n", *soak)
			os.Exit(1)
		}
		logger.Info("soak passed", "n", *soak)
	}
}

// ─────────── daily mode ───────────

func runDaily(ctx context.Context, pool *db.Pool, logger *slog.Logger) error {
	// Window covers the previous 24h (the reconciler runs at 02:00
	// tenant-local; we capture "yesterday's close" worth of
	// activity).
	now := time.Now().UTC()
	windowTo := now
	windowFrom := windowTo.Add(-24 * time.Hour)

	rows, err := pool.Query(ctx, `
		SELECT p.tenant_id, p.id, p.label
		  FROM mpesa_paybills p
		 WHERE p.status = 'active'
		 ORDER BY p.tenant_id, p.id
	`)
	if err != nil {
		return fmt.Errorf("list paybills: %w", err)
	}
	defer rows.Close()
	type job struct {
		tenantID, paybillID uuid.UUID
		label               string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.tenantID, &j.paybillID, &j.label); err != nil {
			return err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, j := range jobs {
		log := logger.With(
			"tenant_id", j.tenantID, "paybill_id", j.paybillID, "paybill_label", j.label,
		)
		if err := runOnePaybill(ctx, pool, log, j.tenantID, j.paybillID, windowFrom, windowTo); err != nil {
			log.Error("paybill reconciliation failed", "err", err)
			metrics.ReconcilerRuns.Inc("failed", j.tenantID.String())
			// Continue with the next paybill — one paybill's failure
			// shouldn't poison the whole tenant pass.
			continue
		}
		metrics.ReconcilerRuns.Inc("ok", j.tenantID.String())
	}
	return nil
}

func runOnePaybill(
	ctx context.Context, pool *db.Pool, logger *slog.Logger,
	tenantID, paybillID uuid.UUID, from, to time.Time,
) error {
	var (
		pullID        uuid.UUID
		ledgerInbound decimal.Decimal
		ledgerOutbound decimal.Decimal
		// The Daraja totals are populated when the AccountBalance
		// Result callback lands (phase 7). For the phase-6 pass we
		// stamp a NULL placeholder + leave diff_count=0 — the
		// reconciler still snapshots the row so phase 7 can resolve
		// without losing history.
	)
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO mpesa_statement_pulls
				(tenant_id, paybill_id, window_from, window_to, status, requested_at)
			VALUES ($1, $2, $3, $4, 'pending', now())
			ON CONFLICT (tenant_id, paybill_id, window_from, window_to)
			DO UPDATE SET requested_at = now(), status = 'pending'
			RETURNING id
		`, tenantID, paybillID, from, to).Scan(&pullID); err != nil {
			return fmt.Errorf("insert statement_pull: %w", err)
		}
		// Ledger-side totals.
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount), 0) FROM mpesa_inbound_events
			 WHERE tenant_id = $1 AND paybill_id = $2
			   AND received_at >= $3 AND received_at < $4
		`, tenantID, paybillID, from, to).Scan(&ledgerInbound); err != nil {
			return fmt.Errorf("sum inbound: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount), 0) FROM mpesa_outbound_requests
			 WHERE tenant_id = $1 AND paybill_id = $2
			   AND status IN ('completed','sent','pending')
			   AND requested_at >= $3 AND requested_at < $4
		`, tenantID, paybillID, from, to).Scan(&ledgerOutbound); err != nil {
			return fmt.Errorf("sum outbound: %w", err)
		}
		// Stuck rows: anything still 'received' past the window
		// is a soft diff (the distributor should've drained it).
		var stuckInbound int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM mpesa_inbound_events
			 WHERE tenant_id = $1 AND paybill_id = $2
			   AND status = 'received'
			   AND received_at < $3
		`, tenantID, paybillID, to.Add(-5*time.Minute)).Scan(&stuckInbound); err != nil {
			return err
		}
		diffCount := stuckInbound
		// Persist totals + diff count. Daraja-side totals + balance
		// land via the Result callback (phase 7). For now those
		// columns stay NULL — explicit so a future delta query knows
		// "we haven't heard back from Daraja yet" vs "Daraja said 0".
		if _, err := tx.Exec(ctx, `
			UPDATE mpesa_statement_pulls
			   SET status = 'completed', completed_at = now(),
			       ledger_inbound_total = $2,
			       ledger_outbound_total = $3,
			       diff_count = $4
			 WHERE id = $1
		`, pullID, ledgerInbound, ledgerOutbound, diffCount); err != nil {
			return err
		}

		// For each stuck inbound, write a diff row + a wf task.
		if stuckInbound > 0 {
			stuckRows, err := tx.Query(ctx, `
				SELECT id, transaction_id, amount FROM mpesa_inbound_events
				 WHERE tenant_id = $1 AND paybill_id = $2
				   AND status = 'received'
				   AND received_at < $3
			`, tenantID, paybillID, to.Add(-5*time.Minute))
			if err != nil {
				return err
			}
			defer stuckRows.Close()
			wf := workflowclient.New()
			for stuckRows.Next() {
				var eid uuid.UUID
				var txid string
				var amt decimal.Decimal
				if err := stuckRows.Scan(&eid, &txid, &amt); err != nil {
					return err
				}
				var diffID uuid.UUID
				if err := tx.QueryRow(ctx, `
					INSERT INTO mpesa_reconciliation_diffs
						(tenant_id, statement_pull_id, paybill_id, kind,
						 mpesa_receipt_number, daraja_amount, ledger_amount, context)
					VALUES ($1, $2, $3, 'inbound_in_ledger_missing_statement',
					        $4, NULL, $5, $6)
					RETURNING id
				`,
					tenantID, pullID, paybillID, txid, amt,
					jsonbContext(map[string]any{
						"event_id":     eid,
						"reason":       "Distributor hasn't drained this inbound event",
						"received_at":  to.Format(time.RFC3339),
					}),
				).Scan(&diffID); err != nil {
					return err
				}
				metrics.ReconcilerDiffs.Inc("stuck_inbound", tenantID.String())
				// Best-effort wf task; soft-skip when no def is seeded.
				if _, err := wf.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
					TenantID:    tenantID,
					ProcessKind: "mpesa_reconciliation_diff",
					SubjectKind: "mpesa_reconciliation_diff",
					SubjectID:   diffID,
					Summary: fmt.Sprintf(
						"M-PESA inbound stuck — %s · KES %s",
						txid, amt.StringFixed(2),
					),
					SourceURL: "/accounting/mpesa-reconciliation?diff=" + diffID.String(),
					Context: map[string]any{
						"diff_id":  diffID,
						"event_id": eid,
						"trans_id": txid,
						"amount":   amt.StringFixed(2),
					},
				}); err != nil && !errors.Is(err, workflowclient.ErrDefinitionNotFound) {
					logger.Warn("create reconciliation wf instance", "err", err, "diff_id", diffID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	logger.Info("paybill pass complete",
		"pull_id", pullID, "ledger_inbound", ledgerInbound.StringFixed(2),
		"ledger_outbound", ledgerOutbound.StringFixed(2))
	return nil
}

// ─────────── soak mode ───────────

// runSoak picks the tenant + first active paybill, synthesises N
// inbound events directly into mpesa_inbound_events (bypassing the
// webhook because we don't want to hit Daraja during a soak), waits
// for the distributor to drain them, then asserts diff_count = 0.
func runSoak(ctx context.Context, pool *db.Pool, logger *slog.Logger, n int, deadline time.Duration) error {
	var tenantID, paybillID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT p.tenant_id, p.id
		  FROM mpesa_paybills p
		 WHERE p.status = 'active'
		 ORDER BY p.created_at ASC
		 LIMIT 1
	`).Scan(&tenantID, &paybillID); err != nil {
		return fmt.Errorf("no active paybill to soak against: %w", err)
	}
	soakTag := "SOAK-" + uuid.NewString()[:8]
	logger.Info("soak start", "tag", soakTag, "n", n, "tenant_id", tenantID, "paybill_id", paybillID)

	// Synth-insert N events. All marked unallocated so the
	// distributor's plan is the empty path (cash leg only). That's
	// the minimal validation that the pipeline drains end-to-end
	// without needing seeded members.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			_, err := tx.Exec(ctx, `
				INSERT INTO mpesa_inbound_events
					(tenant_id, paybill_id, shortcode, transaction_id, transaction_time,
					 amount, msisdn, bill_ref, raw_payload, status, resolved_via)
				VALUES ($1, $2, $3, $4, now(), $5, '254700000000', $6,
				        '{}'::jsonb, 'received', 'unallocated')
			`,
				tenantID, paybillID, "SOAK",
				fmt.Sprintf("%s-%05d", soakTag, i),
				decimal.NewFromInt(100),
				soakTag,
			)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("synth-insert: %w", err)
	}

	// Poll until all soak events have status='distributed' (or
	// deadline expires).
	deadlineAt := time.Now().Add(deadline)
	pollInterval := 1 * time.Second
	var drained int
	for time.Now().Before(deadlineAt) {
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM mpesa_inbound_events
			 WHERE tenant_id = $1 AND bill_ref = $2 AND status = 'distributed'
		`, tenantID, soakTag).Scan(&drained); err != nil {
			return err
		}
		if drained == n {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	if drained != n {
		return fmt.Errorf("soak diff > 0: only %d of %d events drained within %s",
			drained, n, deadline)
	}

	// Best-effort cleanup so re-runs don't accumulate.
	_, _ = pool.Exec(ctx, `
		DELETE FROM mpesa_distribution_runs
		 WHERE inbound_event_id IN (
		   SELECT id FROM mpesa_inbound_events WHERE bill_ref = $1
		 )
	`, soakTag)
	_, _ = pool.Exec(ctx, `DELETE FROM mpesa_inbound_events WHERE bill_ref = $1`, soakTag)
	_, _ = pool.Exec(ctx, `DELETE FROM posting_outbox WHERE payload->>'source_module' = 'mpesa.distribution.cash_leg' AND payload->>'narration' LIKE '%M-PESA%' AND payload->>'tenant_id' = $1`, tenantID.String())
	return nil
}

// ─────────── helpers ───────────

func jsonbContext(m map[string]any) []byte {
	b, _ := json.Marshal(m)
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
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
