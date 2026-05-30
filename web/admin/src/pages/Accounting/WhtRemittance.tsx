// DSID Phase 2.1 — WHT iTax remittance page.
//
// Period picker + tax-type filter + Generate CSV button + table view
// of who's in the file before submission.

import { useEffect, useMemo, useState } from 'react';
import {
  getWHTRemittance,
  whtRemittanceCSVURL,
  openAuthedFile,
  type WHTRemittanceResponse,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

function lastMonthLabel(): string {
  const d = new Date();
  d.setUTCMonth(d.getUTCMonth() - 1);
  return d.toISOString().slice(0, 7); // YYYY-MM
}

function fmtKES(s: string | undefined): string {
  if (!s) return '—';
  const n = parseFloat(s);
  if (Number.isNaN(n)) return s;
  return n.toLocaleString('en-KE', { maximumFractionDigits: 2 });
}

export default function WhtRemittancePage() {
  useDocumentTitle('Accounting · WHT remittance');
  const { hasPermission } = useAuth();
  const allowed = hasPermission('loans:reports');

  const [period, setPeriod] = useState(lastMonthLabel());
  const [taxType, setTaxType] = useState<'interest' | 'dividend' | 'both'>('both');
  const [data, setData] = useState<WHTRemittanceResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setBusy(true); setErr(null);
    try { setData(await getWHTRemittance(period, taxType)); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.'); }
    finally { setBusy(false); }
  }
  useEffect(() => { if (allowed) void load(); }, [period, taxType, allowed]);

  const csvURL = useMemo(() => whtRemittanceCSVURL(period, taxType), [period, taxType]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>WHT remittance</h1></div>
        <div className="alert alert-warn">You need <code>loans:reports</code> to view this page.</div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Accounting · Tax</div>
          <h1>WHT iTax remittance</h1>
          <div className="page-sub">
            Source: <code>tax_payable_ledger</code> rows captured at run-post time. Tax-exempt members
            are excluded once the <code>tax_exempt</code> column is added (Phase 2.4).
          </div>
        </div>
      </div>

      {/* Filters */}
      <div className="card" style={{ padding: 12, marginBottom: 12, display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label>
          <div className="muted tiny" style={{ marginBottom: 2 }}>Period</div>
          <input className="input" type="month" value={period} onChange={(e) => setPeriod(e.target.value)} />
        </label>
        <label>
          <div className="muted tiny" style={{ marginBottom: 2 }}>Tax type</div>
          <select className="select" value={taxType} onChange={(e) => setTaxType(e.target.value as 'interest' | 'dividend' | 'both')}>
            <option value="both">Interest + Dividend</option>
            <option value="interest">Interest only (15%)</option>
            <option value="dividend">Dividend only (5%)</option>
          </select>
        </label>
        <div style={{ flex: 1 }} />
        <button
          className="btn btn-primary"
          disabled={busy || !data || data.items.length === 0}
          onClick={() => void openAuthedFile(csvURL)}
        >Download CSV ↓</button>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {data && (
        <>
          {/* Summary */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 10, marginBottom: 12 }}>
            <Summary label="Payees" value={data.items.length.toString()} />
            <Summary label="Total gross" value={`KES ${fmtKES(data.total_gross)}`} accent="#2c5282" />
            <Summary label="Total withheld" value={`KES ${fmtKES(data.total_wht)}`} accent="#b42318" />
            <Summary label="Total net paid" value={`KES ${fmtKES(data.total_net)}`} accent="#146c43" />
          </div>

          {/* Table */}
          <div className="card">
            <div className="card-hd">
              <h3>Lines · {data.period} · {data.tax_type}</h3>
              <span className="card-sub">{data.items.length} row{data.items.length === 1 ? '' : 's'}</span>
            </div>
            <div className="card-body flush">
              {data.items.length === 0 ? (
                <div className="empty" style={{ padding: 14 }}>
                  No taxable interest / dividend rows posted in this period.
                </div>
              ) : (
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>Member no</th>
                      <th>Full name</th>
                      <th>KRA PIN</th>
                      <th>Source</th>
                      <th>Run</th>
                      <th>Posted</th>
                      <th className="num">Gross</th>
                      <th className="num">WHT %</th>
                      <th className="num">Withheld</th>
                      <th className="num">Net</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.items.map((r, i) => (
                      <tr key={`${r.run_no}-${r.member_no}-${i}`}>
                        <td className="mono">{r.member_no}</td>
                        <td>{r.full_name}</td>
                        <td className="mono tiny">{r.kra_pin || '—'}</td>
                        <td className="tiny">{r.source_kind.replace(/_/g, ' ')}</td>
                        <td className="mono tiny">{r.run_no}</td>
                        <td className="tiny mono">{r.posted_at}</td>
                        <td className="num mono">{fmtKES(r.gross_amount)}</td>
                        <td className="num">{r.wht_rate_pct}%</td>
                        <td className="num mono" style={{ color: 'var(--danger-fg, #b42318)' }}>{fmtKES(r.wht_withheld)}</td>
                        <td className="num mono">{fmtKES(r.net_amount)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function Summary({ label, value, accent }: { label: string; value: string; accent?: string }) {
  return (
    <div className="card" style={{ padding: 12, borderTop: `3px solid ${accent ?? 'var(--muted, #888)'}` }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      <div style={{ fontWeight: 700, fontSize: 18, color: accent ?? undefined }}>{value}</div>
    </div>
  );
}
