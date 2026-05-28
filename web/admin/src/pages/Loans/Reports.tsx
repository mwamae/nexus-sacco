// /loans/reports — Loans Phase 2 Reports page.
//
// Tabbed shell with 8 tabs (PAR / Aging / Vintage / Officers /
// Disbursements / Repayments / Guarantors / Top-N). Hash-routed so
// /loans/reports#vintage etc. work as direct deep-links. Each tab:
//
//   * Filter bar (date range + report-specific filters)
//   * Headline cards (where applicable)
//   * Inline-SVG chart (line/bar/heatmap — no recharts dep)
//   * Data table
//   * Export CSV button (server generates the CSV)
//
// Permission: loans:reports.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  getLoanReportPAR,
  getLoanReportPARHistory,
  getLoanReportAging,
  getLoanReportVintage,
  getLoanReportOfficers,
  getLoanReportDisbursements,
  getLoanReportRepayments,
  getLoanReportGuarantorExposure,
  getLoanReportTopN,
  loanReportCSVURL,
  extractError,
} from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

type TabId = 'par' | 'aging' | 'vintage' | 'officers' | 'disbursements' | 'repayments' | 'guarantors' | 'top-n';

const TABS: { id: TabId; label: string }[] = [
  { id: 'par',           label: 'PAR' },
  { id: 'aging',         label: 'Aging' },
  { id: 'vintage',       label: 'Vintage' },
  { id: 'officers',      label: 'Officers' },
  { id: 'disbursements', label: 'Disbursements' },
  { id: 'repayments',    label: 'Repayments' },
  { id: 'guarantors',    label: 'Guarantors' },
  { id: 'top-n',         label: 'Top-N' },
];

function tabFromHash(): TabId {
  const hash = window.location.hash.replace('#', '') as TabId;
  if (TABS.some((t) => t.id === hash)) return hash;
  return 'par';
}

export default function LoanReports() {
  useDocumentTitle('Loans · Reports');
  const { hasPermission } = useAuth();
  const allowed = hasPermission('loans:reports');
  const [tab, setTab] = useState<TabId>(tabFromHash());

  useEffect(() => {
    const onHash = () => setTab(tabFromHash());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>Reports</h1></div>
        <div className="alert alert-warn">You need <code>loans:reports</code> permission.</div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Loans · Reports</div>
          <h1>Portfolio reports</h1>
          <div className="page-sub">
            Phase 2 — PAR, aging, vintage, officers, registers, exposure. CSV export on every tab.
          </div>
        </div>
        <a className="btn btn-sm" href="/loans/reports/sasra">SASRA quarterly extract →</a>
      </div>

      <div className="card" style={{ marginBottom: 12 }}>
        <div style={{ display: 'flex', gap: 4, padding: '6px 6px 0', borderBottom: '1px solid var(--border)', flexWrap: 'wrap' }}>
          {TABS.map((t) => (
            <a
              key={t.id}
              href={`#${t.id}`}
              style={{
                padding: '8px 14px',
                borderRadius: '6px 6px 0 0',
                borderBottom: tab === t.id ? '2px solid var(--accent)' : '2px solid transparent',
                color: tab === t.id ? 'var(--accent)' : 'var(--muted)',
                fontWeight: tab === t.id ? 600 : 400,
                textDecoration: 'none',
              }}
            >
              {t.label}
            </a>
          ))}
        </div>
        <div className="card-body">
          {tab === 'par'           && <PARTab />}
          {tab === 'aging'         && <AgingTab />}
          {tab === 'vintage'       && <VintageTab />}
          {tab === 'officers'      && <OfficersTab />}
          {tab === 'disbursements' && <DisbursementsTab />}
          {tab === 'repayments'    && <RepaymentsTab />}
          {tab === 'guarantors'    && <GuarantorsTab />}
          {tab === 'top-n'         && <TopNTab />}
        </div>
      </div>
    </div>
  );
}

// ─────────────── PAR ───────────────

function PARTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [par, setPar] = useState<any>(null);
  const [hist, setHist] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void Promise.all([getLoanReportPAR(), getLoanReportPARHistory(90)])
      .then(([p, h]) => { setPar(p); setHist(h); })
      .catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!par) return <div className="empty">Loading…</div>;

  const pts = (hist?.points ?? []) as { snapshot_date: string; par_30: string }[];
  return (
    <>
      <SnapshotBanner meta={par.snapshot} />
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 12, marginBottom: 12 }}>
        <KPI label="PAR-1"  value={fmtPct(par.par_1)}  tone="warn" />
        <KPI label="PAR-30" value={fmtPct(par.par_30)} tone="neg" />
        <KPI label="PAR-90" value={fmtPct(par.par_90)} tone="neg" />
        <KPI label="Total outstanding" value={`${currency} ${fmt(par.total_outstanding)}`} />
      </div>
      <div className="card">
        <div className="card-hd"><h3>PAR-30 over last 90 days</h3></div>
        <div className="card-body">
          {pts.length === 0 ? (
            <div className="empty">Run the loan-snapshotter once to populate history.</div>
          ) : (
            <LineChart points={pts.map((p) => ({ x: p.snapshot_date, y: parseFloat(p.par_30) }))} yLabel="PAR-30 %" />
          )}
        </div>
      </div>
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>By product</h3>
          <div className="card-hd-actions">
            <a className="btn btn-sm" href={loanReportCSVURL('par')}>Download CSV</a>
          </div>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead><tr><th>Product</th><th className="num">Total principal</th><th className="num">PAR-30 principal</th><th className="num">PAR-30 %</th></tr></thead>
            <tbody>
              {(par.by_product ?? []).map((p: any) => (
                <tr key={p.product_id}>
                  <td>{p.product_name}</td>
                  <td className="num mono">{currency} {fmt(p.total_principal)}</td>
                  <td className="num mono">{currency} {fmt(p.par_30_principal)}</td>
                  <td className="num mono">{fmtPct(p.par_30)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────────── Aging ───────────────

function AgingTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getLoanReportAging().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;

  const buckets = data.buckets ?? [];
  const max = Math.max(1, ...buckets.map((b: any) => parseFloat(b.total) || 0));

  return (
    <>
      <SnapshotBanner meta={data.snapshot} />
      <div className="card">
        <div className="card-hd">
          <h3>Aging buckets</h3>
          <div className="card-hd-actions">
            <a className="btn btn-sm" href={loanReportCSVURL('aging-buckets')}>Download CSV</a>
          </div>
        </div>
        <div className="card-body">
          <div style={{ display: 'flex', alignItems: 'flex-end', gap: 14, height: 200, padding: '0 10px' }}>
            {buckets.map((b: any, i: number) => {
              const v = parseFloat(b.total) || 0;
              const heightPct = (v / max) * 100;
              return (
                <a key={i} href={`/loans/register?dpd=1plus`} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', textDecoration: 'none' }}>
                  <div className="muted tiny mono" style={{ marginBottom: 4 }}>{currency} {fmtCompact(v)}</div>
                  <div style={{ width: '100%', height: `${heightPct}%`, background: i === 0 ? '#22c55e' : i < 3 ? '#f59e0b' : '#ef4444', borderRadius: '4px 4px 0 0', minHeight: 2 }} />
                  <div style={{ marginTop: 6, fontSize: 11, textAlign: 'center', color: 'var(--fg)' }}>{b.label}</div>
                  <div className="muted tiny">{b.count} loans</div>
                </a>
              );
            })}
          </div>
        </div>
      </div>
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body flush">
          <table className="tbl">
            <thead><tr><th>Bucket</th><th className="num">Count</th><th className="num">Principal</th><th className="num">Interest</th><th className="num">Penalty</th><th className="num">Total</th></tr></thead>
            <tbody>
              {buckets.map((b: any, i: number) => (
                <tr key={i}>
                  <td>{b.label}</td>
                  <td className="num mono">{b.count}</td>
                  <td className="num mono">{currency} {fmt(b.principal)}</td>
                  <td className="num mono">{currency} {fmt(b.interest)}</td>
                  <td className="num mono">{currency} {fmt(b.penalty)}</td>
                  <td className="num mono">{currency} {fmt(b.total)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────────── Vintage ───────────────

function VintageTab() {
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getLoanReportVintage().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const cohorts = data.cohorts ?? [];
  if (cohorts.length === 0) return <div className="empty">No disbursed cohorts in the selected window.</div>;
  return (
    <>
      <div className="card">
        <div className="card-hd">
          <h3>Vintage heatmap — PAR-30 % by cohort × months on book</h3>
          <div className="card-hd-actions">
            <a className="btn btn-sm" href={loanReportCSVURL('vintage')}>Download CSV</a>
          </div>
        </div>
        <div className="card-body">
          <table style={{ borderCollapse: 'separate', borderSpacing: 2 }}>
            <thead>
              <tr>
                <th className="muted tiny" style={{ padding: 4 }}>Cohort</th>
                {(cohorts[0]?.performance ?? []).map((p: any) => (
                  <th key={p.months_on_book} className="muted tiny" style={{ padding: 4, textAlign: 'center' }}>{p.months_on_book}mo</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {cohorts.map((c: any) => (
                <tr key={c.disbursement_month}>
                  <td className="mono tiny" style={{ padding: 4 }}>
                    {c.disbursement_month}
                    <div className="muted tiny">{c.disbursed_count} loans</div>
                  </td>
                  {c.performance.map((p: any) => {
                    const v = parseFloat(p.par_30) || 0;
                    const tone = v >= 10 ? 'var(--neg)' : v >= 5 ? 'var(--warn)' : 'var(--pos)';
                    return (
                      <td key={p.months_on_book}
                          title={`${c.disbursement_month} · ${p.months_on_book}mo · PAR-30: ${v.toFixed(2)}%`}
                          style={{
                            padding: '8px 12px',
                            background: tone + '22',
                            color: tone,
                            textAlign: 'center',
                            borderRadius: 4,
                            minWidth: 50,
                            fontWeight: 600,
                            fontSize: 11,
                          }}>
                        {v.toFixed(1)}%
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────────── Officers ───────────────

function OfficersTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  const [sortBy, setSortBy] = useState<'disbursed_amount' | 'collected_amount' | 'par_30'>('disbursed_amount');
  useEffect(() => {
    void getLoanReportOfficers().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const officers = (data.officers ?? []).slice().sort((a: any, b: any) => parseFloat(b[sortBy] ?? '0') - parseFloat(a[sortBy] ?? '0'));
  const max = Math.max(1, ...officers.map((o: any) => parseFloat(o.disbursed_amount) || 0));
  return (
    <>
      <div className="card">
        <div className="card-hd">
          <h3>Officer leaderboard — top by {sortBy.replace('_', ' ')}</h3>
          <div className="card-hd-actions">
            <select className="input" value={sortBy} onChange={(e) => setSortBy(e.target.value as any)}>
              <option value="disbursed_amount">Disbursed</option>
              <option value="collected_amount">Collected</option>
              <option value="par_30">PAR-30</option>
            </select>
            <a className="btn btn-sm" href={loanReportCSVURL('officers')}>Download CSV</a>
          </div>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead><tr><th>Officer</th><th className="num">Disbursed (count)</th><th className="num">Disbursed</th><th>%</th><th className="num">Collected</th><th className="num">PAR-30</th><th className="num">Write-off</th></tr></thead>
            <tbody>
              {officers.map((o: any) => {
                const pct = (parseFloat(o.disbursed_amount) / max) * 100;
                return (
                  <tr key={o.user_id}>
                    <td>{o.user_name}</td>
                    <td className="num mono">{o.disbursed_count}</td>
                    <td className="num mono">{currency} {fmt(o.disbursed_amount)}</td>
                    <td><div style={{ background: 'var(--accent)', height: 6, width: `${pct}%`, minWidth: 1, borderRadius: 3 }} /></td>
                    <td className="num mono">{currency} {fmt(o.collected_amount)}</td>
                    <td className="num mono">{fmtPct(o.par_30)}</td>
                    <td className="num mono">{currency} {fmt(o.write_off_amount)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────────── Disbursements ───────────────

function DisbursementsTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getLoanReportDisbursements({ limit: 100 }).then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const rows = data.rows ?? [];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Disbursements — last 30 days</h3>
        <div className="card-hd-actions">
          <a className="btn btn-sm" href={loanReportCSVURL('disbursements')}>Download CSV</a>
        </div>
      </div>
      <div className="card-body flush">
        {rows.length === 0 ? <div className="empty">No disbursements in window.</div> : (
          <table className="tbl">
            <thead><tr><th>Loan no</th><th>Member</th><th>Product</th><th className="num">Amount</th><th>Channel</th><th>Disbursed at</th><th>Officer</th></tr></thead>
            <tbody>
              {rows.map((r: any) => (
                <tr key={r.loan_id}>
                  <td className="mono">{r.loan_no}</td>
                  <td>{r.member_name}<div className="muted tiny mono">{r.member_no}</div></td>
                  <td>{r.product}</td>
                  <td className="num mono">{currency} {fmt(r.amount)}</td>
                  <td>{r.channel ?? '—'}</td>
                  <td className="tiny">{new Date(r.disbursed_at).toLocaleDateString()}</td>
                  <td>{r.officer}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────────── Repayments ───────────────

function RepaymentsTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getLoanReportRepayments({ limit: 100 }).then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const rows = data.rows ?? [];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Repayments — last 30 days</h3>
        <div className="card-hd-actions">
          <a className="btn btn-sm" href={loanReportCSVURL('repayments')}>Download CSV</a>
        </div>
      </div>
      <div className="card-body flush">
        {rows.length === 0 ? <div className="empty">No repayments in window.</div> : (
          <table className="tbl">
            <thead><tr><th>Loan no</th><th>Member</th><th className="num">Amount</th><th>Channel</th><th className="num">Principal</th><th className="num">Interest</th><th>Posted</th></tr></thead>
            <tbody>
              {rows.map((r: any) => (
                <tr key={r.txn_id}>
                  <td className="mono">{r.loan_no}</td>
                  <td>{r.member_name}</td>
                  <td className="num mono">{currency} {fmt(r.amount)}</td>
                  <td>{r.channel ?? '—'}</td>
                  <td className="num mono">{fmt(r.principal)}</td>
                  <td className="num mono">{fmt(r.interest)}</td>
                  <td className="tiny">{new Date(r.posted_at).toLocaleDateString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────────── Guarantor exposure ───────────────

function GuarantorsTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getLoanReportGuarantorExposure().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const rows = (data.rows ?? []) as any[];
  const max = Math.max(1, ...rows.map((r) => parseFloat(r.total_guaranteed) || 0));
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Top-50 guarantors by total guaranteed</h3>
        <div className="card-hd-actions">
          <a className="btn btn-sm" href={loanReportCSVURL('guarantor-exposure')}>Download CSV</a>
        </div>
      </div>
      <div className="card-body flush">
        {rows.length === 0 ? <div className="empty">No guarantors on file.</div> : (
          <table className="tbl">
            <thead><tr><th>Member no</th><th>Guarantor</th><th className="num">Total guaranteed</th><th></th><th>Active count</th></tr></thead>
            <tbody>
              {rows.map((r) => {
                const pct = (parseFloat(r.total_guaranteed) / max) * 100;
                return (
                  <tr key={r.guarantor_member_id}>
                    <td className="mono">{r.guarantor_no}</td>
                    <td>{r.guarantor_name}</td>
                    <td className="num mono">{currency} {fmt(r.total_guaranteed)}</td>
                    <td><div style={{ background: 'var(--accent)', height: 6, width: `${pct}%`, minWidth: 1, borderRadius: 3 }} /></td>
                    <td>{r.active_count}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────────── Top-N ───────────────

function TopNTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [metric, setMetric] = useState<'outstanding' | 'disbursed' | 'collected'>('outstanding');
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  const refresh = useCallback(() => {
    setData(null);
    void getLoanReportTopN(metric, 50).then(setData).catch((e) => setErr(extractError(e)));
  }, [metric]);
  useEffect(() => { refresh(); }, [refresh]);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const rows = (data.rows ?? []) as any[];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Top-50 borrowers by {metric}</h3>
        <div className="card-hd-actions">
          <select className="input" value={metric} onChange={(e) => setMetric(e.target.value as any)}>
            <option value="outstanding">Outstanding</option>
            <option value="disbursed">Disbursed</option>
            <option value="collected">Collected</option>
          </select>
          <a className="btn btn-sm" href={loanReportCSVURL('top-n', { metric })}>Download CSV</a>
        </div>
      </div>
      <div className="card-body flush">
        {rows.length === 0 ? <div className="empty">No data.</div> : (
          <table className="tbl">
            <thead><tr><th>#</th><th>Member no</th><th>Member name</th><th className="num">{metric}</th></tr></thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={r.member_id} style={{ cursor: 'pointer' }} onClick={() => { window.location.href = `/members/${r.member_id}`; }}>
                  <td className="mono">{i + 1}</td>
                  <td className="mono">{r.member_no}</td>
                  <td>{r.member_name}</td>
                  <td className="num mono">{currency} {fmt(r.value)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────────── Inline SVG chart helpers ───────────────

function LineChart({ points, yLabel }: { points: { x: string; y: number }[]; yLabel: string }) {
  if (points.length === 0) return null;
  const W = 600, H = 200, padL = 40, padR = 10, padT = 10, padB = 28;
  const xs = points.map((_, i) => padL + (i / Math.max(1, points.length - 1)) * (W - padL - padR));
  const maxY = Math.max(1, ...points.map((p) => p.y));
  const ys = points.map((p) => H - padB - (p.y / maxY) * (H - padT - padB));
  const path = points.map((_, i) => `${i === 0 ? 'M' : 'L'} ${xs[i].toFixed(1)} ${ys[i].toFixed(1)}`).join(' ');
  return (
    <svg viewBox={`0 0 ${W} ${H}`} width="100%" style={{ maxWidth: 800 }} role="img" aria-label={yLabel}>
      <text x={padL} y={padT + 6} fontSize={10} fill="var(--muted)">{yLabel} (max {maxY.toFixed(1)})</text>
      <line x1={padL} y1={H - padB} x2={W - padR} y2={H - padB} stroke="var(--border)" />
      <line x1={padL} y1={padT} x2={padL} y2={H - padB} stroke="var(--border)" />
      <path d={path} fill="none" stroke="var(--accent)" strokeWidth={2} />
      {points.map((p, i) => (
        <circle key={i} cx={xs[i]} cy={ys[i]} r={3} fill="var(--accent)">
          <title>{p.x}: {p.y.toFixed(2)}</title>
        </circle>
      ))}
      <text x={padL} y={H - 4} fontSize={9} fill="var(--muted)">{points[0]?.x}</text>
      <text x={W - padR} y={H - 4} fontSize={9} fill="var(--muted)" textAnchor="end">{points[points.length - 1]?.x}</text>
    </svg>
  );
}

function KPI({ label, value, tone }: { label: string; value: string; tone?: 'pos' | 'warn' | 'neg' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'warn' ? 'var(--warn)' :
    tone === 'neg' ? 'var(--neg)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="card-body">
        <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
        <div style={{ fontSize: 22, fontWeight: 700, color }}>{value}</div>
      </div>
    </div>
  );
}

// SnapshotBanner — Phase 3 "verification gate" for reports backed by
// loan_dpd_snapshots. Hidden when the snapshot is fresh AND covers
// every eligible loan. Shows a soft warning when the snapshot is
// 1+ day stale or doesn't cover every loan (the report falls back
// to the inline DPD proxy for un-snapshotted loans).
type SnapshotMetaShape = {
  available: boolean;
  latest_snapshot_date?: string;
  staleness_days: number;
  loans_with_snapshots: number;
  loans_without_snapshots: number;
};
function SnapshotBanner({ meta }: { meta?: SnapshotMetaShape | null }) {
  if (!meta) return null;
  if (!meta.available) {
    return (
      <div className="alert alert-warn" style={{ marginBottom: 12 }}>
        DPD snapshots are empty — the dpd-classifier worker hasn't run yet for this
        tenant. Numbers below use the inline DPD proxy and may not match what the
        provisioning cycle will see. The classifier runs nightly; trigger a manual
        run with <code>dpd-classifier --once</code> to refresh immediately.
      </div>
    );
  }
  if (meta.staleness_days >= 1 || meta.loans_without_snapshots > 0) {
    return (
      <div className="alert alert-info" style={{ marginBottom: 12 }}>
        DPD snapshots: latest from <strong>{meta.latest_snapshot_date}</strong>
        {meta.staleness_days >= 1 && (
          <> ({meta.staleness_days} day{meta.staleness_days === 1 ? '' : 's'} stale)</>
        )}
        {meta.loans_without_snapshots > 0 && (
          <> · {meta.loans_without_snapshots} loan{meta.loans_without_snapshots === 1 ? '' : 's'} fall back to the inline DPD proxy</>
        )}
        .
      </div>
    );
  }
  return null;
}

function fmt(v: string | number | undefined): string {
  const n = typeof v === 'number' ? v : parseFloat(v ?? '0');
  if (!isFinite(n)) return String(v ?? '');
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
function fmtPct(v: string | number | undefined): string {
  const n = typeof v === 'number' ? v : parseFloat(v ?? '0');
  if (!isFinite(n)) return '0.00%';
  return n.toFixed(2) + '%';
}
function fmtCompact(n: number): string {
  if (!isFinite(n)) return '0';
  const abs = Math.abs(n);
  if (abs >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (abs >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return n.toLocaleString(undefined, { maximumFractionDigits: 0 });
}
