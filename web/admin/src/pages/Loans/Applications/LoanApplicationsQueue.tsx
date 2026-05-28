// /loans/applications — Loan Applications Queue (Phase 1).
//
// Distinct from /applications (membership onboarding). This queue is
// the credit officer / sacco admin daily landing page for loan apps
// across the full lifecycle (draft → … → disbursed).
//
// Filters:
//   • Status (15 enum values + "all")
//   • Product
//   • Free-text search (member name / application_no)
//   • Date range (created between)
//
// Each row links to /loans/applications/{id} for the detail page
// (Approve / Decline / Counter-offer / Return for info actions).
//
// Permission: loans:view to read; loans:apply to use the "New
// application" button.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../../auth/AuthContext';
import {
  listLoanApplications,
  type LoanAppListItem,
  extractError,
} from '../../../api/client';
import { useDocumentTitle } from '../../../lib/useDocumentTitle';

const STATUS_LABEL: Record<string, string> = {
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

const STATUS_TONE: Record<string, string> = {
  pending_validation: '#3b82f6',
  pending_guarantor: '#3b82f6',
  pending_scoring: '#3b82f6',
  pending_approval: '#f59e0b',
  approved: '#22c55e',
  approved_with_conditions: '#22c55e',
  declined: '#ef4444',
  returned_for_info: '#f59e0b',
  offer_sent: '#06b6d4',
  offer_accepted: '#22c55e',
  offer_declined: '#ef4444',
  expired: '#94a3b8',
  cancelled: '#94a3b8',
  disbursed: '#84cc16',
  draft: '#94a3b8',
};

export default function LoanApplicationsQueue() {
  useDocumentTitle('Loans · Applications');
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const allowed = hasPermission('loans:view');
  const canCreate = hasPermission('loans:apply');

  // Read status from URL query string so the dashboard's deep-links
  // (e.g. /loans/applications?status=pending_approval) work as
  // expected without bouncing.
  const initialStatus = useMemo(() => {
    const p = new URLSearchParams(window.location.search);
    return p.get('status') ?? '';
  }, []);

  const [status, setStatus] = useState(initialStatus);
  const [q, setQ] = useState('');
  const [items, setItems] = useState<LoanAppListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const fetchList = useCallback(async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await listLoanApplications({
        status: status || undefined,
        q: q || undefined,
        limit: 100,
      });
      setItems(r.items);
      setTotal(r.total);
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }, [status, q]);

  useEffect(() => {
    if (!allowed) return;
    void fetchList();
  }, [allowed, fetchList]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>Loan applications</h1></div>
        <div className="alert alert-warn">
          You need <code>loans:view</code> permission. Ask your SACCO admin to grant access.
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Loans · Applications</div>
          <h1>Loan applications</h1>
          <div className="page-sub">{total} application{total === 1 ? '' : 's'} matching filter</div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {canCreate && (
            <a className="btn btn-accent" href="/loans/applications/new">+ New application</a>
          )}
          <button className="btn" disabled={busy} onClick={() => void fetchList()}>↻ Refresh</button>
        </div>
      </div>

      {/* Filters */}
      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12 }}>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Status</div>
            <select className="input" value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {Object.entries(STATUS_LABEL).map(([k, l]) => (
                <option key={k} value={k}>{l}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Search</div>
            <input
              className="input"
              type="text"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') void fetchList(); }}
              placeholder="application_no or member name"
            />
          </label>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-body flush">
          {items.length === 0 && !busy ? (
            <div className="empty">No applications match the current filter.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>App no</th>
                  <th>Member</th>
                  <th>Product</th>
                  <th className="num">Requested</th>
                  <th>Term</th>
                  <th>Status</th>
                  <th>Score</th>
                  <th>Risk</th>
                  <th>Age</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => {
                  const a = it.application;
                  return (
                    <tr
                      key={a.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => { window.location.href = `/loans/applications/${a.id}`; }}
                      title="Open application"
                    >
                      <td className="mono">{a.application_no}</td>
                      <td>
                        <div style={{ fontWeight: 500 }}>{it.member_name}</div>
                        <div className="muted tiny mono">{it.member_no}</div>
                      </td>
                      <td>
                        <div>{it.product_name}</div>
                        <div className="muted tiny mono">{it.product_code}</div>
                      </td>
                      <td className="num mono">{currency} {fmtMoney(a.requested_amount)}</td>
                      <td>{a.requested_term_months}m</td>
                      <td>
                        <span style={{
                          display: 'inline-block',
                          padding: '2px 8px',
                          borderRadius: 999,
                          background: (STATUS_TONE[a.status] ?? '#94a3b8') + '22',
                          color: STATUS_TONE[a.status] ?? '#94a3b8',
                          fontWeight: 600,
                          fontSize: 11,
                        }}>
                          {STATUS_LABEL[a.status] ?? a.status}
                        </span>
                      </td>
                      <td className="mono">{a.credit_score ?? '—'}</td>
                      <td>{a.risk_band ?? '—'}</td>
                      <td className="tiny muted">{ageOf(a.created_at)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function fmtMoney(s: string | undefined): string {
  const n = parseFloat(s ?? '0');
  if (!isFinite(n)) return s ?? '0';
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function ageOf(iso: string): string {
  const d = new Date(iso);
  const diffH = Math.floor((Date.now() - d.getTime()) / 3_600_000);
  if (diffH < 1) return 'just now';
  if (diffH < 24) return `${diffH}h ago`;
  const diffD = Math.floor(diffH / 24);
  if (diffD < 30) return `${diffD}d ago`;
  return d.toISOString().slice(0, 10);
}
