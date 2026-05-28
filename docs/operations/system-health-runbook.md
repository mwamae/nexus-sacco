# System Health Runbook

The `/platform/system-health` page (Platform → System health in the
admin nav, visible only to platform admins) is the single read-only
view of every service, dependency, and worker. It polls the
aggregator at `GET /v1/platform/system-health` (served by the
identity service) every 10 seconds. This runbook is mirrored
verbatim into the page itself so the on-call doesn't have to leave
it, but the markdown lives here as the source of truth and the place
alerting systems should deep-link to.

## URL + permission

- **Platform-side full view**: `/platform/system-health` (apex /
  reserved platform subdomain only). Backed by
  `GET /v1/platform/system-health`. Requires
  `platform:operations:view` (seeded in identity migration 0030) or
  the `is_platform_admin: true` short-circuit. Mounted under
  `RequirePlatform` — returns 404 on tenant subdomains.
- **Tenant-side slim view**: a small pill on the tenant dashboard
  ("Platform operational" / "Platform degraded" / "Platform outage").
  Backed by `GET /v1/platform-status`, no permission required, any
  authenticated user can read it. Shape is intentionally
  `{overall_status, checked_at, message}` only — no per-tenant data
  leaks across, no service internals revealed.

## Status legend

- **OK** — the service / worker / dependency is healthy.
- **Degraded** — reachable but reporting an internal soft failure
  (outbox lag, dependency slow, etc.). Process traffic carefully;
  investigate.
- **Down** — unreachable, or reporting a hard failure. Page should be
  red-bannered; this is incident-class.

Overall status is the worst-of across services + infrastructure +
workers.

## If a service shows Down

1. Open the orchestrator (`docker compose ps` locally, `kubectl get
   pods` in production) and check whether the container is running.
2. If the container is up but unreachable from identity, the network
   path is the problem — restart the gateway / ingress.
3. If the container is crash-looping, tail its logs:

   ```bash
   docker compose logs <service> --tail 100
   ```

   The first 20 lines after the latest restart usually name the failed
   dep (DB DSN wrong, missing env var, sealer key mismatch).

## If a service shows Degraded

- Look at the service's `details` tile. The most common signal is
  `outbox_pending` growing — the dispatcher worker has stalled.
- For savings / accounting outbox stalls, check the
  `posting-dispatcher` worker row below. If it's also degraded/down,
  restart it.
- Outbox rows that hit max attempts (12) drop out of the queue and
  surface in `GET /v1/finance/posting-outbox?status=stuck`. The
  dedicated Finance → System Health UI page was removed when the
  platform aggregator moved to platform admin; query that endpoint
  directly from a finance pod via `curl`.

## If a worker shows Down

- Workers heartbeat every 30 seconds. Degraded fires at 60s without a
  beat; Down at 120s.
- Override the thresholds via `WORKER_HEARTBEAT_DEGRADED_SEC` +
  `WORKER_HEARTBEAT_DOWN_SEC` on identity (env vars on the aggregator,
  not on the workers).
- The worker process probably crashed. Restart it; if it crashes on
  boot the logs will name the failed dep.

## If infrastructure shows Down

- `postgres` down means identity can't reach the database. Every
  service is degraded in this case. Check `docker compose ps postgres`
  and the disk usage on the DB host (full WAL = no writes).
- `redis` shows "not configured" today. The slot is reserved for the
  cache layer that lands later; the empty state isn't a problem.

## If the page itself won't load — diagnostic checklist

The page no longer hangs forever on a blank loading state. If
something is wrong you should see one of the in-page error states
listed below; if you don't, open DevTools → Network and walk down
this checklist:

1. **Open DevTools → Network**. Find the request to
   `/api/v1/platform/system-health`. The page also renders a small
   muted debug strip at the bottom showing the endpoint, last fetch
   timestamp, and HTTP status code — use that to confirm the call is
   firing at all.
2. **200 + empty `services` array** → the in-page banner reads
   *"The aggregator returned an empty service list. Check
   SYSTEM_HEALTH_TARGETS on the identity service."* Fix the env var
   on identity and restart. The default fallback uses docker-compose
   service names that don't resolve on host-network natives.
3. **401 or 403** → the page shows the explicit *"You don't have
   permission to view System Health"* alert. Confirm your JWT
   includes `is_platform_admin: true` (decode at jwt.io) or the
   `platform:operations:view` permission. Tenant staff on the
   platform host will get this.
4. **404** → identity isn't serving the route. The most common cause
   is a stale binary running from before migration 0030 landed —
   restart identity. On host-native dev, `pkill identity && make
   run-identity`.
5. **5xx** → identity itself crashed inside the handler. Tail
   `docker compose logs identity` (or the equivalent in your
   orchestrator).
6. **Loading state for >10 seconds with no error** → the page now
   surfaces a *"Couldn't fetch system health within 10s"* alert with
   a retry button. If you're still seeing the bare "Loading…" string,
   the deployed build is stale — bust the cache.
7. **Page renders nothing, console shows a React stack** → the page
   is wrapped in an `ErrorBoundary` that surfaces the stack inline.
   If you somehow defeated even that (very old browser, JS disabled),
   the underlying error is logged via `console.error`.

## Permission grants

- `platform:operations:view` — granted to the `platform_admin` role
  by migration 0030. Platform admins also bypass via the
  `IsPlatformAdmin` short-circuit in `RequirePermission`.
- The legacy `tenant:operations:view` permission still exists in the
  catalog but no longer gates any active route. Removal is a follow-up.

## Configuration

- `SYSTEM_HEALTH_TARGETS` on identity — comma-separated
  `service=base_url|role` tuples. Empty value falls back to the
  docker-compose defaults (every service on its docker-compose host
  and port). Set this explicitly when running services natively with
  `make all-up` — the compose defaults won't resolve.
- Aggregator response cache: 5s in-process per identity replica.
  Shared between `/v1/platform/system-health` and
  `/v1/platform-status` so polling tenants don't multiply fan-out
  cost.
- UI poll interval: 10s on the platform page; the tenant pill polls
  exactly once on mount.

## Heartbeat-tracked workers

Three continuous workers write to `worker_heartbeats`:

| Worker | Binary | Cadence |
|---|---|---|
| `posting-dispatcher` | `services/savings/cmd/posting-dispatcher` | 30s |
| `b2c-dispatcher` | `services/mpesa/cmd/b2c-dispatcher` | 30s |
| `distributor` | `services/mpesa/cmd/distributor` | 30s |

The `mpesa-reconciler` (cron-style, runs daily) is intentionally
excluded — heartbeats only make sense for long-lived processes.
