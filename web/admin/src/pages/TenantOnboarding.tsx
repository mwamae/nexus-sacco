// Tenant onboarding wizard — platform-admin only. Five steps modeled on
// the member-onboarding wizard so the UX stays consistent.

import { Fragment, useState, type ReactNode } from 'react';
import {
  createTenant,
  extractError,
  type BillingPlan,
  type BranchKind,
  type BranchInput,
  type ContactInput,
  type CreateTenantInput,
} from '../api/client';
import { Icon } from '../components/Icon';

const STEPS = [
  'Organization',
  'Locale & plan',
  'Branches',
  'Contact persons',
  'Owner & review',
] as const;

type FormState = {
  // Organization
  name: string;
  slug: string;
  legal_name: string;
  kind: string;
  registration_no: string;
  tax_pin: string;
  license_no: string;

  // Locale & plan
  country_code: string;
  currency_code: string;
  billing_plan: BillingPlan;

  // Branches
  branches: BranchRow[];

  // Contact persons
  contacts: ContactRow[];

  // Owner
  owner_name: string;
  owner_email: string;
  owner_phone: string;
  owner_password: string;
  owner_confirm: string;
};

type BranchRow = BranchInput & { kind: BranchKind };
type ContactRow = ContactInput;

const INITIAL: FormState = {
  name: '',
  slug: '',
  legal_name: '',
  kind: 'sacco',
  registration_no: '',
  tax_pin: '',
  license_no: '',
  country_code: 'KE',
  currency_code: 'KES',
  billing_plan: 'starter',
  branches: [{ code: 'HQ', name: 'Head Office', kind: 'hq', county: '', sub_county: '', physical_address: '', phone: '' }],
  contacts: [{ full_name: '', title: '', email: '', phone: '' }],
  owner_name: '',
  owner_email: '',
  owner_phone: '',
  owner_password: '',
  owner_confirm: '',
};

const PLAN_LABEL: Record<BillingPlan, string> = {
  starter: 'Starter',
  standard: 'Standard',
  premium: 'Premium',
  enterprise: 'Enterprise',
};

const PLAN_BLURB: Record<BillingPlan, string> = {
  starter: 'Small SACCO. Core member + savings, single branch.',
  standard: 'Full lending workflow, multi-branch, reports.',
  premium: 'Advanced underwriting, integrations, premium support.',
  enterprise: 'Custom pricing for groups & federations.',
};

export default function TenantOnboarding() {
  const [step, setStep] = useState(0);
  const [s, setS] = useState<FormState>(INITIAL);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function update<K extends keyof FormState>(k: K, v: FormState[K]) {
    setS((p) => ({ ...p, [k]: v }));
  }

  const stepErrors = validateStep(step, s);
  const canContinue = stepErrors.length === 0;

  async function onSubmit() {
    setSubmitting(true);
    setError(null);
    try {
      // Strip empty branch + contact rows that the wizard pre-fills.
      const branches = s.branches
        .filter((b) => b.code.trim() && b.name.trim())
        .map((b) => ({
          code: b.code.trim().toUpperCase(),
          name: b.name.trim(),
          kind: b.kind,
          county: b.county?.trim() || undefined,
          sub_county: b.sub_county?.trim() || undefined,
          physical_address: b.physical_address?.trim() || undefined,
          phone: b.phone?.trim() || undefined,
        }));
      const contacts = s.contacts
        .filter((c) => c.full_name.trim())
        .map((c) => ({
          full_name: c.full_name.trim(),
          title: c.title?.trim() || undefined,
          email: c.email?.trim() || undefined,
          phone: c.phone?.trim() || undefined,
        }));

      const input: CreateTenantInput = {
        slug: s.slug.trim().toLowerCase(),
        name: s.name.trim(),
        legal_name: s.legal_name.trim() || undefined,
        kind: s.kind,
        country_code: s.country_code.trim().toUpperCase(),
        currency_code: s.currency_code.trim().toUpperCase(),
        license_no: s.license_no.trim() || undefined,
        registration_no: s.registration_no.trim() || undefined,
        tax_pin: s.tax_pin.trim().toUpperCase() || undefined,
        billing_plan: s.billing_plan,
        owner_email: s.owner_email.trim().toLowerCase(),
        owner_name: s.owner_name.trim(),
        owner_phone: s.owner_phone.trim() || undefined,
        // Blank = invite flow. The backend creates the owner in
        // Pending state and emails an activation link.
        owner_password: s.owner_password.trim() || undefined,
        branches: branches.length ? branches : undefined,
        contacts: contacts.length ? contacts : undefined,
      };
      const r = await createTenant(input);
      window.location.assign(`/?onboarded=${r.tenant.slug}`);
    } catch (e) {
      setError(extractError(e, 'Could not create tenant'));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Platform · Tenant onboarding</div>
          <h1>New tenant</h1>
          <div className="page-sub">Five steps · sets up the SACCO, its branch network, primary contacts, and tenant owner login.</div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href="/"><Icon name="x" size={12} /> Discard</a>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div style={{ padding: '14px 20px' }}>
          <Stepper current={step} onJump={setStep} />
        </div>
      </div>

      {error && <div className="alert alert-error">{error}</div>}

      <div className="card">
        <div className="card-hd">
          <h3>{STEPS[step]}</h3>
          <span className="card-sub">Step {step + 1} of {STEPS.length}</span>
        </div>
        <div className="card-body">
          {step === 0 && <StepOrg s={s} update={update} />}
          {step === 1 && <StepLocale s={s} update={update} />}
          {step === 2 && <StepBranches s={s} setS={setS} />}
          {step === 3 && <StepContacts s={s} setS={setS} />}
          {step === 4 && <StepOwnerReview s={s} update={update} />}
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div style={{ padding: '12px 20px', display: 'flex', alignItems: 'center', gap: 10, background: 'var(--surface-2)' }}>
          <button
            className="btn btn-sm"
            disabled={step === 0 || submitting}
            onClick={() => setStep((x) => Math.max(0, x - 1))}
          >
            <Icon name="chevron_l" size={12} /> Back
          </button>
          <span className="muted tiny">
            {stepErrors.length > 0 ? (
              <span style={{ color: 'var(--neg)' }}>{stepErrors[0]}</span>
            ) : (
              <>Step {step + 1} of {STEPS.length}</>
            )}
          </span>
          <span className="spacer" />
          {step < STEPS.length - 1 ? (
            <button
              className="btn btn-sm btn-accent"
              disabled={!canContinue || submitting}
              onClick={() => setStep((x) => Math.min(STEPS.length - 1, x + 1))}
            >
              Continue <Icon name="chevron_r" size={12} />
            </button>
          ) : (
            <button
              className="btn btn-sm btn-accent"
              disabled={!canContinue || submitting}
              onClick={() => void onSubmit()}
            >
              <Icon name="check" size={12} />
              {submitting ? 'Creating tenant…' : 'Create tenant'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// ─────────── Stepper ───────────

function Stepper({ current, onJump }: { current: number; onJump: (i: number) => void }) {
  return (
    <div className="row" style={{ gap: 0 }}>
      {STEPS.map((s, i) => {
        const done = i < current;
        const active = i === current;
        return (
          <Fragment key={i}>
            <button
              type="button"
              onClick={() => onJump(i)}
              disabled={i > current}
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                flexShrink: 0, background: 'transparent', border: 0, padding: 0,
                cursor: i > current ? 'not-allowed' : 'pointer',
                opacity: i > current ? 0.5 : 1,
              }}
            >
              <div style={{
                width: 22, height: 22, borderRadius: '50%',
                display: 'grid', placeItems: 'center',
                fontSize: 11, fontWeight: 600,
                background: done ? 'var(--accent)' : active ? 'var(--surface)' : 'var(--surface-2)',
                color: done ? '#fff' : active ? 'var(--accent)' : 'var(--fg-3)',
                border: active ? '1.5px solid var(--accent)' : '1px solid var(--border)',
              }}>
                {done ? <Icon name="check" size={11} stroke={2.5} /> : i + 1}
              </div>
              <span style={{ fontSize: 12, fontWeight: active ? 500 : 400, color: !done && !active ? 'var(--fg-3)' : 'var(--fg)' }}>
                {s}
              </span>
            </button>
            {i < STEPS.length - 1 && <div style={{ flex: 1, height: 1, background: 'var(--border)', margin: '0 12px' }} />}
          </Fragment>
        );
      })}
    </div>
  );
}

// ─────────── Validation ───────────

function validateStep(step: number, s: FormState): string[] {
  const errs: string[] = [];
  if (step === 0) {
    if (!s.name.trim()) errs.push('Organization name is required');
    if (!s.slug.trim()) errs.push('Slug is required');
    else if (!/^[a-z0-9]([a-z0-9-]{1,38}[a-z0-9])?$/.test(s.slug.trim())) {
      errs.push('Slug must be 3-40 chars, lowercase letters/digits/hyphens');
    }
    if (s.tax_pin && !/^[A-Za-z][0-9]{9}[A-Za-z]$/.test(s.tax_pin.trim())) {
      errs.push('Tax PIN format looks invalid (expect e.g. A123456789Z)');
    }
  }
  if (step === 1) {
    if (s.country_code.length !== 2) errs.push('Country must be a 2-letter ISO code');
    if (s.currency_code.length !== 3) errs.push('Currency must be a 3-letter ISO code');
  }
  if (step === 2) {
    const populated = s.branches.filter((b) => b.code.trim() || b.name.trim());
    for (const b of populated) {
      if (!b.code.trim() || !b.name.trim()) {
        errs.push('Each branch needs both a code and a name');
        break;
      }
    }
    const codes = new Set<string>();
    for (const b of populated) {
      const c = b.code.trim().toUpperCase();
      if (codes.has(c)) {
        errs.push(`Duplicate branch code: ${c}`);
        break;
      }
      codes.add(c);
    }
  }
  if (step === 4) {
    if (!s.owner_name.trim()) errs.push('Primary contact name is required');
    if (!s.owner_email.trim()) errs.push('Primary contact email is required');
    // Password is now optional — when blank the owner is created in
    // pending state and receives an invitation email. Only validate
    // length + match when something has actually been entered.
    if (s.owner_password) {
      if (s.owner_password.length < 12) errs.push('If setting a password, it must be ≥ 12 characters');
      if (s.owner_password !== s.owner_confirm) errs.push('Password and confirmation do not match');
    }
  }
  return errs;
}

// ─────────── Step bodies ───────────

function StepOrg({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <div className="grid-3">
      <Field label="Organization name" required>
        <input
          className="input"
          value={s.name}
          onChange={(e) => {
            update('name', e.target.value);
            if (!s.slug) update('slug', slugify(e.target.value));
          }}
          placeholder="Harambee SACCO Society"
        />
      </Field>
      <Field label="Slug (subdomain)" required hint="Used in the URL: <slug>.nexussacco.local">
        <input
          className="input mono"
          value={s.slug}
          onChange={(e) => update('slug', e.target.value.toLowerCase())}
          placeholder="harambee"
        />
      </Field>
      <Field label="Tenant type">
        <select className="select" value={s.kind} onChange={(e) => update('kind', e.target.value)}>
          <option value="sacco">SACCO</option>
          <option value="microfinance">Microfinance</option>
          <option value="digital_lender">Digital lender</option>
          <option value="cooperative">Cooperative</option>
          <option value="chama">Chama</option>
        </select>
      </Field>
      <Field label="Legal name" hint="If different from the organization name.">
        <input className="input" value={s.legal_name} onChange={(e) => update('legal_name', e.target.value)} placeholder="Harambee SACCO Society Limited" />
      </Field>
      <Field label="Registration number" hint="e.g. cooperative or company registry">
        <input className="input mono" value={s.registration_no} onChange={(e) => update('registration_no', e.target.value)} placeholder="COOP/12345" />
      </Field>
      <Field label="Tax PIN" hint="Government tax id (e.g. KRA PIN: A123456789Z).">
        <input className="input mono" value={s.tax_pin} onChange={(e) => update('tax_pin', e.target.value.toUpperCase())} placeholder="P051234567Z" maxLength={11} />
      </Field>
      <Field label="License number" hint="e.g. SASRA license (optional).">
        <input className="input mono" value={s.license_no} onChange={(e) => update('license_no', e.target.value)} placeholder="SASRA/2018/0421" />
      </Field>
    </div>
  );
}

function StepLocale({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <>
      <div className="grid-3">
        <Field label="Country (ISO-2)" required>
          <input className="input mono" maxLength={2} value={s.country_code} onChange={(e) => update('country_code', e.target.value.toUpperCase())} />
        </Field>
        <Field label="Currency (ISO-3)" required>
          <input className="input mono" maxLength={3} value={s.currency_code} onChange={(e) => update('currency_code', e.target.value.toUpperCase())} />
        </Field>
      </div>

      <div className="divider" />
      <div className="h-sec">Billing plan</div>
      <div className="grid-4">
        {(Object.keys(PLAN_LABEL) as BillingPlan[]).map((p) => {
          const on = s.billing_plan === p;
          return (
            <button
              key={p}
              type="button"
              className="card"
              style={{
                textAlign: 'left',
                cursor: 'pointer',
                borderColor: on ? 'var(--accent)' : 'var(--border)',
                background: on ? 'var(--accent-bg)' : 'var(--surface)',
                padding: 0,
              }}
              onClick={() => update('billing_plan', p)}
            >
              <div className="card-hd">
                <h3>{PLAN_LABEL[p]}</h3>
                {on && <span className="card-sub" style={{ color: 'var(--accent-fg)' }}>selected</span>}
              </div>
              <div className="card-body">
                <p className="tiny muted" style={{ margin: 0 }}>{PLAN_BLURB[p]}</p>
              </div>
            </button>
          );
        })}
      </div>
    </>
  );
}

function StepBranches({ s, setS }: { s: FormState; setS: (fn: (p: FormState) => FormState) => void }) {
  function setB(i: number, key: keyof BranchRow, v: string) {
    setS((p) => {
      const next = p.branches.slice();
      next[i] = { ...next[i], [key]: v };
      return { ...p, branches: next };
    });
  }
  function addB() {
    setS((p) => ({ ...p, branches: [...p.branches, { code: '', name: '', kind: 'branch', county: '', sub_county: '', physical_address: '', phone: '' }] }));
  }
  function removeB(i: number) {
    setS((p) => ({ ...p, branches: p.branches.filter((_, idx) => idx !== i) }));
  }

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0, marginBottom: 8 }}>
        Start with the head office. Add branches and agencies you operate today — you can add more later.
      </p>

      {s.branches.map((b, i) => (
        <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 8 }}>
          <div className="card-hd">
            <h3>{b.kind === 'hq' ? 'Head office' : `${b.kind === 'agency' ? 'Agency' : 'Branch'} ${i + 1}`}</h3>
            <span className="card-sub">{b.code || 'no code'} · {b.name || 'unnamed'}</span>
            <div className="card-hd-actions">
              {s.branches.length > 1 && (
                <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeB(i)} aria-label="Remove">
                  <Icon name="trash" size={12} />
                </button>
              )}
            </div>
          </div>
          <div className="card-body">
            <div className="grid-3">
              <Field label="Code" required hint="Short id, e.g. HQ, NK01.">
                <input className="input mono" value={b.code} onChange={(e) => setB(i, 'code', e.target.value.toUpperCase())} maxLength={20} />
              </Field>
              <Field label="Name" required>
                <input className="input" value={b.name} onChange={(e) => setB(i, 'name', e.target.value)} placeholder="Nakuru Branch" />
              </Field>
              <Field label="Kind">
                <select className="select" value={b.kind} onChange={(e) => setB(i, 'kind', e.target.value)}>
                  <option value="hq">Head office</option>
                  <option value="branch">Branch</option>
                  <option value="agency">Agency</option>
                </select>
              </Field>
              <Field label="County">
                <input className="input" value={b.county ?? ''} onChange={(e) => setB(i, 'county', e.target.value)} />
              </Field>
              <Field label="Sub-county">
                <input className="input" value={b.sub_county ?? ''} onChange={(e) => setB(i, 'sub_county', e.target.value)} />
              </Field>
              <Field label="Phone">
                <input className="input mono" value={b.phone ?? ''} onChange={(e) => setB(i, 'phone', e.target.value)} />
              </Field>
              <Field label="Physical address" wide>
                <input className="input" value={b.physical_address ?? ''} onChange={(e) => setB(i, 'physical_address', e.target.value)} placeholder="Building, street, area" />
              </Field>
            </div>
          </div>
        </div>
      ))}

      <button type="button" className="btn btn-sm" onClick={addB}>
        <Icon name="plus" size={12} /> Add another branch
      </button>
    </>
  );
}

function StepContacts({ s, setS }: { s: FormState; setS: (fn: (p: FormState) => FormState) => void }) {
  function setC(i: number, key: keyof ContactRow, v: string) {
    setS((p) => {
      const next = p.contacts.slice();
      next[i] = { ...next[i], [key]: v };
      return { ...p, contacts: next };
    });
  }
  function addC() {
    setS((p) => ({ ...p, contacts: [...p.contacts, { full_name: '', title: '', email: '', phone: '' }] }));
  }
  function removeC(i: number) {
    setS((p) => ({ ...p, contacts: p.contacts.filter((_, idx) => idx !== i) }));
  }

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0, marginBottom: 8 }}>
        Senior contacts the platform team should call about this tenant — typically the CEO, operations manager, and IT lead.
      </p>

      {s.contacts.map((c, i) => (
        <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 8 }}>
          <div className="card-hd">
            <h3>Contact {i + 1}</h3>
            <span className="card-sub">{c.title || 'no title'}</span>
            <div className="card-hd-actions">
              {s.contacts.length > 1 && (
                <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeC(i)} aria-label="Remove">
                  <Icon name="trash" size={12} />
                </button>
              )}
            </div>
          </div>
          <div className="card-body">
            <div className="grid-3">
              <Field label="Full name">
                <input className="input" value={c.full_name} onChange={(e) => setC(i, 'full_name', e.target.value)} />
              </Field>
              <Field label="Title / role">
                <input className="input" value={c.title ?? ''} onChange={(e) => setC(i, 'title', e.target.value)} placeholder="CEO" />
              </Field>
              <Field label="Phone">
                <input className="input mono" value={c.phone ?? ''} onChange={(e) => setC(i, 'phone', e.target.value)} placeholder="+254 …" />
              </Field>
              <Field label="Email" wide>
                <input className="input" type="email" value={c.email ?? ''} onChange={(e) => setC(i, 'email', e.target.value)} />
              </Field>
            </div>
          </div>
        </div>
      ))}

      <button type="button" className="btn btn-sm" onClick={addC}>
        <Icon name="plus" size={12} /> Add another contact
      </button>
    </>
  );
}

function StepOwnerReview({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <>
      <div className="h-sec">Primary contact &amp; tenant owner</div>
      <p className="muted tiny" style={{ marginTop: -4, marginBottom: 8 }}>
        The primary contact is automatically provisioned as the <code className="mono">tenant_owner</code> (Tenant Super Admin).
        Leave the password fields blank to send them an <strong>invitation email</strong> — they set their own password and are activated automatically. Filling in a password creates them as Active immediately.
      </p>
      <div className="grid-3">
        <Field label="Full name" required>
          <input className="input" value={s.owner_name} onChange={(e) => update('owner_name', e.target.value)} />
        </Field>
        <Field label="Work email" required hint="Becomes their login identifier.">
          <input className="input" type="email" value={s.owner_email} onChange={(e) => update('owner_email', e.target.value)} />
        </Field>
        <Field label="Phone">
          <input className="input mono" value={s.owner_phone} onChange={(e) => update('owner_phone', e.target.value)} />
        </Field>
        <Field label="Password (optional)" hint="Leave blank to use the invite flow (recommended).">
          <input className="input" type="password" value={s.owner_password} onChange={(e) => update('owner_password', e.target.value)} minLength={12} />
        </Field>
        <Field label="Confirm password">
          <input className="input" type="password" value={s.owner_confirm} onChange={(e) => update('owner_confirm', e.target.value)} />
        </Field>
      </div>

      <div className="divider" />

      <div className="h-sec">Review</div>
      <ReviewSection title="Organization">
        <Row k="Name" v={s.name} />
        <Row k="Slug" v={s.slug} mono />
        <Row k="Type" v={s.kind} />
        <Row k="Legal name" v={s.legal_name || '—'} />
        <Row k="Registration #" v={s.registration_no || '—'} mono />
        <Row k="Tax PIN" v={s.tax_pin || '—'} mono />
        <Row k="License #" v={s.license_no || '—'} mono />
      </ReviewSection>
      <ReviewSection title="Locale & plan">
        <Row k="Country" v={s.country_code} mono />
        <Row k="Currency" v={s.currency_code} mono />
        <Row k="Billing plan" v={PLAN_LABEL[s.billing_plan]} />
      </ReviewSection>
      <ReviewSection title={`Branches (${s.branches.filter((b) => b.code && b.name).length})`}>
        {s.branches.filter((b) => b.code && b.name).length === 0 ? (
          <Row k="" v={<span className="muted">No branches added.</span>} />
        ) : (
          s.branches.filter((b) => b.code && b.name).map((b, i) => (
            <Row key={i} k={<><span className="mono">{b.code}</span> · {b.name}</>} v={<span className="muted">{b.kind}{b.county ? ` · ${b.county}` : ''}</span>} />
          ))
        )}
      </ReviewSection>
      <ReviewSection title={`Contacts (${s.contacts.filter((c) => c.full_name).length})`}>
        {s.contacts.filter((c) => c.full_name).length === 0 ? (
          <Row k="" v={<span className="muted">No contacts added.</span>} />
        ) : (
          s.contacts.filter((c) => c.full_name).map((c, i) => (
            <Row key={i} k={c.full_name} v={<span className="muted">{c.title || 'no title'}{c.email ? ` · ${c.email}` : ''}</span>} />
          ))
        )}
      </ReviewSection>
    </>
  );
}

// ─────────── helpers ───────────

function Field({ label, hint, required, wide, children }: { label: ReactNode; hint?: string; required?: boolean; wide?: boolean; children: ReactNode }) {
  return (
    <div className="field" style={wide ? { gridColumn: 'span 3' } : undefined}>
      <label className="field-label">
        {label}
        {required && <span className="req"> *</span>}
      </label>
      {children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

function ReviewSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="card" style={{ marginBottom: 8, background: 'var(--surface-2)' }}>
      <div className="card-hd">
        <h3>{title}</h3>
      </div>
      <div className="card-body">
        <dl className="kvs">{children}</dl>
      </div>
    </div>
  );
}

function Row({ k, v, mono }: { k: ReactNode; v: ReactNode; mono?: boolean }) {
  return (
    <>
      <dt>{k}</dt>
      <dd className={mono ? 'mono' : ''}>{v}</dd>
    </>
  );
}

function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 40);
}
