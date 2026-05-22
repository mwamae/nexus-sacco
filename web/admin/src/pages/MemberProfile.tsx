// Member 360 — single source of truth for everything about a member.
//
// Six tabs (Overview / Profile / People / Accounts / Documents & KYC /
// Activity). Tab state is mirrored to ?tab= so URLs are shareable. The
// layout collapses from 3-col → 2-col → 1-col across desktop / tablet /
// mobile via the breakpoints in styles.css.

import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  approveMember,
  fetchMemberDocument,
  getMember,
  getTenantSettings,
  listAuditForTarget,
  rejectMember,
  setMemberStatus,
  uploadMemberDocument,
  extractError,
  getDepositAccountsByMember,
  getMemberLoanHistory,
  getShareAccountByMember,
  listGuaranteesByMember,
  type ApiMemberDetail,
  type ApiRelation,
  type AuditEntry,
  type DocumentKind,
  type MemberLoanHistory,
  type MemberStatus,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { MemberStatusCard } from '../components/MemberStatusCard';
import { MemberAccountsPanel } from '../components/MemberAccountsPanel';
import { MemberLedgerPanel } from '../components/MemberLedgerPanel';
import { Icon, type IconName } from '../components/Icon';
import { AsyncPanel, isTimeoutError, useAsyncPanel } from '../components/AsyncPanel';
import { Tabs } from '../components/Tabs';
import { usePageCrumb } from '../lib/pageCrumb';
import { CLASS_TONE } from './Loans';

const DOC_LABELS: Record<DocumentKind, string> = {
  signature: 'Signature',
  passport_photo: 'Passport photo',
  id_front: 'ID front',
  id_back: 'ID back',
};
const DOC_ACCEPT: Record<DocumentKind, string> = {
  signature: 'image/png,image/jpeg,image/svg+xml',
  passport_photo: 'image/png,image/jpeg,image/webp',
  id_front: 'image/png,image/jpeg,image/webp',
  id_back: 'image/png,image/jpeg,image/webp',
};

type TabId = 'overview' | 'profile' | 'people' | 'accounts' | 'loans' | 'documents' | 'activity';
const TABS: { id: TabId; label: string }[] = [
  { id: 'overview',  label: 'Overview' },
  { id: 'profile',   label: 'Profile' },
  { id: 'people',    label: 'People' },
  { id: 'accounts',  label: 'Accounts' },
  { id: 'loans',     label: 'Loans' },
  { id: 'documents', label: 'Documents & KYC' },
  { id: 'activity',  label: 'Activity' },
];

export default function MemberProfile() {
  const memberId = useMemo(() => extractIdFromPath(window.location.pathname), []);
  const initialTab = useMemo<TabId>(() => {
    const t = new URLSearchParams(window.location.search).get('tab') as TabId | null;
    return (t && TABS.some((x) => x.id === t)) ? t : 'overview';
  }, []);
  const { hasPermission, tenant } = useAuth();
  const canApprove = hasPermission('members:approve');
  const canEdit = hasPermission('members:edit');
  const canUpload = hasPermission('members:create');
  const canSeeAudit = hasPermission('audit:view');

  const [tab, setTab] = useState<TabId>(initialTab);
  const [m, setM] = useState<ApiMemberDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [currency, setCurrency] = useState<string>(tenant?.currency_code ?? 'KES');
  // Make the header crumb say "Members → Jane Doe" instead of the
  // registry fallback ("Members → Profile") once the member loads.
  usePageCrumb(m?.full_name);

  function navigateTab(next: TabId) {
    setTab(next);
    const url = new URL(window.location.href);
    url.searchParams.set('tab', next);
    window.history.replaceState({}, '', url);
  }

  async function reload() {
    if (!memberId) return;
    setErr(null);
    try { setM(await getMember(memberId)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [memberId]);

  // Pick up tenant currency from full settings (more reliable than auth context for
  // freshly-onboarded staff who haven't reloaded).
  useEffect(() => {
    if (currency && currency !== 'KES') return;
    void getTenantSettings().then((s) => setCurrency(s.tenant.currency_code)).catch(() => {});
  }, []);

  if (!memberId) {
    return <div className="page"><div className="alert alert-error">Missing member id in URL.</div></div>;
  }

  // ─────────── actions ───────────

  async function onApprove() {
    if (!m) return;
    if (!confirm(`Approve ${m.full_name} (${m.member_no})?`)) return;
    setBusy('approve');
    try { await approveMember(m.id); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
  async function onReject() {
    if (!m) return;
    const reason = prompt(`Reject ${m.full_name}. Reason?`);
    if (!reason) return;
    setBusy('reject');
    try { await rejectMember(m.id, reason); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }
  async function onStatus(next: 'active' | 'suspended' | 'closed') {
    if (!m) return;
    const labels = { active: 'reactivate', suspended: 'suspend', closed: 'close' };
    if (!confirm(`${labels[next]} ${m.full_name}?`)) return;
    setBusy(next);
    try { await setMemberStatus(m.id, next); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/members" style={{ color: 'var(--accent)' }}>← Members</a>
          </div>
          <h1 style={{ marginBottom: 4 }}>{m ? m.full_name : 'Loading…'}</h1>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {!m && !err && <div className="empty">Loading…</div>}

      {m && (
        <>
          <HeaderCard
            m={m}
            busy={busy}
            canApprove={canApprove}
            canEdit={canEdit}
            onApprove={onApprove}
            onReject={onReject}
            onStatus={onStatus}
          />

          <div className="card" style={{ padding: 0 }}>
            <Tabs
              ariaLabel="Member sections"
              tabs={TABS}
              value={tab}
              onChange={navigateTab}
            >
              {(activeId) => (
                <>
                  {activeId === 'overview' && (
                    <OverviewTab m={m} currency={currency} canSeeAudit={canSeeAudit} onJump={navigateTab} />
                  )}
                  {activeId === 'profile' && (
                    <>
                      <ProfileTab m={m} />
                      <MemberStatusCard memberId={m.id} currentStatus={m.status} onChanged={reload} />
                    </>
                  )}
                  {activeId === 'people'   && <PeopleTab m={m} currency={currency} />}
                  {activeId === 'accounts' && <AccountsTab currency={currency} memberId={memberId} />}
                  {activeId === 'loans'    && <LoansTab memberId={memberId} />}
                  {activeId === 'documents' && (
                    <DocumentsTab m={m} canUpload={canUpload} onReplaced={reload} />
                  )}
                  {activeId === 'activity' && <ActivityTab memberId={m.id} canSeeAudit={canSeeAudit} />}
                </>
              )}
            </Tabs>
          </div>
        </>
      )}
    </div>
  );
}

// ─────────── Header card ───────────

function HeaderCard({
  m, busy, canApprove, canEdit, onApprove, onReject, onStatus,
}: {
  m: ApiMemberDetail; busy: string | null;
  canApprove: boolean; canEdit: boolean;
  onApprove: () => void; onReject: () => void;
  onStatus: (s: 'active' | 'suspended' | 'closed') => void;
}) {
  const kyc = computeKYC(m);
  return (
    <div className="card" style={{ marginBottom: 14 }}>
      <div className="m360-hd">
        <PhotoTile member={m} />
        <div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>{m.full_name}</div>
          <div className="m360-hd-meta">
            <span className="mono">{m.member_no}</span>
            <StatusBadge status={m.status} />
            {m.gender !== 'undisclosed' && <Badge tone="neutral">{m.gender}</Badge>}
            <span className="muted">·</span>
            <span className="tiny-mono">{m.id_doc_kind.replace('_', ' ')} {m.id_doc_number}</span>
            {m.kra_pin && (<>
              <span className="muted">·</span>
              <span className="tiny-mono">KRA {m.kra_pin}</span>
            </>)}
            {m.phone && (<>
              <span className="muted">·</span>
              <span className="tiny-mono">{m.phone}</span>
            </>)}
            {m.email && (<>
              <span className="muted">·</span>
              <span className="tiny-mono">{m.email}</span>
            </>)}
          </div>
          <div className="muted tiny" style={{ marginTop: 6 }}>
            Joined {new Date(m.created_at).toISOString().slice(0, 10)} · KYC {kyc.percent}% complete
          </div>
        </div>
        <div className="m360-hd-actions">
          {m.status === 'pending' && canApprove && (
            <>
              <button className="btn btn-sm" style={{ color: 'var(--pos)' }} disabled={busy != null} onClick={onApprove}>
                <Icon name="check" size={12} /> {busy === 'approve' ? 'Approving…' : 'Approve'}
              </button>
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy != null} onClick={onReject}>
                <Icon name="x" size={12} /> Reject
              </button>
            </>
          )}
          {m.status === 'active' && canEdit && (
            <>
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy != null} onClick={() => onStatus('suspended')}>
                <Icon name="lock" size={12} /> Suspend
              </button>
              <button className="btn btn-sm" disabled={busy != null} onClick={() => onStatus('closed')}>
                <Icon name="x" size={12} /> Close
              </button>
            </>
          )}
          {m.status === 'suspended' && canEdit && (
            <button className="btn btn-sm" style={{ color: 'var(--pos)' }} disabled={busy != null} onClick={() => onStatus('active')}>
              <Icon name="check" size={12} /> Reactivate
            </button>
          )}
          <a className="btn btn-sm" href={`/members/${m.id}/statement`}>
            <Icon name="arrow_dn" size={12} /> Statement
          </a>
        </div>
      </div>

      {m.status === 'rejected' && m.rejection_reason && (
        <div className="alert alert-warn" style={{ margin: '0 14px 14px' }}>
          <strong>Rejected:</strong> {m.rejection_reason}
        </div>
      )}
    </div>
  );
}

function PhotoTile({ member }: { member: ApiMemberDetail }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const hasPhoto = (member.documents ?? []).some((d) => d.kind === 'passport_photo');
    if (!hasPhoto) { setUrl(null); return; }
    let revoked = false;
    let objectUrl: string | null = null;
    fetchMemberDocument(member.id, 'passport_photo')
      .then((blob) => {
        if (revoked) return;
        objectUrl = URL.createObjectURL(blob);
        setUrl(objectUrl);
      })
      .catch(() => setUrl(null));
    return () => { revoked = true; if (objectUrl) URL.revokeObjectURL(objectUrl); };
  }, [member.id, (member.documents ?? []).find((d) => d.kind === 'passport_photo')?.uploaded_at]);

  return (
    <div className="m360-hd-photo">
      {url ? <img src={url} alt={member.full_name} /> : <Avatar name={member.full_name} size="xl" />}
    </div>
  );
}

// ─────────── Overview ───────────

function OverviewTab({
  m, currency, canSeeAudit, onJump,
}: {
  m: ApiMemberDetail;
  currency: string;
  canSeeAudit: boolean;
  onJump: (t: TabId) => void;
}) {
  const kyc = computeKYC(m);
  const risk = computeRisk(m, kyc.percent);

  return (
    <>
      <FinancialKPIStrip memberId={m.id} currency={currency} onJump={onJump} />



      <div className="grid-2">
        {/* KYC summary */}
        <div className="card">
          <div className="card-hd">
            <h3>KYC status</h3>
            <span className="card-sub">{kyc.percent}% complete · {kyc.completed}/{kyc.total} items</span>
            <div className="card-hd-actions">
              <button className="btn btn-sm btn-ghost" onClick={() => onJump('documents')}>
                Full checklist <Icon name="chevron_r" size={12} />
              </button>
            </div>
          </div>
          <div className="card-body">
            <div className="risk-bar" style={{ marginBottom: 10 }}>
              <div
                className="risk-bar-fill"
                style={{
                  width: `${kyc.percent}%`,
                  background: kyc.percent >= 80 ? 'var(--pos)' : kyc.percent >= 50 ? 'var(--warn)' : 'var(--neg)',
                }}
              />
            </div>
            <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12.5 }}>
              {kyc.items.slice(0, 5).map((it) => (
                <li key={it.label} style={{ color: it.done ? 'var(--fg)' : 'var(--fg-3)', lineHeight: 1.7 }}>
                  {it.done ? '✓' : '○'} {it.label}
                </li>
              ))}
              {kyc.items.length > 5 && (
                <li className="muted">+{kyc.items.length - 5} more — see Documents &amp; KYC tab</li>
              )}
            </ul>
          </div>
        </div>

        {/* Risk profile */}
        <div className="card">
          <div className="card-hd">
            <h3>Risk profile</h3>
            <span className="card-sub">heuristic score · {risk.tier}</span>
          </div>
          <div className="card-body">
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 8 }}>
              <span className="mono" style={{ fontSize: 28, fontWeight: 500, color: risk.color }}>{risk.score}</span>
              <span className="muted tiny">/ 100</span>
            </div>
            <div className="risk-bar" style={{ marginBottom: 10 }}>
              <div className="risk-bar-fill" style={{ width: `${risk.score}%`, background: risk.color }} />
            </div>
            <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12, color: 'var(--fg-3)' }}>
              {risk.factors.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
            <p className="muted tiny" style={{ marginTop: 8 }}>
              Heuristic only — a real credit risk model lands with the loans module.
            </p>
          </div>
        </div>
      </div>

      {/* Recent activity preview */}
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
          {canSeeAudit
            ? <AuditTimeline memberId={m.id} limit={5} />
            : <span className="muted tiny">You don't have audit:view permission.</span>}
        </div>
      </div>
    </>
  );
}

function FinKPI({ label, value, hint, sub, muted, onClick }: {
  label: string;
  value: string;
  hint?: string;
  sub?: string;
  muted?: boolean;
  onClick?: () => void;
}) {
  return (
    <div className="card" onClick={onClick} style={onClick ? { cursor: 'pointer' } : undefined}>
      <div className="m360-stat">
        <span className="m360-stat-label">{label}</span>
        <span className="m360-stat-value" style={muted ? { color: 'var(--fg-3)' } : undefined}>{value}</span>
        {sub && <span className="m360-stat-sub" style={{ fontWeight: 500 }}>{sub}</span>}
        {hint && <span className="m360-stat-sub">{hint}</span>}
      </div>
    </div>
  );
}

// ─────────── Financial KPI strip ───────────
//
// Aggregates the member's headline financial position from three
// existing endpoints — deposits, loans, shares. Loaded in parallel
// inside the AsyncPanel hook (10s timeout, retry on failure). Each
// tile clicks through to its source module's tab inside this page so
// the user can drill in without leaving the member context.
//
// Bucket semantics:
//   TOTAL SAVINGS  = sum of current_balance on every ACTIVE deposit account
//   ACTIVE LOANS   = count of loans in an open status, with outstanding
//                    principal as the sub-label
//   SHARES BALANCE = share_account.total_value (shares_held × par_value)
//   NET POSITION   = savings + shares − active-loan outstanding principal
//
// An empty bucket renders "KES 0.00", never a dash. The old "module
// pending" copy is gone for good.

type FinancialAggregate = {
  totalSavings: number;
  activeLoanCount: number;
  activeLoanOutstanding: number;
  sharesBalance: number;
  netPosition: number;
};

// Loan statuses that count toward "active" — every other status is
// terminal (settled / closed / written_off / defaulted). Kept inline
// rather than imported from Loans.tsx so the Overview tab doesn't
// pull in Loans.tsx's heavy module dependencies at first render.
const ACTIVE_LOAN_STATUSES = new Set([
  'active', 'in_arrears', 'restructured', 'pending_disbursement',
]);

async function aggregateFinancialPosition(memberId: string): Promise<FinancialAggregate> {
  // Shares is allowed to 404 (the member may never have bought any).
  const [deposits, loans, share] = await Promise.all([
    getDepositAccountsByMember(memberId),
    getMemberLoanHistory(memberId),
    getShareAccountByMember(memberId).catch(() => null),
  ]);

  const totalSavings = deposits
    .filter((d) => d.account.status === 'active')
    .reduce((acc, d) => acc + (parseFloat(d.account.current_balance) || 0), 0);

  const activeLoans = loans.loans.filter((row) => ACTIVE_LOAN_STATUSES.has(row.loan.status));
  const activeLoanCount = activeLoans.length;
  const activeLoanOutstanding = activeLoans.reduce(
    (acc, row) => acc + (parseFloat(row.loan.principal_balance) || 0),
    0,
  );

  const sharesBalance = share ? (parseFloat(share.account.total_value) || 0) : 0;

  const netPosition = totalSavings + sharesBalance - activeLoanOutstanding;

  return { totalSavings, activeLoanCount, activeLoanOutstanding, sharesBalance, netPosition };
}

function fmtFinMoney(n: number): string {
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function FinancialKPIStrip({ memberId, currency, onJump }: {
  memberId: string;
  currency: string;
  onJump: (t: TabId) => void;
}) {
  const fetcher = useMemo(
    () => () => aggregateFinancialPosition(memberId),
    [memberId],
  );
  // Uses the standard AsyncPanel hook — same 10s default timeout that
  // every other panel honours.
  const { state, retry } = useAsyncPanel(fetcher, [memberId]);

  if (state.kind === 'loading') {
    return (
      <div className="grid-4" style={{ marginBottom: 14 }} aria-busy="true">
        <FinKPISkeleton label="Total savings"  />
        <FinKPISkeleton label="Active loans"   />
        <FinKPISkeleton label="Shares balance" />
        <FinKPISkeleton label="Net position"   />
      </div>
    );
  }
  if (state.kind === 'error') {
    const msg = isTimeoutError(state.error)
      ? 'The financial services took too long to respond. Try again.'
      : "We couldn't fetch this member's financial position.";
    return (
      <div className="alert alert-error" role="alert" style={{
        display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14,
      }}>
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 600, marginBottom: 2 }}>Couldn't load financial position</div>
          <div>{msg}</div>
        </div>
        <button className="btn btn-sm" onClick={retry}>Retry</button>
      </div>
    );
  }

  // 'data' and 'empty' both carry a value here — isEmpty wasn't set
  // because every member has a defined (possibly zero) financial
  // position. Treat both as data.
  const agg = (state.kind === 'data' || state.kind === 'empty')
    ? (state as { value?: FinancialAggregate }).value ?? {
        totalSavings: 0, activeLoanCount: 0, activeLoanOutstanding: 0,
        sharesBalance: 0, netPosition: 0,
      }
    : { totalSavings: 0, activeLoanCount: 0, activeLoanOutstanding: 0, sharesBalance: 0, netPosition: 0 };

  return (
    <div className="grid-4" style={{ marginBottom: 14 }}>
      <FinKPI
        label="Total savings"
        value={`${currency} ${fmtFinMoney(agg.totalSavings)}`}
        hint="Sum of active deposit account balances"
        onClick={() => onJump('accounts')}
      />
      <FinKPI
        label="Active loans"
        value={agg.activeLoanCount.toString()}
        sub={`${currency} ${fmtFinMoney(agg.activeLoanOutstanding)} outstanding`}
        hint={agg.activeLoanCount === 1 ? '1 open loan' : `${agg.activeLoanCount} open loans`}
        onClick={() => onJump('loans')}
      />
      <FinKPI
        label="Shares balance"
        value={`${currency} ${fmtFinMoney(agg.sharesBalance)}`}
        hint="Paid-up share equity"
        onClick={() => onJump('accounts')}
      />
      <FinKPI
        label="Net position"
        value={`${currency} ${fmtFinMoney(agg.netPosition)}`}
        hint="Savings + shares − outstanding loans"
        muted={agg.netPosition < 0}
      />
    </div>
  );
}

function FinKPISkeleton({ label }: { label: string }) {
  return (
    <div className="card">
      <div className="m360-stat">
        <span className="m360-stat-label">{label}</span>
        <span className="m360-stat-value" style={{ color: 'var(--fg-3)' }}>
          <span
            role="status"
            aria-label={`Loading ${label}`}
            style={{
              display: 'inline-block',
              width: 80, height: 22,
              background: 'var(--surface-2)',
              borderRadius: 3,
              verticalAlign: 'middle',
            }}
          />
        </span>
        <span className="m360-stat-sub muted">Loading…</span>
      </div>
    </div>
  );
}

// ─────────── Profile tab ───────────

function ProfileTab({ m }: { m: ApiMemberDetail }) {
  return (
    <div className="grid-2">
      <Card title="Identity">
        <KVS>
          <Row k="Full name" v={m.full_name} />
          <Row k="ID type" v={m.id_doc_kind.replace('_', ' ')} />
          <Row k="ID / Passport #" v={m.id_doc_number} mono />
          <Row k="KRA PIN" v={m.kra_pin || '—'} mono />
          <Row k="Gender" v={m.gender} />
          <Row k="Date of birth" v={m.date_of_birth ? new Date(m.date_of_birth).toISOString().slice(0, 10) : '—'} mono />
        </KVS>
      </Card>
      <Card title="Contact & address">
        <KVS>
          <Row k="Phone" v={m.phone || '—'} mono />
          <Row k="Email" v={m.email || '—'} />
          <Row k="County" v={m.county || '—'} />
          <Row k="Sub-county" v={m.sub_county || '—'} />
          <Row k="Physical address" v={m.physical_address || '—'} />
        </KVS>
      </Card>
      <Card title="Employment">
        <KVS>
          <Row k="Status" v={m.employment_status || '—'} />
          <Row k="Job title" v={m.job_title || '—'} />
          <Row k="Employer" v={m.employer || '—'} />
          <Row k="Payroll / staff #" v={m.payroll_no || '—'} mono />
        </KVS>
      </Card>
      <Card title="Onboarding">
        <KVS>
          <Row k="Member #" v={m.member_no} mono />
          <Row k="Status" v={<StatusBadge status={m.status} />} />
          <Row k="Created" v={new Date(m.created_at).toISOString().slice(0, 10)} mono />
          {m.approved_at && (
            <Row k="Approved" v={new Date(m.approved_at).toISOString().slice(0, 16).replace('T', ' ')} mono />
          )}
          {m.rejection_reason && <Row k="Rejection reason" v={m.rejection_reason} />}
        </KVS>
      </Card>
    </div>
  );
}

// ─────────── People tab ───────────

function PeopleTab({ m, currency }: { m: ApiMemberDetail; currency: string }) {
  return (
    <>
      <Card title="Next of kin">
        {m.next_of_kin ? (
          <KVS>
            <Row k="Full name" v={m.next_of_kin.full_name} />
            <Row k="Relationship" v={m.next_of_kin.relationship || '—'} />
            <Row k="Phone" v={m.next_of_kin.phone || '—'} mono />
            <Row k="National ID #" v={m.next_of_kin.id_doc_number || '—'} mono />
            <Row k="Email" v={m.next_of_kin.email || '—'} />
          </KVS>
        ) : (
          <p className="muted tiny" style={{ margin: 0 }}>No next of kin recorded.</p>
        )}
      </Card>

      <BeneficiariesCard beneficiaries={m.beneficiaries} />

      <GuarantorshipsCard memberId={m.id} currency={currency} />
    </>
  );
}

function GuarantorshipsCard({ memberId, currency }: { memberId: string; currency: string }) {
  const fetcher = useMemo(() => () => listGuaranteesByMember(memberId), [memberId]);
  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Guarantorships</h3>
        <span className="card-sub">Loans this member has co-signed as a guarantor.</span>
      </div>
      <div className="card-body flush">
        <AsyncPanel
          fetcher={fetcher}
          deps={[memberId]}
          isEmpty={(rows) => rows.length === 0}
          empty={(
            <div className="empty" style={{ padding: 20 }}>
              No guarantorships on record for this member — they are not currently co-signing any loans.
            </div>
          )}
          errorTitle="Couldn't load guarantorships"
          errorMessage={(err) => isTimeoutError(err)
            ? "The lending service didn't respond in time. Try again in a moment."
            : "We couldn't fetch the loans this member guarantees."}
          skeleton={<div className="muted tiny" role="status" style={{ padding: 14 }}>Loading guarantorships…</div>}
        >
          {(rows) => (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Borrower</th>
                  <th>Product</th>
                  <th>Loan / application</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Guaranteed</th>
                  <th>Requested</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((g) => (
                  <tr key={g.id}>
                    <td>
                      <a href={`/members/${g.borrower_member_id}`} className="tbl-link">{g.borrower_full_name}</a>
                    </td>
                    <td className="tiny">
                      <div>{g.product_name}</div>
                      <div className="muted tiny mono">{g.product_code}</div>
                    </td>
                    <td className="tiny-mono">{g.loan_no ?? g.application_no}</td>
                    <td><StatusBadge status={g.status} /></td>
                    <td className="mono num" style={{ textAlign: 'right' }}>
                      {currency} {fmtMoney(g.amount_guaranteed)}
                    </td>
                    <td className="tiny-mono">{new Date(g.requested_at).toISOString().slice(0, 10)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </AsyncPanel>
      </div>
    </div>
  );
}

function BeneficiariesCard({ beneficiaries }: { beneficiaries: ApiRelation[] }) {
  const total = beneficiaries.reduce((acc, b) => acc + (b.share_percent ?? 0), 0);
  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Beneficiaries</h3>
        <span className="card-sub">
          {beneficiaries.length} listed{beneficiaries.length > 0 && ` · ${total.toFixed(0)}% allocated`}
        </span>
      </div>
      <div className="card-body flush">
        {beneficiaries.length === 0 ? (
          <div className="empty">No beneficiaries on file.</div>
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th style={{ width: 30 }}>#</th>
                <th>Name</th>
                <th>Relationship</th>
                <th>Phone</th>
                <th>National ID</th>
                <th style={{ textAlign: 'right' }}>Share</th>
              </tr>
            </thead>
            <tbody>
              {beneficiaries.map((b, i) => (
                <tr key={b.id}>
                  <td className="mono">{i + 1}</td>
                  <td><strong>{b.full_name}</strong></td>
                  <td>{b.relationship || <span className="muted">—</span>}</td>
                  <td className="mono">{b.phone || <span className="muted">—</span>}</td>
                  <td className="mono">{b.id_doc_number || <span className="muted">—</span>}</td>
                  <td className="mono num" style={{ textAlign: 'right' }}>
                    {b.share_percent != null ? `${b.share_percent.toFixed(0)}%` : <span className="muted">—</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────── Accounts tab ───────────

function AccountsTab({ currency, memberId }: { currency: string; memberId: string }) {
  return (
    <>
      {/* MemberAccountsPanel renders the per-account editor view —
          shares + every deposit account with balances, KPIs, and the
          per-account transaction history. This serves as the
          "Accounts" section requested in the spec; loans live in
          their own tab. */}
      <MemberAccountsPanel memberId={memberId} currency={currency} />
      {/* Below: the unified ledger across all three modules. */}
      <MemberLedgerPanel memberId={memberId} currency={currency} />
    </>
  );
}

// ─────────── Loans tab ───────────

function LoansTab({ memberId }: { memberId: string }) {
  const [data, setData] = useState<MemberLoanHistory | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getMemberLoanHistory(memberId).then(setData).catch((e) => setErr(extractError(e)));
  }, [memberId]);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="muted" role="status">Loading loans…</div>;

  // Primary action — kicks off a new application pre-filled for this
  // member. There is no dedicated /loans/applications/new route today;
  // the global /loans page hosts the new-app form, so we deep-link with
  // the member already selected.
  const newAppHref = `/loans?member=${memberId}&new=1`;
  // Secondary — drops the officer into the full lending workflow with
  // the same member filter applied. Wired in Loans.tsx via the `member`
  // URL param it now reads.
  const lendingHref = `/loans?member=${memberId}`;

  if (data.loans.length === 0) {
    return (
      <div className="card">
        <div className="card-body">
          <div className="empty" style={{ padding: 24, textAlign: 'center' }}>
            <p style={{ margin: '0 0 12px' }}>No loans yet for this member.</p>
            <a href={newAppHref} className="btn btn-primary">
              <Icon name="plus" size={12} /> New loan application →
            </a>
          </div>
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="kpi-grid" style={{ marginBottom: 14 }}>
        <div className="card kpi"><div className="muted tiny">Loans ever taken</div><div className="kpi-value">{data.total_loans_ever_taken}</div></div>
        <div className="card kpi"><div className="muted tiny">Active</div><div className="kpi-value">{data.active_loans}</div></div>
        <div className="card kpi"><div className="muted tiny">Total disbursed</div><div className="kpi-value mono">{fmtMoney(data.total_disbursed)}</div></div>
        <div className="card kpi"><div className="muted tiny">Total outstanding</div><div className="kpi-value mono">{fmtMoney(data.total_outstanding)}</div></div>
      </div>
      <div className="card">
        <div className="card-hd">
          <h3>Loan history</h3>
          <span className="card-sub">{data.loans.length} loan{data.loans.length === 1 ? '' : 's'} for this member</span>
          <div className="card-hd-actions">
            <a href={lendingHref} className="btn btn-sm">View in lending →</a>
            <a href={newAppHref} className="btn btn-sm btn-primary">
              <Icon name="plus" size={12} /> New loan application
            </a>
          </div>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Loan #</th>
                <th>Product</th>
                <th className="r">Principal</th>
                <th className="r">Outstanding</th>
                <th>Status</th>
                <th>Next due</th>
                <th>Classification</th>
              </tr>
            </thead>
            <tbody>
              {data.loans.map((row) => {
                const l = row.loan;
                const outstanding = (
                  parseFloat(l.principal_balance) + parseFloat(l.interest_balance) +
                  parseFloat(l.fees_balance) + parseFloat(l.penalty_balance)
                ).toFixed(2);
                return (
                  <tr key={l.id}>
                    <td className="tiny-mono">
                      <a href={`/loans/${l.id}`} className="tbl-link">{l.loan_no}</a>
                    </td>
                    <td>
                      <div>{row.product_name}</div>
                      <div className="muted tiny mono">{row.product_code}</div>
                    </td>
                    <td className="r mono">{fmtMoney(l.principal)}</td>
                    <td className="r mono">{fmtMoney(outstanding)}</td>
                    <td><StatusBadge status={l.status} /></td>
                    <td className="tiny-mono">
                      {l.next_installment_due_at ? l.next_installment_due_at.slice(0, 10) : '—'}
                      {l.days_past_due > 0 && (
                        <> · <Badge tone="neg">{l.days_past_due}d</Badge></>
                      )}
                    </td>
                    <td>
                      <Badge tone={CLASS_TONE[l.arrears_classification] ?? 'neutral'}>
                        {l.arrears_classification.replace(/_/g, ' ')}
                      </Badge>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

function fmtMoney(s: string | number | undefined | null): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// ComingSoonCard — placeholder for surfaces whose backing data is not
// yet wired into the UI. Replaces the old "module pending" tag with
// neutral, user-facing copy: titles read normally, the body explains
// what will land here without naming internal services / modules.
function ComingSoonCard({ title, sub, body }: { title: string; sub: string; body: string }) {
  return (
    <div className="card pending-card">
      <div className="card-hd">
        <h3>{title}</h3>
        <span className="card-sub">{sub}</span>
        <div className="card-hd-actions">
          <span className="muted tiny">Coming soon</span>
        </div>
      </div>
      <div className="card-body">
        <p className="muted tiny" style={{ margin: 0 }}>{body}</p>
      </div>
    </div>
  );
}

// ─────────── Documents & KYC tab ───────────

function DocumentsTab({
  m, canUpload, onReplaced,
}: {
  m: ApiMemberDetail;
  canUpload: boolean;
  onReplaced: () => void | Promise<void>;
}) {
  const kyc = computeKYC(m);
  return (
    <>
      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>KYC checklist</h3>
          <span className="card-sub">{kyc.percent}% complete · {kyc.completed}/{kyc.total} items</span>
        </div>
        <div className="card-body">
          <div className="risk-bar" style={{ marginBottom: 12 }}>
            <div
              className="risk-bar-fill"
              style={{
                width: `${kyc.percent}%`,
                background: kyc.percent >= 80 ? 'var(--pos)' : kyc.percent >= 50 ? 'var(--warn)' : 'var(--neg)',
              }}
            />
          </div>
          {kyc.items.map((it) => (
            <div key={it.label} className="kyc-row">
              <span className="kyc-check" data-on={it.done ? '1' : '0'}>{it.done ? '✓' : ''}</span>
              <div className="kyc-row-label">
                <div>{it.label}</div>
                {it.hint && <div className="kyc-row-hint">{it.hint}</div>}
              </div>
              {it.value && <span className="tiny-mono">{it.value}</span>}
            </div>
          ))}
        </div>
      </div>

      <DocumentsGallery memberId={m.id} documents={m.documents} canUpload={canUpload} onReplaced={onReplaced} />
    </>
  );
}

function DocumentsGallery({
  memberId, documents, canUpload, onReplaced,
}: {
  memberId: string;
  documents: { kind: DocumentKind; mime: string; size_bytes: number; uploaded_at: string }[] | null | undefined;
  canUpload: boolean;
  onReplaced: () => void | Promise<void>;
}) {
  const docs = documents ?? [];
  const have = new Map(docs.map((d) => [d.kind, d]));
  const kinds: DocumentKind[] = ['signature', 'passport_photo', 'id_front', 'id_back'];
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Documents</h3>
        <span className="card-sub">{docs.length} of {kinds.length} on file</span>
      </div>
      <div className="card-body">
        <div className="grid-4">
          {kinds.map((k) => (
            <DocumentTile
              key={k}
              memberId={memberId}
              kind={k}
              meta={have.get(k)}
              canUpload={canUpload}
              onReplaced={onReplaced}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

function DocumentTile({
  memberId, kind, meta, canUpload, onReplaced,
}: {
  memberId: string;
  kind: DocumentKind;
  meta?: { mime: string; size_bytes: number; uploaded_at: string };
  canUpload: boolean;
  onReplaced: () => void | Promise<void>;
}) {
  const [url, setUrl] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!meta) { setUrl(null); return; }
    let revoked = false;
    let objectUrl: string | null = null;
    fetchMemberDocument(memberId, kind)
      .then((blob) => {
        if (revoked) return;
        objectUrl = URL.createObjectURL(blob);
        setUrl(objectUrl);
      })
      .catch(() => setUrl(null));
    return () => { revoked = true; if (objectUrl) URL.revokeObjectURL(objectUrl); };
  }, [memberId, kind, meta?.uploaded_at]);

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setErr(null);
    setBusy(true);
    try { await uploadMemberDocument(memberId, kind, f); await onReplaced(); }
    catch (ex) { setErr(extractError(ex)); }
    finally { setBusy(false); if (inputRef.current) inputRef.current.value = ''; }
  }

  const isSignature = kind === 'signature';
  return (
    <div className="card" style={{ background: 'var(--surface-2)' }}>
      <div className="card-hd">
        <h3 style={{ fontSize: 11.5, textTransform: 'uppercase', letterSpacing: '0.05em' }}>{DOC_LABELS[kind]}</h3>
        <div className="card-hd-actions">
          {meta ? <Badge tone="pos">on file</Badge> : <Badge tone="neutral">missing</Badge>}
        </div>
      </div>
      <div className="card-body" style={{ padding: 12 }}>
        <div
          style={{
            width: '100%',
            aspectRatio: isSignature ? '3 / 1' : '4 / 5',
            border: '1px dashed var(--border)',
            borderRadius: 'var(--r-md)',
            display: 'grid',
            placeItems: 'center',
            overflow: 'hidden',
            background: isSignature
              ? 'repeating-linear-gradient(135deg, var(--surface), var(--surface) 8px, var(--surface-2) 8px, var(--surface-2) 16px)'
              : 'var(--surface)',
          }}
        >
          {url ? (
            <img src={url} alt={DOC_LABELS[kind]} style={{ width: '100%', height: '100%', objectFit: isSignature ? 'contain' : 'cover' }} />
          ) : (
            <span className="muted tiny">{busy ? 'Uploading…' : 'No file'}</span>
          )}
        </div>
        {meta && (
          <div className="muted tiny" style={{ marginTop: 6 }}>
            <span className="mono">{meta.mime}</span> · {(meta.size_bytes / 1024).toFixed(1)} KB
            <br />updated {new Date(meta.uploaded_at).toISOString().slice(0, 16).replace('T', ' ')}
          </div>
        )}
        {err && <div className="alert alert-error" style={{ marginTop: 6 }}>{err}</div>}
        {canUpload && (
          <div className="row" style={{ marginTop: 8 }}>
            <input
              ref={inputRef}
              type="file"
              accept={DOC_ACCEPT[kind]}
              onChange={onPick}
              style={{ display: 'none' }}
              disabled={busy}
            />
            <button type="button" className="btn btn-sm" disabled={busy} onClick={() => inputRef.current?.click()}>
              <Icon name="arrow_up" size={12} /> {meta ? 'Replace' : 'Upload'}
            </button>
            {url && (
              <a className="btn btn-sm btn-ghost" href={url} download={`${kind}.${(meta?.mime || 'image/png').split('/')[1]}`}>
                <Icon name="arrow_dn" size={12} /> Download
              </a>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ─────────── Activity tab ───────────

function ActivityTab({ memberId, canSeeAudit }: { memberId: string; canSeeAudit: boolean }) {
  return (
    <>
      <div className="card">
        <div className="card-hd">
          <h3>System events</h3>
          <span className="card-sub">audit log entries for this member</span>
        </div>
        <div className="card-body">
          {canSeeAudit
            ? <AuditTimeline memberId={memberId} limit={100} />
            : <span className="muted tiny">You don't have the audit:view permission.</span>}
        </div>
      </div>

      <ComingSoonCard
        title="Communications"
        sub="Email + SMS history sent to this member."
        body="Statements, reminders, OTPs, and broadcasts addressed to this member will appear here once the per-member communications log is enabled."
      />
    </>
  );
}

function AuditTimeline({ memberId, limit }: { memberId: string; limit: number }) {
  const fetcher = useMemo(
    () => () => listAuditForTarget('member', memberId, limit),
    [memberId, limit],
  );
  return (
    <AsyncPanel
      fetcher={fetcher}
      deps={[memberId, limit]}
      isEmpty={(es) => es.length === 0}
      empty={(
        <div className="empty" style={{ padding: 20 }}>
          No audit events yet — nothing has touched this member's record since they were onboarded.
        </div>
      )}
      errorTitle="Couldn't load activity"
      errorMessage={(err) => isTimeoutError(err)
        ? "The audit query took too long to respond. The history is still on file; retry to try again."
        : "We couldn't fetch the system events for this member."}
      skeleton={<span className="muted tiny" role="status">Loading activity…</span>}
    >
      {(entries) => (
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
      )}
    </AsyncPanel>
  );
}

function toneFor(action: string): string {
  if (action.endsWith('.rejected') || action.includes('suspend')) return 'neg';
  if (action.endsWith('.created') || action.endsWith('.uploaded')) return 'muted';
  if (action.endsWith('.approved')) return 'pos';
  return '';
}

function prettyAction(action: string): string {
  return action.replace(/^member\./, '').replace(/_/g, ' ');
}

function summarizeMeta(meta: Record<string, unknown>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(meta)) {
    if (v == null) continue;
    const s = typeof v === 'string' ? v : JSON.stringify(v);
    parts.push(`${k}=${s}`);
  }
  return parts.slice(0, 4).join(', ');
}

// ─────────── KYC + risk computers ───────────

type KYCItem = { label: string; hint?: string; value?: string; done: boolean };
type KYCResult = { items: KYCItem[]; completed: number; total: number; percent: number };

function computeKYC(m: ApiMemberDetail): KYCResult {
  const hasDoc = (k: DocumentKind) => (m.documents ?? []).some((d) => d.kind === k);
  const items: KYCItem[] = [
    { label: 'Full name', value: m.full_name, done: !!m.full_name },
    { label: 'ID / Passport number', hint: m.id_doc_kind.replace('_', ' '), value: m.id_doc_number, done: !!m.id_doc_number },
    { label: 'KRA PIN', value: m.kra_pin, done: !!m.kra_pin },
    { label: 'Date of birth', value: m.date_of_birth?.slice(0, 10), done: !!m.date_of_birth },
    { label: 'Gender recorded', done: m.gender !== 'undisclosed' },
    { label: 'Phone number', value: m.phone, done: !!m.phone },
    { label: 'Email address', value: m.email, done: !!m.email },
    { label: 'Physical address', done: !!m.physical_address },
    { label: 'Passport photo', hint: 'Upload under Documents', done: hasDoc('passport_photo') },
    { label: 'Signature', hint: 'Upload under Documents', done: hasDoc('signature') },
    { label: 'ID document scan', hint: 'Front + back', done: hasDoc('id_front') && hasDoc('id_back') },
    { label: 'Next of kin recorded', done: !!m.next_of_kin },
    { label: 'At least one beneficiary', done: (m.beneficiaries?.length ?? 0) > 0 },
  ];
  const completed = items.filter((i) => i.done).length;
  return {
    items,
    completed,
    total: items.length,
    percent: Math.round((completed / items.length) * 100),
  };
}

type RiskResult = { score: number; tier: string; factors: string[]; color: string };

function computeRisk(m: ApiMemberDetail, kycPercent: number): RiskResult {
  let score = 50;
  const factors: string[] = [];

  if (m.status === 'active') { score += 15; factors.push('+15 active member'); }
  if (m.status === 'rejected') { score -= 25; factors.push('-25 onboarding rejected'); }
  if (m.status === 'suspended') { score -= 15; factors.push('-15 suspended'); }
  if (kycPercent >= 80) { score += 15; factors.push('+15 KYC ≥ 80%'); }
  else if (kycPercent < 50) { score -= 10; factors.push('-10 KYC < 50%'); }
  if (m.employment_status === 'employed') { score += 10; factors.push('+10 employed'); }
  if (m.kra_pin) { score += 5; factors.push('+5 KRA PIN on file'); }
  if (m.date_of_birth) {
    const age = (Date.now() - new Date(m.date_of_birth).getTime()) / (1000 * 60 * 60 * 24 * 365.25);
    if (age >= 25 && age <= 60) { score += 5; factors.push('+5 age in working range'); }
  }

  score = Math.max(0, Math.min(100, score));
  const tier = score >= 75 ? 'low risk' : score >= 50 ? 'moderate' : score >= 30 ? 'elevated' : 'high risk';
  const color = score >= 75 ? 'var(--pos)' : score >= 50 ? 'var(--accent)' : score >= 30 ? 'var(--warn)' : 'var(--neg)';
  return { score, tier, factors, color };
}

// ─────────── presentation helpers ───────────

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="card">
      <div className="card-hd"><h3>{title}</h3></div>
      <div className="card-body">{children}</div>
    </div>
  );
}

function KVS({ children }: { children: ReactNode }) {
  return <dl className="kvs">{children}</dl>;
}

function Row({ k, v, mono }: { k: ReactNode; v: ReactNode; mono?: boolean }) {
  return (<><dt>{k}</dt><dd className={mono ? 'mono' : ''}>{v}</dd></>);
}

function extractIdFromPath(path: string): string {
  const m = path.match(/^\/members\/([^/]+)\/?$/);
  return m ? m[1] : '';
}

// Suppress unused-icon-name warnings if some icons are only used conditionally.
export type _MemberProfileIcons = IconName;
