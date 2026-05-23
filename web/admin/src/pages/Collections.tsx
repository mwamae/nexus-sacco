// Collections queue + case detail (Phase 6e).
//
//   /collections                          → queue (filter + assignment)
//   /collections/<case_id>                → detail (contacts + PTPs + actions)

import { useEffect, useState, type ReactNode } from 'react';
import {
  assignCollectionCase,
  closeCollectionCase,
  createPromiseToPay,
  extractError,
  getCollectionCase,
  listCollectionCases,
  listUsers,
  logCollectionContact,
  resolvePromiseToPay,
  type ApiUserWithRoles,
  type CollectionCase,
  type CollectionCaseDetail,
  type CollectionCaseListItem,
  type CollectionCaseStatus,
  type CollectionContact,
  type CollectionContactKind,
  type ContactOutcome,
  type PromiseToPay,
  type PTPStatus,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

const CLASS_TONE: Record<string, 'pos' | 'neg' | 'warn' | 'accent' | 'neutral'> = {
  performing: 'pos',
  watch: 'accent',
  substandard: 'warn',
  doubtful: 'neg',
  loss: 'neg',
};

const KIND_LABEL: Record<CollectionContactKind, string> = {
  call: 'Phone call',
  sms: 'SMS',
  whatsapp: 'WhatsApp',
  email: 'Email',
  in_person_visit: 'In-person visit',
  letter: 'Letter',
};

const OUTCOME_LABEL: Record<ContactOutcome, string> = {
  reached: 'Reached',
  no_answer: 'No answer',
  wrong_number: 'Wrong number',
  busy: 'Busy',
  left_message: 'Left message',
  promise_made: 'Promise made',
  dispute: 'Dispute',
  refused: 'Refused',
  visited_not_home: 'Visited / not home',
};

export default function CollectionsPage() {
  const path = window.location.pathname;
  const m = /^\/collections\/([0-9a-f-]{36})/.exec(path);
  if (m) return <CaseDetail caseId={m[1]} />;
  return <Queue />;
}

// ─────────── Queue ───────────

function Queue() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [items, setItems] = useState<CollectionCaseListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [status, setStatus] = useState('open');
  const [unassigned, setUnassigned] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function reload() {
    setErr(null);
    try {
      const r = await listCollectionCases({
        status: status || undefined,
        unassigned: unassigned || undefined,
        limit: 200,
      });
      setItems(r.items);
      setTotal(r.total);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status, unassigned]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Lending · Recoveries</div>
          <h1>Collections queue</h1>
          <div className="page-sub">Loans in arrears, classified per CBK/SASRA bands.</div>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-hd">
          <h3>Open cases</h3>
          <span className="card-sub">{total} total</span>
          <div className="card-hd-actions">
            <select className="input" style={{ height: 26, fontSize: 12 }} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All statuses</option>
              {(['open', 'in_progress', 'paused', 'escalated_legal', 'closed_recovered', 'closed_uncollectable'] as CollectionCaseStatus[]).map((s) => (
                <option key={s} value={s}>{s.replace(/_/g, ' ')}</option>
              ))}
            </select>
            <label style={{ display: 'flex', gap: 4, alignItems: 'center', fontSize: 12 }}>
              <input type="checkbox" checked={unassigned} onChange={(e) => setUnassigned(e.target.checked)} />
              <span>unassigned only</span>
            </label>
          </div>
        </div>
        <div className="card-body flush">
          {items.length === 0 ? (
            <div className="empty">No cases match.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Priority</th>
                  <th>Loan #</th>
                  <th>Member</th>
                  <th>Status</th>
                  <th>Class</th>
                  <th style={{ textAlign: 'right' }}>DPD</th>
                  <th style={{ textAlign: 'right' }}>Outstanding</th>
                  <th style={{ textAlign: 'right' }}>Open PTPs</th>
                  <th>Last contact</th>
                  <th>Assignee</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => {
                  const outstanding = (parseFloat(it.loan.principal_balance) + parseFloat(it.loan.interest_balance) + parseFloat(it.loan.fees_balance) + parseFloat(it.loan.penalty_balance)).toFixed(2);
                  return (
                    <tr key={it.case.id}>
                      <td className="mono">{it.case.priority}</td>
                      <td className="tiny-mono">
                        <a className="tbl-link" href={`/collections/${it.case.id}`}>{it.loan.loan_no}</a>
                      </td>
                      <td>
                        <div>{it.member_name}</div>
                        <div className="muted tiny">{it.member_no}</div>
                      </td>
                      <td><StatusBadge status={it.case.status} /></td>
                      <td><Badge tone={CLASS_TONE[it.loan.arrears_classification] ?? 'neutral'}>{it.loan.arrears_classification}</Badge></td>
                      <td className="mono" style={{ textAlign: 'right' }}>{it.loan.days_past_due}d</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(outstanding)}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{it.open_ptps || '—'}</td>
                      <td className="tiny-mono">{it.case.last_contact_at ? it.case.last_contact_at.slice(0, 10) : '—'}</td>
                      <td className="tiny-mono">{it.case.assigned_to ? it.case.assigned_to.slice(0, 8) : '—'}</td>
                      <td>
                        <a className="btn btn-sm" href={`/collections/${it.case.id}`}><Icon name="eye" size={12} /></a>
                      </td>
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

// ─────────── Case detail ───────────

function CaseDetail({ caseId }: { caseId: string }) {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canAct = hasPermission('collections:act');

  const [d, setD] = useState<CollectionCaseDetail | null>(null);
  const [users, setUsers] = useState<ApiUserWithRoles[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [modal, setModal] = useState<null | 'contact' | 'ptp' | 'assign' | 'close'>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try { setD(await getCollectionCase(caseId)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => {
    void load();
    if (canAct) {
      void listUsers().then((r) => setUsers(r.users)).catch(() => {});
    }
    /* eslint-disable-next-line react-hooks/exhaustive-deps */
  }, [caseId]);

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!d) return <div className="page"><div className="empty">Loading…</div></div>;

  const c = d.case;
  const l = d.loan;
  const outstanding = (parseFloat(l.principal_balance) + parseFloat(l.interest_balance) + parseFloat(l.fees_balance) + parseFloat(l.penalty_balance)).toFixed(2);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/collections" style={{ color: 'inherit' }}>Collections</a> · {l.loan_no}</div>
          <h1>Case · {l.loan_no}</h1>
          <div className="page-sub">
            <a href={`/members/${l.counterparty_id}`} className="tbl-link">{l.counterparty_id.slice(0, 8)}…</a> ·
            {' '}{currency} {fmt(l.principal)} loan · {l.interest_rate_pct}% {l.interest_method.replace('_', ' ')}
          </div>
        </div>
        <div className="page-hd-actions">
          <StatusBadge status={c.status} />
          <Badge tone={CLASS_TONE[l.arrears_classification] ?? 'neutral'}>
            DPD {l.days_past_due}d · {l.arrears_classification}
          </Badge>
          {canAct && (
            <>
              <button className="btn btn-sm btn-accent" onClick={() => setModal('contact')}>Log contact</button>
              <button className="btn btn-sm" onClick={() => setModal('ptp')}>Add PTP</button>
              <button className="btn btn-sm" onClick={() => setModal('assign')}>{c.assigned_to ? 'Reassign' : 'Assign'}</button>
              {c.status !== 'closed_recovered' && c.status !== 'closed_uncollectable' && (
                <button className="btn btn-sm" style={{ color: 'var(--neg)' }} onClick={() => setModal('close')}>Close</button>
              )}
            </>
          )}
          <a className="btn btn-sm" href={`/loans/${l.id}`}>Loan detail</a>
        </div>
      </div>

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Outstanding" value={`${currency} ${fmt(outstanding)}`} sub={`P ${fmt(l.principal_balance)} + I ${fmt(l.interest_balance)} + F ${fmt(l.fees_balance)}`} tone="neg" />
        <KPI label="DPD" value={`${l.days_past_due}d`} sub={l.arrears_classification} tone={l.days_past_due > 0 ? 'neg' : 'pos'} />
        <KPI label="Contacts" value={String(c.total_contacts)} sub={c.last_contact_at ? `last ${c.last_contact_at.slice(0, 10)}` : 'none yet'} />
        <KPI label="Priority" value={String(c.priority)} sub="0–100" />
      </div>

      {c.last_action && (
        <p className="muted tiny" style={{ marginBottom: 14 }}>Last action: <strong>{c.last_action}</strong></p>
      )}

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Promises to pay</h3>
          <span className="card-sub">{d.ptps.length} total</span>
        </div>
        <div className="card-body flush">
          {d.ptps.length === 0 ? (
            <div className="empty">No PTPs recorded.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Status</th>
                  <th>Promised</th>
                  <th style={{ textAlign: 'right' }}>Amount</th>
                  <th>Channel</th>
                  <th style={{ textAlign: 'right' }}>Paid</th>
                  <th>Resolved</th>
                  {canAct && <th style={{ width: 1 }}></th>}
                </tr>
              </thead>
              <tbody>
                {d.ptps.map((p) => (
                  <tr key={p.id}>
                    <td><Badge tone={ptpTone(p.status)}>{p.status}</Badge></td>
                    <td className="tiny-mono">{p.promised_date.slice(0, 10)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(p.promised_amount)}</td>
                    <td className="tiny">{p.promised_channel ?? '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(p.paid_amount)}</td>
                    <td className="tiny-mono">{p.resolved_at ? p.resolved_at.slice(0, 10) : '—'}</td>
                    {canAct && (
                      <td>
                        {p.status === 'open' && (
                          <div className="row" style={{ gap: 4 }}>
                            <button className="btn btn-sm" disabled={!!busy} onClick={() => void resolveIt(p, 'kept')}>Kept</button>
                            <button className="btn btn-sm" disabled={!!busy} onClick={() => void resolveIt(p, 'broken')}>Broken</button>
                          </div>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Contact log</h3>
          <span className="card-sub">{d.contacts.length} attempts</span>
        </div>
        <div className="card-body flush">
          {d.contacts.length === 0 ? (
            <div className="empty">No contact attempts logged yet.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr><th>When</th><th>Kind</th><th>Outcome</th><th>Note</th><th>By</th></tr>
              </thead>
              <tbody>
                {d.contacts.map((x) => (
                  <tr key={x.id}>
                    <td className="tiny-mono">{x.contacted_at.slice(0, 16).replace('T', ' ')}</td>
                    <td><Badge tone="accent">{KIND_LABEL[x.kind]}</Badge></td>
                    <td><Badge tone={outcomeTone(x.outcome)}>{OUTCOME_LABEL[x.outcome]}</Badge></td>
                    <td className="tiny">{x.note ?? '—'}</td>
                    <td className="tiny-mono">{x.contacted_by.slice(0, 8)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {modal === 'contact' && canAct && (
        <ContactModal caseId={caseId} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'ptp' && canAct && (
        <PTPModal caseId={caseId} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'assign' && canAct && (
        <AssignModal caseId={caseId} users={users} currentAssignee={c.assigned_to ?? null}
          onClose={() => setModal(null)} onAssigned={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'close' && canAct && (
        <CloseModal caseId={caseId} onClose={() => setModal(null)} onClosed={async () => { setModal(null); await load(); }} />
      )}
    </div>
  );

  async function resolveIt(p: PromiseToPay, status: PTPStatus) {
    setBusy(p.id);
    try {
      const paid = status === 'kept' ? p.promised_amount : '0';
      await resolvePromiseToPay(p.id, status as 'kept' | 'partial' | 'broken' | 'cancelled', paid);
      await load();
    } catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
}

function ptpTone(s: PTPStatus): 'pos' | 'neg' | 'warn' | 'neutral' {
  switch (s) {
    case 'kept': return 'pos';
    case 'broken': return 'neg';
    case 'partial': return 'warn';
    case 'cancelled': return 'neutral';
    default: return 'warn';
  }
}

function outcomeTone(o: ContactOutcome): 'pos' | 'neg' | 'warn' | 'neutral' | 'accent' {
  switch (o) {
    case 'reached': case 'promise_made': return 'pos';
    case 'refused': case 'wrong_number': return 'neg';
    case 'no_answer': case 'busy': case 'visited_not_home': case 'left_message': return 'warn';
    case 'dispute': return 'accent';
    default: return 'neutral';
  }
}

// ─────────── Modals ───────────

function ContactModal({ caseId, onClose, onPosted }: { caseId: string; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [kind, setKind] = useState<CollectionContactKind>('call');
  const [outcome, setOutcome] = useState<ContactOutcome>('reached');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try { await logCollectionContact(caseId, { kind, outcome, note: note || undefined }); await onPosted(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title="Log contact attempt" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Save contact">
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Kind">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value as CollectionContactKind)}>
          {(Object.keys(KIND_LABEL) as CollectionContactKind[]).map((k) => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
        </select>
      </Field>
      <Field label="Outcome">
        <select className="input" value={outcome} onChange={(e) => setOutcome(e.target.value as ContactOutcome)}>
          {(Object.keys(OUTCOME_LABEL) as ContactOutcome[]).map((o) => <option key={o} value={o}>{OUTCOME_LABEL[o]}</option>)}
        </select>
      </Field>
      <Field label="Note (optional)">
        <textarea className="input" rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
    </ModalShell>
  );
}

function PTPModal({ caseId, onClose, onPosted }: { caseId: string; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [amount, setAmount] = useState('');
  const [date, setDate] = useState(new Date(Date.now() + 7 * 86400e3).toISOString().slice(0, 10));
  const [channel, setChannel] = useState('mpesa');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await createPromiseToPay(caseId, { promised_amount: amount, promised_date: date, promised_channel: channel, notes: notes || undefined });
      await onPosted();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title="Record promise-to-pay" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Save PTP"
      disabled={!amount || parseFloat(amount) <= 0 || !date}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Promised amount">
        <input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="0.00" />
      </Field>
      <Field label="Promised date">
        <input className="input mono" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
      </Field>
      <Field label="Channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value)}>
          <option value="mpesa">M-Pesa</option>
          <option value="bank">Bank</option>
          <option value="teller">Teller</option>
          <option value="auto_savings">Auto-debit from savings</option>
        </select>
      </Field>
      <Field label="Notes (optional)">
        <textarea className="input" rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} />
      </Field>
    </ModalShell>
  );
}

function AssignModal({ caseId, users, currentAssignee, onClose, onAssigned }: {
  caseId: string; users: ApiUserWithRoles[]; currentAssignee: string | null;
  onClose: () => void; onAssigned: () => Promise<void> | void;
}) {
  const [userId, setUserId] = useState(currentAssignee ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    if (!userId) { setErr('Pick an officer'); return; }
    setBusy(true); setErr(null);
    try { await assignCollectionCase(caseId, userId); await onAssigned(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title="Assign case to a collections officer" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Assign">
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Officer">
        <select className="input" value={userId} onChange={(e) => setUserId(e.target.value)}>
          <option value="">— pick —</option>
          {users.map((u) => <option key={u.id} value={u.id}>{u.full_name} ({u.email})</option>)}
        </select>
      </Field>
    </ModalShell>
  );
}

function CloseModal({ caseId, onClose, onClosed }: { caseId: string; onClose: () => void; onClosed: () => Promise<void> | void }) {
  const [recovered, setRecovered] = useState(true);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    if (!reason.trim()) { setErr('Reason is required'); return; }
    setBusy(true); setErr(null);
    try { await closeCollectionCase(caseId, recovered, reason); await onClosed(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title="Close collection case" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Close case">
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Outcome">
        <select className="input" value={recovered ? 'recovered' : 'uncollectable'} onChange={(e) => setRecovered(e.target.value === 'recovered')}>
          <option value="recovered">Recovered — member back in good standing</option>
          <option value="uncollectable">Uncollectable — escalate or write off</option>
        </select>
      </Field>
      <Field label="Reason">
        <textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} />
      </Field>
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

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

function fmt(s: string | number | undefined): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

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
      <div className="card" style={{ width: width ?? 520, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
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
