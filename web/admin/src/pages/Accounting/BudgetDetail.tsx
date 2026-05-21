// Budget editor — per-account, per-month grid. Pre-populates with all
// active income + expense CoA accounts; the user types monthly amounts
// (or enters an annual amount and clicks "spread" to distribute
// evenly across 12 months). Bulk-saved via /lines/bulk-upsert.

import { useEffect, useMemo, useState } from 'react';
import {
  approveBudget,
  archiveBudget,
  bulkUpsertBudgetLines,
  getBudget,
  listCoA,
  submitBudget,
  type Budget,
  type BudgetLine,
  type CoAAccount,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const STATUS_COLOR: Record<string, string> = {
  draft: 'var(--muted)',
  submitted: '#3b6ab8',
  approved: 'var(--pos)',
  archived: '#888',
};

type RowAmounts = Record<number, string>; // period_month → amount

export default function BudgetDetailPage() {
  const { tenant } = useAuth();
  const id = window.location.pathname.split('/').pop() ?? '';
  const [budget, setBudget] = useState<Budget | null>(null);
  const [accounts, setAccounts] = useState<CoAAccount[]>([]);
  // grid[account_code] → { 1: '...', 2: '...', ... }
  const [grid, setGrid] = useState<Record<string, RowAmounts>>({});
  const [annual, setAnnual] = useState<Record<string, string>>({});
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState<'income' | 'expense' | 'all'>('all');

  async function load() {
    setErr(null);
    try {
      const [bd, coa] = await Promise.all([getBudget(id), listCoA(true)]);
      setBudget(bd.budget);
      const accts = coa.items.filter((a) => a.class === 'income' || a.class === 'expense');
      setAccounts(accts);
      hydrateGrid(bd.lines, accts);
    } catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line */ }, []);

  function hydrateGrid(lines: BudgetLine[], accts: CoAAccount[]) {
    const g: Record<string, RowAmounts> = {};
    for (const a of accts) g[a.code] = {};
    for (const l of lines) {
      if (!g[l.account_code]) g[l.account_code] = {};
      g[l.account_code][l.period_month] = l.amount;
    }
    setGrid(g);
    const an: Record<string, string> = {};
    for (const code in g) {
      let sum = 0;
      for (let m = 1; m <= 12; m++) sum += parseFloat(g[code][m] || '0');
      an[code] = sum ? sum.toFixed(2) : '';
    }
    setAnnual(an);
  }

  function setCell(code: string, month: number, v: string) {
    setGrid((prev) => ({ ...prev, [code]: { ...prev[code], [month]: v } }));
  }

  function spreadAnnual(code: string) {
    const v = parseFloat(annual[code] || '0');
    if (!v) return;
    const monthly = (v / 12).toFixed(2);
    setGrid((prev) => {
      const row: RowAmounts = {};
      for (let m = 1; m <= 12; m++) row[m] = monthly;
      return { ...prev, [code]: row };
    });
  }

  async function doSave() {
    if (!budget) return;
    if (budget.status !== 'draft') {
      setErr('Only draft budgets are editable.');
      return;
    }
    setBusy(true); setErr(null); setInfo(null);
    try {
      const lines: { account_code: string; period_month: number; amount: string }[] = [];
      for (const code in grid) {
        for (let m = 1; m <= 12; m++) {
          const v = grid[code][m];
          if (v && v !== '' && v !== '0') {
            lines.push({ account_code: code, period_month: m, amount: parseFloat(v).toFixed(2) });
          }
        }
      }
      if (lines.length === 0) {
        setErr('Add at least one non-zero line before saving.');
        return;
      }
      const r = await bulkUpsertBudgetLines(id, lines);
      setBudget(r.budget);
      hydrateGrid(r.lines, accounts);
      setInfo(`Saved. Income ${r.budget.total_income_budget}, expense ${r.budget.total_expense_budget}, net surplus ${r.budget.net_surplus_budget}.`);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doSubmit() { await doTransition('submit'); }
  async function doApprove() { await doTransition('approve'); }
  async function doArchive() { await doTransition('archive'); }

  async function doTransition(action: 'submit' | 'approve' | 'archive') {
    setBusy(true); setErr(null); setInfo(null);
    try {
      const b = action === 'submit' ? await submitBudget(id)
              : action === 'approve' ? await approveBudget(id)
              : await archiveBudget(id);
      setBudget(b);
      setInfo(`Budget ${action}d.`);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const filtered = useMemo(() => accounts.filter((a) => filter === 'all' || a.class === filter), [accounts, filter]);

  const editable = budget?.status === 'draft';

  if (!budget) {
    return <div className="page"><div className="muted">Loading…</div></div>;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Budget</div>
          <h1>{budget.name}</h1>
          <div className="page-sub">
            FY {budget.fiscal_year} · {budget.period_start.slice(0, 10)} → {budget.period_end.slice(0, 10)} ·
            <span style={{ color: STATUS_COLOR[budget.status], fontWeight: 600, marginLeft: 6 }}>{budget.status}</span>
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <a className="btn" href="/budgets">← All budgets</a>
          <a className="btn" href={`/budgets/${id}/variance`}>Variance</a>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {info && <div className="alert" style={{ marginTop: 12, background: 'var(--pos-bg, #e6f5ea)', borderColor: 'var(--pos)' }}>{info}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <Stat label="Total income budget" value={budget.total_income_budget} color="var(--pos)" />
          <Stat label="Total expense budget" value={budget.total_expense_budget} color="var(--neg)" />
          <Stat label="Projected net surplus" value={budget.net_surplus_budget} bold />
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            {editable && (
              <button className="btn btn-primary" disabled={busy} onClick={() => void doSave()}>Save lines</button>
            )}
            {budget.status === 'draft' && (
              <button className="btn" disabled={busy} onClick={() => void doSubmit()}>Submit</button>
            )}
            {budget.status === 'submitted' && (
              <button className="btn btn-primary" disabled={busy} onClick={() => void doApprove()}>Approve</button>
            )}
            {budget.status !== 'archived' && (
              <button className="btn" disabled={busy} onClick={() => void doArchive()}>Archive</button>
            )}
          </div>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Budget lines</h3>
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="btn tiny" data-active={filter === 'all' || undefined} onClick={() => setFilter('all')}>All</button>
            <button className="btn tiny" data-active={filter === 'income' || undefined} onClick={() => setFilter('income')}>Income</button>
            <button className="btn tiny" data-active={filter === 'expense' || undefined} onClick={() => setFilter('expense')}>Expense</button>
          </div>
        </div>
        <div className="card-body" style={{ overflowX: 'auto' }}>
          <table className="tbl" style={{ minWidth: 1200 }}>
            <thead>
              <tr>
                <th style={{ minWidth: 70 }}>Code</th>
                <th style={{ minWidth: 200 }}>Account</th>
                <th>Class</th>
                {MONTHS.map((m, i) => (
                  <th key={i} className="num" style={{ minWidth: 70 }}>{m}</th>
                ))}
                <th className="num" style={{ minWidth: 100 }}>Annual</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((a) => {
                const row = grid[a.code] || {};
                const sum = Array.from({ length: 12 }, (_, i) => parseFloat(row[i + 1] || '0')).reduce((p, c) => p + c, 0);
                return (
                  <tr key={a.code}>
                    <td className="mono">{a.code}</td>
                    <td>{a.name}</td>
                    <td>
                      <span style={{ fontWeight: 600, color: a.class === 'income' ? 'var(--pos)' : 'var(--neg)' }}>{a.class}</span>
                    </td>
                    {MONTHS.map((_, i) => {
                      const m = i + 1;
                      return (
                        <td key={i}>
                          <input
                            value={row[m] ?? ''}
                            onChange={(e) => setCell(a.code, m, e.target.value)}
                            disabled={!editable}
                            placeholder="0"
                            className="mono"
                            style={{ width: 70, textAlign: 'right' }}
                          />
                        </td>
                      );
                    })}
                    <td>
                      <input
                        value={annual[a.code] ?? (sum ? sum.toFixed(2) : '')}
                        onChange={(e) => setAnnual({ ...annual, [a.code]: e.target.value })}
                        disabled={!editable}
                        placeholder="0"
                        className="mono"
                        style={{ width: 90, textAlign: 'right', fontWeight: 700 }}
                      />
                    </td>
                    <td>
                      {editable && (
                        <button className="btn tiny" onClick={() => spreadAnnual(a.code)} title="Distribute annual amount evenly across 12 months">
                          Spread
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value, bold, color }: { label: string; value: string; bold?: boolean; color?: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{
        fontSize: bold ? 22 : 18, fontWeight: bold ? 800 : 700,
        fontFamily: 'var(--font-mono)', color,
      }}>{value}</div>
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
