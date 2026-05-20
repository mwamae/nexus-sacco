// Individual member onboarding wizard. Six steps, modeled after the
// mzizi prototype's NewMemberForm. Submit creates the member then
// uploads the signature + passport photo in sequence.

import { Fragment, useRef, useState, type FormEvent, type ReactNode } from 'react';
import {
  createMember,
  uploadMemberDocument,
  extractError,
  type CreateMemberInput,
  type Gender,
  type IDDocKind,
} from '../api/client';
import { Icon } from '../components/Icon';
import { SignaturePad, type SignaturePadHandle } from '../components/SignaturePad';

const STEPS = [
  'Identity',
  'Contact & address',
  'Employment',
  'Next of kin & beneficiaries',
  'Documents',
  'Review',
] as const;

type FormState = {
  full_name: string;
  id_doc_kind: IDDocKind;
  id_doc_number: string;
  kra_pin: string;
  gender: Gender;
  date_of_birth: string;

  phone: string;
  email: string;
  county: string;
  sub_county: string;
  physical_address: string;

  employment_status: string;
  employer: string;
  payroll_no: string;
  job_title: string;

  next_of_kin_full_name: string;
  next_of_kin_relationship: string;
  next_of_kin_phone: string;
  next_of_kin_email: string;
  next_of_kin_id: string;

  beneficiaries: {
    full_name: string;
    relationship: string;
    phone: string;
    id_doc_number: string;
    share_percent: string;
  }[];
};

const INITIAL: FormState = {
  full_name: '',
  id_doc_kind: 'national_id',
  id_doc_number: '',
  kra_pin: '',
  gender: 'undisclosed',
  date_of_birth: '',
  phone: '',
  email: '',
  county: '',
  sub_county: '',
  physical_address: '',
  employment_status: '',
  employer: '',
  payroll_no: '',
  job_title: '',
  next_of_kin_full_name: '',
  next_of_kin_relationship: '',
  next_of_kin_phone: '',
  next_of_kin_email: '',
  next_of_kin_id: '',
  beneficiaries: [{ full_name: '', relationship: '', phone: '', id_doc_number: '', share_percent: '100' }],
};

export default function MemberOnboarding() {
  const [step, setStep] = useState(0);
  const [s, setS] = useState<FormState>(INITIAL);
  const [signatureFile, setSignatureFile] = useState<Blob | null>(null);
  const [photoFile, setPhotoFile] = useState<Blob | null>(null);
  const [photoPreview, setPhotoPreview] = useState<string | null>(null);
  const sigRef = useRef<SignaturePadHandle | null>(null);

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
      // 1. Gather a signature blob now (if drawn) so we can warn early.
      let sigBlob: Blob | null = signatureFile;
      if (!sigBlob && sigRef.current && !sigRef.current.isEmpty()) {
        sigBlob = await sigRef.current.getBlob();
      }

      // 2. Build payload and create.
      const input: CreateMemberInput = {
        full_name: s.full_name.trim(),
        id_doc_kind: s.id_doc_kind,
        id_doc_number: s.id_doc_number.trim(),
        kra_pin: s.kra_pin.trim() || undefined,
        gender: s.gender,
        date_of_birth: s.date_of_birth || undefined,
        phone: s.phone.trim() || undefined,
        email: s.email.trim() || undefined,
        county: s.county.trim() || undefined,
        sub_county: s.sub_county.trim() || undefined,
        physical_address: s.physical_address.trim() || undefined,
        employment_status: s.employment_status.trim() || undefined,
        employer: s.employer.trim() || undefined,
        payroll_no: s.payroll_no.trim() || undefined,
        job_title: s.job_title.trim() || undefined,
        next_of_kin: s.next_of_kin_full_name.trim()
          ? {
              full_name: s.next_of_kin_full_name.trim(),
              relationship: s.next_of_kin_relationship.trim(),
              phone: s.next_of_kin_phone.trim() || undefined,
              email: s.next_of_kin_email.trim() || undefined,
              id_doc_number: s.next_of_kin_id.trim() || undefined,
            }
          : null,
        beneficiaries: s.beneficiaries
          .filter((b) => b.full_name.trim() !== '')
          .map((b) => ({
            full_name: b.full_name.trim(),
            relationship: b.relationship.trim(),
            phone: b.phone.trim() || undefined,
            id_doc_number: b.id_doc_number.trim() || undefined,
            share_percent: b.share_percent ? Number(b.share_percent) : undefined,
          })),
      };
      const m = await createMember(input);

      // 3. Upload documents (best-effort; failures surface but the member
      //    is already created — they can be re-uploaded from the profile).
      if (sigBlob) {
        try {
          await uploadMemberDocument(m.id, 'signature', sigBlob, 'signature.png');
        } catch (e) {
          console.warn('signature upload failed', e);
        }
      }
      if (photoFile) {
        try {
          await uploadMemberDocument(m.id, 'passport_photo', photoFile);
        } catch (e) {
          console.warn('photo upload failed', e);
        }
      }

      // 4. Done — back to register.
      window.location.assign('/members');
    } catch (e) {
      setError(extractError(e, 'Could not create member'));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Members · Onboarding</div>
          <h1>New member</h1>
          <div className="page-sub">Six steps · all fields can be edited later from the member profile.</div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href="/members"><Icon name="x" size={12} /> Discard</a>
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
          {step === 0 && <StepIdentity s={s} update={update} />}
          {step === 1 && <StepContact s={s} update={update} />}
          {step === 2 && <StepEmployment s={s} update={update} />}
          {step === 3 && <StepPeople s={s} setS={setS} />}
          {step === 4 && (
            <StepDocuments
              sigRef={sigRef}
              signatureFile={signatureFile}
              setSignatureFile={setSignatureFile}
              photoFile={photoFile}
              setPhotoFile={setPhotoFile}
              photoPreview={photoPreview}
              setPhotoPreview={setPhotoPreview}
            />
          )}
          {step === 5 && <StepReview s={s} signatureFile={signatureFile} sigRef={sigRef} photoFile={photoFile} />}
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
              disabled={submitting}
              onClick={() => void onSubmit()}
            >
              <Icon name="check" size={12} />
              {submitting ? 'Submitting…' : 'Create member (pending approval)'}
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
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                flexShrink: 0,
                background: 'transparent',
                border: 0,
                padding: 0,
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
    if (!s.full_name.trim()) errs.push('Full name is required');
    if (!s.id_doc_number.trim()) errs.push('National ID / Passport number is required');
    if (s.kra_pin && !/^[A-Za-z][0-9]{9}[A-Za-z]$/.test(s.kra_pin.trim())) {
      errs.push('KRA PIN must look like A123456789Z (one letter, 9 digits, one letter)');
    }
    if (s.date_of_birth && new Date(s.date_of_birth) > new Date()) {
      errs.push('Date of birth cannot be in the future');
    }
  }
  if (step === 3) {
    const present = s.beneficiaries.filter((b) => b.full_name.trim() !== '');
    if (present.length > 0) {
      const sum = present.reduce((acc, b) => acc + (Number(b.share_percent) || 0), 0);
      if (Math.abs(sum - 100) > 0.01) {
        errs.push(`Beneficiary shares must sum to 100% (currently ${sum})`);
      }
    }
  }
  return errs;
}

// ─────────── Step bodies ───────────

function StepIdentity({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <div className="grid-3">
      <Field label="Full name" required>
        <input className="input" value={s.full_name} onChange={(e) => update('full_name', e.target.value)} placeholder="Jane Wanjiru Mwangi" />
      </Field>
      <Field label="ID type">
        <select className="select" value={s.id_doc_kind} onChange={(e) => update('id_doc_kind', e.target.value as IDDocKind)}>
          <option value="national_id">National ID</option>
          <option value="passport">Passport</option>
          <option value="alien_id">Alien ID</option>
        </select>
      </Field>
      <Field label="ID / Passport number" required>
        <input className="input mono" value={s.id_doc_number} onChange={(e) => update('id_doc_number', e.target.value.trim())} placeholder="33445566" />
      </Field>
      <Field label="KRA PIN" hint="Optional. Format like A123456789Z.">
        <input className="input mono" value={s.kra_pin} onChange={(e) => update('kra_pin', e.target.value.toUpperCase())} placeholder="A123456789Z" maxLength={11} />
      </Field>
      <Field label="Gender">
        <select className="select" value={s.gender} onChange={(e) => update('gender', e.target.value as Gender)}>
          <option value="undisclosed">Undisclosed</option>
          <option value="female">Female</option>
          <option value="male">Male</option>
          <option value="other">Other</option>
        </select>
      </Field>
      <Field label="Date of birth">
        <input className="input mono" type="date" value={s.date_of_birth} onChange={(e) => update('date_of_birth', e.target.value)} />
      </Field>
    </div>
  );
}

function StepContact({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <div className="grid-3">
      <Field label="Phone">
        <input className="input mono" value={s.phone} onChange={(e) => update('phone', e.target.value)} placeholder="+254 712 345 678" />
      </Field>
      <Field label="Email">
        <input className="input" type="email" value={s.email} onChange={(e) => update('email', e.target.value)} placeholder="jane@example.com" />
      </Field>
      <div />
      <Field label="County">
        <input className="input" value={s.county} onChange={(e) => update('county', e.target.value)} placeholder="Nakuru" />
      </Field>
      <Field label="Sub-county">
        <input className="input" value={s.sub_county} onChange={(e) => update('sub_county', e.target.value)} placeholder="Nakuru East" />
      </Field>
      <Field label="Physical address" wide>
        <input className="input" value={s.physical_address} onChange={(e) => update('physical_address', e.target.value)} placeholder="Lanet estate, Nakuru" />
      </Field>
    </div>
  );
}

function StepEmployment({ s, update }: { s: FormState; update: <K extends keyof FormState>(k: K, v: FormState[K]) => void }) {
  return (
    <div className="grid-3">
      <Field label="Employment status">
        <select className="select" value={s.employment_status} onChange={(e) => update('employment_status', e.target.value)}>
          <option value="">—</option>
          <option value="employed">Employed</option>
          <option value="self-employed">Self-employed</option>
          <option value="unemployed">Unemployed</option>
          <option value="retired">Retired</option>
          <option value="student">Student</option>
        </select>
      </Field>
      <Field label="Job title">
        <input className="input" value={s.job_title} onChange={(e) => update('job_title', e.target.value)} placeholder="Account Manager" />
      </Field>
      <div />
      <Field label="Employer">
        <input className="input" value={s.employer} onChange={(e) => update('employer', e.target.value)} placeholder="Sample Co Ltd" />
      </Field>
      <Field label="Payroll / staff #">
        <input className="input mono" value={s.payroll_no} onChange={(e) => update('payroll_no', e.target.value)} placeholder="EMP-1234" />
      </Field>
    </div>
  );
}

function StepPeople({ s, setS }: { s: FormState; setS: (fn: (p: FormState) => FormState) => void }) {
  function setBen(i: number, key: keyof FormState['beneficiaries'][0], v: string) {
    setS((p) => {
      const next = p.beneficiaries.slice();
      next[i] = { ...next[i], [key]: v };
      return { ...p, beneficiaries: next };
    });
  }
  function addBen() {
    setS((p) => ({ ...p, beneficiaries: [...p.beneficiaries, { full_name: '', relationship: '', phone: '', id_doc_number: '', share_percent: '' }] }));
  }
  function removeBen(i: number) {
    setS((p) => ({ ...p, beneficiaries: p.beneficiaries.filter((_, idx) => idx !== i) }));
  }

  return (
    <>
      <div className="h-sec">Next of kin</div>
      <p className="muted tiny" style={{ marginTop: -4, marginBottom: 8 }}>Single emergency contact. Optional but recommended.</p>
      <div className="grid-3">
        <Field label="Full name">
          <input className="input" value={s.next_of_kin_full_name} onChange={(e) => setS((p) => ({ ...p, next_of_kin_full_name: e.target.value }))} />
        </Field>
        <Field label="Relationship">
          <input className="input" value={s.next_of_kin_relationship} onChange={(e) => setS((p) => ({ ...p, next_of_kin_relationship: e.target.value }))} placeholder="spouse, parent, sibling…" />
        </Field>
        <Field label="Phone">
          <input className="input mono" value={s.next_of_kin_phone} onChange={(e) => setS((p) => ({ ...p, next_of_kin_phone: e.target.value }))} />
        </Field>
        <Field label="National ID #">
          <input className="input mono" value={s.next_of_kin_id} onChange={(e) => setS((p) => ({ ...p, next_of_kin_id: e.target.value }))} />
        </Field>
        <Field label="Email">
          <input className="input" type="email" value={s.next_of_kin_email} onChange={(e) => setS((p) => ({ ...p, next_of_kin_email: e.target.value }))} />
        </Field>
      </div>

      <div className="divider" />

      <div className="row" style={{ marginBottom: 8 }}>
        <div>
          <div className="h-sec" style={{ marginBottom: 2 }}>Beneficiaries</div>
          <p className="muted tiny" style={{ margin: 0 }}>Share percentages must sum to 100. Leave blank to skip.</p>
        </div>
        <span className="spacer" />
        <button type="button" className="btn btn-sm" onClick={addBen}>
          <Icon name="plus" size={12} /> Add beneficiary
        </button>
      </div>

      {s.beneficiaries.map((b, i) => (
        <div key={i} className="card" style={{ background: 'var(--surface-2)', marginBottom: 8 }}>
          <div className="card-hd">
            <h3>Beneficiary {i + 1}</h3>
            <span className="card-sub">{b.share_percent ? `${b.share_percent}%` : 'unset'}</span>
            <div className="card-hd-actions">
              {s.beneficiaries.length > 1 && (
                <button type="button" className="btn btn-sm btn-ghost" style={{ color: 'var(--neg)' }} onClick={() => removeBen(i)} aria-label="Remove">
                  <Icon name="trash" size={12} />
                </button>
              )}
            </div>
          </div>
          <div className="card-body">
            <div className="grid-3">
              <Field label="Full name">
                <input className="input" value={b.full_name} onChange={(e) => setBen(i, 'full_name', e.target.value)} />
              </Field>
              <Field label="Relationship">
                <input className="input" value={b.relationship} onChange={(e) => setBen(i, 'relationship', e.target.value)} placeholder="spouse, child…" />
              </Field>
              <Field label="Share %">
                <input className="input mono" inputMode="decimal" value={b.share_percent} onChange={(e) => setBen(i, 'share_percent', e.target.value)} />
              </Field>
              <Field label="Phone">
                <input className="input mono" value={b.phone} onChange={(e) => setBen(i, 'phone', e.target.value)} />
              </Field>
              <Field label="National ID #">
                <input className="input mono" value={b.id_doc_number} onChange={(e) => setBen(i, 'id_doc_number', e.target.value)} />
              </Field>
            </div>
          </div>
        </div>
      ))}
    </>
  );
}

function StepDocuments({
  sigRef,
  signatureFile,
  setSignatureFile,
  photoFile,
  setPhotoFile,
  photoPreview,
  setPhotoPreview,
}: {
  sigRef: React.RefObject<SignaturePadHandle | null>;
  signatureFile: Blob | null;
  setSignatureFile: (b: Blob | null) => void;
  photoFile: Blob | null;
  setPhotoFile: (b: Blob | null) => void;
  photoPreview: string | null;
  setPhotoPreview: (s: string | null) => void;
}) {
  function onPhoto(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    if (!/^image\/(png|jpe?g|webp)$/i.test(f.type)) {
      alert('Photo must be a PNG, JPEG or WebP image.');
      return;
    }
    if (f.size > 5 * 1024 * 1024) {
      alert('Photo must be under 5 MB.');
      return;
    }
    setPhotoFile(f);
    if (photoPreview) URL.revokeObjectURL(photoPreview);
    setPhotoPreview(URL.createObjectURL(f));
  }

  function onSigFile(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    if (!/^image\/(png|jpe?g|svg\+xml)$/i.test(f.type)) {
      alert('Signature must be PNG, JPEG or SVG.');
      return;
    }
    setSignatureFile(f);
  }

  return (
    <div className="grid-2">
      <div>
        <div className="h-sec">Signature</div>
        <p className="muted tiny" style={{ marginTop: -4, marginBottom: 8 }}>Draw with a mouse or touchscreen, or upload a scanned signature.</p>
        <SignaturePad ref={sigRef} />
        <div className="row" style={{ marginTop: 8, gap: 6 }}>
          <button type="button" className="btn btn-sm" onClick={() => sigRef.current?.clear()}>
            <Icon name="refresh" size={12} /> Clear
          </button>
          <span className="spacer" />
          <label className="btn btn-sm">
            <Icon name="arrow_up" size={12} /> Upload file
            <input type="file" accept="image/png,image/jpeg,image/svg+xml" onChange={onSigFile} style={{ display: 'none' }} />
          </label>
        </div>
        {signatureFile && (
          <div className="muted tiny" style={{ marginTop: 6 }}>
            File: {(signatureFile as File).name ?? 'signature'} · {(signatureFile.size / 1024).toFixed(1)} KB
          </div>
        )}
      </div>

      <div>
        <div className="h-sec">Passport photo</div>
        <p className="muted tiny" style={{ marginTop: -4, marginBottom: 8 }}>PNG / JPEG / WebP. Max 5 MB.</p>
        <div style={{
          width: 160, height: 200,
          border: '1px dashed var(--border)',
          borderRadius: 'var(--r-md)',
          display: 'grid', placeItems: 'center',
          background: 'var(--surface-2)',
          overflow: 'hidden',
        }}>
          {photoPreview ? (
            <img src={photoPreview} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
          ) : (
            <span className="muted tiny">No photo</span>
          )}
        </div>
        <div className="row" style={{ marginTop: 8, gap: 6 }}>
          <label className="btn btn-sm">
            <Icon name="arrow_up" size={12} /> {photoFile ? 'Replace' : 'Upload'}
            <input type="file" accept="image/png,image/jpeg,image/webp" onChange={onPhoto} style={{ display: 'none' }} />
          </label>
          {photoFile && (
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              style={{ color: 'var(--neg)' }}
              onClick={() => { setPhotoFile(null); if (photoPreview) URL.revokeObjectURL(photoPreview); setPhotoPreview(null); }}
            >
              <Icon name="trash" size={12} /> Remove
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function StepReview({ s, signatureFile, sigRef, photoFile }: { s: FormState; signatureFile: Blob | null; sigRef: React.RefObject<SignaturePadHandle | null>; photoFile: Blob | null }) {
  const hasSig = signatureFile != null || (sigRef.current ? !sigRef.current.isEmpty() : false);
  return (
    <>
      <ReviewSection title="Identity">
        <Row k="Full name" v={s.full_name} />
        <Row k="ID type" v={s.id_doc_kind.replace('_', ' ')} />
        <Row k="ID / Passport #" v={s.id_doc_number} mono />
        <Row k="KRA PIN" v={s.kra_pin || '—'} mono />
        <Row k="Gender" v={s.gender} />
        <Row k="Date of birth" v={s.date_of_birth || '—'} mono />
      </ReviewSection>

      <ReviewSection title="Contact & address">
        <Row k="Phone" v={s.phone || '—'} mono />
        <Row k="Email" v={s.email || '—'} />
        <Row k="County" v={s.county || '—'} />
        <Row k="Sub-county" v={s.sub_county || '—'} />
        <Row k="Physical address" v={s.physical_address || '—'} />
      </ReviewSection>

      <ReviewSection title="Employment">
        <Row k="Status" v={s.employment_status || '—'} />
        <Row k="Job title" v={s.job_title || '—'} />
        <Row k="Employer" v={s.employer || '—'} />
        <Row k="Payroll #" v={s.payroll_no || '—'} mono />
      </ReviewSection>

      <ReviewSection title="Next of kin">
        {s.next_of_kin_full_name ? (
          <>
            <Row k="Full name" v={s.next_of_kin_full_name} />
            <Row k="Relationship" v={s.next_of_kin_relationship || '—'} />
            <Row k="Phone" v={s.next_of_kin_phone || '—'} mono />
            <Row k="ID #" v={s.next_of_kin_id || '—'} mono />
          </>
        ) : (
          <Row k="" v={<span className="muted">No next of kin recorded.</span>} />
        )}
      </ReviewSection>

      <ReviewSection title="Beneficiaries">
        {s.beneficiaries.filter((b) => b.full_name.trim()).length === 0 ? (
          <Row k="" v={<span className="muted">No beneficiaries listed.</span>} />
        ) : (
          s.beneficiaries.filter((b) => b.full_name.trim()).map((b, i) => (
            <Row key={i} k={`#${i + 1} ${b.full_name}`} v={<>
              <span className="muted">{b.relationship || 'unspecified'}</span>
              {b.share_percent && <span className="mono"> · {b.share_percent}%</span>}
            </>} />
          ))
        )}
      </ReviewSection>

      <ReviewSection title="Documents">
        <Row k="Signature" v={hasSig ? <span style={{ color: 'var(--pos)' }}>provided</span> : <span className="muted">none</span>} />
        <Row k="Passport photo" v={photoFile ? <span style={{ color: 'var(--pos)' }}>provided</span> : <span className="muted">none</span>} />
      </ReviewSection>

      <p className="muted tiny" style={{ marginTop: 12 }}>
        On submit the member will be created in <strong>pending</strong> status. A reviewer with the <code className="mono">members:approve</code> permission completes onboarding.
      </p>
    </>
  );
}

// ─────────── Field shell + tiny review row ───────────

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

// Suppress an unused-form-event warning for a future submit-on-enter handler.
export type _OnboardingFormEvent = FormEvent;
