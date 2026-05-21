// Loan Loss Provisioning — runs that classify every active loan,
// compute the SASRA-required provision, and post the *movement*
// (this run's total provision − previous posted run's total) to the
// general ledger.
//
// Two-step flow: create a "computed" snapshot first; review the bucket
// distribution + per-loan detail; then post the GL movement.

import { useEffect, useMemo, useState } from 'react';
import {
  createProvisionRun,
  getProvisionRun,
  listProvisionRuns,
  postProvisionRun,
  supersedeProvisionRun,
  type ProvisionRun,
  type ProvisionRunLine,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const CLASS_LABEL: Record<string, string> = {
  performing: 'Performing',
  watch: 'Watch (1–30 dpd)',
  substandard: 'Substandard (31–90 dpd)',
  doubtful: 'Doubtful (91–180 dpd)',
  loss: 'Loss (>180 dpd)',
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
  computed: '#3b6ab8',
  posted: 'var(--pos)',
  failed: 'var(--neg)',
  superseded: 'var(--muted)',
};

export default function ProvisioningPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [asOfDate, setAsOfDate] = useState(today);
  const [notes, setNotes] = useState('');
  const [runs, setRuns] = useState<ProvisionRun[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ run: ProvisionRun; lines: ProvisionRunLine[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function loadList() {
    setErr(null);
    try { setRuns((await listProvisionRuns()).items); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void loadList(); }, []);

  useEffect(() => {
    if (!selectedID) { setDetail(null); return; }
    void (async () => {
      try { setDetail(await getProvisionRun(selectedID)); }
      catch (e) { setErr(asMsg(e)); }
    })();
  }, [selectedID]);

  async function create() {
    setErr(null); setBusy(true);
    try {
      const run = await createProvisionRun({
        as_of_date: asOfDate,
        notes: notes || undefined,
      });
      setNotes('');
      await loadList();
      setSelectedID(run.id);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function post(id: string) {
    setErr(null); setBusy(true);
    try {
      await postProvisionRun(id);
      await loadList();
      setDetail(await getProvisionRun(id));
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function supersede(id: string) {
    if (!confirm('Mark this posted run as superseded? You must post a manual reversal of its GL entry first.')) return;
    setErr(null); setBusy(true);
    try {
      await supersedeProvisionRun(id);
      await loadList();
      if (selectedID === id) setDetail(await getProvisionRun(id));
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

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

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Loans · Risk</div>
          <h1>Loan loss provisioning</h1>
          <div className="page-sub">
            Classify every active loan against the SASRA matrix and post the provision movement to the GL.
            Each run snapshots the portfolio for an as-of date and is posted in two steps: compute, then post.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>New run</h3></div>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny">As of date</div>
            <input type="date" value={asOfDate} onChange={(e) => setAsOfDate(e.target.value)} />
          </label>
          <label style={{ flex: 1, minWidth: 240 }}>
            <div className="muted tiny">Notes (optional)</div>
            <input type="text" value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="e.g. Month-end May 2026" />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void create()}>
            {busy ? 'Computing…' : 'Compute snapshot'}
          </button>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(320px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
        <div className="card">
          <div className="card-hd">
            <h3>History</h3>
            <span className="card-sub">{runs.length} run{runs.length === 1 ? '' : 's'}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>As-of</th>
                  <th>Status</th>
                  <th className="num">Provision</th>
                  <th className="num">Movement</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr
                    key={r.id}
                    onClick={() => setSelectedID(r.id)}
                    style={{ cursor: 'pointer', background: selectedID === r.id ? 'var(--surface-2)' : undefined }}
                  >
                    <td className="mono">{r.as_of_date.slice(0, 10)}</td>
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
                ))}
                {runs.length === 0 && (
                  <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 18 }}>No runs yet.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-hd">
            <h3>{detail ? `Run · ${detail.run.as_of_date.slice(0, 10)}` : 'Detail'}</h3>
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

                {detail.run.status === 'computed' && (
                  <button className="btn btn-primary" disabled={busy} onClick={() => void post(detail.run.id)}>
                    {busy ? 'Posting…' : `Post movement to GL (${detail.run.movement})`}
                  </button>
                )}
                {detail.run.status === 'posted' && (
                  <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                    <div className="muted tiny">Journal ref: <span className="mono">{detail.run.journal_entry_ref}</span></div>
                    <button className="btn" style={{ marginLeft: 'auto' }} disabled={busy} onClick={() => void supersede(detail.run.id)}>
                      Supersede
                    </button>
                  </div>
                )}

                {buckets && (
                  <div className="card-body flush">
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>Bucket</th>
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
                    <div className="muted tiny" style={{ padding: 6 }}>Per-loan detail · {detail.lines.length} loan{detail.lines.length === 1 ? '' : 's'}</div>
                    <table className="tbl">
                      <thead>
                        <tr>
                          <th>Loan</th>
                          <th>Bucket</th>
                          <th className="num">DPD</th>
                          <th className="num">Outstanding</th>
                          <th className="num">Rate</th>
                          <th className="num">Provision</th>
                        </tr>
                      </thead>
                      <tbody>
                        {detail.lines.slice(0, 50).map((ln) => (
                          <tr key={ln.id}>
                            <td className="mono">{ln.loan_no}</td>
                            <td><span style={{ color: CLASS_COLOR[ln.classification], fontWeight: 600 }}>{ln.classification}</span></td>
                            <td className="num mono">{ln.days_past_due}</td>
                            <td className="num mono">{ln.outstanding}</td>
                            <td className="num mono">{(parseFloat(ln.provision_rate) * 100).toFixed(2)}%</td>
                            <td className="num mono">{ln.provision_amount}</td>
                          </tr>
                        ))}
                        {detail.lines.length > 50 && (
                          <tr><td colSpan={6} className="muted tiny" style={{ textAlign: 'center', padding: 6 }}>
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
