// Worker heartbeat — continuous worker binaries (the posting-dispatcher,
// the B2C dispatcher, the distributor) upsert into worker_heartbeats
// on a steady cadence. The /v1/system-health aggregator on identity
// reads last_beat_at to classify each worker as ok / degraded /
// down. Same pattern as healthx for HTTP services; this is the
// non-HTTP equivalent.
//
// Heartbeats are intentionally cheap (a single UPSERT to a tiny
// table). Default cadence — every 30 seconds — gives the
// aggregator a 60-second window to flag degraded and a 120-second
// window to flag down before the worker is reported missing.

package healthx

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WriteHeartbeat upserts one row into worker_heartbeats. Returns
// the underlying SQL error if any. Pure helper; callers that just
// want a background goroutine should use RunHeartbeatLoop.
//
// details may be nil; when non-nil, it is JSON-marshalled into the
// details jsonb column.
func WriteHeartbeat(ctx context.Context, pool *pgxpool.Pool, workerName, version string, details map[string]any) error {
	hostname, _ := os.Hostname()
	var detailsJSON []byte
	if len(details) > 0 {
		var err error
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			return err
		}
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO worker_heartbeats (worker_name, last_beat_at, hostname, version, details)
		VALUES ($1, now(), $2, $3, $4)
		ON CONFLICT (worker_name) DO UPDATE
		   SET last_beat_at = EXCLUDED.last_beat_at,
		       hostname     = EXCLUDED.hostname,
		       version      = EXCLUDED.version,
		       details      = EXCLUDED.details
	`, workerName, hostname, version, detailsJSON)
	return err
}

// RunHeartbeatLoop starts a blocking loop that writes a heartbeat
// every `interval`. Stops when ctx is cancelled. Writes the first
// heartbeat immediately so the aggregator sees the worker as
// healthy from second 0, not second 30.
//
// Logger is optional; nil suppresses per-tick log lines.
//
// detailsFn is called on every tick. Return nil if no per-tick
// detail is meaningful — many workers just want to advertise their
// version + hostname, which the upsert handles itself.
func RunHeartbeatLoop(
	ctx context.Context,
	pool *pgxpool.Pool,
	workerName, version string,
	interval time.Duration,
	detailsFn func() map[string]any,
	logger *slog.Logger,
) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	write := func() {
		var details map[string]any
		if detailsFn != nil {
			details = detailsFn()
		}
		// Per-write timeout — the heartbeat must never block worker
		// shutdown for more than a couple of seconds.
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := WriteHeartbeat(writeCtx, pool, workerName, version, details); err != nil && logger != nil {
			logger.Warn("heartbeat write failed", "worker", workerName, "err", err)
		}
	}
	write()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			write()
		}
	}
}
