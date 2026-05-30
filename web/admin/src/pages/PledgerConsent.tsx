// Phase 1.5b — public third-party pledger consent page.
// Reached from the SMS link /g/pledger/{token}. Mirrors GuarantorConsent
// but for collateral pledges.

import { useEffect, useState, type FormEvent } from 'react';

type TokenResp = {
  pledger_name: string;
  pledger_phone_masked: string;
  applicant_name: string;
  application_no: string;
  product_name: string;
  requested_amount: string;
  collateral_kind: string;
  collateral_description: string;
  estimated_value: string;
  tenant_name: string;
  expires_at: string;
  otp_issued: boolean;
  otp_verified: boolean;
  decision: string | null;
};

type Stage = 'loading' | 'need_id' | 'need_otp' | 'need_decision' | 'done' | 'error';

function tokenFromPath(): string {
  const m = window.location.pathname.match(/^\/g\/pledger\/([^/]+)/);
  return m ? m[1] : '';
}

function apiPath(token: string, suffix = ''): string {
  return `/api/p/pledger-consent/${encodeURIComponent(token)}${suffix}`;
}

async function readJSON<T>(r: Response): Promise<T> {
  const text = await r.text();
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try {
      const j = JSON.parse(text);
      msg = j?.error?.message || j?.message || msg;
    } catch {}
    throw new Error(msg);
  }
  return text ? (JSON.parse(text) as T) : ({} as T);
}

export default function PledgerConsent() {
  const token = tokenFromPath();
  const [stage, setStage] = useState<Stage>('loading');
  const [error, setError] = useState<string | null>(null);
  const [tokenData, setTokenData] = useState<TokenResp | null>(null);
  const [nationalID, setNationalID] = useState('');
  const [otp, setOTP] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [doneMessage, setDoneMessage] = useState<string>('');

  useEffect(() => {
    if (!token) {
      setStage('error');
      setError('Missing token. Open the link from your SMS again.');
      return;
    }
    (async () => {
      try {
        const r = await fetch(apiPath(token), { credentials: 'omit' });
        const data = await readJSON<TokenResp>(r);
        setTokenData(data);
        if (data.decision === 'accepted' || data.decision === 'declined' || data.decision === 'opted_offline') {
          setStage('done');
          setDoneMessage(decisionDoneText(data.decision));
          return;
        }
        if (data.decision === 'abandoned') {
          setStage('error');
          setError('This link has been disabled after too many failed verification attempts. Please contact your SACCO.');
          return;
        }
        if (data.otp_verified) setStage('need_decision');
        else if (data.otp_issued) setStage('need_otp');
        else setStage('need_id');
      } catch (e: any) {
        setStage('error');
        setError(e?.message || 'Could not load consent page.');
      }
    })();
  }, [token]);

  async function submitID(e: FormEvent) {
    e.preventDefault();
    if (!nationalID.trim()) { setError('Enter your National ID number.'); return; }
    setError(null); setBusy(true);
    try {
      await readJSON(await fetch(apiPath(token, '/verify-id'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'omit',
        body: JSON.stringify({ national_id: nationalID.trim() }),
      }));
      const refreshed = await readJSON<TokenResp>(await fetch(apiPath(token)));
      setTokenData(refreshed);
      setStage('need_otp');
    } catch (e: any) { setError(e?.message || 'Could not verify your National ID.'); }
    finally { setBusy(false); }
  }

  async function submitOTP(e: FormEvent) {
    e.preventDefault();
    if (!/^\d{6}$/.test(otp.trim())) { setError('Enter the 6-digit code from the SMS.'); return; }
    setError(null); setBusy(true);
    try {
      await readJSON(await fetch(apiPath(token, '/verify-otp'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'omit',
        body: JSON.stringify({ code: otp.trim() }),
      }));
      setStage('need_decision');
    } catch (e: any) { setError(e?.message || 'OTP did not match.'); }
    finally { setBusy(false); }
  }

  async function submitDecision(decision: 'accepted' | 'declined' | 'opted_offline') {
    if (decision === 'declined' && !reason.trim()) {
      setError('Please add a short reason so the SACCO can follow up.');
      return;
    }
    setError(null); setBusy(true);
    try {
      await readJSON(await fetch(apiPath(token, '/respond'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'omit',
        body: JSON.stringify({ decision, reason: decision === 'declined' ? reason.trim() : null }),
      }));
      setStage('done');
      setDoneMessage(decisionDoneText(decision));
    } catch (e: any) { setError(e?.message || 'Could not record your decision.'); }
    finally { setBusy(false); }
  }

  return (
    <div className="auth-shell">
      <div className="auth-card" style={{ maxWidth: 460 }}>
        <div className="brand-mark">N</div>
        <div className="eyebrow">{tokenData?.tenant_name || 'nexusSacco'}</div>

        {stage === 'loading' && <p className="muted tiny">Loading…</p>}

        {stage === 'error' && (
          <>
            <h1>Can&rsquo;t open this link</h1>
            <div className="alert alert-error" style={{ marginTop: 12 }}>{error}</div>
          </>
        )}

        {tokenData && stage !== 'loading' && stage !== 'error' && (
          <>
            <h1 style={{ marginBottom: 4 }}>Pledge consent</h1>
            <p className="muted tiny" style={{ marginBottom: 12 }}>
              {tokenData.applicant_name} has asked you to pledge your asset as security
              for application <strong>{tokenData.application_no}</strong>.
            </p>

            <div className="card" style={{ padding: 12, marginBottom: 14, background: 'var(--surface-2, #f7f7f9)' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                <span className="muted tiny">Asset</span>
                <span>{tokenData.collateral_kind.replace(/_/g, ' ')}</span>
              </div>
              <div className="tiny muted">{tokenData.collateral_description}</div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}>
                <span className="muted tiny">Loan amount</span>
                <span>KES {tokenData.requested_amount}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 2 }}>
                <span className="muted tiny">Asset value</span>
                <strong>KES {tokenData.estimated_value}</strong>
              </div>
            </div>

            {error && <div className="alert alert-error" style={{ marginBottom: 10 }}>{error}</div>}

            {stage === 'need_id' && (
              <form onSubmit={submitID}>
                <p className="muted tiny" style={{ marginBottom: 10 }}>
                  Verify it&rsquo;s you — enter your National ID. We&rsquo;ll text a 6-digit code to {tokenData.pledger_phone_masked}.
                </p>
                <label className="form-label">National ID</label>
                <input className="form-control" inputMode="numeric" autoComplete="off"
                  value={nationalID} onChange={(e) => setNationalID(e.target.value)} disabled={busy} />
                <button className="btn btn-primary btn-block" style={{ marginTop: 14 }} disabled={busy}>
                  {busy ? 'Sending…' : 'Send code'}
                </button>
              </form>
            )}

            {stage === 'need_otp' && (
              <form onSubmit={submitOTP}>
                <p className="muted tiny" style={{ marginBottom: 10 }}>
                  Enter the 6-digit code we just sent to {tokenData.pledger_phone_masked}.
                </p>
                <label className="form-label">Code</label>
                <input className="form-control" inputMode="numeric" pattern="[0-9]*" maxLength={6} autoComplete="one-time-code"
                  value={otp} onChange={(e) => setOTP(e.target.value.replace(/\D/g, '').slice(0, 6))} disabled={busy} />
                <button className="btn btn-primary btn-block" style={{ marginTop: 14 }} disabled={busy}>
                  {busy ? 'Verifying…' : 'Verify code'}
                </button>
              </form>
            )}

            {stage === 'need_decision' && (
              <>
                <p className="muted tiny" style={{ marginBottom: 10 }}>You&rsquo;re verified. Choose how to proceed.</p>
                <button className="btn btn-primary btn-block" style={{ marginBottom: 8 }}
                  onClick={() => submitDecision('accepted')} disabled={busy}>
                  Accept &mdash; agree to pledge my {tokenData.collateral_kind.replace(/_/g, ' ')}
                </button>
                <details style={{ marginBottom: 8 }}>
                  <summary className="muted tiny" style={{ cursor: 'pointer' }}>Decline this request</summary>
                  <div style={{ marginTop: 8 }}>
                    <label className="form-label">Reason</label>
                    <textarea className="form-control" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} disabled={busy} />
                    <button className="btn btn-danger btn-block" style={{ marginTop: 10 }}
                      onClick={() => submitDecision('declined')} disabled={busy || !reason.trim()}>
                      Submit decline
                    </button>
                  </div>
                </details>
                <button className="btn btn-block" onClick={() => submitDecision('opted_offline')} disabled={busy}>
                  I&rsquo;ll sign and mail the paper form instead
                </button>
              </>
            )}

            {stage === 'done' && (
              <>
                <h2 style={{ marginTop: 6 }}>Thank you</h2>
                <p style={{ marginTop: 6 }}>{doneMessage}</p>
                <p className="muted tiny" style={{ marginTop: 14 }}>You can close this page.</p>
              </>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function decisionDoneText(decision: string): string {
  switch (decision) {
    case 'accepted':
      return 'Your consent has been recorded. The SACCO will continue with the loan application.';
    case 'declined':
      return 'You have declined the pledge. The SACCO has been notified.';
    case 'opted_offline':
      return 'Got it — please return the signed paper form to the SACCO. Status will update once they upload the signed copy.';
    default:
      return 'Your response has been recorded.';
  }
}
