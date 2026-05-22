// Platform-admin home. Combines what used to be three separate pages:
//   • Tenants list
//   • Credits operations queue (top-ups, adjustments, analytics)
//   • Shared driver config (SMTP + SMS)
//
// Tabs at the top of the page. The Overview tab augments the existing
// tenants table with inline SMS + Email credit balance columns and a
// low-balance status pill, so the platform admin sees which tenants
// need attention without leaving the table. Clicking a row opens a
// drawer with full credit management (top-up, ledger, pricing,
// adjustment) — same component that powered the standalone Credits
// page.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  extractError,
  listPlatformTenantBalances,
  listTenants,
  type ApiTenant,
  type CreditBalance,
  type PlatformTenantBalance,
} from '../api/client';
import SecurityCard from '../components/SecurityCard';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon, type IconName } from '../components/Icon';
import { PlatformDriversForm } from '../components/PlatformDriversForm';
import { Tabs } from '../components/Tabs';
import {
  AdjustmentsTable,
  AnalyticsPanel,
  RequestsTable,
} from './PlatformCredits';

const PLAN_TONE: Record<string, 'pos' | 'accent' | 'warn' | 'neutral'> = {
  starter: 'neutral',
  standard: 'accent',
  premium: 'warn',
  enterprise: 'pos',
};

type Tab = 'overview' | 'operations' | 'drivers';

export default function PlatformDashboard() {
  const { user } = useAuth();
  const [tab, setTab] = useState<Tab>('overview');

  const [tenants, setTenants] = useState<ApiTenant[] | null>(null);
  const [balances, setBalances] = useState<Map<string, CreditBalance[]>>(new Map());
  const [loadErr, setLoadErr] = useState<string | null>(null);

  const onboardedSlug = useMemo(
    () => new URLSearchParams(window.location.search).get('onboarded'),
    [],
  );

  async function reload() {
    setLoadErr(null);
    try {
      // Tenants + balances load in parallel — the join happens client-side.
      const [tn, bn] = await Promise.all([
        listTenants(),
        listPlatformTenantBalances(),
      ]);
      setTenants(tn);
      setBalances(buildBalanceMap(bn.items ?? []));
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, []);

  if (!user?.is_platform_admin) {
    return <div className="page"><div className="empty">Platform-admin access required.</div></div>;
  }

  const real = (tenants ?? []).filter((t) => t.slug !== 'platform');
  const onboarded = onboardedSlug && real.find((t) => t.slug === onboardedSlug);

  const counts = useMemo(() => ({
    total: real.length,
    active: real.filter((t) => t.status === 'active').length,
    trial: real.filter((t) => t.status === 'trial').length,
    pending: real.filter((t) => t.status === 'pending_setup').length,
    suspended: real.filter((t) => t.status === 'suspended').length,
    expired: real.filter((t) => t.status === 'expired').length,
    archived: real.filter((t) => t.status === 'archived').length,
  }), [real]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Platform · Overview</div>
          <h1>Tenant administration</h1>
          <div className="page-sub">Signed in as {user?.email} · platform super-admin</div>
        </div>
        <div className="page-hd-actions">
          <a href="/tenants/new" className="btn btn-sm btn-accent">
            <Icon name="plus" size={13} /> New tenant
          </a>
        </div>
      </div>

      {onboarded && (
        <div className="alert alert-info">
          ✓ <strong>{onboarded.name}</strong> created.
          {' '}<a href={`//${onboarded.slug}.${import.meta.env.VITE_APP_DOMAIN}:5173`}>
            Open the tenant subdomain →
          </a>
        </div>
      )}
      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      {/* Tabs */}
      <div className="card" style={{ padding: 0 }}>
        <Tabs
          ariaLabel="Platform views"
          tabs={[
            { id: 'overview',   label: 'Overview' },
            { id: 'operations', label: 'Credit operations' },
            { id: 'drivers',    label: 'Drivers' },
          ] as const}
          value={tab}
          onChange={setTab}
        >
          {(activeId) => (
            <>
              {activeId === 'overview' && (
                <OverviewTab
                  real={real}
                  counts={counts}
                  balances={balances}
                  loading={tenants === null}
                />
              )}
              {activeId === 'operations' && <OperationsTab currentUserID={user?.id ?? ''} onChanged={reload} />}
              {activeId === 'drivers'    && <PlatformDriversForm />}
            </>
          )}
        </Tabs>
      </div>
    </div>
  );
}

// ─────────── Overview tab ───────────

function OverviewTab({
  real, counts, balances, loading,
}: {
  real: ApiTenant[];
  counts: { total: number; active: number; trial: number; pending: number; suspended: number; expired: number; archived: number };
  balances: Map<string, CreditBalance[]>;
  loading: boolean;
}) {
  return (
    <>
      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Tenants" value={counts.total} />
        <KPI label="Active" value={counts.active} tone="pos" />
        <KPI label="Trial" value={counts.trial} tone="info" />
        <KPI label="Pending setup" value={counts.pending} tone="warn" />
        <KPI label="Suspended" value={counts.suspended} tone="neg" />
        <KPI label="Expired" value={counts.expired} tone="warn" />
        <KPI label="Archived" value={counts.archived} tone="neutral" />
      </div>

      <SecurityCard />

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>All tenants</h3>
          <span className="card-sub">{real.length} total · click a row to open the tenant profile (credits, branches, status)</span>
        </div>
        <div className="card-body flush">
          {loading && <div className="empty">Loading…</div>}
          {!loading && real.length === 0 && (
            <div className="empty">
              No tenants yet. <a href="/tenants/new" style={{ color: 'var(--accent)' }}>Onboard the first one →</a>
            </div>
          )}
          {real.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Tenant</th>
                  <th>Plan · Status</th>
                  <th className="num">SMS</th>
                  <th className="num">Email</th>
                  <th>Credits</th>
                  <th>Locks</th>
                  <th style={{ width: 36 }}></th>
                </tr>
              </thead>
              <tbody>
                {real.map((t) => {
                  const bs = balances.get(t.id) ?? [];
                  const sms = bs.find((b) => b.channel === 'sms');
                  const email = bs.find((b) => b.channel === 'email');
                  return (
                    <tr
                      key={t.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => { window.location.href = `/tenants/${t.id}`; }}
                      title="Open tenant profile"
                    >
                      <td>
                        <div style={{ fontWeight: 600 }}>{t.name}</div>
                        <div className="muted tiny mono">
                          {t.slug}
                          {t.registration_no ? ` · ${t.registration_no}` : ''}
                        </div>
                      </td>
                      <td>
                        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
                          <Badge tone={PLAN_TONE[t.billing_plan] ?? 'neutral'}>{t.billing_plan}</Badge>
                          <StatusBadge status={t.status} />
                        </div>
                      </td>
                      <td className="num" style={{ color: (sms?.balance ?? 0) < 1 ? 'var(--neg)' : undefined }}>
                        {(sms?.balance ?? 0).toLocaleString()}
                      </td>
                      <td className="num" style={{ color: (email?.balance ?? 0) < 1 ? 'var(--neg)' : undefined }}>
                        {(email?.balance ?? 0).toLocaleString()}
                      </td>
                      <td><CreditPill sms={sms} email={email} /></td>
                      <td><LockIndicators t={t} /></td>
                      <td><Icon name="chevron_r" size={12} /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </>
  );
}

// CreditPill — small status badge that summarises the worst state
// across the two channels for a tenant.
function CreditPill({ sms, email }: { sms?: CreditBalance; email?: CreditBalance }) {
  const zero =
    (sms && sms.balance < 1) || (email && email.balance < 1);
  const low =
    (sms && sms.balance > 0 && sms.low_balance_threshold > 0 && sms.balance <= sms.low_balance_threshold) ||
    (email && email.balance > 0 && email.low_balance_threshold > 0 && email.balance <= email.low_balance_threshold);
  if (zero) {
    return <span style={{
      background: 'var(--neg-bg, #fee)', color: 'var(--neg)', padding: '2px 8px',
      borderRadius: 999, fontWeight: 600, fontSize: 11,
    }}>EXHAUSTED</span>;
  }
  if (low) {
    return <span style={{
      background: 'var(--warn-bg, #ffeacc)', color: 'var(--warn)', padding: '2px 8px',
      borderRadius: 999, fontWeight: 600, fontSize: 11,
    }}>LOW</span>;
  }
  return <span style={{
    background: 'var(--pos-bg, #e6f7ec)', color: 'var(--pos)', padding: '2px 8px',
    borderRadius: 999, fontWeight: 600, fontSize: 11,
  }}>OK</span>;
}

// ─────────── Operations tab — reuses the existing queue + analytics ───────────

function OperationsTab({ currentUserID, onChanged }: { currentUserID: string; onChanged: () => void }) {
  const [sub, setSub] = useState<'requests' | 'adjustments' | 'analytics'>('requests');
  return (
    <div>
      <Tabs
        ariaLabel="Credit operations"
        tabs={[
          { id: 'requests',    label: 'Top-up requests' },
          { id: 'adjustments', label: 'Adjustments' },
          { id: 'analytics',   label: 'Analytics' },
        ] as const}
        value={sub}
        onChange={setSub}
        tablistStyle={{ padding: 0, marginBottom: 10 }}
        panelStyle={{}}
      >
        {(activeId) => (
          <>
            {activeId === 'requests'    && <RequestsTable onChanged={onChanged} />}
            {activeId === 'adjustments' && <AdjustmentsTable currentUserID={currentUserID} onChanged={onChanged} />}
            {activeId === 'analytics'   && <AnalyticsPanel />}
          </>
        )}
      </Tabs>
    </div>
  );
}

// ─────────── Helpers ───────────

function buildBalanceMap(rows: PlatformTenantBalance[]): Map<string, CreditBalance[]> {
  const m = new Map<string, CreditBalance[]>();
  for (const r of rows) m.set(r.tenant_id, r.balances);
  return m;
}

function LockIndicators({ t }: { t: ApiTenant }) {
  // ApiTenant in the list endpoint includes a 'restrictions' shape.
  const r = t.restrictions;
  if (!r) return <span className="muted tiny">—</span>;
  const items: Array<{ on: boolean; icon: IconName; title: string }> = [
    { on: !!r.operations_frozen,     icon: 'shield', title: 'Operations frozen' },
    { on: !!r.users_locked,          icon: 'key',    title: 'Users locked' },
    { on: !!r.transactions_disabled, icon: 'bank',   title: 'Transactions disabled' },
  ];
  const on = items.filter((i) => i.on);
  if (on.length === 0) return <span className="muted tiny">—</span>;
  return (
    <span className="row" style={{ gap: 4 }}>
      {on.map((i, idx) => <Pip key={idx} icon={i.icon} title={i.title} />)}
    </span>
  );
}

function Pip({ icon, title }: { icon: IconName; title: string }) {
  return (
    <span title={title} style={{
      display: 'inline-flex', alignItems: 'center', gap: 2,
      padding: '2px 6px', background: 'var(--surface-2)', borderRadius: 4,
      color: 'var(--warn)', fontSize: 11,
    }}>
      <Icon name={icon} size={10} />
    </span>
  );
}

function KPI({ label, value, tone }: { label: string; value: number; tone?: 'pos' | 'neg' | 'warn' | 'info' | 'neutral' }) {
  const color =
    tone === 'pos'  ? 'var(--pos)'  :
    tone === 'neg'  ? 'var(--neg)'  :
    tone === 'warn' ? 'var(--warn)' :
    tone === 'info' ? 'var(--accent)' :
                      'var(--fg)';
  return (
    <div className="card">
      <div className="card-body">
        <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
        <div style={{ fontSize: 24, fontWeight: 700, color }}>{value}</div>
      </div>
    </div>
  );
}

// Wrapping ReactNode so an unused import doesn't lint-error if we
// later trim the component.
export type _ReactNode = ReactNode;
