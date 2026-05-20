import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  listTenants,
  extractError,
  type ApiTenant,
} from '../api/client';
import SecurityCard from '../components/SecurityCard';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon, type IconName } from '../components/Icon';

const PLAN_TONE: Record<string, 'pos' | 'accent' | 'warn' | 'neutral'> = {
  starter: 'neutral',
  standard: 'accent',
  premium: 'warn',
  enterprise: 'pos',
};

export default function PlatformDashboard() {
  const { user } = useAuth();
  const [tenants, setTenants] = useState<ApiTenant[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const onboardedSlug = useMemo(
    () => new URLSearchParams(window.location.search).get('onboarded'),
    [],
  );

  async function reload() {
    setLoadErr(null);
    try {
      setTenants(await listTenants());
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, []);

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
          {' '}<a href={`//${onboarded.slug}.${import.meta.env.VITE_APP_DOMAIN}:5173`}>Open the tenant subdomain →</a>
        </div>
      )}
      {loadErr && <div className="alert alert-error">{loadErr}</div>}

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
          <span className="card-sub">{real.length} total</span>
        </div>
        <div className="card-body flush">
          {!tenants && !loadErr && <div className="empty">Loading…</div>}
          {tenants && real.length === 0 && (
            <div className="empty">
              No tenants yet. <a href="/tenants/new" style={{ color: 'var(--accent)' }}>Onboard the first one →</a>
            </div>
          )}
          {real.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Slug</th>
                  <th>Name</th>
                  <th>Type</th>
                  <th>Plan</th>
                  <th>Status</th>
                  <th>Locks</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {real.map((t) => (
                  <tr key={t.id}>
                    <td className="mono">
                      <a href={`/tenants/${t.id}`} className="tbl-link">{t.slug}</a>
                    </td>
                    <td>
                      <div>{t.name}</div>
                      {t.registration_no && <div className="muted tiny mono">{t.registration_no}</div>}
                    </td>
                    <td><Badge tone="neutral">{t.kind}</Badge></td>
                    <td><Badge tone={PLAN_TONE[t.billing_plan] ?? 'neutral'}>{t.billing_plan}</Badge></td>
                    <td><StatusBadge status={t.status} /></td>
                    <td>
                      <LockIndicators t={t} />
                    </td>
                    <td>
                      <a className="btn btn-sm" href={`/tenants/${t.id}`}>
                        Manage <Icon name="chevron_r" size={12} />
                      </a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function LockIndicators({ t }: { t: ApiTenant }) {
  // ApiTenant in the list endpoint doesn't include restrictions, so
  // this is a placeholder — operators see the full picture on the
  // tenant profile page. Keeping the column to avoid table reshuffles
  // once the list endpoint is enriched.
  // Hint the archived case so the table still feels informative.
  if (t.status === 'archived') {
    return (
      <span style={{ display: 'inline-flex', gap: 4 }}>
        <Pip icon="lock" title="Users locked" />
        <Pip icon="lock" title="Operations frozen" />
        <Pip icon="lock" title="Transactions disabled" />
      </span>
    );
  }
  return <span className="muted tiny">—</span>;
}

function Pip({ icon, title }: { icon: IconName; title: string }) {
  return (
    <span
      title={title}
      style={{
        width: 18, height: 18, borderRadius: '50%',
        display: 'inline-grid', placeItems: 'center',
        background: 'var(--neg-bg)', color: 'var(--neg)',
      }}
    >
      <Icon name={icon} size={10} />
    </span>
  );
}

function KPI({ label, value, tone }: { label: string; value: number; tone?: 'pos' | 'neg' | 'warn' | 'info' | 'neutral' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' :
    tone === 'info' ? 'var(--info)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
      </div>
    </div>
  );
}
