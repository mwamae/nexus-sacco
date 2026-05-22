// Tenant-side configuration: branding / region / operations.
// Lives behind tenant:settings:view (read) and tenant:settings:edit (write).

import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  clearLogo,
  fetchTenantLogo,
  getTenantSettings,
  listDepositProducts,
  updateBranding,
  updateMembership,
  updateOperations,
  updateRegion,
  uploadLogo,
  extractError,
  getApprovalSettings,
  getOTPSettings,
  updateApprovalSettings,
  updateOTPSettings,
  type ApprovalToggles,
  type OTPSettings,
  type NotificationChannel,
  type DividendFrequency,
  type InterestMethod,
  type TenantBranding,
  type TenantMembership,
  type TenantOperations,
  type TenantRegion,
  type TenantSettings,
} from '../api/client';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';
import { Tabs } from '../components/Tabs';

type Tab = 'branding' | 'region' | 'operations' | 'membership' | 'approvals' | 'notifications';
const TABS: { id: Tab; label: string; hint: string }[] = [
  { id: 'branding',      label: 'Branding',      hint: 'Logo, colors, typography, channel sender IDs' },
  { id: 'region',        label: 'Region',        hint: 'Timezone, language, regulator, tax rates' },
  { id: 'operations',    label: 'Operations',    hint: 'Lending, savings, dividends, penalties, approval thresholds' },
  { id: 'membership',    label: 'Membership',    hint: 'Registration fee and onboarding policy applied to every new applicant.' },
  { id: 'approvals',     label: 'Approvals',     hint: 'Per-kind maker-checker toggles for cash actions' },
  { id: 'notifications', label: 'Notifications', hint: 'OTP / 2FA policy. SMS + email providers are managed by the platform — see the Credits page for balance + usage.' },
];

const FONT_OPTIONS = ['IBM Plex Sans', 'Inter', 'System', 'Roboto', 'Source Sans 3'];
const TZ_OPTIONS = [
  'Africa/Nairobi', 'Africa/Dar_es_Salaam', 'Africa/Kampala', 'Africa/Lagos',
  'Africa/Johannesburg', 'Africa/Cairo', 'Europe/London', 'UTC',
];
const LANG_OPTIONS = [
  { v: 'en', label: 'English' },
  { v: 'sw', label: 'Swahili' },
  { v: 'fr', label: 'French' },
];
const DATE_FORMATS = ['YYYY-MM-DD', 'DD/MM/YYYY', 'MM/DD/YYYY', 'D MMM YYYY'];

export default function TenantSettings() {
  const { hasPermission } = useAuth();
  const canView = hasPermission('tenant:settings:view');
  const canEdit = hasPermission('tenant:settings:edit');
  const [tab, setTab] = useState<Tab>('branding');
  const [s, setS] = useState<TenantSettings | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function reload() {
    setErr(null);
    try { setS(await getTenantSettings()); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); }, []);

  if (!canView) {
    return <div className="page"><div className="alert alert-error">You don't have permission to view settings.</div></div>;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{s?.tenant.name ?? 'Tenant'} · Configuration</div>
          <h1>Settings</h1>
          <div className="page-sub">
            White-label your SACCO, set regional defaults, and configure lending &amp; savings rules.
            {!canEdit && <> · <Badge tone="warn">read-only</Badge></>}
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card" style={{ padding: 0 }}>
        <Tabs ariaLabel="Tenant settings" tabs={TABS} value={tab} onChange={setTab}>
          {(activeId) => (
            <>
              <p className="muted tiny" style={{ margin: '0 0 14px' }}>{TABS.find((t) => t.id === activeId)?.hint}</p>
              {!s && <div className="empty">Loading…</div>}
              {s && activeId === 'branding' && (
                <BrandingTab branding={s.branding} canEdit={canEdit} onSaved={reload} />
              )}
              {s && activeId === 'region' && (
                <RegionTab region={s.region} canEdit={canEdit} onSaved={reload} />
              )}
              {s && activeId === 'operations' && (
                <OperationsTab operations={s.operations} currency={s.tenant.currency_code} canEdit={canEdit} onSaved={reload} />
              )}
              {s && activeId === 'membership' && (
                <MembershipTab membership={s.membership} currency={s.tenant.currency_code} canEdit={canEdit} onSaved={reload} />
              )}
              {s && activeId === 'approvals' && (
                <ApprovalsTab canEdit={canEdit} />
              )}
              {s && activeId === 'notifications' && (
                <NotificationsConfigTab canEdit={canEdit} />
              )}
            </>
          )}
        </Tabs>
      </div>
    </div>
  );
}

// ─────────── Approvals (Phase 7b) ───────────

function ToggleRow({
  k, t, disabled, onFlip,
}: {
  k: { key: keyof ApprovalToggles; label: string; hint: string };
  t: ApprovalToggles;
  disabled: boolean;
  onFlip: (field: keyof ApprovalToggles, next: boolean) => void;
}) {
  return (
    <div className="row" style={{ alignItems: 'center', gap: 12, padding: '8px 0', borderBottom: '1px solid var(--border)' }}>
      <label className="row" style={{ alignItems: 'center', gap: 6 }}>
        <input
          type="checkbox"
          checked={!!t[k.key]}
          disabled={disabled}
          onChange={(e) => onFlip(k.key, e.target.checked)}
        />
        <strong>{k.label}</strong>
      </label>
      <span className="muted tiny">{k.hint}</span>
    </div>
  );
}

function ApprovalsTab({ canEdit }: { canEdit: boolean }) {
  const [t, setT] = useState<ApprovalToggles | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setErr(null);
    try { setT(await getApprovalSettings()); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); }, []);

  async function flip(field: keyof ApprovalToggles, next: boolean) {
    setBusy(true); setErr(null);
    try {
      const updated = await updateApprovalSettings({ [field]: next } as Partial<ApprovalToggles>);
      setT(updated);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  if (err) return <div className="alert alert-error">{err}</div>;
  if (!t) return <div className="empty">Loading…</div>;

  const DEPOSIT_KINDS: Array<{ key: keyof ApprovalToggles; label: string; hint: string }> = [
    { key: 'deposit',          label: 'Deposits',           hint: 'Cash, M-Pesa, bank, payroll inflows to a deposit account.' },
    { key: 'withdrawal',       label: 'Withdrawals',        hint: 'Outflows from a deposit account.' },
    { key: 'deposit_transfer', label: 'Account transfers',  hint: 'Transfers between a member\'s own deposit accounts.' },
  ];
  const SHARE_KINDS: Array<{ key: keyof ApprovalToggles; label: string; hint: string }> = [
    { key: 'share_purchase', label: 'Share purchases', hint: 'Members buying shares (any payment channel).' },
    { key: 'share_transfer', label: 'Share transfers', hint: 'Member-to-member share transfers. Used during member exit since share capital cannot be redeemed.' },
    { key: 'share_bonus',    label: 'Bonus share issues', hint: 'Tenant-wide AGM-driven bonus issue. Affects every active account.' },
    { key: 'share_lien',     label: 'Share liens', hint: 'Pledging shares (e.g. as loan collateral).' },
  ];
  const LOAN_KINDS: Array<{ key: keyof ApprovalToggles; label: string; hint: string }> = [
    { key: 'loan_disbursement',        label: 'Loan disbursements',        hint: 'Releasing approved loan funds to the borrower. Acts as a second-line check on top of the existing loan approval workflow.' },
    { key: 'loan_repayment',           label: 'Loan repayments',           hint: 'Borrower-side repayments via any channel (cash, M-Pesa, auto-debit, payroll).' },
    { key: 'loan_settle',              label: 'Early settlements',         hint: 'Paying off the full outstanding balance ahead of schedule.' },
    { key: 'loan_reverse',             label: 'Reversals',                 hint: 'Reversing a posted loan transaction. Always carries audit risk.' },
    { key: 'loan_writeoff',            label: 'Write-offs',                hint: 'Board-authorised write-off of an unrecoverable loan. Cannot be reversed.' },
    { key: 'loan_reschedule',          label: 'Rescheduling',              hint: 'Re-amortising remaining principal over a new term.' },
    { key: 'loan_moratorium',          label: 'Moratoriums',               hint: 'Payment holidays — pushes unpaid installments forward.' },
    { key: 'loan_settlement_discount', label: 'Settlement discounts',      hint: 'Accepting less than the full balance as full payment.' },
  ];

  return (
    <>
      <p className="muted" style={{ marginTop: 0 }}>
        When a toggle is on, the action requires a second user to approve before it posts to
        the ledger. The original submitter shows up in the Cash approvals queue under their own
        name; a different user opens the row and clicks <strong>Approve &amp; post</strong>.
      </p>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Deposits</h3>
          <span className="card-sub">Cash actions on deposit accounts</span>
        </div>
        <div className="card-body">
          {DEPOSIT_KINDS.map((k) => <ToggleRow key={k.key} k={k} t={t} disabled={!canEdit || busy} onFlip={flip} />)}
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Shares</h3>
          <span className="card-sub">Share capital operations</span>
        </div>
        <div className="card-body">
          {SHARE_KINDS.map((k) => <ToggleRow key={k.key} k={k} t={t} disabled={!canEdit || busy} onFlip={flip} />)}
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Loans</h3>
          <span className="card-sub">Disbursement, servicing, and restructuring</span>
        </div>
        <div className="card-body">
          {LOAN_KINDS.map((k) => <ToggleRow key={k.key} k={k} t={t} disabled={!canEdit || busy} onFlip={flip} />)}
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Self-approval</h3>
          <span className="card-sub">Segregation of duties</span>
        </div>
        <div className="card-body">
          <label className="row" style={{ alignItems: 'center', gap: 6 }}>
            <input
              type="checkbox"
              checked={t.allow_self}
              disabled={!canEdit || busy}
              onChange={(e) => void flip('allow_self', e.target.checked)}
            />
            <span>Allow the same user to be both maker and checker</span>
          </label>
          <p className="muted tiny" style={{ marginTop: 8 }}>
            Off (recommended): the user who submitted the action cannot approve it. Required for SASRA-aligned
            segregation of duties. Turn on only for tiny SACCOs where the same person handles both roles.
          </p>
        </div>
      </div>
    </>
  );
}

// ─────────── Branding ───────────

function BrandingTab({ branding, canEdit, onSaved }: {
  branding: TenantBranding; canEdit: boolean; onSaved: () => void | Promise<void>;
}) {
  const [b, setB] = useState(branding);
  const [logoURL, setLogoURL] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => setB(branding), [branding]);

  // Load logo preview when one is on file.
  useEffect(() => {
    if (!branding.has_logo) { setLogoURL(null); return; }
    let revoked = false;
    let objectUrl: string | null = null;
    fetchTenantLogo().then((blob) => {
      if (!blob || revoked) return;
      objectUrl = URL.createObjectURL(blob);
      setLogoURL(objectUrl);
    }).catch(() => {});
    return () => { revoked = true; if (objectUrl) URL.revokeObjectURL(objectUrl); };
  }, [branding.has_logo, branding.logo_updated_at]);

  function dirtyPatch(): Partial<typeof b> {
    const out: Partial<typeof b> = {};
    if (b.primary_color !== branding.primary_color) out.primary_color = b.primary_color;
    if (b.accent_color !== branding.accent_color) out.accent_color = b.accent_color;
    if (b.font_family !== branding.font_family) out.font_family = b.font_family;
    if ((b.email_from_name ?? '') !== (branding.email_from_name ?? '')) out.email_from_name = b.email_from_name ?? '';
    if ((b.sms_sender_id ?? '') !== (branding.sms_sender_id ?? '')) out.sms_sender_id = b.sms_sender_id ?? '';
    if ((b.custom_domain ?? '') !== (branding.custom_domain ?? '')) out.custom_domain = b.custom_domain ?? '';
    return out;
  }
  const isDirty = Object.keys(dirtyPatch()).length > 0;

  async function save() {
    setErr(null);
    setBusy(true);
    try { await updateBranding(dirtyPatch()); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function onLogoPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setErr(null);
    setBusy(true);
    try { await uploadLogo(f); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); if (inputRef.current) inputRef.current.value = ''; }
  }

  async function onLogoClear() {
    if (!confirm('Remove the current logo?')) return;
    setBusy(true);
    try { await clearLogo(); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <>
      {err && <div className="alert alert-error">{err}</div>}

      <div className="grid-2">
        {/* ─────────── Logo ─────────── */}
        <Section title="Logo">
          <div className="row" style={{ alignItems: 'flex-start', gap: 14 }}>
            <div style={{
              width: 96, height: 96, borderRadius: 'var(--r-md)',
              border: '1px solid var(--border)', background: 'var(--surface-2)',
              display: 'grid', placeItems: 'center', overflow: 'hidden',
            }}>
              {logoURL ? (
                <img src={logoURL} alt="" style={{ width: '100%', height: '100%', objectFit: 'contain' }} />
              ) : (
                <span className="muted tiny">No logo</span>
              )}
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <p className="tiny muted" style={{ margin: 0 }}>
                PNG, JPEG, WebP, or SVG. Replaces the brand mark in the sidebar.
              </p>
              {branding.has_logo && (
                <p className="tiny-mono" style={{ margin: '6px 0 0', color: 'var(--fg-3)' }}>
                  {branding.logo_mime} · {((branding.logo_size_bytes ?? 0) / 1024).toFixed(1)} KB
                </p>
              )}
              <div className="row" style={{ marginTop: 10, gap: 6 }}>
                {canEdit && (
                  <>
                    <input
                      ref={inputRef}
                      type="file"
                      accept="image/png,image/jpeg,image/svg+xml,image/webp"
                      onChange={onLogoPick}
                      style={{ display: 'none' }}
                      disabled={busy}
                    />
                    <button className="btn btn-sm" disabled={busy} onClick={() => inputRef.current?.click()}>
                      <Icon name="arrow_up" size={12} /> {branding.has_logo ? 'Replace' : 'Upload'}
                    </button>
                    {branding.has_logo && (
                      <button className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} disabled={busy} onClick={onLogoClear}>
                        <Icon name="trash" size={12} /> Remove
                      </button>
                    )}
                  </>
                )}
              </div>
            </div>
          </div>
        </Section>

        {/* ─────────── Colors ─────────── */}
        <Section title="Colors">
          <div className="grid-2">
            <ColorField
              label="Primary"
              value={b.primary_color}
              onChange={(v) => setB({ ...b, primary_color: v })}
              disabled={!canEdit || busy}
            />
            <ColorField
              label="Accent"
              value={b.accent_color}
              onChange={(v) => setB({ ...b, accent_color: v })}
              disabled={!canEdit || busy}
            />
          </div>
          <Preview color={b.primary_color} accent={b.accent_color} />
        </Section>
      </div>

      <div className="grid-2" style={{ marginTop: 14 }}>
        {/* ─────────── Typography ─────────── */}
        <Section title="Typography">
          <Field label="Font family">
            <select className="select" value={b.font_family} disabled={!canEdit || busy} onChange={(e) => setB({ ...b, font_family: e.target.value })}>
              {FONT_OPTIONS.map((f) => <option key={f} value={f}>{f}</option>)}
            </select>
            <div className="field-hint">Applies to the dashboard chrome on the next reload.</div>
          </Field>
        </Section>

        {/* ─────────── Channels ─────────── */}
        <Section title="Communications">
          <Field label="Email — from name" hint="Overrides 'nexusSacco' in outbound emails.">
            <input className="input" disabled={!canEdit || busy} value={b.email_from_name ?? ''} onChange={(e) => setB({ ...b, email_from_name: e.target.value })} placeholder="Tujenge SACCO" />
          </Field>
          <Field label="SMS sender ID" hint="Alphanumeric ID or shortcode (telco-registered).">
            <input className="input mono" disabled={!canEdit || busy} value={b.sms_sender_id ?? ''} onChange={(e) => setB({ ...b, sms_sender_id: e.target.value })} placeholder="TUJENGE" maxLength={11} />
          </Field>
          <Field label="Custom domain" hint="Set up by your platform team — value is stored only.">
            <input className="input mono" disabled={!canEdit || busy} value={b.custom_domain ?? ''} onChange={(e) => setB({ ...b, custom_domain: e.target.value })} placeholder="banking.tujenge.co.ke" />
          </Field>
        </Section>
      </div>

      {canEdit && (
        <SaveBar disabled={!isDirty} busy={busy} onSave={save} />
      )}
    </>
  );
}

function ColorField({ label, value, onChange, disabled }: {
  label: string; value: string; onChange: (v: string) => void; disabled?: boolean;
}) {
  return (
    <Field label={label}>
      <div className="row" style={{ gap: 6 }}>
        <input
          type="color"
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value.toLowerCase())}
          style={{ width: 36, height: 30, padding: 0, border: '1px solid var(--border)', borderRadius: 'var(--r-md)', cursor: disabled ? 'not-allowed' : 'pointer' }}
        />
        <input
          className="input mono"
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          pattern="^#[0-9A-Fa-f]{6}$"
          maxLength={7}
        />
      </div>
    </Field>
  );
}

function Preview({ color, accent }: { color: string; accent: string }) {
  return (
    <div style={{
      marginTop: 12, padding: 14, borderRadius: 'var(--r-md)',
      border: '1px solid var(--border)', background: 'var(--surface-2)',
      display: 'flex', alignItems: 'center', gap: 14,
    }}>
      <div style={{
        width: 36, height: 36, borderRadius: 8,
        background: color, color: '#fff',
        display: 'grid', placeItems: 'center', fontWeight: 600,
      }}>Aa</div>
      <button
        type="button"
        style={{
          height: 28, padding: '0 12px', borderRadius: 'var(--r-md)',
          background: color, color: '#fff', border: '1px solid ' + color,
          font: 'inherit', fontSize: 12, fontWeight: 500, cursor: 'default',
        }}
      >Primary action</button>
      <span style={{
        height: 22, padding: '0 8px', borderRadius: 3,
        background: accent, color: '#fff',
        font: 'inherit', fontSize: 11, fontWeight: 500,
        display: 'inline-flex', alignItems: 'center',
      }}>Accent badge</span>
      <span className="muted tiny" style={{ marginLeft: 'auto' }}>Preview</span>
    </div>
  );
}

// ─────────── Region ───────────

function RegionTab({ region, canEdit, onSaved }: {
  region: TenantRegion; canEdit: boolean; onSaved: () => void | Promise<void>;
}) {
  const [r, setR] = useState(region);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => setR(region), [region]);

  const isDirty = useMemo(() => JSON.stringify(r) !== JSON.stringify(region), [r, region]);

  async function save() {
    setErr(null);
    setBusy(true);
    try { await updateRegion(r); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="grid-3">
        <Field label="Timezone">
          <select className="select" disabled={!canEdit || busy} value={r.timezone} onChange={(e) => setR({ ...r, timezone: e.target.value })}>
            {TZ_OPTIONS.map((t) => <option key={t} value={t}>{t}</option>)}
            {!TZ_OPTIONS.includes(r.timezone) && <option value={r.timezone}>{r.timezone}</option>}
          </select>
        </Field>
        <Field label="Language">
          <select className="select" disabled={!canEdit || busy} value={r.language} onChange={(e) => setR({ ...r, language: e.target.value })}>
            {LANG_OPTIONS.map((l) => <option key={l.v} value={l.v}>{l.label}</option>)}
          </select>
        </Field>
        <Field label="Date format">
          <select className="select" disabled={!canEdit || busy} value={r.date_format} onChange={(e) => setR({ ...r, date_format: e.target.value })}>
            {DATE_FORMATS.map((f) => <option key={f} value={f}>{f}</option>)}
          </select>
        </Field>
        <Field label="Regulator" hint="e.g. SASRA (KE), BoT (TZ), CBK (UG).">
          <input className="input" disabled={!canEdit || busy} value={r.regulator ?? ''} onChange={(e) => setR({ ...r, regulator: e.target.value })} placeholder="SASRA" />
        </Field>
        <Field label="Jurisdiction" hint="Country whose regulations apply.">
          <input className="input" disabled={!canEdit || busy} value={r.jurisdiction ?? ''} onChange={(e) => setR({ ...r, jurisdiction: e.target.value })} placeholder="Kenya" />
        </Field>
        <div />
        <Field label="VAT rate (%)" hint="Applied to fees and chargeable services.">
          <input className="input mono" type="number" step="0.01" min={0} max={100} disabled={!canEdit || busy} value={r.vat_rate} onChange={(e) => setR({ ...r, vat_rate: Number(e.target.value) })} />
        </Field>
        <Field label="Withholding tax rate (%)" hint="Applied to dividends / interest income.">
          <input className="input mono" type="number" step="0.01" min={0} max={100} disabled={!canEdit || busy} value={r.withholding_tax_rate} onChange={(e) => setR({ ...r, withholding_tax_rate: Number(e.target.value) })} />
        </Field>
      </div>
      {canEdit && <SaveBar disabled={!isDirty} busy={busy} onSave={save} />}
    </>
  );
}

// ─────────── Membership ───────────

const REGISTRATION_CHANNELS: Array<{ v: string; label: string; hint: string }> = [
  { v: 'mpesa',         label: 'M-Pesa',         hint: 'Paybill / Till confirmation code' },
  { v: 'airtel_money',  label: 'Airtel Money',   hint: 'Airtel transaction reference' },
  { v: 'bank_transfer', label: 'Bank transfer',  hint: 'EFT / RTGS / cheque deposit slip' },
  { v: 'cash',          label: 'Cash (teller)',  hint: 'Teller receipt number' },
  { v: 'cheque',        label: 'Cheque',         hint: 'Cheque number + drawer bank' },
];

function MembershipTab({ membership, currency, canEdit, onSaved }: {
  membership: TenantMembership; currency: string; canEdit: boolean; onSaved: () => void | Promise<void>;
}) {
  const [m, setM] = useState(membership);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [products, setProducts] = useState<Array<{ id: string; name: string; code: string }>>([]);
  useEffect(() => setM(membership), [membership]);
  useEffect(() => {
    void (async () => {
      try {
        const ps = await listDepositProducts(false);
        setProducts(ps.map((p) => ({ id: p.id, name: p.name, code: p.code })));
      } catch { /* products are optional for the form */ }
    })();
  }, []);

  const isDirty = useMemo(() => JSON.stringify(m) !== JSON.stringify(membership), [m, membership]);

  async function save() {
    setErr(null); setBusy(true);
    try { await updateMembership(m); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  function toggleChannel(v: string, on: boolean) {
    const next = on
      ? Array.from(new Set([...m.accepted_payment_channels, v]))
      : m.accepted_payment_channels.filter((c) => c !== v);
    setM({ ...m, accepted_payment_channels: next });
  }

  const disabled = !m.collect_registration_fee;

  return (
    <>
      {err && <div className="alert alert-error">{err}</div>}

      <Field label="Collect registration fee" hint="When off, the registration-fee step is hidden from the onboarding workflow entirely.">
        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: canEdit ? 'pointer' : 'default' }}>
          <input
            type="checkbox"
            checked={m.collect_registration_fee}
            onChange={(e) => setM({ ...m, collect_registration_fee: e.target.checked })}
            disabled={!canEdit || busy}
          />
          <span>{m.collect_registration_fee ? 'Yes — fee captured on every application' : 'No — onboarding skips the fee step'}</span>
        </label>
      </Field>

      <div className="grid-2">
        <Field label={`Fee — Individual (${currency})`} hint="Charged to natural persons. Must be ≥ 0.">
          <input
            className="input mono"
            type="number" step="0.01" min={0}
            value={m.registration_fee_individual}
            onChange={(e) => setM({ ...m, registration_fee_individual: Number(e.target.value) })}
            disabled={!canEdit || busy || disabled}
          />
        </Field>
        <Field label={`Fee — Institutional (${currency})`} hint="Charged to businesses, groups, chamas, companies, NGOs, etc.">
          <input
            className="input mono"
            type="number" step="0.01" min={0}
            value={m.registration_fee_institutional}
            onChange={(e) => setM({ ...m, registration_fee_institutional: Number(e.target.value) })}
            disabled={!canEdit || busy || disabled}
          />
        </Field>
      </div>

      <Field label="Accepted payment channels" hint="The officer captures one of these as proof of payment.">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 8 }}>
          {REGISTRATION_CHANNELS.map((c) => (
            <label key={c.v} style={{ display: 'flex', gap: 8, alignItems: 'flex-start', cursor: canEdit ? 'pointer' : 'default' }}>
              <input
                type="checkbox"
                checked={m.accepted_payment_channels.includes(c.v)}
                onChange={(e) => toggleChannel(c.v, e.target.checked)}
                disabled={!canEdit || busy || disabled}
              />
              <div>
                <div style={{ fontWeight: 600 }}>{c.label}</div>
                <div className="muted tiny">{c.hint}</div>
              </div>
            </label>
          ))}
        </div>
      </Field>

      <Field label="Fee is refundable on rejection" hint="When on, declining a paid application prompts an officer to post the refund leg to the GL. When off, the fee is retained as SACCO income.">
        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: canEdit ? 'pointer' : 'default' }}>
          <input
            type="checkbox"
            checked={m.fee_refundable_on_rejection}
            onChange={(e) => setM({ ...m, fee_refundable_on_rejection: e.target.checked })}
            disabled={!canEdit || busy || disabled}
          />
          <span>{m.fee_refundable_on_rejection ? 'Refundable — declined applications trigger a refund' : 'Non-refundable — declined applications retain the fee'}</span>
        </label>
      </Field>

      <Field label="Default deposit product for new members" hint="When set, the approval pipeline auto-opens a zero-balance savings account in this product for every newly-activated member. When unset, only the share account is opened — the operator opens deposit accounts manually later.">
        <select
          className="select"
          value={m.default_deposit_product_id ?? ''}
          onChange={(e) => setM({ ...m, default_deposit_product_id: e.target.value || null })}
          disabled={!canEdit || busy}
        >
          <option value="">— None (skip auto-open) —</option>
          {products.map((p) => (
            <option key={p.id} value={p.id}>{p.code} · {p.name}</option>
          ))}
        </select>
      </Field>

      {canEdit && <SaveBar disabled={!isDirty} busy={busy} onSave={save} />}
    </>
  );
}

// ─────────── Operations ───────────

function OperationsTab({ operations, currency, canEdit, onSaved }: {
  operations: TenantOperations; currency: string; canEdit: boolean; onSaved: () => void | Promise<void>;
}) {
  const [o, setO] = useState(operations);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => setO(operations), [operations]);

  const isDirty = useMemo(() => JSON.stringify(o) !== JSON.stringify(operations), [o, operations]);

  async function save() {
    setErr(null);
    setBusy(true);
    try { await updateOperations(o); await onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  function num<K extends keyof TenantOperations>(k: K) {
    return (v: number | string) => setO({ ...o, [k]: typeof v === 'string' ? Number(v) : v } as TenantOperations);
  }

  return (
    <>
      {err && <div className="alert alert-error">{err}</div>}

      <Section title="Lending defaults">
        <div className="grid-3">
          <Field label={`Min loan amount (${currency})`}>
            <input className="input mono" type="number" min={0} step={100} disabled={!canEdit || busy} value={o.loan_min_amount} onChange={(e) => num('loan_min_amount')(e.target.value)} />
          </Field>
          <Field label={`Max loan amount (${currency})`}>
            <input className="input mono" type="number" min={0} step={1000} disabled={!canEdit || busy} value={o.loan_max_amount} onChange={(e) => num('loan_max_amount')(e.target.value)} />
          </Field>
          <Field label="Max term (months)">
            <input className="input mono" type="number" min={1} max={360} step={1} disabled={!canEdit || busy} value={o.loan_max_term_months} onChange={(e) => num('loan_max_term_months')(e.target.value)} />
          </Field>
          <Field label="Default interest method">
            <select className="select" disabled={!canEdit || busy} value={o.default_interest_method} onChange={(e) => setO({ ...o, default_interest_method: e.target.value as InterestMethod })}>
              <option value="reducing_balance">Reducing balance</option>
              <option value="declining_balance">Declining balance</option>
              <option value="flat">Flat</option>
            </select>
          </Field>
          <Field label="Default interest rate (% p.a.)">
            <input className="input mono" type="number" step={0.01} min={0} max={200} disabled={!canEdit || busy} value={o.default_interest_rate} onChange={(e) => num('default_interest_rate')(e.target.value)} />
          </Field>
        </div>
      </Section>

      <Section title="Savings rules">
        <div className="grid-3">
          <Field label={`Min opening balance (${currency})`}>
            <input className="input mono" type="number" min={0} step={50} disabled={!canEdit || busy} value={o.savings_min_opening_bal} onChange={(e) => num('savings_min_opening_bal')(e.target.value)} />
          </Field>
          <Field label={`Min running balance (${currency})`}>
            <input className="input mono" type="number" min={0} step={50} disabled={!canEdit || busy} value={o.savings_min_running_bal} onChange={(e) => num('savings_min_running_bal')(e.target.value)} />
          </Field>
          <Field label={`Withdrawal fee (${currency})`}>
            <input className="input mono" type="number" min={0} step={1} disabled={!canEdit || busy} value={o.savings_withdrawal_fee} onChange={(e) => num('savings_withdrawal_fee')(e.target.value)} />
          </Field>
        </div>
      </Section>

      <Section title="Dividend rules">
        <div className="grid-3">
          <Field label="Dividend rate (% p.a.)">
            <input className="input mono" type="number" step={0.01} min={0} max={100} disabled={!canEdit || busy} value={o.dividend_rate} onChange={(e) => num('dividend_rate')(e.target.value)} />
          </Field>
          <Field label="Distribution frequency">
            <select className="select" disabled={!canEdit || busy} value={o.dividend_frequency} onChange={(e) => setO({ ...o, dividend_frequency: e.target.value as DividendFrequency })}>
              <option value="annual">Annual</option>
              <option value="semi_annual">Semi-annual</option>
              <option value="quarterly">Quarterly</option>
            </select>
          </Field>
        </div>
      </Section>

      <Section title="Penalty rules">
        <div className="grid-3">
          <Field label="Late repayment fee (% of overdue)">
            <input className="input mono" type="number" step={0.01} min={0} max={100} disabled={!canEdit || busy} value={o.penalty_late_fee_rate} onChange={(e) => num('penalty_late_fee_rate')(e.target.value)} />
          </Field>
          <Field label="Grace period (days)">
            <input className="input mono" type="number" min={0} step={1} disabled={!canEdit || busy} value={o.penalty_grace_period_days} onChange={(e) => num('penalty_grace_period_days')(e.target.value)} />
          </Field>
        </div>
      </Section>

      <Section title="Guarantor policies">
        <div className="grid-3">
          <Field label="Minimum guarantors">
            <input className="input mono" type="number" min={0} step={1} disabled={!canEdit || busy} value={o.guarantor_min_count} onChange={(e) => num('guarantor_min_count')(e.target.value)} />
          </Field>
          <Field label={`Self-guarantor max amount (${currency})`} hint="Loans up to this size do not need external guarantors.">
            <input className="input mono" type="number" min={0} step={1000} disabled={!canEdit || busy} value={o.guarantor_self_max_amount} onChange={(e) => num('guarantor_self_max_amount')(e.target.value)} />
          </Field>
        </div>
      </Section>

      <Section title="Approval levels" hint="Loan amounts above each threshold require approval at that level.">
        <div className="grid-3">
          <Field label={`Branch limit (${currency})`}>
            <input className="input mono" type="number" min={0} step={1000} disabled={!canEdit || busy} value={o.approval_branch_limit} onChange={(e) => num('approval_branch_limit')(e.target.value)} />
          </Field>
          <Field label={`Credit committee limit (${currency})`}>
            <input className="input mono" type="number" min={0} step={1000} disabled={!canEdit || busy} value={o.approval_credit_limit} onChange={(e) => num('approval_credit_limit')(e.target.value)} />
          </Field>
          <Field label={`Board limit (${currency})`}>
            <input className="input mono" type="number" min={0} step={1000} disabled={!canEdit || busy} value={o.approval_board_limit} onChange={(e) => num('approval_board_limit')(e.target.value)} />
          </Field>
        </div>
      </Section>

      {canEdit && <SaveBar disabled={!isDirty} busy={busy} onSave={save} />}
    </>
  );
}

// ─────────── presentation helpers ───────────

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 14 }}>
      <div className="h-sec" style={{ marginBottom: 2 }}>{title}</div>
      {hint && <p className="muted tiny" style={{ margin: '0 0 8px' }}>{hint}</p>}
      {children}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label className="field-label">{label}</label>
      {children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

function SaveBar({ disabled, busy, onSave }: { disabled: boolean; busy: boolean; onSave: () => void | Promise<void> }) {
  return (
    <div style={{
      position: 'sticky', bottom: 0, marginTop: 18, padding: '10px 14px',
      background: 'var(--surface-2)', borderTop: '1px solid var(--border)',
      borderRadius: '0 0 var(--r-md) var(--r-md)',
      display: 'flex', alignItems: 'center', gap: 10,
    }}>
      <span className="muted tiny">{disabled ? 'No changes' : 'Unsaved changes'}</span>
      <span className="spacer" style={{ flex: 1 }} />
      <button className="btn btn-sm btn-accent" disabled={disabled || busy} onClick={() => void onSave()}>
        <Icon name="check" size={12} /> {busy ? 'Saving…' : 'Save changes'}
      </button>
    </div>
  );
}

// ─────────── Notifications config (Stage 2 — SMTP) ───────────

function NotificationsConfigTab({ canEdit }: { canEdit: boolean }) {
  return (
    <>
      <div className="alert alert-info" style={{ marginBottom: 14 }}>
        SMTP and SMS provider configuration is now managed by the platform — your SACCO uses
        the shared driver and pays per credit. Manage your <strong>credit balance, top-ups
        and usage history</strong> from the <strong>Credits</strong> page in the sidebar.
      </div>
      <OTPConfigSection canEdit={canEdit} />
    </>
  );
}

// ─────────── OTP config (Stage 6) ───────────

function OTPConfigSection({ canEdit }: { canEdit: boolean }) {
  const [cfg, setCfg] = useState<OTPSettings | null | undefined>(undefined);
  const [codeLength, setCodeLength] = useState(6);
  const [expiry, setExpiry] = useState(5);
  const [maxAttempts, setMaxAttempts] = useState(3);
  const [cooldown, setCooldown] = useState(60);
  const [channel, setChannel] = useState<NotificationChannel>('sms');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const c = await getOTPSettings();
        setCfg(c);
        setCodeLength(c.code_length);
        setExpiry(c.expiry_minutes);
        setMaxAttempts(c.max_attempts);
        setCooldown(c.resend_cooldown_seconds);
        setChannel(c.default_channel);
      } catch (e) { setErr(e instanceof Error ? e.message : 'failed to load'); }
    })();
  }, []);

  async function save() {
    setBusy(true); setErr(null); setSavedAt(null);
    try {
      const c = await updateOTPSettings({
        code_length: codeLength,
        expiry_minutes: expiry,
        max_attempts: maxAttempts,
        resend_cooldown_seconds: cooldown,
        default_channel: channel,
      });
      setCfg(c);
      setSavedAt(new Date().toISOString());
    } catch (e) { setErr(e instanceof Error ? e.message : 'save failed'); }
    finally { setBusy(false); }
  }

  if (cfg === undefined) return <div className="empty">Loading OTP settings…</div>;
  if (cfg === null) return <div className="alert alert-error">OTP settings unavailable.</div>;

  return (
    <div className="card">
      <div className="card-hd">
        <h3>OTP / 2FA</h3>
        <span className="card-sub">
          Central one-time-code policy. Every service that needs an OTP (login MFA, transaction sign-off, password reset, phone/email verification) calls the notification service. Codes are HMAC-SHA256-hashed before storage.
        </span>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error">{err}</div>}
        <div className="grid-3">
          <Field label="Code length (digits)" hint="4–8. Spec recommends 6.">
            <input className="input mono" type="number" min={4} max={8} value={codeLength}
              onChange={(e) => setCodeLength(parseInt(e.target.value, 10) || 6)} disabled={!canEdit} />
          </Field>
          <Field label="Expiry (minutes)" hint="1–60. Default 5.">
            <input className="input mono" type="number" min={1} max={60} value={expiry}
              onChange={(e) => setExpiry(parseInt(e.target.value, 10) || 5)} disabled={!canEdit} />
          </Field>
          <Field label="Max verification attempts" hint="3–5. Exhaustion is terminal.">
            <input className="input mono" type="number" min={3} max={5} value={maxAttempts}
              onChange={(e) => setMaxAttempts(parseInt(e.target.value, 10) || 3)} disabled={!canEdit} />
          </Field>
          <Field label="Resend cooldown (seconds)" hint="15–600. Prevents OTP spam.">
            <input className="input mono" type="number" min={15} max={600} value={cooldown}
              onChange={(e) => setCooldown(parseInt(e.target.value, 10) || 60)} disabled={!canEdit} />
          </Field>
          <Field label="Default delivery channel" hint="Overridable per request.">
            <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as NotificationChannel)} disabled={!canEdit}>
              <option value="sms">SMS</option>
              <option value="email">Email</option>
              <option value="in_app">In-app</option>
            </select>
          </Field>
        </div>
        {canEdit && (
          <div className="row" style={{ marginTop: 10, gap: 8 }}>
            <button className="btn btn-accent" disabled={busy} onClick={() => void save()}>
              <Icon name="check" size={12} /> {busy ? 'Saving…' : 'Save OTP settings'}
            </button>
            {savedAt && <span className="muted tiny" style={{ alignSelf: 'center' }}>Saved {new Date(savedAt).toLocaleTimeString()}</span>}
          </div>
        )}
        <p className="muted tiny" style={{ marginTop: 12 }}>
          Last updated {cfg.updated_at?.slice(0, 19).replace('T', ' ')} · request audit is available via the notification log (filter by event <span className="mono">OTP_REQUESTED</span>).
        </p>
      </div>
    </div>
  );
}
