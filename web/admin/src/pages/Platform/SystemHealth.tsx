// /platform/system-health — the consolidated platform health view.
//
// Polls identity's /v1/platform/system-health every 10s. The
// aggregator fans out across every service's /healthz, reads
// worker_heartbeats, and pings postgres; we render the result as
// role-grouped service cards + infrastructure + workers + runbook.
//
// Permission: platform:operations:view (seeded in identity
// migration 0030). Platform admins also bypass via the
// is_platform_admin short-circuit in RequirePermission. The nav
// entry only appears under the Platform section so the only way to
// land here without permission is a typed URL — when that happens
// the page renders an explicit "you don't have access" empty state
// instead of hanging forever or showing a blank screen.
//
// Defensive rendering: the previous tenant-side version of this
// page could silently render a Loading… skeleton forever when an
// error happened during the very first fetch. The new version:
//
//   * Caps the loading skeleton at 10s; after that we surface
//     diagnostic copy that names the endpoint + suggests Network
//     tab + the 401/403 vs 200-empty branches.
//   * Surfaces e.response.status / e.response.data on axios errors,
//     not just extractError() which returns "" for unusual shapes.
//   * Logs a one-line console.info summary on each successful fetch
//     so an operator opening DevTools sees the snapshot immediately.
//   * Logs console.warn with the raw payload when the snapshot is
//     suspicious (no services, missing infra, all workers down).
//   * Renders an unconditional debug strip in the page footer so
//     "is the API even being called" is never a question.
//   * Banner above the page when snapshot.services is empty but the
//     HTTP call succeeded — the SYSTEM_HEALTH_TARGETS misconfiguration
//     failure mode that produced the original blank page.
//   * Wrapped in <ErrorBoundary> so a render crash shows the stack
//     instead of unmounting the shell.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { AxiosError } from 'axios';
import { api, extractError } from '../../api/client';
import { Badge } from '../../components/Badge';
import { ErrorBoundary } from '../../components/ErrorBoundary';
import { useAuth } from '../../auth/AuthContext';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

type Status = 'ok' | 'degraded' | 'down';

type DependencyResult = {
  reachable: boolean;
  latency_ms?: number;
  error?: string;
};

type ServiceHealth = {
  name: string;
  role: string;
  target_url: string;
  status: Status;
  version?: string;
  started_at?: string;
  uptime_seconds?: number;
  latency_ms: number;
  dependencies?: Record<string, DependencyResult>;
  details?: Record<string, unknown>;
  error?: string;
};

type InfrastructureCheck = {
  status: Status;
  latency_ms?: number;
  error?: string;
  note?: string;
};

type WorkerHealth = {
  name: string;
  status: Status;
  last_beat_at: string;
  staleness_seconds: number;
  hostname?: string;
  version?: string;
  details?: Record<string, unknown>;
};

type SystemHealth = {
  overall_status: Status;
  checked_at: string;
  services: ServiceHealth[];
  infrastructure: Record<string, InfrastructureCheck>;
  workers: WorkerHealth[];
};

const POLL_MS = 10_000;
const LOADING_TIMEOUT_MS = 10_000;
const ENDPOINT = '/v1/platform/system-health';

const ROLE_LABELS: Record<string, string> = {
  core: 'Core platform',
  consumer: 'Consumer services',
  integration: 'External integrations',
  service: 'Services',
};

export default function PlatformSystemHealth() {
  return (
    <ErrorBoundary
      fallback={(err, retry) => (
        <div className="page">
          <div className="page-hd"><h1>System health</h1></div>
          <div className="alert alert-error">
            <strong>This page crashed while rendering.</strong>
            <pre className="code" style={{ marginTop: 8, whiteSpace: 'pre-wrap' }}>
              {err.stack ?? err.message}
            </pre>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={retry}>Retry</button>
          </div>
        </div>
      )}
    >
      <PlatformSystemHealthBody />
    </ErrorBoundary>
  );
}

function PlatformSystemHealthBody() {
  useDocumentTitle('System health');
  const { hasPermission, user } = useAuth();
  const allowed = hasPermission('platform:operations:view') || !!user?.is_platform_admin;

  const [snapshot, setSnapshot] = useState<SystemHealth | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [lastStatusCode, setLastStatusCode] = useState<number | null>(null);
  const [lastFetchedAt, setLastFetchedAt] = useState<Date | null>(null);
  const [runbookOpen, setRunbookOpen] = useState(false);
  // Flips true once the loading window has elapsed without a snapshot
  // or error. Drives the "this should have loaded by now" empty state.
  const [loadingTimedOut, setLoadingTimedOut] = useState(false);
  const initialLoad = useRef(true);

  const refresh = useCallback(async () => {
    try {
      const r = await api.get(ENDPOINT);
      const raw = (r.data?.data ?? r.data) as Partial<SystemHealth> | null;
      // Defensive coalesce — Go nil slices marshal as JSON null and
      // the rest of the page dereferences .length / .map / Object.entries
      // unconditionally. We normalise to empty containers here so a
      // server-side regression can't crash the page render.
      const body: SystemHealth = {
        overall_status: raw?.overall_status ?? 'ok',
        checked_at: raw?.checked_at ?? new Date().toISOString(),
        services: raw?.services ?? [],
        infrastructure: raw?.infrastructure ?? {},
        workers: raw?.workers ?? [],
      };
      setSnapshot(body);
      setErr(null);
      setLastStatusCode(r.status);
      setLastFetchedAt(new Date());

      // Operator-facing diagnostics — visible in DevTools without
      // needing to peer into React state.
      const svcCount = body.services?.length ?? 0;
      const wkCount = body.workers?.length ?? 0;
      const infraKeys = Object.keys(body.infrastructure ?? {});
      console.info(
        `Platform System Health: status=${body.overall_status}, services=${svcCount}, workers=${wkCount}, infrastructure=${infraKeys.join(',') || 'none'}`,
      );
      if (svcCount === 0 || infraKeys.length === 0 || (wkCount > 0 && body.workers.every((w) => w.status === 'down'))) {
        console.warn('Platform System Health: snapshot looks suspicious', body);
      }
    } catch (e) {
      const axiosErr = e as AxiosError<{ error?: { message?: string } }>;
      const status = axiosErr?.response?.status ?? 0;
      const data = axiosErr?.response?.data;
      setLastStatusCode(status || null);
      setLastFetchedAt(new Date());

      let message: string;
      if (status === 401 || status === 403) {
        message = "You don't have permission to view System Health. This page requires platform admin access.";
      } else if (status >= 400) {
        const detail = typeof data === 'object' && data
          ? (data.error?.message ?? JSON.stringify(data))
          : '';
        message = `HTTP ${status} from ${ENDPOINT}${detail ? ' — ' + detail : ''}`;
      } else {
        // Network error / aborted / no response.
        const fallback = extractError(e) || axiosErr?.message || 'unknown error';
        message = `Couldn't reach ${ENDPOINT}: ${fallback}`;
      }
      setErr(message);
    } finally {
      initialLoad.current = false;
    }
  }, []);

  useEffect(() => {
    if (!allowed) return;
    void refresh();
    const t = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [refresh, allowed]);

  // Loading-timeout guard. If the initial fetch hasn't resolved
  // (snapshot AND err both still null) after LOADING_TIMEOUT_MS,
  // surface the diagnostic empty state instead of the bare
  // "Loading…" string the user used to see.
  useEffect(() => {
    if (!allowed) return;
    const t = setTimeout(() => {
      if (!snapshot && !err) setLoadingTimedOut(true);
    }, LOADING_TIMEOUT_MS);
    return () => clearTimeout(t);
  }, [allowed, snapshot, err]);

  // Group services by role for the visual layout. Memo so the
  // groups don't re-render every snapshot tick.
  const grouped = useMemo(() => {
    if (!snapshot) return new Map<string, ServiceHealth[]>();
    const m = new Map<string, ServiceHealth[]>();
    for (const s of snapshot.services) {
      const arr = m.get(s.role) ?? [];
      arr.push(s);
      m.set(s.role, arr);
    }
    const ordered = new Map<string, ServiceHealth[]>();
    for (const k of ['core', 'consumer', 'integration']) {
      if (m.has(k)) ordered.set(k, m.get(k)!);
    }
    for (const [k, v] of m) {
      if (!ordered.has(k)) ordered.set(k, v);
    }
    return ordered;
  }, [snapshot]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>System health</h1></div>
        <div className="alert alert-warn">
          You need the <code>platform:operations:view</code> permission to view this page.
          This is a platform-admin surface; tenant admins see the slim platform-status pill
          on the tenant dashboard instead.
        </div>
      </div>
    );
  }

  const servicesEmpty = !!snapshot && snapshot.services.length === 0;

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Platform · Operations</div>
          <h1>System health</h1>
          <div className="page-sub">
            Live status of every service, dependency, and worker. Auto-refresh every {POLL_MS / 1000}s.
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          {snapshot && (
            <StatusBadge status={snapshot.overall_status} />
          )}
          <button
            className="btn btn-sm"
            onClick={() => setRunbookOpen((v) => !v)}
            aria-expanded={runbookOpen}
            aria-controls="system-health-runbook"
          >
            {runbookOpen ? 'Hide runbook' : 'Show runbook'}
          </button>
          <button className="btn" onClick={() => void refresh()}>↻ Refresh now</button>
        </div>
      </div>

      {servicesEmpty && (
        <div className="alert alert-warn" role="alert">
          <strong>The aggregator returned an empty service list.</strong> Check{' '}
          <code>SYSTEM_HEALTH_TARGETS</code> on the identity service —
          it's the comma-separated list of <code>name=url|role</code> tuples
          the aggregator fans out across. Infrastructure + workers are
          probed independently and may still be reporting below.
        </div>
      )}

      {err && (
        <div className="alert alert-error" role="alert">
          <strong>Couldn't fetch system health.</strong>
          <div style={{ marginTop: 4 }}>{err}</div>
          <div className="muted tiny" style={{ marginTop: 6 }}>
            The aggregator endpoint is <code>{ENDPOINT}</code>. If identity itself is down,
            this page can't load and the underlying issue is visible in the container orchestrator.
            Open DevTools → Network to see the raw response.
          </div>
        </div>
      )}

      {!snapshot && !err && !loadingTimedOut && initialLoad.current && (
        <div className="empty">Loading…</div>
      )}

      {!snapshot && !err && loadingTimedOut && (
        <div className="alert alert-warn" role="alert">
          <strong>Couldn't fetch system health within {LOADING_TIMEOUT_MS / 1000}s.</strong>
          <div className="muted" style={{ marginTop: 6 }}>
            Open DevTools → Network and look for <code>{ENDPOINT}</code>. If the
            request is returning non-2xx, the response body explains why.
            Common causes: identity is down (404 on the route), you're missing
            the <code>platform:operations:view</code> permission (403), or
            the aggregator's upstreams are slow (long-running pending request).
          </div>
          <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => { setLoadingTimedOut(false); void refresh(); }}>
            Retry now
          </button>
        </div>
      )}

      {snapshot && (
        <>
          {/* Per-role service groupings */}
          {[...grouped.entries()].map(([role, services]) => (
            <div key={role} className="card" style={{ marginTop: 12 }}>
              <div className="card-hd">
                <h3>{ROLE_LABELS[role] ?? role}</h3>
              </div>
              <div className="card-body" style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))',
                gap: 12,
              }}>
                {services.map((s) => <ServiceCard key={s.name} svc={s} />)}
              </div>
            </div>
          ))}

          {/* Infrastructure block */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Infrastructure</h3></div>
            <div className="card-body" style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
              gap: 12,
            }}>
              {Object.entries(snapshot.infrastructure).map(([name, ic]) => (
                <InfrastructureCard key={name} name={name} check={ic} />
              ))}
            </div>
          </div>

          {/* Worker heartbeats */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Background workers</h3></div>
            <div className="card-body">
              {snapshot.workers.length === 0 ? (
                <div className="muted">
                  No worker heartbeats reported yet. After deploying, the first heartbeats
                  arrive within 30 seconds.
                </div>
              ) : (
                <table className="tbl" aria-label="Background worker heartbeats">
                  <thead>
                    <tr>
                      <th>Worker</th>
                      <th>Status</th>
                      <th>Last beat</th>
                      <th>Host</th>
                      <th>Version</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.workers.map((w) => (
                      <tr key={w.name}>
                        <td className="mono">{w.name}</td>
                        <td><StatusBadge status={w.status} /></td>
                        <td>{formatAgo(w.staleness_seconds)}</td>
                        <td className="mono">{w.hostname || '—'}</td>
                        <td className="mono">{w.version || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          {runbookOpen && (
            <div id="system-health-runbook" className="card" style={{ marginTop: 12 }}>
              <div className="card-hd"><h3>Runbook</h3></div>
              <div className="card-body">
                <Runbook />
              </div>
            </div>
          )}

          {lastFetchedAt && (
            <p className="muted tiny" style={{ marginTop: 12 }}>
              Last refreshed {lastFetchedAt.toLocaleTimeString()}; aggregator checked at{' '}
              {new Date(snapshot.checked_at).toLocaleTimeString()}.
            </p>
          )}
        </>
      )}

      {/* Debug strip — always rendered, even on the loading state.
          Removes "is the API even being called?" from the troubleshooting
          decision tree. */}
      <p className="muted tiny" data-testid="system-health-debug" style={{ marginTop: 12, opacity: 0.7 }}>
        Endpoint: <code>{ENDPOINT}</code>
        {' · '}Last fetch: {lastFetchedAt ? lastFetchedAt.toISOString() : '—'}
        {' · '}Status code: {lastStatusCode ?? '—'}
      </p>
    </div>
  );
}

function ServiceCard({ svc }: { svc: ServiceHealth }) {
  const reachable = !svc.error;
  return (
    <div
      style={{
        border: '1px solid var(--border)',
        borderRadius: 8,
        padding: 12,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
      aria-label={`${svc.name} ${svc.status}`}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <strong className="mono">{svc.name}</strong>
        <StatusBadge status={svc.status} />
      </div>
      <div className="muted tiny mono" style={{ wordBreak: 'break-all' }}>{svc.target_url}</div>
      {!reachable && svc.error && (
        <div className="alert alert-error" style={{ fontSize: 12 }}>{svc.error}</div>
      )}
      <div className="tiny" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4 }}>
        {svc.version && <div><span className="muted">version</span> <span className="mono">{svc.version}</span></div>}
        <div><span className="muted">latency</span> <span className="mono">{svc.latency_ms}ms</span></div>
        {svc.uptime_seconds !== undefined && svc.uptime_seconds > 0 && (
          <div><span className="muted">uptime</span> <span className="mono">{formatUptime(svc.uptime_seconds)}</span></div>
        )}
      </div>
      {svc.dependencies && Object.keys(svc.dependencies).length > 0 && (
        <div>
          <div className="muted tiny" style={{ marginBottom: 2 }}>Dependencies</div>
          <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12 }}>
            {Object.entries(svc.dependencies).map(([dep, r]) => (
              <li key={dep}>
                <span className="mono">{dep}</span>:{' '}
                {r.reachable ? (
                  <span style={{ color: 'var(--pos)' }}>✓ {r.latency_ms ?? 0}ms</span>
                ) : (
                  <span style={{ color: 'var(--neg)' }}>✗ {r.error || 'unreachable'}</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {svc.details && Object.keys(svc.details).length > 0 && (
        <div>
          <div className="muted tiny" style={{ marginBottom: 2 }}>Details</div>
          <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12 }}>
            {Object.entries(svc.details).map(([k, v]) => (
              <li key={k}>
                <span className="mono">{k}</span>: <span className="mono">{formatDetail(v)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function InfrastructureCard({ name, check }: { name: string; check: InfrastructureCheck }) {
  return (
    <div
      style={{
        border: '1px solid var(--border)',
        borderRadius: 8,
        padding: 12,
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <strong className="mono">{name}</strong>
        <StatusBadge status={check.status} />
      </div>
      {check.latency_ms !== undefined && check.latency_ms > 0 && (
        <div className="muted tiny" style={{ marginTop: 4 }}>{check.latency_ms}ms</div>
      )}
      {check.note && (
        <div className="muted tiny" style={{ marginTop: 4 }}>{check.note}</div>
      )}
      {check.error && (
        <div className="alert alert-error" style={{ fontSize: 12, marginTop: 4 }}>{check.error}</div>
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: Status }) {
  switch (status) {
    case 'ok':       return <Badge tone="pos">OK</Badge>;
    case 'degraded': return <Badge tone="warn">Degraded</Badge>;
    case 'down':     return <Badge tone="neg">Down</Badge>;
  }
}

function formatDetail(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

function formatAgo(seconds: number): string {
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

// Inline runbook — duplicates the markdown file at
// docs/operations/system-health-runbook.md so the on-call doesn't
// have to leave the page. Keep the two in rough sync; the markdown
// file is the source of truth for git blame + future linkage from
// alerting systems.
function Runbook() {
  return (
    <div style={{ lineHeight: 1.6 }}>
      <h4 style={{ marginTop: 0 }}>If a service shows <strong>Down</strong></h4>
      <ol>
        <li>
          Open the orchestrator (docker compose locally,{' '}
          <code>kubectl get pods</code> in production) and check whether
          the container is running.
        </li>
        <li>
          If the container is up but unreachable, the network path is
          the problem — restart the gateway / nginx / ingress.
        </li>
        <li>
          If the container is crash-looping, tail its logs:
          <pre className="code" style={{ marginTop: 4 }}>docker compose logs &lt;service&gt; --tail 100</pre>
        </li>
      </ol>

      <h4>If a service shows <strong>Degraded</strong></h4>
      <ul>
        <li>
          Look at the service's <code>details</code> tile. The most common
          signal is <code>outbox_pending</code> growing — the dispatcher
          worker has stalled.
        </li>
        <li>
          For savings / accounting outbox stalls, check the{' '}
          <code>posting-dispatcher</code> worker row below. If it's also
          degraded/down, restart it.
        </li>
        <li>
          Outbox rows that hit max attempts (12) drop out of the queue
          and surface in <code>/v1/finance/posting-outbox?status=stuck</code>
          (query directly via curl from a finance pod — the dedicated
          UI page was removed when System Health moved to platform).
        </li>
      </ul>

      <h4>If a worker shows <strong>Down</strong></h4>
      <ul>
        <li>
          Workers heartbeat every 30 seconds. Degraded = no heartbeat
          for 60s. Down = no heartbeat for 120s. Tunable via{' '}
          <code>WORKER_HEARTBEAT_DEGRADED_SEC</code> +{' '}
          <code>WORKER_HEARTBEAT_DOWN_SEC</code> on identity.
        </li>
        <li>
          The worker process probably crashed. Restart it; if it crashes
          on boot the logs will name the failed dep (sealer key
          missing, accounting unreachable, DB DSN wrong).
        </li>
      </ul>

      <h4>If infrastructure shows <strong>Down</strong></h4>
      <ul>
        <li>
          <code>postgres</code> down means identity can't reach the
          database. Every service is degraded in this case. Check{' '}
          <code>docker compose ps postgres</code> and the disk usage on
          the DB host (full WAL = no writes).
        </li>
        <li>
          <code>redis</code> shows "not configured" today. The slot is
          reserved for the cache layer that lands later; the empty state
          isn't a problem.
        </li>
      </ul>

      <h4>If the page itself won't load</h4>
      <ol>
        <li>
          Open DevTools → Network tab. Look for <code>{ENDPOINT}</code>.
        </li>
        <li>
          200 with an empty <code>services</code> array → fix{' '}
          <code>SYSTEM_HEALTH_TARGETS</code> on identity. The banner above
          calls this out automatically.
        </li>
        <li>
          401 / 403 → your account isn't a platform admin. Confirm{' '}
          <code>is_platform_admin: true</code> on your JWT (decode at
          jwt.io) or that you have <code>platform:operations:view</code>.
        </li>
        <li>
          404 → identity isn't serving this route. The most common cause is
          a stale binary running from before the System Health PR landed —
          restart identity.
        </li>
        <li>
          5xx → identity itself crashed inside the handler. Tail{' '}
          <code>docker compose logs identity</code>.
        </li>
      </ol>
    </div>
  );
}
