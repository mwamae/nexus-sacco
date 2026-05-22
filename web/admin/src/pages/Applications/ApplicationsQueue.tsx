// Unified Membership Application Queue — single view for both
// individual and institutional applications. Filters mirror the
// backend's ApplicationListFilter.

import { useEffect, useMemo, useState } from 'react';
import {
  listApplications,
  type ApplicationKind,
  type ApplicationStatus,
  type MembershipApplication,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const STATUS_LABEL: Record<ApplicationStatus, string> = {
  submitted: 'Pending review',
  under_review: 'Under review',
  returned_for_correction: 'Returned',
  reviewed_pending_approval: 'Pending approval',
  approved_active: 'Approved',
  declined: 'Declined',
  withdrawn: 'Withdrawn',
};
const STATUS_COLOR: Record<ApplicationStatus, string> = {
  submitted: '#3b6ab8',
  under_review: '#c97a00',
  returned_for_correction: 'var(--neg)',
  reviewed_pending_approval: '#3b6ab8',
  approved_active: 'var(--pos)',
  declined: 'var(--neg)',
  withdrawn: 'var(--muted)',
};
const FEE_COLOR: Record<string, string> = {
  paid: 'var(--pos)',
  shortfall: '#c97a00',
  not_paid: 'var(--neg)',
  not_required: 'var(--muted)',
};

export default function ApplicationsQueuePage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<MembershipApplication[]>([]);
  const [total, setTotal] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const [kind, setKind] = useState<ApplicationKind | ''>('');
  const [status, setStatus] = useState<ApplicationStatus | ''>('');
  const [feeStatus, setFeeStatus] = useState('');
  const [unassigned, setUnassigned] = useState(false);
  const [q, setQ] = useState('');

  async function load() {
    setBusy(true); setErr(null);
    try {
      const r = await listApplications({
        kind: kind || undefined,
        status: status || undefined,
        fee_status: feeStatus || undefined,
        unassigned: unassigned || undefined,
        q: q || undefined,
      });
      setItems(r.items); setTotal(r.total);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  const ageColor = useMemo(() => (days: number) => {
    if (days >= 7) return 'var(--neg)';
    if (days >= 3) return '#c97a00';
    return 'var(--muted)';
  }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Members · Applications</div>
          <h1>Membership applications</h1>
          <div className="page-sub">
            Unified queue for both individual and institutional applicants. Officers capture
            applications here, reviewers walk the checklist, and approvers finalise.
          </div>
        </div>
        <a className="btn btn-primary" href="/applications/new">+ New application</a>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr) auto', gap: 12, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">Kind</div>
            <select value={kind} onChange={(e) => setKind(e.target.value as ApplicationKind | '')}>
              <option value="">All kinds</option>
              <option value="individual">Individual</option>
              <option value="institutional">Institutional</option>
            </select>
          </label>
          <label>
            <div className="muted tiny">Status</div>
            <select value={status} onChange={(e) => setStatus(e.target.value as ApplicationStatus | '')}>
              <option value="">All statuses</option>
              {(Object.keys(STATUS_LABEL) as ApplicationStatus[]).map((s) => (
                <option key={s} value={s}>{STATUS_LABEL[s]}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny">Registration fee</div>
            <select value={feeStatus} onChange={(e) => setFeeStatus(e.target.value)}>
              <option value="">Any</option>
              <option value="paid">Paid</option>
              <option value="shortfall">Shortfall</option>
              <option value="not_paid">Not paid</option>
              <option value="not_required">Not required</option>
            </select>
          </label>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input type="checkbox" checked={unassigned} onChange={(e) => setUnassigned(e.target.checked)} />
            <span>Unassigned to reviewer</span>
          </label>
          <label>
            <div className="muted tiny">Search</div>
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Application no, name, email" />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Refresh'}
          </button>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Queue</h3><span className="card-sub">{total} application{total === 1 ? '' : 's'}</span></div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>App no</th>
                <th>Applicant</th>
                <th>Kind</th>
                <th>Status</th>
                <th>Fee</th>
                <th>Submitted</th>
                <th className="num">Age</th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => (
                <tr key={a.id} style={{ cursor: 'pointer' }} onClick={() => { window.location.href = `/applications/${a.id}`; }}>
                  <td className="mono">{a.application_no}</td>
                  <td>
                    <div>{a.applicant_name}</div>
                    {a.entity_type && <div className="muted tiny">{a.entity_type}</div>}
                  </td>
                  <td>
                    <span style={{ fontWeight: 600, color: a.kind === 'individual' ? '#3b6ab8' : '#c97a00' }}>
                      {a.kind}
                    </span>
                  </td>
                  <td><span style={{ color: STATUS_COLOR[a.status], fontWeight: 600 }}>{STATUS_LABEL[a.status]}</span></td>
                  <td>
                    <span style={{ color: FEE_COLOR[a.fee_status] ?? 'var(--muted)', fontWeight: 600 }}>{a.fee_status}</span>
                    {a.fee_status !== 'not_required' && a.fee_required && (
                      <div className="muted tiny mono">{a.fee_amount_paid} / {a.fee_amount_due}</div>
                    )}
                  </td>
                  <td className="mono tiny">{a.submitted_at.slice(0, 10)}</td>
                  <td className="num" style={{ color: ageColor(a.days_in_queue), fontWeight: 600 }}>
                    {a.days_in_queue}d
                  </td>
                </tr>
              ))}
              {items.length === 0 && (
                <tr><td colSpan={7} className="muted" style={{ textAlign: 'center', padding: 18 }}>No applications match the filters.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
