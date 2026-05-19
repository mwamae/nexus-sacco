import { useMemo, useState, type FormEvent } from 'react';
import { extractError, passwordReset } from '../api/client';

export default function ResetPassword() {
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
      setError('Missing reset token. Open the link from your email again.');
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
      await passwordReset(token, password);
      setDone(true);
    } catch (err) {
      setError(extractError(err, 'Could not reset password'));
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
            <h1>Password updated</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              You can now sign in with your new password. All previous sessions have been signed out.
            </p>
            <a className="btn btn-primary btn-block" href="/">Continue to sign in</a>
          </>
        ) : (
          <>
            <h1>Choose a new password</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              Pick something at least 12 characters long. After resetting, every active session on your account will be signed out.
            </p>

            {error && <div className="alert alert-error">{error}</div>}
            {!token && (
              <div className="alert alert-error">
                No token in URL. Open the reset link from your email.
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
                <label className="field-label" htmlFor="pwc">Confirm new password</label>
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
                {busy ? 'Updating…' : 'Set new password'}
              </button>
            </form>
          </>
        )}
      </div>
    </div>
  );
}
