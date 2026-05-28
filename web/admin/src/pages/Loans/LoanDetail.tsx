// /loans/register/{id} — Consolidated Loan Workspace (Phase 1).
//
// Single source-of-truth page for one loan. Tabs cover everything an
// officer needs without bouncing between pages.
//
// Tabs (per the strategic doc §7.4):
//
//   Overview               · KPI strip of balances, key dates
//   Schedule               · amortisation rows with paid status
//   Transactions           · every loan_transaction chronologically
//   Guarantors             · linked guarantees
//   Collateral             · linked collateral items
//   Documents              · Phase 2 wires uploads
//   Collections timeline   · "Phase 4 — coming soon" placeholder
//   Restructure history    · reschedule/moratorium/settlement_discount events
//   Comments               · Phase 2 wires the loan_comments thread
//
// Action bar (Phase 1):
//   Make repayment · Settle · Reschedule · Moratorium · Reverse last
//   txn · Settlement discount · Write-off
//   (Top-up / Refinance / Issue PTP are disabled — Phase 5/4)
//
// Permission: loans:view to read; loans:* for the matching actions.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  getLoan,
  repayLoan,
  getLoanClassificationHistory,
  loanCollectionsEvents,
  logCall,
  logCollectionNote,
  createCollectionPTP,
  cancelCollectionPTP,
  sendCollectionSMS,
  generateCollectionLetter,
  escalateCollection,
  legalHandover,
  createTopUpApplication,
  createRefinanceApplication,
  type LoanDetail as LoanDetailType,
  type LoanInstallment,
  type LoanTransaction,
  type LoanGuarantee,
  type LoanCollateralItem,
  type LoanClassificationHistoryRow,
  type CollectionEvent,
  type CollectionLetterKind,
  extractError,
} from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

type TabId = 'overview' | 'schedule' | 'transactions' | 'guarantors' | 'collateral' | 'documents' | 'collections' | 'restructure' | 'classification' | 'comments';

const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',       label: 'Overview' },
  { id: 'schedule',       label: 'Schedule' },
  { id: 'transactions',   label: 'Transactions' },
  { id: 'guarantors',     label: 'Guarantors' },
  { id: 'collateral',     label: 'Collateral' },
  { id: 'documents',      label: 'Documents' },
  { id: 'collections',    label: 'Collections' },
  { id: 'restructure',    label: 'Restructure history' },
  { id: 'classification', label: 'Classification' }, // Phase 3
  { id: 'comments',       label: 'Comments' },
];

const STATUS_LABEL: Record<string, string> = {
  pending_disbursement: 'Pending disbursement',
  active: 'Active',
  in_arrears: 'In arrears',
  defaulted: 'Defaulted',
  restructured: 'Restructured',
  settled: 'Settled',
  written_off: 'Written off',
  closed: 'Closed',
};

export default function LoanDetail() {
  useDocumentTitle('Loans · Detail');
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canView = hasPermission('loans:view');
  const canTransact = hasPermission('savings:transact');
  const canApprove = hasPermission('savings:approve');
  const canReverse = hasPermission('loans:reverse');
  const canRestructure = hasPermission('loans:restructure');
  const canWriteoff = hasPermission('loans:writeoff');

  const id = useMemo(() => {
    const parts = window.location.pathname.split('/').filter(Boolean);
    return parts[parts.length - 1] ?? '';
  }, []);

  const [detail, setDetail] = useState<LoanDetailType | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<TabId>('overview');
  const [modal, setModal] = useState<null | 'repay' | 'topup' | 'refinance'>(null);

  const refresh = useCallback(async () => {
    if (!id) return;
    try {
      const d = await getLoan(id);
      setDetail(d);
      setErr(null);
    } catch (e) {
      setErr(extractError(e));
    }
  }, [id]);

  useEffect(() => {
    if (!canView) return;
    void refresh();
  }, [canView, refresh]);

  if (!canView) {
    return <div className="page"><div className="alert alert-warn">You need <code>loans:view</code>.</div></div>;
  }
  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!detail) return <div className="page"><div className="empty">Loading loan…</div></div>;

  const l = detail.loan;
  const outstanding =
    (parseFloat(l.principal_balance) || 0) +
    (parseFloat(l.interest_balance) || 0) +
    (parseFloat(l.fees_balance) || 0) +
    (parseFloat(l.penalty_balance) || 0);

  // Phase 1 classification chip — every active/in_arrears loan
  // reads as "Performing" until Phase 3 wires the real DPD engine
  // that drives arrears_classification.
  const classification = l.status === 'active' ? 'Performing' :
    l.status === 'in_arrears' ? 'Watch' :
    l.status === 'defaulted' ? 'Loss' :
    STATUS_LABEL[l.status] ?? l.status;

  return (
    <div className="page">
      {/* ── Header ──────────────────────────────────────────── */}
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/loans/register" style={{ color: 'inherit' }}>← Register</a>
          </div>
          <h1>
            <span className="mono">{l.loan_no}</span>
            <span style={{ marginLeft: 12, fontSize: 14, fontWeight: 400, color: 'var(--muted)' }}>
              {STATUS_LABEL[l.status] ?? l.status} · {classification}
            </span>
          </h1>
          <div className="page-sub">
            {currency} {fmt(l.principal)} principal · {l.term_months}m · {l.interest_rate_pct}% {l.interest_method.replace('_', ' ')}
          </div>
        </div>
      </div>

      {/* ── KPI strip ───────────────────────────────────────── */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 10, marginBottom: 12 }}>
        <Kpi label="Outstanding" value={`${currency} ${fmt(outstanding)}`} />
        <Kpi label="Principal balance" value={`${currency} ${fmt(l.principal_balance)}`} />
        <Kpi label="Interest balance" value={`${currency} ${fmt(l.interest_balance)}`} />
        <Kpi label="Fees balance" value={`${currency} ${fmt(l.fees_balance)}`} />
        <Kpi label="Penalty balance" value={`${currency} ${fmt(l.penalty_balance)}`} />
        <Kpi label="Disbursed" value={l.disbursed_at ? new Date(l.disbursed_at).toLocaleDateString() : '—'} />
      </div>

      {/* ── Tabs ────────────────────────────────────────────── */}
      <div className="card" style={{ marginBottom: 12 }}>
        <div style={{ display: 'flex', gap: 4, padding: '6px 6px 0', borderBottom: '1px solid var(--border)', flexWrap: 'wrap' }}>
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setActiveTab(t.id)}
              style={{
                padding: '8px 14px',
                borderRadius: '6px 6px 0 0',
                border: 'none',
                borderBottom: activeTab === t.id ? '2px solid var(--accent)' : '2px solid transparent',
                background: 'none',
                cursor: 'pointer',
                color: activeTab === t.id ? 'var(--accent)' : 'var(--muted)',
                fontWeight: activeTab === t.id ? 600 : 400,
              }}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div className="card-body">
          {activeTab === 'overview'     && <OverviewTab d={detail} currency={currency} />}
          {activeTab === 'schedule'     && <ScheduleTab rows={detail.schedule} currency={currency} />}
          {activeTab === 'transactions' && <TransactionsTab txns={detail.transactions} currency={currency} />}
          {activeTab === 'guarantors'   && <GuarantorsTab gs={detail.guarantees} currency={currency} />}
          {activeTab === 'collateral'   && <CollateralTab cs={detail.collateral} currency={currency} />}
          {activeTab === 'documents'    && <PlaceholderTab phase="Phase 2" what="Documents (upload + download)" />}
          {activeTab === 'collections'  && <CollectionsTab loanID={id} />}
          {activeTab === 'restructure'  && <RestructureTab txns={detail.transactions} currency={currency} />}
          {activeTab === 'classification' && <ClassificationTab loanID={id} />}
          {activeTab === 'comments'     && <PlaceholderTab phase="Phase 2" what="Comments thread" />}
        </div>
      </div>

      {/* ── Action bar ──────────────────────────────────────── */}
      <div className="card" style={{ position: 'sticky', bottom: 0, background: 'var(--surface)', borderTop: '2px solid var(--accent)' }}>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
          <button className="btn" disabled title="Phase 4">Issue PTP</button>
          {hasPermission('loans:topup') && (
            <button className="btn" onClick={() => setModal('topup')}>Top-up</button>
          )}
          {hasPermission('loans:refinance') && (
            <button className="btn" onClick={() => setModal('refinance')}>Refinance</button>
          )}
          {canRestructure && <button className="btn" disabled title="Use the legacy /loans page until the modals are extracted">Reschedule</button>}
          {canRestructure && <button className="btn" disabled title="Use the legacy /loans page until the modals are extracted">Moratorium</button>}
          {canRestructure && <button className="btn" disabled title="Use the legacy /loans page until the modals are extracted">Settlement discount</button>}
          {canReverse && <button className="btn" disabled title="Use the legacy /loans page until the modals are extracted">Reverse last txn</button>}
          {canWriteoff && <button className="btn btn-danger" disabled title="Use the legacy /loans page until the modals are extracted">Write-off</button>}
          {canApprove && <button className="btn" disabled title="Use the legacy /loans page until the modals are extracted">Settle</button>}
          {canTransact && <button className="btn btn-accent" onClick={() => setModal('repay')}>Make repayment</button>}
        </div>
      </div>

      {modal === 'repay' && (
        <RepayModal loan={l} currency={currency} onClose={() => setModal(null)} onPosted={() => { setModal(null); void refresh(); }} />
      )}
      {modal === 'topup' && (
        <TopUpModal loan={l} onClose={() => setModal(null)} onSubmitted={(appID) => { setModal(null); window.location.assign(`/applications/${appID}`); }} />
      )}
      {modal === 'refinance' && (
        <RefinanceModal loan={l} onClose={() => setModal(null)} onSubmitted={(appID) => { setModal(null); window.location.assign(`/applications/${appID}`); }} />
      )}

      {/* Transitional notice — most action modals still live in the
          legacy /loans page. Phase 2 extracts them into shared
          components and wires them here. */}
      <p className="muted tiny" style={{ marginTop: 12 }}>
        Phase 1 ships the consolidated detail surface. Reschedule / Moratorium /
        Settle / Write-off / Reverse modals stay on the legacy <code>/loans</code> path
        (linked from the dashboard) until they're extracted into shared
        components in Phase 2.
      </p>
    </div>
  );
}

// ─────────────── Tabs ───────────────

function OverviewTab({ d, currency }: { d: LoanDetailType; currency: string }) {
  const l = d.loan;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
      <Kv label="Loan no" value={l.loan_no} mono />
      <Kv label="Status" value={STATUS_LABEL[l.status] ?? l.status} />
      <Kv label="Principal" value={`${currency} ${fmt(l.principal)}`} />
      <Kv label="Interest rate" value={`${l.interest_rate_pct}% ${l.interest_method.replace('_', ' ')}`} />
      <Kv label="Term" value={`${l.term_months} months`} />
      <Kv label="Grace period" value={`${l.grace_period_months ?? 0} months`} />
      <Kv label="First due" value={l.first_due_date ?? '—'} />
      <Kv label="Disbursed" value={l.disbursed_at ? new Date(l.disbursed_at).toLocaleString() : '—'} />
      <Kv label="Net disbursed" value={l.net_disbursed ? `${currency} ${fmt(l.net_disbursed)}` : '—'} />
      <Kv label="Total fees deducted" value={`${currency} ${fmt(l.total_fees_deducted)}`} />
      <Kv label="Disbursement channel" value={l.disbursement_channel ?? '—'} />
      <Kv label="Disbursement ref" value={l.disbursement_ref ?? '—'} mono />
    </div>
  );
}

function ScheduleTab({ rows, currency }: { rows: LoanInstallment[]; currency: string }) {
  if (rows.length === 0) return <div className="empty">No schedule rows.</div>;
  return (
    <div style={{ maxHeight: 480, overflow: 'auto' }}>
      <table className="tbl">
        <thead>
          <tr><th>#</th><th>Due</th><th className="num">Principal</th><th className="num">Interest</th><th className="num">Fee</th><th className="num">Total</th><th>Status</th></tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id} style={{ color: r.status === 'paid' ? 'var(--muted)' : undefined }}>
              <td className="mono">{r.installment_no}</td>
              <td>{r.due_date}</td>
              <td className="num mono">{currency} {fmt(r.principal_due)}</td>
              <td className="num mono">{currency} {fmt(r.interest_due)}</td>
              <td className="num mono">{currency} {fmt(r.fee_due)}</td>
              <td className="num mono">{currency} {fmt(r.total_due)}</td>
              <td>{r.status}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TransactionsTab({ txns, currency }: { txns: LoanTransaction[]; currency: string }) {
  if (txns.length === 0) return <div className="empty">No transactions yet.</div>;
  return (
    <table className="tbl">
      <thead>
        <tr><th>Txn no</th><th>Type</th><th className="num">Amount</th><th>Value date</th><th>Channel</th><th>Reference</th></tr>
      </thead>
      <tbody>
        {txns.map((t) => (
          <tr key={t.id}>
            <td className="mono">{t.txn_no}</td>
            <td>{t.txn_type.replace('_', ' ')}</td>
            <td className="num mono">{currency} {fmt(t.amount)}</td>
            <td>{t.value_date}</td>
            <td>{t.channel ?? '—'}</td>
            <td className="mono">{t.channel_ref ?? '—'}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function GuarantorsTab({ gs, currency }: { gs: LoanGuarantee[]; currency: string }) {
  if (gs.length === 0) return <div className="empty">No guarantors.</div>;
  return (
    <table className="tbl">
      <thead><tr><th>Guarantor</th><th className="num">Amount</th><th>Status</th></tr></thead>
      <tbody>
        {gs.map((g) => (
          <tr key={g.id}>
            <td className="mono">{g.guarantor_member_id}</td>
            <td className="num mono">{currency} {fmt(g.amount_guaranteed)}</td>
            <td>{g.status}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function CollateralTab({ cs, currency }: { cs: LoanCollateralItem[]; currency: string }) {
  if (cs.length === 0) return <div className="empty">No collateral.</div>;
  return (
    <table className="tbl">
      <thead><tr><th>Type</th><th>Description</th><th className="num">Estimated value</th><th>Status</th></tr></thead>
      <tbody>
        {cs.map((c) => (
          <tr key={c.id}>
            <td>{c.kind}</td>
            <td>{c.description}</td>
            <td className="num mono">{currency} {fmt(c.estimated_value)}</td>
            <td>{c.status}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function RestructureTab({ txns, currency }: { txns: LoanTransaction[]; currency: string }) {
  // Phase 1: filter loan_transactions to the restructure event types.
  // Phase 2 wires the dedicated loan_restructurings table for a
  // richer view (before/after schedule comparison, etc).
  const events = txns.filter((t) => ['settlement_discount', 'write_off', 'reversal'].includes(t.txn_type));
  if (events.length === 0) return <div className="empty">No restructure / write-off events on file.</div>;
  return (
    <table className="tbl">
      <thead><tr><th>Date</th><th>Type</th><th className="num">Amount</th><th>Reference</th></tr></thead>
      <tbody>
        {events.map((t) => (
          <tr key={t.id}>
            <td>{t.value_date}</td>
            <td>{t.txn_type.replace('_', ' ')}</td>
            <td className="num mono">{currency} {fmt(t.amount)}</td>
            <td className="mono">{t.channel_ref ?? '—'}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function PlaceholderTab({ phase, what }: { phase: string; what: string }) {
  return (
    <div className="empty">
      {what} — coming in {phase}. The underlying data already exists in the schema;
      this tab is waiting on the matching UI components.
    </div>
  );
}

// ─────────────── Collections timeline + actions (Phase 4) ───────────────

function CollectionsTab({ loanID }: { loanID: string }) {
  const [data, setData] = useState<{ events: CollectionEvent[]; contacts: any[]; ptps: any[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<null | 'call' | 'note' | 'ptp' | 'sms' | 'letter' | 'escalate' | 'legal'>(null);

  const reload = async () => {
    setErr(null);
    try { setData(await loanCollectionsEvents(loanID)); }
    catch (e) { setErr(extractError(e)); }
  };
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [loanID]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading collections timeline…</div>;

  const openPTPs = (data.ptps as any[]).filter((p) => p.status === 'open');

  // Build a unified timeline: events + contacts merged by timestamp,
  // most-recent first.
  type TimelineItem =
    | { kind: 'event'; t: string; ev: CollectionEvent }
    | { kind: 'contact'; t: string; c: any };
  const timeline: TimelineItem[] = [
    ...data.events.map((e): TimelineItem => ({ kind: 'event', t: e.occurred_at, ev: e })),
    ...(data.contacts as any[]).map((c): TimelineItem => ({ kind: 'contact', t: c.contacted_at, c })),
  ].sort((a, b) => (a.t < b.t ? 1 : -1));

  return (
    <div style={{ display: 'grid', gap: 12 }}>
      {/* Open PTPs strip — surfaces prominently at top. */}
      {openPTPs.length > 0 && (
        <div className="card" style={{ borderLeft: '4px solid var(--warn)' }}>
          <div className="card-hd"><h4 style={{ margin: 0 }}>Open promise{openPTPs.length === 1 ? '' : 's'} to pay</h4></div>
          <div className="card-body" style={{ display: 'grid', gap: 8 }}>
            {openPTPs.map((p) => {
              const daysLeft = Math.ceil((new Date(p.promised_date).getTime() - Date.now()) / (1000 * 60 * 60 * 24));
              return (
                <div key={p.id} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span className="mono">{p.promised_amount}</span>
                  <span className="muted">by {p.promised_date}</span>
                  <span style={{ color: daysLeft < 0 ? 'var(--neg)' : daysLeft <= 2 ? 'var(--warn)' : 'var(--muted)' }}>
                    ({daysLeft < 0 ? `${-daysLeft}d overdue` : `${daysLeft}d left`})
                  </span>
                  {p.notes && <span className="muted tiny">— {p.notes}</span>}
                  <button className="btn btn-sm" style={{ marginLeft: 'auto' }} disabled={busy} onClick={async () => {
                    const reason = window.prompt('Cancel reason?');
                    if (!reason) return;
                    setBusy(true);
                    try { await cancelCollectionPTP(loanID, p.id, reason); await reload(); }
                    catch (e) { setErr(extractError(e)); }
                    finally { setBusy(false); }
                  }}>Cancel</button>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Action bar */}
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        <button className="btn" onClick={() => setModal('call')}>Log call</button>
        <button className="btn" onClick={() => setModal('note')}>Add note</button>
        <button className="btn" onClick={() => setModal('ptp')}>Create PTP</button>
        <button className="btn" onClick={() => setModal('sms')}>Send SMS</button>
        <button className="btn" onClick={() => setModal('letter')}>Send letter</button>
        <button className="btn" onClick={() => setModal('escalate')}>Escalate</button>
        <button className="btn btn-danger" onClick={() => setModal('legal')}>Hand to legal</button>
      </div>

      {/* Timeline */}
      <div className="card">
        <div className="card-hd"><h4 style={{ margin: 0 }}>Timeline</h4>
          <span className="card-sub">{timeline.length} entr{timeline.length === 1 ? 'y' : 'ies'}</span></div>
        <div className="card-body" style={{ display: 'grid', gap: 6 }}>
          {timeline.length === 0
            ? <div className="empty">No collections activity yet.</div>
            : timeline.map((it, i) => (
              <div key={i} style={{ display: 'flex', gap: 8, padding: '4px 0', borderBottom: '1px solid var(--border)' }}>
                <span className="muted tiny mono" style={{ minWidth: 130 }}>{new Date(it.t).toLocaleString()}</span>
                {it.kind === 'event'
                  ? <EventLine ev={it.ev} />
                  : <ContactLine c={it.c} />}
              </div>
            ))}
        </div>
      </div>

      {modal && <ActionModal
        loanID={loanID}
        kind={modal}
        onClose={() => setModal(null)}
        onDone={async () => { setModal(null); await reload(); }}
      />}
    </div>
  );
}

function EventLine({ ev }: { ev: CollectionEvent }) {
  const label = ev.kind.replaceAll('_', ' ');
  let detail = '';
  if (ev.kind === 'ptp_created' && ev.amount) detail = `· ${ev.amount} by ${ev.promised_date ?? '—'}`;
  if (ev.kind === 'letter_generated' && ev.letter_kind) detail = `· ${ev.letter_kind}`;
  if (ev.kind === 'note') detail = `: ${(ev.details as any)?.text ?? ''}`;
  return <span><strong>{label}</strong> <span className="muted tiny">{detail}</span></span>;
}

function ContactLine({ c }: { c: any }) {
  return <span><strong>{c.kind}</strong> · {c.outcome}{c.note && <span className="muted tiny"> — {c.note}</span>}</span>;
}

function ActionModal({ loanID, kind, onClose, onDone }: {
  loanID: string;
  kind: 'call' | 'note' | 'ptp' | 'sms' | 'letter' | 'escalate' | 'legal';
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [text, setText] = useState('');
  const [outcome, setOutcome] = useState('reached_promised');
  const [amt, setAmt] = useState('');
  const [date, setDate] = useState('');
  const [letterKind, setLetterKind] = useState<CollectionLetterKind>('demand');
  const [role, setRole] = useState('branch_manager');

  async function submit() {
    setBusy(true); setErr(null);
    try {
      if (kind === 'call') {
        await logCall(loanID, {
          outcome, note: text, duration_seconds: 0,
          promised_amount: outcome === 'reached_promised' ? amt : undefined,
          promised_date: outcome === 'reached_promised' ? date : undefined,
        });
      } else if (kind === 'note') {
        await logCollectionNote(loanID, text);
      } else if (kind === 'ptp') {
        await createCollectionPTP(loanID, { promised_amount: amt, promised_date: date, note: text });
      } else if (kind === 'sms') {
        await sendCollectionSMS(loanID, text);
      } else if (kind === 'letter') {
        await generateCollectionLetter(loanID, letterKind, 'email');
      } else if (kind === 'escalate') {
        await escalateCollection(loanID, role, text);
      } else if (kind === 'legal') {
        await legalHandover(loanID, text);
      }
      await onDone();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  const title = ({
    call: 'Log call', note: 'Add note', ptp: 'Create PTP', sms: 'Send SMS',
    letter: 'Generate letter', escalate: 'Escalate', legal: 'Hand to legal',
  })[kind];

  return (
    <div role="dialog" aria-modal="true" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, width: '90%', maxWidth: 520, padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>{title}</h3>
        {kind === 'call' && (
          <>
            <label><div className="muted tiny">Outcome</div>
              <select className="input" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
                <option value="reached_promised">Reached — promised</option>
                <option value="reached_refused">Reached — refused</option>
                <option value="reached_dispute">Reached — dispute</option>
                <option value="voicemail">Voicemail</option>
                <option value="no_answer">No answer</option>
                <option value="wrong_number">Wrong number</option>
              </select>
            </label>
            {outcome === 'reached_promised' && (
              <div style={{ display: 'flex', gap: 8 }}>
                <label><div className="muted tiny">Promised amount</div>
                  <input className="input" value={amt} onChange={(e) => setAmt(e.target.value)} />
                </label>
                <label><div className="muted tiny">Promised by</div>
                  <input className="input" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
                </label>
              </div>
            )}
            <label><div className="muted tiny">Note</div>
              <textarea className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} />
            </label>
          </>
        )}
        {kind === 'note' && (
          <textarea className="input" rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="Free-form note" />
        )}
        {kind === 'ptp' && (
          <>
            <label><div className="muted tiny">Promised amount</div>
              <input className="input" value={amt} onChange={(e) => setAmt(e.target.value)} />
            </label>
            <label><div className="muted tiny">Promised date</div>
              <input className="input" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
            </label>
            <textarea className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} placeholder="Note (optional)" />
          </>
        )}
        {kind === 'sms' && (
          <textarea className="input" rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="SMS body" />
        )}
        {kind === 'letter' && (
          <label><div className="muted tiny">Letter kind</div>
            <select className="input" value={letterKind} onChange={(e) => setLetterKind(e.target.value as CollectionLetterKind)}>
              <option value="pre_collection">Pre-collection</option>
              <option value="demand">Demand</option>
              <option value="final_demand">Final demand</option>
              <option value="legal_notice">Legal notice</option>
            </select>
          </label>
        )}
        {kind === 'escalate' && (
          <>
            <label><div className="muted tiny">Escalate to role</div>
              <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
                <option value="branch_manager">Branch Manager</option>
                <option value="sacco_admin">SACCO Admin</option>
                <option value="legal">Legal</option>
              </select>
            </label>
            <textarea className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} placeholder="Reason" />
          </>
        )}
        {kind === 'legal' && (
          <textarea className="input" rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="Why is this going to legal? Include the recovery posture (demand letter, court filing, etc.)" />
        )}
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy} onClick={() => void submit()}>{busy ? 'Saving…' : 'Save'}</button>
        </div>
      </div>
    </div>
  );
}

// ─────────────── Classification timeline (Phase 3) ───────────────

function ClassificationTab({ loanID }: { loanID: string }) {
  const [items, setItems] = useState<LoanClassificationHistoryRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void (async () => {
      try {
        const r = await getLoanClassificationHistory(loanID);
        setItems(r.items);
      } catch (e) {
        setErr(extractError(e));
      }
    })();
  }, [loanID]);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!items) return <div className="empty">Loading classification history…</div>;
  if (items.length === 0) return (
    <div className="empty">
      No classification changes yet. The daily DPD classifier writes a row
      each time the SASRA bucket or IFRS 9 stage changes.
    </div>
  );
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>When</th>
          <th>SASRA</th>
          <th>IFRS 9 stage</th>
          <th className="num">DPD</th>
          <th>Trigger</th>
        </tr>
      </thead>
      <tbody>
        {items.map((row, i) => (
          <tr key={i}>
            <td className="mono">{new Date(row.changed_at).toLocaleString()}</td>
            <td>
              {row.prev_sasra && <span className="muted">{row.prev_sasra} → </span>}
              <strong>{row.new_sasra}</strong>
            </td>
            <td className="mono">
              {row.prev_ifrs9_stage != null && <span className="muted">{row.prev_ifrs9_stage} → </span>}
              <strong>{row.new_ifrs9_stage}</strong>
            </td>
            <td className="num mono">{row.dpd_days}</td>
            <td className="muted tiny">{row.trigger_source}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ─────────────── Repay modal ───────────────

function RepayModal({ loan, currency, onClose, onPosted }: {
  loan: LoanDetailType['loan']; currency: string;
  onClose: () => void; onPosted: () => void;
}) {
  const [amount, setAmount] = useState('');
  const [channel, setChannel] = useState('cash');
  const [channelRef, setChannelRef] = useState('');
  const [narration, setNarration] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await repayLoan(loan.id, {
        amount,
        channel,
        channel_ref: channelRef,
        narration: narration || undefined,
      });
      onPosted();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <div role="dialog" aria-modal="true" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, maxWidth: 480, width: '90%', padding: 20 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>Make repayment — {loan.loan_no}</h3>
          <button className="btn btn-sm btn-ghost" onClick={onClose}>×</button>
        </div>
        <div style={{ display: 'grid', gap: 10 }}>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Amount ({currency})</div>
            <input className="input" type="number" step="0.01" min="0" value={amount} onChange={(e) => setAmount(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Channel</div>
            <select className="input" value={channel} onChange={(e) => setChannel(e.target.value)}>
              <option value="cash">Cash</option>
              <option value="mpesa">M-PESA</option>
              <option value="bank">Bank transfer</option>
              <option value="payroll">Payroll</option>
              <option value="auto_savings">Auto from savings</option>
            </select>
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Reference (M-PESA / bank ref)</div>
            <input className="input" type="text" value={channelRef} onChange={(e) => setChannelRef(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Narration</div>
            <input className="input" type="text" value={narration} onChange={(e) => setNarration(e.target.value)} />
          </label>
        </div>
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || !amount} onClick={() => void submit()}>{busy ? 'Posting…' : 'Post repayment'}</button>
        </div>
      </div>
    </div>
  );
}

// ─────────────── Helpers ───────────────

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <div className="card">
      <div className="card-body">
        <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
        <div style={{ fontWeight: 600 }}>{value}</div>
      </div>
    </div>
  );
}

function Kv({ label, value, mono }: { label: string; value: string | number; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 2 }}>{label}</div>
      <div className={mono ? 'mono' : undefined}>{value}</div>
    </div>
  );
}

function fmt(v: string | number | undefined): string {
  const n = typeof v === 'number' ? v : parseFloat(v ?? '0');
  if (!isFinite(n)) return String(v ?? '');
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// ─────────────── Top-up / Refinance modals (Phase 5) ───────────────

function TopUpModal({ loan, onClose, onSubmitted }: {
  loan: LoanDetailType['loan']; onClose: () => void; onSubmitted: (appID: string) => void;
}) {
  const [amount, setAmount] = useState('');
  const [term, setTerm] = useState(String(loan.term_months));
  const [note, setNote] = useState('');
  const [rebroadcast, setRebroadcast] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      const app = await createTopUpApplication({
        base_loan_id: loan.id,
        top_up_amount: amount,
        requested_term_months: parseInt(term, 10),
        purpose_note: note || undefined,
        rebroadcast_consent: rebroadcast,
      });
      onSubmitted(app.id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, width: '90%', maxWidth: 480, padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>Top-up against {loan.loan_no}</h3>
        <p className="muted tiny">Creates a new loan application. On disbursement, the new loan settles the existing one and the member receives the top-up amount.</p>
        <label><div className="muted tiny">Top-up amount</div>
          <input className="input" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="0.00" />
        </label>
        <label><div className="muted tiny">New loan term (months)</div>
          <input className="input" type="number" value={term} onChange={(e) => setTerm(e.target.value)} />
        </label>
        <label><div className="muted tiny">Purpose note (optional)</div>
          <textarea className="input" rows={2} value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <label style={{ display: 'flex', gap: 6, marginTop: 8 }}>
          <input type="checkbox" checked={rebroadcast} onChange={(e) => setRebroadcast(e.target.checked)} />
          <span className="muted tiny">Rebroadcast to existing guarantors for re-consent</span>
        </label>
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !amount || !term} onClick={() => void submit()}>
            {busy ? 'Submitting…' : 'Submit top-up application'}
          </button>
        </div>
      </div>
    </div>
  );
}

function RefinanceModal({ loan, onClose, onSubmitted }: {
  loan: LoanDetailType['loan']; onClose: () => void; onSubmitted: (appID: string) => void;
}) {
  const [otherIDs, setOtherIDs] = useState('');
  const [term, setTerm] = useState(String(loan.term_months));
  const [rate, setRate] = useState('');
  const [note, setNote] = useState('');
  const [rebroadcast, setRebroadcast] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      const ids = [loan.id, ...otherIDs.split(',').map(s => s.trim()).filter(Boolean)];
      const app = await createRefinanceApplication({
        base_loan_ids: ids,
        requested_term_months: parseInt(term, 10),
        requested_interest_rate: rate || undefined,
        purpose_note: note || undefined,
        rebroadcast_consent: rebroadcast,
      });
      onSubmitted(app.id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, width: '90%', maxWidth: 520, padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>Refinance {loan.loan_no}</h3>
        <p className="muted tiny">Creates a new loan that settles the selected loan(s). Use to reschedule, re-price, or consolidate multiple loans of the same member + product.</p>
        <label><div className="muted tiny">Consolidate with other loan IDs (optional, comma-separated)</div>
          <input className="input" value={otherIDs} onChange={(e) => setOtherIDs(e.target.value)} placeholder="loan UUID, loan UUID…" />
        </label>
        <label><div className="muted tiny">New term (months)</div>
          <input className="input" type="number" value={term} onChange={(e) => setTerm(e.target.value)} />
        </label>
        <label><div className="muted tiny">New interest rate % (optional — defaults to product rate)</div>
          <input className="input" value={rate} onChange={(e) => setRate(e.target.value)} />
        </label>
        <label><div className="muted tiny">Purpose note (optional)</div>
          <textarea className="input" rows={2} value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <label style={{ display: 'flex', gap: 6, marginTop: 8 }}>
          <input type="checkbox" checked={rebroadcast} onChange={(e) => setRebroadcast(e.target.checked)} />
          <span className="muted tiny">Rebroadcast to guarantors for re-consent</span>
        </label>
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !term} onClick={() => void submit()}>
            {busy ? 'Submitting…' : 'Submit refinance application'}
          </button>
        </div>
      </div>
    </div>
  );
}
