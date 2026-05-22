// CounterpartyProfile — the unified kind-aware detail page.
//
// Replaces the previous split between MemberProfile and
// OrganizationProfile. ONE tab strip with seven tabs (per the
// merge spec — Overview / Profile / Accounts / People / Banking /
// Documents / Activity), each branching on counterparty.kind for
// the tab's content. Loans appear as a sub-section inside Accounts
// (no orphan tab).
//
// URL contract: both /members/:id and /orgs/:id route here when
// tenant.feature_flags.unified_counterparties is on. The path
// prefix tells us which legacy detail endpoint to load:
//   /members/<members.id>   → GET /v1/members/{id}   (kind=individual)
//   /orgs/<org_members.id>  → GET /v1/orgs/{id}      (kind=institutional)
// Both responses now carry cp_number + counterparty_id (Phase B),
// which the unified header reads to render the canonical CP-* ID
// with the legacy M-* / ORG-* as a muted secondary.
//
// The legacy MemberProfile + OrganizationProfile components are
// kept as the flag-off fallback. When the destructive Phase C+ PR
// ships, the legacy files come out and this file becomes the only
// detail page in the app.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  getMember,
  getOrg,
  listAuditForTarget,
  extractError,
  type ApiMemberDetail,
  type ApiOrgDetail,
  type ApiRelation,
  type ApiOfficial,
  type ApiSignatory,
  type AuditEntry,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';
import { Tabs } from '../components/Tabs';
import { AsyncPanel, isTimeoutError } from '../components/AsyncPanel';
import { MemberAccountsPanel } from '../components/MemberAccountsPanel';
import { MemberAccountsSummary } from '../components/MemberAccountsSummary';
import { MemberLedgerPanel } from '../components/MemberLedgerPanel';
import { MemberStatusCard } from '../components/MemberStatusCard';
import { usePageCrumb } from '../lib/pageCrumb';

// One tab strip — spec-defined, identical for both kinds.
type TabId = 'overview' | 'profile' | 'accounts' | 'people' | 'banking' | 'documents' | 'activity';
const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',  label: 'Overview' },
  { id: 'profile',   label: 'Profile' },
  { id: 'accounts',  label: 'Accounts' },   // Loans sub-section inside
  { id: 'people',    label: 'People' },
  { id: 'banking',   label: 'Banking' },
  { id: 'documents', label: 'Documents & KYC' },
  { id: 'activity',  label: 'Activity' },
];

// ─────────── URL helpers ───────────

function extractIdFromPath(path: string): string {
  // /members/<uuid> or /orgs/<uuid>
  const m = path.match(/^\/(?:members|orgs)\/([^/?]+)/);
  return m ? m[1] : '';
}

function kindFromPath(path: string): 'individual' | 'institutional' {
  return path.startsWith('/orgs/') ? 'institutional' : 'individual';
}

// ─────────── Page ───────────

export default function CounterpartyProfile() {
  const { hasPermission, tenant } = useAuth();
  const canSeeAudit = hasPermission('audit:view');
  const id = useMemo(() => extractIdFromPath(window.location.pathname), []);
  const kind = useMemo(() => kindFromPath(window.location.pathname), []);
  const currency = tenant?.currency_code ?? 'KES';

  const initialTab = useMemo<TabId>(() => {
    const t = new URLSearchParams(window.location.search).get('tab') as TabId | null;
    return (t && TABS.some((x) => x.id === t)) ? t : 'overview';
  }, []);
  const [tab, setTab] = useState<TabId>(initialTab);

  // Both detail endpoints return cp_number + a `display_name`-like
  // field (full_name for members, registered_name for orgs). One
  // AsyncPanel per kind keeps the data plumbing tidy.
  // Both endpoints return a kind-specific shape; the AsyncPanel
  // doesn't care which — the shell discriminates via the `kind`
  // closure-captured below.
  const fetcher = useMemo<() => Promise<ApiMemberDetail | ApiOrgDetail>>(
    () => () => kind === 'individual' ? getMember(id) : getOrg(id),
    [id, kind],
  );

  function navigateTab(next: TabId) {
    setTab(next);
    const url = new URL(window.location.href);
    url.searchParams.set('tab', next);
    window.history.replaceState({}, '', url);
  }

  return (
    <div className="page">
      <AsyncPanel
        fetcher={fetcher}
        deps={[id, kind]}
        empty={<div className="empty">No such counterparty.</div>}
        errorTitle="Couldn't load counterparty"
        errorMessage={(err) => isTimeoutError(err)
          ? "The member service didn't respond in time. Try again."
          : "We couldn't fetch this counterparty's profile."}
        skeleton={<div className="empty" role="status">Loading profile…</div>}
      >
        {(entity) => (
          <CounterpartyShell
            entity={entity}
            kind={kind}
            currency={currency}
            tab={tab}
            navigateTab={navigateTab}
            canSeeAudit={canSeeAudit}
          />
        )}
      </AsyncPanel>
    </div>
  );
}

// ─────────── Shell ───────────

type EntityIndividual = { kind: 'individual'; m: ApiMemberDetail };
type EntityInstitution = { kind: 'institutional'; o: ApiOrgDetail };
type Entity = EntityIndividual | EntityInstitution;

function makeEntity(kind: 'individual' | 'institutional', data: ApiMemberDetail | ApiOrgDetail): Entity {
  return kind === 'individual'
    ? { kind: 'individual', m: data as ApiMemberDetail }
    : { kind: 'institutional', o: data as ApiOrgDetail };
}

function CounterpartyShell({
  entity: data,
  kind,
  currency,
  tab,
  navigateTab,
  canSeeAudit,
}: {
  entity: ApiMemberDetail | ApiOrgDetail;
  kind: 'individual' | 'institutional';
  currency: string;
  tab: TabId;
  navigateTab: (t: TabId) => void;
  canSeeAudit: boolean;
}) {
  const entity = makeEntity(kind, data);
  const cpNumber = (data as { cp_number?: string | null }).cp_number ?? null;
  const displayName = entity.kind === 'individual' ? entity.m.full_name : entity.o.registered_name;
  const legacyId = entity.kind === 'individual' ? entity.m.member_no : entity.o.org_no;
  const status = entity.kind === 'individual' ? entity.m.status : entity.o.status;
  usePageCrumb(displayName);

  return (
    <>
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href={kind === 'individual' ? '/members' : '/orgs'} style={{ color: 'inherit' }}>
              {kind === 'individual' ? '← Members' : '← Organisations'}
            </a>
          </div>
          <h1 style={{ marginBottom: 4 }}>{displayName}</h1>
          <div className="page-sub" style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
            {/* Unified header: CP# is the canonical identifier;
                legacy id (M-* or ORG-*) is shown muted underneath. */}
            {cpNumber ? (
              <>
                <span className="mono"><strong>{cpNumber}</strong></span>
                <span className="muted tiny mono">Legacy: {legacyId}</span>
              </>
            ) : (
              <span className="mono"><strong>{legacyId}</strong></span>
            )}
            <KindBadge kind={entity.kind} subKind={entity.kind === 'institutional' ? entity.o.kind : undefined} />
            <StatusBadge status={status} />
          </div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <Tabs
          ariaLabel="Counterparty sections"
          tabs={TABS}
          value={tab}
          onChange={(t) => navigateTab(t)}
        >
          {(activeId) => (
            <>
              {activeId === 'overview'  && <OverviewTab entity={entity} currency={currency} onJump={navigateTab} canSeeAudit={canSeeAudit} />}
              {activeId === 'profile'   && <ProfileTab entity={entity} />}
              {activeId === 'accounts'  && <AccountsTab entity={entity} currency={currency} />}
              {activeId === 'people'    && <PeopleTab entity={entity} />}
              {activeId === 'banking'   && <BankingTab entity={entity} />}
              {activeId === 'documents' && <DocumentsTab entity={entity} />}
              {activeId === 'activity'  && <ActivityTab entity={entity} canSeeAudit={canSeeAudit} />}
            </>
          )}
        </Tabs>
      </div>
    </>
  );
}

// ─────────── Kind badge ───────────

function KindBadge({ kind, subKind }: { kind: 'individual' | 'institutional'; subKind?: string }) {
  if (kind === 'individual') return <Badge tone="neutral">Individual</Badge>;
  return <Badge tone="accent">{subKind ? subKind : 'Organisation'}</Badge>;
}

// ─────────── Tab bodies ───────────

function OverviewTab({ entity, currency, onJump, canSeeAudit }: {
  entity: Entity;
  currency: string;
  onJump: (t: TabId) => void;
  canSeeAudit: boolean;
}) {
  void currency;
  // For both kinds: a snapshot card + a recent-activity preview.
  // The richer KYC/risk widgets from the old MemberProfile overview
  // live under Profile / Documents now, keeping Overview compact.
  return (
    <>
      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>At a glance</h3>
          <span className="card-sub">
            {entity.kind === 'individual'
              ? 'Individual member — savings, shares, loans, and KYC.'
              : 'Organisation — officials, signatories, banking, and compliance.'}
          </span>
        </div>
        <div className="card-body">
          <p className="muted tiny" style={{ margin: 0 }}>
            Use the tabs above to drill into accounts, people, banking, documents, and activity.
            Status changes go through the workflow at the bottom of the Profile tab.
          </p>
        </div>
      </div>

      <div className="card">
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
          {canSeeAudit
            ? <AuditTimeline entity={entity} limit={5} />
            : <span className="muted tiny">You don't have audit:view permission.</span>}
        </div>
      </div>
    </>
  );
}

function ProfileTab({ entity }: { entity: Entity }) {
  if (entity.kind === 'individual') return <IndividualProfileSection m={entity.m} />;
  return <InstitutionalProfileSection o={entity.o} />;
}

function IndividualProfileSection({ m }: { m: ApiMemberDetail }) {
  return (
    <>
      <div className="grid-2">
        <ProfileCard title="Identity">
          <KVS>
            <Row k="Full name" v={m.full_name} />
            <Row k="ID type" v={m.id_doc_kind.replace('_', ' ')} />
            <Row k="ID / Passport #" v={m.id_doc_number} mono />
            <Row k="KRA PIN" v={m.kra_pin || '—'} mono />
            <Row k="Gender" v={m.gender} />
            <Row k="Date of birth" v={m.date_of_birth ? new Date(m.date_of_birth).toISOString().slice(0, 10) : '—'} mono />
          </KVS>
        </ProfileCard>
        <ProfileCard title="Contact & address">
          <KVS>
            <Row k="Phone" v={m.phone || '—'} mono />
            <Row k="Email" v={m.email || '—'} />
            <Row k="County" v={m.county || '—'} />
            <Row k="Sub-county" v={m.sub_county || '—'} />
            <Row k="Physical address" v={m.physical_address || '—'} />
          </KVS>
        </ProfileCard>
        <ProfileCard title="Employment">
          <KVS>
            <Row k="Status" v={m.employment_status || '—'} />
            <Row k="Job title" v={m.job_title || '—'} />
            <Row k="Employer" v={m.employer || '—'} />
            <Row k="Payroll / staff #" v={m.payroll_no || '—'} mono />
          </KVS>
        </ProfileCard>
        <ProfileCard title="Membership">
          <KVS>
            <Row k="Status" v={<StatusBadge status={m.status} />} />
            <Row k="Joined" v={new Date(m.created_at).toISOString().slice(0, 10)} mono />
            {m.approved_at && (
              <Row k="Approved" v={new Date(m.approved_at).toISOString().slice(0, 16).replace('T', ' ')} mono />
            )}
            {m.rejection_reason && <Row k="Rejection reason" v={m.rejection_reason} />}
          </KVS>
        </ProfileCard>
      </div>
      <MemberStatusCard memberId={m.id} currentStatus={m.status} onChanged={async () => { window.location.reload(); }} />
    </>
  );
}

function InstitutionalProfileSection({ o }: { o: ApiOrgDetail }) {
  return (
    <div className="grid-2">
      <ProfileCard title="Identity">
        <KVS>
          <Row k="Registered name" v={o.registered_name} />
          <Row k="Trading name" v={o.trading_name || '—'} />
          <Row k="Kind" v={o.kind} />
          <Row k="Registration #" v={o.registration_no || '—'} mono />
          <Row k="Registered" v={o.date_of_registration ? new Date(o.date_of_registration).toISOString().slice(0, 10) : '—'} mono />
          <Row k="Industry" v={o.industry || '—'} />
        </KVS>
      </ProfileCard>
      <ProfileCard title="Address">
        <KVS>
          <Row k="Physical address" v={o.physical_address || '—'} />
          <Row k="Postal address" v={o.postal_address || '—'} />
          <Row k="County" v={o.county || '—'} />
          <Row k="Sub-county" v={o.sub_county || '—'} />
        </KVS>
      </ProfileCard>
      <ProfileCard title="Compliance">
        <KVS>
          <Row k="KYC status" v={<Badge tone={o.kyc_status === 'verified' ? 'pos' : 'warn'}>{o.kyc_status.replace('_', ' ')}</Badge>} />
          <Row k="Risk band" v={<Badge tone={o.risk_category === 'low' ? 'pos' : o.risk_category === 'high' ? 'neg' : 'warn'}>{o.risk_category}</Badge>} />
          {o.blacklisted && <Row k="Blacklist" v={<Badge tone="neg">BLACKLISTED</Badge>} />}
        </KVS>
      </ProfileCard>
      <ProfileCard title="Membership">
        <KVS>
          <Row k="Status" v={<StatusBadge status={o.status} />} />
          <Row k="Onboarded" v={new Date(o.created_at).toISOString().slice(0, 10)} mono />
        </KVS>
      </ProfileCard>
    </div>
  );
}

function AccountsTab({ entity, currency }: { entity: Entity; currency: string }) {
  if (entity.kind === 'individual') {
    // Shares + deposit + loan history all hang off members.id today.
    // Reuses the already-shipped components from prior prompts.
    return (
      <>
        <MemberAccountsSummary memberId={entity.m.id} currency={currency} />
        <MemberAccountsPanel memberId={entity.m.id} currency={currency} />
        <MemberLedgerPanel memberId={entity.m.id} currency={currency} />
      </>
    );
  }
  // Institutional accounts wait on the savings-side counterparty_id
  // FK promotion (the destructive Phase C PR). Until then, show
  // honest "coming soon" copy — no broken UI.
  return (
    <div className="card pending-card">
      <div className="card-hd">
        <h3>Accounts</h3>
        <span className="card-sub">Shares + deposit accounts for organisations.</span>
        <div className="card-hd-actions"><span className="muted tiny">Coming soon</span></div>
      </div>
      <div className="card-body">
        <p className="muted tiny" style={{ margin: 0 }}>
          Share + deposit accounts for organisations will appear here once the
          savings tables accept counterparty-based ownership.
          In the meantime, use the legacy
          {' '}<a href={`/orgs/${entity.o.id}?tab=banking`} style={{ color: 'var(--accent)' }}>Banking tab</a> to configure
          the organisation's payout account.
        </p>
      </div>
    </div>
  );
}

function PeopleTab({ entity }: { entity: Entity }) {
  if (entity.kind === 'individual') return <IndividualPeopleSection m={entity.m} />;
  return <InstitutionalPeopleSection o={entity.o} />;
}

function IndividualPeopleSection({ m }: { m: ApiMemberDetail }) {
  return (
    <>
      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd"><h3>Next of kin</h3></div>
        <div className="card-body">
          {m.next_of_kin
            ? <RelationView r={m.next_of_kin} />
            : <p className="muted tiny" style={{ margin: 0 }}>No next of kin recorded.</p>}
        </div>
      </div>
      <div className="card">
        <div className="card-hd">
          <h3>Beneficiaries</h3>
          <span className="card-sub">{m.beneficiaries.length} on record</span>
        </div>
        <div className="card-body flush">
          {m.beneficiaries.length === 0
            ? <div className="empty">No beneficiaries listed.</div>
            : (
              <table className="tbl">
                <thead><tr><th>Name</th><th>Relationship</th><th>Phone</th><th className="r">Share</th></tr></thead>
                <tbody>
                  {m.beneficiaries.map((b) => (
                    <tr key={b.id}>
                      <td>{b.full_name}</td>
                      <td>{b.relationship || '—'}</td>
                      <td className="mono">{b.phone || '—'}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        {b.share_percent != null ? `${b.share_percent.toFixed(0)}%` : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
        </div>
      </div>
    </>
  );
}

function InstitutionalPeopleSection({ o }: { o: ApiOrgDetail }) {
  return (
    <>
      <PeopleCard title="Officials" subtitle={`${o.officials.length} on record`}>
        {o.officials.length === 0
          ? <div className="empty">No officials recorded.</div>
          : (
            <table className="tbl">
              <thead><tr><th>Position</th><th>Name</th><th>Phone</th><th>Email</th><th>Flags</th></tr></thead>
              <tbody>
                {o.officials.map((p: ApiOfficial) => (
                  <tr key={p.id}>
                    <td className="mono tiny">{p.position_label || p.position}</td>
                    <td>{p.full_name}</td>
                    <td className="mono tiny">{p.phone || '—'}</td>
                    <td className="tiny">{p.email || '—'}</td>
                    <td>
                      {p.is_pep && <Badge tone="warn">PEP</Badge>}
                      {p.sanctions_hit && <Badge tone="neg">SANCTIONS HIT</Badge>}
                      {!p.is_pep && !p.sanctions_hit && <span className="muted">—</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
      </PeopleCard>

      <PeopleCard title="Signatories" subtitle={`${o.signatories.length} on record`}>
        {o.signatories.length === 0
          ? <div className="empty">No signatories recorded.</div>
          : (
            <table className="tbl">
              <thead><tr><th>Class</th><th>Official</th><th>Signing order</th><th>Txn limit</th></tr></thead>
              <tbody>
                {o.signatories.map((s: ApiSignatory) => {
                  // Signatories reference officials by official_id;
                  // join client-side so the row carries a name.
                  const official = o.officials.find((p) => p.id === s.official_id);
                  return (
                    <tr key={s.id}>
                      <td><Badge tone="neutral">{s.class}</Badge></td>
                      <td>{official?.full_name ?? <span className="muted tiny-mono">{s.official_id.slice(0, 8)}…</span>}</td>
                      <td className="mono tiny">{s.signing_order}</td>
                      <td className="mono tiny">{s.txn_limit ?? <span className="muted">—</span>}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
      </PeopleCard>

      <PeopleCard title="Beneficial owners" subtitle="Persons holding ≥25% interest">
        {/* Beneficial owners live as a flag on officials in this
            schema — anyone with is_beneficial_owner is one. */}
        {(() => {
          const owners = o.officials.filter((p: ApiOfficial) => p.is_beneficial_owner);
          if (owners.length === 0) return <div className="empty">No beneficial owners declared.</div>;
          return (
            <table className="tbl">
              <thead><tr><th>Name</th><th>Role</th><th>Phone</th></tr></thead>
              <tbody>
                {owners.map((p: ApiOfficial) => (
                  <tr key={p.id}>
                    <td>{p.full_name}</td>
                    <td className="mono tiny">{p.position}</td>
                    <td className="mono tiny">{p.phone || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          );
        })()}
      </PeopleCard>
    </>
  );
}

function BankingTab({ entity }: { entity: Entity }) {
  if (entity.kind === 'institutional' && entity.o.banking) {
    const b = entity.o.banking;
    return (
      <div className="card">
        <div className="card-hd">
          <h3>Banking</h3>
          <span className="card-sub">Disbursement + collection account.</span>
        </div>
        <div className="card-body">
          <KVS>
            <Row k="Account name" v={b.account_name} />
            <Row k="Account number" v={b.account_number} mono />
            <Row k="Bank" v={b.bank_name} />
            <Row k="Branch" v={b.bank_branch || '—'} />
            <Row k="Bank code" v={b.bank_code || '—'} mono />
            <Row k="Checkoff arrangement" v={b.checkoff_arrangement || '—'} />
          </KVS>
        </div>
      </div>
    );
  }
  if (entity.kind === 'institutional') {
    return <div className="empty" style={{ padding: 24 }}>No banking details on file. Use Edit on the org page to add one.</div>;
  }
  return (
    <div className="card pending-card">
      <div className="card-hd">
        <h3>Banking</h3>
        <span className="card-sub">Member bank accounts for payouts.</span>
        <div className="card-hd-actions"><span className="muted tiny">Coming soon</span></div>
      </div>
      <div className="card-body">
        <p className="muted tiny" style={{ margin: 0 }}>
          Individual members' linked bank accounts for loan disbursements and dividend
          payouts will appear here once the member-banking workflow ships.
        </p>
      </div>
    </div>
  );
}

function DocumentsTab({ entity }: { entity: Entity }) {
  if (entity.kind === 'individual') {
    const docs = entity.m.documents ?? [];
    return (
      <div className="card">
        <div className="card-hd">
          <h3>Documents &amp; KYC</h3>
          <span className="card-sub">{docs.length} on file</span>
        </div>
        <div className="card-body flush">
          {docs.length === 0
            ? <div className="empty">No documents uploaded yet.</div>
            : (
              <table className="tbl">
                <thead><tr><th>Kind</th><th>Uploaded</th><th>Size</th></tr></thead>
                <tbody>
                  {docs.map((d) => (
                    <tr key={d.id}>
                      <td className="mono tiny">{d.kind.replace('_', ' ')}</td>
                      <td className="tiny-mono">{new Date(d.uploaded_at).toISOString().slice(0, 10)}</td>
                      <td className="mono tiny">{Math.round(d.size_bytes / 1024)} KB</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
        </div>
      </div>
    );
  }
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Documents &amp; KYC</h3>
        <span className="card-sub">{entity.o.documents.length} on file · org-specific KYC set</span>
      </div>
      <div className="card-body flush">
        {entity.o.documents.length === 0
          ? <div className="empty">No documents uploaded yet.</div>
          : (
            <table className="tbl">
              <thead><tr><th>Kind</th><th>Issued</th><th>Verification</th><th>Uploaded</th></tr></thead>
              <tbody>
                {entity.o.documents.map((d) => (
                  <tr key={d.id}>
                    <td className="mono tiny">{d.kind.replace(/_/g, ' ')}</td>
                    <td className="tiny-mono">{d.issue_date || '—'}</td>
                    <td><Badge tone={d.verification === 'verified' ? 'pos' : d.verification === 'rejected' ? 'neg' : 'warn'}>{d.verification}</Badge></td>
                    <td className="tiny-mono">{new Date(d.uploaded_at).toISOString().slice(0, 10)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
      </div>
    </div>
  );
}

function ActivityTab({ entity, canSeeAudit }: { entity: Entity; canSeeAudit: boolean }) {
  return (
    <div className="card">
      <div className="card-hd">
        <h3>System events</h3>
        <span className="card-sub">Audit log entries for this counterparty.</span>
      </div>
      <div className="card-body">
        {canSeeAudit
          ? <AuditTimeline entity={entity} limit={100} />
          : <span className="muted tiny">You don't have audit:view permission.</span>}
      </div>
    </div>
  );
}

// ─────────── Shared helpers ───────────

function AuditTimeline({ entity, limit }: { entity: Entity; limit: number }) {
  const target = entity.kind === 'individual' ? 'member' : 'org_member';
  const id = entity.kind === 'individual' ? entity.m.id : entity.o.id;
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    listAuditForTarget(target as 'member', id, limit)
      .then((es) => { if (!cancelled) setEntries(es); })
      .catch((e) => { if (!cancelled) setErr(extractError(e)); });
    return () => { cancelled = true; };
  }, [target, id, limit]);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!entries) return <span className="muted tiny" role="status">Loading activity…</span>;
  if (entries.length === 0) return <div className="empty" style={{ padding: 20 }}>No audit events yet.</div>;
  return (
    <ol className="tl" style={{ listStyle: 'none', margin: 0 }}>
      {entries.map((e) => (
        <li key={e.id} className="tl-item">
          <div className="tl-action">{e.action.replace(/_/g, ' ')}</div>
          <div className="tl-meta">
            <time>{new Date(e.created_at).toISOString().replace('T', ' ').slice(0, 19)}</time>
          </div>
        </li>
      ))}
    </ol>
  );
}

function ProfileCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="card">
      <div className="card-hd"><h3>{title}</h3></div>
      <div className="card-body">{children}</div>
    </div>
  );
}

function PeopleCard({ title, subtitle, children }: { title: string; subtitle?: string; children: ReactNode }) {
  return (
    <div className="card" style={{ marginBottom: 14 }}>
      <div className="card-hd">
        <h3>{title}</h3>
        {subtitle && <span className="card-sub">{subtitle}</span>}
      </div>
      <div className="card-body flush">{children}</div>
    </div>
  );
}

function KVS({ children }: { children: ReactNode }) {
  return <dl className="kvs">{children}</dl>;
}

function Row({ k, v, mono }: { k: string; v: ReactNode; mono?: boolean }) {
  return (
    <>
      <dt>{k}</dt>
      <dd className={mono ? 'mono' : undefined}>{v}</dd>
    </>
  );
}

function RelationView({ r }: { r: ApiRelation }) {
  return (
    <KVS>
      <Row k="Name" v={r.full_name} />
      <Row k="Relationship" v={r.relationship || '—'} />
      <Row k="Phone" v={r.phone || '—'} mono />
      <Row k="Email" v={r.email || '—'} />
      <Row k="ID #" v={r.id_doc_number || '—'} mono />
    </KVS>
  );
}
