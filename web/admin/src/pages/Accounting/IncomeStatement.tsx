// Income Statement (Statement of Comprehensive Income) — income and
// expenses for a period, plus the net surplus that flows to retained
// earnings.

import { useEffect, useState } from 'react';
import { downloadReport, incomeStatement, type IncomeStatementRow } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function IncomeStatementPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const monthStart = today.slice(0, 8) + '01';
  const [from, setFrom] = useState(monthStart);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<Awaited<ReturnType<typeof incomeStatement>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await incomeStatement(from, to)); }
    catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Income Statement</h1>
          <div className="page-sub">
            Income and expenditure for the selected window. Net surplus flows to retained earnings
            at year-end close.
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
            <button className="btn" disabled={!data} onClick={() => void downloadReport('income-statement', { from, to })}>
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
            title="Income"
            rows={data.items.filter((r) => r.class === 'income')}
            total={data.total_income}
            totalLabel="Total Income"
          />
          <Section
            title="Expenses"
            rows={data.items.filter((r) => r.class === 'expense')}
            total={data.total_expense}
            totalLabel="Total Expenses"
          />
          <div className="card" style={{ marginTop: 14 }}>
            <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
              <div>
                <div className="muted tiny">Total income</div>
                <div style={{ fontSize: 20, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--pos)' }}>{data.total_income}</div>
              </div>
              <div>
                <div className="muted tiny">Total expenses</div>
                <div style={{ fontSize: 20, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--neg)' }}>{data.total_expense}</div>
              </div>
              <div style={{ marginLeft: 'auto' }}>
                <div className="muted tiny">Net surplus / (deficit)</div>
                <div style={{
                  fontSize: 24, fontWeight: 700, fontFamily: 'var(--font-mono)',
                  color: parseFloat(data.net_surplus) >= 0 ? 'var(--pos)' : 'var(--neg)',
                }}>
                  {data.net_surplus}
                </div>
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function Section({ title, rows, total, totalLabel }: { title: string; rows: IncomeStatementRow[]; total: string; totalLabel: string }) {
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
