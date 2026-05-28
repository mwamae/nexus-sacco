// Loans Phase 3 — Loan Loss Provisioning (v2).
//
// Monthly cycle backed by /v1/loans/provisioning/*. Each run is keyed
// by period_month (first of the month) and reads from loan_dpd_snapshots
// + ecl_rate_matrix (no live DPD recompute — the dpd-classifier worker
// produces a snapshot per loan per day).
//
// Three-state flow:
//   draft     — created via "Compute month" button. Loans + provisions
//               visible; no GL impact yet.
//   posted    — the JE for the movement is on the ledger.
//   cancelled — operator aborted before posting; reason captured.
//
// The page also shows IFRS 9 stage alongside SASRA classification on
// each line. Stage-driven ECL rates come from the per-tenant matrix
// (editable at /settings/loans-policy).

import { useEffect, useMemo, useState } from 'react';
import {
  cancelProvisioningV2Run,
  createProvisioningV2Run,
  getProvisioningV2Run,
  listProvisioningV2Runs,
  postProvisioningV2Run,
  type ProvisionRun,
  type ProvisionRunLine,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const CLASS_LABEL: Record<string, string> = {
  performing: 'Performing',
  watch: 'Watch',
  substandard: 'Substandard',
  doubtful: 'Doubtful',
  loss: 'Loss',
};
const CLASS_ORDER = ['loss', 'doubtful', 'substandard', 'watch', 'performing'];
const CLASS_COLOR: Record<string, string> = {
  performing: 'var(--pos)',
  watch: '#d4a017',
  substandard: '#c97a00',
  doubtful: '#c33',
  loss: 'var(--neg)',
};
const STATUS_COLOR: Record<string, string> = {
  pending: 'var(--muted)',
  draft: '#3b6ab8',
  computed: '#3b6ab8',
  posted: 'var(--pos)',
  failed: 'var(--neg)',
  superseded: 'var(--muted)',
  cancelled: 'var(--muted)',
};
const STAGE_LABEL: Record<number, string> = {
  1: 'Stage 1',
  2: 'Stage 2',
  3: 'Stage 3',
};

function firstOfMonth(d: Date): string {
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}-01`;
}

function monthOptions(): { value: string; label: string }[] {
  // Last 12 months ending with current month.
  const out: { value: string; label: string }[] = [];
  const now = new Date();
  for (let i = 0; i < 12; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    out.push({
      value: firstOfMonth(d),
      label: d.toLocaleDateString('en-GB', { month: 'long', year: 'numeric', timeZone: 'UTC' }),
    });
  }
  return out;
}

export default function ProvisioningPage() {
  useDocumentTitle('Loans · Provisioning');
  const { tenant, hasPermission } = useAuth();
  const canRun = hasPermission('loans:provisioning:run');
  const canPost = hasPermission('loans:provisioning:post');

  const months = useMemo(monthOptions, []);
  const [periodMonth, setPeriodMonth] = useState(months[0].value);
  const [notes, setNotes] = useState('');
  const [runs, setRuns] = useState<ProvisionRun[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ run: ProvisionRun; lines: ProvisionRunLine[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);

  async function loadList() {
    setErr(null);
    try { setRuns((await listProvisioningV2Runs()).items); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void loadList(); }, []);

  useEffect(() => {
    if (!selectedID) { setDetail(null); return; }
    void (async () => {
      try { setDetail(await getProvisioningV2Run(selectedID)); }
      catch (e) { setErr(asMsg(e)); }
    })();
  }, [selectedID]);

  async function create() {
    if (!canRun) return;
    setErr(null); setBusy(true);
    try {
      const run = await createProvisioningV2Run({
        period_month: periodMonth,
        notes: notes || undefined,
      });
      setNotes('');
      await loadList();
      setSelectedID(run.id);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function post(id: string) {
    if (!canPost) return;
    if (!confirm('Post the provision movement to the GL? This is the JE — review the per-loan detail first.')) return;
    setErr(null); setBusy(true);
    try {
      await postProvisioningV2Run(id);
      await loadList();
      setDetail(await getProvisioningV2Run(id));
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function cancel(id: string, reason: string) {
    if (!canRun) return;
    setErr(null); setBusy(true);
    try {
      await cancelProvisioningV2Run(id, reason);
      setCancelOpen(false);
      await loadList();
      setDetail(await getProvisioningV2Run(id));
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  // Bucket aggregation — same shape Phase 1 had, plus stage breakdown.
  const buckets = useMemo(() => {
    if (!detail) return null;
    const acc: Record<string, { count: number; outstanding: number; provision: number }> = {};
    for (const c of CLASS_ORDER) acc[c] = { count: 0, outstanding: 0, provision: 0 };
    for (const ln of detail.lines) {
      const a = acc[ln.classification];
      if (!a) continue;
      a.count += 1;
      a.outstanding += parseFloat(ln.outstanding);
      a.provision += parseFloat(ln.provision_amount);
    }
    return acc;
  }, [detail]);

  const stageCounts = useMemo(() => {
    if (!detail) return null;
    const counts: Record<number, number> = { 1: 0, 2: 0, 3: 0 };
    for (const ln of detail.lines) {
      const s = ln.classification_ifrs9_stage ?? 1;
      counts[s] = (counts[s] ?? 0) + 1;
    }
    return counts;
  }, [detail]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Loans · Provisioning</div>
          <h1>Loan loss provisioning</h1>
          <div className="page-sub">
            Monthly SASRA + IFRS 9 provisioning. Inputs come from the daily DPD classifier;
            rates come from your <a href="/settings/loans-policy">ECL rate matrix</a>.
            Each run is one period (month); compute → review → post the movement.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>New monthly run</h3></div>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny">Period</div>
            <select className="input" value={periodMonth} onChange={(e) => setPeriodMonth(e.target.value)}>
              {months.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
            </select>
          </label>
          <label style={{ flex: 1, minWidth: 240 }}>
            <div className="muted tiny">Notes (optional)</div>
            <input
              className="input"
              type="text"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="e.g. April month-end provisioning"
            />
          </label>
          <button
            className="btn btn-primary"
            disabled={busy || !canRun}
            title={canRun ? undefined : 'You need loans:provisioning:run'}
            onClick={() => void create()}
          >
            {busy ? 'Computing…' : 'Compute month'}
          </button>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(340px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
        <div className="card">
          <div className="card-hd">
            <h3>History</h3>
            <span className="card-sub">{runs.length} run{runs.length === 1 ? '' : 's'}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Period</th>
                  <th>Status</th>
                  <th className="num">Provision</th>
                  <th className="num">Movement</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => {
                  const periodLabel = r.period_month
                    ? new Date(r.period_month).toLocaleDateString('en-GB', { month: 'short', year: 'numeric', timeZone: 'UTC' })
                    : r.as_of_date.slice(0, 10);
                  return (
                    <tr
                      key={r.id}
                      onClick={() => setSelectedID(r.id)}
                      style={{ cursor: 'pointer', background: selectedID === r.id ? 'var(--surface-2)' : undefined }}
                    >
                      <td className="mono">{periodLabel}</td>
                      <td>
                        <span style={{ color: STATUS_COLOR[r.status], fontWeight: 600 }}>
                          {r.status}
                        </span>
                      </td>
                      <td className="num mono">{r.total_provision}</td>
                      <td className="num mono" style={{
                        color: parseFloat(r.movement) > 0 ? 'var(--neg)' : parseFloat(r.movement) < 0 ? 'var(--pos)' : 'var(--muted)',
                      }}>
                        {r.movement}
                      </td>
                    </tr>
                  );
                })}
                {runs.length === 0 && (
                  <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 18 }}>No runs yet.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-hd">
            <h3>
              {detail
                ? `Run · ${detail.run.period_month
                    ? new Date(detail.run.period_month).toLocaleDateString('en-GB', { month: 'long', year: 'numeric', timeZone: 'UTC' })
                    : detail.run.as_of_date.slice(0, 10)}`
                : 'Detail'}
            </h3>
            {detail && (
              <span className="card-sub" style={{ color: STATUS_COLOR[detail.run.status] }}>{detail.run.status}</span>
            )}
          </div>
          <div className="card-body" style={{ display: 'grid', gap: 12 }}>
            {!detail && <div className="muted">Pick a run from the list.</div>}

            {detail && (
              <>
                <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
                  <Stat label="Loans classified" value={String(detail.run.loans_classified)} />
                  <Stat label="Total outstanding" value={detail.run.total_outstanding} mono />
                  <Stat label="Total provision" value={detail.run.total_provision} mono />
                  <Stat label="Previous provision" value={detail.run.previous_provision} mono />
                  <Stat
                    label="Movement"
                    value={detail.run.movement}
                    mono
                    color={parseFloat(detail.run.movement) > 0 ? 'var(--neg)' : parseFloat(detail.run.movement) < 0 ? 'var(--pos)' : undefined}
                  />
                </div>

                {(detail.run.status === 'draft' || detail.run.status === 'computed') && (
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button
                      className="btn btn-primary"
                      disabled={busy || !canPost}
                      title={canPost ? undefined : 'You need loans:provisioning:post'}
                      onClick={() => void post(detail.run.id)}
                    >
                      {busy ? 'Posting…' : `Post movement to GL (${detail.run.movement})`}
                    </button>
                    <button
                      className="btn"
                      disabled={busy || !canRun}
                      onClick={() => setCancelOpen(true)}
                    >
                      Cancel draft
                    </button>
                  </div>
                )}
                {detail.run.status === 'posted' && (
                  <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
                    <div className="muted tiny">Journal ref: <span className="mono">{detail.run.journal_entry_ref}</span></div>
                    {detail.run.posted_at && (
                      <div className="muted tiny">Posted: {new Date(detail.run.posted_at).toLocaleString()}</div>
                    )}
                  </div>
                )}
                {detail.run.status === 'cancelled' && detail.run.cancel_reason && (
                  <div className="alert alert-warn">
                    Cancelled: {detail.run.cancel_reason}
                  </div>
                )}

                {stageCounts && (
                  <div className="card-body flush">
                    <div className="muted tiny" style={{ padding: 6 }}>IFRS 9 stage distribution</div>
                    <div style={{ display: 'flex', gap: 12, padding: 6 }}>
                      {[1, 2, 3].map((s) => (
                        <div key={s} style={{
                          padding: '6px 12px', borderRadius: 4,
                          background: s === 1 ? 'rgba(0,160,90,0.08)' : s === 2 ? 'rgba(212,160,23,0.10)' : 'rgba(195,51,51,0.10)',
                        }}>
                          <span style={{ fontWeight: 600 }}>{STAGE_LABEL[s]}</span>:{' '}
                          <span className="mono">{stageCounts[s] ?? 0}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {buckets && (
                  <div className="card-body flush">
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>SASRA bucket</th>
                          <th className="num">Loans</th>
                          <th className="num">Outstanding</th>
                          <th className="num">Provision</th>
                        </tr>
                      </thead>
                      <tbody>
                        {CLASS_ORDER.map((c) => {
                          const row = buckets[c];
                          if (!row || row.count === 0) return null;
                          return (
                            <tr key={c}>
                              <td><span style={{ color: CLASS_COLOR[c], fontWeight: 600 }}>{CLASS_LABEL[c]}</span></td>
                              <td className="num mono">{row.count}</td>
                              <td className="num mono">{row.outstanding.toFixed(2)}</td>
                              <td className="num mono">{row.provision.toFixed(2)}</td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                )}

                {detail.lines.length > 0 && (
                  <div className="card-body flush">
                    <div className="muted tiny" style={{ padding: 6 }}>
                      Per-loan detail · {detail.lines.length} loan{detail.lines.length === 1 ? '' : 's'}
                    </div>
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>Loan</th>
                          <th>SASRA</th>
                          <th>Stage</th>
                          <th className="num">DPD</th>
                          <th className="num">Outstanding</th>
                          <th className="num">Rate</th>
                          <th className="num">Provision</th>
                          <th className="num">Δ vs prev</th>
                        </tr>
                      </thead>
                      <tbody>
                        {detail.lines.slice(0, 50).map((ln) => (
                          <tr key={ln.id}>
                            <td className="mono">{ln.loan_no}</td>
                            <td><span style={{ color: CLASS_COLOR[ln.classification], fontWeight: 600 }}>{ln.classification}</span></td>
                            <td className="mono">{ln.classification_ifrs9_stage ? STAGE_LABEL[ln.classification_ifrs9_stage] : '—'}</td>
                            <td className="num mono">{ln.days_past_due}</td>
                            <td className="num mono">{ln.outstanding}</td>
                            <td className="num mono">{(parseFloat(ln.provision_rate) * 100).toFixed(2)}%</td>
                            <td className="num mono">{ln.provision_amount}</td>
                            <td className="num mono" style={{
                              color: ln.delta && parseFloat(ln.delta) > 0 ? 'var(--neg)' : ln.delta && parseFloat(ln.delta) < 0 ? 'var(--pos)' : 'var(--muted)',
                            }}>
                              {ln.delta ?? '—'}
                            </td>
                          </tr>
                        ))}
                        {detail.lines.length > 50 && (
                          <tr><td colSpan={8} className="muted tiny" style={{ textAlign: 'center', padding: 6 }}>
                            … and {detail.lines.length - 50} more
                          </td></tr>
                        )}
                      </tbody>
                    </table>
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      </div>

      {cancelOpen && detail && (
        <CancelDialog
          onClose={() => setCancelOpen(false)}
          onSubmit={(reason) => void cancel(detail.run.id, reason)}
          busy={busy}
        />
      )}
    </div>
  );
}

function CancelDialog({ onClose, onSubmit, busy }: { onClose: () => void; onSubmit: (reason: string) => void; busy: boolean }) {
  const [reason, setReason] = useState('');
  return (
    <div role="dialog" aria-modal="true" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, width: '90%', maxWidth: 480, padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>Cancel draft run</h3>
        <p className="muted tiny">
          A cancelled run is kept for audit but frees the period slot so you can compute a fresh run.
          Cancellation is permanent.
        </p>
        <label>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Reason</div>
          <textarea
            className="input"
            rows={3}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. recomputing after correcting a misclassified restructured loan"
          />
        </label>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Keep draft</button>
          <button className="btn btn-danger" disabled={busy || !reason.trim()} onClick={() => onSubmit(reason.trim())}>
            {busy ? 'Cancelling…' : 'Cancel run'}
          </button>
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value, mono, color }: { label: string; value: string; mono?: boolean; color?: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{ fontSize: 18, fontWeight: 700, fontFamily: mono ? 'var(--font-mono)' : undefined, color }}>
        {value}
      </div>
    </div>
  );
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
