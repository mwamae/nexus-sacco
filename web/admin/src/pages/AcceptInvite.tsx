// Anonymous page reached from the invite link. Token comes from ?token=
// in the URL; user sets a new password and the backend flips the account
// from pending → active.

import { useMemo, useState, type FormEvent } from 'react';
import { extractError, inviteAccept } from '../api/client';

export default function AcceptInvite() {
  const token = useMemo(() => new URLSearchParams(window.location.search).get('token') ?? '', []);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  const tooShort = password.length > 0 && password.length < 12;
  const mismatch = confirm.length > 0 && password !== confirm;

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!token) {
      setError('Missing invite token. Open the link from your email again.');
      return;
    }
    if (password.length < 12) {
      setError('Password must be at least 12 characters.');
      return;
    }
    if (password !== confirm) {
      setError('Passwords do not match.');
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await inviteAccept(token, password);
      setDone(true);
    } catch (err) {
      setError(extractError(err, 'Could not accept invite'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-shell">
      <div className="auth-card">
        <div className="brand-mark">N</div>
        <div className="eyebrow">nexusSacco</div>

        {done ? (
          <>
            <h1>Account activated</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              Your password is set and your account is active. Sign in below to get started.
            </p>
            <a className="btn btn-primary btn-block" href="/">Continue to sign in</a>
          </>
        ) : (
          <>
            <h1>Accept your invite</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              Pick a password (at least 12 characters) to activate your account.
            </p>

            {error && <div className="alert alert-error">{error}</div>}
            {!token && (
              <div className="alert alert-error">
                No token in URL. Open the invite link from your email.
              </div>
            )}

            <form onSubmit={onSubmit}>
              <div className="field">
                <label className="field-label" htmlFor="pw">New password</label>
                <input
                  id="pw"
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  required
                  minLength={12}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
                {tooShort && <div className="muted tiny">At least 12 characters.</div>}
              </div>
              <div className="field">
                <label className="field-label" htmlFor="pwc">Confirm password</label>
                <input
                  id="pwc"
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  required
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                />
                {mismatch && <div className="muted tiny" style={{ color: 'var(--neg)' }}>Passwords do not match.</div>}
              </div>
              <button
                type="submit"
                className="btn btn-primary btn-block"
                disabled={busy || !token || password.length < 12 || password !== confirm}
              >
                {busy ? 'Activating…' : 'Set password & activate'}
              </button>
            </form>
          </>
        )}
      </div>
    </div>
  );
}
