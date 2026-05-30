// DSID Phase 2.2 — Joint account owners + pending withdrawals panel.
//
// Two sections:
//   1. Owners list (add/remove) + required-signers count
//   2. Pending withdrawals with per-signer status

import { useEffect, useMemo, useState } from 'react';
import {
  JointOwner, PendingJointWithdrawal,
  listJointOwners, addJointOwner, removeJointOwner,
  putJointConfig, listPendingJointWithdrawals,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = {
  accountID: string;
  initiallyJoint?: boolean;
  initialRequiredSigners?: number;
};

export default function JointAccountPanel({ accountID, initiallyJoint, initialRequiredSigners }: Props) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const [owners, setOwners] = useState<JointOwner[]>([]);
  const [pending, setPending] = useState<PendingJointWithdrawal[]>([]);
  const [isJoint, setIsJoint] = useState(!!initiallyJoint);
  const [required, setRequired] = useState(initialRequiredSigners ?? 2);
  const [newOwnerCP, setNewOwnerCP] = useState('');
  const [newOwnerRole, setNewOwnerRole] = useState<'primary' | 'co_owner' | 'signatory'>('co_owner');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  async function refresh() {
    try {
      const [o, p] = await Promise.all([
        listJointOwners(accountID),
        listPendingJointWithdrawals(accountID),
      ]);
      setOwners(o);
      setPending(p);
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.');
    }
  }
  useEffect(() => { void refresh(); }, [accountID]);

  const activeOwners = useMemo(() => owners.filter(o => !o.removed_at), [owners]);
  const requiredCap = Math.max(1, activeOwners.length || required);

  async function saveConfig() {
    setBusy(true); setErr(null); setSaved(false);
    try {
      await putJointConfig(accountID, { is_joint: isJoint, required_signers: required });
      setSaved(true);
      setTimeout(() => setSaved(false), 2500);
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.');
    } finally { setBusy(false); }
  }
  async function addOwner() {
    if (!newOwnerCP) return;
    setBusy(true); setErr(null);
    try {
      await addJointOwner(accountID, { counterparty_id: newOwnerCP, signing_role: newOwnerRole });
      setNewOwnerCP('');
      setNewOwnerRole('co_owner');
      await refresh();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Add owner failed.');
    } finally { setBusy(false); }
  }
  async function dropOwner(cpID: string) {
    if (!confirm('Remove this owner? They lose the ability to sign on future withdrawals.')) return;
    setBusy(true); setErr(null);
    try {
      await removeJointOwner(accountID, cpID);
      await refresh();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Remove failed.');
    } finally { setBusy(false); }
  }

  return (
    <div className="card" style={{ padding: 14, marginTop: 16 }}>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 4 }}>
        <h3 style={{ margin: 0 }}>Joint account</h3>
        {isJoint && <span className="badge badge-info">Joint signing enabled</span>}
      </div>
      <div className="muted tiny" style={{ marginBottom: 14 }}>
        With joint signing on, each withdrawal needs N of {activeOwners.length || '—'} owners to consent via SMS.
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {/* Config strip */}
      <div className="modal-section" style={{ background: 'var(--surface-2)', border: '1px solid var(--border)', borderRadius: 'var(--r-md)', padding: 12 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>Configuration</div>
        <div className="row" style={{ gap: 16, flexWrap: 'wrap' }}>
          <label className="row" style={{ gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={isJoint} onChange={(e) => setIsJoint(e.target.checked)} />
            <span style={{ fontSize: 12.5 }}>Joint account</span>
          </label>
          <div className="field" style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <label className="field-label" style={{ margin: 0 }}>Required signers</label>
            <input
              type="number"
              min={1}
              max={requiredCap}
              className="input"
              style={{ width: 70, fontFamily: 'var(--font-mono)', textAlign: 'center' }}
              value={required}
              onChange={(e) => setRequired(Math.max(1, parseInt(e.target.value, 10) || 1))}
              disabled={!isJoint}
            />
            <span className="muted tiny">of {activeOwners.length} owner{activeOwners.length === 1 ? '' : 's'}</span>
          </div>
          <div className="spacer" />
          <div className="row" style={{ gap: 8 }}>
            {saved && <span className="badge badge-pos">Saved</span>}
            <button className="btn btn-sm btn-primary" onClick={() => void saveConfig()} disabled={busy}>
              {busy ? 'Saving…' : 'Save config'}
            </button>
          </div>
        </div>
      </div>

      {/* Owners */}
      <div className="modal-section" style={{ marginTop: 16 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          Owners ({activeOwners.length} active)
        </div>
        {activeOwners.length === 0 ? (
          <div className="muted" style={{ padding: '12px 0', textAlign: 'center' }}>
            No owners yet — add at least {required} below.
          </div>
        ) : (
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            <table className="tbl">
              <thead>
                <tr>
                  <th>Counterparty</th>
                  <th>Role</th>
                  <th>Added</th>
                  <th style={{ textAlign: 'right', paddingRight: 12 }}></th>
                </tr>
              </thead>
              <tbody>
                {activeOwners.map(o => (
                  <tr key={o.id}>
                    <td><code style={{ fontSize: 11 }}>{o.counterparty_id.slice(0, 8)}…{o.counterparty_id.slice(-4)}</code></td>
                    <td><RoleBadge role={o.signing_role} /></td>
                    <td className="muted tiny">{new Date(o.added_at).toLocaleDateString(undefined, { day: '2-digit', month: 'short', year: 'numeric' })}</td>
                    <td style={{ textAlign: 'right', paddingRight: 12 }}>
                      <button className="btn btn-sm btn-danger" disabled={busy} onClick={() => void dropOwner(o.counterparty_id)}>
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Add-owner form */}
        <div style={{ marginTop: 12, padding: 12, background: 'var(--surface-2)', border: '1px solid var(--border)', borderRadius: 'var(--r-md)' }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>Add owner</div>
          <div className="row" style={{ gap: 8, alignItems: 'flex-end' }}>
            <div className="field" style={{ flex: 2 }}>
              <label className="field-label">Counterparty ID</label>
              <input
                className="input"
                style={{ fontFamily: 'var(--font-mono)' }}
                placeholder="UUID — find on the member's profile"
                value={newOwnerCP}
                onChange={(e) => setNewOwnerCP(e.target.value.trim())}
              />
            </div>
            <div className="field" style={{ flex: 1 }}>
              <label className="field-label">Signing role</label>
              <select className="select" value={newOwnerRole} onChange={(e) => setNewOwnerRole(e.target.value as any)}>
                <option value="primary">Primary</option>
                <option value="co_owner">Co-owner</option>
                <option value="signatory">Signatory</option>
              </select>
            </div>
            <button className="btn btn-sm btn-primary" onClick={() => void addOwner()} disabled={busy || !newOwnerCP} style={{ marginBottom: 0 }}>
              Add
            </button>
          </div>
        </div>
      </div>

      {/* Pending withdrawals */}
      <div className="modal-section" style={{ marginTop: 16, marginBottom: 0 }}>
        <div className="eyebrow" style={{ marginBottom: 8 }}>
          Pending withdrawals ({pending.length})
        </div>
        {pending.length === 0 ? (
          <div className="muted" style={{ padding: '12px 0', textAlign: 'center' }}>
            No pending withdrawals awaiting signatures.
          </div>
        ) : (
          <div className="col" style={{ gap: 8 }}>
            {pending.map(p => (
              <PendingCard key={p.id} pending={p} currency={currency} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function RoleBadge({ role }: { role: string }) {
  const cls = ({
    primary:    'badge badge-accent',
    co_owner:   'badge badge-info',
    signatory:  'badge badge-outline',
  } as Record<string, string>)[role] || 'badge';
  const label = ({ primary: 'Primary', co_owner: 'Co-owner', signatory: 'Signatory' } as Record<string, string>)[role] || role;
  return <span className={cls}>{label}</span>;
}

function SignerStatusBadge({ status }: { status: string }) {
  const cls = ({
    approved: 'badge badge-pos',
    rejected: 'badge badge-neg',
    pending:  'badge badge-warn',
  } as Record<string, string>)[status] || 'badge';
  return <span className={cls}>{status}</span>;
}

function PendingCard({ pending, currency }: { pending: PendingJointWithdrawal; currency: string }) {
  const approved = pending.signers.filter(s => s.signer_status === 'approved').length;
  const rejected = pending.signers.filter(s => s.signer_status === 'rejected').length;
  const expiresAt = new Date(pending.expires_at);
  const expired = Date.now() > expiresAt.getTime();
  return (
    <div style={{ padding: 12, border: '1px solid var(--border)', borderRadius: 'var(--r-md)', background: 'var(--surface)' }}>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 6 }}>
        <div>
          <div style={{ fontSize: 13.5, fontWeight: 600, fontFamily: 'var(--font-mono)' }}>
            {currency} {Number(pending.amount).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
          </div>
          <div className="muted tiny" style={{ marginTop: 2 }}>
            {approved} of {pending.required_signers} approved
            {rejected > 0 ? <> · {rejected} rejected</> : null}
          </div>
        </div>
        <div className="muted tiny" style={{ textAlign: 'right' }}>
          {expired ? <span className="badge badge-neg">Expired</span> : <>expires {expiresAt.toLocaleString(undefined, { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' })}</>}
        </div>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {pending.signers.map(s => (
          <div
            key={s.id}
            className="row"
            style={{
              gap: 6,
              padding: '4px 8px',
              border: '1px solid var(--border)',
              borderRadius: 'var(--r-md)',
              background: 'var(--surface-2)',
            }}
          >
            <code style={{ fontSize: 11 }}>{s.signer_counterparty_id.slice(0, 6)}…</code>
            <SignerStatusBadge status={s.signer_status} />
          </div>
        ))}
      </div>
    </div>
  );
}
