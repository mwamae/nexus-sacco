// Loans Phase 4 — Collections queue page at /loans/collections.
//
// Top KPI cards: my queue, all overdue, PTPs due this week, in legal.
// Tabs: My queue / All overdue / PTPs / Field visits / Legal.
// Filter bar: DPD bucket, officer (free-form), product (free-form).
// Table per tab: loan_no, member, outstanding, DPD, last activity,
// PTP status, classification chip.

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  collectionsQueue,
  collectionsPTPSummary,
  type PhaseFourQueueRow,
  type PhaseFourPTPSummary,
  type QueueFilter,
} from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const DPD_BUCKETS = [
  { label: 'Any',         dpd_min: undefined, dpd_max: undefined },
  { label: 'Current (0)', dpd_min: 0,         dpd_max: 0 },
  { label: '1–7',         dpd_min: 1,         dpd_max: 7 },
  { label: '8–30',        dpd_min: 8,         dpd_max: 30 },
  { label: '31–60',       dpd_min: 31,        dpd_max: 60 },
  { label: '61–90',       dpd_min: 61,        dpd_max: 90 },
  { label: '90+',         dpd_min: 91,        dpd_max: undefined },
] as const;

type TabId = 'mine' | 'all' | 'ptps' | 'visits' | 'legal';

const CLASS_COLOR: Record<string, string> = {
  performing: 'var(--pos)',
  watch: '#d4a017',
  substandard: '#c97a00',
  doubtful: '#c33',
  loss: 'var(--neg)',
};

function fmtTimeAgo(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  const diffSec = Math.max(0, (Date.now() - d.getTime()) / 1000);
  if (diffSec < 60) return `${Math.round(diffSec)}s ago`;
  if (diffSec < 3600) return `${Math.round(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.round(diffSec / 3600)}h ago`;
  return `${Math.round(diffSec / 86400)}d ago`;
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}

export default function CollectionsQueue() {
  useDocumentTitle('Loans · Collections queue');
  const { user, tenant, hasPermission } = useAuth();
  const allowed = hasPermission('loans:collect');

  const [tab, setTab] = useState<TabId>('mine');
  const [bucket, setBucket] = useState<number>(0); // index into DPD_BUCKETS
  const [officerFilter, setOfficerFilter] = useState('');
  const [productFilter, setProductFilter] = useState('');
  const [items, setItems] = useState<PhaseFourQueueRow[]>([]);
  const [summary, setSummary] = useState<PhaseFourPTPSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const filter: QueueFilter = useMemo(() => {
    const b = DPD_BUCKETS[bucket];
    const f: QueueFilter = { limit: 200 };
    if (b.dpd_min !== undefined) f.dpd_min = b.dpd_min;
    if (b.dpd_max !== undefined) f.dpd_max = b.dpd_max;
    if (tab === 'mine' && user?.id) f.officer_id = user.id;
    if (tab === 'ptps') f.ptp_status = 'open';
    if (officerFilter) f.officer_id = officerFilter;
    if (productFilter) f.product_id = productFilter;
    // 'legal' tab — backend doesn't have a legal-only filter yet;
    // we just show items where last_event_kind = legal_handover client-side.
    return f;
  }, [tab, bucket, user?.id, officerFilter, productFilter]);

  const reload = async () => {
    if (!allowed) return;
    setBusy(true); setErr(null);
    try {
      const [q, s] = await Promise.all([
        collectionsQueue(filter),
        collectionsPTPSummary(),
      ]);
      let rows = q.items;
      if (tab === 'legal') rows = rows.filter((r) => r.last_event_kind === 'legal_handover');
      if (tab === 'visits') rows = rows.filter((r) => r.last_event_kind === 'auto_sms' || r.last_event_kind === 'letter_generated');
      setItems(rows);
      setSummary(s);
    } catch (e) {
      setErr(asMsg(e));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [tab, bucket, officerFilter, productFilter]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="alert alert-warn">You need <code>loans:collect</code> to view the collections queue.</div>
      </div>
    );
  }

  const TABS: { id: TabId; label: string }[] = [
    { id: 'mine',   label: 'My queue' },
    { id: 'all',    label: 'All overdue' },
    { id: 'ptps',   label: 'PTPs (open)' },
    { id: 'visits', label: 'Auto-sent' },
    { id: 'legal',  label: 'Legal' },
  ];

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Loans · Collections</div>
          <h1>Collections queue</h1>
          <div className="page-sub">
            Drive recovery: drill into loans in arrears, log calls + visits + letters,
            capture PTPs, and escalate stuck cases. Daily auto-SMS + letters run via
            the collections-engine worker.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {summary && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 10, marginTop: 12 }}>
          <KPI label="My queue (assigned)" value={items.filter((r) => r.assigned_officer === user?.id).length.toString()} />
          <KPI label="All overdue" value={items.length.toString()} />
          <KPI label="PTPs open" value={summary.open.toString()} />
          <KPI label="PTPs due this week" value={summary.due_this_week.toString()} tone="warn" />
          <KPI label="PTPs overdue" value={summary.overdue.toString()} tone="neg" />
          <KPI label="PTPs kept (lifetime)" value={summary.kept.toString()} tone="pos" />
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
          <div style={{ display: 'flex', gap: 6 }}>
            {TABS.map((t) => (
              <button
                key={t.id}
                className={`btn ${tab === t.id ? 'btn-primary' : ''}`}
                onClick={() => setTab(t.id)}
              >{t.label}</button>
            ))}
          </div>
          <label>
            <div className="muted tiny">DPD bucket</div>
            <select className="input" value={bucket} onChange={(e) => setBucket(parseInt(e.target.value, 10))}>
              {DPD_BUCKETS.map((b, i) => <option key={i} value={i}>{b.label}</option>)}
            </select>
          </label>
          <label>
            <div className="muted tiny">Officer (UUID)</div>
            <input className="input" type="text" value={officerFilter} onChange={(e) => setOfficerFilter(e.target.value)} placeholder="all officers" style={{ width: 240 }} />
          </label>
          <label>
            <div className="muted tiny">Product (UUID)</div>
            <input className="input" type="text" value={productFilter} onChange={(e) => setProductFilter(e.target.value)} placeholder="all products" style={{ width: 240 }} />
          </label>
          <button className="btn" disabled={busy} onClick={() => void reload()}>{busy ? 'Refreshing…' : '↻'}</button>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Loan</th>
                <th>Member</th>
                <th className="num">Outstanding</th>
                <th className="num">DPD</th>
                <th>Classification</th>
                <th>PTP</th>
                <th>Last event</th>
                <th>Assigned</th>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 ? (
                <tr><td colSpan={8} className="muted" style={{ textAlign: 'center', padding: 18 }}>No loans in this view.</td></tr>
              ) : items.map((r) => (
                <tr key={r.loan_id} style={{ cursor: 'pointer' }} onClick={() => window.location.assign(`/loans/register/${r.loan_id}`)}>
                  <td className="mono">{r.loan_no}</td>
                  <td>{r.member_name}</td>
                  <td className="num mono">{r.outstanding_total}</td>
                  <td className="num mono">{r.dpd_days}</td>
                  <td><span style={{ color: CLASS_COLOR[r.classification] ?? 'inherit', fontWeight: 600 }}>{r.classification}</span></td>
                  <td className="muted tiny">
                    {r.open_ptp_status ? (
                      <span>{r.open_ptp_status} · {r.open_ptp_date?.slice(0, 10)}</span>
                    ) : '—'}
                  </td>
                  <td className="muted tiny">{r.last_event_kind ?? '—'} {r.last_event_at && `· ${fmtTimeAgo(r.last_event_at)}`}</td>
                  <td className="muted tiny mono">
                    {r.assigned_officer
                      ? <span>{r.assigned_officer.slice(0, 8)}…</span>
                      : <span className="muted">unassigned</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function KPI({ label, value, tone }: { label: string; value: string; tone?: 'pos' | 'warn' | 'neg' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'warn' ? 'var(--warn)' : tone === 'neg' ? 'var(--neg)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="card-body">
        <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
        <div style={{ fontSize: 22, fontWeight: 700, color }}>{value}</div>
      </div>
    </div>
  );
}
