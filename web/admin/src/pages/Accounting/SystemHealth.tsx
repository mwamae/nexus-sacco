// /accounting/system-health — operational health of the GL pipeline.
//
// Polls savings /healthz every 10 seconds and surfaces:
//   • status (ok / degraded)
//   • outbox_pending  (queued GL posts the dispatcher hasn't drained)
//   • outbox_oldest_age_seconds  (lag of the oldest pending row)
//   • accounting_reachable  (TCP-level dial of accounting from savings)
//
// This is the page Mike opens next time he suspects the pipeline is
// silent. The "degraded" badge is the operational signal — paired
// with the server-side log WARN per dry-run event from
// services/savings/internal/posting/client.go's DryRun path.

import { useCallback, useEffect, useState } from 'react';
import { api, extractError } from '../../api/client';
import { Badge } from '../../components/Badge';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

type HealthSnapshot = {
  status: 'ok' | 'degraded';
  outbox_pending: number;
  outbox_oldest_age_seconds: number;
  accounting_reachable: boolean;
};

const POLL_MS = 10_000;

export default function SystemHealth() {
  useDocumentTitle('System health');
  const [snapshot, setSnapshot] = useState<HealthSnapshot | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [lastFetchedAt, setLastFetchedAt] = useState<Date | null>(null);

  const refresh = useCallback(async () => {
    try {
      // /healthz is mounted directly on savings — bypasses /api/v1/...
      // The dev proxy forwards /api/healthz unchanged.
      const r = await api.get('/healthz');
      // The handler emits the snapshot directly (no { data: ... } wrap)
      const body = (r.data?.data ?? r.data) as HealthSnapshot;
      setSnapshot(body);
      setErr(null);
      setLastFetchedAt(new Date());
    } catch (e) {
      setErr(extractError(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const t = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [refresh]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/accounting/dashboard" style={{ color: 'inherit' }}>← Accounting</a></div>
          <h1>System health</h1>
          <div className="page-sub">
            GL posting pipeline state. Refreshes every {POLL_MS / 1000}s.
          </div>
        </div>
        <button className="btn" onClick={() => void refresh()}>↻ Refresh now</button>
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {!snapshot && !err && <div className="empty">Loading…</div>}

      {snapshot && (
        <>
          <div className="card">
            <div className="card-hd">
              <h3>Pipeline state</h3>
              {snapshot.status === 'ok'
                ? <Badge tone="pos">OK</Badge>
                : <Badge tone="neg">Degraded</Badge>}
            </div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
              <Metric
                label="Outbox pending"
                value={snapshot.outbox_pending.toString()}
                hint="GL posts queued in the posting_outbox table that the dispatcher hasn't drained yet"
                tone={snapshot.outbox_pending === 0 ? 'pos' : snapshot.outbox_pending < 10 ? 'warn' : 'neg'}
              />
              <Metric
                label="Oldest pending age"
                value={formatAge(snapshot.outbox_oldest_age_seconds)}
                hint="Wall-clock age of the oldest undelivered outbox row. Threshold for 'degraded' is configurable via SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S (default 60s)."
                tone={
                  snapshot.outbox_oldest_age_seconds === 0
                    ? 'pos'
                    : snapshot.outbox_oldest_age_seconds < 60
                      ? 'warn'
                      : 'neg'
                }
              />
              <Metric
                label="Accounting reachable"
                value={snapshot.accounting_reachable ? 'Yes' : 'No'}
                hint="TCP-level dial probe to the accounting service from savings. No auth round-trip."
                tone={snapshot.accounting_reachable ? 'pos' : 'neg'}
              />
            </div>
          </div>

          {snapshot.status === 'degraded' && (
            <div className="alert alert-warn" style={{ marginTop: 12 }}>
              <strong>Dispatcher appears stalled.</strong> The oldest pending outbox
              row is older than the lag threshold. Common causes:
              <ul style={{ marginTop: 4, marginBottom: 0 }}>
                <li>The <code>posting-dispatcher</code> container is stopped or crashing — <code>docker compose ps posting-dispatcher</code> + <code>docker compose logs posting-dispatcher</code>.</li>
                <li>The accounting service is unreachable — check the "Accounting reachable" tile.</li>
                <li>A row is failing repeatedly — open <code>/v1/finance/posting-outbox?status=stuck</code> to inspect.</li>
              </ul>
            </div>
          )}

          {!snapshot.accounting_reachable && (
            <div className="alert alert-error" style={{ marginTop: 12 }}>
              <strong>Savings can't reach accounting.</strong> Every money event
              will queue in the outbox but no JE will be posted until accounting
              comes back. Check <code>docker compose ps accounting</code>.
            </div>
          )}
        </>
      )}

      {lastFetchedAt && (
        <p className="muted tiny" style={{ marginTop: 12 }}>
          Last refreshed {lastFetchedAt.toLocaleTimeString()}.
        </p>
      )}
    </div>
  );
}

function Metric({ label, value, hint, tone }: {
  label: string;
  value: string;
  hint: string;
  tone: 'pos' | 'warn' | 'neg';
}) {
  const colour = tone === 'pos' ? 'var(--pos)' : tone === 'warn' ? 'var(--warn)' : 'var(--neg)';
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      <div className="mono" style={{ fontSize: 20, fontWeight: 600, color: colour }}>{value}</div>
      <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>
    </div>
  );
}

function formatAge(secs: number): string {
  if (secs === 0) return '0s';
  if (secs < 60) return `${secs}s`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return s === 0 ? `${m}m` : `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm === 0 ? `${h}h` : `${h}h ${mm}m`;
}
