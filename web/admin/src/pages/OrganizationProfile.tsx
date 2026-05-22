// Organisation 360 — sticky header + six tabs:
//   Overview · Profile · Documents · People · Banking · Activity
//
// Document expiry rendered as "OK / expiring in N / EXPIRED" badges so
// compliance staff can spot at a glance what needs renewal. PEP and
// sanctions flags surface in the People tab.

import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  approveOrg,
  fetchOfficialFile,
  fetchOrgDocument,
  getOrg,
  listAuditForTarget,
  rejectOrg,
  screenOfficial,
  setOrgKYCStatus,
  setOrgStatus,
  uploadOfficialFile,
  uploadOrgDocument,
  verifyOrgDocument,
  extractError,
  type ApiBanking,
  type ApiOfficial,
  type ApiOrgContact,
  type ApiOrgDetail,
  type ApiOrgDocument,
  type ApiSignatory,
  type AuditEntry,
  type DocVerification,
  type KYCReviewStatus,
  type OfficialPosition,
  type OrgDocKind,
  type OrgKind,
} from '../api/client';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';
import { usePageCrumb } from '../lib/pageCrumb';

type TabId = 'overview' | 'profile' | 'documents' | 'people' | 'banking' | 'activity';
const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',  label: 'Overview' },
  { id: 'profile',   label: 'Profile' },
  { id: 'documents', label: 'Documents' },
  { id: 'people',    label: 'People' },
  { id: 'banking',   label: 'Banking' },
  { id: 'activity',  label: 'Activity' },
];

const KIND_LABEL: Record<OrgKind, string> = {
  group: 'Group', chama: 'Chama', ltd: 'Limited Co', sole_prop: 'Sole Proprietorship',
  ngo: 'NGO', church: 'Church', sacco: 'SACCO', cooperative: 'Cooperative', school: 'School',
};

const DOC_LABEL: Record<OrgDocKind, string> = {
  registration_certificate: 'Registration certificate',
  cr12: 'CR12',
  kra_pin_certificate: 'KRA PIN certificate',
  memorandum_articles: 'Memorandum & Articles',
  constitution_bylaws: 'Constitution / By-laws',
  business_permit: 'Business permit',
  tax_compliance_certificate: 'Tax compliance certificate',
  vat_certificate: 'VAT certificate',
  ngo_certificate: 'NGO certificate',
  cooperative_certificate: 'Cooperative certificate',
  proof_of_address: 'Proof of address',
  audited_financials: 'Audited financials',
  bank_statement: 'Bank statement',
  board_resolution: 'Board resolution',
  signatory_appointment_resolution: 'Signatory appointment resolution',
  beneficial_ownership_declaration: 'Beneficial ownership declaration',
};

// The minimum set we ask every org to provide. Specific kinds layer
// extras on top (eg ltd → CR12 + M&A; NGO → ngo_certificate).
const ALWAYS_REQUIRED: OrgDocKind[] = [
  'registration_certificate', 'kra_pin_certificate', 'proof_of_address',
  'board_resolution', 'signatory_appointment_resolution',
];

const PER_KIND_REQUIRED: Partial<Record<OrgKind, OrgDocKind[]>> = {
  ltd: ['cr12', 'memorandum_articles', 'business_permit', 'tax_compliance_certificate', 'audited_financials'],
  sole_prop: ['business_permit'],
  ngo: ['ngo_certificate', 'constitution_bylaws'],
  cooperative: ['cooperative_certificate', 'constitution_bylaws'],
  sacco: ['cooperative_certificate', 'audited_financials'],
  group: ['constitution_bylaws'],
  chama: ['constitution_bylaws'],
  church: ['constitution_bylaws'],
  school: ['business_permit'],
};

const KYC_TONE: Record<KYCReviewStatus, 'neutral' | 'warn' | 'pos' | 'neg'> = {
  not_started: 'neutral', in_review: 'warn', verified: 'pos', rejected: 'neg',
};

const POSITION_LABEL: Record<OfficialPosition, string> = {
  chairperson: 'Chairperson', vice_chairperson: 'Vice Chairperson',
  treasurer: 'Treasurer', secretary: 'Secretary',
  director: 'Director', trustee: 'Trustee',
  principal: 'Principal', pastor: 'Pastor', other: 'Other',
};

export default function OrganizationProfile() {
  const orgId = useMemo(() => extractIdFromPath(window.location.pathname), []);
  const initialTab = useMemo<TabId>(() => {
    const t = new URLSearchParams(window.location.search).get('tab') as TabId | null;
    return (t && TABS.some((x) => x.id === t)) ? t : 'overview';
  }, []);
  const { hasPermission } = useAuth();
  const canApprove = hasPermission('members:approve');
  const canEdit = hasPermission('members:edit');
  const canCreate = hasPermission('members:create');
  const canSeeAudit = hasPermission('audit:view');

  const [tab, setTab] = useState<TabId>(initialTab);
  const [o, setO] = useState<ApiOrgDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // Crumb now reads "Organisations → Acme SACCO" once the org loads.
  usePageCrumb(o?.registered_name);

  function navigateTab(next: TabId) {
    setTab(next);
    const url = new URL(window.location.href);
    url.searchParams.set('tab', next);
    window.history.replaceState({}, '', url);
  }

  async function reload() {
    if (!orgId) return;
    setErr(null);
    try { setO(await getOrg(orgId)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [orgId]);

  if (!orgId) {
    return <div className="page"><div className="alert alert-error">Missing org id in URL.</div></div>;
  }

  async function onApprove() {
    if (!o) return;
    if (!confirm(`Approve ${o.registered_name}?`)) return;
    setBusy('approve');
    try { await approveOrg(o.id); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
  async function onReject() {
    if (!o) return;
    const reason = prompt(`Reject ${o.registered_name}. Reason?`);
    if (!reason) return;
    setBusy('reject');
    try { await rejectOrg(o.id, reason); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
  async function onStatus(next: 'active' | 'suspended' | 'closed' | 'dormant') {
    if (!o) return;
    const labels = { active: 'reactivate', suspended: 'suspend', closed: 'close', dormant: 'mark dormant' };
    if (!confirm(`${labels[next]} ${o.registered_name}?`)) return;
    setBusy(next);
    try { await setOrgStatus(o.id, next); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
  async function onKYC(next: KYCReviewStatus) {
    if (!o) return;
    setBusy('kyc');
    try { await setOrgKYCStatus(o.id, next); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/orgs" style={{ color: 'var(--accent)' }}>← Organisations</a>
          </div>
          <h1 style={{ marginBottom: 4 }}>{o ? o.registered_name : 'Loading…'}</h1>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {!o && !err && <div className="empty">Loading…</div>}

      {o && (
        <>
          <HeaderCard
            o={o} busy={busy}
            canApprove={canApprove} canEdit={canEdit}
            onApprove={onApprove} onReject={onReject} onStatus={onStatus} onKYC={onKYC}
          />

          <div className="card" style={{ padding: 0 }}>
            <div className="tabs" style={{ padding: '0 14px' }}>
              {TABS.map((t) => (
                <div key={t.id} className="tab" data-active={tab === t.id} onClick={() => navigateTab(t.id)}>
                  {t.label}
                </div>
              ))}
            </div>
            <div style={{ padding: 14 }}>
              {tab === 'overview'  && <OverviewTab o={o} onJump={navigateTab} canSeeAudit={canSeeAudit} />}
              {tab === 'profile'   && <ProfileTab o={o} />}
              {tab === 'documents' && <DocumentsTab o={o} canCreate={canCreate} canEdit={canEdit} onChanged={reload} />}
              {tab === 'people'    && <PeopleTab o={o} canCreate={canCreate} canEdit={canEdit} onChanged={reload} />}
              {tab === 'banking'   && <BankingTab banking={o.banking} contacts={o.contacts} />}
              {tab === 'activity'  && <ActivityTab orgId={o.id} canSeeAudit={canSeeAudit} />}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

// ─────────── Header ───────────

function HeaderCard({
  o, busy, canApprove, canEdit,
  onApprove, onReject, onStatus, onKYC,
}: {
  o: ApiOrgDetail; busy: string | null;
  canApprove: boolean; canEdit: boolean;
  onApprove: () => void; onReject: () => void;
  onStatus: (s: 'active' | 'suspended' | 'closed' | 'dormant') => void;
  onKYC: (s: KYCReviewStatus) => void;
}) {
  const required = requiredDocsFor(o.kind);
  const have = new Set(o.documents.map((d) => d.kind));
  const verified = o.documents.filter((d) => d.verification === 'verified').length;
  const docCoverage = required.length === 0 ? 100 :
    Math.round((required.filter((k) => have.has(k)).length / required.length) * 100);

  return (
    <div className="card" style={{ marginBottom: 14 }}>
      <div className="m360-hd">
        <div className="m360-hd-photo" style={{ background: 'var(--accent-bg)', borderColor: 'var(--accent)' }}>
          <span className="mono" style={{ fontSize: 24, fontWeight: 700, color: 'var(--accent-fg)' }}>
            {o.registered_name.charAt(0).toUpperCase()}
          </span>
        </div>
        <div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>{o.registered_name}</div>
          {o.trading_name && o.trading_name !== o.registered_name && (
            <div className="muted tiny">t/a {o.trading_name}</div>
          )}
          <div className="m360-hd-meta">
            <span className="mono">{o.org_no}</span>
            <Badge tone="neutral">{KIND_LABEL[o.kind]}</Badge>
            <StatusBadge status={o.status} />
            <Badge tone={KYC_TONE[o.kyc_status]}>KYC: {o.kyc_status.replace('_', ' ')}</Badge>
            <Badge tone={o.risk_category === 'low' ? 'pos' : o.risk_category === 'high' ? 'neg' : 'warn'}>
              risk: {o.risk_category}
            </Badge>
            {o.blacklisted && <Badge tone="neg">BLACKLISTED</Badge>}
            {o.registration_no && (<>
              <span className="muted">·</span>
              <span className="tiny-mono">{o.registration_no}</span>
            </>)}
          </div>
          <div className="muted tiny" style={{ marginTop: 6 }}>
            {o.county ? `${o.county}${o.sub_county ? ', ' + o.sub_county : ''} · ` : ''}
            Document coverage {docCoverage}% ({verified}/{o.documents.length} verified) · Joined {new Date(o.created_at).toISOString().slice(0, 10)}
          </div>
        </div>
        <div className="m360-hd-actions">
          {o.status === 'pending' && canApprove && (
            <>
              <button className="btn btn-sm" style={{ color: 'var(--pos)' }} disabled={busy != null} onClick={onApprove}>
                <Icon name="check" size={12} /> {busy === 'approve' ? 'Approving…' : 'Approve'}
              </button>
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy != null} onClick={onReject}>
                <Icon name="x" size={12} /> Reject
              </button>
            </>
          )}
          {o.status === 'active' && canEdit && (
            <>
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy != null} onClick={() => onStatus('suspended')}>
                <Icon name="lock" size={12} /> Suspend
              </button>
              <button className="btn btn-sm" disabled={busy != null} onClick={() => onStatus('dormant')}>
                Mark dormant
              </button>
            </>
          )}
          {o.status === 'suspended' && canEdit && (
            <button className="btn btn-sm" style={{ color: 'var(--pos)' }} disabled={busy != null} onClick={() => onStatus('active')}>
              <Icon name="check" size={12} /> Reactivate
            </button>
          )}
          {canEdit && o.status === 'active' && (
            <select
              className="select"
              style={{ height: 28, fontSize: 12 }}
              value={o.kyc_status}
              disabled={busy != null}
              onChange={(e) => onKYC(e.target.value as KYCReviewStatus)}
              title="KYC review status"
            >
              <option value="not_started">KYC not started</option>
              <option value="in_review">KYC in review</option>
              <option value="verified">KYC verified</option>
              <option value="rejected">KYC rejected</option>
            </select>
          )}
        </div>
      </div>
      {o.status === 'rejected' && o.rejection_reason && (
        <div className="alert alert-warn" style={{ margin: '0 14px 14px' }}>
          <strong>Rejected:</strong> {o.rejection_reason}
        </div>
      )}
    </div>
  );
}

// ─────────── Overview ───────────

function OverviewTab({ o, onJump, canSeeAudit }: { o: ApiOrgDetail; onJump: (t: TabId) => void; canSeeAudit: boolean }) {
  const required = requiredDocsFor(o.kind);
  const have = new Set(o.documents.map((d) => d.kind));
  const missing = required.filter((k) => !have.has(k));
  const expiringSoon = o.documents.filter((d) => d.expiry_date && daysToExpiry(d.expiry_date) <= 30 && daysToExpiry(d.expiry_date) > 0);
  const expired = o.documents.filter((d) => d.expiry_date && daysToExpiry(d.expiry_date) <= 0);
  const pendingVerify = o.documents.filter((d) => d.verification === 'pending');
  const beneficialOwners = o.officials.filter((p) => p.is_beneficial_owner);
  const peps = o.officials.filter((p) => p.is_pep);
  const sanctionsHits = o.officials.filter((p) => p.sanctions_hit);
  const unscreened = o.officials.filter((p) => !p.sanctions_screened_at);

  return (
    <>
      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Documents on file" value={`${o.documents.length} / ${required.length}`} />
        <KPI label="Verified" value={`${o.documents.filter((d) => d.verification === 'verified').length}`} tone="pos" />
        <KPI label="Expiring ≤ 30d" value={`${expiringSoon.length}`} tone="warn" />
        <KPI label="Expired" value={`${expired.length}`} tone="neg" />
        <KPI label="Officials" value={`${o.officials.length}`} />
        <KPI label="Signatories" value={`${o.signatories.length}`} />
        <KPI label="Beneficial owners" value={`${beneficialOwners.length}`} tone={beneficialOwners.length > 0 ? 'pos' : 'warn'} />
        <KPI label="PEP / sanctions" value={`${peps.length + sanctionsHits.length}`} tone={peps.length + sanctionsHits.length > 0 ? 'neg' : 'pos'} />
      </div>

      <div className="grid-2">
        {/* KYC summary */}
        <div className="card">
          <div className="card-hd">
            <h3>KYC readiness</h3>
            <span className="card-sub">{KIND_LABEL[o.kind]} required set</span>
            <div className="card-hd-actions">
              <button className="btn btn-sm btn-ghost" onClick={() => onJump('documents')}>
                Manage docs <Icon name="chevron_r" size={12} />
              </button>
            </div>
          </div>
          <div className="card-body">
            {missing.length === 0 && expired.length === 0 && pendingVerify.length === 0 ? (
              <p className="tiny" style={{ margin: 0, color: 'var(--pos)' }}>✓ Required set complete and verified.</p>
            ) : (
              <>
                {missing.length > 0 && (
                  <div style={{ marginBottom: 8 }}>
                    <div className="tiny" style={{ color: 'var(--warn)', fontWeight: 500, marginBottom: 4 }}>Missing ({missing.length})</div>
                    <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12 }}>
                      {missing.map((k) => <li key={k}>{DOC_LABEL[k]}</li>)}
                    </ul>
                  </div>
                )}
                {expired.length > 0 && (
                  <div style={{ marginBottom: 8 }}>
                    <div className="tiny" style={{ color: 'var(--neg)', fontWeight: 500, marginBottom: 4 }}>Expired ({expired.length})</div>
                    <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12 }}>
                      {expired.map((d) => <li key={d.id}>{DOC_LABEL[d.kind]} — expired {d.expiry_date?.slice(0, 10)}</li>)}
                    </ul>
                  </div>
                )}
                {pendingVerify.length > 0 && (
                  <div>
                    <div className="tiny muted" style={{ fontWeight: 500, marginBottom: 4 }}>Pending verification ({pendingVerify.length})</div>
                    <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12, color: 'var(--fg-3)' }}>
                      {pendingVerify.map((d) => <li key={d.id}>{DOC_LABEL[d.kind]}</li>)}
                    </ul>
                  </div>
                )}
              </>
            )}
          </div>
        </div>

        {/* Screening summary */}
        <div className="card">
          <div className="card-hd">
            <h3>Compliance screening</h3>
            <span className="card-sub">PEP + sanctions per official</span>
            <div className="card-hd-actions">
              <button className="btn btn-sm btn-ghost" onClick={() => onJump('people')}>
                People tab <Icon name="chevron_r" size={12} />
              </button>
            </div>
          </div>
          <div className="card-body">
            <Row k="Officials screened" v={`${o.officials.length - unscreened.length} / ${o.officials.length}`} />
            <Row k="PEP flags" v={peps.length === 0 ? '—' : peps.map((p) => p.full_name).join(', ')} />
            <Row k="Sanctions hits" v={sanctionsHits.length === 0 ? '—' : sanctionsHits.map((p) => p.full_name).join(', ')} />
            {unscreened.length > 0 && (
              <p className="tiny" style={{ color: 'var(--warn)', marginTop: 6 }}>
                {unscreened.length} official{unscreened.length === 1 ? '' : 's'} not yet screened.
              </p>
            )}
            <p className="muted tiny" style={{ marginTop: 8 }}>
              Screening is manual today — flag PEPs at onboarding and record sanctions checks on each official. Automated screening lands when we wire ComplyAdvantage / OFAC.
            </p>
          </div>
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Recent activity</h3>
          <span className="card-sub">last 5 events</span>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={() => onJump('activity')}>
              All activity <Icon name="chevron_r" size={12} />
            </button>
          </div>
        </div>
        <div className="card-body">
          {canSeeAudit ? <AuditTimeline orgId={o.id} limit={5} /> : <span className="muted tiny">You don't have audit:view.</span>}
        </div>
      </div>
    </>
  );
}

function KPI({ label, value, tone }: { label: string; value: number | string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="m360-stat">
        <span className="m360-stat-label">{label}</span>
        <span className="m360-stat-value" style={{ color }}>{value}</span>
      </div>
    </div>
  );
}

// ─────────── Profile ───────────

function ProfileTab({ o }: { o: ApiOrgDetail }) {
  return (
    <div className="grid-2">
      <Card title="Identity">
        <KVS>
          <Row k="Registered name" v={o.registered_name} />
          <Row k="Trading name" v={o.trading_name || '—'} />
          <Row k="Type" v={KIND_LABEL[o.kind]} />
          <Row k="Registration #" v={o.registration_no || '—'} mono />
          <Row k="Registered on" v={o.date_of_registration?.slice(0, 10) || '—'} mono />
          <Row k="Operating since" v={o.date_of_operation?.slice(0, 10) || '—'} mono />
        </KVS>
      </Card>
      <Card title="Business">
        <KVS>
          <Row k="Industry" v={o.industry || '—'} />
          <Row k="Nature of business" v={o.nature_of_business || '—'} />
          <Row k="Members" v={o.member_count != null ? o.member_count.toLocaleString() : '—'} mono />
          <Row k="Employees" v={o.employee_count != null ? o.employee_count.toLocaleString() : '—'} mono />
          <Row k="Risk category" v={o.risk_category} />
        </KVS>
      </Card>
      <Card title="Location">
        <KVS>
          <Row k="Physical address" v={o.physical_address || '—'} />
          <Row k="Postal address" v={o.postal_address || '—'} />
          <Row k="County" v={o.county || '—'} />
          <Row k="Sub-county" v={o.sub_county || '—'} />
          <Row k="Ward" v={o.ward || '—'} />
        </KVS>
      </Card>
      <Card title="Onboarding">
        <KVS>
          <Row k="Org #" v={o.org_no} mono />
          <Row k="Status" v={<StatusBadge status={o.status} />} />
          <Row k="KYC" v={<Badge tone={KYC_TONE[o.kyc_status]}>{o.kyc_status.replace('_', ' ')}</Badge>} />
          <Row k="Created" v={new Date(o.created_at).toISOString().slice(0, 10)} mono />
          {o.approved_at && <Row k="Approved" v={new Date(o.approved_at).toISOString().slice(0, 16).replace('T', ' ')} mono />}
        </KVS>
      </Card>
    </div>
  );
}

// ─────────── Documents ───────────

function DocumentsTab({
  o, canCreate, canEdit, onChanged,
}: {
  o: ApiOrgDetail; canCreate: boolean; canEdit: boolean; onChanged: () => void | Promise<void>;
}) {
  const required = requiredDocsFor(o.kind);
  const optional = (Object.keys(DOC_LABEL) as OrgDocKind[]).filter((k) => !required.includes(k));
  const have = new Map(o.documents.map((d) => [d.kind, d]));

  return (
    <>
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Required documents for <strong>{KIND_LABEL[o.kind]}</strong>. Capture expiry dates so we can warn before renewal time. Verified ≠ uploaded — verification is a separate compliance step.
      </p>
      <div className="card">
        <div className="card-hd"><h3>Required documents</h3></div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Document</th>
                <th>Status</th>
                <th>Issue / expiry</th>
                <th>Verification</th>
                <th>Updated</th>
                <th style={{ width: 1 }}></th>
              </tr>
            </thead>
            <tbody>
              {required.map((k) => (
                <DocumentRow
                  key={k} orgId={o.id} kind={k} doc={have.get(k)}
                  required canUpload={canCreate} canVerify={canEdit}
                  onChanged={onChanged}
                />
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Optional documents</h3>
          <span className="card-sub">helpful but not mandatory</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Document</th>
                <th>Status</th>
                <th>Issue / expiry</th>
                <th>Verification</th>
                <th>Updated</th>
                <th style={{ width: 1 }}></th>
              </tr>
            </thead>
            <tbody>
              {optional.map((k) => (
                <DocumentRow
                  key={k} orgId={o.id} kind={k} doc={have.get(k)}
                  required={false} canUpload={canCreate} canVerify={canEdit}
                  onChanged={onChanged}
                />
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

function DocumentRow({
  orgId, kind, doc, required, canUpload, canVerify, onChanged,
}: {
  orgId: string; kind: OrgDocKind; doc?: ApiOrgDocument;
  required: boolean; canUpload: boolean; canVerify: boolean;
  onChanged: () => void | Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [issue, setIssue] = useState(doc?.issue_date?.slice(0, 10) ?? '');
  const [expiry, setExpiry] = useState(doc?.expiry_date?.slice(0, 10) ?? '');
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    setIssue(doc?.issue_date?.slice(0, 10) ?? '');
    setExpiry(doc?.expiry_date?.slice(0, 10) ?? '');
  }, [doc?.issue_date, doc?.expiry_date]);

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setBusy(true);
    try {
      await uploadOrgDocument(orgId, kind, f, {
        issue_date: issue || undefined,
        expiry_date: expiry || undefined,
      });
      await onChanged();
    } catch (ex) {
      alert(extractError(ex));
    } finally {
      setBusy(false);
      if (inputRef.current) inputRef.current.value = '';
    }
  }

  async function onVerify(status: DocVerification) {
    setBusy(true);
    try {
      const note = status === 'rejected' ? (prompt('Verification note (reason for rejection)?') ?? '') : '';
      await verifyOrgDocument(orgId, kind, status, note);
      await onChanged();
    } catch (ex) {
      alert(extractError(ex));
    } finally {
      setBusy(false);
    }
  }

  async function onView() {
    if (!doc) return;
    try {
      const blob = await fetchOrgDocument(orgId, kind);
      const url = URL.createObjectURL(blob);
      window.open(url, '_blank');
      setTimeout(() => URL.revokeObjectURL(url), 30000);
    } catch (ex) {
      alert(extractError(ex));
    }
  }

  return (
    <tr>
      <td>
        <strong>{DOC_LABEL[kind]}</strong>
        {required && <span className="badge badge-warn" style={{ marginLeft: 6 }}>required</span>}
      </td>
      <td>{doc ? <Badge tone="pos">on file</Badge> : <Badge tone="neutral">missing</Badge>}</td>
      <td>
        <div className="row" style={{ gap: 4 }}>
          <input
            type="date"
            className="input mono"
            style={{ height: 24, fontSize: 11, width: 130 }}
            value={issue} onChange={(e) => setIssue(e.target.value)}
            disabled={!canUpload || busy}
            title="Issue date"
          />
          <span className="muted tiny">→</span>
          <input
            type="date"
            className="input mono"
            style={{ height: 24, fontSize: 11, width: 130 }}
            value={expiry} onChange={(e) => setExpiry(e.target.value)}
            disabled={!canUpload || busy}
            title="Expiry date"
          />
        </div>
        {doc?.expiry_date && <ExpiryBadge expiry={doc.expiry_date} />}
      </td>
      <td>
        {doc ? (
          <Badge tone={doc.verification === 'verified' ? 'pos' : doc.verification === 'rejected' ? 'neg' : 'warn'}>
            {doc.verification}
          </Badge>
        ) : <span className="muted tiny">—</span>}
        {doc?.verification_note && (
          <div className="muted tiny" title={doc.verification_note}>
            {doc.verification_note.length > 24 ? doc.verification_note.slice(0, 24) + '…' : doc.verification_note}
          </div>
        )}
      </td>
      <td className="tiny-mono">{doc ? new Date(doc.uploaded_at).toISOString().slice(0, 10) : '—'}</td>
      <td>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          {doc && <button type="button" className="btn btn-sm" title="View" onClick={onView}><Icon name="eye" size={12} /></button>}
          {canUpload && (
            <>
              <input
                ref={inputRef}
                type="file"
                accept="image/png,image/jpeg,image/webp,application/pdf"
                onChange={onPick}
                style={{ display: 'none' }}
                disabled={busy}
              />
              <button type="button" className="btn btn-sm" disabled={busy} onClick={() => inputRef.current?.click()}>
                <Icon name="arrow_up" size={12} /> {doc ? 'Replace' : 'Upload'}
              </button>
            </>
          )}
          {doc && canVerify && doc.verification !== 'verified' && (
            <button type="button" className="btn btn-sm" style={{ color: 'var(--pos)' }} disabled={busy} title="Mark verified" onClick={() => onVerify('verified')}>
              <Icon name="check" size={12} />
            </button>
          )}
          {doc && canVerify && doc.verification !== 'rejected' && (
            <button type="button" className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy} title="Reject" onClick={() => onVerify('rejected')}>
              <Icon name="x" size={12} />
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

function ExpiryBadge({ expiry }: { expiry: string }) {
  const days = daysToExpiry(expiry);
  if (days <= 0) return <span className="badge badge-neg" style={{ marginTop: 4, display: 'inline-block' }}>expired {-days}d ago</span>;
  if (days <= 30) return <span className="badge badge-warn" style={{ marginTop: 4, display: 'inline-block' }}>expires in {days}d</span>;
  return <span className="muted tiny" style={{ marginTop: 4, display: 'inline-block' }}>expires {expiry.slice(0, 10)}</span>;
}

function daysToExpiry(iso: string): number {
  const ms = new Date(iso).getTime() - Date.now();
  return Math.ceil(ms / (1000 * 60 * 60 * 24));
}

// ─────────── People ───────────

function PeopleTab({
  o, canCreate, canEdit, onChanged,
}: {
  o: ApiOrgDetail; canCreate: boolean; canEdit: boolean; onChanged: () => void | Promise<void>;
}) {
  const sigByOff = new Map<string, ApiSignatory>(o.signatories.map((s) => [s.official_id, s]));
  return (
    <>
      <div className="card">
        <div className="card-hd">
          <h3>Officials &amp; office bearers</h3>
          <span className="card-sub">{o.officials.length} on file</span>
        </div>
        <div className="card-body flush">
          {o.officials.length === 0 ? (
            <div className="empty">No officials recorded.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Position</th>
                  <th>Contact</th>
                  <th>Flags</th>
                  <th>Signatory</th>
                  <th>Files</th>
                  <th>Screening</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {o.officials.map((p) => (
                  <OfficialRow
                    key={p.id}
                    orgId={o.id}
                    official={p}
                    signatory={sigByOff.get(p.id)}
                    canCreate={canCreate}
                    canEdit={canEdit}
                    onChanged={onChanged}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {o.mandate && Object.keys(o.mandate.rules ?? {}).length > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Mandate rules</h3>
            <span className="card-sub">how signatures clear transactions</span>
          </div>
          <div className="card-body">
            <pre className="mono tiny" style={{
              background: 'var(--surface-2)', padding: 10, borderRadius: 'var(--r-md)',
              border: '1px solid var(--border)', margin: 0, overflowX: 'auto',
            }}>{JSON.stringify(o.mandate.rules, null, 2)}</pre>
          </div>
        </div>
      )}
    </>
  );
}

function OfficialRow({
  orgId, official, signatory, canCreate, canEdit, onChanged,
}: {
  orgId: string; official: ApiOfficial; signatory?: ApiSignatory;
  canCreate: boolean; canEdit: boolean; onChanged: () => void | Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [pickKind, setPickKind] = useState<'passport_photo' | 'signature' | 'id_copy' | 'kra_pin_certificate'>('passport_photo');

  async function onScreen() {
    const note = prompt('Sanctions screening — note (e.g. screened against OFAC + UN list, no hits)') ?? '';
    if (!note) return;
    const hit = confirm('Did the screening produce a HIT? OK = hit, Cancel = clean.');
    setBusy(true);
    try { await screenOfficial(orgId, official.id, hit, note); await onChanged(); }
    catch (ex) { alert(extractError(ex)); }
    finally { setBusy(false); }
  }

  async function onUploadPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setBusy(true);
    try { await uploadOfficialFile(orgId, official.id, pickKind, f); await onChanged(); }
    catch (ex) { alert(extractError(ex)); }
    finally { setBusy(false); if (inputRef.current) inputRef.current.value = ''; }
  }

  async function viewFile(kind: string) {
    try {
      const blob = await fetchOfficialFile(orgId, official.id, kind);
      const url = URL.createObjectURL(blob);
      window.open(url, '_blank');
      setTimeout(() => URL.revokeObjectURL(url), 30000);
    } catch (ex) { alert(extractError(ex)); }
  }

  const files = official.files ?? {};
  const filesList = Object.keys(files);

  return (
    <tr>
      <td>
        <div style={{ fontWeight: 500 }}>{official.full_name}</div>
        <div className="tiny-mono">{official.id_doc_number}{official.kra_pin ? ` · KRA ${official.kra_pin}` : ''}</div>
      </td>
      <td>
        {POSITION_LABEL[official.position]}
        {official.appointed_on && <div className="muted tiny">since {official.appointed_on.slice(0, 10)}</div>}
      </td>
      <td className="tiny-mono">
        {official.phone || <span className="muted">—</span>}
        {official.email && <div>{official.email}</div>}
      </td>
      <td>
        {official.is_pep && <Badge tone="warn">PEP</Badge>}{' '}
        {official.is_beneficial_owner && (
          <Badge tone="accent">BO {official.ownership_percent ?? '?'}%</Badge>
        )}
        {!official.is_pep && !official.is_beneficial_owner && <span className="muted tiny">—</span>}
      </td>
      <td>
        {signatory ? (
          <>
            <Badge tone={signatory.class === 'mandatory' ? 'pos' : signatory.class === 'optional' ? 'warn' : 'neutral'}>
              {signatory.class}
            </Badge>
            <div className="muted tiny mono">order {signatory.signing_order}{signatory.txn_limit ? ` · cap ${signatory.txn_limit.toLocaleString()}` : ''}</div>
          </>
        ) : <span className="muted tiny">—</span>}
      </td>
      <td className="tiny">
        {filesList.length === 0 ? <span className="muted">—</span> : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            {filesList.map((k) => (
              <button
                key={k}
                type="button"
                className="btn btn-sm btn-ghost"
                style={{ height: 18, padding: '0 4px', fontSize: 10.5, justifyContent: 'flex-start' }}
                onClick={() => viewFile(k)}
                title={`Open ${k}`}
              >
                <Icon name="eye" size={10} /> {k}
              </button>
            ))}
          </div>
        )}
      </td>
      <td className="tiny">
        {official.sanctions_screened_at ? (
          <>
            <Badge tone={official.sanctions_hit ? 'neg' : 'pos'}>
              {official.sanctions_hit ? 'hit' : 'clean'}
            </Badge>
            <div className="muted tiny">{official.sanctions_screened_at.slice(0, 10)}</div>
          </>
        ) : <Badge tone="warn">unscreened</Badge>}
      </td>
      <td>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          {canCreate && (
            <div style={{ display: 'flex', gap: 2 }}>
              <select
                className="select"
                style={{ height: 22, fontSize: 10.5, width: 90 }}
                value={pickKind}
                onChange={(e) => setPickKind(e.target.value as typeof pickKind)}
                title="File kind"
              >
                <option value="passport_photo">photo</option>
                <option value="signature">sig</option>
                <option value="id_copy">id copy</option>
                <option value="kra_pin_certificate">KRA</option>
              </select>
              <input
                ref={inputRef}
                type="file"
                accept="image/png,image/jpeg,image/webp,application/pdf"
                onChange={onUploadPick}
                style={{ display: 'none' }}
                disabled={busy}
              />
              <button type="button" className="btn btn-sm" disabled={busy} title={`Upload ${pickKind}`} onClick={() => inputRef.current?.click()}>
                <Icon name="arrow_up" size={11} />
              </button>
            </div>
          )}
          {canEdit && (
            <button type="button" className="btn btn-sm" disabled={busy} title="Sanctions screen" onClick={() => void onScreen()}>
              <Icon name="shield" size={12} />
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

// ─────────── Banking ───────────

function BankingTab({ banking, contacts }: { banking?: ApiBanking; contacts: ApiOrgContact[] }) {
  return (
    <>
      <div className="grid-2">
        <Card title="Bank account">
          {banking?.bank_name ? (
            <KVS>
              <Row k="Bank" v={`${banking.bank_name}${banking.bank_branch ? ' — ' + banking.bank_branch : ''}`} />
              <Row k="Bank code" v={banking.bank_code || '—'} mono />
              <Row k="SWIFT / BIC" v={banking.swift_code || '—'} mono />
              <Row k="Account name" v={banking.account_name || '—'} />
              <Row k="Account #" v={banking.account_number || '—'} mono />
            </KVS>
          ) : (
            <p className="muted tiny" style={{ margin: 0 }}>No bank account on file.</p>
          )}
        </Card>
        <Card title="Mobile money">
          {banking?.paybill || banking?.till_number || banking?.mobile_money_phones ? (
            <KVS>
              <Row k="Paybill" v={banking?.paybill || '—'} mono />
              <Row k="Till #" v={banking?.till_number || '—'} mono />
              <Row k="Linked phones" v={banking?.mobile_money_phones || '—'} mono />
              <Row k="Settlement" v={banking?.mobile_settlement_account || '—'} />
            </KVS>
          ) : (
            <p className="muted tiny" style={{ margin: 0 }}>No mobile money setup.</p>
          )}
        </Card>
        <Card title="Disbursement &amp; repayment">
          <KVS>
            <Row k="Disbursement method" v={banking?.preferred_disbursement || '—'} />
            <Row k="Repayment method" v={banking?.preferred_repayment || '—'} />
            <Row k="Standing order" v={banking?.standing_order_details || '—'} />
            <Row k="Check-off" v={banking?.checkoff_arrangement || '—'} />
          </KVS>
        </Card>
        <Card title="Operational contacts">
          {contacts.length === 0 ? (
            <p className="muted tiny" style={{ margin: 0 }}>No contacts.</p>
          ) : (
            <KVS>
              {contacts.map((c) => (
                <Row key={c.id} k={contactLabel(c.kind)} v={<>
                  <strong>{c.full_name}</strong>
                  {c.role ? <span className="muted"> · {c.role}</span> : null}
                  {c.phone ? <div className="tiny-mono">{c.phone}</div> : null}
                  {c.email ? <div className="tiny-mono">{c.email}</div> : null}
                </>} />
              ))}
            </KVS>
          )}
        </Card>
      </div>
    </>
  );
}

// ─────────── Activity ───────────

function ActivityTab({ orgId, canSeeAudit }: { orgId: string; canSeeAudit: boolean }) {
  return (
    <div className="card">
      <div className="card-hd">
        <h3>System events</h3>
        <span className="card-sub">audit log entries for this org</span>
      </div>
      <div className="card-body">
        {canSeeAudit ? <AuditTimeline orgId={orgId} limit={100} /> : <span className="muted tiny">No audit:view permission.</span>}
      </div>
    </div>
  );
}

function AuditTimeline({ orgId, limit }: { orgId: string; limit: number }) {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    listAuditForTarget('org', orgId, limit)
      .then((es) => { if (!cancelled) setEntries(es); })
      .catch((e) => { if (!cancelled) setErr(extractError(e)); });
    return () => { cancelled = true; };
  }, [orgId, limit]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (!entries) return <span className="muted tiny">Loading…</span>;
  if (entries.length === 0) return <div className="empty" style={{ padding: 20 }}>No audit events yet.</div>;

  return (
    <ol className="tl" style={{ listStyle: 'none', margin: 0 }}>
      {entries.map((e) => (
        <li key={e.id} className="tl-item" data-tone={toneFor(e.action)}>
          <div className="tl-action">{prettyAction(e.action)}</div>
          <div className="tl-meta">
            <time>{new Date(e.created_at).toISOString().replace('T', ' ').slice(0, 19)}</time>
            {e.metadata && Object.keys(e.metadata).length > 0 && (
              <span> · <span className="mono">{summarizeMeta(e.metadata)}</span></span>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}

function toneFor(action: string): string {
  if (action.endsWith('.rejected') || action.includes('suspend')) return 'neg';
  if (action.endsWith('.created') || action.endsWith('.uploaded')) return 'muted';
  if (action.endsWith('.approved') || action.endsWith('.verified')) return 'pos';
  return '';
}
function prettyAction(action: string): string { return action.replace(/^org\./, '').replace(/_/g, ' '); }
function summarizeMeta(meta: Record<string, unknown>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(meta)) {
    if (v == null) continue;
    const s = typeof v === 'string' ? v : JSON.stringify(v);
    parts.push(`${k}=${s}`);
  }
  return parts.slice(0, 4).join(', ');
}

// ─────────── helpers ───────────

function requiredDocsFor(kind: OrgKind): OrgDocKind[] {
  return Array.from(new Set([...ALWAYS_REQUIRED, ...(PER_KIND_REQUIRED[kind] ?? [])]));
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="card">
      <div className="card-hd"><h3>{title}</h3></div>
      <div className="card-body">{children}</div>
    </div>
  );
}
function KVS({ children }: { children: ReactNode }) { return <dl className="kvs">{children}</dl>; }
function Row({ k, v, mono }: { k: ReactNode; v: ReactNode; mono?: boolean }) {
  return (<><dt>{k}</dt><dd className={mono ? 'mono' : ''}>{v}</dd></>);
}
function contactLabel(k: ApiOrgContact['kind']): string {
  return ({ primary: 'Primary', finance: 'Finance', hr_payroll: 'HR / Payroll', compliance: 'Compliance' } as Record<ApiOrgContact['kind'], string>)[k];
}
function extractIdFromPath(path: string): string {
  const m = path.match(/^\/orgs\/([^/]+)\/?$/);
  return m ? m[1] : '';
}
