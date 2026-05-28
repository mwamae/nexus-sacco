// Package healthx is the per-service health-probe primitive shared
// across the platform. Each service constructs a Builder at startup
// (with its DB / dependency probes wired in) and serves the Handler
// at /healthz.
//
// The contract is one JSON envelope; the aggregator on identity at
// /v1/system-health fans out across every service's /healthz and
// normalises into a single payload for the operations dashboard.
//
// Worst-of aggregation: if any dep is down → service is down; if
// any dep is slow OR the service's own DetailsAndStatus reports
// degraded → service is degraded; else ok.
//
// The previous trivial {"status":"ok"} response shape is preserved
// as a top-level field on the new envelope, so today's callers
// (kubernetes liveness, docker healthcheck) keep working without
// change.

package healthx

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status is the per-service rollup. Values are sortable
// alphabetically: degraded < down < ok is meaningless; we sort by
// severity via worse() below.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// DependencyResult is the per-dependency snapshot. A probe returns
// one of these. Reachable=false means the dep is down; LatencyMS is
// populated only when Reachable=true.
type DependencyResult struct {
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Response is the wire envelope every service's /healthz returns.
type Response struct {
	Status       Status                      `json:"status"`
	Service      string                      `json:"service"`
	Version      string                      `json:"version"`
	StartedAt    time.Time                   `json:"started_at"`
	CheckedAt    time.Time                   `json:"checked_at"`
	Dependencies map[string]DependencyResult `json:"dependencies"`
	Details      map[string]any              `json:"details,omitempty"`
}

// Probe runs one dependency check. Implementations are constructed
// via the helpers in probes.go (DBPingProbe, TCPDialProbe,
// HTTPHealthzProbe) — services compose those at startup.
//
// A Probe returns either a populated DependencyResult OR an error
// (mutually exclusive — the Handler treats either as "dep is down").
type Probe func(ctx context.Context) DependencyResult

// Builder constructs the per-service health handler. Each service
// builds one in cmd/server/main.go:
//
//	b := &healthx.Builder{
//	    Service:   "savings",
//	    Version:   buildVersion,
//	    StartedAt: bootTime,
//	    Probes: map[string]healthx.Probe{
//	        "database":   healthx.DBPingProbe(pool.Pool),
//	        "accounting": healthx.HTTPHealthzProbe(cfg.AccountingURL),
//	    },
//	    DetailsAndStatus: func(ctx context.Context) (healthx.Status, map[string]any) {
//	        // read outbox + return ("degraded"/"ok", {outbox_pending, ...})
//	    },
//	}
//	r.Get("/healthz", b.Handler(500*time.Millisecond))
type Builder struct {
	Service   string
	Version   string
	StartedAt time.Time

	// Probes — dep name → probe. Probes run in parallel; the
	// per-probe context carries the timeout passed to Handler.
	Probes map[string]Probe

	// DetailsAndStatus is the service-specific signal. Returns the
	// payload that goes into the `details` map AND a Status the
	// service self-reports (e.g. savings → degraded when outbox is
	// stale). Nil = no detail signal; status defaults to ok.
	DetailsAndStatus func(ctx context.Context) (Status, map[string]any)
}

// Handler returns the http.HandlerFunc to mount at /healthz.
// perProbeTimeout caps the time each individual probe is allowed —
// the Handler's overall budget is roughly perProbeTimeout (probes
// run in parallel, not sequentially).
//
// HTTP status code: 200 when status=ok, 503 otherwise. LBs that
// only read status codes drain traffic correctly without parsing
// the JSON body.
func (b *Builder) Handler(perProbeTimeout time.Duration) http.HandlerFunc {
	if perProbeTimeout <= 0 {
		perProbeTimeout = 500 * time.Millisecond
	}
	return func(w http.ResponseWriter, r *http.Request) {
		resp := b.snapshot(r.Context(), perProbeTimeout)
		w.Header().Set("Content-Type", "application/json")
		if resp.Status != StatusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// Snapshot is the testable + reusable form of the Handler logic.
// Useful when a service wants to inspect its own health without
// going via HTTP (a startup self-check, an alerter loop).
func (b *Builder) Snapshot(ctx context.Context, perProbeTimeout time.Duration) Response {
	if perProbeTimeout <= 0 {
		perProbeTimeout = 500 * time.Millisecond
	}
	return b.snapshot(ctx, perProbeTimeout)
}

func (b *Builder) snapshot(parent context.Context, timeout time.Duration) Response {
	deps := make(map[string]DependencyResult, len(b.Probes))
	if len(b.Probes) > 0 {
		var (
			mu sync.Mutex
			wg sync.WaitGroup
		)
		for name, probe := range b.Probes {
			wg.Add(1)
			go func(name string, probe Probe) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(parent, timeout)
				defer cancel()
				res := probe(ctx)
				mu.Lock()
				deps[name] = res
				mu.Unlock()
			}(name, probe)
		}
		wg.Wait()
	}

	// Aggregate: worst-of across deps + the service's own self-report.
	status := StatusOK
	for _, d := range deps {
		if !d.Reachable {
			status = worse(status, StatusDown)
		}
	}
	var details map[string]any
	if b.DetailsAndStatus != nil {
		// Self-report runs against the same overall budget as one
		// probe — the service's DB query / outbox read should be just
		// as cheap as a TCP dial.
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		selfStatus, d := b.DetailsAndStatus(ctx)
		if selfStatus != "" {
			status = worse(status, selfStatus)
		}
		details = d
	}

	return Response{
		Status:       status,
		Service:      b.Service,
		Version:      b.versionOrDev(),
		StartedAt:    b.StartedAt,
		CheckedAt:    time.Now().UTC(),
		Dependencies: deps,
		Details:      details,
	}
}

func (b *Builder) versionOrDev() string {
	if b.Version == "" {
		return "dev"
	}
	return b.Version
}

// worse returns the more-severe of two statuses. Severity:
//
//	ok < degraded < down
//
// Empty self-status falls through to the dependency rollup
// unchanged.
func worse(a, b Status) Status {
	sev := func(s Status) int {
		switch s {
		case StatusOK:
			return 0
		case StatusDegraded:
			return 1
		case StatusDown:
			return 2
		}
		return 0
	}
	if sev(b) > sev(a) {
		return b
	}
	return a
}
