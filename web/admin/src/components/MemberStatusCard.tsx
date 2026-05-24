// MemberStatusCard — surfaced inside the Member 360 page.
// Drives the entire status-change UX: shows current state + behaviour +
// open proposals + a transition modal that handles both direct-apply and
// workflow-mediated paths transparently.

import { useCallback, useRef, useState, type ReactNode } from 'react';
import {
  changeMemberStatus,
  getMemberStatusActions,
  listMemberStatusHistory,
  uploadStatusSupportingDoc,
  extractError,
  type MemberStatus,
  type MemberStatusChange,
  type MemberStatusReason,
  type StatusTransition,
} from '../api/client';
import { Badge, StatusBadge } from './Badge';
import { Icon } from './Icon';
import { AsyncPanel, isTimeoutError } from './AsyncPanel';

const REASON_OPTIONS: { v: MemberStatusReason; label: string }[] = [
  { v: 'admin_action',          label: 'Admin action' },
  { v: 'compliance_hold',       label: 'Compliance hold' },
  { v: 'disciplinary_action',   label: 'Disciplinary action' },
  { v: 'loan_default',          label: 'Loan default' },
  { v: 'fraud_investigation',   label: 'Fraud investigation' },
  { v: 'regulatory_directive',  label: 'Regulatory directive' },
  { v: 'member_request',        label: 'Member request' },
  { v: 'reactivation_request',  label: 'Reactivation request' },
  { v: 'deceased_notification', label: 'Deceased notification' },
  { v: 'dormancy_inactivity',   label: 'Dormancy (inactivity)' },
  { v: 'system_correction',     label: 'System correction' },
  { v: 'other',                 label: 'Other' },
];

export function MemberStatusCard({ counterpartyId, currentStatus, onChanged }: {
  counterpartyId: string;
  currentStatus: MemberStatus;
  onChanged: () => void | Promise<void>;
}) {
  const [choosing, setChoosing] = useState<StatusTransition | null>(null);
  // Bumped after a successful status change so both AsyncPanels (actions
  // + history) refetch via their deps arrays.
  const [reloadNonce, setReloadNonce] = useState(0);
  const bumpReload = useCallback(() => setReloadNonce((n) => n + 1), []);

  const fetchActions = useCallback(
    () => getMemberStatusActions(counterpartyId),
    // currentStatus / reloadNonce drive a refetch after a transition.
    [counterpartyId, currentStatus, reloadNonce], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const fetchHistory = useCallback(
    () => listMemberStatusHistory(counterpartyId),
    [counterpartyId, currentStatus, reloadNonce], // eslint-disable-line react-hooks/exhaustive-deps
  );

  return (
    <>
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Status &amp; lifecycle</h3>
        </div>
        <div className="card-body">
          <div className="row" style={{ gap: 10, alignItems: 'center', marginBottom: 14 }}>
            <span className="muted tiny">Current</span>
            <StatusBadge status={currentStatus} />
          </div>

          <div className="h-sec">Change to</div>
          <AsyncPanel
            fetcher={fetchActions}
            deps={[counterpartyId, currentStatus, reloadNonce]}
            isEmpty={(a) => a.transitions.length === 0}
            empty={(
              <div className="muted tiny">
                No outbound transitions from <strong>{currentStatus}</strong>. This is a terminal state.
              </div>
            )}
            errorTitle="Couldn't load status actions"
            errorMessage={(err) => isTimeoutError(err)
              ? "The member service didn't respond in time. Status options will reappear once it's reachable."
              : "We couldn't reach the member service to fetch the available status changes."}
            skeleton={<div className="muted tiny" role="status">Loading status options…</div>}
          >
            {(actions) => (
              <>
                {actions.open_proposals && actions.open_proposals.length > 0 && (
                  <div className="alert alert-warn" style={{ marginBottom: 10 }}>
                    <strong>Pending proposals:</strong>{' '}
                    {actions.open_proposals.map((p) => (
                      <span key={p.id}>
                        → {p.proposed_status} ({p.reason_category.replace(/_/g, ' ')}){' '}
                        · <a href={`/approvals?wf=${p.workflow_instance_id}`} style={{ color: 'var(--accent)' }}>
                          open in approvals →
                        </a>
                      </span>
                    ))}
                  </div>
                )}

                <div className="fchips">
                  {actions.transitions.map((t) => (
                    <button
                      key={t.To}
                      type="button"
                      className="fchip"
                      onClick={() => setChoosing(t)}
                      title={t.Note}
                    >
                      → {t.To.replace('_', ' ')}{t.Sensitive && <Badge tone="warn">approval</Badge>}
                    </button>
                  ))}
                </div>

                <div className="muted tiny" style={{ marginTop: 8 }}>
                  Visibility: <strong>{actions.visibility}</strong> · {actions.system_behavior}
                </div>

                {actions.allowed_actions.length > 0 && (
                  <>
                    <div className="divider" />
                    <div className="h-sec">What a {currentStatus} member can do</div>
                    <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
                      {actions.allowed_actions.map((a) => (
                        <Badge key={a.action} tone={a.allowed ? 'pos' : 'neutral'}>
                          {a.allowed ? '✓' : '×'} {a.action.replace(/_/g, ' ')}
                        </Badge>
                      ))}
                    </div>
                  </>
                )}
              </>
            )}
          </AsyncPanel>
        </div>
      </div>

      <StatusHistoryCard fetcher={fetchHistory} deps={[counterpartyId, currentStatus, reloadNonce]} />

      {choosing && (
        <ChangeModal
          counterpartyId={counterpartyId}
          transition={choosing}
          onClose={() => setChoosing(null)}
          onApplied={async () => {
            setChoosing(null);
            bumpReload();
            await onChanged();
          }}
        />
      )}
    </>
  );
}

function StatusHistoryCard({
  fetcher,
  deps,
}: {
  fetcher: () => Promise<MemberStatusChange[]>;
  deps: unknown[];
}) {
  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Status history</h3>
      </div>
      <div className="card-body">
        <AsyncPanel
          fetcher={fetcher}
          deps={deps}
          isEmpty={(h) => h.length === 0}
          empty={(
            <div className="empty">
              No status changes recorded yet — this member has been in their current state since onboarding.
            </div>
          )}
          errorTitle="Couldn't load status history"
          errorMessage={(err) => isTimeoutError(err)
            ? 'The audit lookup timed out. The history is still on file; try again in a moment.'
            : "We couldn't fetch the change log for this member."}
          skeleton={<div className="muted tiny" role="status">Loading history…</div>}
        >
          {(history) => (
            <ol className="tl" style={{ listStyle: 'none', margin: 0 }}>
              {history.map((c) => (
                <li key={c.id} className="tl-item" data-tone={historyTone(c.to_status)}>
                  <div className="tl-action">
                    {c.from_status ? <>{c.from_status.replace('_', ' ')} → </> : null}
                    <strong>{c.to_status.replace('_', ' ')}</strong>
                    {c.workflow_instance_id && <Badge tone="accent">via approval</Badge>}
                  </div>
                  <div className="tl-meta">
                    <time>{new Date(c.changed_at).toISOString().replace('T', ' ').slice(0, 19)}</time>
                    {' · '}
                    <span className="mono">{c.reason_category.replace(/_/g, ' ')}</span>
                    {c.reason_note && <span> · {c.reason_note}</span>}
                    {c.has_supporting_doc && <span> · <Icon name="arrow_dn" size={11} /> doc on file</span>}
                  </div>
                </li>
              ))}
            </ol>
          )}
        </AsyncPanel>
      </div>
    </div>
  );
}

function historyTone(s: MemberStatus): string {
  switch (s) {
    case 'active':       return 'pos';
    case 'blacklisted':
    case 'rejected':
    case 'deceased':     return 'neg';
    case 'suspended':    return 'neg';
    case 'dormant':      return 'muted';
    case 'exited':       return 'muted';
    default:             return '';
  }
}

function ChangeModal({
  counterpartyId, transition, onClose, onApplied,
}: {
  counterpartyId: string;
  transition: StatusTransition;
  onClose: () => void;
  onApplied: () => void | Promise<void>;
}) {
  const [reason, setReason] = useState<MemberStatusReason>(suggestReason(transition.To));
  const [note, setNote] = useState('');
  const [reviewDate, setReviewDate] = useState('');
  const [supportingDoc, setSupportingDoc] = useState<{ storage_path: string; mime: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Unified Inbox (PR #5): when the backend returns mode=proposed,
  // we surface a deep-link to the created workflow instance in
  // place of the legacy alert(). Cleared as soon as the user
  // closes the modal or submits another change.
  const [proposedWFID, setProposedWFID] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  async function onUpload(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setErr(null);
    setBusy(true);
    try {
      const meta = await uploadStatusSupportingDoc(counterpartyId, f);
      setSupportingDoc({ storage_path: meta.storage_path, mime: meta.mime });
    } catch (ex) {
      setErr(extractError(ex));
    } finally {
      setBusy(false);
      if (inputRef.current) inputRef.current.value = '';
    }
  }

  async function submit() {
    setErr(null);
    if (!note.trim() && (transition.To === 'blacklisted' || transition.To === 'suspended' || transition.To === 'rejected')) {
      setErr('A reason note is required for this transition.');
      return;
    }
    setBusy(true);
    try {
      const r = await changeMemberStatus(counterpartyId, {
        target_status: transition.To,
        reason_category: reason,
        reason_note: note || undefined,
        review_date: reviewDate || undefined,
        supporting_doc_path: supportingDoc?.storage_path,
        supporting_doc_mime: supportingDoc?.mime,
      });
      if (r.mode === 'proposed') {
        // Show inline banner with deep-link instead of a modal alert
        // — the alert was easy to dismiss without anyone noticing the
        // change went to approvals, leading to "where did my change
        // go?" support questions.
        setProposedWFID(r.workflow_instance_id ?? null);
        await onApplied(); // refresh the underlying page state…
        return;            // …but don't auto-close the modal — leave
                           //    the banner up until the user dismisses.
      }
      await onApplied();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,.45)',
        display: 'grid', placeItems: 'center',
      }}
      onClick={onClose}
    >
      <div
        className="card"
        style={{ width: 540, maxWidth: '90vw' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="card-hd">
          <h3>Change status: {transition.From.replace('_', ' ')} → <span style={{ color: 'var(--accent)' }}>{transition.To.replace('_', ' ')}</span></h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={13} /></button>
          </div>
        </div>
        <div className="card-body">
          {proposedWFID && (
            <div className="alert alert-info">
              <strong>Submitted for approval.</strong> The change is now in the unified Approvals Inbox awaiting decision.
              <a
                href={`/approvals/${proposedWFID}`}
                className="btn btn-sm btn-accent"
                style={{ marginLeft: 8 }}
              >
                Open in Inbox →
              </a>
            </div>
          )}
          {transition.Sensitive && !proposedWFID && (
            <div className="alert alert-warn">
              <strong>Sensitive transition.</strong> This routes through the approval workflow engine — the member's status stays unchanged until every required level approves.
            </div>
          )}
          {transition.To === 'exited' && (
            <div className="alert alert-warn">
              <strong>Shares must be transferred first.</strong>{' '}
              Share capital is equity and cannot be redeemed for cash. The
              member's share balance must be fully transferred to one or
              more active members via the Shares panel before this exit
              can be finalized.
            </div>
          )}
          {err && <div className="alert alert-error">{err}</div>}

          <p className="muted tiny" style={{ marginTop: 0 }}>{transition.Note}</p>

          <Field label="Reason category">
            <select className="select" value={reason} onChange={(e) => setReason(e.target.value as MemberStatusReason)}>
              {REASON_OPTIONS.map((o) => <option key={o.v} value={o.v}>{o.label}</option>)}
            </select>
          </Field>

          <Field label={`Reason / notes${requiresNote(transition.To) ? ' (required)' : ''}`}>
            <textarea
              className="input"
              style={{ minHeight: 70, padding: 8, fontFamily: 'inherit', resize: 'vertical' }}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={placeholderFor(transition.To)}
            />
          </Field>

          {transition.To === 'suspended' && (
            <Field label="Review date (optional)" hint="When the suspension should be reviewed.">
              <input className="input mono" type="date" value={reviewDate} onChange={(e) => setReviewDate(e.target.value)} />
            </Field>
          )}

          <Field label="Supporting document" hint="PDF / image. Useful for blacklist directives, death certificates, exit-clearance forms.">
            <div className="row" style={{ gap: 6 }}>
              <input
                ref={inputRef}
                type="file"
                accept="image/png,image/jpeg,image/webp,application/pdf"
                onChange={onUpload}
                style={{ display: 'none' }}
                disabled={busy}
              />
              <button type="button" className="btn btn-sm" disabled={busy} onClick={() => inputRef.current?.click()}>
                <Icon name="arrow_up" size={12} /> {supportingDoc ? 'Replace' : 'Upload'}
              </button>
              {supportingDoc && (
                <span className="tiny-mono">{supportingDoc.mime} attached</span>
              )}
            </div>
          </Field>

          <div className="row" style={{ gap: 8, marginTop: 14 }}>
            <button className="btn btn-sm btn-accent" disabled={busy} onClick={() => void submit()}>
              <Icon name="check" size={12} />
              {busy
                ? (transition.Sensitive ? 'Submitting…' : 'Applying…')
                : (transition.Sensitive ? 'Submit for approval' : `Apply: ${transition.To.replace('_', ' ')}`)}
            </button>
            <button className="btn btn-sm btn-ghost" disabled={busy} onClick={onClose}>Cancel</button>
          </div>
        </div>
      </div>
    </div>
  );
}

function suggestReason(to: MemberStatus): MemberStatusReason {
  switch (to) {
    case 'dormant':     return 'dormancy_inactivity';
    case 'suspended':   return 'compliance_hold';
    case 'blacklisted': return 'fraud_investigation';
    case 'exited':      return 'member_request';
    case 'deceased':    return 'deceased_notification';
    case 'active':      return 'reactivation_request';
    case 'rejected':    return 'onboarding_rejection';
  }
  return 'admin_action';
}

function requiresNote(to: MemberStatus): boolean {
  return to === 'blacklisted' || to === 'suspended' || to === 'rejected';
}

function placeholderFor(to: MemberStatus): string {
  switch (to) {
    case 'blacklisted': return 'Why is this member being blacklisted? Reference the directive / case number.';
    case 'suspended':   return 'Reason for suspension + any review date in the notes.';
    case 'exited':      return 'Reason for exit. Reconciliation status will be tracked separately.';
    case 'deceased':    return 'Date of death + reporting source.';
    case 'active':      return 'How was the member reactivated? Note any conditions met (deposit, KYC).';
  }
  return 'Optional context for the audit trail.';
}

function Field({ label, hint, children }: { label: ReactNode; hint?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label className="field-label">{label}</label>
      {children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}
