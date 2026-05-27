// /settings/mpesa — staff-facing surface for the Daraja integration.
//
// What's here:
//   • List of registered paybills with status chips
//   • Test-auth button per row (calls /v1/mpesa/paybills/{id}/test-auth)
//   • Rotate-credentials modal (sends ciphertext-bound payload)
//   • Daraja webhook URLs section (copy-to-clipboard) — every paybill
//     shows the exact URLs to paste into the Daraja portal, complete
//     with the embedded webhook_token
//   • Recent traffic panel — last 50 inbound events
//
// Permission model: read-only when the user lacks
// tenant:settings:edit; Add / Rotate / Test buttons hide. Everyone
// with tenant:settings:view sees the list + URLs (URLs are still
// secret-bearing but visibility is gated by the same tenant scope).

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  listMpesaPaybills,
  createMpesaPaybill,
  testMpesaAuth,
  rotateMpesaCredential,
  listMpesaInboundEvents,
  mpesaDarajaWebhookURLs,
  extractError,
  type ApiMpesaPaybill,
  type ApiMpesaInboundEvent,
  type MpesaCredentialKind,
  type MpesaEnvironment,
  type MpesaPaybillPurpose,
  type MpesaTestAuthResult,
} from '../../api/client';
import { Badge } from '../../components/Badge';
import { Icon } from '../../components/Icon';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

export default function MpesaPaybills() {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('tenant:settings:edit');
  useDocumentTitle('M-PESA paybills');

  const [paybills, setPaybills] = useState<ApiMpesaPaybill[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<ApiMpesaPaybill | null>(null);
  const [registerOpen, setRegisterOpen] = useState(false);
  const [rotateModal, setRotateModal] = useState<ApiMpesaPaybill | null>(null);
  const [testResults, setTestResults] = useState<Record<string, MpesaTestAuthResult & { busy?: boolean }>>({});
  const [eventsByPaybill, setEventsByPaybill] = useState<Record<string, ApiMpesaInboundEvent[]>>({});

  async function load() {
    setErr(null);
    try {
      const list = await listMpesaPaybills();
      setPaybills(list);
      // Auto-select the first paybill so the URLs + traffic panels
      // show something meaningful on first render.
      if (!selected && list.length > 0) setSelected(list[0]);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  // Refresh the recent-traffic list whenever the selected paybill
  // changes. Capped at 50 rows; the dashboard panel never paginates.
  useEffect(() => {
    if (!selected) return;
    let cancelled = false;
    listMpesaInboundEvents({ paybill_id: selected.id, limit: 50 })
      .then((r) => {
        if (cancelled) return;
        setEventsByPaybill((m) => ({ ...m, [selected.id]: r.events }));
      })
      .catch(() => { /* leave undefined; the panel renders "no traffic" */ });
    return () => { cancelled = true; };
  }, [selected]);

  async function runTestAuth(p: ApiMpesaPaybill) {
    setTestResults((m) => ({ ...m, [p.id]: { ok: false, busy: true } }));
    try {
      const r = await testMpesaAuth(p.id);
      setTestResults((m) => ({ ...m, [p.id]: { ...r, busy: false } }));
    } catch (e) {
      setTestResults((m) => ({
        ...m,
        [p.id]: { ok: false, error: extractError(e), busy: false },
      }));
    }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/settings" style={{ color: 'inherit' }}>← Settings</a></div>
          <h1>M-PESA paybills</h1>
          <div className="page-sub">
            Manage Daraja paybills + credentials, copy callback URLs,
            and watch live traffic. {!canEdit && '— Read-only (no tenant:settings:edit).'}
          </div>
        </div>
        {canEdit && (
          <button className="btn btn-accent" onClick={() => setRegisterOpen(true)}>
            + Register paybill
          </button>
        )}
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {!paybills && !err && <div className="empty">Loading…</div>}
      {paybills && paybills.length === 0 && (
        <div className="card"><div className="card-body">
          No paybills registered yet.
          {canEdit
            ? <> Click <strong>Register paybill</strong> above to add one.</>
            : <> Ask a tenant admin (needs <code>tenant:settings:edit</code>) to register one.</>}
        </div></div>
      )}

      {paybills && paybills.length > 0 && (
        <>
          <div className="card">
            <div className="card-hd">
              <h3>Paybills</h3>
              <span className="card-sub">{paybills.length} registered</span>
            </div>
            <div className="card-body flush">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Shortcode</th>
                    <th>Label</th>
                    <th>Purpose</th>
                    <th>Scope</th>
                    <th>Env</th>
                    <th>Status</th>
                    <th style={{ width: 1 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {paybills.map((p) => {
                    const test = testResults[p.id];
                    const isSelected = selected?.id === p.id;
                    return (
                      <tr key={p.id} style={{ background: isSelected ? 'var(--surface-subtle)' : undefined }}>
                        <td className="mono"><strong>{p.shortcode}</strong></td>
                        <td>
                          <a className="tbl-link" href="#" onClick={(e) => { e.preventDefault(); setSelected(p); }}>
                            {p.label}
                          </a>
                          {p.is_default && <Badge tone="accent" style={{ marginLeft: 6 }}>Default</Badge>}
                        </td>
                        <td><PurposeBadge purpose={p.purpose} /></td>
                        <td>
                          {(p.scope ?? []).map((s) => (
                            <Badge key={s} tone="neutral" style={{ marginRight: 4 }}>{s}</Badge>
                          ))}
                        </td>
                        <td><Badge tone={p.environment === 'production' ? 'pos' : 'warn'}>{p.environment}</Badge></td>
                        <td>
                          <Badge tone={p.status === 'active' ? 'pos' : 'neg'}>{p.status}</Badge>
                        </td>
                        <td>
                          <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                            {canEdit && (
                              <button className="btn btn-sm" disabled={test?.busy}
                                onClick={() => void runTestAuth(p)}>
                                {test?.busy ? 'Testing…' : 'Test auth'}
                              </button>
                            )}
                            {canEdit && (
                              <button className="btn btn-sm" onClick={() => setRotateModal(p)}>
                                Rotate creds
                              </button>
                            )}
                          </div>
                          {test && !test.busy && (
                            <div className="tiny" style={{ marginTop: 4, textAlign: 'right' }}>
                              {test.ok
                                ? <span style={{ color: 'var(--pos)' }}>✓ OK · expires {test.expires_at?.slice(0, 16).replace('T', ' ')}</span>
                                : <span style={{ color: 'var(--neg)' }}>✗ {test.error ?? 'failed'}</span>}
                            </div>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          {selected && (
            <>
              <DarajaURLsPanel paybill={selected} />
              <RecentTrafficPanel paybill={selected} events={eventsByPaybill[selected.id]} />
            </>
          )}
        </>
      )}

      {rotateModal && (
        <RotateCredentialsModal
          paybill={rotateModal}
          onClose={() => setRotateModal(null)}
          onSaved={async () => { setRotateModal(null); }}
        />
      )}
      {registerOpen && (
        <RegisterPaybillModal
          onClose={() => setRegisterOpen(false)}
          onCreated={async (p) => {
            setRegisterOpen(false);
            await load();
            setSelected(p);
          }}
        />
      )}
    </div>
  );
}

// ─────────── Daraja URLs ───────────

function DarajaURLsPanel({ paybill }: { paybill: ApiMpesaPaybill }) {
  const urls = useMemo(() => mpesaDarajaWebhookURLs(paybill), [paybill]);
  const isB2C = paybill.purpose === 'disbursement' || paybill.purpose === 'both';
  const isC2B = paybill.purpose === 'collection' || paybill.purpose === 'both';
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="card-hd">
        <h3>Daraja portal URLs · {paybill.label}</h3>
        <span className="card-sub">Paste these into the Safaricom Daraja portal. The token in the query string is the paybill's webhook secret — keep it confidential.</span>
      </div>
      <div className="card-body" style={{ display: 'grid', gap: 8 }}>
        {isC2B && (
          <>
            <CopyableURL label="Validation URL (C2B)"    value={urls.validation} />
            <CopyableURL label="Confirmation URL (C2B)" value={urls.confirmation} />
          </>
        )}
        {isB2C && (
          <>
            <CopyableURL label="Result URL (B2C)"       value={urls.b2cResult} />
            <CopyableURL label="Timeout URL (B2C)"      value={urls.b2cTimeout} />
          </>
        )}
      </div>
    </div>
  );
}

function CopyableURL({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch { /* ignore — staff can select manually */ }
  }
  return (
    <div style={{ display: 'grid', gap: 4 }}>
      <div className="muted tiny">{label}</div>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <code style={{
          flex: 1, padding: '6px 10px', background: 'var(--surface-subtle)',
          borderRadius: 'var(--r-sm)', fontSize: 12, wordBreak: 'break-all',
        }}>{value}</code>
        <button type="button" className="btn btn-sm" onClick={() => void copy()}>
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}

// ─────────── Recent traffic ───────────

function RecentTrafficPanel({ paybill, events }: { paybill: ApiMpesaPaybill; events?: ApiMpesaInboundEvent[] }) {
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <div className="card-hd">
        <h3>Recent traffic · {paybill.label}</h3>
        <span className="card-sub">Last 50 inbound events</span>
        <div className="card-hd-actions">
          <a className="btn btn-sm" href={`/accounting/mpesa-reconciliation?paybill_id=${paybill.id}`}>
            Open in reconciliation →
          </a>
        </div>
      </div>
      <div className="card-body flush">
        {events === undefined && <div className="empty">Loading…</div>}
        {events !== undefined && events.length === 0 && (
          <div className="empty">No inbound events yet for this paybill.</div>
        )}
        {events && events.length > 0 && (
          <table className="tbl">
            <thead>
              <tr>
                <th>Received</th>
                <th>Tx ID</th>
                <th>From</th>
                <th>Bill ref</th>
                <th style={{ textAlign: 'right' }}>Amount</th>
                <th>Resolved via</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td className="tiny-mono">{new Date(e.received_at).toISOString().replace('T', ' ').slice(0, 19)}</td>
                  <td className="mono tiny">{e.transaction_id}</td>
                  <td className="mono tiny">{e.msisdn}</td>
                  <td className="mono tiny">{e.bill_ref || '—'}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{e.amount}</td>
                  <td>{e.resolved_via ? <Badge tone={e.resolved_via === 'unallocated' ? 'warn' : 'neutral'}>{e.resolved_via}</Badge> : <span className="muted tiny">—</span>}</td>
                  <td><InboundStatusBadge status={e.status} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────── Rotate-credentials modal ───────────

function RotateCredentialsModal({ paybill, onClose, onSaved }: {
  paybill: ApiMpesaPaybill;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<MpesaCredentialKind[]>([]);
  const [draft, setDraft] = useState<Record<MpesaCredentialKind, string>>({
    consumer_key: '',
    consumer_secret: '',
    passkey: '',
    initiator_name: '',
    initiator_password: '',
  });
  const fields: { kind: MpesaCredentialKind; label: string; hint?: string }[] = [
    { kind: 'consumer_key',       label: 'Consumer key' },
    { kind: 'consumer_secret',    label: 'Consumer secret',    hint: 'Treated as a password.' },
    { kind: 'passkey',            label: 'Passkey',            hint: 'STK push + C2B confirmation signature.' },
    { kind: 'initiator_name',     label: 'Initiator name',     hint: 'Required for B2C only.' },
    { kind: 'initiator_password', label: 'Initiator password', hint: 'RSA-encrypted before submit. Required for B2C only.' },
  ];

  async function submit() {
    setBusy(true); setErr(null);
    try {
      // Only push fields the operator filled in. Empty inputs leave
      // the existing ciphertext untouched.
      const written: MpesaCredentialKind[] = [];
      for (const f of fields) {
        const v = draft[f.kind].trim();
        if (!v) continue;
        await rotateMpesaCredential(paybill.id, f.kind, v);
        written.push(f.kind);
      }
      setDone(written);
      if (written.length === 0) {
        setErr('No fields filled — nothing to rotate.');
        return;
      }
      await onSaved();
    } catch (e) {
      setErr(extractError(e));
    } finally { setBusy(false); }
  }

  return (
    <ModalShell
      title={`Rotate credentials · ${paybill.label}`}
      busy={busy}
      submitLabel={done.length > 0 ? 'Close' : 'Save'}
      onClose={onClose}
      onSubmit={done.length > 0 ? onClose : submit}
    >
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Plaintext values are envelope-encrypted before write. Existing
        ciphertext rows are replaced atomically. Fields left blank
        keep their current values.
      </p>
      {err && <div className="alert alert-error">{err}</div>}
      {done.length > 0 && (
        <div className="alert alert-success">
          Rotated: {done.join(', ')}. Test auth on the row to confirm.
        </div>
      )}
      {fields.map((f) => (
        <Field key={f.kind} label={f.label} hint={f.hint}>
          <input
            className="input"
            type="password"
            value={draft[f.kind]}
            onChange={(e) => setDraft((d) => ({ ...d, [f.kind]: e.target.value }))}
            placeholder="Leave blank to keep current value"
            autoComplete="off"
          />
        </Field>
      ))}
    </ModalShell>
  );
}

// ─────────── Register-paybill modal ───────────

// Scope options, grouped by which purposes the backend permits them
// for. The user picks freely; the form filters to the relevant set
// when purpose changes (defensible defaults vs full freedom). The
// backend doesn't enforce scope contents — keeping the UI list tight
// is purely a UX choice so operators don't ship typos.
const SCOPE_OPTIONS: { value: string; label: string; purposes: MpesaPaybillPurpose[] }[] = [
  { value: 'member_deposits',   label: 'Member deposits',   purposes: ['collection', 'both'] },
  { value: 'loan_repayment',    label: 'Loan repayment',    purposes: ['collection', 'both'] },
  { value: 'fees',              label: 'Fees',              purposes: ['collection', 'both'] },
  { value: 'loan_disbursement', label: 'Loan disbursement', purposes: ['disbursement', 'both'] },
  { value: 'refund',            label: 'Refund',            purposes: ['disbursement', 'both'] },
];

function RegisterPaybillModal({ onClose, onCreated }: {
  onClose: () => void;
  onCreated: (p: ApiMpesaPaybill) => Promise<void> | void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [created, setCreated] = useState<ApiMpesaPaybill | null>(null);

  const [label, setLabel] = useState('');
  const [shortcode, setShortcode] = useState('');
  const [purpose, setPurpose] = useState<MpesaPaybillPurpose>('collection');
  const [environment, setEnvironment] = useState<MpesaEnvironment>('sandbox');
  const [scope, setScope] = useState<string[]>(['member_deposits']);

  const visibleScopes = useMemo(
    () => SCOPE_OPTIONS.filter((s) => s.purposes.includes(purpose)),
    [purpose],
  );

  // When purpose changes, drop any selected scopes that are no longer
  // valid + auto-tick the first sensible default if the user hasn't
  // chosen anything (avoids submitting an empty scope by accident).
  useEffect(() => {
    setScope((prev) => {
      const visible = visibleScopes.map((s) => s.value);
      const kept = prev.filter((s) => visible.includes(s));
      if (kept.length === 0 && visible.length > 0) return [visible[0]];
      return kept;
    });
  }, [visibleScopes]);

  function toggleScope(v: string) {
    setScope((prev) => (prev.includes(v) ? prev.filter((s) => s !== v) : [...prev, v]));
  }

  async function submit() {
    setErr(null);
    if (!label.trim() || !shortcode.trim()) {
      setErr('Label and shortcode are required.');
      return;
    }
    setBusy(true);
    try {
      const p = await createMpesaPaybill({
        label: label.trim(),
        shortcode: shortcode.trim(),
        purpose,
        scope,
        environment,
      });
      setCreated(p);
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  // After-create state shows the new paybill's webhook URLs so the
  // operator can copy them into the Daraja portal before closing.
  // Closing fires onCreated, which refreshes the list + auto-selects
  // the new row.
  if (created) {
    return (
      <ModalShell
        title={`Registered · ${created.label}`}
        submitLabel="Done"
        onClose={() => void onCreated(created)}
        onSubmit={() => void onCreated(created)}
      >
        <div className="alert alert-success" style={{ marginTop: 0 }}>
          Paybill <strong>{created.shortcode}</strong> registered. Copy
          the Daraja portal URLs below before closing — the webhook
          token is embedded and grants ingress to the C2B handler.
        </div>
        <DarajaURLsPanel paybill={created} />
        <p className="muted tiny" style={{ marginTop: 12 }}>
          Next steps: open the row's <strong>Rotate creds</strong>
          {' '}modal to paste the Daraja consumer key + secret (plus
          initiator name/password for B2C), then <strong>Test auth</strong>
          {' '}to confirm OAuth round-trip works.
        </p>
      </ModalShell>
    );
  }

  return (
    <ModalShell
      title="Register paybill"
      busy={busy}
      submitLabel="Register"
      onClose={onClose}
      onSubmit={submit}
    >
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Registers a new Safaricom paybill / till for this tenant. Daraja
        credentials come in a separate step via Rotate creds — this form
        only persists the metadata + generates the webhook token.
      </p>
      {err && <div className="alert alert-error">{err}</div>}

      <Field label="Label" hint="Human-readable name (e.g. 'Sandbox C2B + B2C').">
        <input
          className="input"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Sandbox paybill"
          autoFocus
        />
      </Field>

      <Field label="Shortcode" hint="Safaricom paybill / till number.">
        <input
          className="input mono"
          value={shortcode}
          onChange={(e) => setShortcode(e.target.value)}
          placeholder="600000"
        />
      </Field>

      <Field label="Purpose" hint="C2B = incoming. B2C = outgoing. Both = same shortcode handles both flows.">
        <select
          className="input"
          value={purpose}
          onChange={(e) => setPurpose(e.target.value as MpesaPaybillPurpose)}
        >
          <option value="collection">Collection (C2B)</option>
          <option value="disbursement">Disbursement (B2C)</option>
          <option value="both">Both (C2B + B2C)</option>
        </select>
      </Field>

      <Field label="Environment" hint="Production paybills must have MPESA_TRUSTED_IPS set on the mpesa service.">
        <select
          className="input"
          value={environment}
          onChange={(e) => setEnvironment(e.target.value as MpesaEnvironment)}
        >
          <option value="sandbox">Sandbox</option>
          <option value="production">Production</option>
        </select>
      </Field>

      <Field label="Scope" hint="What this paybill is allowed to handle. Filtered by purpose.">
        <div style={{ display: 'grid', gap: 6 }}>
          {visibleScopes.map((s) => (
            <label key={s.value} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <input
                type="checkbox"
                checked={scope.includes(s.value)}
                onChange={() => toggleScope(s.value)}
              />
              <span>{s.label}</span>
              <code className="muted tiny">{s.value}</code>
            </label>
          ))}
        </div>
      </Field>
    </ModalShell>
  );
}

// ─────────── Local modal shell (lightweight) ───────────

function ModalShell({ title, busy, onClose, children, submitLabel, onSubmit }: {
  title: string; busy?: boolean; onClose: () => void;
  children: ReactNode; submitLabel: string; onSubmit: () => void | Promise<void>;
}) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 520, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy} onClick={() => void onSubmit()}>
            {busy ? 'Working…' : submitLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

// ─────────── Small badge helpers ───────────

function PurposeBadge({ purpose }: { purpose: ApiMpesaPaybill['purpose'] }) {
  const label = purpose === 'both' ? 'C2B + B2C' : purpose === 'disbursement' ? 'B2C' : 'C2B';
  return <Badge tone="accent">{label}</Badge>;
}

function InboundStatusBadge({ status }: { status: ApiMpesaInboundEvent['status'] }) {
  const tone = status === 'distributed' ? 'pos' : status === 'failed' ? 'neg' : 'warn';
  return <Badge tone={tone}>{status}</Badge>;
}
