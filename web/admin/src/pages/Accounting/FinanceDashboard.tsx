// Management KPI Dashboard — landing page for finance leadership.
// 10 KPI tiles, two SVG line charts (12-month balance + monthly P&L),
// and the top 5 income / expense accounts year-to-date. Everything
// derives from the posted GL via a single endpoint.

import { useEffect, useMemo, useState } from 'react';
import {
  getFinanceDashboard,
  type Dashboard,
  type MonthPoint,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function FinanceDashboardPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [asOf, setAsOf] = useState(today);
  const [data, setData] = useState<Dashboard | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await getFinanceDashboard(asOf)); }
    catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Executive</div>
          <h1>Management dashboard</h1>
          <div className="page-sub">
            Headline KPIs, 12-month trend, and the top contributors to revenue and expense.
            All values derive from the posted GL through the selected as-of date.
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">As of</div>
            <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Refresh'}
          </button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && (
        <>
          {/* ─── Primary KPI tiles ─── */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12, marginTop: 12 }}>
            <Card label="Total assets" value={data.kpis.total_assets} primary />
            <Card label="Total deposits" value={data.kpis.total_deposits} />
            <Card label="Gross loans" value={data.kpis.gross_loans} />
            <Card label="Cash position" value={data.kpis.cash_position} />
            <Card label="Net surplus YTD" value={data.kpis.net_surplus_ytd}
                  color={parseFloat(data.kpis.net_surplus_ytd) >= 0 ? 'var(--pos)' : 'var(--neg)'} />
            <Card label="Total equity" value={data.kpis.total_equity} />
            <Card label="Core capital" value={data.kpis.core_capital} />
            <Card label="Provisions" value={data.kpis.provisions}
                  color="var(--neg)" />
          </div>

          {/* ─── Ratio strip ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Key ratios</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', gap: 18 }}>
              <Ratio label="Liquidity ratio" pct={data.kpis.liquidity_ratio_pct} min={15} />
              <Ratio label="Loan-to-deposit" pct={data.kpis.loan_to_deposit_ratio_pct}
                     comment="Typical SACCO target: 70-80%" />
              <Ratio label="Core capital ratio" pct={data.kpis.core_capital_ratio_pct} min={10} />
              <Ratio label="Cost-to-income" pct={data.kpis.cost_to_income_ratio_pct} max={75}
                     comment="Lower is better" />
              <Ratio label="Provision coverage" pct={data.kpis.provision_coverage_pct} />
            </div>
          </div>

          {/* ─── Trend charts ─── */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 12, marginTop: 12 }}>
            <TrendChart title="Balance sheet trend" trend={data.monthly_trend} series={[
              { key: 'total_assets', label: 'Assets', color: '#3b6ab8' },
              { key: 'total_deposits', label: 'Deposits', color: '#c97a00' },
              { key: 'gross_loans', label: 'Loans', color: 'var(--pos)' },
            ]} />
            <TrendChart title="Monthly P&L" trend={data.monthly_trend} series={[
              { key: 'income', label: 'Income', color: 'var(--pos)' },
              { key: 'expense', label: 'Expense', color: 'var(--neg)' },
              { key: 'net_surplus', label: 'Net surplus', color: '#3b6ab8' },
            ]} />
          </div>

          {/* ─── Top accounts ─── */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 12, marginTop: 12 }}>
            <TopList title="Top 5 income (YTD)" rows={data.top_income_ytd} positive />
            <TopList title="Top 5 expense (YTD)" rows={data.top_expense_ytd} />
          </div>
        </>
      )}
    </div>
  );
}

function Card({ label, value, primary, color }: { label: string; value: string; primary?: boolean; color?: string }) {
  return (
    <div className="card" style={{ padding: 16 }}>
      <div className="muted tiny">{label}</div>
      <div className="mono" style={{
        fontSize: primary ? 28 : 22, fontWeight: primary ? 800 : 700, marginTop: 6,
        color,
      }}>
        {value}
      </div>
    </div>
  );
}

function Ratio({ label, pct, min, max, comment }: { label: string; pct: string; min?: number; max?: number; comment?: string }) {
  const v = parseFloat(pct);
  let compliant = true;
  if (min !== undefined && v < min) compliant = false;
  if (max !== undefined && v > max) compliant = false;
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div className="mono" style={{ fontSize: 22, fontWeight: 700, color: compliant ? undefined : 'var(--neg)' }}>
        {pct}%
      </div>
      <div className="muted tiny" style={{ marginTop: 2 }}>
        {min !== undefined && <span>min {min}% · </span>}
        {max !== undefined && <span>max {max}% · </span>}
        {comment ?? (compliant ? '✓ ok' : '✗ check')}
      </div>
    </div>
  );
}

type SeriesKey = 'total_assets' | 'total_deposits' | 'gross_loans' | 'income' | 'expense' | 'net_surplus';

function TrendChart({ title, trend, series }: {
  title: string;
  trend: MonthPoint[];
  series: { key: SeriesKey; label: string; color: string }[];
}) {
  const width = 520, height = 220, padding = { left: 50, right: 12, top: 24, bottom: 30 };
  const innerW = width - padding.left - padding.right;
  const innerH = height - padding.top - padding.bottom;

  const points = useMemo(() => {
    return series.map((s) => ({
      ...s,
      data: trend.map((t, i) => ({ x: i, y: parseFloat(t[s.key]) })),
    }));
  }, [trend, series]);

  const allY = points.flatMap((p) => p.data.map((d) => d.y));
  let minY = Math.min(0, ...allY);
  let maxY = Math.max(0, ...allY);
  if (minY === maxY) maxY = minY + 1;
  const ySpan = maxY - minY;
  const xCount = Math.max(1, trend.length - 1);

  const scaleX = (i: number) => padding.left + (i / xCount) * innerW;
  const scaleY = (v: number) => padding.top + innerH - ((v - minY) / ySpan) * innerH;

  function path(data: { x: number; y: number }[]) {
    return data.map((p, i) => `${i === 0 ? 'M' : 'L'}${scaleX(p.x).toFixed(1)},${scaleY(p.y).toFixed(1)}`).join(' ');
  }

  // y-axis labels at 0, mid, max
  const ticks = [maxY, (maxY + minY) / 2, minY];

  return (
    <div className="card">
      <div className="card-hd"><h3>{title}</h3></div>
      <div className="card-body" style={{ overflow: 'auto' }}>
        <svg width={width} height={height} style={{ display: 'block', maxWidth: '100%' }}>
          {/* Gridlines + y-axis labels */}
          {ticks.map((t, i) => {
            const y = scaleY(t);
            return (
              <g key={i}>
                <line x1={padding.left} x2={width - padding.right} y1={y} y2={y}
                      stroke="var(--surface-2)" strokeDasharray="2 3" />
                <text x={padding.left - 6} y={y + 3} fontSize="10" textAnchor="end" fill="var(--muted)">
                  {fmtShort(t)}
                </text>
              </g>
            );
          })}

          {/* X-axis labels (every 2nd month so it fits) */}
          {trend.map((t, i) => (
            i % 2 === 0 && (
              <text key={i} x={scaleX(i)} y={height - padding.bottom + 14}
                    fontSize="10" textAnchor="middle" fill="var(--muted)">
                {t.month.slice(2)}
              </text>
            )
          ))}

          {/* Series lines */}
          {points.map((p) => (
            <g key={p.key}>
              <path d={path(p.data)} fill="none" stroke={p.color} strokeWidth={2} />
              {p.data.map((d, i) => (
                <circle key={i} cx={scaleX(d.x)} cy={scaleY(d.y)} r={2.5} fill={p.color} />
              ))}
            </g>
          ))}
        </svg>

        {/* Legend */}
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', marginTop: 4 }}>
          {points.map((p) => (
            <div key={p.key} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ display: 'inline-block', width: 12, height: 12, background: p.color, borderRadius: 2 }} />
              <span className="tiny">{p.label}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

function TopList({ title, rows, positive }: { title: string; rows: { code: string; name: string; amount: string }[]; positive?: boolean }) {
  return (
    <div className="card">
      <div className="card-hd"><h3>{title}</h3><span className="card-sub">{rows.length}</span></div>
      <div className="card-body flush">
        <table className="tbl">
          <thead><tr><th>Code</th><th>Account</th><th className="num">Amount</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.code}>
                <td className="mono">{r.code}</td>
                <td>{r.name}</td>
                <td className="num mono" style={{ color: positive ? 'var(--pos)' : 'var(--neg)' }}>{r.amount}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><td colSpan={3} className="muted" style={{ textAlign: 'center', padding: 12 }}>No activity in the period.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function fmtShort(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1_000_000) return (v / 1_000_000).toFixed(1) + 'M';
  if (abs >= 1_000) return (v / 1_000).toFixed(1) + 'k';
  return v.toFixed(0);
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
