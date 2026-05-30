// DSID Phase 2.2 — Reactivate dormant deposit account.
//
// Surfaces a Reactivate button when status='dormant'. Clicking opens
// a small modal that captures (reason, kyc_refresh_confirmed) and
// POSTs to /v1/deposit-accounts/{id}/reactivate, which files a
// branch-manager approval workflow.

import { useState } from 'react';
import { reactivateAccount } from '../../api/client';

type Props = {
  accountID: string;
  accountNo?: string;
  status: string;
  onAfterRequest?: () => void;
};

export default function DormantAccountActions({ accountID, accountNo, status, onAfterRequest }: Props) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const [kycOK, setKycOK] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [filed, setFiled] = useState(false);

  if (status !== 'dormant') return null;

  function reset() {
    setReason('');
    setKycOK(false);
    setErr(null);
    setFiled(false);
  }
  function close() {
    setOpen(false);
    reset();
  }

  async function submit() {
    setBusy(true); setErr(null);
    try {
      await reactivateAccount(accountID, { reason, kyc_refresh_confirmed: kycOK });
      setFiled(true);
      if (onAfterRequest) onAfterRequest();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Reactivation request failed.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <button
        className="btn btn-sm btn-primary"
        onClick={() => setOpen(true)}
        title="File a reactivation request for this dormant account."
      >
        Reactivate
      </button>
      {open && (
        <div className="modal-backdrop" onClick={close}>
          <div className="modal" style={{ maxWidth: 480 }} onClick={(e) => e.stopPropagation()}>
            <div className="modal-section">
              <h3 style={{ marginTop: 0, marginBottom: 2 }}>Reactivate dormant account</h3>
              <div className="muted tiny">
                {accountNo ? <>Account <strong>{accountNo}</strong> · </> : null}
                Files a branch-manager approval — withdrawals stay blocked until approved.
              </div>
            </div>

            {filed ? (
              <>
                <div className="modal-section">
                  <div className="alert" style={{ background: 'var(--pos-bg)', color: 'var(--pos)', border: '1px solid var(--pos)' }}>
                    Reactivation request filed. A branch manager must approve before the account flips to active.
                  </div>
                </div>
                <div className="modal-actions">
                  <button className="btn btn-sm btn-primary" onClick={close}>Close</button>
                </div>
              </>
            ) : (
              <>
                <div className="modal-section">
                  <div className="field">
                    <label className="field-label">Reason <span className="req">*</span></label>
                    <textarea
                      className="input"
                      rows={3}
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                      placeholder="e.g. Member returned to deposit + refreshed KYC documents on 2026-05-30."
                      style={{ height: 'auto', padding: '8px 10px', resize: 'vertical' }}
                    />
                    <div className="field-hint">Shown to the approving branch manager in the workflow inbox.</div>
                  </div>
                </div>

                <div className="modal-section">
                  <label
                    className="field"
                    style={{
                      flexDirection: 'row',
                      alignItems: 'flex-start',
                      gap: 8,
                      border: '1px solid var(--border)',
                      borderRadius: 'var(--r-md)',
                      padding: '10px 12px',
                      background: 'var(--surface-2)',
                      cursor: 'pointer',
                    }}
                  >
                    <input
                      type="checkbox"
                      checked={kycOK}
                      onChange={(e) => setKycOK(e.target.checked)}
                      style={{ marginTop: 2 }}
                    />
                    <div>
                      <div style={{ fontSize: 12.5, fontWeight: 600 }}>
                        I confirm the member's KYC documents are current. <span className="req">*</span>
                      </div>
                      <div className="field-hint">No expired ID, address proof, or employer letter on file.</div>
                    </div>
                  </label>
                </div>

                {err && <div className="alert alert-error">{err}</div>}

                <div className="modal-actions">
                  <button className="btn btn-sm" onClick={close} disabled={busy}>Cancel</button>
                  <button
                    className="btn btn-sm btn-primary"
                    disabled={busy || !reason || !kycOK}
                    onClick={() => void submit()}
                  >
                    {busy ? 'Filing…' : 'File reactivation request'}
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </>
  );
}
