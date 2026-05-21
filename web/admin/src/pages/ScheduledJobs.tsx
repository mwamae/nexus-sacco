// Scheduled jobs admin — list cron-driven background jobs, edit
// schedule/active flag, manually trigger, and see recent run history.
//
// The jobs themselves are seeded by migration; only their cron
// schedule and on/off flag are tenant-editable. Adding a new job_key
// means shipping a Go handler — that's a code change.

import { useEffect, useMemo, useState } from 'react';
import {
  listJobRuns,
  listScheduledJobs,
  previewCron,
  runScheduledJob,
  updateScheduledJob,
  type JobRun,
  type ScheduledJob,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Icon } from '../components/Icon';

export default function ScheduledJobsPage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<ScheduledJob[] | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await listScheduledJobs();
      setItems(r.items);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  const selected = useMemo(() => items?.find((j) => j.id === selectedId) ?? null, [items, selectedId]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Scheduled jobs</div>
          <h1>Scheduled jobs</h1>
          <div className="page-sub">
            Background jobs that run on a cron schedule. Edit the schedule, toggle on/off, or trigger manually.
          </div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-ghost" onClick={() => void load()}>Refresh</button>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-body flush">
          {items === null && <div className="empty">Loading…</div>}
          {items !== null && items.length === 0 && (
            <div className="empty">No scheduled jobs registered for this tenant.</div>
          )}
          {items !== null && items.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Job</th>
                  <th>Cron</th>
                  <th>Active</th>
                  <th>Last run</th>
                  <th>Next run</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {items.map((j) => (
                  <tr key={j.id}>
                    <td>
                      <div><strong>{prettyJobKey(j.job_key)}</strong></div>
                      {j.description && <div className="muted tiny">{j.description}</div>}
                    </td>
                    <td><code className="tiny">{j.cron_expr}</code></td>
                    <td>
                      <span style={{
                        color: j.is_active ? 'var(--pos)' : 'var(--fg-3)',
                        fontWeight: 600,
                      }}>
                        {j.is_active ? 'On' : 'Off'}
                      </span>
                    </td>
                    <td className="tiny">{j.last_run_at ? new Date(j.last_run_at).toLocaleString() : '—'}</td>
                    <td className="tiny">
                      {j.next_run_at ? new Date(j.next_run_at).toLocaleString() : '—'}
                    </td>
                    <td>
                      <button className="btn btn-sm btn-ghost" onClick={() => setSelectedId(j.id)}>Open</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {selected && (
        <JobDetailModal
          job={selected}
          onClose={() => setSelectedId(null)}
          onChanged={() => void load()}
        />
      )}
    </div>
  );
}

function JobDetailModal({
  job, onClose, onChanged,
}: {
  job: ScheduledJob;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [cronExpr, setCronExpr] = useState(job.cron_expr);
  const [isActive, setIsActive] = useState(job.is_active);
  const [nextFirings, setNextFirings] = useState<string[] | null>(null);
  const [runs, setRuns] = useState<JobRun[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  async function loadRuns() {
    try {
      const r = await listJobRuns(job.id, 25);
      setRuns(r.items);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void loadRuns(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [job.id]);

  useEffect(() => {
    let cancelled = false;
    if (!cronExpr.trim()) { setNextFirings(null); return; }
    const t = setTimeout(async () => {
      try {
        const r = await previewCron(cronExpr);
        if (!cancelled) setNextFirings(r.next_firings);
      } catch {
        if (!cancelled) setNextFirings(null);
      }
    }, 300);
    return () => { cancelled = true; clearTimeout(t); };
  }, [cronExpr]);

  async function onSave() {
    setErr(null);
    setBusy('save');
    try {
      await updateScheduledJob(job.id, { cron_expr: cronExpr, is_active: isActive });
      setSavedAt(new Date().toLocaleTimeString());
      onChanged();
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(null);
    }
  }

  async function onRunNow() {
    if (!window.confirm('Run this job now? It will dispatch notifications immediately.')) return;
    setErr(null);
    setBusy('run');
    try {
      const r = await runScheduledJob(job.id);
      await loadRuns();
      onChanged();
      alert(`Job ${r.status}: processed ${r.processed}, failed ${r.failed}`);
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 720, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{prettyJobKey(job.job_key)}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">
          {job.description && <p className="muted" style={{ marginTop: 0 }}>{job.description}</p>}

          <label style={{ display: 'block', marginBottom: 12 }}>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Cron expression</div>
            <input
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
            />
            <div className="muted tiny" style={{ marginTop: 4 }}>
              Standard 5-field cron: <code>min hour dom month dow</code>. E.g. <code>0 8 * * *</code> means 8:00 every day.
            </div>
            {nextFirings && nextFirings.length > 0 && (
              <div style={{ marginTop: 8 }}>
                <div className="muted tiny" style={{ marginBottom: 4 }}>Next 5 firings:</div>
                <ul className="tiny" style={{ margin: 0, paddingLeft: 18 }}>
                  {nextFirings.map((t, i) => <li key={i}>{new Date(t).toLocaleString()}</li>)}
                </ul>
              </div>
            )}
          </label>

          <label className="row" style={{ gap: 6, alignItems: 'center', marginBottom: 12 }}>
            <input
              type="checkbox"
              checked={isActive}
              onChange={(e) => setIsActive(e.target.checked)}
            />
            Active — job fires on its cron schedule.
          </label>

          {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}

          <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 12 }}>
            <button className="btn" disabled={!!busy} onClick={() => void onRunNow()}>
              {busy === 'run' ? 'Running…' : 'Run now'}
            </button>
            <button className="btn btn-accent" disabled={!!busy} onClick={() => void onSave()}>
              {busy === 'save' ? 'Saving…' : 'Save schedule'}
            </button>
            {savedAt && <span className="muted tiny" style={{ alignSelf: 'center' }}>Saved at {savedAt}</span>}
          </div>

          <h4 style={{ marginTop: 20, marginBottom: 8 }}>Recent runs</h4>
          {runs === null && <div className="muted tiny">Loading…</div>}
          {runs !== null && runs.length === 0 && <div className="muted tiny">No runs yet.</div>}
          {runs !== null && runs.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Duration</th>
                  <th className="num">Processed</th>
                  <th className="num">Failed</th>
                  <th>Status</th>
                  <th>Error</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => {
                  const dur = r.finished_at
                    ? `${Math.max(0, Math.round((new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()) / 100) / 10)}s`
                    : 'running';
                  const color =
                    r.status === 'succeeded' ? 'var(--pos)' :
                    r.status === 'failed'    ? 'var(--neg)' :
                                               'var(--accent)';
                  return (
                    <tr key={r.id}>
                      <td className="tiny">{new Date(r.started_at).toLocaleString()}</td>
                      <td className="tiny">{dur}</td>
                      <td className="num">{r.records_processed}</td>
                      <td className="num" style={{ color: r.records_failed > 0 ? 'var(--neg)' : undefined }}>{r.records_failed}</td>
                      <td><span style={{ color, fontWeight: 600 }}>{r.status}</span></td>
                      <td className="tiny" style={{ maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        {r.error_message ?? '—'}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function prettyJobKey(k: string): string {
  return k.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
