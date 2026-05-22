// New membership application — single capture form with a kind
// toggle at the top. Branches the visible fields between individual
// (personal KYC) and institutional (entity KYC) variants. The
// registration-fee section auto-shows when the tenant has the
// toggle on.

import { useEffect, useState } from 'react';
import {
  createApplication,
  getTenantSettings,
  type ApplicantPayload,
  type ApplicationKind,
  type TenantMembership,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const INSTITUTIONAL_TYPES = [
  'chama', 'group', 'sole_proprietorship', 'ltd', 'plc', 'partnership',
  'ngo', 'church', 'cooperative', 'school', 'sacco',
];

export default function NewApplicationPage() {
  const { tenant } = useAuth();
  const [kind, setKind] = useState<ApplicationKind>('individual');
  const [applicantName, setApplicantName] = useState('');
  const [entityType, setEntityType] = useState('chama');
  const [primaryPhone, setPrimaryPhone] = useState('');
  const [primaryEmail, setPrimaryEmail] = useState('');
  const [payload, setPayload] = useState<ApplicantPayload>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [membership, setMembership] = useState<TenantMembership | null>(null);

  // Registration fee fields
  const [feeAmountPaid, setFeeAmountPaid] = useState('');
  const [feeChannel, setFeeChannel] = useState('');
  const [feeReference, setFeeReference] = useState('');
  const [feeDate, setFeeDate] = useState(new Date().toISOString().slice(0, 10));
  const [feeShortfallNote, setFeeShortfallNote] = useState('');

  useEffect(() => {
    void (async () => {
      try {
        const s = await getTenantSettings();
        setMembership(s.membership);
        if (s.membership.accepted_payment_channels.length > 0) {
          setFeeChannel(s.membership.accepted_payment_channels[0]);
        }
      } catch (e) { setErr(asMsg(e)); }
    })();
  }, []);

  const feeRequired = membership?.collect_registration_fee ?? false;
  const feeDue = kind === 'individual'
    ? membership?.registration_fee_individual ?? 0
    : membership?.registration_fee_institutional ?? 0;

  function set<K extends keyof ApplicantPayload>(k: K, v: ApplicantPayload[K]) {
    setPayload({ ...payload, [k]: v });
  }

  async function submit() {
    setBusy(true); setErr(null);
    try {
      const input: Parameters<typeof createApplication>[0] = {
        kind,
        applicant_name: applicantName,
        entity_type: kind === 'institutional' ? entityType : undefined,
        primary_phone: primaryPhone || undefined,
        primary_email: primaryEmail || undefined,
        applicant_payload: payload,
      };
      if (feeRequired) {
        input.registration_fee = {
          amount_paid: feeAmountPaid || '0',
          payment_channel: feeChannel,
          payment_reference: feeReference,
          payment_date: feeDate,
          shortfall_note: feeShortfallNote || undefined,
        };
      }
      const a = await createApplication(input);
      window.location.href = `/applications/${a.id}`;
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const paid = parseFloat(feeAmountPaid || '0');
  const isShortfall = feeRequired && paid > 0 && paid < feeDue;

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Members · New application</div>
          <h1>Capture membership application</h1>
          <div className="page-sub">
            One form for both individual and institutional applicants. Submitting enters the
            application into the review queue at <span className="mono">Pending review</span>.
          </div>
        </div>
        <a className="btn" href="/applications">← Queue</a>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Applicant type</h3></div>
        <div className="card-body" style={{ display: 'flex', gap: 18 }}>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <input type="radio" checked={kind === 'individual'} onChange={() => setKind('individual')} />
            <span><strong>Individual</strong> — natural person</span>
          </label>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <input type="radio" checked={kind === 'institutional'} onChange={() => setKind('institutional')} />
            <span><strong>Institutional</strong> — business, chama, NGO, etc.</span>
          </label>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>{kind === 'individual' ? 'Personal details' : 'Entity details'}</h3></div>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
          <label>
            <div className="muted tiny">{kind === 'individual' ? 'Full name' : 'Registered name'} *</div>
            <input value={applicantName} onChange={(e) => setApplicantName(e.target.value)} placeholder={kind === 'individual' ? 'Jane Wanjiru Mwangi' : 'Acme Investment Chama'} />
          </label>
          {kind === 'institutional' && (
            <>
              <label>
                <div className="muted tiny">Entity type</div>
                <select value={entityType} onChange={(e) => setEntityType(e.target.value)}>
                  {INSTITUTIONAL_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              </label>
              <label>
                <div className="muted tiny">Trading name</div>
                <input value={payload.trading_name ?? ''} onChange={(e) => set('trading_name', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Registration number</div>
                <input value={payload.registration_number ?? ''} onChange={(e) => set('registration_number', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Date of registration</div>
                <input type="date" value={payload.date_of_registration ?? ''} onChange={(e) => set('date_of_registration', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Industry</div>
                <input value={payload.industry ?? ''} onChange={(e) => set('industry', e.target.value)} />
              </label>
              <label style={{ gridColumn: '1 / -1' }}>
                <div className="muted tiny">Nature of business</div>
                <input value={payload.nature_of_business ?? ''} onChange={(e) => set('nature_of_business', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Entity KRA PIN</div>
                <input value={payload.kra_pin ?? ''} onChange={(e) => set('kra_pin', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Board resolution reference</div>
                <input value={payload.board_resolution_ref ?? ''} onChange={(e) => set('board_resolution_ref', e.target.value)} />
              </label>
              <label style={{ gridColumn: '1 / -1' }}>
                <div className="muted tiny">Beneficial ownership declaration</div>
                <textarea rows={2} value={payload.beneficial_owners ?? ''} onChange={(e) => set('beneficial_owners', e.target.value)}
                  placeholder="Named persons holding ≥10% beneficial interest. Per AML requirements." />
              </label>
            </>
          )}
          {kind === 'individual' && (
            <>
              <label>
                <div className="muted tiny">Date of birth</div>
                <input type="date" value={payload.date_of_birth ?? ''} onChange={(e) => set('date_of_birth', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Gender</div>
                <select value={payload.gender ?? ''} onChange={(e) => set('gender', e.target.value)}>
                  <option value="">—</option>
                  <option value="female">Female</option>
                  <option value="male">Male</option>
                  <option value="undisclosed">Undisclosed</option>
                </select>
              </label>
              <label>
                <div className="muted tiny">Nationality</div>
                <input value={payload.nationality ?? ''} onChange={(e) => set('nationality', e.target.value)} placeholder="Kenyan" />
              </label>
              <label>
                <div className="muted tiny">ID kind</div>
                <select value={payload.id_doc_kind ?? 'national_id'} onChange={(e) => set('id_doc_kind', e.target.value)}>
                  <option value="national_id">National ID</option>
                  <option value="passport">Passport</option>
                </select>
              </label>
              <label>
                <div className="muted tiny">ID number</div>
                <input value={payload.id_doc_number ?? ''} onChange={(e) => set('id_doc_number', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">KRA PIN</div>
                <input value={payload.kra_pin ?? ''} onChange={(e) => set('kra_pin', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Occupation</div>
                <input value={payload.occupation ?? ''} onChange={(e) => set('occupation', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Employer</div>
                <input value={payload.employer ?? ''} onChange={(e) => set('employer', e.target.value)} />
              </label>
              <label>
                <div className="muted tiny">Monthly income</div>
                <input value={payload.monthly_income ?? ''} onChange={(e) => set('monthly_income', e.target.value)} />
              </label>
            </>
          )}
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Contact</h3></div>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
          <label>
            <div className="muted tiny">Phone</div>
            <input value={primaryPhone} onChange={(e) => setPrimaryPhone(e.target.value)} placeholder="+2547xxxxxxxx" />
          </label>
          <label>
            <div className="muted tiny">Email</div>
            <input type="email" value={primaryEmail} onChange={(e) => setPrimaryEmail(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">County</div>
            <input value={payload.county ?? ''} onChange={(e) => set('county', e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">Sub-county</div>
            <input value={payload.sub_county ?? ''} onChange={(e) => set('sub_county', e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">Ward</div>
            <input value={payload.ward ?? ''} onChange={(e) => set('ward', e.target.value)} />
          </label>
          <label style={{ gridColumn: '1 / -1' }}>
            <div className="muted tiny">Physical address</div>
            <input value={payload.physical_address ?? ''} onChange={(e) => set('physical_address', e.target.value)} />
          </label>
        </div>
      </div>

      {kind === 'individual' && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd"><h3>Next of kin</h3></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12 }}>
            <label><div className="muted tiny">Name</div><input value={payload.next_of_kin_name ?? ''} onChange={(e) => set('next_of_kin_name', e.target.value)} /></label>
            <label><div className="muted tiny">Relationship</div><input value={payload.next_of_kin_relation ?? ''} onChange={(e) => set('next_of_kin_relation', e.target.value)} /></label>
            <label><div className="muted tiny">Phone</div><input value={payload.next_of_kin_phone ?? ''} onChange={(e) => set('next_of_kin_phone', e.target.value)} /></label>
            <label><div className="muted tiny">ID number</div><input value={payload.next_of_kin_id_number ?? ''} onChange={(e) => set('next_of_kin_id_number', e.target.value)} /></label>
          </div>
        </div>
      )}

      {feeRequired && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd">
            <h3>Registration fee</h3>
            <span className="card-sub">Due: <span className="mono">{feeDue}</span></span>
          </div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12 }}>
            <label>
              <div className="muted tiny">Amount paid</div>
              <input type="number" step="0.01" value={feeAmountPaid} onChange={(e) => setFeeAmountPaid(e.target.value)} placeholder={String(feeDue)} />
            </label>
            <label>
              <div className="muted tiny">Channel</div>
              <select value={feeChannel} onChange={(e) => setFeeChannel(e.target.value)}>
                {membership?.accepted_payment_channels.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
            </label>
            <label>
              <div className="muted tiny">Reference</div>
              <input value={feeReference} onChange={(e) => setFeeReference(e.target.value)} placeholder="M-Pesa code / receipt no" />
            </label>
            <label>
              <div className="muted tiny">Date paid</div>
              <input type="date" value={feeDate} onChange={(e) => setFeeDate(e.target.value)} />
            </label>
            {isShortfall && (
              <label style={{ gridColumn: '1 / -1' }}>
                <div className="muted tiny" style={{ color: '#c97a00' }}>
                  Shortfall: paid {paid} of {feeDue}. Add a note explaining (the reviewer will assess):
                </div>
                <input value={feeShortfallNote} onChange={(e) => setFeeShortfallNote(e.target.value)} />
              </label>
            )}
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Submission</h3></div>
        <div className="card-body" style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <a className="btn" href="/applications">Cancel</a>
          <button className="btn btn-primary" disabled={busy || !applicantName.trim()} onClick={() => void submit()}>
            {busy ? 'Submitting…' : 'Submit application'}
          </button>
        </div>
      </div>
    </div>
  );
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
