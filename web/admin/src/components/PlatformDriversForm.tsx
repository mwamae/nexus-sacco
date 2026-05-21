// Platform-admin SMTP + SMS driver configuration. Used inside the
// Platform Dashboard's Drivers tab.
//
// Both forms support an inline "send test" button so the admin can
// verify the credentials before flipping `is_enabled` on. Secret fields
// (password / API key / webhook secret) are write-only — the GET
// response returns only a `has_*` boolean and the form posts the new
// value only when the field is non-empty.

import { useEffect, useState, type ReactNode } from 'react';
import {
  getPlatformSMTP,
  getPlatformSMS,
  testPlatformSMTP,
  testPlatformSMS,
  updatePlatformSMTP,
  updatePlatformSMS,
  type PlatformSMTPConfig,
  type PlatformSMSConfig,
  type SMSProvider,
  type SMTPEncryption,
} from '../api/client';

export function PlatformDriversForm() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <p className="muted" style={{ margin: 0 }}>
        Platform-owned drivers shared across every tenant. Tenants don't see
        these credentials — they only pay per send from their prepaid balance.
      </p>
      <SMTPForm />
      <SMSForm />
    </div>
  );
}

// ─────────── SMTP ───────────

function SMTPForm() {
  const [cfg, setCfg] = useState<PlatformSMTPConfig | null>(null);
  const [host, setHost] = useState('');
  const [port, setPort] = useState(587);
  const [encryption, setEncryption] = useState<SMTPEncryption>('starttls');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [fromAddress, setFromAddress] = useState('');
  const [fromName, setFromName] = useState('');
  const [isEnabled, setIsEnabled] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [testTo, setTestTo] = useState('');
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null);

  async function load() {
    setErr(null);
    try {
      const c = await getPlatformSMTP();
      setCfg(c);
      setHost(c.host || '');
      setPort(c.port || 587);
      setEncryption(c.encryption || 'starttls');
      setUsername(c.username || '');
      setFromAddress(c.from_address || '');
      setFromName(c.from_name || '');
      setIsEnabled(c.is_enabled || false);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  async function save() {
    setErr(null); setSavedAt(null);
    setBusy('save');
    try {
      // Omit password from the payload when the input is blank — that
      // tells the backend to keep the existing encrypted value.
      const payload: Parameters<typeof updatePlatformSMTP>[0] = {
        host, port, encryption, username,
        from_address: fromAddress, from_name: fromName,
        is_enabled: isEnabled,
      };
      if (password.trim() !== '') payload.password = password;
      const c = await updatePlatformSMTP(payload);
      setCfg(c);
      setPassword('');
      setSavedAt(new Date().toLocaleTimeString());
    } catch (e) {
      setErr(extractErr(e));
    } finally { setBusy(null); }
  }

  async function runTest() {
    if (!testTo) { setTestResult({ ok: false, msg: 'Enter a recipient address first.' }); return; }
    setBusy('test'); setTestResult(null);
    try {
      const r = await testPlatformSMTP(testTo, 'nexusSacco SMTP test', 'This is a test email from the shared platform SMTP driver.');
      if (r.ok) {
        setTestResult({ ok: true, msg: `Sent to ${r.to ?? testTo}. Provider message id: ${r.provider_message_id ?? '—'}` });
      } else {
        setTestResult({ ok: false, msg: r.error ?? 'unknown error' });
      }
    } catch (e) {
      setTestResult({ ok: false, msg: extractErr(e) });
    } finally { setBusy(null); }
  }

  return (
    <div className="card">
      <div className="card-hd">
        <h3 style={{ margin: 0 }}>SMTP (Email)</h3>
        <span className="card-sub">
          Outbound email goes through this server for every tenant.
        </span>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error" style={{ marginBottom: 10 }}>{err}</div>}

        <div className="grid-2">
          <Field label="Host">
            <input value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" style={{ width: '100%' }} />
          </Field>
          <Field label="Port">
            <input
              type="number"
              value={port}
              onChange={(e) => setPort(parseInt(e.target.value, 10) || 0)}
              style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
            />
          </Field>
          <Field label="Encryption">
            <select value={encryption} onChange={(e) => setEncryption(e.target.value as SMTPEncryption)} style={{ width: '100%' }}>
              <option value="none">None (Mailpit / dev)</option>
              <option value="starttls">STARTTLS (port 587)</option>
              <option value="tls">Implicit TLS / SMTPS (port 465)</option>
            </select>
          </Field>
          <Field label="Username">
            <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="often the from address" style={{ width: '100%' }} />
          </Field>
          <Field
            label={cfg?.has_password ? 'New password (leave blank to keep)' : 'Password'}
            hint="Stored encrypted at rest (AES-GCM)."
          >
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={cfg?.has_password ? '••••••••' : ''}
              style={{ width: '100%' }}
            />
          </Field>
          <Field label="Active">
            <label className="row" style={{ gap: 6, alignItems: 'center' }}>
              <input type="checkbox" checked={isEnabled} onChange={(e) => setIsEnabled(e.target.checked)} />
              {isEnabled ? 'Workers will dispatch emails using this driver.' : 'Email queue paused.'}
            </label>
          </Field>
        </div>

        <h4 style={{ marginTop: 16, marginBottom: 6 }}>Identity</h4>
        <div className="grid-2">
          <Field label="From address">
            <input value={fromAddress} onChange={(e) => setFromAddress(e.target.value)} placeholder="no-reply@nexussacco.local" style={{ width: '100%' }} />
          </Field>
          <Field label="From name (display)">
            <input value={fromName} onChange={(e) => setFromName(e.target.value)} placeholder="nexusSacco" style={{ width: '100%' }} />
          </Field>
        </div>

        <div className="row" style={{ gap: 8, marginTop: 12, alignItems: 'center' }}>
          <button className="btn btn-accent" disabled={busy === 'save' || !host || !fromAddress} onClick={() => void save()}>
            {busy === 'save' ? 'Saving…' : 'Save SMTP config'}
          </button>
          {savedAt && <span className="muted tiny">Saved {savedAt}</span>}
        </div>

        <h4 style={{ marginTop: 18, marginBottom: 6 }}>Send a test email</h4>
        <div className="row" style={{ gap: 8, alignItems: 'flex-end' }}>
          <Field label="Recipient address">
            <input value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="you@example.com" style={{ width: '100%' }} />
          </Field>
          <button className="btn" disabled={busy === 'test' || !cfg?.is_enabled} onClick={() => void runTest()}>
            {busy === 'test' ? 'Sending…' : 'Send test'}
          </button>
        </div>
        {!cfg?.is_enabled && (
          <p className="muted tiny" style={{ marginTop: 6 }}>Enable the SMTP config first.</p>
        )}
        {testResult && (
          <div className={`alert ${testResult.ok ? 'alert-info' : 'alert-error'}`} style={{ marginTop: 10 }}>
            {testResult.msg}
          </div>
        )}
      </div>
    </div>
  );
}

// ─────────── SMS ───────────

function SMSForm() {
  const [cfg, setCfg] = useState<PlatformSMSConfig | null>(null);
  const [provider, setProvider] = useState<SMSProvider>('mock');
  const [username, setUsername] = useState('');
  const [apiKey, setAPIKey] = useState('');
  const [senderID, setSenderID] = useState('');
  const [ratePerMinute, setRatePerMinute] = useState(600);
  const [webhookSecret, setWebhookSecret] = useState('');
  const [isEnabled, setIsEnabled] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [testTo, setTestTo] = useState('');
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null);

  async function load() {
    setErr(null);
    try {
      const c = await getPlatformSMS();
      setCfg(c);
      setProvider(c.provider || 'mock');
      setUsername(c.username || '');
      setSenderID(c.sender_id || '');
      setRatePerMinute(c.rate_per_minute || 600);
      setIsEnabled(c.is_enabled || false);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  async function save() {
    setErr(null); setSavedAt(null);
    setBusy('save');
    try {
      const payload: Parameters<typeof updatePlatformSMS>[0] = {
        provider, username, sender_id: senderID, rate_per_minute: ratePerMinute,
        is_enabled: isEnabled,
      };
      if (apiKey.trim() !== '') payload.api_key = apiKey;
      if (webhookSecret.trim() !== '') payload.webhook_secret = webhookSecret;
      const c = await updatePlatformSMS(payload);
      setCfg(c);
      setAPIKey('');
      setWebhookSecret('');
      setSavedAt(new Date().toLocaleTimeString());
    } catch (e) {
      setErr(extractErr(e));
    } finally { setBusy(null); }
  }

  async function runTest() {
    if (!testTo) { setTestResult({ ok: false, msg: 'Enter a phone number first.' }); return; }
    setBusy('test'); setTestResult(null);
    try {
      const r = await testPlatformSMS(testTo, 'nexusSacco platform SMS driver test.');
      if (r.ok) {
        setTestResult({ ok: true, msg: `Sent to ${r.to ?? testTo}. Provider message id: ${r.provider_message_id ?? '—'}` });
      } else {
        setTestResult({ ok: false, msg: r.error ?? 'unknown error' });
      }
    } catch (e) {
      setTestResult({ ok: false, msg: extractErr(e) });
    } finally { setBusy(null); }
  }

  return (
    <div className="card">
      <div className="card-hd">
        <h3 style={{ margin: 0 }}>SMS (Africa's Talking)</h3>
        <span className="card-sub">
          One Africa's Talking account for every tenant. Tenants don't see your API key.
        </span>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error" style={{ marginBottom: 10 }}>{err}</div>}

        <div className="grid-2">
          <Field label="Provider">
            <select value={provider} onChange={(e) => setProvider(e.target.value as SMSProvider)} style={{ width: '100%' }}>
              <option value="mock">Mock (dev — no network)</option>
              <option value="sandbox">Sandbox (Africa's Talking)</option>
              <option value="production">Production (Africa's Talking)</option>
            </select>
          </Field>
          <Field label="Sender ID (alphanumeric, up to 11 chars)">
            <input value={senderID} onChange={(e) => setSenderID(e.target.value)} placeholder="NEXUS" style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
          </Field>
          <Field label="AT username" hint={provider === 'mock' ? 'Optional for mock' : 'Required'}>
            <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="sandbox" style={{ width: '100%' }} />
          </Field>
          <Field
            label={cfg?.has_api_key ? 'New API key (leave blank to keep)' : 'API key'}
            hint={provider === 'mock' ? 'Optional for mock' : 'Required'}
          >
            <input
              type="password"
              value={apiKey}
              onChange={(e) => setAPIKey(e.target.value)}
              placeholder={cfg?.has_api_key ? '••••••••' : ''}
              style={{ width: '100%' }}
            />
          </Field>
          <Field label="Send rate cap (per minute)" hint="Workers honour this as a hard ceiling.">
            <input
              type="number"
              min={1}
              value={ratePerMinute}
              onChange={(e) => setRatePerMinute(parseInt(e.target.value, 10) || 0)}
              style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
            />
          </Field>
          <Field
            label={cfg?.has_webhook_secret ? 'New webhook secret (leave blank to keep)' : 'Webhook secret (optional)'}
            hint="Used to verify AT delivery-report POSTs."
          >
            <input
              type="password"
              value={webhookSecret}
              onChange={(e) => setWebhookSecret(e.target.value)}
              placeholder={cfg?.has_webhook_secret ? '••••••••' : ''}
              style={{ width: '100%' }}
            />
          </Field>
          <Field label="Active">
            <label className="row" style={{ gap: 6, alignItems: 'center' }}>
              <input type="checkbox" checked={isEnabled} onChange={(e) => setIsEnabled(e.target.checked)} />
              {isEnabled ? 'Workers will dispatch SMS using this driver.' : 'SMS queue paused.'}
            </label>
          </Field>
        </div>

        <div className="row" style={{ gap: 8, marginTop: 12, alignItems: 'center' }}>
          <button className="btn btn-accent" disabled={busy === 'save' || !senderID} onClick={() => void save()}>
            {busy === 'save' ? 'Saving…' : 'Save SMS config'}
          </button>
          {savedAt && <span className="muted tiny">Saved {savedAt}</span>}
        </div>

        <h4 style={{ marginTop: 18, marginBottom: 6 }}>Send a test SMS</h4>
        <div className="row" style={{ gap: 8, alignItems: 'flex-end' }}>
          <Field label="Recipient phone (E.164, e.g. +254712345678)">
            <input value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder="+254..." style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
          </Field>
          <button className="btn" disabled={busy === 'test' || !cfg?.is_enabled} onClick={() => void runTest()}>
            {busy === 'test' ? 'Sending…' : 'Send test'}
          </button>
        </div>
        {!cfg?.is_enabled && (
          <p className="muted tiny" style={{ marginTop: 6 }}>Enable the SMS config first.</p>
        )}
        {testResult && (
          <div className={`alert ${testResult.ok ? 'alert-info' : 'alert-error'}`} style={{ marginTop: 10 }}>
            {testResult.msg}
          </div>
        )}
      </div>
    </div>
  );
}

// ─────────── Bits ───────────

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
