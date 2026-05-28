// Common probe constructors for the healthx Builder. Three shapes
// cover ~all dependency checks today:
//
//   • DBPingProbe — pgxpool.Ping with a short context. Used by every
//     service to probe its own postgres pool.
//
//   • TCPDialProbe — net.DialTimeout to host:port from a URL string.
//     Cheap "is this thing accepting connections" check; doesn't
//     auth or HTTP-round-trip. Used for SMTP, redis-via-host:port,
//     Daraja sandbox (where the OAuth call costs a token), etc.
//
//   • HTTPHealthzProbe — GET {url}/healthz with a short context.
//     The deeper check; the upstream's own health endpoint is
//     authoritative for "is this service actually serving."

package healthx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBPingProbe returns a Probe that does a cheap pgxpool.Ping. The
// per-probe context carries the timeout the Builder passed.
func DBPingProbe(pool *pgxpool.Pool) Probe {
	return func(ctx context.Context) DependencyResult {
		if pool == nil {
			return DependencyResult{Reachable: false, Error: "pool is nil"}
		}
		start := time.Now()
		if err := pool.Ping(ctx); err != nil {
			return DependencyResult{Reachable: false, Error: err.Error()}
		}
		return DependencyResult{
			Reachable: true,
			LatencyMS: time.Since(start).Milliseconds(),
		}
	}
}

// TCPDialProbe returns a Probe that net.DialTimeout's the host:port
// derived from a URL string. Empty URL → unreachable with a clear
// error so the operator knows what to wire.
func TCPDialProbe(rawURL string) Probe {
	return func(ctx context.Context) DependencyResult {
		if rawURL == "" {
			return DependencyResult{Reachable: false, Error: "url not configured"}
		}
		host, err := hostFromURL(rawURL)
		if err != nil {
			return DependencyResult{Reachable: false, Error: err.Error()}
		}
		deadline, ok := ctx.Deadline()
		timeout := 500 * time.Millisecond
		if ok {
			if rem := time.Until(deadline); rem > 0 && rem < timeout {
				timeout = rem
			}
		}
		start := time.Now()
		conn, err := net.DialTimeout("tcp", host, timeout)
		if err != nil {
			return DependencyResult{Reachable: false, Error: err.Error()}
		}
		_ = conn.Close()
		return DependencyResult{
			Reachable: true,
			LatencyMS: time.Since(start).Milliseconds(),
		}
	}
}

// HTTPHealthzProbe returns a Probe that GETs {baseURL}/healthz with
// the per-probe context's deadline. Treats any non-2xx as
// unreachable. The upstream's body is intentionally ignored — the
// aggregator does the deep read; this probe is just the boundary.
func HTTPHealthzProbe(baseURL string) Probe {
	return func(ctx context.Context) DependencyResult {
		if baseURL == "" {
			return DependencyResult{Reachable: false, Error: "url not configured"}
		}
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			return DependencyResult{Reachable: false, Error: err.Error()}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return DependencyResult{Reachable: false, Error: err.Error()}
		}
		defer resp.Body.Close()
		lat := time.Since(start).Milliseconds()
		// 503 is the upstream's "i'm here but degraded" signal —
		// reachable=true, but the caller's worst-of aggregation will
		// pick up the actual status from the aggregator's parsed body.
		if resp.StatusCode >= 500 && resp.StatusCode != http.StatusServiceUnavailable {
			return DependencyResult{
				Reachable: false,
				LatencyMS: lat,
				Error:     fmt.Sprintf("upstream status %d", resp.StatusCode),
			}
		}
		return DependencyResult{Reachable: true, LatencyMS: lat}
	}
}

// hostFromURL extracts host:port from a URL, defaulting ports to
// the scheme convention. Pure helper — exported for tests.
func hostFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("missing host in url")
	}
	if u.Port() != "" {
		return u.Host, nil
	}
	switch u.Scheme {
	case "https":
		return u.Host + ":443", nil
	case "http", "":
		return u.Host + ":80", nil
	default:
		return u.Host, nil
	}
}
