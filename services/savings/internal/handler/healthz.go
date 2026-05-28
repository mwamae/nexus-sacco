// /healthz — operational health probe for the savings service,
// built on top of the shared healthx.Builder.
//
// What we report:
//   • database     — pgxpool.Ping
//   • accounting   — HTTPHealthzProbe on the accounting service
//   • Details:
//       outbox_pending             rows in posting_outbox with dispatched_at IS NULL
//       outbox_oldest_age_seconds  age of the oldest pending row
//
// Self-reported status flips to "degraded" when
// outbox_oldest_age_seconds exceeds the threshold (env
// SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S, default 60). That's the
// visible signal the dispatcher has stopped draining.
//
// Public — NOT behind auth or tenant scoping. Health checks must
// always work. The outbox query intentionally runs on the unscoped
// pool (pool.Pool, not WithTenantTx) — health is platform-wide.

package handler

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/shared/healthx"
)

// NewHealthBuilder constructs the per-service Builder used at
// /healthz (and at /v1/finance/health, the proxied UI-facing
// alias). The returned Builder is safe to share; its Handler
// method is the http.HandlerFunc the routes wire in.
//
// lagThresholdSec ≤ 0 falls back to env SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S
// (default 60s).
func NewHealthBuilder(pool *db.Pool, accountingURL, version string, startedAt time.Time, lagThresholdSec int) *healthx.Builder {
	if lagThresholdSec <= 0 {
		lagThresholdSec = envIntOr("SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S", 60)
	}
	return &healthx.Builder{
		Service:   "savings",
		Version:   version,
		StartedAt: startedAt,
		Probes: map[string]healthx.Probe{
			"database":   healthx.DBPingProbe(pool.Pool),
			"accounting": healthx.HTTPHealthzProbe(accountingURL),
		},
		DetailsAndStatus: func(ctx context.Context) (healthx.Status, map[string]any) {
			pending, oldestAge := readOutboxLag(ctx, pool)
			details := map[string]any{
				"outbox_pending":             pending,
				"outbox_oldest_age_seconds":  oldestAge,
			}
			if oldestAge > lagThresholdSec {
				return healthx.StatusDegraded, details
			}
			return healthx.StatusOK, details
		},
	}
}

func readOutboxLag(ctx context.Context, pool *db.Pool) (pending, oldestAgeSeconds int) {
	var oldest *time.Time
	_ = pool.Pool.QueryRow(ctx, `
		SELECT count(*), min(enqueued_at)
		  FROM posting_outbox
		 WHERE dispatched_at IS NULL
	`).Scan(&pending, &oldest)
	if oldest != nil {
		oldestAgeSeconds = int(time.Since(*oldest).Seconds())
	}
	return pending, oldestAgeSeconds
}

func envIntOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
