// Cash approvals queue — Phase 7b.
//
// One page, two views:
//   • Queue   — table of pending (and optionally closed) approvals.
//   • Detail  — single approval with payload + maker info + actions.
//
// Approval settings (per-kind toggles) live at the bottom of Tenant
// Settings — this page is for acting on items already in the queue.

import { useEffect, useMemo, useState } from 'react';
import {
  approvePendingApproval,
  cancelPendingApproval,
  declinePendingApproval,
  extractError,
  getPendingApproval,
  listPendingApprovals,
  type ApprovalKind,
  type ApprovalStatus,
  type PendingApproval,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { StatusBadge } from '../components/Badge';

const KIND_LABEL: Record<string, string> = {
  deposit: 'Deposit',
  withdrawal: 'Withdrawal',
  deposit_transfer: 'Account transfer',
  share_purchase: 'Share purchase',
  share_redeem: 'Share redeem',
  share_transfer: 'Share transfer',
  share_bonus: 'Bonus shares',
  loan_disbursement: 'Loan disbursement',
  loan_repayment: 'Loan repayment',
  loan_settle: 'Loan settle',
  loan_reverse: 'Reverse loan txn',
  loan_writeoff: 'Loan write-off',
  loan_reschedule: 'Reschedule loan',
  loan_moratorium: 'Loan moratorium',
  loan_settlement_discount: 'Settlement discount',
};

export default function CashApprovalsPage() {
  const path = window.location.pathname;
  const detailMatch = /^\/cash-approvals\/([0-9a-f-]{36})/.exec(path);
  if (detailMatch) return <ApprovalDetail id={detailMatch[1]} />;
  return <ApprovalQueue />;
}

// ─────────── Queue ───────────

function ApprovalQueue() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<PendingApproval[]>([]);
  const [status, setStatus] = useState<ApprovalStatus | ''>('pending');
  const [kind, setKind] = useState<ApprovalKind | ''>('');
  const [includeClosed, setIncludeClosed] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setBusy(true); setErr(null);
    try {
      const r = await listPendingApprovals({
        status: status || undefined,
        kind: kind || undefined,
        include_closed: includeClosed || undefined,
        limit: 200,
      });
      setItems(r.items);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status, kind, includeClosed]);

  const pendingCount = useMemo(() => items.filter((i) => i.status === 'pending').length, [items]);
  const currency = tenant?.currency_code ?? 'KES';

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <h1>Cash approvals</h1>
          <div className="page-sub">{pendingCount} pending · maker-checker queue</div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Filters</h3>
        </div>
        <div className="card-body">
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            <select className="input" value={status} onChange={(e) => setStatus(e.target.value as ApprovalStatus | '')}>
              <option value="">Any status</option>
              <option value="pending">Pending</option>
              <option value="approved">Approved</option>
              <option value="declined">Declined</option>
              <option value="cancelled">Cancelled</option>
              <option value="execution_error">Execution error</option>
            </select>
            <select className="input" value={kind} onChange={(e) => setKind(e.target.value as ApprovalKind | '')}>
              <option value="">Any kind</option>
              {Object.entries(KIND_LABEL).map(([k, v]) => (
                <option key={k} value={k}>{v}</option>
              ))}
            </select>
            <label className="row" style={{ gap: 4, alignItems: 'center' }}>
              <input type="checkbox" checked={includeClosed} onChange={(e) => setIncludeClosed(e.target.checked)} />
              <span className="muted tiny">include closed</span>
            </label>
            <button className="btn btn-sm" onClick={() => void load()} disabled={busy}>Refresh</button>
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-hd">
          <h3>Queue</h3>
          <span className="card-sub">{items.length} shown</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Kind</th>
                <th>Title</th>
                <th className="r">Amount</th>
                <th>Maker</th>
                <th>Submitted</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map((p) => (
                <tr key={p.id}>
                  <td>{KIND_LABEL[p.kind] ?? p.kind}</td>
                  <td>{p.title}</td>
                  <td className="r mono">{p.amount ? `${currency} ${fmt(p.amount)}` : '—'}</td>
                  <td className="tiny-mono">{p.maker_user_id.slice(0, 8)}…</td>
                  <td className="tiny-mono">{p.maker_at.slice(0, 16).replace('T', ' ')}</td>
                  <td><StatusBadge status={p.status} /></td>
                  <td><a className="btn btn-sm" href={`/cash-approvals/${p.id}`}>Open</a></td>
                </tr>
              ))}
              {items.length === 0 && (
                <tr><td colSpan={7} className="muted center">No items match the current filter</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

// ─────────── Detail ───────────

function ApprovalDetail({ id }: { id: string }) {
  const { tenant, user, hasPermission } = useAuth();
  const [p, setP] = useState<PendingApproval | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [note, setNote] = useState('');

  async function load() {
    setErr(null);
    try { setP(await getPendingApproval(id)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [id]);

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!p) return <div className="page"><div className="empty">Loading…</div></div>;

  const currency = tenant?.currency_code ?? 'KES';
  const canAct = hasPermission('approvals:act');
  const isMaker = user?.id === p.maker_user_id;
  const isPending = p.status === 'pending';

  async function run(label: string, fn: () => Promise<unknown>) {
    setBusy(label); setErr(null);
    try { await fn(); await load(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(null); }
  }

  // Payload may have arrived as a base64 string OR an inline object.
  let payload: any = p.payload;
  if (typeof payload === 'string') {
    try { payload = JSON.parse(atob(payload)); } catch { /* leave as-is */ }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/cash-approvals" style={{ color: 'inherit' }}>Cash approvals</a></div>
          <h1>{p.title}</h1>
          <div className="page-sub">
            {KIND_LABEL[p.kind] ?? p.kind}
            {p.amount ? ` · ${currency} ${fmt(p.amount)}` : ''}
          </div>
        </div>
        <div className="page-hd-actions"><StatusBadge status={p.status} /></div>
      </div>

      <div className="grid-3" style={{ marginBottom: 14 }}>
        <div className="card">
          <div className="card-hd"><h3>Maker</h3></div>
          <div className="card-body">
            <KV label="User" value={<span className="mono">{p.maker_user_id.slice(0, 8)}…</span>} />
            <KV label="Submitted" value={p.maker_at.slice(0, 19).replace('T', ' ')} />
            {p.maker_note && <KV label="Note" value={p.maker_note} />}
          </div>
        </div>
        <div className="card">
          <div className="card-hd"><h3>Checker</h3></div>
          <div className="card-body">
            {p.checker_user_id
              ? <>
                  <KV label="User" value={<span className="mono">{p.checker_user_id.slice(0, 8)}…</span>} />
                  <KV label="Decided" value={p.checker_at?.slice(0, 19).replace('T', ' ')} />
                  {p.checker_note && <KV label="Note" value={p.checker_note} />}
                </>
              : <span className="muted tiny">Awaiting decision</span>}
          </div>
        </div>
        <div className="card">
          <div className="card-hd"><h3>Result</h3></div>
          <div className="card-body">
            {p.result_txn_id && <KV label="Posted txn id" value={<span className="mono">{p.result_txn_id.slice(0, 8)}…</span>} />}
            {p.result_error && <div className="alert alert-error">Execution error: {p.result_error}</div>}
            {!p.result_txn_id && !p.result_error && <span className="muted tiny">No result yet</span>}
          </div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Payload</h3>
          <span className="card-sub">Original request body</span>
        </div>
        <div className="card-body">
          <pre className="mono tiny" style={{ background: 'var(--bg-2)', padding: 8, borderRadius: 4, overflow: 'auto' }}>{JSON.stringify(payload, null, 2)}</pre>
        </div>
      </div>

      {isPending && (
        <div className="card">
          <div className="card-hd"><h3>Decision</h3></div>
          <div className="card-body">
            <label>
              <span className="muted tiny">Note (required for decline)</span>
              <input className="input" value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. verified slip" />
            </label>
            <div className="row" style={{ gap: 6, marginTop: 10, flexWrap: 'wrap' }}>
              {canAct && (
                <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('approve', () => approvePendingApproval(id, note))}>
                  Approve & post
                </button>
              )}
              {canAct && (
                <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={!!busy || !note} onClick={() => run('decline', () => declinePendingApproval(id, note))}>
                  Decline
                </button>
              )}
              {isMaker && (
                <button className="btn btn-sm" disabled={!!busy} onClick={() => run('cancel', () => cancelPendingApproval(id, note))}>
                  Cancel (maker)
                </button>
              )}
            </div>
            {!canAct && !isMaker && (
              <p className="muted tiny" style={{ marginTop: 8 }}>You don't have the approvals:act permission.</p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function KV({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 6 }}>
      <div className="muted tiny">{label}</div>
      <div>{value}</div>
    </div>
  );
}

function fmt(s: string | number | undefined | null): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
