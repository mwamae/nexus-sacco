// Statement of Changes in Equity — for each equity account: opening
// balance carried in, increases + decreases over the period, closing
// balance. The Net surplus row at the foot shows the P&L impact that
// has not yet been closed into retained earnings (zero if the year
// has been formally closed).

import { useEffect, useState } from 'react';
import { changesInEquity, type ChangesInEquityRow } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function ChangesInEquityPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const yearStart = today.slice(0, 4) + '-01-01';
  const [from, setFrom] = useState(yearStart);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<Awaited<ReturnType<typeof changesInEquity>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await changesInEquity(from, to)); }
    catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Statement of Changes in Equity</h1>
          <div className="page-sub">
            Per-account opening, period activity, and closing balance for every equity account.
            The unclosed net surplus is surfaced separately to reconcile to retained earnings.
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">From</div>
            <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">To</div>
            <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Run report'}
          </button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd">
            <h3>Equity movement</h3>
            <span className="card-sub">{data.items.length} account{data.items.length === 1 ? '' : 's'}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 90 }}>Code</th>
                  <th>Account</th>
                  <th className="num">Opening</th>
                  <th className="num">Increase</th>
                  <th className="num">Decrease</th>
                  <th className="num">Closing</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((r) => (
                  <tr key={r.account_id ?? r.account_code}>
                    <td className="mono">{r.account_code}</td>
                    <td>{r.account_name}</td>
                    <td className="num mono">{r.opening}</td>
                    <td className="num mono" style={{ color: parseFloat(r.increase) > 0 ? 'var(--pos)' : undefined }}>
                      {nonZero(r.increase)}
                    </td>
                    <td className="num mono" style={{ color: parseFloat(r.decrease) > 0 ? 'var(--neg)' : undefined }}>
                      {nonZero(r.decrease)}
                    </td>
                    <td className="num mono"><strong>{r.closing}</strong></td>
                  </tr>
                ))}
                <tr style={{ background: 'var(--surface-2)', fontWeight: 600 }}>
                  <td></td>
                  <td>Total equity (excluding unclosed P&L)</td>
                  <td className="num mono">{data.total_opening}</td>
                  <td className="num mono">{data.total_increase}</td>
                  <td className="num mono">{data.total_decrease}</td>
                  <td className="num mono">{data.total_closing}</td>
                </tr>
                {parseFloat(data.net_surplus) !== 0 && (
                  <tr style={{ background: 'var(--surface-2)' }}>
                    <td></td>
                    <td><em>Net surplus / (deficit) for period (unclosed)</em></td>
                    <td></td>
                    <td></td>
                    <td></td>
                    <td className="num mono" style={{
                      color: parseFloat(data.net_surplus) >= 0 ? 'var(--pos)' : 'var(--neg)',
                      fontWeight: 700,
                    }}>
                      {data.net_surplus}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

function nonZero(v: string): string {
  return v === '0' || v === '0.00' ? '' : v;
}
