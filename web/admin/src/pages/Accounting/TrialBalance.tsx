// Trial Balance report — every active account with movement in the
// selected window, grouped by class, with foot-totals showing balance.

import { useEffect, useState } from 'react';
import { downloadReport, trialBalance, type TrialBalanceRow } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const CLASS_LABEL: Record<string, string> = {
  asset: 'Assets', liability: 'Liabilities', equity: 'Equity',
  income: 'Income', expense: 'Expenses',
};
const CLASS_ORDER = ['asset', 'liability', 'equity', 'income', 'expense'];

export default function TrialBalancePage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const monthStart = today.slice(0, 8) + '01';
  const [from, setFrom] = useState(monthStart);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<{ items: TrialBalanceRow[]; total_debits: string; total_credits: string; balanced: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try {
      const r = await trialBalance(from, to);
      setData(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'failed to load');
    } finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Trial Balance</h1>
          <div className="page-sub">
            Closing balances per active account for the selected period. Total debits and total
            credits must agree — any imbalance points to a posting bug.
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
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
            <button className="btn" disabled={!data} onClick={() => void downloadReport('trial-balance', { from, to })}>
              Export XLSX
            </button>
            <button className="btn" onClick={() => window.print()}>Print</button>
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && (
        <>
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
              <div>
                <div className="muted tiny">Total Debits</div>
                <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)' }}>
                  {data.total_debits}
                </div>
              </div>
              <div>
                <div className="muted tiny">Total Credits</div>
                <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)' }}>
                  {data.total_credits}
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

          {CLASS_ORDER.map((cls) => {
            const rows = data.items.filter((r) => r.class === cls);
            if (rows.length === 0) return null;
            return (
              <div key={cls} className="card" style={{ marginTop: 12 }}>
                <div className="card-hd">
                  <h3>{CLASS_LABEL[cls]}</h3>
                  <span className="card-sub">{rows.length} accounts</span>
                </div>
                <div className="card-body flush">
                  <table className="tbl">
                    <thead>
                      <tr>
                        <th>Code</th>
                        <th>Account</th>
                        <th className="num">Period debits</th>
                        <th className="num">Period credits</th>
                        <th className="num">Closing debit</th>
                        <th className="num">Closing credit</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r) => (
                        <tr key={r.account_id}>
                          <td className="mono">{r.account_code}</td>
                          <td>{r.account_name}</td>
                          <td className="num mono">{nonZero(r.period_debits)}</td>
                          <td className="num mono">{nonZero(r.period_credits)}</td>
                          <td className="num mono"><strong>{nonZero(r.closing_debit)}</strong></td>
                          <td className="num mono"><strong>{nonZero(r.closing_credit)}</strong></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            );
          })}
        </>
      )}
    </div>
  );
}

function nonZero(v: string): string {
  return v === '0' || v === '0.00' ? '' : v;
}
