// Subledger Reconciliation report.
//
// One traffic-light per CoA account: does the GL balance match the
// subledger it should mirror? Drift here means a post failed silently
// (R2 outbox outage) or a code path bypassed the GL (R3 interest run
// pre-fix). The big banner at the top is the at-a-glance signal for
// the finance team; the per-account rows are the drill-in.

import { useEffect, useState } from 'react';
import { reconciliation, type SubledgerReconciliation, type ReconciliationRow } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function ReconciliationPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [asOf, setAsOf] = useState(today);
  const [data, setData] = useState<SubledgerReconciliation | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await reconciliation(asOf)); }
    catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Subledger Reconciliation</h1>
          <div className="page-sub">
            Does each account's GL balance match the subledger source of truth?
            Drift means a post failed silently or a code path bypassed the GL.
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">As of</div>
            <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Run reconciliation'}
          </button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && (
        <>
          <StatusBanner status={data.overall_status} rowCount={data.rows.length} />

          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>By account</h3></div>
            <div className="card-body flush">
              {data.rows.length === 0 ? (
                <div className="empty" style={{ padding: 20 }}>
                  No accounts with both GL + subledger activity to reconcile yet.
                </div>
              ) : (
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>Status</th>
                      <th>Code</th>
                      <th>Account</th>
                      <th className="num">GL balance</th>
                      <th className="num">Subledger</th>
                      <th className="num">Delta</th>
                      <th className="num">Δ % of GL</th>
                      <th>Last JE</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {data.rows.map((row) => <Row key={row.code} row={row} />)}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function StatusBanner({ status, rowCount }: { status: 'ok' | 'warn' | 'error'; rowCount: number }) {
  const palette = {
    ok:    { bg: 'var(--pos-bg, #e9f7ee)', fg: 'var(--pos, #1c7c4f)', label: '✓ All accounts reconciled' },
    warn:  { bg: 'var(--warn-bg, #fff5e0)', fg: 'var(--warn, #a07000)', label: '⚠ Drift within tolerance' },
    error: { bg: 'var(--neg-bg, #ffe5e5)', fg: 'var(--neg, #b00020)', label: '✗ Subledger drift detected — investigate' },
  }[status];
  return (
    <div className="card" style={{ marginTop: 12, background: palette.bg, color: palette.fg }}>
      <div className="card-body" style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
        <div style={{ fontSize: 28, fontWeight: 700 }}>{palette.label}</div>
        <div className="tiny" style={{ marginLeft: 'auto' }}>
          {rowCount} account{rowCount === 1 ? '' : 's'} checked
        </div>
      </div>
    </div>
  );
}

function Row({ row }: { row: ReconciliationRow }) {
  const dot = {
    ok:    { color: 'var(--pos, #1c7c4f)', label: '●' },
    warn:  { color: 'var(--warn, #a07000)', label: '●' },
    error: { color: 'var(--neg, #b00020)', label: '●' },
  }[row.status];
  return (
    <tr>
      <td>
        <span style={{ color: dot.color, fontSize: 20 }}>{dot.label}</span>{' '}
        <span className="tiny" style={{ textTransform: 'uppercase', color: dot.color }}>{row.status}</span>
      </td>
      <td className="mono">{row.code}</td>
      <td>{row.name}</td>
      <td className="num mono">{row.gl_balance}</td>
      <td className="num mono">{row.subledger_balance}</td>
      <td className="num mono"><strong>{row.delta}</strong></td>
      <td className="num mono">{row.delta_pct_of_gl}%</td>
      <td>
        {row.last_je_no
          ? <a className="tbl-link mono" href={`/accounting/journal-entries/${row.last_je_id ?? ''}`}>{row.last_je_no}</a>
          : <span className="muted">—</span>}
      </td>
      <td>
        {row.status !== 'ok' && (
          <a className="btn btn-sm" href={`/accounting/journal-entries?account=${row.code}`}>
            Investigate
          </a>
        )}
      </td>
    </tr>
  );
}
