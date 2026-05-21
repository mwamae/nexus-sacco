// Budgets — list + create. Click a row to edit the budget.

import { useEffect, useState } from 'react';
import { createBudget, listBudgets, type Budget } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const STATUS_COLOR: Record<string, string> = {
  draft: 'var(--muted)',
  submitted: '#3b6ab8',
  approved: 'var(--pos)',
  archived: '#888',
};

export default function BudgetsPage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<Budget[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const currentYear = new Date().getFullYear();
  const [show, setShow] = useState(false);
  const [name, setName] = useState('');
  const [year, setYear] = useState(currentYear + 1);
  const [notes, setNotes] = useState('');

  async function load() {
    setErr(null);
    try { setItems((await listBudgets()).items); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); }, []);

  async function doCreate() {
    setBusy(true); setErr(null);
    try {
      const b = await createBudget({ name, fiscal_year: year, notes: notes || undefined });
      setName(''); setNotes(''); setShow(false);
      window.location.href = `/budgets/${b.id}`;
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Budgeting</div>
          <h1>Budgets</h1>
          <div className="page-sub">
            Plan revenue and expense targets per fiscal year. Approved budgets become the baseline
            for variance reporting against actuals.
          </div>
        </div>
        <button className="btn btn-primary" onClick={() => setShow(!show)}>
          {show ? 'Cancel' : 'New budget'}
        </button>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {show && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd"><h3>New budget</h3></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
            <label><div className="muted tiny">Name</div><input value={name} onChange={(e) => setName(e.target.value)} placeholder="FY 2027 budget" /></label>
            <label><div className="muted tiny">Fiscal year</div><input type="number" value={year} onChange={(e) => setYear(parseInt(e.target.value || String(currentYear), 10))} /></label>
            <label style={{ gridColumn: '1 / -1' }}><div className="muted tiny">Notes</div><input value={notes} onChange={(e) => setNotes(e.target.value)} /></label>
            <div style={{ gridColumn: '1 / -1', textAlign: 'right' }}>
              <button className="btn btn-primary" disabled={busy || !name || !year} onClick={() => void doCreate()}>
                {busy ? 'Creating…' : 'Create'}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>All budgets</h3><span className="card-sub">{items.length}</span></div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Fiscal year</th>
                <th>Name</th>
                <th>Status</th>
                <th className="num">Income budget</th>
                <th className="num">Expense budget</th>
                <th className="num">Net surplus</th>
              </tr>
            </thead>
            <tbody>
              {items.map((b) => (
                <tr key={b.id} style={{ cursor: 'pointer' }} onClick={() => { window.location.href = `/budgets/${b.id}`; }}>
                  <td className="mono">{b.fiscal_year}</td>
                  <td>{b.name}</td>
                  <td><span style={{ color: STATUS_COLOR[b.status], fontWeight: 600 }}>{b.status}</span></td>
                  <td className="num mono">{b.total_income_budget}</td>
                  <td className="num mono">{b.total_expense_budget}</td>
                  <td className="num mono"><strong>{b.net_surplus_budget}</strong></td>
                </tr>
              ))}
              {items.length === 0 && (
                <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 18 }}>No budgets yet.</td></tr>
              )}
            </tbody>
          </table>
        </div>
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
