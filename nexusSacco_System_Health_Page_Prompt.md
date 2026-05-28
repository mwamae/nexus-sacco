# System Health page — aggregate every service's `/healthz` into one admin view

## What this gives you

A single admin page at `/operations/system-health` that shows live status for every nexusSacco service, refreshed on a short interval. Each row tells you: service name, status (ok / degraded / down), URL it's hitting, response time, last-checked timestamp, and any service-specific signal worth surfacing (outbox lag for savings, dispatcher state for mpesa, etc.). The page is the place an operator opens at 9am to confirm the platform is healthy and the page they open at 11pm when something feels off — replacing the current "ssh to the host and curl each /healthz" loop.

## What's already in place (don't rebuild)

I confirmed by grep — every service already exposes `GET /healthz`:

- `services/identity/internal/handler/routes.go:40` — trivial `{status:"ok"}`
- `services/member/internal/handler/routes.go:34` — trivial
- `services/workflow/internal/handler/routes.go:32` — trivial
- `services/accounting/internal/handler/routes.go:52` — trivial
- `services/notification/internal/handler/routes.go:50` — trivial
- `services/savings/internal/handler/routes.go:55` — `Health.Handle` with `{status, outbox_pending, outbox_oldest_age_seconds, accounting_reachable}`
- `services/savings/internal/handler/routes.go:74` — additional `/healthz/finance` config-integrity probe
- `services/mpesa/internal/handler/routes.go:46, 61` — `/healthz` plus `/readyz` with posting outbox check

So the **endpoints exist**. The work is: (1) normalise the response shape, (2) write a small aggregator endpoint that fans out across services and returns a single payload, (3) build the UI on top.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. Build a System Health admin page that shows the live status of every service. The /healthz endpoints exist on every service today; the work is to normalise their response shape, add an aggregator endpoint that fans out from one place, and build the UI that consumes it. Operators visit `/operations/system-health` to see at a glance whether the platform is healthy.
>
> **Scope**
>
> 1. Standardise every service's `/healthz` response shape to a common contract.
> 2. Add a new aggregator endpoint on the identity service: `GET /v1/system-health` that fans out across all services, collects + normalises responses, returns one payload.
> 3. Build the `/operations/system-health` admin page that consumes it.
> 4. Wire auto-refresh + a one-page operator runbook embedded in the page itself.
>
> **Files you will read first**
>
> - `services/identity/internal/handler/routes.go:40`, `services/member/internal/handler/routes.go:34`, `services/workflow/internal/handler/routes.go:32`, `services/accounting/internal/handler/routes.go:52`, `services/notification/internal/handler/routes.go:50` — the trivial `/healthz` handlers.
> - `services/savings/internal/handler/healthz.go` — the only one with rich signal today. Use this shape as the template for everyone else.
> - `services/mpesa/internal/handler/routes.go:46-65` — `/healthz` + `/readyz` pattern.
> - `web/admin/src/api/client.ts` — find an existing GET-against-identity admin endpoint to mirror the call pattern.
> - `docker-compose.yml` — confirms the in-network DNS names (postgres, redis, identity, member, workflow, accounting, savings, mpesa, notification, posting-dispatcher) used in service-to-service URLs.
>
> ---
>
> ### Step 1 — standardise the `/healthz` response shape across every service
>
> Every service's `/healthz` returns the same JSON envelope. Service-specific signals go in the `details` map.
>
> ```json
> {
>   "status": "ok" | "degraded" | "down",
>   "service": "savings",
>   "version": "git-sha-or-build-stamp",
>   "started_at": "2026-05-27T08:00:00Z",
>   "checked_at": "2026-05-27T14:30:21Z",
>   "dependencies": {
>     "database": { "reachable": true, "latency_ms": 3 },
>     "redis":    { "reachable": true, "latency_ms": 1 },
>     "accounting": { "reachable": true, "latency_ms": 12 }
>   },
>   "details": {
>     "outbox_pending": 0,
>     "outbox_oldest_age_seconds": 0
>   }
> }
> ```
>
> 1. Create a small shared package `shared/healthx/healthx.go` exporting:
>    ```go
>    type Status string
>    const (
>        StatusOK       Status = "ok"
>        StatusDegraded Status = "degraded"
>        StatusDown     Status = "down"
>    )
>
>    type DependencyResult struct {
>        Reachable bool  `json:"reachable"`
>        LatencyMS int64 `json:"latency_ms,omitempty"`
>        Error     string `json:"error,omitempty"`
>    }
>
>    type Response struct {
>        Status       Status                      `json:"status"`
>        Service      string                      `json:"service"`
>        Version      string                      `json:"version"`
>        StartedAt    time.Time                   `json:"started_at"`
>        CheckedAt    time.Time                   `json:"checked_at"`
>        Dependencies map[string]DependencyResult `json:"dependencies"`
>        Details      map[string]any              `json:"details,omitempty"`
>    }
>
>    type Probe func(ctx context.Context) (DependencyResult, error)
>
>    type Builder struct {
>        Service   string
>        Version   string
>        StartedAt time.Time
>        Probes    map[string]Probe // dep name → probe fn
>        // Service-specific signal: returns the `details` payload + a
>        // local Status override (e.g. savings degrades when outbox > 60s).
>        DetailsAndStatus func(ctx context.Context) (Status, map[string]any)
>    }
>
>    func (b *Builder) Handler(timeout time.Duration) http.HandlerFunc { … }
>    ```
>    The `Handler` runs every probe with a per-probe `timeout` (default 500ms), in parallel, collects results, runs `DetailsAndStatus`, picks the worst-of (any dep down → service down; any dep slow → service degraded), and writes the response.
> 2. Wire `healthx.Builder` into every service's `cmd/server/main.go`:
>    - **identity**: probes `database`, `redis`. No details.
>    - **member**: probes `database`. No details.
>    - **workflow**: probes `database`. No details.
>    - **accounting**: probes `database`. Details: pending journal-entry rows in the queue if any (`SELECT count(*) FROM journal_entries WHERE status='draft'` — only matters if accounting has a draft state).
>    - **notification**: probes `database`, `smtp` (TCP-connect to host:port; deep ping is too brittle).
>    - **savings**: probes `database`, `accounting` (TCP-connect to its `/healthz`). Details: existing `outbox_pending`, `outbox_oldest_age_seconds`. Status degrades to `degraded` when oldest pending > 60s (env-configurable as already implemented).
>    - **mpesa**: probes `database`, `daraja` (TCP-connect to the configured sandbox/prod URL — don't make the OAuth round-trip, that costs a token and burns the rate limit), `savings` (the `/internal` callback target). Details: `outbox_pending` from `mpesa_inbound_events WHERE status='received'`, `b2c_queued`, `unallocated_events`.
> 3. The `version` field comes from a build-time `-ldflags "-X main.version=$(git rev-parse --short HEAD)"` — add this to each Dockerfile's RUN line. In dev (no ldflags) it falls back to `"dev"`.
> 4. The `started_at` is captured at process boot.
>
> The trivial old `/healthz` handlers (identity, member, workflow, etc.) get replaced with `healthx.Builder.Handler(500*time.Millisecond)`. The savings handler keeps its existing detail signal but moves it into the `DetailsAndStatus` callback.
>
> ### Step 2 — aggregator endpoint on identity
>
> A new admin endpoint `GET /v1/system-health` on identity (or wherever the admin gateway lives — identity is the natural choice since it already auths admin requests). It:
>
> 1. Reads a static service list from config — env var `SYSTEM_HEALTH_TARGETS` is a CSV of `name:url:role` triples:
>    ```
>    SYSTEM_HEALTH_TARGETS=identity:http://identity:8081:core,member:http://member:8082:core,workflow:http://workflow:8083:core,accounting:http://accounting:8086:money,savings:http://savings:8085:money,mpesa:http://mpesa:8084:money,notification:http://notification:8089:comms
>    ```
>    Role is one of: `core | money | comms | infra | worker`. Drives UI grouping. Default value is the above list (identity ships with it baked in).
> 2. Fans out: GET each `{url}/healthz` with a 1s timeout, in parallel. Use a goroutine per target + `sync.WaitGroup`.
> 3. Normalises responses into the shared `healthx.Response` shape. Adds aggregate-only fields: `target_url`, `role`, `aggregate_latency_ms`. On error (connection refused / timeout), synthesises `{status: "down", error: <message>}`.
> 4. Returns:
>    ```json
>    {
>      "overall_status": "ok" | "degraded" | "down",
>      "checked_at": "2026-05-27T14:30:21Z",
>      "services": [
>        { "name": "identity", "role": "core", "target_url": "http://identity:8081",
>          "status": "ok", "version": "abc123", "checked_at": "...",
>          "aggregate_latency_ms": 8, "dependencies": {...}, "details": {} },
>        { "name": "savings", ... },
>        ...
>      ],
>      "infrastructure": {
>        "postgres": { "reachable": true, "latency_ms": 2 },
>        "redis":    { "reachable": true, "latency_ms": 1 }
>      }
>    }
>    ```
>    `overall_status` is worst-of across services + infra. The `infrastructure` block is a direct probe from identity (it has both pool connections). Worker processes (posting-dispatcher, b2c-dispatcher) get a `worker` role and are checked via their **heartbeat table** rather than HTTP — they don't expose an HTTP server. Add a step:
> 5. **Worker heartbeats.** Each background worker (`services/savings/cmd/posting-dispatcher`, `services/mpesa/cmd/b2c-dispatcher`, `services/mpesa/cmd/distributor`, `services/mpesa/cmd/reconciler`) updates a row in a new `worker_heartbeats` table every poll cycle.
>    ```sql
>    -- New migration in identity (shared platform DB)
>    CREATE TABLE IF NOT EXISTS worker_heartbeats (
>      worker_name  text PRIMARY KEY,        -- 'posting-dispatcher', 'mpesa-b2c-dispatcher', etc.
>      last_beat_at timestamptz NOT NULL DEFAULT now(),
>      hostname     text,
>      version      text,
>      details      jsonb
>    );
>    ```
>    Each worker's poll loop: `INSERT ... ON CONFLICT (worker_name) DO UPDATE SET last_beat_at = now(), details = $2`. The aggregator reads these rows and synthesises a "service" entry per worker with `status = ok` if last_beat_at < 60s ago, `degraded` if 60-180s, `down` if > 180s.
> 6. Cache the aggregator result for 5 seconds — the page refreshes every 10s, so a 5s cache halves the load on /healthz calls but stays fresh enough that operators see real changes.
> 7. Auth: require `tenant:operations:view` permission (new permission — add to the seed). Don't gate on platform-admin only; tenant admins want this view too.
>
> ### Step 3 — admin UI page
>
> 1. New page `web/admin/src/pages/Operations/SystemHealth.tsx` mounted at `/operations/system-health`. Add a nav item under a new "Operations" group in the sidebar (alongside Settings).
> 2. Layout:
>    - **Top banner**: large overall status pill. Green "All systems operational" / amber "Some systems degraded" / red "Outage" — text changes with `overall_status`. Last-checked relative timestamp ("2 seconds ago").
>    - **Infrastructure card**: postgres + redis pills. One line each.
>    - **Service cards grouped by role** in a 2-column grid. Each card shows:
>      - Service name, role chip, status pill
>      - Version (short sha), uptime ("running for 4d 3h")
>      - Dependencies row: small reachable/not-reachable pills with latency in ms
>      - Details section: collapsible per-service block rendering the `details` JSON. For savings, render `outbox_pending` and `outbox_oldest_age_seconds` with a warn color when degraded. For mpesa, render `outbox_pending`, `b2c_queued`, `unallocated_events`. For workers, render `last_beat_at` ("4s ago") and the worker's own details.
>      - Target URL, copyable
>    - **Refresh control**: auto-refresh every 10s by default, a button to pause, a button to refresh-now, and a "Last refreshed Xs ago" indicator.
>    - **Runbook panel** at the bottom: collapsible. Renders a small markdown block that explains what to check when something is red. Specifically — for each known degradation state, a paragraph: "Savings degraded with outbox_pending > 0 means the posting-dispatcher worker isn't draining. Check the posting-dispatcher service card above; if it's red, restart the container with `docker compose restart posting-dispatcher`. If it's green but outbox still climbing, the accounting service is probably rejecting posts — see the next paragraph." Source the runbook content from `docs/operations/system-health-runbook.md`; render it via a markdown lib already in the bundle.
> 3. Optimistic UI: on initial load, show skeleton rows for the known service list (from `SYSTEM_HEALTH_TARGETS` echoed back by the API). When the fetch returns, fade in real status. On a fetch error, the page stays last-known-good with a "stale, retrying" indicator — don't blank the page on a transient failure.
> 4. Accessibility: status pills have text labels, not just colours. Screen-reader announces overall_status changes.
>
> ### Step 4 — tests
>
> Go side:
> - `shared/healthx/healthx_test.go` — unit-test the Builder with synthetic probes (one ok, one degraded, one down) and assert the worst-of aggregation.
> - `services/identity/internal/handler/system_health_test.go` — table-driven test with mocked target servers (`httptest`) returning each combination of statuses; assert the aggregator's output shape and `overall_status`.
> - Per-service health smoke test: hit `/healthz` against a freshly-booted in-process server, assert the new shape.
>
> React side:
> - `web/admin/src/pages/Operations/SystemHealth.test.tsx` — render with three mocked payloads (all-ok, one-degraded, one-down) and assert pill colours, status text, runbook visibility.
>
> ### Step 5 — observability + alerts (optional but cheap)
>
> 1. The aggregator endpoint also writes its result to a `/metrics` line per call. Operators wiring Prometheus get `nexussacco_service_status{service="savings"} 1|0` cleanly.
> 2. Add a worker `services/identity/cmd/health-alerter` (or reuse a notification path) that runs the aggregator every minute and posts a notification to the platform-admin channel when `overall_status` flips to non-ok. Optional, behind `SYSTEM_HEALTH_ALERTS_ENABLED=true`. Defaults off.
>
> ### Acceptance walkthrough
>
> 1. Run `make all-up` (the full-stack target from the GL operational PR). Open `/operations/system-health`. Top banner reads "All systems operational" in green. Every service card is green; postgres + redis are green; posting-dispatcher and b2c-dispatcher show worker heartbeats < 60s old.
> 2. Stop the mpesa container: `docker compose stop mpesa`. Within 10s the page refreshes: the mpesa card flips to red, the overall banner flips to amber/red. Latency on the mpesa probe shows `timeout`. The runbook panel highlights the relevant paragraph.
> 3. Restart mpesa: `docker compose start mpesa`. Card recovers within 10s.
> 4. Stop the posting-dispatcher worker. Within 60s the dispatcher's card flips to degraded; within 180s to down (last_beat_at threshold). Restart it; recovery within 10s of the next heartbeat.
> 5. Hit `GET /v1/system-health` directly with curl. Response matches the shape in Step 2.
> 6. Run as a non-`tenant:operations:view` user. The nav item is hidden, the page route returns 403 cleanly.
>
> ### Idempotency / safety
>
> - The /healthz refactor is response-shape-additive: today's callers (kubernetes liveness probes, docker healthcheck) read only `status: ok`; the new shape preserves that as a top-level field. No breakage.
> - The `worker_heartbeats` table is a new shared platform table — add the migration to identity (the platform-data home) so all services (savings, mpesa) can write to it. RLS-free since the table is platform-wide, not tenant-scoped.
> - The `SYSTEM_HEALTH_TARGETS` env var ships with a sensible default; production override is the same comma-list format.
> - The aggregator's 1s per-target timeout caps the worst-case latency of `/v1/system-health` at ~1s + serialisation; the 5s cache prevents thundering-herd.
> - The page MUST work without the alerter (Step 5.2 is optional).
> - `gofmt`, `go vet`, full `go test ./...`, `pnpm test` all green.
>
> When you're done, paste the new aggregator endpoint's sample response, the new nav-item screenshot, and one acceptance screenshot per state (all-ok, one-degraded, one-down) into the PR description.

---

## Why this shape

A System Health page is one of those features that's underwhelming to design and hugely valuable once it ships. Operators stop SSHing in to debug; pages on-call get answered in 30 seconds; the next time someone says "the books aren't moving" you point them at `/operations/system-health` first and rule out infra before debugging code.

Three pieces of the design are worth defending explicitly:

**Shared `shared/healthx` package** — every service uses the same Builder, so the response shape stays in lockstep. Today's drift (savings has rich details, everyone else has `{status:"ok"}`) goes away on day one.

**Worker heartbeats via DB row, not HTTP probe** — background workers don't expose an HTTP server and shouldn't. A heartbeat table is the canonical pattern. It's also useful for future worker-debugging: an operator can `SELECT * FROM worker_heartbeats` and see exactly when each worker last did anything.

**Runbook embedded in the page** — most "what do I do now" reference docs go stale because they live in a different tool. Embedding the runbook in the page that surfaces the state makes the docs gravity-bound to the symptoms they describe. When you add a new degradation state, you add a new paragraph to the runbook in the same PR.