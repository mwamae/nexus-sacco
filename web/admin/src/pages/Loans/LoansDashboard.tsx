// /loans — Phase 1 Loans Dashboard.
//
// Single-page overview of every loan KPI a branch manager / sacco
// admin needs to start their day. Data comes from one backend call
// (GET /v1/loans/dashboard) so the page is cheap; polls every 60s
// while the tab is visible.
//
// Layout (top → bottom):
//
//   ┌───── KPI strip — 4 wide cards ──────────────────────────────┐
//   │ Total outstanding · PAR proxy · Disbursed M · Collected M    │
//   ├───── Donuts — 2 wide ───────────────────────────────────────┤
//   │ By product (donut) · By status (donut)                       │
//   ├───── 3 column tables ───────────────────────────────────────┤
//   │ Apps by status │ Approaching disbursement │ At-risk preview │
//   └─────────────────────────────────────────────────────────────┘
//
// Donuts are inline SVG — Recharts isn't in the bundle and a Phase
// 1 dashboard doesn't need an interactive chart library. ~60 LOC of
// SVG produces the same visual.
//
// Permission: loans:view.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { getLoanDashboard, extractError, type LoanDashboardKPIs } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const POLL_MS = 60_000;

const STATUS_LABEL: Record<string, string> = {
  pending_disbursement: 'Pending disbursement',
  active: 'Active',
  in_arrears: 'In arrears',
  restructured: 'Restructured',
  defaulted: 'Defaulted',
  settled: 'Settled',
  written_off: 'Written off',
  closed: 'Closed',
};

const STATUS_COLOR: Record<string, string> = {
  active: '#22c55e',
  in_arrears: '#f59e0b',
  restructured: '#a855f7',
  defaulted: '#ef4444',
  pending_disbursement: '#3b82f6',
  settled: '#94a3b8',
  written_off: '#475569',
  closed: '#64748b',
};

const APP_STATUS_LABEL: Record<string, string> = {
  draft: 'Draft',
  pending_validation: 'Pending validation',
  pending_guarantor: 'Pending guarantor',
  pending_scoring: 'Pending scoring',
  pending_approval: 'Pending approval',
  approved: 'Approved',
  approved_with_conditions: 'Approved (conditions)',
  declined: 'Declined',
  returned_for_info: 'Returned for info',
  offer_sent: 'Offer sent',
  offer_accepted: 'Offer accepted',
  offer_declined: 'Offer declined',
  expired: 'Expired',
  cancelled: 'Cancelled',
  disbursed: 'Disbursed',
};

// Recharts-style segment colours sized for 7-ish product slots.
const DONUT_PALETTE = [
  '#3b82f6', '#22c55e', '#f59e0b', '#a855f7',
  '#ef4444', '#06b6d4', '#84cc16', '#ec4899',
  '#f97316', '#64748b',
];

export default function LoansDashboard() {
  useDocumentTitle('Loans · Dashboard');
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const allowed = hasPermission('loans:view');

  const [kpis, setKpis] = useState<LoanDashboardKPIs | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [lastFetchedAt, setLastFetchedAt] = useState<Date | null>(null);
  const initialLoad = useRef(true);

  const refresh = useCallback(async () => {
    try {
      const k = await getLoanDashboard();
      setKpis(k);
      setErr(null);
      setLastFetchedAt(new Date());
    } catch (e) {
      setErr(extractError(e));
    } finally {
      initialLoad.current = false;
    }
  }, []);

  useEffect(() => {
    if (!allowed) return;
    void refresh();
    const t = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(t);
  }, [allowed, refresh]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>Loans</h1></div>
        <div className="alert alert-warn">
          You need the <code>loans:view</code> permission to see the Loans
          dashboard. Ask your SACCO admin to grant your role access.
        </div>
      </div>
    );
  }

  // Pre-compute slices for the two donuts.
  const productSlices = useMemo(() => {
    if (!kpis) return [];
    return kpis.by_product.map((p, i) => ({
      label: p.product_name,
      value: parseFloat(p.outstanding) || 0,
      color: DONUT_PALETTE[i % DONUT_PALETTE.length],
      meta: `${p.active_count} active`,
      filterHref: `/loans/register?product=${p.product_id}`,
    }));
  }, [kpis]);

  const statusSlices = useMemo(() => {
    if (!kpis) return [];
    return Object.entries(kpis.by_status)
      .filter(([_, n]) => n > 0)
      .map(([status, count]) => ({
        label: STATUS_LABEL[status] ?? status,
        value: count,
        color: STATUS_COLOR[status] ?? '#94a3b8',
        meta: `${count} loan${count === 1 ? '' : 's'}`,
        filterHref: `/loans/register?status=${status}`,
      }));
  }, [kpis]);

  const total = kpis?.total_outstanding;
  const totalOutstanding = total
    ? sumDecimal(total.principal_balance, total.interest_balance, total.fees_balance, total.penalty_balance)
    : 0;

  // PAR proxy — at_risk_count / active_count. 0 when no active loans.
  const parPct = total && total.active_count > 0
    ? ((kpis?.at_risk_count ?? 0) / total.active_count) * 100
    : 0;

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Loans · Phase 1</div>
          <h1>Dashboard</h1>
          <div className="page-sub">
            One-pane snapshot of the loan book. Auto-refresh every {POLL_MS / 1000}s.
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          {lastFetchedAt && (
            <span className="muted tiny">
              Updated {lastFetchedAt.toLocaleTimeString()}
            </span>
          )}
          <button className="btn" onClick={() => void refresh()}>↻ Refresh</button>
        </div>
      </div>

      {err && (
        <div className="alert alert-error" role="alert">
          Couldn't load dashboard: {err}
        </div>
      )}
      {!kpis && !err && initialLoad.current && (
        <div className="empty">Loading dashboard…</div>
      )}

      {kpis && (
        <>
          {/* ── Top: 4 KPI cards ──────────────────────────────── */}
          <div style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
            gap: 12,
            marginTop: 12,
          }}>
            <KPI
              label="Total outstanding"
              value={`${currency} ${formatCompact(totalOutstanding)}`}
              sub={`${total?.active_count ?? 0} active loans`}
            />
            <KPI
              label="At-risk (PAR proxy)"
              value={`${parPct.toFixed(1)}%`}
              sub={`${kpis.at_risk_count} of ${total?.active_count ?? 0} active overdue`}
              tone={parPct >= 10 ? 'neg' : parPct >= 5 ? 'warn' : 'pos'}
            />
            <KPI
              label="Disbursed this month"
              value={`${currency} ${formatCompact(parseFloat(kpis.disbursed_this_month) || 0)}`}
              sub={monthLabel()}
            />
            <KPI
              label="Collected this month"
              value={`${currency} ${formatCompact(parseFloat(kpis.collected_this_month) || 0)}`}
              sub={monthLabel()}
            />
          </div>

          {/* ── Middle: 2 donuts ──────────────────────────────── */}
          <div style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(380px, 1fr))',
            gap: 12,
            marginTop: 12,
          }}>
            <Donut title="By product" subtitle="Outstanding per loan product" slices={productSlices} valueFmt={(n) => `${currency} ${formatCompact(n)}`} />
            <Donut title="By status" subtitle="Loan count per status" slices={statusSlices} valueFmt={(n) => `${n}`} />
          </div>

          {/* ── Bottom: 3 panels ──────────────────────────────── */}
          <div style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))',
            gap: 12,
            marginTop: 12,
          }}>
            <AppsByStatusPanel kpis={kpis} />
            <ApproachingDisbursementPanel count={kpis.approaching_disbursement_count} />
            <AtRiskPanel count={kpis.at_risk_count} />
          </div>

          <p className="muted tiny" style={{ marginTop: 14 }}>
            As of {new Date(kpis.as_of).toLocaleString()}.
            PAR proxy is a Phase 1 heuristic (overdue active loans / active loans);
            Phase 3 wires the real DPD engine.
          </p>
        </>
      )}
    </div>
  );
}

// ─────────────── KPI tile ───────────────

function KPI({ label, value, sub, tone }: {
  label: string;
  value: string;
  sub?: string;
  tone?: 'pos' | 'warn' | 'neg';
}) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'warn' ? 'var(--warn)' :
    tone === 'neg' ? 'var(--neg)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="card-body">
        <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
        <div style={{ fontSize: 22, fontWeight: 700, color }}>{value}</div>
        {sub && <div className="muted tiny" style={{ marginTop: 4 }}>{sub}</div>}
      </div>
    </div>
  );
}

// ─────────────── Donut chart (inline SVG, no library) ───────────────

type Slice = {
  label: string;
  value: number;
  color: string;
  meta?: string;
  filterHref?: string;
};

function Donut({ title, subtitle, slices, valueFmt }: {
  title: string;
  subtitle?: string;
  slices: Slice[];
  valueFmt: (n: number) => string;
}) {
  const total = slices.reduce((a, s) => a + s.value, 0);
  // Donut geometry — 200x200 svg, ring inside.
  const size = 200;
  const r = 80;
  const cx = size / 2;
  const cy = size / 2;
  const c = 2 * Math.PI * r;
  let acc = 0;

  return (
    <div className="card">
      <div className="card-hd">
        <h3>{title}</h3>
        {subtitle && <span className="card-sub">{subtitle}</span>}
      </div>
      <div className="card-body" style={{ display: 'flex', gap: 16, alignItems: 'center', flexWrap: 'wrap' }}>
        {total === 0 ? (
          <div className="empty" style={{ flex: 1 }}>No data yet</div>
        ) : (
          <>
            <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={title}>
              <circle cx={cx} cy={cy} r={r} fill="none" stroke="var(--surface-2, #f5f5f5)" strokeWidth={24} />
              {slices.map((s) => {
                const len = (s.value / total) * c;
                const offset = -acc;
                acc += len;
                return (
                  <circle
                    key={s.label}
                    cx={cx} cy={cy} r={r}
                    fill="none"
                    stroke={s.color}
                    strokeWidth={24}
                    strokeDasharray={`${len} ${c - len}`}
                    strokeDashoffset={offset}
                    transform={`rotate(-90 ${cx} ${cy})`}
                  >
                    <title>{`${s.label}: ${valueFmt(s.value)}`}</title>
                  </circle>
                );
              })}
              <text x={cx} y={cy - 4} textAnchor="middle" style={{ fontSize: 12, fill: 'var(--muted)' }}>
                Total
              </text>
              <text x={cx} y={cy + 14} textAnchor="middle" style={{ fontSize: 16, fontWeight: 600, fill: 'var(--fg)' }}>
                {valueFmt(total)}
              </text>
            </svg>
            <ul style={{ listStyle: 'none', margin: 0, padding: 0, flex: 1, minWidth: 0 }}>
              {slices.map((s) => (
                <li key={s.label} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0', minWidth: 0 }}>
                  <span style={{ width: 10, height: 10, background: s.color, borderRadius: 2, flexShrink: 0 }} />
                  {s.filterHref ? (
                    <a href={s.filterHref} style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {s.label}
                    </a>
                  ) : (
                    <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {s.label}
                    </span>
                  )}
                  <span className="mono tiny muted" style={{ flexShrink: 0 }}>{valueFmt(s.value)}</span>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </div>
  );
}

// ─────────────── Bottom-row panels ───────────────

function AppsByStatusPanel({ kpis }: { kpis: LoanDashboardKPIs }) {
  // Show non-zero counts in priority order. Sort by count desc so the
  // busiest buckets land at top of the column.
  const rows = Object.entries(kpis.applications_by_status)
    .filter(([_, n]) => n > 0)
    .sort((a, b) => b[1] - a[1]);
  return (
    <div className="card">
      <div className="card-hd"><h3>Applications by status</h3></div>
      <div className="card-body flush">
        {rows.length === 0 ? (
          <div className="empty">No applications yet</div>
        ) : (
          <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
            {rows.map(([status, count]) => (
              <li key={status}>
                <a
                  href={`/loans/applications?status=${status}`}
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    padding: '8px 12px',
                    borderBottom: '1px solid var(--border)',
                    color: 'inherit',
                    textDecoration: 'none',
                  }}
                >
                  <span>{APP_STATUS_LABEL[status] ?? status}</span>
                  <span className="mono" style={{ fontWeight: 600 }}>{count}</span>
                </a>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function ApproachingDisbursementPanel({ count }: { count: number }) {
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Approaching disbursement</h3>
        <span className="card-sub">Loans awaiting disburse step</span>
      </div>
      <div className="card-body">
        {count === 0 ? (
          <div className="empty">No loans awaiting disbursement</div>
        ) : (
          <>
            <div style={{ fontSize: 24, fontWeight: 700 }}>{count}</div>
            <div className="muted tiny" style={{ marginTop: 4 }}>
              Click to open the register filtered to pending disbursement.
            </div>
            <a className="btn btn-sm" href="/loans/register?status=pending_disbursement" style={{ marginTop: 8 }}>
              Open list →
            </a>
          </>
        )}
      </div>
    </div>
  );
}

function AtRiskPanel({ count }: { count: number }) {
  // Phase 1: just the count + a link. Phase 3's DPD engine will let
  // us preview the top 5 highest-balance overdue loans here.
  return (
    <div className="card">
      <div className="card-hd">
        <h3>At-risk loans</h3>
        <span className="card-sub">Active loans past their next due date</span>
      </div>
      <div className="card-body">
        {count === 0 ? (
          <div className="empty">Nothing overdue. 🎉</div>
        ) : (
          <>
            <div style={{ fontSize: 24, fontWeight: 700, color: 'var(--warn)' }}>{count}</div>
            <div className="muted tiny" style={{ marginTop: 4 }}>
              Loans with next_installment_due_at in the past.
              Phase 3 surfaces top-5 preview + real DPD classification.
            </div>
            <a className="btn btn-sm" href="/loans/register?dpd=1plus" style={{ marginTop: 8 }}>
              Open at-risk list →
            </a>
          </>
        )}
      </div>
    </div>
  );
}

// ─────────────── Helpers ───────────────

function sumDecimal(...parts: (string | undefined)[]): number {
  return parts.reduce((acc: number, p) => acc + (parseFloat(p ?? '0') || 0), 0);
}

function formatCompact(n: number): string {
  if (!isFinite(n)) return '0';
  const abs = Math.abs(n);
  if (abs >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (abs >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return n.toLocaleString(undefined, { maximumFractionDigits: 0 });
}

function monthLabel(): string {
  return new Date().toLocaleString(undefined, { month: 'long', year: 'numeric' });
}

// Silence unused-import lint when the file is pruned in tests.
export type _R = ReactNode;
