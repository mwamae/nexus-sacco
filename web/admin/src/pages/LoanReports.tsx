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
import { Tabs } from '../components/Tabs';

type Tab = 'portfolio' | 'aging' | 'maturing' | 'restructured' | 'writeoffs' | 'crb';

const TABS: Array<{ id: Tab; label: string; hint: string }> = [
  { id: 'portfolio',    label: 'Portfolio',    hint: 'Live totals, breakdowns by product and status.' },
  { id: 'aging',        label: 'Aging',        hint: 'Arrears classification, provisioning per tenant policy, and NPL ratio.' },
  { id: 'maturing',     label: 'Maturing',     hint: 'Loans whose final installment falls within the selected window.' },
  { id: 'restructured', label: 'Restructured', hint: 'Register of rescheduled / moratorium / settlement-discount events.' },
  { id: 'writeoffs',    label: 'Write-offs',   hint: 'Board-authorised write-offs and any subsequent recoveries.' },
  { id: 'crb',          label: 'CRB',          hint: 'Export the JSON submission file for credit-reference bureaus.' },
];

export default function LoanReportsPage() {
  const { tenant } = useAuth();
  const [tab, setTab] = useState<Tab>(() => {
    const h = (window.location.hash || '').replace('#', '');
    return (TABS.find((t) => t.id === h)?.id ?? 'portfolio') as Tab;
  });

  function selectTab(t: Tab) {
    setTab(t);
    window.location.hash = t;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Reporting</div>
          <h1>Loan reports</h1>
          <div className="page-sub">Portfolio totals, aging with provisioning, write-offs, and CRB submission.</div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <Tabs ariaLabel="Loan report sections" tabs={TABS} value={tab} onChange={selectTab}>
          {(activeId) => (
            <>
              <p className="muted tiny" style={{ margin: '0 0 14px' }}>{TABS.find((t) => t.id === activeId)?.hint}</p>
              {activeId === 'portfolio'    && <PortfolioTab />}
              {activeId === 'aging'        && <AgingTab />}
              {activeId === 'maturing'     && <MaturingTab />}
              {activeId === 'restructured' && <RestructuredTab />}
              {activeId === 'writeoffs'    && <WriteoffsTab />}
              {activeId === 'crb'          && <CRBTab />}
            </>
          )}
        </Tabs>
      </div>
    </div>
  );
}

// ─────────── Portfolio ───────────

function PortfolioTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<PortfolioSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getPortfolioSummary().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  return (
    <>
      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Active loans"     value={String(data.active_loans)} />
        <KPI label="In arrears"       value={String(data.in_arrears_loans)} tone={data.in_arrears_loans > 0 ? 'warn' : undefined} />
        <KPI label="Restructured"     value={String(data.restructured_loans)} />
        <KPI label="Lifetime loans"   value={String(data.total_loans_lifetime)} sub={`${data.settled_loans} settled · ${data.written_off_loans} written off`} />
      </div>

      <div className="grid-3" style={{ marginBottom: 14 }}>
        <KPI label="Total outstanding"     value={`${currency} ${fmt(data.total_outstanding)}`} />
        <KPI label="Principal outstanding" value={`${currency} ${fmt(data.principal_outstanding)}`} />
        <KPI label="Interest receivable"   value={`${currency} ${fmt(data.interest_receivable)}`} />
      </div>
      <div className="grid-3" style={{ marginBottom: 14 }}>
        <KPI label="Fees receivable"      value={`${currency} ${fmt(data.fees_receivable)}`} />
        <KPI label="Penalty receivable"   value={`${currency} ${fmt(data.penalty_receivable)}`} />
        <KPI label="Lifetime disbursed"   value={`${currency} ${fmt(data.total_disbursed_lifetime)}`} />
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>By product</h3>
          <span className="card-sub">{(data.by_product ?? []).length} products configured</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Product</th>
                <th className="num">Active loans</th>
                <th className="num">Principal outstanding</th>
                <th className="num">Total outstanding</th>
              </tr>
            </thead>
            <tbody>
              {(data.by_product ?? []).map((r) => (
                <tr key={r.product_id}>
                  <td><span className="mono">{r.product_code}</span> · {r.product_name}</td>
                  <td className="mono">{r.active_loans}</td>
                  <td className="mono">{fmt(r.principal_outstanding)}</td>
                  <td className="mono">{fmt(r.total_outstanding)}</td>
                </tr>
              ))}
              {(data.by_product ?? []).length === 0 && (
                <tr><td colSpan={4} className="al-c muted">No products configured.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>By status</h3>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Status</th>
                <th className="num">Loans</th>
                <th className="num">Outstanding</th>
              </tr>
            </thead>
            <tbody>
              {(data.by_status ?? []).map((r) => (
                <tr key={r.status}>
                  <td>{r.status.replace('_', ' ')}</td>
                  <td className="mono">{r.loan_count}</td>
                  <td className="mono">{fmt(r.outstanding)}</td>
                </tr>
              ))}
              {(data.by_status ?? []).length === 0 && (
                <tr><td colSpan={3} className="al-c muted">No loans on file yet.</td></tr>
              )}
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
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<AgingReport | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getAgingReport().then(setData).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;

  const bands = data.bands ?? [];
  const nplPct = parseFloat(data.npl_ratio_pct);
  const nplTone: 'warn' | 'neg' | undefined = nplPct >= 10 ? 'neg' : nplPct >= 5 ? 'warn' : undefined;

  return (
    <>
      <div className="grid-3" style={{ marginBottom: 14 }}>
        <KPI label="Total loans"        value={String(data.total_loans)} />
        <KPI label="Total outstanding"  value={`${currency} ${fmt(data.total_outstanding)}`} />
        <KPI label="Total provisioning" value={`${currency} ${fmt(data.total_provisioning)}`} />
      </div>
      <div className="grid-3" style={{ marginBottom: 14 }}>
        <KPI label="NPL loans"       value={String(data.npl_loan_count)} tone={data.npl_loan_count > 0 ? 'warn' : undefined} />
        <KPI label="NPL outstanding" value={`${currency} ${fmt(data.npl_outstanding)}`} />
        <KPI label="NPL ratio"       value={`${data.npl_ratio_pct}%`} tone={nplTone} sub="substandard + doubtful + loss / total" />
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Aging by classification</h3>
          <span className="card-sub">Provisioning rates per tenant policy (CBK defaults: 1 / 25 / 50 / 100%)</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Band</th>
                <th className="num">Loans</th>
                <th className="num">Principal</th>
                <th className="num">Interest</th>
                <th className="num">Outstanding</th>
                <th className="num">Prov. %</th>
                <th className="num">Provisioning</th>
              </tr>
            </thead>
            <tbody>
              {bands.length === 0 && (
                <tr><td colSpan={7} className="al-c muted">No active, in-arrears, or restructured loans to age.</td></tr>
              )}
              {bands.map((b) => (
                <tr key={b.classification}>
                  <td>{BAND_LABEL[b.classification] ?? b.classification}</td>
                  <td className="mono">{b.loan_count}</td>
                  <td className="mono">{fmt(b.principal_balance)}</td>
                  <td className="mono">{fmt(b.interest_balance)}</td>
                  <td className="mono">{fmt(b.total_outstanding)}</td>
                  <td className="mono">{b.provisioning_pct}%</td>
                  <td className="mono">{fmt(b.provisioning_amount)}</td>
                </tr>
              ))}
            </tbody>
            {bands.length > 0 && (
              <tfoot>
                <tr>
                  <td><strong>Total</strong></td>
                  <td className="mono"><strong>{data.total_loans}</strong></td>
                  <td></td>
                  <td></td>
                  <td className="mono"><strong>{fmt(data.total_outstanding)}</strong></td>
                  <td></td>
                  <td className="mono"><strong>{fmt(data.total_provisioning)}</strong></td>
                </tr>
              </tfoot>
            )}
          </table>
        </div>
      </div>
    </>
  );
}

// ─────────── Maturing ───────────

function MaturingTab() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [within, setWithin] = useState(30);
  const [data, setData] = useState<{ within_days: number; items: MaturingLoan[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    getMaturingLoans(within)
      .then((d) => !cancelled && setData(d))
      .catch((e) => !cancelled && setErr(extractError(e)));
    return () => { cancelled = true; };
  }, [within]);
  if (err) return <div className="alert alert-error">{err}</div>;
  const items = data?.items ?? [];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Loans maturing within {within} days</h3>
        <div className="card-hd-actions">
          {[30, 60, 90, 180, 365].map((d) => (
            <button
              key={d}
              className="btn btn-sm"
              data-active={within === d || undefined}
              onClick={() => setWithin(d)}
            >{d}d</button>
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
              <th className="num">Days left</th>
              <th className="num">Outstanding</th>
            </tr>
          </thead>
          <tbody>
            {!data && (
              <tr><td colSpan={6} className="al-c muted">Loading…</td></tr>
            )}
            {data && items.length === 0 && (
              <tr><td colSpan={6} className="al-c muted">No loans maturing in this window.</td></tr>
            )}
            {items.map((r) => {
              const outstanding = sumBal(r.loan);
              return (
                <tr key={r.loan.id}>
                  <td><a href={`/loans/${r.loan.id}`} className="tbl-link mono">{r.loan.loan_no}</a></td>
                  <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                  <td>{r.product_name}</td>
                  <td className="mono">{r.final_due_date?.slice(0, 10)}</td>
                  <td className="mono">{r.days_until_final}</td>
                  <td className="mono">{currency} {fmt(outstanding)}</td>
                </tr>
              );
            })}
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
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    getRestructuringRegister(kind)
      .then((d) => !cancelled && setItems(d))
      .catch((e) => !cancelled && setErr(extractError(e)));
    return () => { cancelled = true; };
  }, [kind]);
  if (err) return <div className="alert alert-error">{err}</div>;
  const rows = items ?? [];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Restructured loans</h3>
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
            {items === null && (
              <tr><td colSpan={6} className="al-c muted">Loading…</td></tr>
            )}
            {items !== null && rows.length === 0 && (
              <tr><td colSpan={6} className="al-c muted">No restructuring events recorded.</td></tr>
            )}
            {rows.map((r) => (
              <tr key={r.restructuring.id}>
                <td><a href={`/loans/${r.restructuring.loan_id}`} className="tbl-link mono">{r.loan_no}</a></td>
                <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                <td>{r.product_name}</td>
                <td>{KIND_LABEL[r.restructuring.kind] ?? r.restructuring.kind}</td>
                <td className="truncate" title={r.restructuring.reason}>{r.restructuring.reason}</td>
                <td className="mono">{r.restructuring.created_at?.slice(0, 10)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Write-offs ───────────

function WriteoffsTab() {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [items, setItems] = useState<WriteoffRegisterEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showAction, setShowAction] = useState(false);

  function load() {
    getWriteoffRegister().then(setItems).catch((e) => setErr(extractError(e)));
  }
  useEffect(() => { load(); }, []);

  const rows = items ?? [];
  const total = useMemo(
    () => rows.reduce((acc, r) => acc + parseFloat(r.writeoff.total_written_off || '0'), 0),
    [rows],
  );
  const recovered = useMemo(
    () => rows.reduce((acc, r) => acc + parseFloat(r.recovered_amount || '0'), 0),
    [rows],
  );

  if (err) return <div className="alert alert-error">{err}</div>;

  return (
    <>
      <div className="grid-3" style={{ marginBottom: 14 }}>
        <KPI label="Loans written off"   value={items === null ? '…' : String(rows.length)} />
        <KPI label="Total written off"   value={`${currency} ${fmt(total)}`} />
        <KPI label="Recovered to date"   value={`${currency} ${fmt(recovered)}`} tone={recovered > 0 ? 'pos' : undefined} />
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Write-off register</h3>
          {hasPermission('loans:writeoff') && (
            <div className="card-hd-actions">
              <button className="btn btn-sm btn-accent" onClick={() => setShowAction(true)}>
                Write off a loan
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
                <th className="num">Principal</th>
                <th className="num">Interest</th>
                <th className="num">Fees</th>
                <th className="num">Total</th>
                <th className="num">Recovered</th>
                <th>Reason</th>
                <th>Authorized</th>
              </tr>
            </thead>
            <tbody>
              {items === null && (
                <tr><td colSpan={9} className="al-c muted">Loading…</td></tr>
              )}
              {items !== null && rows.length === 0 && (
                <tr><td colSpan={9} className="al-c muted">No write-offs on file.</td></tr>
              )}
              {rows.map((r) => (
                <tr key={r.writeoff.id}>
                  <td><a href={`/loans/${r.writeoff.loan_id}`} className="tbl-link mono">{r.loan_no}</a></td>
                  <td><span className="mono">{r.member_no}</span> · {r.member_name}</td>
                  <td className="mono">{fmt(r.writeoff.principal_written_off)}</td>
                  <td className="mono">{fmt(r.writeoff.interest_written_off)}</td>
                  <td className="mono">{fmt(r.writeoff.fees_written_off)}</td>
                  <td className="mono"><strong>{fmt(r.writeoff.total_written_off)}</strong></td>
                  <td className="mono">{fmt(r.recovered_amount)}</td>
                  <td className="truncate" title={r.writeoff.reason}>{r.writeoff.reason}</td>
                  <td className="mono">{r.writeoff.authorized_at?.slice(0, 10)}</td>
                </tr>
              ))}
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
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!loanId || !reason) return;
    setBusy(true); setErr(null);
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
        <h3 style={{ marginTop: 0 }}>Write off a loan</h3>
        <p className="muted tiny">
          Posts a write-off ledger entry, zeros all loan balances, marks the loan written_off, and
          records an audit row. Cannot be reversed. Find the loan ID on the loan detail page.
        </p>
        <form onSubmit={submit}>
          <div className="field">
            <label>Loan ID (UUID)</label>
            <input className="input mono" value={loanId} onChange={(e) => setLoanId(e.target.value)} required />
          </div>
          <div className="field">
            <label>Reason</label>
            <textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} required
              placeholder="e.g. unrecoverable per board resolution YYYY-MM-DD"
            />
          </div>
          {err && <div className="alert alert-error">{err}</div>}
          <div className="row" style={{ gap: 6, justifyContent: 'flex-end', marginTop: 12 }}>
            <button type="button" className="btn btn-sm" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy}>
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
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [data, setData] = useState<{ records: CRBRecord[]; record_count: number } | null>(null);
  const [err, setErr] = useState<string | null>(null);
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
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  const records = data.records ?? [];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>CRB submission</h3>
        <div className="card-hd-actions">
          <span className="muted tiny" style={{ marginRight: 12 }}>{data.record_count} records</span>
          <button className="btn btn-sm btn-accent" onClick={downloadJSON} disabled={records.length === 0}>
            Download JSON
          </button>
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
              <th className="num">Principal</th>
              <th className="num">Outstanding</th>
              <th className="num">DPD</th>
              <th>Classification</th>
              <th>NPL?</th>
            </tr>
          </thead>
          <tbody>
            {records.length === 0 && (
              <tr><td colSpan={9} className="al-c muted">No records to submit.</td></tr>
            )}
            {records.map((r) => (
              <tr key={r.loan_no}>
                <td className="mono">{r.loan_no}</td>
                <td>{r.member_name}</td>
                <td className="mono">{r.id_doc_number}</td>
                <td className="mono">{r.disbursed_at?.slice(0, 10)}</td>
                <td className="mono">{currency} {fmt(r.principal_disbursed)}</td>
                <td className="mono">{currency} {fmt(r.outstanding_balance)}</td>
                <td className="mono">{r.days_past_due}</td>
                <td>{r.classification}</td>
                <td>{r.is_npl ? 'Yes' : 'No'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Helpers ───────────

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color, fontSize: 18 }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
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
