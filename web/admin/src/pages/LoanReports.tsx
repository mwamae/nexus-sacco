// Phase 6f: Loan reports dashboard.
//
// Tabbed single-page layout:
//   • Portfolio    — totals, by product, by status
//   • Aging        — arrears classification + provisioning + NPL ratio
//   • Maturing     — loans whose final installment falls within N days
//   • Restructured — register filtered by kind
//   • Write-offs   — register + recoveries; write-off action button
//   • CRB          — export-ready JSON record list

import { useEffect, useMemo, useState } from 'react';
import {
  extractError,
  getAgingReport,
  getCRBSubmission,
  getMaturingLoans,
  getPortfolioSummary,
  getRestructuringRegister,
  getWriteoffRegister,
  writeOffLoan,
  type AgingReport,
  type CRBRecord,
  type MaturingLoan,
  type PortfolioSummary,
  type RestructuringRegisterEntry,
  type WriteoffRegisterEntry,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';

type Tab = 'portfolio' | 'aging' | 'maturing' | 'restructured' | 'writeoffs' | 'crb';

const TABS: Array<{ key: Tab; label: string }> = [
  { key: 'portfolio', label: 'Portfolio' },
  { key: 'aging', label: 'Aging' },
  { key: 'maturing', label: 'Maturing' },
  { key: 'restructured', label: 'Restructured' },
  { key: 'writeoffs', label: 'Write-offs' },
  { key: 'crb', label: 'CRB' },
];

export default function LoanReportsPage() {
  const { tenant } = useAuth();
  const [tab, setTab] = useState<Tab>(() => {
    const h = (window.location.hash || '').replace('#', '');
    return (TABS.find((t) => t.key === h)?.key ?? 'portfolio') as Tab;
  });

  function selectTab(t: Tab) {
    setTab(t);
    window.location.hash = t;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <h1>Loan reports</h1>
        <span className="muted tiny">{tenant?.name}</span>
      </div>

      <div className="tabs" style={{ marginBottom: 14 }}>
        {TABS.map((t) => (
          <button
            key={t.key}
            className={`tab ${tab === t.key ? 'active' : ''}`}
            onClick={() => selectTab(t.key)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'portfolio' && <PortfolioTab />}
      {tab === 'aging' && <AgingTab />}
      {tab === 'maturing' && <MaturingTab />}
      {tab === 'restructured' && <RestructuredTab />}
      {tab === 'writeoffs' && <WriteoffsTab />}
      {tab === 'crb' && <CRBTab />}
    </div>
  );
}

// ─────────── Portfolio ───────────

function PortfolioTab() {
  const [data, setData] = useState<PortfolioSummary | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    getPortfolioSummary().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="error">{err}</div>;
  if (!data) return <div className="muted">Loading…</div>;
  const kpis = [
    { label: 'Active loans', value: data.active_loans },
    { label: 'In arrears', value: data.in_arrears_loans },
    { label: 'Restructured', value: data.restructured_loans },
    { label: 'Settled', value: data.settled_loans },
    { label: 'Written off', value: data.written_off_loans },
    { label: 'Lifetime loans', value: data.total_loans_lifetime },
  ];
  return (
    <>
      <div className="kpi-grid" style={{ marginBottom: 14 }}>
        {kpis.map((k) => (
          <div className="card kpi" key={k.label}>
            <div className="muted tiny">{k.label}</div>
            <div className="kpi-value">{k.value}</div>
          </div>
        ))}
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h2>Outstanding balances</h2>
          <span className="card-sub">As of now</span>
        </div>
        <div className="card-body">
          <div className="kpi-grid">
            <KPI label="Total outstanding" value={fmt(data.total_outstanding)} />
            <KPI label="Principal" value={fmt(data.principal_outstanding)} />
            <KPI label="Interest receivable" value={fmt(data.interest_receivable)} />
            <KPI label="Fees receivable" value={fmt(data.fees_receivable)} />
            <KPI label="Penalty receivable" value={fmt(data.penalty_receivable)} />
            <KPI label="Total disbursed (lifetime)" value={fmt(data.total_disbursed_lifetime)} />
          </div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h2>By product</h2>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Product</th>
                <th className="r">Active loans</th>
                <th className="r">Principal outstanding</th>
                <th className="r">Total outstanding</th>
              </tr>
            </thead>
            <tbody>
              {data.by_product.map((r) => (
                <tr key={r.product_id}>
                  <td><span className="mono">{r.product_code}</span> · {r.product_name}</td>
                  <td className="r mono">{r.active_loans}</td>
                  <td className="r mono">{fmt(r.principal_outstanding)}</td>
                  <td className="r mono">{fmt(r.total_outstanding)}</td>
                </tr>
              ))}
              {data.by_product.length === 0 && (
                <tr><td colSpan={4} className="muted center">No products</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h2>By status</h2>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Status</th>
                <th className="r">Loans</th>
                <th className="r">Outstanding</th>
              </tr>
            </thead>
            <tbody>
              {data.by_status.map((r) => (
                <tr key={r.status}>
                  <td>{r.status}</td>
                  <td className="r mono">{r.loan_count}</td>
                  <td className="r mono">{fmt(r.outstanding)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────── Aging ───────────

const BAND_LABEL: Record<string, string> = {
  performing: 'Performing (0 dpd)',
  watch: 'Watch (1–30 dpd)',
  substandard: 'Substandard (31–90 dpd)',
  doubtful: 'Doubtful (91–180 dpd)',
  loss: 'Loss (180+ dpd)',
};

function AgingTab() {
  const [data, setData] = useState<AgingReport | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    getAgingReport().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="error">{err}</div>;
  if (!data) return <div className="muted">Loading…</div>;

  const nplPct = parseFloat(data.npl_ratio_pct);
  return (
    <>
      <div className="kpi-grid" style={{ marginBottom: 14 }}>
        <KPI label="Total loans" value={data.total_loans} />
        <KPI label="Total outstanding" value={fmt(data.total_outstanding)} />
        <KPI label="Total provisioning" value={fmt(data.total_provisioning)} />
        <KPI label="NPL loan count" value={data.npl_loan_count} />
        <KPI label="NPL outstanding" value={fmt(data.npl_outstanding)} />
        <KPI
          label="NPL ratio"
          value={`${data.npl_ratio_pct}%`}
          tone={nplPct >= 10 ? 'danger' : nplPct >= 5 ? 'warning' : undefined}
        />
      </div>

      <div className="card">
        <div className="card-hd">
          <h2>Aging by classification</h2>
          <span className="card-sub">Provisioning rates per tenant policy</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Band</th>
                <th className="r">Loans</th>
                <th className="r">Principal</th>
                <th className="r">Interest</th>
                <th className="r">Outstanding</th>
                <th className="r">Prov. %</th>
                <th className="r">Provisioning</th>
              </tr>
            </thead>
            <tbody>
              {data.bands.map((b) => (
                <tr key={b.classification}>
                  <td>{BAND_LABEL[b.classification] ?? b.classification}</td>
                  <td className="r mono">{b.loan_count}</td>
                  <td className="r mono">{fmt(b.principal_balance)}</td>
                  <td className="r mono">{fmt(b.interest_balance)}</td>
                  <td className="r mono">{fmt(b.total_outstanding)}</td>
                  <td className="r mono">{b.provisioning_pct}%</td>
                  <td className="r mono">{fmt(b.provisioning_amount)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td><strong>Total</strong></td>
                <td className="r mono"><strong>{data.total_loans}</strong></td>
                <td className="r"></td>
                <td className="r"></td>
                <td className="r mono"><strong>{fmt(data.total_outstanding)}</strong></td>
                <td className="r"></td>
                <td className="r mono"><strong>{fmt(data.total_provisioning)}</strong></td>
              </tr>
            </tfoot>
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────── Maturing ───────────

function MaturingTab() {
  const [within, setWithin] = useState(30);
  const [data, setData] = useState<{ within_days: number; items: MaturingLoan[] } | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    let cancelled = false;
    getMaturingLoans(within)
      .then((d) => !cancelled && setData(d))
      .catch((e) => !cancelled && setErr(extractError(e)));
    return () => { cancelled = true; };
  }, [within]);
  if (err) return <div className="error">{err}</div>;
  return (
    <div className="card">
      <div className="card-hd">
        <h2>Loans maturing within</h2>
        <div className="card-hd-actions">
          {[30, 60, 90, 180, 365].map((d) => (
            <button
              key={d}
              className={`btn small ${within === d ? 'active' : ''}`}
              onClick={() => setWithin(d)}
            >{d} days</button>
          ))}
        </div>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th>Loan</th>
              <th>Member</th>
              <th>Product</th>
              <th>Final due</th>
              <th className="r">Days left</th>
              <th className="r">Outstanding</th>
            </tr>
          </thead>
          <tbody>
            {(data?.items ?? []).map((r) => {
              const outstanding = sumBal(r.loan);
              return (
                <tr key={r.loan.id}>
                  <td>
                    <a href={`/loans/${r.loan.id}`} className="mono">{r.loan.loan_no}</a>
                  </td>
                  <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                  <td>{r.product_name}</td>
                  <td className="mono">{r.final_due_date?.slice(0, 10)}</td>
                  <td className="r mono">{r.days_until_final}</td>
                  <td className="r mono">{fmt(outstanding)}</td>
                </tr>
              );
            })}
            {data && data.items.length === 0 && (
              <tr><td colSpan={6} className="muted center">No loans maturing in this window</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Restructured ───────────

const KIND_LABEL: Record<string, string> = {
  reschedule: 'Reschedule',
  moratorium: 'Moratorium',
  settlement_discount: 'Settlement discount',
  topup: 'Top-up',
  refinance: 'Refinance',
};

function RestructuredTab() {
  const [kind, setKind] = useState('');
  const [items, setItems] = useState<RestructuringRegisterEntry[] | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    let cancelled = false;
    getRestructuringRegister(kind)
      .then((d) => !cancelled && setItems(d))
      .catch((e) => !cancelled && setErr(extractError(e)));
    return () => { cancelled = true; };
  }, [kind]);
  if (err) return <div className="error">{err}</div>;
  return (
    <div className="card">
      <div className="card-hd">
        <h2>Restructured loans</h2>
        <div className="card-hd-actions">
          <select className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">All kinds</option>
            {Object.entries(KIND_LABEL).map(([k, v]) => (
              <option key={k} value={k}>{v}</option>
            ))}
          </select>
        </div>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th>Loan</th>
              <th>Member</th>
              <th>Product</th>
              <th>Kind</th>
              <th>Reason</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {(items ?? []).map((r) => (
              <tr key={r.restructuring.id}>
                <td><a href={`/loans/${r.restructuring.loan_id}`} className="mono">{r.loan_no}</a></td>
                <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                <td>{r.product_name}</td>
                <td>{KIND_LABEL[r.restructuring.kind] ?? r.restructuring.kind}</td>
                <td className="truncate" title={r.restructuring.reason}>{r.restructuring.reason}</td>
                <td className="mono">{r.restructuring.created_at?.slice(0, 10)}</td>
              </tr>
            ))}
            {items && items.length === 0 && (
              <tr><td colSpan={6} className="muted center">No restructuring events</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Write-offs ───────────

function WriteoffsTab() {
  const { hasPermission } = useAuth();
  const [items, setItems] = useState<WriteoffRegisterEntry[] | null>(null);
  const [err, setErr] = useState('');
  const [showAction, setShowAction] = useState(false);
  function load() {
    getWriteoffRegister().then(setItems).catch((e) => setErr(extractError(e)));
  }
  useEffect(() => { load(); }, []);
  if (err) return <div className="error">{err}</div>;
  const total = useMemo(() => {
    return (items ?? []).reduce((acc, r) => acc + parseFloat(r.writeoff.total_written_off || '0'), 0);
  }, [items]);
  const recovered = useMemo(() => {
    return (items ?? []).reduce((acc, r) => acc + parseFloat(r.recovered_amount || '0'), 0);
  }, [items]);
  return (
    <>
      <div className="kpi-grid" style={{ marginBottom: 14 }}>
        <KPI label="Loans written off" value={items?.length ?? '…'} />
        <KPI label="Total written off" value={fmt(total)} />
        <KPI label="Recovered to date" value={fmt(recovered)} />
      </div>

      <div className="card">
        <div className="card-hd">
          <h2>Write-off register</h2>
          {hasPermission('loans:writeoff') && (
            <div className="card-hd-actions">
              <button className="btn primary small" onClick={() => setShowAction(true)}>
                + Write off a loan
              </button>
            </div>
          )}
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Loan</th>
                <th>Member</th>
                <th className="r">Principal</th>
                <th className="r">Interest</th>
                <th className="r">Fees</th>
                <th className="r">Total</th>
                <th className="r">Recovered</th>
                <th>Reason</th>
                <th>Authorized</th>
              </tr>
            </thead>
            <tbody>
              {(items ?? []).map((r) => (
                <tr key={r.writeoff.id}>
                  <td><a href={`/loans/${r.writeoff.loan_id}`} className="mono">{r.loan_no}</a></td>
                  <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                  <td className="r mono">{fmt(r.writeoff.principal_written_off)}</td>
                  <td className="r mono">{fmt(r.writeoff.interest_written_off)}</td>
                  <td className="r mono">{fmt(r.writeoff.fees_written_off)}</td>
                  <td className="r mono"><strong>{fmt(r.writeoff.total_written_off)}</strong></td>
                  <td className="r mono">{fmt(r.recovered_amount)}</td>
                  <td className="truncate" title={r.writeoff.reason}>{r.writeoff.reason}</td>
                  <td className="mono">{r.writeoff.authorized_at?.slice(0, 10)}</td>
                </tr>
              ))}
              {items && items.length === 0 && (
                <tr><td colSpan={9} className="muted center">No write-offs on file</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {showAction && (
        <WriteoffModal
          onClose={() => setShowAction(false)}
          onSuccess={() => { setShowAction(false); load(); }}
        />
      )}
    </>
  );
}

function WriteoffModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const [loanId, setLoanId] = useState('');
  const [reason, setReason] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!loanId || !reason) return;
    setBusy(true); setErr('');
    try {
      const r = await writeOffLoan(loanId.trim(), reason.trim());
      if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
      onSuccess();
    } catch (ex) {
      setErr(extractError(ex));
    } finally { setBusy(false); }
  }
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Write off a loan</h2>
        <p className="muted tiny">
          Posts a write-off ledger entry, zeros all loan balances, marks the loan written_off, and
          records an audit row. Cannot be reversed. Find the loan ID on the loan detail page.
        </p>
        <form onSubmit={submit} className="form">
          <label>
            <span>Loan ID (UUID)</span>
            <input className="input mono" value={loanId} onChange={(e) => setLoanId(e.target.value)} required />
          </label>
          <label>
            <span>Reason</span>
            <textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} required
              placeholder="e.g. unrecoverable per board resolution YYYY-MM-DD"
            />
          </label>
          {err && <div className="error">{err}</div>}
          <div className="form-actions">
            <button type="button" className="btn" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn danger" disabled={busy}>
              {busy ? 'Writing off…' : 'Write off'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ─────────── CRB ───────────

function CRBTab() {
  const [data, setData] = useState<{ records: CRBRecord[]; record_count: number } | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    getCRBSubmission().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  function downloadJSON() {
    if (!data) return;
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `crb-submission-${new Date().toISOString().slice(0, 10)}.json`;
    a.click();
    URL.revokeObjectURL(url);
  }
  if (err) return <div className="error">{err}</div>;
  if (!data) return <div className="muted">Loading…</div>;
  return (
    <div className="card">
      <div className="card-hd">
        <h2>CRB submission</h2>
        <div className="card-hd-actions">
          <span className="muted tiny" style={{ marginRight: 12 }}>{data.record_count} records</span>
          <button className="btn primary small" onClick={downloadJSON}>Download JSON</button>
        </div>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th>Loan</th>
              <th>Member</th>
              <th>ID number</th>
              <th>Disbursed</th>
              <th className="r">Principal</th>
              <th className="r">Outstanding</th>
              <th className="r">DPD</th>
              <th>Classification</th>
              <th>NPL?</th>
            </tr>
          </thead>
          <tbody>
            {data.records.map((r) => (
              <tr key={r.loan_no}>
                <td className="mono">{r.loan_no}</td>
                <td>{r.member_name}</td>
                <td className="mono">{r.id_doc_number}</td>
                <td className="mono">{r.disbursed_at?.slice(0, 10)}</td>
                <td className="r mono">{fmt(r.principal_disbursed)}</td>
                <td className="r mono">{fmt(r.outstanding_balance)}</td>
                <td className="r mono">{r.days_past_due}</td>
                <td>{r.classification}</td>
                <td>{r.is_npl ? 'Yes' : 'No'}</td>
              </tr>
            ))}
            {data.records.length === 0 && (
              <tr><td colSpan={9} className="muted center">No records to submit</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Helpers ───────────

function KPI({ label, value, tone }: { label: string; value: string | number; tone?: 'warning' | 'danger' }) {
  const cls = tone ? `card kpi ${tone}` : 'card kpi';
  return (
    <div className={cls}>
      <div className="muted tiny">{label}</div>
      <div className="kpi-value mono">{value}</div>
    </div>
  );
}

function sumBal(l: { principal_balance: string; interest_balance: string; fees_balance: string; penalty_balance: string }) {
  return (parseFloat(l.principal_balance) + parseFloat(l.interest_balance) + parseFloat(l.fees_balance) + parseFloat(l.penalty_balance)).toFixed(2);
}

function fmt(s: string | number | undefined | null): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
