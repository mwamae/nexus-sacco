// Member-login page. Uses memberLogin() which hits the existing
// /v1/auth/login endpoint for now — a dedicated /v1/auth/member/login
// with SMS-OTP password reset lands in a follow-up identity PR.

import { useState } from 'react';
import { memberLogin } from '../api';

export default function Login({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [memberNo, setMemberNo] = useState('');
  const [password, setPassword] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    setBusy(true); setErr(null);
    try {
      await memberLogin(memberNo.trim(), password);
      onLoggedIn();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Login failed');
    } finally { setBusy(false); }
  }

  return (
    <div className="card" style={{ maxWidth: 420, margin: '24px auto' }}>
      <h2 style={{ marginTop: 0 }}>Sign in</h2>
      <p style={{ color: '#555', fontSize: 13 }}>
        Use your member number / email and the password your SACCO set up for the portal.
        If you don't have one, contact your SACCO office.
      </p>
      <label>
        <div style={{ fontSize: 12, color: '#555', marginBottom: 4 }}>Member number or email</div>
        <input className="input" value={memberNo} onChange={(e) => setMemberNo(e.target.value)} autoFocus />
      </label>
      <label>
        <div style={{ fontSize: 12, color: '#555', marginBottom: 4 }}>Password</div>
        <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      {err && <div style={{ color: '#c33', marginTop: 8, fontSize: 13 }}>{err}</div>}
      <div style={{ marginTop: 14 }}>
        <button className="btn btn-primary" disabled={busy || !memberNo || !password} onClick={() => void submit()}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </div>
    </div>
  );
}
