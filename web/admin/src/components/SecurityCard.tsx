// Security card — lets the signed-in user enable / disable email 2FA.

import { useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import { Badge } from './Badge';
import {
  confirmMFAEnable,
  disableMFA,
  extractError,
  passwordChange,
  startMFAEnable,
  type MFARequiredResponse,
} from '../api/client';

type Step = 'idle' | 'enabling' | 'disabling' | 'changing_password';

export default function SecurityCard() {
  const { user, refresh, logout } = useAuth();
  const [step, setStep] = useState<Step>('idle');
  const [pending, setPending] = useState<MFARequiredResponse | null>(null);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function reset() {
    setStep('idle');
    setPending(null);
    setCode('');
    setPassword('');
    setNewPassword('');
    setConfirmPassword('');
    setError(null);
  }

  async function startEnable() {
    setError(null);
    setBusy(true);
    try {
      const r = await startMFAEnable();
      setPending(r);
      setStep('enabling');
    } catch (e) {
      setError(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  async function submitEnable(e: FormEvent) {
    e.preventDefault();
    if (!pending) return;
    setError(null);
    setBusy(true);
    try {
      await confirmMFAEnable(pending.mfa_token, code.trim());
      await refresh();
      reset();
    } catch (e) {
      setError(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  async function submitChangePassword(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (newPassword.length < 12) {
      setError('New password must be at least 12 characters.');
      return;
    }
    if (newPassword !== confirmPassword) {
      setError('New password and confirmation do not match.');
      return;
    }
    if (newPassword === password) {
      setError('New password must differ from the current one.');
      return;
    }
    setBusy(true);
    try {
      await passwordChange(password, newPassword);
      // Server revoked all sessions. Sign the user out so they re-auth.
      setNotice('Password updated — signing you out…');
      setTimeout(() => { void logout(); }, 900);
    } catch (e) {
      setError(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  async function submitDisable(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await disableMFA(password);
      await refresh();
      reset();
    } catch (e) {
      setError(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Security</h3>
        <span className="card-sub">{user?.email}</span>
        <div className="card-hd-actions">
          {user?.mfa_enabled ? <Badge tone="pos">2FA on</Badge> : <Badge tone="warn">2FA off</Badge>}
        </div>
      </div>
      <div className="card-body">
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-info">{notice}</div>}

        {step === 'idle' && (
          <>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingBottom: 12, borderBottom: '1px solid var(--border)' }}>
              <div>
                <div style={{ fontWeight: 500 }}>Password</div>
                <div className="muted tiny">Last changed: see audit log</div>
              </div>
              <button className="btn btn-sm" onClick={() => { setError(null); setStep('changing_password'); }} disabled={busy}>
                Change
              </button>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingTop: 12 }}>
              <div>
                <div style={{ fontWeight: 500 }}>Email 2FA</div>
                <div className="muted tiny">
                  {user?.mfa_enabled
                    ? `Enabled — codes sent to ${user.email}`
                    : 'Disabled — sign-in requires only your password'}
                </div>
              </div>
              {user?.mfa_enabled ? (
                <button className="btn btn-sm" onClick={() => setStep('disabling')} disabled={busy}>Disable</button>
              ) : (
                <button className="btn btn-sm btn-accent" onClick={startEnable} disabled={busy}>
                  {busy ? 'Sending code…' : 'Enable email 2FA'}
                </button>
              )}
            </div>
          </>
        )}

        {step === 'changing_password' && (
          <form onSubmit={submitChangePassword}>
            <p className="tiny muted" style={{ marginBottom: 10 }}>
              Choose a new password (≥ 12 chars). All other sessions will be signed out.
            </p>
            <div className="grid-3">
              <div className="field">
                <label className="field-label">Current password</label>
                <input className="input" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
              </div>
              <div className="field">
                <label className="field-label">New password</label>
                <input className="input" type="password" autoComplete="new-password" required minLength={12} value={newPassword} onChange={(e) => setNewPassword(e.target.value)} />
              </div>
              <div className="field">
                <label className="field-label">Confirm</label>
                <input className="input" type="password" autoComplete="new-password" required value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} />
              </div>
            </div>
            <div className="row" style={{ marginTop: 12, gap: 8 }}>
              <button type="submit" className="btn btn-accent btn-sm" disabled={busy}>
                {busy ? 'Updating…' : 'Update password'}
              </button>
              <button type="button" className="btn btn-sm btn-ghost" onClick={reset} disabled={busy}>Cancel</button>
            </div>
          </form>
        )}

        {step === 'enabling' && pending && (
          <form onSubmit={submitEnable}>
            <p className="tiny muted" style={{ marginBottom: 10 }}>
              Enter the 6-digit code we sent to <strong>{pending.delivery_hint}</strong>.
            </p>
            <div className="field" style={{ maxWidth: 220 }}>
              <label className="field-label">Verification code</label>
              <input
                className="input mono"
                inputMode="numeric"
                pattern="\d{6}"
                maxLength={6}
                required
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                placeholder="123456"
                style={{ letterSpacing: '0.4em', textAlign: 'center', fontSize: 16 }}
              />
            </div>
            <div className="row" style={{ marginTop: 12, gap: 8 }}>
              <button type="submit" className="btn btn-accent btn-sm" disabled={busy || code.length !== 6}>
                {busy ? 'Confirming…' : 'Confirm'}
              </button>
              <button type="button" className="btn btn-sm btn-ghost" onClick={reset} disabled={busy}>Cancel</button>
            </div>
          </form>
        )}

        {step === 'disabling' && (
          <form onSubmit={submitDisable}>
            <p className="tiny muted" style={{ marginBottom: 10 }}>Re-enter your password to disable 2FA.</p>
            <div className="field" style={{ maxWidth: 320 }}>
              <label className="field-label">Password</label>
              <input className="input" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            <div className="row" style={{ marginTop: 12, gap: 8 }}>
              <button type="submit" className="btn btn-sm btn-danger" disabled={busy}>
                {busy ? 'Disabling…' : 'Disable 2FA'}
              </button>
              <button type="button" className="btn btn-sm btn-ghost" onClick={reset} disabled={busy}>Cancel</button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
