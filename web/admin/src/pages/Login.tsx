import { useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import { currentTenantSlug, isPlatformHost } from '../auth/tenant';
import { extractError, passwordForgot, type MFARequiredResponse } from '../api/client';

type Mode = 'login' | 'mfa' | 'forgot' | 'forgot_sent';

export default function Login() {
  const { login, completeMFA } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [mfa, setMfa] = useState<MFARequiredResponse | null>(null);
  const [code, setCode] = useState('');
  const [mode, setMode] = useState<Mode>('login');
  const [forgotEmail, setForgotEmail] = useState('');

  const tenantSlug = currentTenantSlug();
  const platform = isPlatformHost();

  async function onPasswordSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const r = await login(email, password);
      if (r.kind === 'mfa_required') {
        setMfa(r.mfa);
        setCode('');
        setMode('mfa');
      }
    } catch (err) {
      setError(extractError(err, 'Login failed'));
    } finally {
      setBusy(false);
    }
  }

  async function onForgotSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await passwordForgot(forgotEmail);
      setMode('forgot_sent');
    } catch (err) {
      setError(extractError(err, 'Could not request reset'));
    } finally {
      setBusy(false);
    }
  }

  async function onCodeSubmit(e: FormEvent) {
    e.preventDefault();
    if (!mfa) return;
    setError(null);
    setBusy(true);
    try {
      await completeMFA(mfa.mfa_token, code.trim());
    } catch (err) {
      setError(extractError(err, 'Verification failed'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-shell">
      <div className="auth-card">
        <div className="brand-mark">N</div>
        <div className="eyebrow">nexusSacco</div>
        {mode === 'forgot_sent' ? (
          <>
            <h1>Check your email</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              If an account exists for <strong>{forgotEmail}</strong>, we've sent a password reset link.
              The link expires in 30 minutes.
            </p>
            <button className="btn btn-block" onClick={() => { setMode('login'); setError(null); }}>
              Back to sign in
            </button>
          </>
        ) : mode === 'forgot' ? (
          <>
            <h1>Reset your password</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              Enter the email on your nexusSacco account and we'll send a reset link.
            </p>

            {error && <div className="alert alert-error">{error}</div>}

            <form onSubmit={onForgotSubmit}>
              <div className="field">
                <label className="field-label" htmlFor="femail">Email</label>
                <input
                  id="femail"
                  className="input"
                  type="email"
                  autoComplete="email"
                  required
                  autoFocus
                  value={forgotEmail}
                  onChange={(e) => setForgotEmail(e.target.value)}
                />
              </div>
              <button type="submit" className="btn btn-primary btn-block" disabled={busy}>
                {busy ? 'Sending…' : 'Email me a reset link'}
              </button>
            </form>

            <p className="muted tiny" style={{ marginTop: 16, textAlign: 'center' }}>
              <a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); setError(null); }}>
                Back to sign in
              </a>
            </p>
          </>
        ) : mode === 'mfa' && mfa ? (
          <>
            <h1>Two-factor verification</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              We sent a 6-digit code to <strong>{mfa.delivery_hint}</strong>. It expires in 10 minutes.
            </p>

            {error && <div className="alert alert-error">{error}</div>}

            <form onSubmit={onCodeSubmit}>
              <div className="field">
                <label className="field-label" htmlFor="code">Verification code</label>
                <input
                  id="code"
                  className="input mono"
                  inputMode="numeric"
                  pattern="\d{6}"
                  maxLength={6}
                  required
                  autoFocus
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                  placeholder="123456"
                  style={{ letterSpacing: '0.4em', textAlign: 'center', fontSize: 18 }}
                />
              </div>
              <button type="submit" className="btn btn-primary btn-block" disabled={busy || code.length !== 6}>
                {busy ? 'Verifying…' : 'Verify and sign in'}
              </button>
            </form>

            <p className="muted tiny" style={{ marginTop: 16, textAlign: 'center' }}>
              Didn't get the code?{' '}
              <a
                href="#"
                onClick={(e) => {
                  e.preventDefault();
                  setMfa(null);
                  setPassword('');
                  setError(null);
                  setMode('login');
                }}
              >
                Start over
              </a>
            </p>
          </>
        ) : (
          <>
            <h1>Sign in</h1>
            <p className="muted tiny" style={{ marginBottom: 18 }}>
              {platform ? <>Platform administration console.</> : <>Signing in to <strong>{tenantSlug}</strong>.</>}
            </p>

            {error && <div className="alert alert-error">{error}</div>}

            <form onSubmit={onPasswordSubmit}>
              <div className="field">
                <label className="field-label" htmlFor="email">Email</label>
                <input
                  id="email"
                  className="input"
                  type="email"
                  autoComplete="email"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                />
              </div>
              <div className="field">
                <label className="field-label" htmlFor="password">Password</label>
                <input
                  id="password"
                  className="input"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="••••••••"
                />
              </div>
              <button type="submit" className="btn btn-primary btn-block" disabled={busy}>
                {busy ? 'Signing in…' : 'Sign in'}
              </button>
            </form>

            <p className="muted tiny" style={{ marginTop: 14, textAlign: 'center' }}>
              <a
                href="#"
                onClick={(e) => {
                  e.preventDefault();
                  setForgotEmail(email);
                  setError(null);
                  setMode('forgot');
                }}
              >
                Forgot your password?
              </a>
            </p>

            {!platform && (
              <p className="muted tiny" style={{ marginTop: 8, textAlign: 'center' }}>
                Wrong tenant? Switch subdomain in the URL.
              </p>
            )}
          </>
        )}
      </div>
    </div>
  );
}
