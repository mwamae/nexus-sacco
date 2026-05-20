// Interest runs — list, create, drill-into-detail.
//
// Single-file page that switches between list and detail views based
// on the URL: /interest-runs        → list + new-run modal
//             /interest-runs/<id>   → detail (preview, approve, post, lock)

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  approveInterestRun,
  cancelInterestRun,
  computeInterestRun,
  createInterestRun,
  extractError,
  getInterestRun,
  getWHTSchedule,
  listDepositProducts,
  listInterestRuns,
  lockInterestRun,
  postInterestRun,
  submitInterestRun,
  updateInterestLine,
  type DepositProduct,
  type InterestPayoutMethod,
  type InterestRun,
  type InterestRunDetail,
  type InterestRunLine,
  type InterestRunStatus,
  type WHTSchedule,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

const STATUS_LABEL: Record<InterestRunStatus, string> = {
  draft: 'Draft',
  computing: 'Computing',
  preview: 'Preview',
  approved: 'Approved',
  posting: 'Posting',
  posted: 'Posted',
  locked: 'Locked',
  cancelled: 'Cancelled',
};

export default function InterestRunsPage() {
  // Route handling — keep it simple and self-contained.
  const path = window.location.pathname;
  const detailMatch = /^\/interest-runs\/([0-9a-f-]{36})/.exec(path);
  if (detailMatch) return <RunDetail runId={detailMatch[1]} />;
  return <RunList />;
}

// ─────────── List view ───────────

function RunList() {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canRun = hasPermission('interest:run');

  const [items, setItems] = useState<InterestRun[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [status, setStatus] = useState('');
  const [fy, setFy] = useState('');
  const [openNew, setOpenNew] = useState(false);
  const [whtModal, setWhtModal] = useState<{ fy: string } | null>(null);

  async function reload() {
    setErr(null);
    try {
      const r = await listInterestRuns({
        status: status || undefined,
        fy: fy || undefined,
        limit: 100,
      });
      setItems(r.items); setTotal(r.total);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status, fy]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">FY Accounting · Deposits</div>
          <h1>Interest runs</h1>
          <div className="page-sub">Weighted-average deposit interest declared at the AGM.</div>
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
              {(['draft', 'preview', 'approved', 'posted', 'locked', 'cancelled'] as InterestRunStatus[]).map((s) => (
                <option key={s} value={s}>{STATUS_LABEL[s]}</option>
              ))}
            </select>
          </div>
        </div>
        <div className="card-body flush">
          {items.length === 0 ? (
            <div className="empty">
              No runs yet.
              {canRun && <> <a style={{ color: 'var(--accent)' }} onClick={() => setOpenNew(true)}>Create one →</a></>}
            </div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Run #</th>
                  <th>Financial year</th>
                  <th>Status</th>
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
                    <td className="tiny-mono">
                      <a className="tbl-link" href={`/interest-runs/${r.id}`}>{r.run_no}</a>
                    </td>
                    <td>
                      <div>{r.financial_year_label}</div>
                      <div className="muted tiny">{r.fy_start.slice(0, 10)} → {r.fy_end.slice(0, 10)}</div>
                    </td>
                    <td><StatusBadge status={r.status} /></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.agm_rate_pct}%</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.member_count ?? '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.total_gross_interest ? `${currency} ${fmt(r.total_gross_interest)}` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right', color: 'var(--neg)' }}>{r.total_wht ? `${currency} ${fmt(r.total_wht)}` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.total_net_interest ? `${currency} ${fmt(r.total_net_interest)}` : '—'}</td>
                    <td className="tiny-mono">{r.agm_resolution_ref}</td>
                    <td>
                      <div className="row" style={{ gap: 4 }}>
                        <a className="btn btn-sm" href={`/interest-runs/${r.id}`}><Icon name="eye" size={12} /></a>
                        <button className="btn btn-sm" onClick={() => setWhtModal({ fy: r.financial_year_label })}>WHT</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {openNew && <NewRunModal onClose={() => setOpenNew(false)} onCreated={async () => { setOpenNew(false); await reload(); }} />}
      {whtModal && <WHTScheduleModal fy={whtModal.fy} currency={currency} onClose={() => setWhtModal(null)} />}
    </div>
  );
}

// ─────────── New-run modal ───────────

function NewRunModal({ onClose, onCreated }: { onClose: () => void; onCreated: (r: InterestRun) => void | Promise<void> }) {
  const [products, setProducts] = useState<DepositProduct[]>([]);
  const [pErr, setPErr] = useState<string | null>(null);
  useEffect(() => {
    listDepositProducts(false).then((all) => setProducts(all.filter((p) => p.interest_eligible))).catch((e) => setPErr(extractError(e)));
  }, []);

  const today = new Date().toISOString().slice(0, 10);
  const yearStart = `${new Date().getFullYear() - 1}-01-01`;
  const yearEnd = `${new Date().getFullYear() - 1}-12-31`;
  const [fyStart, setFyStart] = useState(yearStart);
  const [fyEnd, setFyEnd] = useState(yearEnd);
  const [rate, setRate] = useState('8.5');
  const [agmRef, setAgmRef] = useState('');
  const [agmDate, setAgmDate] = useState(today);
  const [productIDs, setProductIDs] = useState<Set<string>>(new Set());
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Default to all eligible products selected.
  useEffect(() => {
    if (products.length > 0 && productIDs.size === 0) {
      setProductIDs(new Set(products.map((p) => p.id)));
    }
  }, [products, productIDs.size]);

  function togglePid(id: string) {
    const next = new Set(productIDs);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setProductIDs(next);
  }

  async function submit() {
    setErr(null);
    if (!agmRef.trim() || !rate || productIDs.size === 0) {
      setErr('All fields are required and at least one product must be selected.');
      return;
    }
    setBusy(true);
    try {
      const r = await createInterestRun({
        fy_start: fyStart, fy_end: fyEnd,
        agm_rate_pct: rate,
        agm_resolution_ref: agmRef,
        agm_resolution_date: agmDate,
        product_ids: Array.from(productIDs),
        notes: notes || undefined,
      });
      // Send the operator straight to the new run's detail page so they can compute.
      window.location.href = `/interest-runs/${r.id}`;
      await onCreated(r);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <ModalShell title="New interest run" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Create draft"
      disabled={!agmRef.trim() || !rate || productIDs.size === 0}>
      {pErr && <div className="alert alert-error">{pErr}</div>}
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        <strong>AGM gate.</strong> Capture the AGM resolution that authorised this rate before computing — the system blocks any forward progress without it.
      </div>
      <div className="grid-2">
        <Field label="FY start"><input className="input mono" type="date" value={fyStart} onChange={(e) => setFyStart(e.target.value)} /></Field>
        <Field label="FY end (inclusive)"><input className="input mono" type="date" value={fyEnd} onChange={(e) => setFyEnd(e.target.value)} /></Field>
        <Field label="AGM-approved rate (% p.a.)"><input className="input mono" value={rate} onChange={(e) => setRate(e.target.value)} placeholder="8.5" /></Field>
        <Field label="AGM resolution date"><input className="input mono" type="date" value={agmDate} onChange={(e) => setAgmDate(e.target.value)} /></Field>
      </div>
      <Field label="AGM resolution reference"><input className="input" value={agmRef} onChange={(e) => setAgmRef(e.target.value)} placeholder="AGM/2026/RES-014" /></Field>
      <Field label="Notes (optional)"><textarea className="input" rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} /></Field>
      <Field label="In-scope products">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 200, overflowY: 'auto' }}>
          {products.map((p) => (
            <label key={p.id} style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 13 }}>
              <input type="checkbox" checked={productIDs.has(p.id)} onChange={() => togglePid(p.id)} />
              <span>{p.name} <span className="muted tiny">· {p.product_type} · {p.code}</span></span>
            </label>
          ))}
          {products.length === 0 && <div className="muted tiny">No interest-eligible products configured.</div>}
        </div>
      </Field>
    </ModalShell>
  );
}

// ─────────── Run detail ───────────

function RunDetail({ runId }: { runId: string }) {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canRun = hasPermission('interest:run');
  const canApprove = hasPermission('interest:approve');
  const canPost = hasPermission('interest:post');

  const [data, setData] = useState<InterestRunDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try { setData(await getInterestRun(runId)); }
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
          <div className="eyebrow"><a href="/interest-runs" style={{ color: 'inherit' }}>Interest runs</a> · {r.run_no}</div>
          <h1>{r.financial_year_label}</h1>
          <div className="page-sub">
            FY {r.fy_start.slice(0, 10)} → {r.fy_end.slice(0, 10)} ·
            Rate <strong>{r.agm_rate_pct}%</strong> · WHT <strong>{r.wht_rate_pct}%</strong>
          </div>
        </div>
        <div className="page-hd-actions">
          <StatusBadge status={r.status} />
        </div>
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
            <KV label="Rate applied" value={<span className="mono">{r.agm_rate_pct}% p.a.</span>} />
          </div>
        </div>
      </div>

      {r.member_count != null && (
        <div className="grid-4" style={{ marginBottom: 14 }}>
          <KPI label="Members" value={String(r.member_count)} />
          <KPI label="Total weighted balance" value={`${currency} ${fmt(r.total_weighted_balance ?? '0')}`} />
          <KPI label="Gross interest" value={`${currency} ${fmt(r.total_gross_interest ?? '0')}`} />
          <KPI label="Net (after WHT)" value={`${currency} ${fmt(r.total_net_interest ?? '0')}`} sub={`WHT ${currency} ${fmt(r.total_wht ?? '0')}`} />
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
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('compute', () => computeInterestRun(runId))}>
                {busy === 'compute' ? 'Computing…' : 'Compute preview'}
              </button>
            )}
            {canRun && stat === 'preview' && !r.workflow_instance_id && (
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('submit', () => submitInterestRun(runId))}>
                {busy === 'submit' ? 'Submitting…' : 'Submit for workflow approval'}
              </button>
            )}
            {canApprove && stat === 'preview' && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('approve', async () => {
                if (!confirm('Approve this run directly (bypass workflow)?')) return;
                await approveInterestRun(runId, 'Direct approval');
              })}>
                {busy === 'approve' ? 'Approving…' : 'Approve directly'}
              </button>
            )}
            {canPost && stat === 'approved' && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('post', async () => {
                if (!confirm(`Post ${lines.length} line${lines.length === 1 ? '' : 's'}? This credits member accounts and writes WHT entries. Cannot be reversed without a formal adjustment.`)) return;
                await postInterestRun(runId);
              })}>
                {busy === 'post' ? 'Posting…' : `Post all (${lines.length})`}
              </button>
            )}
            {canPost && stat === 'posted' && (
              <button className="btn btn-sm" disabled={!!busy} onClick={() => run('lock', async () => {
                if (!confirm('Lock this run? Once locked, no further changes are possible without a formal adjustment.')) return;
                await lockInterestRun(runId);
              })}>
                {busy === 'lock' ? 'Locking…' : 'Lock run'}
              </button>
            )}
            {canRun && stat !== 'posted' && stat !== 'locked' && stat !== 'cancelled' && (
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={!!busy} onClick={() => run('cancel', async () => {
                const reason = prompt('Cancellation reason?'); if (!reason) return;
                await cancelInterestRun(runId, reason);
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
            <p className="muted tiny" style={{ marginTop: 8 }}>
              <strong>Cancelled:</strong> {r.cancellation_reason}
            </p>
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
                  <th>Account</th>
                  <th>Member</th>
                  <th style={{ textAlign: 'right' }}>Days</th>
                  <th style={{ textAlign: 'right' }}>Weighted avg</th>
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
  line: InterestRunLine; runStatus: InterestRunStatus; currency: string; onReload: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const editable = (runStatus === 'preview' || runStatus === 'draft') && !line.posted_at;
  return (
    <tr>
      <td className="tiny-mono">{line.account_id.slice(0, 8)}…</td>
      <td>
        <a className="tbl-link" href={`/members/${line.member_id}?tab=accounts`}>{line.member_id.slice(0, 8)}…</a>
      </td>
      <td className="mono" style={{ textAlign: 'right' }}>
        {line.days_with_snapshots}/{line.days_in_fy}
      </td>
      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(line.weighted_avg_balance)}</td>
      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(line.gross_interest)}</td>
      <td className="mono" style={{ textAlign: 'right', color: 'var(--neg)' }}>{currency} {fmt(line.wht_amount)}</td>
      <td className="mono" style={{ textAlign: 'right', color: 'var(--pos)' }}><strong>{currency} {fmt(line.net_interest)}</strong></td>
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

function PayoutEditModal({ line, onClose, onSaved }: { line: InterestRunLine; onClose: () => void; onSaved: () => Promise<void> }) {
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
          await updateInterestLine(line.id, {
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
          <Field label="External reference (optional)">
            <input className="input" value={extRef} onChange={(e) => setExtRef(e.target.value)} />
          </Field>
        </>
      )}
    </ModalShell>
  );
}

// ─────────── WHT schedule modal ───────────

function WHTScheduleModal({ fy, currency, onClose }: { fy: string; currency: string; onClose: () => void }) {
  const [data, setData] = useState<WHTSchedule | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getWHTSchedule(fy).then(setData).catch((e) => setErr(extractError(e)));
  }, [fy]);

  function downloadCSV() {
    if (!data) return;
    const rows = [['member_no', 'member_name', 'gross_amount', 'wht_amount']];
    for (const r of data.rows) rows.push([r.member_no, r.member_name, r.gross_amount, r.wht_amount]);
    const csv = rows.map((r) => r.map((c) => `"${(c ?? '').replace(/"/g, '""')}"`).join(',')).join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = `wht-${fy.replace(/[^a-z0-9]/gi, '_')}.csv`; a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <ModalShell title={`WHT remittance schedule — ${fy}`} onClose={onClose} onSubmit={downloadCSV} submitLabel="Download CSV" width={720} disabled={!data}>
      {err && <div className="alert alert-error">{err}</div>}
      {!data && !err && <div className="empty">Loading…</div>}
      {data && (
        <>
          <div className="grid-2" style={{ marginBottom: 10 }}>
            <KPI label="Members" value={String(data.rows.length)} />
            <KPI label="Total WHT" value={`${currency} ${fmt(data.total_wht)}`} tone="neg" />
          </div>
          {data.rows.length === 0 ? (
            <div className="empty">No WHT entries for this FY.</div>
          ) : (
            <table className="tbl">
              <thead><tr><th>Member #</th><th>Member name</th><th style={{ textAlign: 'right' }}>Gross</th><th style={{ textAlign: 'right' }}>WHT</th></tr></thead>
              <tbody>
                {data.rows.map((r) => (
                  <tr key={r.member_id}>
                    <td className="tiny-mono">{r.member_no}</td>
                    <td>{r.member_name}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(r.gross_amount)}</td>
                    <td className="mono" style={{ textAlign: 'right', color: 'var(--neg)' }}>{currency} {fmt(r.wht_amount)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
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

function downloadLinesCSV(run: InterestRun, lines: InterestRunLine[], currency: string) {
  const rows = [['run_no', 'fy_label', 'account_id', 'member_id', 'days_in_fy', 'snapshot_days', 'weighted_avg', 'rate_pct', `gross (${currency})`, 'wht', `net (${currency})`, 'payout_method', 'posted_at']];
  for (const l of lines) {
    rows.push([
      run.run_no, run.financial_year_label, l.account_id, l.member_id,
      String(l.days_in_fy), String(l.days_with_snapshots),
      l.weighted_avg_balance, l.rate_applied_pct,
      l.gross_interest, l.wht_amount, l.net_interest,
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
