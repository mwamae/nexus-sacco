// Organisation onboarding wizard. Six steps; submit creates the org
// plus officials, signatories, mandate, banking, and contacts in a
// single POST. Document uploads happen post-create on the profile page
// so we don't have to multipart-the-wizard.

import { Fragment, useState, type ReactNode } from 'react';
import {
  createOrg,
  extractError,
  type ContactKind,
  type CreateOrgInput,
  type Gender,
  type IDDocKind,
  type OfficialInput,
  type OfficialPosition,
  type OrgKind,
  type RiskCategory,
  type SignatoryClass,
} from '../api/client';
import { Icon } from '../components/Icon';

const STEPS = [
  'Profile',
  'Officials',
  'Signatories & mandate',
  'Banking',
  'Contacts',
  'Review',
] as const;

const KIND_OPTIONS: { v: OrgKind; label: string; hint: string }[] = [
  { v: 'group',       label: 'Group',         hint: 'Welfare / investment group' },
  { v: 'chama',       label: 'Chama',         hint: 'Informal savings group' },
  { v: 'ltd',         label: 'Limited Co.',   hint: 'Private limited company' },
  { v: 'sole_prop',   label: 'Sole Prop.',    hint: 'Sole proprietorship' },
  { v: 'ngo',         label: 'NGO',           hint: 'Non-governmental organisation' },
  { v: 'church',      label: 'Church',        hint: 'Religious organisation' },
  { v: 'sacco',       label: 'SACCO',         hint: 'Sister cooperative society' },
  { v: 'cooperative', label: 'Cooperative',   hint: 'Producer / consumer co-op' },
  { v: 'school',      label: 'School',        hint: 'Educational institution' },
];

type OfficialRow = OfficialInput;

type ContactRow = { kind: ContactKind; full_name: string; role: string; phone: string; email: string };

type FormState = {
  // Profile
  registered_name: string;
  trading_name: string;
  kind: OrgKind;
  registration_no: string;
  date_of_registration: string;
  date_of_operation: string;
  industry: string;
  nature_of_business: string;
  member_count: string;
  employee_count: string;

  physical_address: string;
  postal_address: string;
  county: string;
  sub_county: string;
  ward: string;

  risk_category: RiskCategory;

  // Officials
  officials: OfficialRow[];

  // Mandate
  mandate_default: 'any_one' | 'any_two' | 'all' | 'custom';
  mandate_rule_amount: string;
  mandate_rule_required: string; // CSV of position codes for the >amount rule

  // Banking
  bank_name: string;
  bank_branch: string;
  bank_code: string;
  swift_code: string;
  account_name: string;
  account_number: string;
  paybill: string;
  till_number: string;
  mobile_money_phones: string;
  mobile_settlement_account: string;
  preferred_disbursement: string;
  preferred_repayment: string;
  standing_order_details: string;
  checkoff_arrangement: string;

  // Contacts
  contacts: ContactRow[];
};

const EMPTY_OFFICIAL: OfficialRow = {
  full_name: '', id_doc_kind: 'national_id', id_doc_number: '', kra_pin: '',
  date_of_birth: '', gender: 'undisclosed', nationality: 'Kenyan', phone: '', email: '',
  physical_address: '', occupation: '', position: 'director', position_label: '', appointed_on: '',
  is_pep: false, pep_note: '', is_beneficial_owner: false, ownership_percent: undefined,
};

const INITIAL: FormState = {
  registered_name: '', trading_name: '', kind: 'chama', registration_no: '',
  date_of_registration: '', date_of_operation: '',
  industry: '', nature_of_business: '',
  member_count: '', employee_count: '',
  physical_address: '', postal_address: '', county: '', sub_county: '', ward: '',
  risk_category: 'medium',
  officials: [
    { ...EMPTY_OFFICIAL, position: 'chairperson', signatory: { class: 'mandatory', signing_order: 1 } },
    { ...EMPTY_OFFICIAL, position: 'treasurer',   signatory: { class: 'mandatory', signing_order: 2 } },
  ],
  mandate_default: 'any_two',
  mandate_rule_amount: '500000',
  mandate_rule_required: 'chairperson,treasurer',
  bank_name: '', bank_branch: '', bank_code: '', swift_code: '',
  account_name: '', account_number: '',
  paybill: '', till_number: '', mobile_money_phones: '', mobile_settlement_account: '',
  preferred_disbursement: '', preferred_repayment: '',
  standing_order_details: '', checkoff_arrangement: '',
  contacts: [
    { kind: 'primary', full_name: '', role: '', phone: '', email: '' },
    { kind: 'finance', full_name: '', role: '', phone: '', email: '' },
  ],
};

export default function OrganizationOnboarding() {
  const [step, setStep] = useState(0);
  const [s, setS] = useState<FormState>(INITIAL);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function update<K extends keyof FormState>(k: K, v: FormState[K]) {
    setS((p) => ({ ...p, [k]: v }));
  }

  const errors = validateStep(step, s);
  const canContinue = errors.length === 0;

  async function onSubmit() {
    setSubmitting(true);
    setError(null);
    try {
      const officials = s.officials
        .filter((o) => o.full_name.trim() && o.id_doc_number.trim())
        .map((o, i) => ({
          ...o,
          full_name: o.full_name.trim(),
          id_doc_number: o.id_doc_number.trim(),
          signatory: o.signatory ? {
            ...o.signatory,
            signing_order: o.signatory.signing_order ?? i + 1,
          } : undefined,
        }));
      const contacts = s.contacts
        .filter((c) => c.full_name.trim())
        .map((c) => ({
          kind: c.kind,
          full_name: c.full_name.trim(),
          role: c.role.trim() || undefined,
          phone: c.phone.trim() || undefined,
          email: c.email.trim() || undefined,
        }));

      // Build mandate from the structured pickers.
      const mandate: Record<string, unknown> = { default: s.mandate_default };
      if (s.mandate_rule_amount && s.mandate_rule_required) {
        mandate.rules = [{
          above_amount: Number(s.mandate_rule_amount),
          require: s.mandate_rule_required.split(',').map((x) => x.trim()).filter(Boolean),
        }];
      }

      const banking = {
        bank_name: s.bank_name.trim(), bank_branch: s.bank_branch.trim(),
        bank_code: s.bank_code.trim(), swift_code: s.swift_code.trim(),
        account_name: s.account_name.trim(), account_number: s.account_number.trim(),
        paybill: s.paybill.trim(), till_number: s.till_number.trim(),
        mobile_money_phones: s.mobile_money_phones.trim(),
        mobile_settlement_account: s.mobile_settlement_account.trim(),
        preferred_disbursement: s.preferred_disbursement.trim(),
        preferred_repayment: s.preferred_repayment.trim(),
        standing_order_details: s.standing_order_details.trim(),
        checkoff_arrangement: s.checkoff_arrangement.trim(),
      };

      const input: CreateOrgInput = {
        registered_name: s.registered_name.trim(),
        trading_name: s.trading_name.trim() || undefined,
        kind: s.kind,
        registration_no: s.registration_no.trim() || undefined,
        date_of_registration: s.date_of_registration || undefined,
        date_of_operation: s.date_of_operation || undefined,
        industry: s.industry.trim() || undefined,
        nature_of_business: s.nature_of_business.trim() || undefined,
        member_count: s.member_count ? Number(s.member_count) : undefined,
        employee_count: s.employee_count ? Number(s.employee_count) : undefined,
        physical_address: s.physical_address.trim() || undefined,
        postal_address: s.postal_address.trim() || undefined,
        county: s.county.trim() || undefined,
        sub_county: s.sub_county.trim() || undefined,
        ward: s.ward.trim() || undefined,
        risk_category: s.risk_category,
        officials, banking, contacts, mandate,
      };
      const created = await createOrg(input);
      window.location.assign(`/orgs/${created.id}?tab=documents`);
    } catch (e) {
      setError(extractError(e, 'Could not create organisation'));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Members · Onboarding · Organisation</div>
          <h1>New organisation</h1>
          <div className="page-sub">
            Six steps. The organisation lands in <strong>pending</strong> with KYC <strong>not started</strong>; upload documents on the profile page right after submit to move it into review.
          </div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href="/orgs"><Icon name="x" size={12} /> Discard</a>
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
          {step === 0 && <StepProfile s={s} update={update} />}
          {step === 1 && <StepOfficials s={s} setS={setS} />}
          {step === 2 && <StepSignatories s={s} setS={setS} update={update} />}
          {step === 3 && <StepBanking s={s} update={update} />}
          {step === 4 && <StepContacts s={s} setS={setS} />}
          {step === 5 && <StepReview s={s} />}
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div style={{ padding: '12px 20px', display: 'flex', alignItems: 'center', gap: 10, background: 'var(--surface-2)' }}>
          <button className="btn btn-sm" disabled={step === 0 || submitting} onClick={() => setStep((x) => Math.max(0, x - 1))}>
            <Icon name="chevron_l" size={12} /> Back
          </button>
          <span className="muted tiny">
            {errors.length > 0 ? <span style={{ color: 'var(--neg)' }}>{errors[0]}</span> : <>Step {step + 1} of {STEPS.length}</>}
          </span>
          <span className="spacer" style={{ flex: 1 }} />
          {step < STEPS.length - 1 ? (
            <button className="btn btn-sm btn-accent" disabled={!canContinue || submitting} onClick={() => setStep((x) => Math.min(STEPS.length - 1, x + 1))}>
              Continue <Icon name="chevron_r" size={12} />
            </button>
          ) : (
            <button className="btn btn-sm btn-accent" disabled={!canContinue || submitting} onClick={() => void onSubmit()}>
              <Icon name="check" size={12} />
              {submitting ? 'Creating…' : 'Create organisation'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// ─────────── Validation ───────────

function validateStep(step: number, s: FormState): string[] {
  const errs: string[] = [];
  if (step === 0) {
    if (!s.registered_name.trim()) errs.push('Registered name is required');
    if (!s.kind) errs.push('Entity type is required');
    if (s.member_count && Number.isNaN(Number(s.member_count))) errs.push('Member count must be a number');
    if (s.employee_count && Number.isNaN(Number(s.employee_count))) errs.push('Employee count must be a number');
  }
  if (step === 1) {
    const filled = s.officials.filter((o) => o.full_name.trim() || o.id_doc_number.trim());
    if (filled.length === 0) errs.push('Add at least one official');
    for (const o of filled) {
      if (!o.full_name.trim()) { errs.push('Every official needs a full name'); break; }
      if (!o.id_doc_number.trim()) { errs.push('Every official needs an ID / passport number'); break; }
      if (o.is_beneficial_owner && (o.ownership_percent == null || o.ownership_percent < 0 || o.ownership_percent > 100)) {
        errs.push('Beneficial owners need an ownership % between 0 and 100');
        break;
      }
    }
  }
  return errs;
}

// ─────────── Stepper ───────────

function Stepper({ current, onJump }: { current: number; onJump: (i: number) => void }) {
  return (
    <div className="row" style={{ gap: 0 }}>
      {STEPS.map((label, i) => {
        const done = i < current;
        const active = i === current;
        return (
          <Fragment key={i}>
            <button
              type="button"
              onClick={() => onJump(i)}
              disabled={i > current}
              style={{
                display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0,
                background: 'transparent', border: 0, padding: 0,
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
                {label}
              </span>
            </button>
            {i < STEPS.length - 1 && <div style={{ flex: 1, height: 1, background: 'var(--border)', margin: '0 12px' }} />}
          </Fragment>
        );
      })}
    </div>
  );
}

// ─────────── Steps ───────────

function StepProfile({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <>
      <div className="h-sec">Entity type</div>
      <div className="grid-3" style={{ marginBottom: 14 }}>
        {KIND_OPTIONS.map((k) => {
          const on = s.kind === k.v;
          return (
            <button
              key={k.v}
              type="button"
              className="card"
              style={{
                textAlign: 'left', cursor: 'pointer', padding: 0,
                borderColor: on ? 'var(--accent)' : 'var(--border)',
                background: on ? 'var(--accent-bg)' : 'var(--surface)',
              }}
              onClick={() => update('kind', k.v)}
            >
              <div className="card-hd">
                <h3>{k.label}</h3>
                {on && <span className="card-sub" style={{ color: 'var(--accent-fg)' }}>selected</span>}
              </div>
              <div className="card-body" style={{ paddingTop: 0 }}>
                <p className="tiny muted" style={{ margin: 0 }}>{k.hint}</p>
              </div>
            </button>
          );
        })}
      </div>

      <div className="h-sec">Identity</div>
      <div className="grid-3">
        <Field label="Registered name" required>
          <input className="input" value={s.registered_name} onChange={(e) => update('registered_name', e.target.value)} />
        </Field>
        <Field label="Trading name (if different)">
          <input className="input" value={s.trading_name} onChange={(e) => update('trading_name', e.target.value)} />
        </Field>
        <Field label="Registration number">
          <input className="input mono" value={s.registration_no} onChange={(e) => update('registration_no', e.target.value)} placeholder="CR/CHM/COOP …" />
        </Field>
        <Field label="Date of registration">
          <input className="input mono" type="date" value={s.date_of_registration} onChange={(e) => update('date_of_registration', e.target.value)} />
        </Field>
        <Field label="Date of operation">
          <input className="input mono" type="date" value={s.date_of_operation} onChange={(e) => update('date_of_operation', e.target.value)} />
        </Field>
        <Field label="Industry">
          <input className="input" value={s.industry} onChange={(e) => update('industry', e.target.value)} placeholder="financial_services, education, agriculture…" />
        </Field>
        <Field label="Nature of business" wide>
          <input className="input" value={s.nature_of_business} onChange={(e) => update('nature_of_business', e.target.value)} placeholder="What does the organisation do?" />
        </Field>
        <Field label="Members (count)">
          <input className="input mono" type="number" min={0} value={s.member_count} onChange={(e) => update('member_count', e.target.value)} />
        </Field>
        <Field label="Employees (count)">
          <input className="input mono" type="number" min={0} value={s.employee_count} onChange={(e) => update('employee_count', e.target.value)} />
        </Field>
        <Field label="Risk category">
          <select className="select" value={s.risk_category} onChange={(e) => update('risk_category', e.target.value as RiskCategory)}>
            <option value="low">Low</option>
            <option value="medium">Medium</option>
            <option value="high">High</option>
          </select>
        </Field>
      </div>

      <div className="divider" />
      <div className="h-sec">Location</div>
      <div className="grid-3">
        <Field label="County">
          <input className="input" value={s.county} onChange={(e) => update('county', e.target.value)} />
        </Field>
        <Field label="Sub-county">
          <input className="input" value={s.sub_county} onChange={(e) => update('sub_county', e.target.value)} />
        </Field>
        <Field label="Ward">
          <input className="input" value={s.ward} onChange={(e) => update('ward', e.target.value)} />
        </Field>
        <Field label="Physical address" wide>
          <input className="input" value={s.physical_address} onChange={(e) => update('physical_address', e.target.value)} placeholder="Building, street, area" />
        </Field>
        <Field label="Postal address" wide>
          <input className="input" value={s.postal_address} onChange={(e) => update('postal_address', e.target.value)} placeholder="P.O. Box …" />
        </Field>
      </div>
    </>
  );
}

function StepOfficials({ s, setS }: { s: FormState; setS: (fn: (p: FormState) => FormState) => void }) {
  function setO(i: number, patch: Partial<OfficialRow>) {
    setS((p) => {
      const next = p.officials.slice();
      next[i] = { ...next[i], ...patch };
      return { ...p, officials: next };
    });
  }
  function addO() {
    setS((p) => ({ ...p, officials: [...p.officials, { ...EMPTY_OFFICIAL }] }));
  }
  function removeO(i: number) {
    setS((p) => ({ ...p, officials: p.officials.filter((_, idx) => idx !== i) }));
  }

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Capture every signatory + director + office-bearer. Personal KYC mirrors what we collect for individual members. Mark beneficial owners and the % they hold; flag PEPs for compliance review.
      </p>

      {s.officials.map((o, i) => (
        <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 10 }}>
          <div className="card-hd">
            <h3>{positionLabel(o.position)} {o.full_name && <span className="muted tiny">· {o.full_name}</span>}</h3>
            <div className="card-hd-actions">
              {o.is_pep && <span className="badge badge-warn">PEP</span>}
              {o.is_beneficial_owner && <span className="badge badge-accent">beneficial owner</span>}
              {s.officials.length > 1 && (
                <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeO(i)}>
                  <Icon name="trash" size={12} />
                </button>
              )}
            </div>
          </div>
          <div className="card-body">
            <div className="grid-3">
              <Field label="Full name" required>
                <input className="input" value={o.full_name} onChange={(e) => setO(i, { full_name: e.target.value })} />
              </Field>
              <Field label="Position">
                <select className="select" value={o.position ?? 'director'} onChange={(e) => setO(i, { position: e.target.value as OfficialPosition })}>
                  <option value="chairperson">Chairperson</option>
                  <option value="vice_chairperson">Vice Chairperson</option>
                  <option value="treasurer">Treasurer</option>
                  <option value="secretary">Secretary</option>
                  <option value="director">Director</option>
                  <option value="trustee">Trustee</option>
                  <option value="principal">Principal</option>
                  <option value="pastor">Pastor</option>
                  <option value="other">Other</option>
                </select>
              </Field>
              <Field label="Appointed on">
                <input className="input mono" type="date" value={o.appointed_on ?? ''} onChange={(e) => setO(i, { appointed_on: e.target.value })} />
              </Field>
              <Field label="ID type">
                <select className="select" value={o.id_doc_kind ?? 'national_id'} onChange={(e) => setO(i, { id_doc_kind: e.target.value as IDDocKind })}>
                  <option value="national_id">National ID</option>
                  <option value="passport">Passport</option>
                  <option value="alien_id">Alien ID</option>
                </select>
              </Field>
              <Field label="ID / Passport #" required>
                <input className="input mono" value={o.id_doc_number} onChange={(e) => setO(i, { id_doc_number: e.target.value.trim() })} />
              </Field>
              <Field label="KRA PIN">
                <input className="input mono" maxLength={11} value={o.kra_pin ?? ''} onChange={(e) => setO(i, { kra_pin: e.target.value.toUpperCase() })} placeholder="A123456789B" />
              </Field>
              <Field label="Phone">
                <input className="input mono" value={o.phone ?? ''} onChange={(e) => setO(i, { phone: e.target.value })} />
              </Field>
              <Field label="Email">
                <input className="input" type="email" value={o.email ?? ''} onChange={(e) => setO(i, { email: e.target.value })} />
              </Field>
              <Field label="Date of birth">
                <input className="input mono" type="date" value={o.date_of_birth ?? ''} onChange={(e) => setO(i, { date_of_birth: e.target.value })} />
              </Field>
              <Field label="Gender">
                <select className="select" value={o.gender ?? 'undisclosed'} onChange={(e) => setO(i, { gender: e.target.value as Gender })}>
                  <option value="undisclosed">Undisclosed</option>
                  <option value="female">Female</option>
                  <option value="male">Male</option>
                  <option value="other">Other</option>
                </select>
              </Field>
              <Field label="Nationality">
                <input className="input" value={o.nationality ?? ''} onChange={(e) => setO(i, { nationality: e.target.value })} />
              </Field>
              <Field label="Occupation">
                <input className="input" value={o.occupation ?? ''} onChange={(e) => setO(i, { occupation: e.target.value })} />
              </Field>
              <Field label="Address" wide>
                <input className="input" value={o.physical_address ?? ''} onChange={(e) => setO(i, { physical_address: e.target.value })} />
              </Field>
            </div>

            <div className="divider" />
            <div className="h-sec">Compliance flags</div>
            <div className="grid-3">
              <Field label="Politically Exposed Person">
                <label className="row" style={{ gap: 6 }}>
                  <input type="checkbox" checked={!!o.is_pep} onChange={(e) => setO(i, { is_pep: e.target.checked })} style={{ accentColor: 'var(--warn)' }} />
                  <span className="tiny">Flag as PEP</span>
                </label>
                {o.is_pep && (
                  <input className="input" style={{ marginTop: 6 }} value={o.pep_note ?? ''} onChange={(e) => setO(i, { pep_note: e.target.value })} placeholder="PEP details / source" />
                )}
              </Field>
              <Field label="Beneficial owner">
                <label className="row" style={{ gap: 6 }}>
                  <input type="checkbox" checked={!!o.is_beneficial_owner} onChange={(e) => setO(i, { is_beneficial_owner: e.target.checked })} style={{ accentColor: 'var(--accent)' }} />
                  <span className="tiny">≥ 25% ownership / effective control</span>
                </label>
              </Field>
              <Field label="Ownership %">
                <input
                  className="input mono"
                  type="number" step="0.01" min={0} max={100}
                  value={o.ownership_percent ?? ''}
                  onChange={(e) => setO(i, { ownership_percent: e.target.value === '' ? undefined : Number(e.target.value) })}
                  disabled={!o.is_beneficial_owner}
                />
              </Field>
            </div>
          </div>
        </div>
      ))}

      <button type="button" className="btn btn-sm" onClick={addO}>
        <Icon name="plus" size={12} /> Add another official
      </button>
    </>
  );
}

function StepSignatories({ s, setS, update }: {
  s: FormState; setS: (fn: (p: FormState) => FormState) => void;
  update: <K extends keyof FormState>(k: K, v: FormState[K]) => void;
}) {
  function setSig(i: number, patch: { class?: SignatoryClass; signing_order?: number; txn_limit?: number | null }) {
    setS((p) => {
      const next = p.officials.slice();
      const current = next[i].signatory ?? { class: 'mandatory', signing_order: i + 1 };
      next[i] = { ...next[i], signatory: { ...current, ...patch } };
      return { ...p, officials: next };
    });
  }
  function toggleSignatory(i: number, on: boolean) {
    setS((p) => {
      const next = p.officials.slice();
      next[i] = { ...next[i], signatory: on ? { class: 'mandatory', signing_order: i + 1 } : undefined };
      return { ...p, officials: next };
    });
  }

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Mark which officials sign on behalf of the organisation. Set their class, signing order, and per-signatory transaction caps. The mandate rule below applies to the org as a whole.
      </p>

      <div className="h-sec">Signatories</div>
      {s.officials.map((o, i) => {
        const sig = o.signatory;
        const hasSig = !!sig;
        return (
          <div key={i} className="kyc-row">
            <input
              type="checkbox"
              checked={hasSig}
              onChange={(e) => toggleSignatory(i, e.target.checked)}
              style={{ accentColor: 'var(--accent)' }}
            />
            <div className="kyc-row-label">
              <div><strong>{o.full_name || '(unnamed)'}</strong> · {positionLabel(o.position)}</div>
              {hasSig && (
                <div className="row" style={{ gap: 8, marginTop: 6 }}>
                  <select
                    className="select"
                    style={{ height: 26, fontSize: 12 }}
                    value={sig.class ?? 'mandatory'}
                    onChange={(e) => setSig(i, { class: e.target.value as SignatoryClass })}
                  >
                    <option value="mandatory">Mandatory</option>
                    <option value="optional">Optional</option>
                    <option value="alternate">Alternate</option>
                  </select>
                  <input
                    className="input mono"
                    style={{ height: 26, width: 80, fontSize: 12 }}
                    type="number" min={1} step={1}
                    value={sig.signing_order ?? i + 1}
                    onChange={(e) => setSig(i, { signing_order: Number(e.target.value) })}
                    title="Signing order"
                  />
                  <span className="tiny muted">Txn cap (KES, optional)</span>
                  <input
                    className="input mono"
                    style={{ height: 26, width: 120, fontSize: 12 }}
                    type="number" min={0} step={1000}
                    value={sig.txn_limit ?? ''}
                    onChange={(e) => setSig(i, { txn_limit: e.target.value === '' ? null : Number(e.target.value) })}
                  />
                </div>
              )}
            </div>
          </div>
        );
      })}

      <div className="divider" />
      <div className="h-sec">Mandate rule</div>
      <p className="muted tiny" style={{ marginTop: -4, marginBottom: 10 }}>
        This rule governs which signatures clear a transaction. A finer engine lands with the loans module.
      </p>
      <div className="grid-3">
        <Field label="Default rule">
          <select className="select" value={s.mandate_default} onChange={(e) => update('mandate_default', e.target.value as FormState['mandate_default'])}>
            <option value="any_one">Any one signatory</option>
            <option value="any_two">Any two signatories</option>
            <option value="all">All mandatory signatories</option>
            <option value="custom">Custom (rules array)</option>
          </select>
        </Field>
        <Field label="Above amount (KES)" hint="Override the default for high-value txns.">
          <input
            className="input mono" type="number" min={0} step={1000}
            value={s.mandate_rule_amount}
            onChange={(e) => update('mandate_rule_amount', e.target.value)}
          />
        </Field>
        <Field label="Required positions (CSV)" hint="e.g. chairperson,treasurer">
          <input
            className="input mono"
            value={s.mandate_rule_required}
            onChange={(e) => update('mandate_rule_required', e.target.value)}
          />
        </Field>
      </div>
    </>
  );
}

function StepBanking({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <>
      <div className="h-sec">Bank account</div>
      <div className="grid-3">
        <Field label="Bank name">
          <input className="input" value={s.bank_name} onChange={(e) => update('bank_name', e.target.value)} />
        </Field>
        <Field label="Branch">
          <input className="input" value={s.bank_branch} onChange={(e) => update('bank_branch', e.target.value)} />
        </Field>
        <Field label="Bank code">
          <input className="input mono" value={s.bank_code} onChange={(e) => update('bank_code', e.target.value)} />
        </Field>
        <Field label="Account name">
          <input className="input" value={s.account_name} onChange={(e) => update('account_name', e.target.value)} />
        </Field>
        <Field label="Account number">
          <input className="input mono" value={s.account_number} onChange={(e) => update('account_number', e.target.value)} />
        </Field>
        <Field label="SWIFT / BIC">
          <input className="input mono" value={s.swift_code} onChange={(e) => update('swift_code', e.target.value)} />
        </Field>
      </div>

      <div className="divider" />
      <div className="h-sec">Mobile money</div>
      <div className="grid-3">
        <Field label="Paybill">
          <input className="input mono" value={s.paybill} onChange={(e) => update('paybill', e.target.value)} />
        </Field>
        <Field label="Till number">
          <input className="input mono" value={s.till_number} onChange={(e) => update('till_number', e.target.value)} />
        </Field>
        <Field label="Linked phones (CSV)">
          <input className="input mono" value={s.mobile_money_phones} onChange={(e) => update('mobile_money_phones', e.target.value)} placeholder="+254700…,+254711…" />
        </Field>
        <Field label="Settlement account" hint="Where mobile money sweeps to." wide>
          <input className="input" value={s.mobile_settlement_account} onChange={(e) => update('mobile_settlement_account', e.target.value)} placeholder="bank, till, or account #" />
        </Field>
      </div>

      <div className="divider" />
      <div className="h-sec">Disbursement &amp; repayment</div>
      <div className="grid-2">
        <Field label="Preferred disbursement method">
          <select className="select" value={s.preferred_disbursement} onChange={(e) => update('preferred_disbursement', e.target.value)}>
            <option value="">—</option>
            <option value="bank">Bank transfer</option>
            <option value="mobile">Mobile money</option>
            <option value="cheque">Cheque</option>
          </select>
        </Field>
        <Field label="Preferred repayment method">
          <select className="select" value={s.preferred_repayment} onChange={(e) => update('preferred_repayment', e.target.value)}>
            <option value="">—</option>
            <option value="standing_order">Standing order</option>
            <option value="checkoff">Check-off</option>
            <option value="mpesa">M-Pesa / mobile</option>
            <option value="cash">Cash</option>
          </select>
        </Field>
        <Field label="Standing order details" wide>
          <input className="input" value={s.standing_order_details} onChange={(e) => update('standing_order_details', e.target.value)} placeholder="Frequency, amount, account…" />
        </Field>
        <Field label="Check-off arrangement" wide>
          <input className="input" value={s.checkoff_arrangement} onChange={(e) => update('checkoff_arrangement', e.target.value)} placeholder="Employer / payroll reference…" />
        </Field>
      </div>
    </>
  );
}

function StepContacts({ s, setS }: { s: FormState; setS: (fn: (p: FormState) => FormState) => void }) {
  function setC(i: number, patch: Partial<ContactRow>) {
    setS((p) => {
      const next = p.contacts.slice();
      next[i] = { ...next[i], ...patch };
      return { ...p, contacts: next };
    });
  }
  function addC(kind: ContactKind) {
    setS((p) => ({ ...p, contacts: [...p.contacts, { kind, full_name: '', role: '', phone: '', email: '' }] }));
  }
  function removeC(i: number) {
    setS((p) => ({ ...p, contacts: p.contacts.filter((_, idx) => idx !== i) }));
  }
  const has = (k: ContactKind) => s.contacts.some((c) => c.kind === k);

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0 }}>
        One contact per role. Add the contacts your staff will actually call.
      </p>

      {s.contacts.map((c, i) => (
        <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 10 }}>
          <div className="card-hd">
            <h3>{contactLabel(c.kind)}</h3>
            <div className="card-hd-actions">
              <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeC(i)}>
                <Icon name="trash" size={12} />
              </button>
            </div>
          </div>
          <div className="card-body">
            <div className="grid-3">
              <Field label="Full name">
                <input className="input" value={c.full_name} onChange={(e) => setC(i, { full_name: e.target.value })} />
              </Field>
              <Field label="Role / title">
                <input className="input" value={c.role} onChange={(e) => setC(i, { role: e.target.value })} placeholder="Chair, Treasurer, Head of Finance…" />
              </Field>
              <Field label="Phone">
                <input className="input mono" value={c.phone} onChange={(e) => setC(i, { phone: e.target.value })} />
              </Field>
              <Field label="Email" wide>
                <input className="input" type="email" value={c.email} onChange={(e) => setC(i, { email: e.target.value })} />
              </Field>
            </div>
          </div>
        </div>
      ))}

      <div className="row" style={{ gap: 6 }}>
        {(['primary', 'finance', 'hr_payroll', 'compliance'] as ContactKind[]).map((k) => (
          <button
            key={k}
            type="button"
            className="btn btn-sm"
            disabled={has(k)}
            title={has(k) ? 'One per role — remove the existing first' : ''}
            onClick={() => addC(k)}
          >
            <Icon name="plus" size={12} /> {contactLabel(k)}
          </button>
        ))}
      </div>
    </>
  );
}

function StepReview({ s }: { s: FormState }) {
  const officials = s.officials.filter((o) => o.full_name.trim());
  const signatories = officials.filter((o) => o.signatory);
  const contacts = s.contacts.filter((c) => c.full_name.trim());
  return (
    <>
      <ReviewSection title="Profile">
        <Row k="Name" v={s.registered_name + (s.trading_name && s.trading_name !== s.registered_name ? ` (t/a ${s.trading_name})` : '')} />
        <Row k="Type" v={KIND_OPTIONS.find((k) => k.v === s.kind)?.label ?? s.kind} />
        <Row k="Registration #" v={s.registration_no || '—'} mono />
        <Row k="Industry" v={s.industry || '—'} />
        <Row k="Members / Employees" v={`${s.member_count || '—'} / ${s.employee_count || '—'}`} mono />
        <Row k="Risk" v={s.risk_category} />
      </ReviewSection>

      <ReviewSection title={`Officials (${officials.length})`}>
        {officials.length === 0 ? (
          <Row k="" v={<span className="muted">No officials.</span>} />
        ) : officials.map((o, i) => (
          <Row
            key={i}
            k={<><strong>{o.full_name}</strong> · {positionLabel(o.position)}</>}
            v={<span className="muted">{o.id_doc_number}{o.is_pep ? ' · PEP' : ''}{o.is_beneficial_owner ? ` · BO ${o.ownership_percent ?? '?'}%` : ''}</span>}
          />
        ))}
      </ReviewSection>

      <ReviewSection title={`Signatories (${signatories.length})`}>
        {signatories.length === 0 ? (
          <Row k="" v={<span className="muted">No signatories.</span>} />
        ) : signatories.map((o, i) => (
          <Row
            key={i}
            k={<>{o.full_name}</>}
            v={<><span className="mono">order {o.signatory!.signing_order ?? i + 1}</span> · {o.signatory!.class}{o.signatory!.txn_limit ? ` · cap KES ${o.signatory!.txn_limit.toLocaleString()}` : ''}</>}
          />
        ))}
        <Row k="Mandate" v={`${s.mandate_default} · override above KES ${Number(s.mandate_rule_amount).toLocaleString()} → ${s.mandate_rule_required}`} />
      </ReviewSection>

      <ReviewSection title="Banking">
        <Row k="Bank" v={s.bank_name ? `${s.bank_name}${s.bank_branch ? ' — ' + s.bank_branch : ''}` : '—'} />
        <Row k="Account" v={s.account_number ? `${s.account_name} / ${s.account_number}` : '—'} mono />
        <Row k="Mobile" v={s.paybill || s.till_number ? `${s.paybill ? 'Paybill ' + s.paybill : ''}${s.till_number ? '  Till ' + s.till_number : ''}` : '—'} mono />
        <Row k="Repayment" v={s.preferred_repayment || '—'} />
      </ReviewSection>

      <ReviewSection title={`Contacts (${contacts.length})`}>
        {contacts.length === 0 ? (
          <Row k="" v={<span className="muted">No contacts.</span>} />
        ) : contacts.map((c, i) => (
          <Row key={i} k={contactLabel(c.kind)} v={<>{c.full_name}{c.role ? ` · ${c.role}` : ''}{c.phone ? ` · ${c.phone}` : ''}</>} />
        ))}
      </ReviewSection>

      <p className="muted tiny" style={{ marginTop: 12 }}>
        Submitting creates the organisation in <strong>pending</strong> status. You'll be sent to the profile to upload registration certificate, CR12, KRA PIN, M&amp;A, etc. KYC moves to <strong>in review</strong> automatically when an approver acts.
      </p>
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
      <div className="card-hd"><h3>{title}</h3></div>
      <div className="card-body"><dl className="kvs">{children}</dl></div>
    </div>
  );
}

function Row({ k, v, mono }: { k: ReactNode; v: ReactNode; mono?: boolean }) {
  return (<><dt>{k}</dt><dd className={mono ? 'mono' : ''}>{v}</dd></>);
}

function positionLabel(p?: OfficialPosition): string {
  if (!p) return 'Director';
  return p.replace('_', ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

function contactLabel(k: ContactKind): string {
  return ({
    primary: 'Primary contact',
    finance: 'Finance contact',
    hr_payroll: 'HR / Payroll contact',
    compliance: 'Compliance contact',
  } as Record<ContactKind, string>)[k];
}
