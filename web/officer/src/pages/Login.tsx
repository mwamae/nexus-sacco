import { useState } from 'react';
import { officerLogin } from '../api';

export default function Login({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await officerLogin(email.trim(), password);
      onLoggedIn();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Login failed');
    } finally { setBusy(false); }
  }
  return (
    <div className="card">
      <h2 style={{ marginTop: 0 }}>Sign in</h2>
      <p className="muted">Use your staff credentials.</p>
      <label>
        <div className="muted" style={{ marginBottom: 4 }}>Email</div>
        <input className="input" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
      </label>
      <label style={{ display: 'block', marginTop: 8 }}>
        <div className="muted" style={{ marginBottom: 4 }}>Password</div>
        <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      {err && <div style={{ color: '#c33', marginTop: 8, fontSize: 13 }}>{err}</div>}
      <div style={{ marginTop: 14 }}>
        <button className="btn btn-primary" disabled={busy || !email || !password} onClick={() => void submit()}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </div>
    </div>
  );
}
