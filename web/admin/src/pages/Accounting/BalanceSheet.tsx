// Balance Sheet (Statement of Financial Position) — assets,
// liabilities and equity as at the chosen date. Built directly from
// posted journal entries: every cash deposit, loan disbursement,
// share purchase etc. shows up here automatically once the auto-post
// integration is live.

import { useEffect, useState } from 'react';
import { balanceSheet, downloadReport, type BalanceSheetRow } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function BalanceSheetPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [asOf, setAsOf] = useState(today);
  const [data, setData] = useState<Awaited<ReturnType<typeof balanceSheet>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await balanceSheet(asOf)); }
    catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Balance Sheet</h1>
          <div className="page-sub">
            Statement of Financial Position. Assets = Liabilities + Equity.
            Any imbalance points to a posting bug.
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
            {busy ? 'Loading…' : 'Run report'}
          </button>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
            <button className="btn" disabled={!data} onClick={() => void downloadReport('balance-sheet', { as_of: asOf })}>
              Export XLSX
            </button>
            <button className="btn" onClick={() => window.print()}>Print</button>
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && (
        <>
          <Section
            title="Assets"
            rows={data.items.filter((r) => r.class === 'asset')}
            total={data.total_assets}
            totalLabel="Total Assets"
          />
          <Section
            title="Liabilities"
            rows={data.items.filter((r) => r.class === 'liability')}
            total={data.total_liabilities}
            totalLabel="Total Liabilities"
          />
          <Section
            title="Equity"
            rows={data.items.filter((r) => r.class === 'equity')}
            total={data.total_equity}
            totalLabel="Total Equity"
          />
          <div className="card" style={{ marginTop: 14 }}>
            <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
              <div>
                <div className="muted tiny">Total Assets</div>
                <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)' }}>{data.total_assets}</div>
              </div>
              <div>
                <div className="muted tiny">Liabilities + Equity</div>
                <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)' }}>
                  {(parseFloat(data.total_liabilities) + parseFloat(data.total_equity)).toFixed(2)}
                </div>
              </div>
              <div style={{ marginLeft: 'auto' }}>
                <div className="muted tiny">Balanced?</div>
                <div style={{
                  fontWeight: 700, fontSize: 18,
                  color: data.balanced ? 'var(--pos)' : 'var(--neg)',
                }}>
                  {data.balanced ? '✓ Yes' : '✗ NO — INVESTIGATE'}
                </div>
              </div>
            </div>
          </div>
        </>
      )}

      {data && data.items.length === 0 && (
        <div className="empty" style={{ marginTop: 14 }}>
          No posted activity yet for this date. Make a deposit, share purchase, or loan
          disbursement and refresh to see entries flow through the GL.
        </div>
      )}
    </div>
  );
}

function Section({ title, rows, total, totalLabel }: { title: string; rows: BalanceSheetRow[]; total: string; totalLabel: string }) {
  if (rows.length === 0) return null;
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="card-hd">
        <h3>{title}</h3>
        <span className="card-sub">{rows.length} account{rows.length === 1 ? '' : 's'}</span>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th style={{ width: 100 }}>Code</th>
              <th>Account</th>
              <th className="num">Amount</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.account_id ?? r.account_code}>
                <td className="mono">{r.account_code}</td>
                <td>{r.account_name}</td>
                <td className="num mono">{r.amount}</td>
              </tr>
            ))}
            <tr style={{ background: 'var(--surface-2)' }}>
              <td></td>
              <td><strong>{totalLabel}</strong></td>
              <td className="num mono"><strong>{total}</strong></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  );
}
