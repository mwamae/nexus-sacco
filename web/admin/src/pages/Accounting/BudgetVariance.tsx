// Budget variance report — actual P&L vs budget for a window.
// Favourable for income = actual > budget; for expense = actual < budget.

import { useEffect, useState } from 'react';
import { budgetVariance, getBudget, type Budget, type VarianceReport } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function BudgetVariancePage() {
  const { tenant } = useAuth();
  const parts = window.location.pathname.split('/');
  const id = parts[parts.length - 2]; // /budgets/{id}/variance
  const today = new Date().toISOString().slice(0, 10);
  const [budget, setBudget] = useState<Budget | null>(null);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState(today);
  const [data, setData] = useState<VarianceReport | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const { budget: b } = await getBudget(id);
        setBudget(b);
        setFrom(b.period_start.slice(0, 10));
      } catch (e) { setErr(asMsg(e)); }
    })();
  }, [id]);

  async function load() {
    if (!from || !to) return;
    setBusy(true); setErr(null);
    try { setData(await budgetVariance(id, from, to)); }
    catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => {
    if (from && to) void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [from]);

  if (!budget) return <div className="page"><div className="muted">Loading…</div></div>;

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Variance</div>
          <h1>{budget.name} — variance</h1>
          <div className="page-sub">
            Actual P&L vs budget for the selected window. Favourable: income above OR expense below.
          </div>
        </div>
        <a className="btn" href={`/budgets/${id}`}>← Budget</a>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <label><div className="muted tiny">From</div><input type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></label>
          <label><div className="muted tiny">To</div><input type="date" value={to} onChange={(e) => setTo(e.target.value)} /></label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Run'}
          </button>
        </div>
      </div>

      {data && (
        <>
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 18 }}>
              <Group label="Income"
                budget={data.total_income_budget} actual={data.total_income_actual}
                favourable={parseFloat(data.total_income_actual) >= parseFloat(data.total_income_budget)} />
              <Group label="Expense"
                budget={data.total_expense_budget} actual={data.total_expense_actual}
                favourable={parseFloat(data.total_expense_actual) <= parseFloat(data.total_expense_budget)} />
              <Group label="Net surplus"
                budget={data.net_surplus_budget} actual={data.net_surplus_actual}
                favourable={parseFloat(data.net_surplus_variance) >= 0}
                varianceOverride={data.net_surplus_variance}
              />
            </div>
          </div>

          <Section title="Income" cls="income" rows={data.rows.filter((r) => r.account_class === 'income')} />
          <Section title="Expense" cls="expense" rows={data.rows.filter((r) => r.account_class === 'expense')} />
        </>
      )}
    </div>
  );
}

function Group({ label, budget, actual, favourable, varianceOverride }: {
  label: string; budget: string; actual: string; favourable: boolean; varianceOverride?: string;
}) {
  const variance = varianceOverride ?? (parseFloat(actual) - parseFloat(budget)).toFixed(2);
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div className="mono" style={{ fontSize: 14, marginTop: 2 }}>Budget: <strong>{budget}</strong></div>
      <div className="mono" style={{ fontSize: 14 }}>Actual: <strong>{actual}</strong></div>
      <div className="mono" style={{ fontSize: 16, fontWeight: 700, color: favourable ? 'var(--pos)' : 'var(--neg)' }}>
        Δ {variance} {favourable ? ' ✓' : ' ✗'}
      </div>
    </div>
  );
}

function Section({ title, cls, rows }: { title: string; cls: 'income' | 'expense'; rows: import('../../api/client').VarianceRow[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="card-hd"><h3>{title}</h3><span className="card-sub">{rows.length} accounts</span></div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th style={{ width: 90 }}>Code</th>
              <th>Account</th>
              <th className="num">Budget</th>
              <th className="num">Actual</th>
              <th className="num">Variance</th>
              <th className="num">%</th>
              <th>Favourable?</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.account_id}>
                <td className="mono">{r.account_code}</td>
                <td>{r.account_name}</td>
                <td className="num mono">{r.budget}</td>
                <td className="num mono">{r.actual}</td>
                <td className="num mono" style={{ color: r.favourable ? 'var(--pos)' : 'var(--neg)' }}>
                  {r.variance}
                </td>
                <td className="num mono">{r.variance_pct}%</td>
                <td><span style={{ color: r.favourable ? 'var(--pos)' : 'var(--neg)', fontWeight: 600 }}>
                  {r.favourable ? '✓' : '✗'}
                </span></td>
              </tr>
            ))}
          </tbody>
        </table>
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
