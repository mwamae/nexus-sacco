// GET /v1/platform/system-health — full aggregated health view.
// GET /v1/platform-status      — slim {overall_status, checked_at, message}.
//
// Identity is the only service every operator already authenticates
// against, so the dashboard's single fan-out call lives here instead
// of on accounting / mpesa. The handler:
//
//   1. Reads SYSTEM_HEALTH_TARGETS from env — comma-separated
//      "<service>=<base_url>[|role]" tuples (e.g. "savings=http://savings:8084|consumer").
//      Each entry's /healthz is GET'd in parallel with a 1.5s timeout.
//   2. Probes the local pgxpool for the "postgres" infrastructure
//      slot. Redis isn't wired in this monorepo yet — the field is
//      reserved so the UI can render "not configured" without a
//      schema change later.
//   3. Reads worker_heartbeats and classifies each row as
//      ok / degraded / down based on staleness.
//   4. Computes overall_status as the worst-of across services +
//      infra + workers.
//
// Caching: the snapshot is cached in-process for 5s so the UI's
// auto-refresh (10s by default) only fans out twice per minute
// per replica. Both Get and GetForTenant share the same snapshot,
// so polling tenants don't multiply the fan-out cost.
//
// Permissions:
//   • /v1/platform/system-health → platform:operations:view (migration
//     0030). Mounted under RequirePlatform — only resolves on the
//     platform host, returns 404 on tenant subdomains.
//   • /v1/platform-status        → no permission. Available to any
//     authenticated user on tenant or platform host. Response shape
//     is intentionally identical across tenants — no per-tenant data.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/shared/healthx"
)

// SystemHealthHandler holds the live deps it fans across. Each
// request reads them; the handler's own state is just the cache
// guard.
type SystemHealthHandler struct {
	DB     *db.Pool
	Logger *slog.Logger
	// HTTPClient — separate from http.DefaultClient so test code can
	// substitute a roundtripper. Per-request timeouts are set on the
	// http.Request, not the client.
	HTTPClient *http.Client
	// Cache TTL — defaults to 5s when zero.
	CacheTTL time.Duration

	mu       sync.Mutex
	cache    *SystemHealthResponse
	cacheAt  time.Time
	inflight chan struct{} // dedupes concurrent cold-cache fan-outs

	// snapshotOverride is a test-only hook. When set, snapshot()
	// returns its result directly instead of running real probes.
	// Lets the auth-matrix tests exercise the routing layer without
	// standing up a Postgres pool or upstream HTTP servers.
	snapshotOverride func(context.Context) *SystemHealthResponse
}

// SystemHealthResponse is the wire envelope the UI consumes.
type SystemHealthResponse struct {
	OverallStatus  healthx.Status                 `json:"overall_status"`
	CheckedAt      time.Time                      `json:"checked_at"`
	Services       []ServiceHealth                `json:"services"`
	Infrastructure map[string]InfrastructureCheck `json:"infrastructure"`
	Workers        []WorkerHealth                 `json:"workers"`
}

// ServiceHealth carries one /healthz result, normalised for the UI.
type ServiceHealth struct {
	Name         string                              `json:"name"`
	Role         string                              `json:"role"`
	TargetURL    string                              `json:"target_url"`
	Status       healthx.Status                      `json:"status"`
	Version      string                              `json:"version,omitempty"`
	StartedAt    *time.Time                          `json:"started_at,omitempty"`
	UptimeSec    int64                               `json:"uptime_seconds,omitempty"`
	LatencyMS    int64                               `json:"latency_ms"`
	Dependencies map[string]healthx.DependencyResult `json:"dependencies,omitempty"`
	Details      map[string]any                      `json:"details,omitempty"`
	// Error is set when the probe couldn't fetch /healthz at all
	// (timeout, refused conn, malformed response). Status will be
	// "down" in that case; the field surfaces *why*.
	Error string `json:"error,omitempty"`
}

// InfrastructureCheck — for postgres + the placeholder redis slot.
type InfrastructureCheck struct {
	Status    healthx.Status `json:"status"`
	LatencyMS int64          `json:"latency_ms,omitempty"`
	Error     string         `json:"error,omitempty"`
	Note      string         `json:"note,omitempty"` // e.g. "not configured" for redis
}

// WorkerHealth — one row from worker_heartbeats, classified.
type WorkerHealth struct {
	Name         string         `json:"name"`
	Status       healthx.Status `json:"status"`
	LastBeatAt   time.Time      `json:"last_beat_at"`
	StalenessSec int64          `json:"staleness_seconds"`
	Hostname     string         `json:"hostname,omitempty"`
	Version      string         `json:"version,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

// Get handles the HTTP request. Permission gating is applied in
// routes.go.
func (h *SystemHealthHandler) Get(w http.ResponseWriter, r *http.Request) {
	resp := h.snapshot(r.Context())
	httpx.OK(w, resp)
}

// PlatformStatusResponse — the slim shape tenant admins see. No
// per-service detail, no URLs, no worker heartbeats. Just enough
// for the tenant-side dashboard pill to say "platform is OK" /
// "degraded — ops aware" / "outage in progress".
type PlatformStatusResponse struct {
	OverallStatus healthx.Status `json:"overall_status"`
	CheckedAt     time.Time      `json:"checked_at"`
	Message       string         `json:"message"`
}

// GetForTenant — slim platform-status endpoint mounted at
// /v1/platform-status. Reuses the cached snapshot so a polling
// tenant doesn't multiply fan-out cost. Returns 200 with a derived
// human-readable message regardless of status so the UI can render
// without branching on HTTP code.
func (h *SystemHealthHandler) GetForTenant(w http.ResponseWriter, r *http.Request) {
	snap := h.snapshot(r.Context())
	httpx.OK(w, PlatformStatusResponse{
		OverallStatus: snap.OverallStatus,
		CheckedAt:     snap.CheckedAt,
		Message:       messageFor(snap.OverallStatus),
	})
}

func messageFor(s healthx.Status) string {
	switch s {
	case healthx.StatusOK:
		return "All systems operational"
	case healthx.StatusDegraded:
		return "Some non-critical systems are degraded — operations team has visibility"
	case healthx.StatusDown:
		return "An outage is in progress — operations team is engaged"
	}
	return "Platform status unknown"
}

func (h *SystemHealthHandler) snapshot(ctx context.Context) *SystemHealthResponse {
	if h.snapshotOverride != nil {
		return h.snapshotOverride(ctx)
	}
	ttl := h.CacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}

	h.mu.Lock()
	if h.cache != nil && time.Since(h.cacheAt) < ttl {
		c := h.cache
		h.mu.Unlock()
		return c
	}
	// Cold cache. Dedupe concurrent fan-outs — the second caller
	// blocks on the same channel until the first caller publishes.
	if h.inflight != nil {
		wait := h.inflight
		h.mu.Unlock()
		<-wait
		h.mu.Lock()
		c := h.cache
		h.mu.Unlock()
		if c != nil {
			return c
		}
		// Fall through: producer failed to publish; do our own probe.
		h.mu.Lock()
	}
	h.inflight = make(chan struct{})
	guard := h.inflight
	h.mu.Unlock()

	resp := h.runProbes(ctx)

	h.mu.Lock()
	h.cache = resp
	h.cacheAt = time.Now()
	h.inflight = nil
	h.mu.Unlock()
	close(guard)
	return resp
}

// targets parses SYSTEM_HEALTH_TARGETS:
//
//	"savings=http://savings:8084|consumer,mpesa=http://mpesa:8087|integration"
//
// Empty role defaults to "service". Empty env returns a sensible
// default that mirrors docker-compose.
type targetEntry struct {
	Name string
	URL  string
	Role string
}

func parseTargets(raw string) []targetEntry {
	if strings.TrimSpace(raw) == "" {
		return defaultTargets()
	}
	var out []targetEntry
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var name, url, role string
		parts := strings.SplitN(tok, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name = strings.TrimSpace(parts[0])
		rest := strings.SplitN(parts[1], "|", 2)
		url = strings.TrimSpace(rest[0])
		if len(rest) == 2 {
			role = strings.TrimSpace(rest[1])
		}
		if role == "" {
			role = "service"
		}
		if name == "" || url == "" {
			continue
		}
		out = append(out, targetEntry{Name: name, URL: url, Role: role})
	}
	return out
}

// defaultTargets — what we ship in local docker-compose. Lets
// development work without SYSTEM_HEALTH_TARGETS set explicitly.
// Roles map to the dashboard's grouping ("core" / "consumer" /
// "integration").
func defaultTargets() []targetEntry {
	return []targetEntry{
		{Name: "identity", URL: "http://identity:8081", Role: "core"},
		{Name: "member", URL: "http://member:8082", Role: "consumer"},
		{Name: "workflow", URL: "http://workflow:8083", Role: "core"},
		{Name: "savings", URL: "http://savings:8084", Role: "consumer"},
		{Name: "notification", URL: "http://notification:8085", Role: "core"},
		{Name: "accounting", URL: "http://accounting:8086", Role: "core"},
		{Name: "mpesa", URL: "http://mpesa:8087", Role: "integration"},
	}
}

func (h *SystemHealthHandler) runProbes(parent context.Context) *SystemHealthResponse {
	targets := parseTargets(os.Getenv("SYSTEM_HEALTH_TARGETS"))

	// Per-service fan-out, parallel. 1.5s budget — generous compared
	// to the 500ms probe budget services use internally, so a
	// slow-but-alive replica still reports.
	results := make([]ServiceHealth, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t targetEntry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
			defer cancel()
			results[i] = h.probeService(ctx, t)
		}(i, t)
	}

	// Infrastructure + workers run in parallel with the service
	// fan-out — they're independent reads.
	var (
		infra   map[string]InfrastructureCheck
		workers []WorkerHealth
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
		defer cancel()
		infra = h.probeInfrastructure(ctx)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
		defer cancel()
		workers = h.probeWorkers(ctx)
	}()
	wg.Wait()

	overall := healthx.StatusOK
	for _, s := range results {
		overall = worseStatus(overall, s.Status)
	}
	for _, ic := range infra {
		overall = worseStatus(overall, ic.Status)
	}
	for _, wk := range workers {
		overall = worseStatus(overall, wk.Status)
	}

	// Always marshal as JSON arrays / objects, never null. The UI
	// dereferences .length / Object.entries unconditionally; a nil
	// slice or nil map here would crash the page on first paint.
	if results == nil {
		results = []ServiceHealth{}
	}
	if infra == nil {
		infra = map[string]InfrastructureCheck{}
	}
	if workers == nil {
		workers = []WorkerHealth{}
	}

	return &SystemHealthResponse{
		OverallStatus:  overall,
		CheckedAt:      time.Now().UTC(),
		Services:       results,
		Infrastructure: infra,
		Workers:        workers,
	}
}

func (h *SystemHealthHandler) probeService(ctx context.Context, t targetEntry) ServiceHealth {
	out := ServiceHealth{
		Name:      t.Name,
		Role:      t.Role,
		TargetURL: t.URL,
		Status:    healthx.StatusDown,
	}
	client := h.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL+"/healthz", nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}
	defer resp.Body.Close()
	out.LatencyMS = time.Since(start).Milliseconds()

	var body healthx.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// Old-shape services that still return {"status":"ok"} land
		// here — fall back: status code = source of truth.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			out.Status = healthx.StatusOK
		}
		out.Error = "non-standard response: " + err.Error()
		return out
	}
	out.Status = body.Status
	if out.Status == "" {
		// Empty status field — treat 2xx as ok, anything else as down.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			out.Status = healthx.StatusOK
		} else {
			out.Status = healthx.StatusDown
		}
	}
	out.Version = body.Version
	if !body.StartedAt.IsZero() {
		st := body.StartedAt
		out.StartedAt = &st
		out.UptimeSec = int64(time.Since(st).Seconds())
	}
	out.Dependencies = body.Dependencies
	out.Details = body.Details
	return out
}

func (h *SystemHealthHandler) probeInfrastructure(ctx context.Context) map[string]InfrastructureCheck {
	out := map[string]InfrastructureCheck{}

	// Postgres — direct ping on identity's pool. If identity can't
	// reach the DB, /v1/system-health wouldn't even be hit; this
	// reports the pool's current state for the dashboard's infra row.
	pgStart := time.Now()
	if err := h.DB.Pool.Ping(ctx); err != nil {
		out["postgres"] = InfrastructureCheck{
			Status: healthx.StatusDown,
			Error:  err.Error(),
		}
	} else {
		out["postgres"] = InfrastructureCheck{
			Status:    healthx.StatusOK,
			LatencyMS: time.Since(pgStart).Milliseconds(),
		}
	}

	// Redis — not wired in this monorepo yet. Surface as "not
	// configured" so the UI can render the slot greyed-out without
	// crashing on a missing key.
	out["redis"] = InfrastructureCheck{
		Status: healthx.StatusOK,
		Note:   "not configured",
	}

	return out
}

func (h *SystemHealthHandler) probeWorkers(ctx context.Context) []WorkerHealth {
	rows, err := h.DB.Pool.Query(ctx, `
		SELECT worker_name, last_beat_at, hostname, version, details
		  FROM worker_heartbeats
		 ORDER BY worker_name
	`)
	if err != nil {
		// Table missing or query failed — the dashboard will render
		// "no worker heartbeats reported yet" instead of crashing.
		return nil
	}
	defer rows.Close()
	var out []WorkerHealth
	for rows.Next() {
		var (
			w        WorkerHealth
			hostname *string
			version  *string
			detailsB []byte
			lastBeat time.Time
		)
		if err := rows.Scan(&w.Name, &lastBeat, &hostname, &version, &detailsB); err != nil {
			continue
		}
		w.LastBeatAt = lastBeat
		w.StalenessSec = int64(time.Since(lastBeat).Seconds())
		if hostname != nil {
			w.Hostname = *hostname
		}
		if version != nil {
			w.Version = *version
		}
		if len(detailsB) > 0 {
			_ = json.Unmarshal(detailsB, &w.Details)
		}
		w.Status = classifyWorker(w.StalenessSec)
		out = append(out, w)
	}
	return out
}

// classifyWorker — fixed thresholds. Workers heartbeat every 30s so
// the dashboard expects fresh rows within 60s; 120s+ means a worker
// has died.
//
// Override-able via WORKER_HEARTBEAT_DEGRADED_SEC + _DOWN_SEC for
// services that tune the heartbeat cadence differently.
func classifyWorker(stalenessSec int64) healthx.Status {
	degraded := int64(envIntOrDefault("WORKER_HEARTBEAT_DEGRADED_SEC", 60))
	down := int64(envIntOrDefault("WORKER_HEARTBEAT_DOWN_SEC", 120))
	switch {
	case stalenessSec >= down:
		return healthx.StatusDown
	case stalenessSec >= degraded:
		return healthx.StatusDegraded
	default:
		return healthx.StatusOK
	}
}

func envIntOrDefault(key string, def int) int {
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

// worseStatus mirrors healthx.worse — re-declared because the
// shared one is private. Tiny inline saves an export of an internal
// helper.
func worseStatus(a, b healthx.Status) healthx.Status {
	sev := func(s healthx.Status) int {
		switch s {
		case healthx.StatusOK:
			return 0
		case healthx.StatusDegraded:
			return 1
		case healthx.StatusDown:
			return 2
		}
		return 0
	}
	if sev(b) > sev(a) {
		return b
	}
	return a
}

// Silence unused-import warnings when a build flag prunes one of
// the branches above.
var _ = errors.New
