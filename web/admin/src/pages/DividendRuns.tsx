// Dividend runs — list, create, drill-into-detail.
//
// Structurally mirrors InterestRuns.tsx but with a calc-method picker
// in the new-run wizard and share-basis columns in the per-line table.

import { useEffect, useState, type ReactNode } from 'react';
import {
  approveDividendRun,
  cancelDividendRun,
  computeDividendRun,
  createDividendRun,
  extractError,
  getDividendRun,
  listDividendRuns,
  lockDividendRun,
  postDividendRun,
  submitDividendRun,
  updateDividendLine,
  type DividendCalcMethod,
  type DividendRun,
  type DividendRunDetail,
  type DividendRunLine,
  type DividendRunStatus,
  type InterestPayoutMethod,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

const STATUS_LABEL: Record<DividendRunStatus, string> = {
  draft: 'Draft',
  computing: 'Computing',
  preview: 'Preview',
  approved: 'Approved',
  posting: 'Posting',
  posted: 'Posted',
  locked: 'Locked',
  cancelled: 'Cancelled',
};

const CALC_METHOD_LABEL: Record<DividendCalcMethod, string> = {
  closing_balance: 'Closing balance',
  average_monthly: 'Average monthly',
  pro_rated: 'Pro-rated',
};

const CALC_METHOD_DESC: Record<DividendCalcMethod, string> = {
  closing_balance: 'Share balance at the close of the financial year.',
  average_monthly: 'Average of the twelve month-end balances over the FY.',
  pro_rated: 'Closing balance × (days held / days in FY) — handles mid-year openings/exits.',
};

export default function DividendRunsPage() {
  const path = window.location.pathname;
  const detailMatch = /^\/dividend-runs\/([0-9a-f-]{36})/.exec(path);
  if (detailMatch) return <RunDetail runId={detailMatch[1]} />;
  return <RunList />;
}

// ─────────── List ───────────

function RunList() {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canRun = hasPermission('dividends:run');

  const [items, setItems] = useState<DividendRun[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [status, setStatus] = useState('');
  const [fy, setFy] = useState('');
  const [openNew, setOpenNew] = useState(false);

  async function reload() {
    setErr(null);
    try {
      const r = await listDividendRuns({ status: status || undefined, fy: fy || undefined, limit: 100 });
      setItems(r.items); setTotal(r.total);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status, fy]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">FY Accounting · Equity</div>
          <h1>Dividend runs</h1>
          <div className="page-sub">AGM-declared dividend on share holdings.</div>
        </div>
        <div className="page-hd-actions">
          {canRun && <button className="btn btn-sm btn-accent" onClick={() => setOpenNew(true)}><Icon name="plus" size={12} /> New run</button>}
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-hd">
          <h3>Runs</h3>
          <span className="card-sub">{total} total</span>
          <div className="card-hd-actions">
            <form onSubmit={(e) => { e.preventDefault(); void reload(); }} style={{ display: 'flex', gap: 4 }}>
              <input className="input" style={{ height: 26, fontSize: 12, width: 180 }} placeholder="FY label search" value={fy} onChange={(e) => setFy(e.target.value)} />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
            <select className="input" style={{ height: 26, fontSize: 12 }} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {(['draft', 'preview', 'approved', 'posted', 'locked', 'cancelled'] as DividendRunStatus[]).map((s) => (
                <option key={s} value={s}>{STATUS_LABEL[s]}</option>
              ))}
            </select>
          </div>
        </div>
        <div className="card-body flush">
          {items.length === 0 ? (
            <div className="empty">
              No dividend runs yet.
              {canRun && <> <a style={{ color: 'var(--accent)' }} onClick={() => setOpenNew(true)}>Create one →</a></>}
            </div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Run #</th>
                  <th>Financial year</th>
                  <th>Status</th>
                  <th>Method</th>
                  <th style={{ textAlign: 'right' }}>Rate</th>
                  <th style={{ textAlign: 'right' }}>Members</th>
                  <th style={{ textAlign: 'right' }}>Gross</th>
                  <th style={{ textAlign: 'right' }}>WHT</th>
                  <th style={{ textAlign: 'right' }}>Net</th>
                  <th>AGM ref</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {items.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny-mono"><a className="tbl-link" href={`/dividend-runs/${r.id}`}>{r.run_no}</a></td>
                    <td>
                      <div>{r.financial_year_label}</div>
                      <div className="muted tiny">{r.fy_start.slice(0, 10)} → {r.fy_end.slice(0, 10)}</div>
                    </td>
                    <td><StatusBadge status={r.status} /></td>
                    <td><Badge tone="accent">{CALC_METHOD_LABEL[r.calc_method]}</Badge></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.agm_rate_pct}%</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.member_count ?? '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.total_gross_dividend ? `${currency} ${fmt(r.total_gross_dividend)}` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right', color: 'var(--neg)' }}>{r.total_wht ? `${currency} ${fmt(r.total_wht)}` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.total_net_dividend ? `${currency} ${fmt(r.total_net_dividend)}` : '—'}</td>
                    <td className="tiny-mono">{r.agm_resolution_ref}</td>
                    <td><a className="btn btn-sm" href={`/dividend-runs/${r.id}`}><Icon name="eye" size={12} /></a></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {openNew && <NewRunModal onClose={() => setOpenNew(false)} onCreated={() => setOpenNew(false)} />}
    </div>
  );
}

// ─────────── New-run modal ───────────

function NewRunModal({ onClose, onCreated }: { onClose: () => void; onCreated: (r: DividendRun) => void }) {
  const today = new Date().toISOString().slice(0, 10);
  const yearStart = `${new Date().getFullYear() - 1}-01-01`;
  const yearEnd = `${new Date().getFullYear() - 1}-12-31`;
  const [fyStart, setFyStart] = useState(yearStart);
  const [fyEnd, setFyEnd] = useState(yearEnd);
  const [method, setMethod] = useState<DividendCalcMethod>('closing_balance');
  const [rate, setRate] = useState('10.0');
  const [agmRef, setAgmRef] = useState('');
  const [agmDate, setAgmDate] = useState(today);
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setErr(null);
    if (!agmRef.trim() || !rate) {
      setErr('AGM resolution reference and rate are required.');
      return;
    }
    setBusy(true);
    try {
      const r = await createDividendRun({
        fy_start: fyStart, fy_end: fyEnd,
        calc_method: method,
        agm_rate_pct: rate,
        agm_resolution_ref: agmRef,
        agm_resolution_date: agmDate,
        notes: notes || undefined,
      });
      window.location.href = `/dividend-runs/${r.id}`;
      onCreated(r);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <ModalShell title="New dividend run" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Create draft"
      disabled={!agmRef.trim() || !rate}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        <strong>AGM gate.</strong> Capture the AGM resolution before computing — the system blocks any progression without it.
      </div>
      <div className="grid-2">
        <Field label="FY start"><input className="input mono" type="date" value={fyStart} onChange={(e) => setFyStart(e.target.value)} /></Field>
        <Field label="FY end (inclusive)"><input className="input mono" type="date" value={fyEnd} onChange={(e) => setFyEnd(e.target.value)} /></Field>
      </div>
      <Field label="Calculation method" hint={CALC_METHOD_DESC[method]}>
        <select className="input" value={method} onChange={(e) => setMethod(e.target.value as DividendCalcMethod)}>
          <option value="closing_balance">Closing balance</option>
          <option value="average_monthly">Average monthly balance</option>
          <option value="pro_rated">Pro-rated (days held / days in FY)</option>
        </select>
      </Field>
      <div className="grid-2">
        <Field label="AGM-approved rate (% on capital basis)"><input className="input mono" value={rate} onChange={(e) => setRate(e.target.value)} placeholder="10.0" /></Field>
        <Field label="AGM resolution date"><input className="input mono" type="date" value={agmDate} onChange={(e) => setAgmDate(e.target.value)} /></Field>
      </div>
      <Field label="AGM resolution reference"><input className="input" value={agmRef} onChange={(e) => setAgmRef(e.target.value)} placeholder="AGM/2026/RES-014" /></Field>
      <Field label="Notes (optional)"><textarea className="input" rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} /></Field>
    </ModalShell>
  );
}

// ─────────── Detail ───────────

function RunDetail({ runId }: { runId: string }) {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canRun = hasPermission('dividends:run');
  const canApprove = hasPermission('dividends:approve');

  const [data, setData] = useState<DividendRunDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try { setData(await getDividendRun(runId)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [runId]);

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!data) return <div className="page"><div className="empty">Loading…</div></div>;

  const r = data.run;
  const lines = data.lines;
  const stat = r.status;

  async function run(action: string, fn: () => Promise<unknown>) {
    setBusy(action);
    try { await fn(); await load(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/dividend-runs" style={{ color: 'inherit' }}>Dividend runs</a> · {r.run_no}</div>
          <h1>{r.financial_year_label}</h1>
          <div className="page-sub">
            FY {r.fy_start.slice(0, 10)} → {r.fy_end.slice(0, 10)} ·
            <Badge tone="accent">{CALC_METHOD_LABEL[r.calc_method]}</Badge> ·
            Rate <strong>{r.agm_rate_pct}%</strong> · WHT <strong>{r.wht_rate_pct}%</strong>
          </div>
        </div>
        <div className="page-hd-actions"><StatusBadge status={r.status} /></div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>AGM authorisation</h3>
          <span className="card-sub">Required for every transition</span>
        </div>
        <div className="card-body">
          <div className="grid-3">
            <KV label="Resolution ref" value={<strong className="tiny-mono">{r.agm_resolution_ref}</strong>} />
            <KV label="Resolution date" value={<span className="mono">{r.agm_resolution_date.slice(0, 10)}</span>} />
            <KV label="Rate applied" value={<span className="mono">{r.agm_rate_pct}% on capital basis</span>} />
          </div>
        </div>
      </div>

      {r.member_count != null && (
        <div className="grid-4" style={{ marginBottom: 14 }}>
          <KPI label="Members" value={String(r.member_count)} />
          <KPI label="Total share basis" value={fmt(r.total_share_basis ?? '0')} sub="shares" />
          <KPI label="Gross dividend" value={`${currency} ${fmt(r.total_gross_dividend ?? '0')}`} />
          <KPI label="Net (after WHT)" value={`${currency} ${fmt(r.total_net_dividend ?? '0')}`} sub={`WHT ${currency} ${fmt(r.total_wht ?? '0')}`} />
        </div>
      )}

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Actions</h3>
          <span className="card-sub">Lifecycle controls — gated by status</span>
        </div>
        <div className="card-body">
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {canRun && (stat === 'draft' || stat === 'preview') && (
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('compute', () => computeDividendRun(runId))}>
                {busy === 'compute' ? 'Computing…' : 'Compute preview'}
              </button>
            )}
            {canRun && stat === 'preview' && !r.workflow_instance_id && (
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('submit', () => submitDividendRun(runId))}>
                {busy === 'submit' ? 'Submitting…' : 'Submit for workflow approval'}
              </button>
            )}
            {canApprove && stat === 'preview' && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('approve', async () => {
                if (!confirm('Approve this dividend run directly (bypass workflow)?')) return;
                await approveDividendRun(runId, 'Direct approval');
              })}>
                {busy === 'approve' ? 'Approving…' : 'Approve directly'}
              </button>
            )}
            {canApprove && stat === 'approved' && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('post', async () => {
                if (!confirm(`Post ${lines.length} line${lines.length === 1 ? '' : 's'}? This credits member accounts (or buys shares) and writes WHT entries.`)) return;
                await postDividendRun(runId);
              })}>
                {busy === 'post' ? 'Posting…' : `Post all (${lines.length})`}
              </button>
            )}
            {canApprove && stat === 'posted' && (
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('lock', async () => {
                if (!confirm('Lock this run? No further changes possible without a formal adjustment.')) return;
                await lockDividendRun(runId);
              })}>
                {busy === 'lock' ? 'Locking…' : 'Lock run'}
              </button>
            )}
            {canRun && stat !== 'posted' && stat !== 'locked' && stat !== 'cancelled' && (
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={!!busy} onClick={() => run('cancel', async () => {
                const reason = prompt('Cancellation reason?'); if (!reason) return;
                await cancelDividendRun(runId, reason);
              })}>
                Cancel run
              </button>
            )}
          </div>
          {r.workflow_instance_id && (
            <p className="muted tiny" style={{ marginTop: 8 }}>
              Workflow instance: <a className="tbl-link" href={`/approvals/${r.workflow_instance_id}`}>{r.workflow_instance_id.slice(0, 8)}…</a>
            </p>
          )}
          {r.cancellation_reason && (
            <p className="muted tiny" style={{ marginTop: 8 }}><strong>Cancelled:</strong> {r.cancellation_reason}</p>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Member lines</h3>
          <span className="card-sub">{lines.length} {lines.length === 1 ? 'line' : 'lines'}</span>
          <div className="card-hd-actions">
            <button className="btn btn-sm" onClick={() => downloadLinesCSV(r, lines, currency)}><Icon name="more" size={12} /> CSV</button>
          </div>
        </div>
        <div className="card-body flush">
          {lines.length === 0 ? (
            <div className="empty">{stat === 'draft' ? 'No preview yet — compute to generate lines.' : 'No member lines.'}</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Member</th>
                  <th style={{ textAlign: 'right' }}>Share basis</th>
                  <th style={{ textAlign: 'right' }}>Capital basis</th>
                  <th style={{ textAlign: 'right' }}>Gross</th>
                  <th style={{ textAlign: 'right' }}>WHT</th>
                  <th style={{ textAlign: 'right' }}>Net</th>
                  <th>Payout</th>
                  <th>Posted</th>
                </tr>
              </thead>
              <tbody>
                {lines.map((l) => (
                  <LineRow key={l.id} line={l} runStatus={stat} currency={currency} onReload={load} />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function LineRow({ line, runStatus, currency, onReload }: {
  line: DividendRunLine; runStatus: DividendRunStatus; currency: string; onReload: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const editable = (runStatus === 'preview' || runStatus === 'draft') && !line.posted_at;
  return (
    <tr>
      <td>
        <a className="tbl-link" href={`/members/${line.member_id}?tab=accounts`}>{line.member_id.slice(0, 8)}…</a>
        {line.days_held_in_fy != null && <div className="muted tiny">days held: {line.days_held_in_fy}/{line.days_in_fy}</div>}
      </td>
      <td className="mono" style={{ textAlign: 'right' }}>{fmt(line.shares_basis)}</td>
      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(line.capital_basis)}</td>
      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(line.gross_dividend)}</td>
      <td className="mono" style={{ textAlign: 'right', color: 'var(--neg)' }}>{currency} {fmt(line.wht_amount)}</td>
      <td className="mono" style={{ textAlign: 'right', color: 'var(--pos)' }}><strong>{currency} {fmt(line.net_dividend)}</strong></td>
      <td>
        <Badge tone={line.payout_method === 'credit_savings' ? 'pos' : line.payout_method === 'buy_shares' ? 'accent' : 'info'}>
          {line.payout_method.replace('_', ' ')}
        </Badge>
        {editable && <button className="btn btn-sm" style={{ marginLeft: 4 }} onClick={() => setEditing(true)}>Edit</button>}
      </td>
      <td className="tiny-mono">{line.posted_at ? line.posted_at.slice(0, 10) : '—'}</td>
      {editing && <PayoutEditModal line={line} onClose={() => setEditing(false)} onSaved={async () => { setEditing(false); await onReload(); }} />}
    </tr>
  );
}

function PayoutEditModal({ line, onClose, onSaved }: { line: DividendRunLine; onClose: () => void; onSaved: () => Promise<void> }) {
  const [method, setMethod] = useState<InterestPayoutMethod>(line.payout_method);
  const [targetAcct, setTargetAcct] = useState(line.payout_target_account_id ?? '');
  const [extChannel, setExtChannel] = useState(line.payout_external_channel ?? 'mpesa');
  const [extRef, setExtRef] = useState(line.payout_external_ref ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title="Edit payout method" busy={busy} onClose={onClose} submitLabel="Save"
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          await updateDividendLine(line.id, {
            payout_method: method,
            payout_target_account_id: method === 'credit_savings' && targetAcct ? targetAcct : undefined,
            payout_external_channel: method === 'external' ? extChannel : undefined,
            payout_external_ref: method === 'external' && extRef ? extRef : undefined,
          });
          await onSaved();
        } catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Payout method">
        <select className="input" value={method} onChange={(e) => setMethod(e.target.value as InterestPayoutMethod)}>
          <option value="credit_savings">Credit savings</option>
          <option value="buy_shares">Buy shares</option>
          <option value="external">External (M-Pesa / bank)</option>
        </select>
      </Field>
      {method === 'credit_savings' && (
        <Field label="Target savings account ID (optional — defaults to ordinary)">
          <input className="input tiny-mono" value={targetAcct} onChange={(e) => setTargetAcct(e.target.value)} placeholder="UUID; leave blank to default" />
        </Field>
      )}
      {method === 'external' && (
        <>
          <Field label="External channel">
            <select className="input" value={extChannel} onChange={(e) => setExtChannel(e.target.value)}>
              <option value="mpesa">M-Pesa</option><option value="bank_transfer">Bank transfer</option><option value="airtel_money">Airtel Money</option>
            </select>
          </Field>
          <Field label="External reference (optional)"><input className="input" value={extRef} onChange={(e) => setExtRef(e.target.value)} /></Field>
        </>
      )}
    </ModalShell>
  );
}

// ─────────── Atoms ───────────

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

function KV({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div>{value}</div>
    </div>
  );
}

function fmt(s: string | number | undefined): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function downloadLinesCSV(run: DividendRun, lines: DividendRunLine[], currency: string) {
  const rows = [['run_no', 'fy_label', 'method', 'share_account_id', 'member_id', 'shares_basis', 'capital_basis', 'rate_pct', `gross (${currency})`, 'wht', `net (${currency})`, 'days_held_in_fy', 'payout_method', 'posted_at']];
  for (const l of lines) {
    rows.push([
      run.run_no, run.financial_year_label, l.calc_method,
      l.share_account_id, l.member_id,
      l.shares_basis, l.capital_basis,
      l.rate_applied_pct, l.gross_dividend, l.wht_amount, l.net_dividend,
      l.days_held_in_fy != null ? String(l.days_held_in_fy) : '',
      l.payout_method, l.posted_at ?? '',
    ]);
  }
  const csv = rows.map((r) => r.map((c) => `"${(c ?? '').replace(/"/g, '""')}"`).join(',')).join('\n');
  const blob = new Blob([csv], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = `${run.run_no}_lines.csv`; a.click();
  URL.revokeObjectURL(url);
}

// ─────────── Shared modal shell ───────────

function ModalShell({ title, busy, onClose, children, submitLabel, onSubmit, disabled, width }: {
  title: string; busy?: boolean; onClose: () => void;
  children: ReactNode; submitLabel: string; onSubmit: () => void | Promise<void>;
  disabled?: boolean; width?: number;
}) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: width ?? 560, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || disabled} onClick={() => void onSubmit()}>{busy ? 'Working…' : submitLabel}</button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}
