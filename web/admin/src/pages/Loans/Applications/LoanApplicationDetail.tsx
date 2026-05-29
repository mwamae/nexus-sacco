// /loans/applications/{id} — Loan Application Detail (Phase 1).
//
// The consolidated workflow page per §7.3 of the strategic doc:
//
//   Header           : app no, member, product, amount/term, status,
//                      age, officer, score, risk band
//   Decision summary : populated once scoring has run; shows
//                      affordability, multiplier, DTI, recommended
//                      decision
//   Tabs             : Overview / Income / Guarantors / Collateral
//                      / Documents / Score / Schedule preview /
//                      Timeline / Comments
//   Action bar       : Approve / Approve with conditions /
//                      Counter-offer / Return for info / Decline
//
// Counter-offer is the Phase 1 NEW UX (rest are existing API calls).
// The modal lets the officer adjust approved_amount / term /
// interest_rate, then submits via approveLoanApplication — same
// endpoint as plain "Approve" but with the new values. The original
// requested_* fields stay on the application for audit.
//
// Permission: loans:view to read; loans:approve to act on
// approve/decline/counter-offer; loans:assess to use "Return for
// info" (which re-routes through the same approve endpoint).

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../../auth/AuthContext';
import {
  getLoanApplication,
  approveLoanApplication,
  declineLoanApplication,
  rescoreLoanApplication,
  type LoanAppDetail,
  type LoanApplication,
  type LoanGuarantee,
  type LoanCollateralItem,
  extractError,
} from '../../../api/client';
import { useDocumentTitle } from '../../../lib/useDocumentTitle';

type TabId = 'overview' | 'income' | 'guarantors' | 'collateral' | 'documents' | 'score' | 'schedule' | 'timeline' | 'comments';

const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',   label: 'Overview' },
  { id: 'income',     label: 'Income' },
  { id: 'guarantors', label: 'Guarantors' },
  { id: 'collateral', label: 'Collateral' },
  { id: 'documents',  label: 'Documents' },
  { id: 'score',      label: 'Score' },
  { id: 'schedule',   label: 'Schedule preview' },
  { id: 'timeline',   label: 'Timeline' },
  { id: 'comments',   label: 'Comments' },
];

const STATUS_LABEL: Record<string, string> = {
  draft: 'Draft', pending_validation: 'Pending validation',
  pending_guarantor: 'Pending guarantor', pending_scoring: 'Pending scoring',
  pending_approval: 'Pending approval', approved: 'Approved',
  approved_with_conditions: 'Approved (conditions)', declined: 'Declined',
  returned_for_info: 'Returned for info', offer_sent: 'Offer sent',
  offer_accepted: 'Offer accepted', offer_declined: 'Offer declined',
  expired: 'Expired', cancelled: 'Cancelled', disbursed: 'Disbursed',
};

// Action-bar visibility per status. Each row encodes "can this
// status accept this action?" — keeps the action-bar branching
// out of inline JSX.
const ACTIONS_PER_STATUS: Record<string, ('approve' | 'decline' | 'return' | 'counter')[]> = {
  pending_approval:        ['approve', 'decline', 'return', 'counter'],
  pending_scoring:         ['return'],
  pending_guarantor:       ['return'],
  pending_validation:      ['return'],
  approved_with_conditions:['counter'],
  returned_for_info:       [],
  approved:                [],
  declined:                [],
  offer_sent:              [],
  offer_accepted:          [],
  offer_declined:          [],
  expired:                 [],
  cancelled:               [],
  disbursed:               [],
  draft:                   [],
};

export default function LoanApplicationDetail() {
  useDocumentTitle('Loans · Application');
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canView = hasPermission('loans:view');
  const canApprove = hasPermission('loans:approve');
  const canAssess = hasPermission('loans:assess');

  const id = useMemo(() => {
    const parts = window.location.pathname.split('/').filter(Boolean);
    return parts[parts.length - 1] ?? '';
  }, []);

  const [detail, setDetail] = useState<LoanAppDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [activeTab, setActiveTab] = useState<TabId>('overview');

  // Modal state — each opens to a focused action form.
  const [modal, setModal] = useState<null | 'approve' | 'decline' | 'return' | 'counter'>(null);

  const refresh = useCallback(async () => {
    if (!id) return;
    try {
      const d = await getLoanApplication(id);
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
    return (
      <div className="page">
        <div className="page-hd"><h1>Loan application</h1></div>
        <div className="alert alert-warn">You need <code>loans:view</code> permission.</div>
      </div>
    );
  }
  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!detail) return <div className="page"><div className="empty">Loading application…</div></div>;

  const a = detail.application;
  const actions = ACTIONS_PER_STATUS[a.status] ?? [];

  async function rescore() {
    setBusy(true);
    try {
      await rescoreLoanApplication(id);
      await refresh();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page">
      {/* ── Header ──────────────────────────────────────────── */}
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/loans/applications" style={{ color: 'inherit' }}>← Applications</a>
          </div>
          <h1>
            <span className="mono">{a.application_no}</span>
            <span style={{ marginLeft: 12, fontSize: 14, fontWeight: 400, color: 'var(--muted)' }}>
              {STATUS_LABEL[a.status] ?? a.status}
            </span>
          </h1>
          <div className="page-sub">
            Requested {currency} {fmt(a.requested_amount)} over {a.requested_term_months}m
            {a.credit_score !== undefined && (
              <> · Score <strong>{a.credit_score}</strong>{a.risk_band ? ` (${a.risk_band})` : ''}</>
            )}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {canAssess && (
            <button className="btn" disabled={busy} onClick={() => void rescore()}>↻ Re-score</button>
          )}
        </div>
      </div>

      {/* ── Decision summary card (visible once scored) ───────── */}
      {a.scored_at && (
        <div className="card" style={{ marginBottom: 12 }}>
          <div className="card-hd"><h3>Decision summary</h3></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 12 }}>
            <Kv label="Affordability" value={a.affordability_pass === true ? '✓ Pass' : a.affordability_pass === false ? '✗ Fail' : '—'} />
            <Kv label="DTI ratio" value={a.dti_ratio ?? '—'} />
            <Kv label="Net disposable" value={`${currency} ${fmt(a.net_disposable_income)}`} />
            <Kv label="Max amount" value={`${currency} ${fmt(a.computed_max_amount)}`} />
            <Kv label="Max installment" value={`${currency} ${fmt(a.computed_max_installment)}`} />
            <Kv label="Recommended" value={`${currency} ${fmt(a.recommended_amount)} · ${a.recommended_term_months ?? '—'}m`} />
            <Kv label="CRB" value="— (Phase 6)" />
            <Kv label="Guarantor coverage" value={`${detail.guarantees.length} guarantor${detail.guarantees.length === 1 ? '' : 's'}`} />
            <Kv label="Collateral" value={`${detail.collateral.length} item${detail.collateral.length === 1 ? '' : 's'}`} />
          </div>
        </div>
      )}

      {/* ── Tabs ─────────────────────────────────────────────── */}
      <div className="card" style={{ marginBottom: 12 }}>
        <div className="tabs" style={{ display: 'flex', gap: 4, padding: '6px 6px 0', borderBottom: '1px solid var(--border)', flexWrap: 'wrap' }}>
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
          {activeTab === 'overview'   && <OverviewTab a={a} currency={currency} />}
          {activeTab === 'income'     && <IncomeTab a={a} currency={currency} />}
          {activeTab === 'guarantors' && <GuarantorsTab gs={detail.guarantees} currency={currency} />}
          {activeTab === 'collateral' && <CollateralTab cs={detail.collateral} currency={currency} />}
          {activeTab === 'documents'  && <DocumentsTab />}
          {activeTab === 'score'      && <ScoreTab a={a} />}
          {activeTab === 'schedule'   && <ScheduleTab d={detail} currency={currency} />}
          {activeTab === 'timeline'   && <TimelineTab a={a} />}
          {activeTab === 'comments'   && <CommentsTab />}
        </div>
      </div>

      {/* ── Action bar ───────────────────────────────────────── */}
      {actions.length > 0 && canApprove && (
        <div className="card" style={{
          position: 'sticky', bottom: 0, background: 'var(--surface)',
          borderTop: '2px solid var(--accent)',
        }}>
          <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
            {actions.includes('return') && (
              <button className="btn" disabled={busy} onClick={() => setModal('return')}>Return for info</button>
            )}
            {actions.includes('counter') && (
              <button className="btn" disabled={busy} onClick={() => setModal('counter')}>Counter-offer</button>
            )}
            {actions.includes('decline') && (
              <button className="btn btn-danger" disabled={busy} onClick={() => setModal('decline')}>Decline</button>
            )}
            {actions.includes('approve') && (
              <button className="btn btn-accent" disabled={busy} onClick={() => setModal('approve')}>Approve as-is</button>
            )}
          </div>
        </div>
      )}

      {/* ── Modals ───────────────────────────────────────────── */}
      {modal === 'approve' && (
        <ApproveModal app={a} currency={currency} onClose={() => setModal(null)} onSaved={() => { setModal(null); void refresh(); }} />
      )}
      {modal === 'counter' && (
        <CounterOfferModal app={a} currency={currency} onClose={() => setModal(null)} onSaved={() => { setModal(null); void refresh(); }} />
      )}
      {modal === 'decline' && (
        <DeclineModal app={a} onClose={() => setModal(null)} onSaved={() => { setModal(null); void refresh(); }} />
      )}
      {modal === 'return' && (
        <ReturnForInfoModal app={a} onClose={() => setModal(null)} onSaved={() => { setModal(null); void refresh(); }} />
      )}
    </div>
  );
}

// ─────────────── Tabs ───────────────

function OverviewTab({ a, currency }: { a: LoanApplication; currency: string }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
      <Kv label="Application no" value={a.application_no} mono />
      <Kv label="Status" value={STATUS_LABEL[a.status] ?? a.status} />
      <Kv label="Requested amount" value={`${currency} ${fmt(a.requested_amount)}`} />
      <Kv label="Requested term" value={`${a.requested_term_months} months`} />
      <Kv label="Approved amount" value={a.approved_amount ? `${currency} ${fmt(a.approved_amount)}` : '—'} />
      <Kv label="Approved term" value={a.approved_term_months ? `${a.approved_term_months} months` : '—'} />
      <Kv label="Approved rate" value={a.approved_interest_rate_pct ? `${a.approved_interest_rate_pct}%` : '—'} />
      <Kv label="Created" value={new Date(a.created_at).toLocaleString()} />
      {a.purpose_note && <Kv label="Purpose" value={a.purpose_note} />}
      {a.approval_conditions && <Kv label="Conditions" value={a.approval_conditions} />}
      {a.decline_reason && <Kv label="Decline reason" value={`${a.decline_category ?? ''} · ${a.decline_reason}`} />}
    </div>
  );
}

function IncomeTab({ a, currency }: { a: LoanApplication; currency: string }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
      <Kv label="Employment" value={a.employment_type ?? '—'} />
      <Kv label="Employer" value={a.employer_name ?? '—'} />
      <Kv label="Monthly net income" value={`${currency} ${fmt(a.monthly_net_income)}`} />
      <Kv label="Other income" value={`${currency} ${fmt(a.other_income)}`} />
      <Kv label="Monthly expenses" value={`${currency} ${fmt(a.monthly_expenses)}`} />
      <Kv label="Monthly obligations" value={`${currency} ${fmt(a.monthly_existing_obligations)}`} />
    </div>
  );
}

function GuarantorsTab({ gs, currency }: { gs: LoanGuarantee[]; currency: string }) {
  if (gs.length === 0) return <div className="empty">No guarantors yet.</div>;
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Guarantor</th>
          <th>Member no</th>
          <th className="num">Amount</th>
          <th>Status</th>
          <th>Requested</th>
        </tr>
      </thead>
      <tbody>
        {gs.map((g) => (
          <tr key={g.id}>
            <td>
              {g.guarantor_name
                ? g.guarantor_name
                : <span className="muted tiny mono" title={g.guarantor_member_id}>
                    {g.guarantor_member_id.slice(0, 8)}…
                  </span>}
            </td>
            <td className="mono">{g.guarantor_member_no || <span className="muted">—</span>}</td>
            <td className="num mono">{currency} {fmt(g.amount_guaranteed)}</td>
            <td>{g.status}</td>
            <td className="tiny muted">{new Date(g.requested_at).toLocaleDateString()}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function CollateralTab({ cs, currency }: { cs: LoanCollateralItem[]; currency: string }) {
  if (cs.length === 0) return <div className="empty">No collateral on file.</div>;
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

function DocumentsTab() {
  return (
    <div className="empty">
      Documents tab — Phase 2 wires upload/download. The
      loan_documents table already exists; this just needs the
      multipart endpoint + file-list wiring.
    </div>
  );
}

function ScoreTab({ a }: { a: LoanApplication }) {
  if (!a.scoring_details && !a.scoring_flags) {
    return <div className="empty">No scoring data yet. Hit Re-score in the header to compute.</div>;
  }
  return (
    <div>
      <h4>Scoring details</h4>
      <pre style={{ background: 'var(--surface-2)', padding: 12, borderRadius: 4, overflow: 'auto', maxHeight: 400 }}>
        {JSON.stringify(a.scoring_details ?? {}, null, 2)}
      </pre>
      <h4 style={{ marginTop: 16 }}>Flags</h4>
      <pre style={{ background: 'var(--surface-2)', padding: 12, borderRadius: 4, overflow: 'auto', maxHeight: 400 }}>
        {JSON.stringify(a.scoring_flags ?? [], null, 2)}
      </pre>
    </div>
  );
}

function ScheduleTab({ d, currency }: { d: LoanAppDetail; currency: string }) {
  const s = d.schedule;
  if (!s) return <div className="empty">Schedule preview unavailable — application needs to be scored first.</div>;
  return (
    <>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12, marginBottom: 12 }}>
        <Kv label="Principal" value={`${currency} ${fmt(s.principal)}`} />
        <Kv label="Interest" value={`${currency} ${fmt(s.total_interest)}`} />
        <Kv label="Installment" value={`${currency} ${fmt(s.installment)}`} />
        <Kv label="Net disbursed" value={`${currency} ${fmt(s.net_disbursed)}`} />
        <Kv label="Total payable" value={`${currency} ${fmt(s.total_payable)}`} />
        <Kv label="First due" value={s.first_due_date} />
      </div>
      <table className="tbl">
        <thead><tr><th>#</th><th>Due</th><th className="num">Principal</th><th className="num">Interest</th><th className="num">Fee</th><th className="num">Total</th></tr></thead>
        <tbody>
          {s.rows.map((row) => (
            <tr key={row.installment_no}>
              <td className="mono">{row.installment_no}</td>
              <td>{row.due_date}</td>
              <td className="num mono">{fmt(row.principal_due)}</td>
              <td className="num mono">{fmt(row.interest_due)}</td>
              <td className="num mono">{fmt(row.fee_due)}</td>
              <td className="num mono">{fmt(row.total_due)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

function TimelineTab({ a }: { a: LoanApplication }) {
  // Phase 1 — render the lifecycle events we have inline on the app
  // row. Phase 2 wires the audit_log feed for the richer timeline.
  const events: { at: string | undefined; label: string }[] = [
    { at: a.created_at, label: 'Application created' },
    { at: a.scored_at, label: 'Scoring computed' },
    { at: a.approved_at, label: 'Approved' },
    { at: a.offer_sent_at, label: 'Offer sent' },
    { at: a.offer_accepted_at, label: 'Offer accepted' },
  ].filter((e) => !!e.at);
  if (events.length === 0) return <div className="empty">No timeline events yet.</div>;
  return (
    <ol style={{ listStyle: 'none', margin: 0, padding: 0 }}>
      {events.map((e, i) => (
        <li key={i} style={{ padding: '8px 0', borderBottom: '1px solid var(--border)' }}>
          <div className="mono tiny muted">{new Date(e.at!).toLocaleString()}</div>
          <div>{e.label}</div>
        </li>
      ))}
    </ol>
  );
}

function CommentsTab() {
  return (
    <div className="empty">
      Comments tab — Phase 2 wires the loan_comments table (or audit-
      log-backed thread; design TBD).
    </div>
  );
}

// ─────────────── Modals ───────────────

function ApproveModal({ app, currency, onClose, onSaved }: {
  app: LoanApplication; currency: string;
  onClose: () => void; onSaved: () => void;
}) {
  const [conditions, setConditions] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await approveLoanApplication(app.id, { approval_conditions: conditions || undefined });
      onSaved();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <Modal title="Approve application" onClose={onClose}>
      <p className="muted tiny">Approving as-is: keeps requested terms ({currency} {fmt(app.requested_amount)} · {app.requested_term_months}m).</p>
      <label>
        <div className="muted tiny" style={{ marginBottom: 4 }}>Conditions (optional)</div>
        <textarea className="input" value={conditions} onChange={(e) => setConditions(e.target.value)} rows={3} />
      </label>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <ModalActions>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn btn-accent" disabled={busy} onClick={() => void submit()}>{busy ? 'Approving…' : 'Approve'}</button>
      </ModalActions>
    </Modal>
  );
}

function CounterOfferModal({ app, currency, onClose, onSaved }: {
  app: LoanApplication; currency: string;
  onClose: () => void; onSaved: () => void;
}) {
  const [amount, setAmount] = useState(app.recommended_amount ?? app.requested_amount);
  const [term, setTerm] = useState(String(app.recommended_term_months ?? app.requested_term_months));
  const [rate, setRate] = useState(app.approved_interest_rate_pct ?? '');
  const [conditions, setConditions] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      // Counter-offer reuses the approve endpoint with adjusted
      // approved_* fields. The member sees the counter on the offer
      // step — they can accept (continue) or decline (cancel).
      await approveLoanApplication(app.id, {
        approved_amount: amount,
        approved_term_months: parseInt(term, 10),
        approved_interest_rate_pct: rate || undefined,
        approval_conditions: conditions || 'Counter-offer: amount/term/rate adjusted from request.',
      });
      onSaved();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <Modal title="Counter-offer" onClose={onClose}>
      <p className="muted tiny">
        Submit a counter-offer with different amount / term / rate.
        Member sees the adjusted terms on the offer step and decides
        whether to accept.
      </p>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
        <label>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Amount ({currency})</div>
          <input className="input" type="number" step="0.01" value={amount} onChange={(e) => setAmount(e.target.value)} />
        </label>
        <label>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Term (months)</div>
          <input className="input" type="number" min="1" value={term} onChange={(e) => setTerm(e.target.value)} />
        </label>
        <label>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Interest rate (%, optional)</div>
          <input className="input" type="number" step="0.01" value={rate} onChange={(e) => setRate(e.target.value)} placeholder="(use product default)" />
        </label>
      </div>
      <label style={{ display: 'block', marginTop: 8 }}>
        <div className="muted tiny" style={{ marginBottom: 4 }}>Note to member</div>
        <textarea className="input" value={conditions} onChange={(e) => setConditions(e.target.value)} rows={3}
          placeholder="Why are we proposing different terms?" />
      </label>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <ModalActions>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn btn-accent" disabled={busy} onClick={() => void submit()}>{busy ? 'Submitting…' : 'Submit counter-offer'}</button>
      </ModalActions>
    </Modal>
  );
}

function DeclineModal({ app, onClose, onSaved }: {
  app: LoanApplication; onClose: () => void; onSaved: () => void;
}) {
  const [category, setCategory] = useState('insufficient_income');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await declineLoanApplication(app.id, category, reason);
      onSaved();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <Modal title="Decline application" onClose={onClose}>
      <label>
        <div className="muted tiny" style={{ marginBottom: 4 }}>Category</div>
        <select className="input" value={category} onChange={(e) => setCategory(e.target.value)}>
          <option value="insufficient_income">Insufficient income</option>
          <option value="excessive_dti">Excessive DTI</option>
          <option value="insufficient_collateral">Insufficient collateral / guarantors</option>
          <option value="adverse_credit">Adverse credit history</option>
          <option value="other">Other</option>
        </select>
      </label>
      <label style={{ display: 'block', marginTop: 8 }}>
        <div className="muted tiny" style={{ marginBottom: 4 }}>Reason</div>
        <textarea className="input" value={reason} onChange={(e) => setReason(e.target.value)} rows={4} required />
      </label>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <ModalActions>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn btn-danger" disabled={busy || !reason} onClick={() => void submit()}>{busy ? 'Declining…' : 'Decline'}</button>
      </ModalActions>
    </Modal>
  );
}

function ReturnForInfoModal({ app, onClose, onSaved }: {
  app: LoanApplication; onClose: () => void; onSaved: () => void;
}) {
  // Phase 1: "return for info" piggybacks on decline with a
  // distinct category so the row goes back to returned_for_info via
  // a follow-up status flip. If the backend has a dedicated endpoint
  // for this transition, swap it in here. For now we mark the
  // request informational and surface a clear category.
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await declineLoanApplication(app.id, 'returned_for_info', reason);
      onSaved();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <Modal title="Return for info" onClose={onClose}>
      <p className="muted tiny">
        Ask the member or originator for more information. The
        application moves to "returned for info"; once details are
        re-submitted it goes back to pending_approval.
      </p>
      <label>
        <div className="muted tiny" style={{ marginBottom: 4 }}>What's missing?</div>
        <textarea className="input" value={reason} onChange={(e) => setReason(e.target.value)} rows={4} required />
      </label>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <ModalActions>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={busy || !reason} onClick={() => void submit()}>{busy ? 'Submitting…' : 'Return for info'}</button>
      </ModalActions>
    </Modal>
  );
}

// ─────────────── Shared helpers ───────────────

function Kv({ label, value, mono }: { label: string; value: string | number; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 2 }}>{label}</div>
      <div className={mono ? 'mono' : undefined}>{value}</div>
    </div>
  );
}

function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div role="dialog" aria-modal="true" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div style={{
        background: 'var(--surface)', borderRadius: 8, maxWidth: 600, width: '90%',
        maxHeight: '90vh', overflow: 'auto', padding: 20,
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>{title}</h3>
          <button className="btn btn-sm btn-ghost" onClick={onClose}>×</button>
        </div>
        {children}
      </div>
    </div>
  );
}

function ModalActions({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
      {children}
    </div>
  );
}

function fmt(s: string | number | undefined): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
