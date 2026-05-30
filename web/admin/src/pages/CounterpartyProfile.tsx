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
  uploadMemberDocument,
  verifyMemberDocument,
  deleteMemberDocument,
  memberDocumentURL,
  fetchMemberDocument,
  uploadOrgDocument,
  verifyOrgDocument,
  deleteOrgDocument,
  fetchOrgDocument,
  DOC_KIND_LABELS,
  KYC_REQUIRED_INDIVIDUAL,
  KYC_REQUIRED_INSTITUTION,
  type ApiDocument,
  type ApiMemberDetail,
  type ApiOrg,
  type ApiOrgDocument,
  type ApiOrgDetail,
  type ApiRelation,
  type ApiOfficial,
  type ApiSignatory,
  type AuditEntry,
  type DocVerification,
  type DocumentKind,
  type OrgDocKind,
  // Phase 1.5b — Pledges given tab data.
  getPledgesGivenByCounterparty,
  type PledgeGivenRow,
} from '../api/client';
import StatementsTab from '../components/Members/StatementsTab';
import StandingOrdersTab from '../components/Members/StandingOrdersTab';
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
import { useDocumentTitle } from '../lib/useDocumentTitle';
import { FeesSummaryView } from './Accounting/FeesSummary';

// One tab strip — spec-defined, identical for both kinds.
type TabId = 'overview' | 'profile' | 'accounts' | 'people' | 'banking' | 'documents' | 'activity' | 'fees' | 'pledges' | 'statements' | 'standing-orders';
const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',  label: 'Overview' },
  { id: 'profile',   label: 'Profile' },
  { id: 'accounts',  label: 'Accounts' },   // Loans sub-section inside
  { id: 'people',    label: 'People' },
  { id: 'banking',   label: 'Banking' },
  { id: 'documents', label: 'Documents & KYC' },
  { id: 'fees',      label: 'Fees' },       // per-member Fees & Collections
  { id: 'activity',  label: 'Activity' },
  { id: 'pledges',   label: 'Pledges given' },  // Phase 1.5b — third-party + self collateral
  { id: 'statements', label: 'Statements' },    // DSID Phase 2.1 — deposit/share/interest/dividend PDFs
  { id: 'standing-orders', label: 'Standing orders' }, // DSID Phase 2.2
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
  useDocumentTitle(displayName);

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
          ariaLabel="Member sections"
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
              {activeId === 'fees'      && <FeesTab entity={entity} />}
              {activeId === 'activity'  && <ActivityTab entity={entity} canSeeAudit={canSeeAudit} />}
              {activeId === 'pledges'   && <PledgesGivenTab entity={entity} currency={currency} />}
              {activeId === 'statements' && (
                <StatementsTab
                  counterpartyId={entity.kind === 'individual'
                    ? (entity.m.counterparty_id ?? entity.m.id)
                    : (entity.o.counterparty_id ?? entity.o.id)}
                  memberEmail={entity.kind === 'individual' ? (entity.m.email ?? undefined) : undefined}
                />
              )}
              {activeId === 'standing-orders' && (
                <StandingOrdersTab
                  counterpartyId={entity.kind === 'individual'
                    ? (entity.m.counterparty_id ?? entity.m.id)
                    : (entity.o.counterparty_id ?? entity.o.id)}
                />
              )}
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
      <MemberStatusCard counterpartyId={m.counterparty_id ?? m.id} currentStatus={m.status} onChanged={async () => { window.location.reload(); }} />
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
    // Post-Phase D sub-PR 3: shares + deposit + loan history hang off
    // counterparties.id. counterparty_id is always set on individual
    // members post-backfill (member migration 0008); render an error
    // panel if it's somehow missing to surface the broken state
    // rather than silently passing the wrong id.
    if (!entity.m.counterparty_id) {
      return (
        <div className="card pending-card">
          <div className="card-hd"><h3>Accounts</h3></div>
          <div className="card-body">
            <div className="empty">
              This member has no counterparty bridge — re-run the Phase A
              backfill or contact engineering.
            </div>
          </div>
        </div>
      );
    }
    return (
      <>
        <MemberAccountsSummary counterpartyId={entity.m.counterparty_id} currency={currency} />
        <MemberAccountsPanel counterpartyId={entity.m.counterparty_id} currency={currency} />
        <MemberLedgerPanel counterpartyId={entity.m.counterparty_id} currency={currency} />
      </>
    );
  }
  // Phase E B: institutional accounts go through the same panels —
  // backend savings handlers were unified to read a kind-aware
  // CounterpartyView, so the panels render shares + deposits + loan
  // history for orgs the same way they do for individuals.
  // Authorization/signatory model stays per-org (signatories tab on
  // the org profile), but plain views + officer-initiated postings
  // work.
  if (!entity.o.counterparty_id) {
    return (
      <div className="card pending-card">
        <div className="card-hd"><h3>Accounts</h3></div>
        <div className="card-body">
          <div className="empty">
            This organisation has no counterparty bridge — re-run the Phase A
            backfill or contact engineering.
          </div>
        </div>
      </div>
    );
  }
  return (
    <>
      <MemberAccountsSummary counterpartyId={entity.o.counterparty_id} currency={currency} />
      <MemberAccountsPanel counterpartyId={entity.o.counterparty_id} currency={currency} />
      <MemberLedgerPanel counterpartyId={entity.o.counterparty_id} currency={currency} />
    </>
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

// DocumentsTab is the canonical KYC workstation for a counterparty.
// Same component for individual + institutional; the kind decides
// which API helpers + enum + required-doc checklist to use.
function DocumentsTab({ entity }: { entity: Entity }) {
  return entity.kind === 'individual'
    ? <DocsWorkstationIndividual m={entity.m} />
    : <DocsWorkstationInstitution o={entity.o} />;
}

function DocsWorkstationIndividual({ m }: { m: ApiMemberDetail }) {
  const counterpartyId = m.counterparty_id ?? m.id;
  const [docs, setDocs] = useState<ApiDocument[]>(m.documents ?? []);
  return (
    <DocsWorkstation<DocumentKind, ApiDocument>
      title="Documents & KYC"
      docs={docs}
      setDocs={setDocs}
      reload={async () => {
        const fresh = await getMember(m.id);
        setDocs(fresh.documents ?? []);
      }}
      requiredKinds={KYC_REQUIRED_INDIVIDUAL}
      allKinds={INDIVIDUAL_KINDS}
      upload={(kind, file, opts) => uploadMemberDocument(counterpartyId, kind, file, opts)}
      verify={(kind, status, note) => verifyMemberDocument(counterpartyId, kind, status, note)}
      remove={(kind) => deleteMemberDocument(counterpartyId, kind)}
      previewURL={(kind) => memberDocumentURL(counterpartyId, kind)}
      fetchBlob={(kind) => fetchMemberDocument(counterpartyId, kind)}
    />
  );
}

function DocsWorkstationInstitution({ o }: { o: ApiOrgDetail }) {
  const [docs, setDocs] = useState<ApiOrgDocument[]>(o.documents ?? []);
  return (
    <DocsWorkstation<OrgDocKind, ApiOrgDocument>
      title="Documents & KYC"
      docs={docs}
      setDocs={setDocs}
      reload={async () => {
        const fresh = await getOrg(o.id);
        setDocs(fresh.documents ?? []);
      }}
      requiredKinds={KYC_REQUIRED_INSTITUTION}
      allKinds={ORG_KINDS}
      upload={(kind, file, opts) => uploadOrgDocument(o.id, kind, file, opts)}
      verify={(kind, status, note) => verifyOrgDocument(o.id, kind, status, note)}
      remove={(kind) => deleteOrgDocument(o.id, kind)}
      previewURL={() => ''} // org-side has no direct URL helper — preview always streams via blob
      fetchBlob={(kind) => fetchOrgDocument(o.id, kind)}
    />
  );
}

// ─────────── Workstation (kind-generic) ───────────

const INDIVIDUAL_KINDS: DocumentKind[] = [
  'id_front', 'id_back', 'passport_photo', 'kra_pin_certificate', 'signature',
  'proof_of_address', 'bank_statement', 'payslip', 'employment_letter',
  'business_permit', 'signed_application_form', 'next_of_kin_id',
  'death_certificate', 'exit_clearance', 'blacklist_directive', 'other',
];

const ORG_KINDS: OrgDocKind[] = [
  'registration_certificate', 'cr12', 'kra_pin_certificate', 'memorandum_articles',
  'constitution_bylaws', 'business_permit', 'tax_compliance_certificate', 'vat_certificate',
  'ngo_certificate', 'cooperative_certificate', 'proof_of_address', 'audited_financials',
  'bank_statement', 'board_resolution', 'signatory_appointment_resolution',
  'beneficial_ownership_declaration',
];

type DocLike = {
  id: string;
  mime: string;
  size_bytes: number;
  issue_date?: string;
  expiry_date?: string;
  verification: DocVerification;
  verified_at?: string;
  verification_note?: string;
  uploaded_at: string;
  uploaded_by?: string;
};

function DocsWorkstation<K extends string, D extends DocLike & { kind: K }>({
  title,
  docs,
  setDocs,
  reload,
  requiredKinds,
  allKinds,
  upload,
  verify,
  remove,
  previewURL,
  fetchBlob,
}: {
  title: string;
  docs: D[];
  setDocs: (d: D[]) => void;
  reload: () => Promise<void>;
  requiredKinds: K[];
  allKinds: K[];
  upload: (kind: K, file: File, opts: { issue_date?: string; expiry_date?: string }) => Promise<D>;
  verify: (kind: K, status: DocVerification, note?: string) => Promise<void>;
  remove: (kind: K) => Promise<void>;
  previewURL: (kind: K) => string;
  fetchBlob: (kind: K) => Promise<Blob>;
}) {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('members:edit');
  const [search, setSearch] = useState('');
  const [expiredOnly, setExpiredOnly] = useState(false);
  const [addModal, setAddModal] = useState<{ open: boolean; preselect?: K }>({ open: false });
  const [rejectModal, setRejectModal] = useState<{ kind: K } | null>(null);
  const [deleteModal, setDeleteModal] = useState<{ kind: K } | null>(null);
  const [preview, setPreview] = useState<{ kind: K; url: string; mime: string } | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);

  // KYC progress against the required-doc checklist for this kind.
  const required = requiredKinds;
  const presentRequired = required.filter((k) => docs.some((d) => d.kind === k));
  const verifiedRequired = required.filter((k) => docs.some((d) => d.kind === k && d.verification === 'verified'));
  const totalRequired = required.length;
  const pct = totalRequired === 0 ? 0 : Math.round((verifiedRequired.length / totalRequired) * 100);

  const today = new Date();
  const filtered = docs.filter((d) => {
    if (search && !DOC_KIND_LABELS[d.kind].toLowerCase().includes(search.toLowerCase())) return false;
    if (expiredOnly) {
      if (!d.expiry_date) return false;
      const exp = new Date(d.expiry_date);
      const daysOut = (exp.getTime() - today.getTime()) / (24 * 3600 * 1000);
      if (daysOut > 30) return false;
    }
    return true;
  });

  async function handleVerify(kind: K) {
    setActionErr(null);
    const prev = docs;
    setDocs(docs.map((d) => d.kind === kind ? { ...d, verification: 'verified' } : d));
    try {
      await verify(kind, 'verified');
      await reload();
    } catch (e) {
      setDocs(prev);
      setActionErr(extractError(e));
    }
  }

  async function handlePreview(kind: K, mime: string) {
    setActionErr(null);
    try {
      if (mime.startsWith('image/') && previewURL(kind)) {
        // Cheaper: cookie-auth'd <img src>. Falls back to blob when
        // the family-specific URL helper is empty (org side).
        setPreview({ kind, url: previewURL(kind), mime });
        return;
      }
      const blob = await fetchBlob(kind);
      const url = URL.createObjectURL(blob);
      setPreview({ kind, url, mime });
    } catch (e) {
      setActionErr(extractError(e));
    }
  }

  function closePreview() {
    if (preview?.url.startsWith('blob:')) URL.revokeObjectURL(preview.url);
    setPreview(null);
  }

  return (
    <>
      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
          <div style={{ flex: 1, minWidth: 220 }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
              <strong>KYC completion</strong>
              <span className="muted tiny">{verifiedRequired.length}/{totalRequired} verified · {presentRequired.length}/{totalRequired} uploaded</span>
            </div>
            <div style={{ background: 'var(--border)', borderRadius: 4, height: 8, overflow: 'hidden' }}>
              <div style={{
                width: `${pct}%`,
                height: '100%',
                background: pct === 100 ? 'var(--pos)' : pct >= 50 ? 'var(--accent)' : 'var(--warn)',
                transition: 'width .25s ease',
              }} />
            </div>
            <div className="muted tiny" style={{ marginTop: 6 }}>
              {required.filter((k) => !docs.some((d) => d.kind === k)).map((k) => (
                <Badge key={k} tone="warn">Missing: {DOC_KIND_LABELS[k]}</Badge>
              )).reduce<ReactNode[]>((acc, b, i) => (i === 0 ? [b] : [...acc, ' ', b]), [])}
              {required.every((k) => docs.some((d) => d.kind === k)) && (
                <span className="muted tiny">All required documents uploaded.</span>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>{title}</h3>
          <span className="card-sub">{docs.length} on file</span>
          <div className="card-hd-actions" style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input
              className="input"
              placeholder="Search by kind…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{ minWidth: 180 }}
            />
            <label className="muted tiny" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <input type="checkbox" checked={expiredOnly} onChange={(e) => setExpiredOnly(e.target.checked)} />
              Expiring / expired
            </label>
            {canEdit && (
              <button className="btn btn-accent" onClick={() => setAddModal({ open: true })}>
                <Icon name="plus" size={12} /> Add document
              </button>
            )}
          </div>
        </div>
        {actionErr && <div className="alert alert-error" style={{ margin: 12 }}>{actionErr}</div>}
        <div className="card-body" style={{ display: 'grid', gap: 10 }}>
          {filtered.length === 0 ? (
            <div className="empty">{docs.length === 0 ? 'No documents uploaded yet.' : 'No documents match the current filters.'}</div>
          ) : filtered.map((d) => (
            <DocCard
              key={d.id}
              doc={d}
              canEdit={canEdit}
              onPreview={() => void handlePreview(d.kind, d.mime)}
              onReplace={() => setAddModal({ open: true, preselect: d.kind })}
              onVerify={() => void handleVerify(d.kind)}
              onReject={() => setRejectModal({ kind: d.kind })}
              onDelete={() => setDeleteModal({ kind: d.kind })}
            />
          ))}
        </div>
      </div>

      {addModal.open && (
        <DocAddModal<K>
          kinds={allKinds}
          preselect={addModal.preselect}
          docs={docs}
          onClose={() => setAddModal({ open: false })}
          onSaved={async (kind, file, opts) => {
            await upload(kind, file, opts);
            await reload();
            setAddModal({ open: false });
          }}
        />
      )}

      {rejectModal && (
        <DocRejectModal
          label={DOC_KIND_LABELS[rejectModal.kind]}
          onClose={() => setRejectModal(null)}
          onSubmit={async (note) => {
            await verify(rejectModal.kind, 'rejected', note);
            await reload();
            setRejectModal(null);
          }}
        />
      )}

      {deleteModal && (
        <DocConfirmModal
          title="Delete document?"
          message={`Permanently delete "${DOC_KIND_LABELS[deleteModal.kind]}". This action is recorded in the audit log.`}
          confirmLabel="Delete"
          danger
          onClose={() => setDeleteModal(null)}
          onConfirm={async () => {
            await remove(deleteModal.kind);
            await reload();
            setDeleteModal(null);
          }}
        />
      )}

      {preview && <DocPreviewModal mime={preview.mime} url={preview.url} label={DOC_KIND_LABELS[preview.kind]} onClose={closePreview} />}
    </>
  );
}

function DocCard({
  doc, canEdit, onPreview, onReplace, onVerify, onReject, onDelete,
}: {
  doc: DocLike & { kind: string };
  canEdit: boolean;
  onPreview: () => void;
  onReplace: () => void;
  onVerify: () => void;
  onReject: () => void;
  onDelete: () => void;
}) {
  const expiry = doc.expiry_date ? new Date(doc.expiry_date) : null;
  const today = new Date();
  const daysOut = expiry ? Math.round((expiry.getTime() - today.getTime()) / (24 * 3600 * 1000)) : null;
  const expState: 'ok' | 'soon' | 'expired' | null = expiry
    ? (expiry < today ? 'expired' : daysOut !== null && daysOut <= 30 ? 'soon' : 'ok')
    : null;
  return (
    <div className="card" style={{ padding: 0, border: '1px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, padding: 12 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <strong>{DOC_KIND_LABELS[doc.kind as DocumentKind] ?? doc.kind}</strong>
            <Badge tone={doc.verification === 'verified' ? 'pos' : doc.verification === 'rejected' ? 'neg' : 'warn'}>
              {doc.verification}
            </Badge>
            {expState === 'expired' && <Badge tone="neg">Expired</Badge>}
            {expState === 'soon' && <Badge tone="warn">Expires in {daysOut}d</Badge>}
          </div>
          <div className="muted tiny" style={{ marginTop: 4 }}>
            {doc.mime} · {(doc.size_bytes / 1024).toFixed(1)} KB · uploaded {new Date(doc.uploaded_at).toISOString().slice(0, 10)}
          </div>
          {(doc.issue_date || doc.expiry_date) && (
            <div className="muted tiny" style={{ marginTop: 2 }}>
              {doc.issue_date && <>Issued {doc.issue_date}</>}
              {doc.issue_date && doc.expiry_date && ' · '}
              {doc.expiry_date && <>Expires {doc.expiry_date}</>}
            </div>
          )}
          {doc.verification_note && (
            <div className="muted tiny" style={{ marginTop: 4, fontStyle: 'italic' }}>
              Note: {doc.verification_note}
            </div>
          )}
        </div>
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          <button className="btn btn-sm btn-ghost" onClick={onPreview}>Preview</button>
          {canEdit && <button className="btn btn-sm" onClick={onReplace}>Replace</button>}
          {canEdit && doc.verification !== 'verified' && (
            <button className="btn btn-sm btn-accent" onClick={onVerify}>Verify</button>
          )}
          {canEdit && doc.verification !== 'rejected' && (
            <button className="btn btn-sm" onClick={onReject}>Reject</button>
          )}
          {canEdit && <button className="btn btn-sm btn-danger" onClick={onDelete}>Delete</button>}
        </div>
      </div>
    </div>
  );
}

function DocAddModal<K extends string>({
  kinds, preselect, docs, onClose, onSaved,
}: {
  kinds: K[];
  preselect?: K;
  docs: { kind: K }[];
  onClose: () => void;
  onSaved: (kind: K, file: File, opts: { issue_date?: string; expiry_date?: string }) => Promise<void>;
}) {
  const [kind, setKind] = useState<K>(preselect ?? kinds[0]);
  const [file, setFile] = useState<File | null>(null);
  const [issueDate, setIssueDate] = useState('');
  const [expiryDate, setExpiryDate] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const MAX = 10 * 1024 * 1024;
  function validate(): string | null {
    if (!file) return 'Pick a file first.';
    if (file.size > MAX) return 'File is larger than 10 MB.';
    if (issueDate && expiryDate && expiryDate < issueDate) return 'Expiry date must be on or after issue date.';
    return null;
  }
  // Keep the submit button enabled once a file is present so the
  // date-cross-check error has somewhere to surface (a disabled button
  // would silently swallow the click). The submit handler re-runs
  // validate() and parks the message in `err`.
  const disabled = !file;

  // Replacement warning when the chosen kind already has a doc on file
  // and isn't 'other' (which legitimately repeats).
  const existing = docs.find((d) => d.kind === kind);
  const replacing = existing && kind !== ('other' as K);

  return (
    <LocalModalShell
      title={preselect ? 'Replace document' : 'Add document'}
      busy={busy}
      submitLabel={preselect ? 'Replace' : 'Upload'}
      disabled={disabled}
      onClose={onClose}
      onSubmit={async () => {
        const v = validate();
        if (v) { setErr(v); return; }
        setErr(null); setBusy(true);
        try {
          await onSaved(kind, file!, {
            issue_date: issueDate || undefined,
            expiry_date: expiryDate || undefined,
          });
        } catch (e) {
          setErr(extractError(e));
        } finally { setBusy(false); }
      }}
    >
      {err && <div className="alert alert-error">{err}</div>}
      {replacing && (
        <div className="alert alert-warn">
          A document already exists for this kind. Uploading will replace it and reset verification to pending.
        </div>
      )}
      <Field label="Document kind">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value as K)} disabled={!!preselect}>
          {kinds.map((k) => (
            <option key={k} value={k}>{DOC_KIND_LABELS[k as DocumentKind] ?? k}</option>
          ))}
        </select>
      </Field>
      <Field label="File" hint="Max 10 MB. PDF, Word, or image.">
        <input type="file" className="input" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
      </Field>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <Field label="Issue date (optional)">
          <input type="date" className="input" value={issueDate} onChange={(e) => setIssueDate(e.target.value)} />
        </Field>
        <Field label="Expiry date (optional)">
          <input type="date" className="input" value={expiryDate} onChange={(e) => setExpiryDate(e.target.value)} />
        </Field>
      </div>
    </LocalModalShell>
  );
}

function DocRejectModal({ label, onClose, onSubmit }: {
  label: string;
  onClose: () => void;
  onSubmit: (note: string) => Promise<void>;
}) {
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <LocalModalShell
      title={`Reject ${label}`}
      busy={busy}
      submitLabel="Reject"
      disabled={!note.trim()}
      onClose={onClose}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try { await onSubmit(note.trim()); }
        catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}
    >
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Reason" hint="Required — shown to the member or onboarding officer.">
        <textarea
          className="input"
          rows={4}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="e.g. ID photo is blurry; please re-upload."
        />
      </Field>
    </LocalModalShell>
  );
}

function DocConfirmModal({
  title, message, confirmLabel, danger, onClose, onConfirm,
}: {
  title: string;
  message: string;
  confirmLabel: string;
  danger?: boolean;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <LocalModalShell
      title={title}
      busy={busy}
      submitLabel={confirmLabel}
      submitClassName={danger ? 'btn btn-danger' : undefined}
      onClose={onClose}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try { await onConfirm(); }
        catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}
    >
      {err && <div className="alert alert-error">{err}</div>}
      <p>{message}</p>
    </LocalModalShell>
  );
}

function DocPreviewModal({ mime, url, label, onClose }: { mime: string; url: string; label: string; onClose: () => void }) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.55)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 720, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{label}</h3>
          <div className="card-hd-actions">
            <a className="btn btn-sm" href={url} target="_blank" rel="noreferrer">Open in new tab</a>
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body" style={{ display: 'grid', placeItems: 'center' }}>
          {mime.startsWith('image/')
            ? <img src={url} alt={label} style={{ maxWidth: '100%', maxHeight: '70vh' }} />
            : mime === 'application/pdf'
              ? <iframe title={label} src={url} style={{ width: '100%', height: '70vh', border: 0 }} />
              : <p className="muted">Preview not available for this file type. Open in a new tab to view.</p>}
        </div>
      </div>
    </div>
  );
}

// LocalModalShell — DocumentsTab keeps its own copy so it doesn't pull
// from MemberAccountsPanel (which would force a non-essential import
// graph entanglement just to reuse 25 lines of layout).
function LocalModalShell({
  title, busy, onClose, children, submitLabel, onSubmit, disabled, submitClassName,
}: {
  title: string;
  busy?: boolean;
  onClose: () => void;
  children: ReactNode;
  submitLabel: string;
  onSubmit: () => void | Promise<void>;
  disabled?: boolean;
  submitClassName?: string;
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
          <button className={submitClassName ?? 'btn btn-accent'} disabled={busy || disabled} onClick={() => void onSubmit()}>
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

// FeesTab calls the same Fees & Collections endpoint as the standalone
// /accounting/fees-summary report, scoped to this counterparty. Defaults
// the window to the past 12 months because "all fees this member has
// ever paid" is the natural ask from the support desk.
//
// Receipts join on counterparties.id, NOT members.id. ApiMemberDetail
// has both — use the counterparty_id field. Falls back to .id only as
// defensive belt-and-suspenders for pre-migration data; today's
// receipts always carry the materialised counterparty_id.
function FeesTab({ entity }: { entity: Entity }) {
  const counterpartyID =
    entity.kind === 'individual'
      ? (entity.m.counterparty_id ?? entity.m.id)
      : (entity.o.counterparty_id ?? entity.o.id);
  const today = new Date().toISOString().slice(0, 10);
  const yearAgo = new Date(Date.now() - 365 * 24 * 3600 * 1000).toISOString().slice(0, 10);
  const [from, setFrom] = useState(yearAgo);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<import('../api/client').FeesSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try {
      const { feesSummary } = await import('../api/client');
      setData(await feesSummary({ from, to, counterparty_id: counterpartyID }));
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'failed to load');
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [counterpartyID]);

  return (
    <>
      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">From</div>
            <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">To</div>
            <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Refresh'}
          </button>
        </div>
      </div>
      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {data && <FeesSummaryView data={data} />}
    </>
  );
}

// ─────────── Shared helpers ───────────

// AUDIT_LABELS maps the canonical action strings (the same strings the
// Go handlers emit via h.audit) to UI-friendly verbs. Anything not in
// the map falls back to a derived label (replace _ with space). Add
// new entries here when wiring a new audit-emitting endpoint.
const AUDIT_LABELS: Record<string, string> = {
  // documents (PR: Documents & KYC workstation)
  'member.document_uploaded': 'Member document uploaded',
  'member.document_verified': 'Member document verified',
  'member.document_removed': 'Member document deleted',
  'org.document_uploaded': 'Org document uploaded',
  'org.document_verified': 'Org document verified',
  'org.document_removed': 'Org document deleted',
  // M-PESA (phases 2-4)
  'mpesa.inbound_received':            'M-PESA payment received',
  'mpesa.distribution_run.started':    'M-PESA distribution started',
  'mpesa.distribution_run.split':      'M-PESA split posted',
  'mpesa.distribution_run.completed':  'M-PESA distribution completed',
  'mpesa.distribution_run.failed':     'M-PESA distribution failed',
  'mpesa.b2c.enqueued':                'M-PESA B2C queued',
  'mpesa.b2c.sent':                    'M-PESA B2C sent to Daraja',
  'mpesa.b2c.result':                  'M-PESA B2C result received',
  'mpesa.b2c.reversed':                'M-PESA B2C reversed',
};

function labelForAuditAction(action: string): string {
  return AUDIT_LABELS[action] ?? action.replace(/_/g, ' ');
}

function AuditTimeline({ entity, limit }: { entity: Entity; limit: number }) {
  const target = entity.kind === 'individual' ? 'member' : 'org_member';
  const id = entity.kind === 'individual' ? entity.m.id : entity.o.id;
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [mpesaPanel, setMpesaPanel] = useState<AuditEntry | null>(null);
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
    <>
      <ol className="tl" style={{ listStyle: 'none', margin: 0 }}>
        {entries.map((e) => (
          <li key={e.id} className="tl-item">
            <div className="tl-action" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span>{labelForAuditAction(e.action)}</span>
              {isMpesaAction(e.action) && (
                <button
                  className="btn btn-sm btn-ghost"
                  title="View M-PESA receipt details"
                  onClick={() => setMpesaPanel(e)}
                  style={{ padding: '2px 6px', fontSize: 11 }}
                >
                  <Badge tone="accent">M-PESA</Badge>
                </button>
              )}
            </div>
            <div className="tl-meta">
              <time>{new Date(e.created_at).toISOString().replace('T', ' ').slice(0, 19)}</time>
            </div>
          </li>
        ))}
      </ol>
      {mpesaPanel && (
        <MpesaActivityPanel entry={mpesaPanel} onClose={() => setMpesaPanel(null)} />
      )}
    </>
  );
}

// isMpesaAction tags audit rows that the M-PESA chip should be
// rendered against. The audit-writer emits these action keys from
// phases 2-4 (inbound received + distribution started/split/
// completed/failed + outbound sent/result).
function isMpesaAction(action: string): boolean {
  return action.startsWith('mpesa.');
}

// MpesaActivityPanel — side panel surfaced when an operator taps the
// "M-PESA" chip on an audit row. Shows whatever's in the audit
// metadata (split breakdown, Safaricom receipt, transaction id) plus
// a link to the raw event on the reconciliation page.
function MpesaActivityPanel({ entry, onClose }: { entry: AuditEntry; onClose: () => void }) {
  const meta = (entry.metadata ?? {}) as Record<string, unknown>;
  const eventID = typeof meta.event_id === 'string' ? meta.event_id : undefined;
  const receipt = typeof meta.trans_id === 'string' ? meta.trans_id : undefined;
  const amount  = typeof meta.amount   === 'string' ? meta.amount   : undefined;
  const leg     = typeof meta.leg      === 'string' ? meta.leg      : undefined;
  const via     = typeof meta.resolved_via === 'string' ? meta.resolved_via : undefined;
  return (
    <div
      role="dialog"
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,.40)', display: 'flex', justifyContent: 'flex-end',
      }}
      onClick={onClose}
    >
      <aside
        className="card"
        style={{
          width: 420, maxWidth: '92vw', height: '100%',
          borderRadius: 0, overflow: 'auto', display: 'flex', flexDirection: 'column',
        }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="card-hd">
          <h3>M-PESA · {labelForAuditAction(entry.action)}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body" style={{ display: 'grid', gap: 10 }}>
          <KVS>
            <Row k="Audit action" v={<code className="mono tiny">{entry.action}</code>} />
            <Row k="When" v={new Date(entry.created_at).toISOString().replace('T', ' ').slice(0, 19)} mono />
            {receipt && <Row k="Safaricom receipt" v={receipt} mono />}
            {amount  && <Row k="Amount"            v={amount}  mono />}
            {leg     && <Row k="Distribution leg"  v={<Badge tone="neutral">{leg}</Badge>} />}
            {via     && <Row k="Resolved via"      v={<Badge tone={via === 'unallocated' ? 'warn' : 'neutral'}>{via}</Badge>} />}
            {typeof meta.run_id === 'string' && <Row k="Run id" v={<code className="mono tiny">{meta.run_id}</code>} />}
            {typeof meta.target_ref === 'string' && <Row k="Target" v={<code className="mono tiny">{meta.target_ref}</code>} />}
          </KVS>
          {eventID && (
            <a className="btn btn-sm" href={`/accounting/mpesa-reconciliation?event=${eventID}`}>
              Open raw event in reconciliation →
            </a>
          )}
          <details>
            <summary className="muted tiny">Raw metadata</summary>
            <pre style={{
              fontSize: 11, background: 'var(--surface-subtle)',
              padding: 8, borderRadius: 'var(--r-sm)', overflow: 'auto',
            }}>{JSON.stringify(meta, null, 2)}</pre>
          </details>
        </div>
      </aside>
    </div>
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

// ─────────── Phase 1.5b — Pledges given tab ───────────

function PledgesGivenTab({ entity, currency }: { entity: Entity; currency: string }) {
  const counterpartyID = entity.kind === 'individual'
    ? (entity.m.counterparty_id ?? entity.m.id)
    : (entity.o.counterparty_id ?? entity.o.id);

  const [items, setItems] = useState<PledgeGivenRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void getPledgesGivenByCounterparty(counterpartyID)
      .then((r) => setItems(r.items))
      .catch((e: any) => setErr(e?.response?.data?.error?.message || e?.message || 'Failed to load.'));
  }, [counterpartyID]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (!items) return <div className="muted">Loading…</div>;
  if (items.length === 0) return <div className="empty">No collateral pledged by this member.</div>;

  const fmt = (s?: string) => {
    if (!s) return '—';
    const n = parseFloat(s);
    if (Number.isNaN(n)) return s;
    return n.toLocaleString('en-KE', { maximumFractionDigits: 2 });
  };

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Pledges given</h3>
        <span className="card-sub">{items.length} item{items.length === 1 ? '' : 's'}</span>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead>
            <tr>
              <th>Loan / app</th>
              <th>Borrower</th>
              <th>Kind</th>
              <th>Description</th>
              <th>Status</th>
              <th>Consent</th>
              <th className="num">FSV</th>
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.collateral_id}>
                <td className="mono">{r.loan_no ?? r.application_no}</td>
                <td>
                  {r.is_self_pledge
                    ? <span className="muted tiny">self-pledge</span>
                    : r.borrower_name}
                </td>
                <td>{r.kind.replace(/_/g, ' ')}</td>
                <td>{r.description}</td>
                <td className="tiny">{r.status}</td>
                <td className="tiny">{r.pledger_consent_status ?? '—'}</td>
                <td className="num mono">{r.forced_sale_value ? `${currency} ${fmt(r.forced_sale_value)}` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
