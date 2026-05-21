// Cash Flow Statement — indirect method.
//
// Starts with net surplus from the Income Statement, adds back non-cash
// expenses, then layers in working-capital changes for operating
// activities. Investing covers fixed asset moves; Financing covers
// member savings, share capital, long-term borrowings and the change
// in the loan portfolio.
//
// The footer reconciles total cash flow to the opening/closing cash
// balance — the `reconciles` flag from the backend is the canary.

import { useEffect, useState } from 'react';
import { cashFlow, type CashFlowSection } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function CashFlowPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const yearStart = today.slice(0, 4) + '-01-01';
  const [from, setFrom] = useState(yearStart);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<Awaited<ReturnType<typeof cashFlow>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await cashFlow(from, to)); }
    catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Cash Flow Statement</h1>
          <div className="page-sub">
            Indirect method. Reconciles net surplus to the actual change in cash by adjusting for
            non-cash items, working-capital changes, investing and financing activities.
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
        <>
          {data.sections.map((s) => <Section key={s.name} s={s} />)}

          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <div>
                <div className="muted tiny">Opening cash</div>
                <div className="mono" style={{ fontSize: 18 }}>{data.opening_cash}</div>
              </div>
              <div>
                <div className="muted tiny">Closing cash</div>
                <div className="mono" style={{ fontSize: 18 }}>{data.closing_cash}</div>
              </div>
              <div>
                <div className="muted tiny">Net change in cash (computed)</div>
                <div className="mono" style={{ fontSize: 18, fontWeight: 700 }}>{data.net_change_in_cash}</div>
              </div>
              <div style={{ marginLeft: 'auto' }}>
                <div className="muted tiny">Reconciles?</div>
                <div style={{
                  fontWeight: 700, fontSize: 18,
                  color: data.reconciles ? 'var(--pos)' : 'var(--neg)',
                }}>
                  {data.reconciles ? '✓ Yes' : '✗ Investigate'}
                </div>
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function Section({ s }: { s: CashFlowSection }) {
  if (s.rows.length === 0) {
    return (
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>{s.name}</h3></div>
        <div className="card-body"><div className="muted">No activity in the period.</div></div>
      </div>
    );
  }
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="card-hd"><h3>{s.name}</h3></div>
      <div className="card-body flush">
        <table className="tbl">
          <tbody>
            {s.rows.map((r, i) => (
              <tr key={i}>
                <td>{r.label}</td>
                <td className="num mono" style={{
                  color: parseFloat(r.amount) > 0 ? 'var(--pos)' : parseFloat(r.amount) < 0 ? 'var(--neg)' : undefined,
                }}>
                  {r.amount}
                </td>
              </tr>
            ))}
            <tr style={{ background: 'var(--surface-2)', fontWeight: 700 }}>
              <td>{`Net cash from ${s.name.toLowerCase()}`}</td>
              <td className="num mono">{s.subtotal}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  );
}
