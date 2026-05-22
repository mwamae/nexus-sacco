// Application detail + reviewer checklist + approver actions.
//
// One page covers the entire post-submission lifecycle. The visible
// action set depends on the application's current status:
//   submitted                 → reviewer can Start review
//   under_review              → reviewer responds to checklist, then either
//                               Submit for approval, Return for correction, or Withdraw
//   returned_for_correction   → officer can Resubmit
//   reviewed_pending_approval → approver can Approve, Approve with conditions,
//                               Decline, or Return to reviewer
//   approved_active | declined | withdrawn → terminal; read-only

import { useEffect, useState } from 'react';
import {
  approveApplication,
  declineApplication,
  getApplication,
  postRegistrationFeeRefund,
  respondToChecklist,
  resubmitApplication,
  returnForCorrection,
  returnToReviewer,
  startReview,
  submitForApproval,
  withdrawApplication,
  type ApplicationDetail,
  type ApplicationStatus,
  type ChecklistItem,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const STATUS_LABEL: Record<ApplicationStatus, string> = {
  submitted: 'Pending review',
  under_review: 'Under review',
  returned_for_correction: 'Returned for correction',
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
const RESPONSE_COLOR: Record<string, string> = {
  confirmed: 'var(--pos)',
  flagged: 'var(--neg)',
  'n/a': 'var(--muted)',
};

export default function ApplicationDetailPage() {
  const { tenant } = useAuth();
  const id = window.location.pathname.split('/').pop() ?? '';
  const [data, setData] = useState<ApplicationDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);

  // Action prompts
  const [returnNote, setReturnNote] = useState('');
  const [reviewSummary, setReviewSummary] = useState('');
  const [declineReason, setDeclineReason] = useState('');
  const [approveConditions, setApproveConditions] = useState('');
  const [withdrawReason, setWithdrawReason] = useState('');

  async function load() {
    setErr(null);
    try { setData(await getApplication(id)); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [id]);

  async function action(fn: () => Promise<unknown>, ok: string) {
    setBusy(true); setErr(null); setInfo(null);
    try { await fn(); setInfo(ok); await load(); }
    catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function onChecklist(item: ChecklistItem, response: 'confirmed' | 'flagged' | 'n/a') {
    const note = response === 'flagged' ? (prompt(`Flag note for "${item.label}":`, '') ?? '') : '';
    await action(() => respondToChecklist(id, { code: item.code, response, note: note || undefined }), `${item.label}: ${response}`);
  }

  if (!data) {
    return (
      <div className="page">
        {err && <div className="alert alert-error">{err}</div>}
        {!err && <div className="muted">Loading…</div>}
      </div>
    );
  }

  const a = data.application;
  const responses = new Map(data.checklist_responses.map((r) => [r.checklist_code, r]));
  const allMandatoryAddressed = data.checklist_items
    .filter((it) => it.mandatory)
    .every((it) => responses.has(it.code) && responses.get(it.code)!.response !== 'flagged');

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Members · Application</div>
          <h1>{a.applicant_name}</h1>
          <div className="page-sub">
            <span className="mono">{a.application_no}</span> · {a.kind}{a.entity_type ? ` (${a.entity_type})` : ''} ·
            <span style={{ color: STATUS_COLOR[a.status], fontWeight: 600, marginLeft: 6 }}>
              {STATUS_LABEL[a.status]}
            </span>
          </div>
        </div>
        <a className="btn" href="/applications">← Queue</a>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {info && <div className="alert" style={{ marginTop: 12, background: 'var(--pos-bg, #e6f5ea)', borderColor: 'var(--pos)' }}>{info}</div>}

      {a.status === 'approved_active' && a.materialized_member_id && (
        <div className="card" style={{ marginTop: 12, borderColor: 'var(--pos)' }}>
          <div className="card-hd"><h3>Activation</h3><span className="card-sub" style={{ color: 'var(--pos)' }}>✓ materialized</span></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16 }}>
            <Field label="Member ID" value={a.materialized_member_id.slice(0, 8) + '…'} mono />
            <Field label="Materialized at" value={a.materialized_at?.slice(0, 19).replace('T', ' ') ?? '—'} mono />
            <Field label="Fee journal entry" value={a.fee_journal_entry_id ? a.fee_journal_entry_id.slice(0, 8) + '…' : '—'} mono />
            <a className="btn" href={`/members/${a.materialized_member_id}`} style={{ alignSelf: 'center' }}>Open member →</a>
          </div>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 12, marginTop: 12 }}>
        {/* ─── Left column: details + checklist ─── */}
        <div style={{ display: 'grid', gap: 12 }}>
          <div className="card">
            <div className="card-hd"><h3>Applicant snapshot</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 16 }}>
              <Field label="Name" value={a.applicant_name} />
              <Field label="Kind" value={a.kind} />
              {a.entity_type && <Field label="Entity type" value={a.entity_type} />}
              {a.primary_phone && <Field label="Phone" value={a.primary_phone} />}
              {a.primary_email && <Field label="Email" value={a.primary_email} />}
              {a.applicant_payload.id_doc_number && <Field label="ID number" value={a.applicant_payload.id_doc_number} mono />}
              {a.applicant_payload.kra_pin && <Field label="KRA PIN" value={a.applicant_payload.kra_pin} mono />}
              {a.applicant_payload.registration_number && <Field label="Registration no" value={a.applicant_payload.registration_number} mono />}
              {a.applicant_payload.physical_address && <Field label="Physical address" value={a.applicant_payload.physical_address} />}
              {a.applicant_payload.county && <Field label="County" value={a.applicant_payload.county} />}
              {a.applicant_payload.occupation && <Field label="Occupation" value={a.applicant_payload.occupation} />}
              {a.applicant_payload.industry && <Field label="Industry" value={a.applicant_payload.industry} />}
              {a.applicant_payload.next_of_kin_name && <Field label="Next of kin" value={`${a.applicant_payload.next_of_kin_name} (${a.applicant_payload.next_of_kin_relation ?? '—'})`} />}
            </div>
          </div>

          {a.fee_required && (
            <div className="card">
              <div className="card-hd">
                <h3>Registration fee</h3>
                <span className="card-sub" style={{
                  color: a.fee_status === 'paid' ? 'var(--pos)' : a.fee_status === 'shortfall' ? '#c97a00' : 'var(--neg)',
                  fontWeight: 600,
                }}>{a.fee_status}</span>
              </div>
              <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16 }}>
                <Field label="Due" value={a.fee_amount_due} mono />
                <Field label="Paid" value={a.fee_amount_paid} mono />
                <Field label="Channel" value={a.fee_payment_channel ?? '—'} />
                <Field label="Reference" value={a.fee_payment_reference ?? '—'} mono />
                <Field label="Date" value={a.fee_payment_date?.slice(0, 10) ?? '—'} mono />
                {a.fee_shortfall_note && <Field label="Shortfall note" value={a.fee_shortfall_note} />}
              </div>
            </div>
          )}

          {/* Reviewer checklist */}
          <div className="card">
            <div className="card-hd">
              <h3>Review checklist</h3>
              <span className="card-sub">
                {responses.size} / {data.checklist_items.length} responded
              </span>
            </div>
            <div className="card-body flush">
              <table className="tbl">
                <thead>
                  <tr><th>Item</th><th>Response</th><th>Note</th><th></th></tr>
                </thead>
                <tbody>
                  {data.checklist_items.map((it) => {
                    const r = responses.get(it.code);
                    const disabled = a.status !== 'under_review' || busy;
                    return (
                      <tr key={it.code}>
                        <td>
                          <div style={{ fontWeight: 600 }}>{it.label}</div>
                          {it.description && <div className="muted tiny">{it.description}</div>}
                          {!it.mandatory && <div className="muted tiny">Optional</div>}
                        </td>
                        <td>
                          {r ? (
                            <span style={{ color: RESPONSE_COLOR[r.response], fontWeight: 600 }}>
                              {r.response}
                            </span>
                          ) : (
                            <span className="muted">—</span>
                          )}
                        </td>
                        <td>
                          {r?.note && <div className="muted tiny">{r.note}</div>}
                        </td>
                        <td style={{ whiteSpace: 'nowrap' }}>
                          <button className="btn tiny" disabled={disabled} onClick={() => void onChecklist(it, 'confirmed')}>✓</button>{' '}
                          <button className="btn tiny" disabled={disabled} onClick={() => void onChecklist(it, 'flagged')}>⚑</button>{' '}
                          <button className="btn tiny" disabled={disabled} onClick={() => void onChecklist(it, 'n/a')}>n/a</button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        {/* ─── Right column: action panel ─── */}
        <div style={{ display: 'grid', gap: 12, alignContent: 'start' }}>
          <div className="card">
            <div className="card-hd"><h3>Actions</h3></div>
            <div className="card-body" style={{ display: 'grid', gap: 10 }}>
              {a.status === 'submitted' && (
                <button className="btn btn-primary" disabled={busy} onClick={() => void action(() => startReview(id), 'Started review')}>
                  Start review
                </button>
              )}
              {a.status === 'under_review' && (
                <>
                  <label>
                    <div className="muted tiny">Review summary (optional)</div>
                    <textarea rows={2} value={reviewSummary} onChange={(e) => setReviewSummary(e.target.value)} />
                  </label>
                  <button className="btn btn-primary" disabled={busy || !allMandatoryAddressed} onClick={() => void action(() => submitForApproval(id, reviewSummary || undefined), 'Submitted for approval')}>
                    Submit for approval
                  </button>
                  {!allMandatoryAddressed && (
                    <div className="muted tiny">Confirm or N/A all mandatory items first. Flagged items block submission.</div>
                  )}
                  <hr />
                  <label>
                    <div className="muted tiny">Return for correction — note required</div>
                    <textarea rows={2} value={returnNote} onChange={(e) => setReturnNote(e.target.value)} placeholder="Describe what the officer must fix" />
                  </label>
                  <button className="btn" disabled={busy || !returnNote.trim()} onClick={() => void action(() => returnForCorrection(id, returnNote), 'Returned for correction')}>
                    Return for correction
                  </button>
                </>
              )}
              {a.status === 'returned_for_correction' && (
                <>
                  <label>
                    <div className="muted tiny">Resubmission note</div>
                    <textarea rows={2} value={returnNote} onChange={(e) => setReturnNote(e.target.value)} placeholder="What's been fixed" />
                  </label>
                  <button className="btn btn-primary" disabled={busy || !returnNote.trim()} onClick={() => void action(() => resubmitApplication(id, returnNote), 'Resubmitted')}>
                    Resubmit
                  </button>
                </>
              )}
              {a.status === 'reviewed_pending_approval' && (
                <>
                  <label>
                    <div className="muted tiny">Approval conditions (optional)</div>
                    <textarea rows={2} value={approveConditions} onChange={(e) => setApproveConditions(e.target.value)} placeholder="e.g. submit missing CR12 within 30 days" />
                  </label>
                  <button className="btn btn-primary" disabled={busy} onClick={() => void action(() => approveApplication(id, approveConditions || undefined), 'Approved')}>
                    {approveConditions ? 'Approve with conditions' : 'Approve'}
                  </button>
                  <hr />
                  <label>
                    <div className="muted tiny">Decline reason (required)</div>
                    <textarea rows={2} value={declineReason} onChange={(e) => setDeclineReason(e.target.value)} />
                  </label>
                  <button className="btn" disabled={busy || !declineReason.trim()} onClick={() => void action(() => declineApplication(id, declineReason), 'Declined')}>
                    Decline
                  </button>
                  <hr />
                  <label>
                    <div className="muted tiny">Return to reviewer — note required</div>
                    <textarea rows={2} value={returnNote} onChange={(e) => setReturnNote(e.target.value)} />
                  </label>
                  <button className="btn" disabled={busy || !returnNote.trim()} onClick={() => void action(() => returnToReviewer(id, returnNote), 'Returned to reviewer')}>
                    Return to reviewer
                  </button>
                </>
              )}
              {!['approved_active', 'declined', 'withdrawn'].includes(a.status) && (
                <>
                  <hr />
                  <label>
                    <div className="muted tiny">Withdraw — reason required</div>
                    <textarea rows={2} value={withdrawReason} onChange={(e) => setWithdrawReason(e.target.value)} />
                  </label>
                  <button className="btn" disabled={busy || !withdrawReason.trim()} onClick={() => void action(() => withdrawApplication(id, withdrawReason), 'Withdrawn')}>
                    Withdraw
                  </button>
                </>
              )}
              {a.status === 'declined' && a.fee_required && parseFloat(a.fee_amount_paid) > 0 && !a.fee_refund_journal_entry_id && (
                <>
                  <hr />
                  <div className="muted tiny">
                    Registration fee paid: <span className="mono">{a.fee_amount_paid}</span>. If your
                    tenant has marked the fee refundable, post the reversal entry now.
                  </div>
                  <button className="btn" disabled={busy} onClick={() => void action(() => postRegistrationFeeRefund(id), 'Refund posted to GL')}>
                    Post fee refund
                  </button>
                </>
              )}
              {a.fee_refund_journal_entry_id && (
                <div className="muted tiny" style={{ marginTop: 6 }}>
                  Fee refund posted · JE <span className="mono">{a.fee_refund_journal_entry_id.slice(0, 8)}…</span>
                </div>
              )}
              {['approved_active', 'declined', 'withdrawn'].includes(a.status) && a.status !== 'declined' && (
                <div className="muted">No further actions — application is in a terminal state.</div>
              )}
            </div>
          </div>

          {data.correction_history.length > 0 && (
            <div className="card">
              <div className="card-hd"><h3>Correction history</h3></div>
              <div className="card-body" style={{ display: 'grid', gap: 8 }}>
                {data.correction_history.map((c) => (
                  <div key={c.id}>
                    <div className="mono tiny">{new Date(c.created_at).toLocaleString()} · {c.event_kind}</div>
                    <div>{c.note}</div>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="card">
            <div className="card-hd"><h3>Lifecycle</h3></div>
            <div className="card-body" style={{ display: 'grid', gap: 4 }}>
              <div className="muted tiny">Submitted {new Date(a.submitted_at).toLocaleString()} ({a.days_in_queue}d ago)</div>
              {a.review_started_at && <div className="muted tiny">Review started {new Date(a.review_started_at).toLocaleString()}</div>}
              {a.review_completed_at && <div className="muted tiny">Review completed {new Date(a.review_completed_at).toLocaleString()}</div>}
              {a.review_summary_note && <div>Reviewer note: {a.review_summary_note}</div>}
              {a.approved_at && <div className="muted tiny">Approved {new Date(a.approved_at).toLocaleString()}</div>}
              {a.approval_conditions && <div>Conditions: {a.approval_conditions}</div>}
              {a.decline_reason && <div>Decline: {a.decline_reason}</div>}
              {a.withdrawn_at && <div className="muted tiny">Withdrawn {new Date(a.withdrawn_at).toLocaleString()}: {a.withdraw_reason}</div>}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{ fontFamily: mono ? 'var(--font-mono)' : undefined }}>{value}</div>
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
