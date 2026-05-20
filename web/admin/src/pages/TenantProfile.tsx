// Tenant profile / detail page. Platform-admin only.
//
// Shows org details + branches + contacts and provides the controls
// platform admins use to manage tenant lifecycle:
//   * change status (activate / deactivate / trial / expired / pending setup)
//   * flip restriction toggles (freeze operations, lock users, disable transactions)
//   * offboarding actions (data export, backup, archive)

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  archiveTenant,
  backupTenant,
  exportTenant,
  getTenant,
  setTenantRestrictions,
  setTenantStatus,
  extractError,
  type ApiTenantDetail,
  type TenantStatus,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

const ACTIVE_STATUSES: Exclude<TenantStatus, 'archived'>[] = [
  'active', 'trial', 'pending_setup', 'suspended', 'expired',
];

const STATUS_DESCRIPTIONS: Record<TenantStatus, string> = {
  active: 'Live tenant — all features available.',
  trial: 'Limited trial — full access pre-billing.',
  pending_setup: 'Onboarded but not yet activated by the platform team.',
  suspended: 'Access blocked. No login, no API traffic.',
  expired: 'Billing or licence lapsed. Access blocked.',
  archived: 'Off-boarded. Data retained, no access at all. Cannot be reactivated.',
};

const PLAN_TONE: Record<string, 'pos' | 'accent' | 'warn' | 'neutral'> = {
  starter: 'neutral',
  standard: 'accent',
  premium: 'warn',
  enterprise: 'pos',
};

export default function TenantProfile() {
  const tenantId = useMemo(() => extractIdFromPath(window.location.pathname), []);
  const { user } = useAuth();

  const [t, setT] = useState<ApiTenantDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [downloading, setDownloading] = useState<string | null>(null);

  async function reload() {
    if (!tenantId) return;
    setErr(null);
    try {
      setT(await getTenant(tenantId));
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [tenantId]);

  if (!user?.is_platform_admin) {
    return <div className="page"><div className="alert alert-error">Platform admin required.</div></div>;
  }
  if (!tenantId) {
    return <div className="page"><div className="alert alert-error">Missing tenant id.</div></div>;
  }

  const archived = t?.status === 'archived';

  async function onStatusChange(next: Exclude<TenantStatus, 'archived'>) {
    if (!t) return;
    if (next === t.status) return;
    if (!confirm(`Set ${t.name} to "${next.replace('_', ' ')}"?`)) return;
    setBusy(`status:${next}`);
    try { await setTenantStatus(t.id, next); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  async function onToggle(key: keyof ApiTenantDetail['restrictions']) {
    if (!t) return;
    const next = !t.restrictions[key];
    setBusy(`restrict:${key}`);
    try { await setTenantRestrictions(t.id, { [key]: next }); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  async function onArchive() {
    if (!t) return;
    if (!confirm(
      `Archive ${t.name}?\n\nThis flips status to "archived" and turns on ` +
      `every restriction toggle. The tenant becomes unreachable and cannot be reactivated.`,
    )) return;
    setBusy('archive');
    try { await archiveTenant(t.id); await reload(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  async function onDownload(kind: 'export' | 'backup') {
    if (!t) return;
    setDownloading(kind);
    try {
      if (kind === 'export') await exportTenant(t.id);
      else await backupTenant(t.id);
    } catch (e) {
      alert(extractError(e));
    } finally {
      setDownloading(null);
    }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div className="row" style={{ gap: 14, alignItems: 'flex-start' }}>
          <div style={{
            width: 64, height: 64, borderRadius: 'var(--r-md)',
            background: 'var(--accent)', color: '#fff',
            display: 'grid', placeItems: 'center',
            fontSize: 24, fontWeight: 700, fontFamily: 'var(--font-mono)',
          }}>{(t?.slug ?? '?').charAt(0).toUpperCase()}</div>
          <div>
            <div className="eyebrow">
              <a href="/" style={{ color: 'var(--accent)' }}>← Tenants</a>
            </div>
            <h1 style={{ marginBottom: 4 }}>{t ? t.name : 'Loading…'}</h1>
            {t && (
              <div className="page-sub" style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
                <span className="mono">{t.slug}.{import.meta.env.VITE_APP_DOMAIN}</span>
                <StatusBadge status={t.status} />
                <Badge tone={PLAN_TONE[t.billing_plan] ?? 'neutral'}>{t.billing_plan}</Badge>
                <Badge tone="neutral">{t.kind}</Badge>
                <span className="muted">·</span>
                <span className="tiny-mono">{t.country_code}/{t.currency_code}</span>
              </div>
            )}
          </div>
        </div>
        <div className="page-hd-actions">
          {t && !archived && (
            <a className="btn btn-sm" href={`//${t.slug}.${import.meta.env.VITE_APP_DOMAIN}:5173`} target="_blank" rel="noreferrer">
              <Icon name="arrow_up" size={12} /> Open tenant
            </a>
          )}
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {!t && !err && <div className="empty">Loading…</div>}

      {archived && (
        <div className="alert alert-warn">
          <strong>Archived:</strong> {t!.name} is off-boarded. Status and restrictions cannot be changed; only data export and backup remain available.
        </div>
      )}

      {t && (
        <>
          <div className="grid-2">
            <Card title="Organization">
              <KVS>
                <Row k="Name" v={t.name} />
                <Row k="Slug" v={t.slug} mono />
                <Row k="Legal name" v={t.legal_name || '—'} />
                <Row k="Type" v={t.kind} />
                <Row k="Registration #" v={t.registration_no || '—'} mono />
                <Row k="Tax PIN" v={t.tax_pin || '—'} mono />
                <Row k="License #" v={t.license_no || '—'} mono />
              </KVS>
            </Card>
            <Card title="Locale & plan">
              <KVS>
                <Row k="Country" v={t.country_code} mono />
                <Row k="Currency" v={t.currency_code} mono />
                <Row k="Billing plan" v={<Badge tone={PLAN_TONE[t.billing_plan] ?? 'neutral'}>{t.billing_plan}</Badge>} />
                <Row k="Created" v={new Date(t.created_at).toISOString().slice(0, 10)} mono />
                <Row k="Last updated" v={new Date(t.updated_at).toISOString().slice(0, 10)} mono />
              </KVS>
            </Card>
          </div>

          <div className="card" style={{ marginTop: 14 }}>
            <div className="card-hd">
              <h3>Branches</h3>
              <span className="card-sub">{t.branches.length} on file</span>
            </div>
            <div className="card-body flush">
              {t.branches.length === 0 ? (
                <div className="empty">No branches recorded.</div>
              ) : (
                <table className="tbl">
                  <thead><tr>
                    <th>Code</th><th>Name</th><th>Kind</th>
                    <th>County</th><th>Sub-county</th><th>Phone</th>
                  </tr></thead>
                  <tbody>
                    {t.branches.map((b) => (
                      <tr key={b.id}>
                        <td className="mono">{b.code}</td>
                        <td>{b.name}</td>
                        <td><Badge tone={b.kind === 'hq' ? 'accent' : b.kind === 'agency' ? 'warn' : 'neutral'}>{b.kind}</Badge></td>
                        <td>{b.county || <span className="muted">—</span>}</td>
                        <td>{b.sub_county || <span className="muted">—</span>}</td>
                        <td className="mono">{b.phone || <span className="muted">—</span>}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          <div className="card" style={{ marginTop: 14 }}>
            <div className="card-hd">
              <h3>Contacts</h3>
              <span className="card-sub">{t.contacts.length} on file</span>
            </div>
            <div className="card-body flush">
              {t.contacts.length === 0 ? (
                <div className="empty">No contacts recorded.</div>
              ) : (
                <table className="tbl">
                  <thead><tr>
                    <th style={{ width: 44 }}></th><th>Name</th><th>Title</th><th>Email</th><th>Phone</th>
                  </tr></thead>
                  <tbody>
                    {t.contacts.map((c) => (
                      <tr key={c.id}>
                        <td><Avatar name={c.full_name} size="sm" /></td>
                        <td>{c.full_name}</td>
                        <td>{c.title || <span className="muted">—</span>}</td>
                        <td className="tiny-mono">{c.email || <span className="muted">—</span>}</td>
                        <td className="mono">{c.phone || <span className="muted">—</span>}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          <StatusCard t={t} busy={busy} onChange={onStatusChange} disabled={archived} />
          <RestrictionsCard t={t} busy={busy} onToggle={onToggle} disabled={archived} />

          <DangerZone
            t={t}
            archived={archived}
            busy={busy}
            downloading={downloading}
            onDownload={onDownload}
            onArchive={onArchive}
          />
        </>
      )}
    </div>
  );
}

// ─────────── Status card ───────────

function StatusCard({
  t, busy, onChange, disabled,
}: {
  t: ApiTenantDetail;
  busy: string | null;
  onChange: (s: Exclude<TenantStatus, 'archived'>) => void;
  disabled: boolean;
}) {
  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Lifecycle status</h3>
        <span className="card-sub">{STATUS_DESCRIPTIONS[t.status]}</span>
      </div>
      <div className="card-body">
        <div className="fchips">
          {ACTIVE_STATUSES.map((s) => {
            const isOn = s === t.status;
            return (
              <button
                key={s}
                type="button"
                className="fchip"
                data-active={isOn || undefined}
                disabled={disabled || busy !== null}
                onClick={() => onChange(s)}
                title={STATUS_DESCRIPTIONS[s]}
              >
                {busy === `status:${s}` ? '…' : s.replace('_', ' ')}
              </button>
            );
          })}
        </div>
        <p className="muted tiny" style={{ marginTop: 10 }}>
          To off-board, use the <strong>Archive tenant</strong> action in the Danger zone below.
        </p>
      </div>
    </div>
  );
}

// ─────────── Restrictions card ───────────

function RestrictionsCard({
  t, busy, onToggle, disabled,
}: {
  t: ApiTenantDetail;
  busy: string | null;
  onToggle: (k: keyof ApiTenantDetail['restrictions']) => void;
  disabled: boolean;
}) {
  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Restrictions</h3>
        <span className="card-sub">Flip on independently of lifecycle status.</span>
      </div>
      <div className="card-body">
        <Toggle
          label="Freeze operations"
          hint="Block member onboarding, role changes, configuration edits. Existing data is read-only."
          enforcement="enforced when the relevant service is updated to honor it"
          value={t.restrictions.operations_frozen}
          busy={busy === 'restrict:operations_frozen'}
          disabled={disabled}
          onChange={() => onToggle('operations_frozen')}
        />
        <Toggle
          label="Lock users"
          hint="Block all logins and token refreshes for this tenant. Currently-valid tokens expire naturally."
          enforcement="enforced now"
          value={t.restrictions.users_locked}
          busy={busy === 'restrict:users_locked'}
          disabled={disabled}
          onChange={() => onToggle('users_locked')}
        />
        <Toggle
          label="Disable transactions"
          hint="Block savings deposits / withdrawals and loan disbursements. Reads still allowed."
          enforcement="enforced when transaction handlers exist"
          value={t.restrictions.transactions_disabled}
          busy={busy === 'restrict:transactions_disabled'}
          disabled={disabled}
          onChange={() => onToggle('transactions_disabled')}
        />
      </div>
    </div>
  );
}

function Toggle({
  label, hint, enforcement, value, busy, disabled, onChange,
}: {
  label: string;
  hint: string;
  enforcement: string;
  value: boolean;
  busy: boolean;
  disabled: boolean;
  onChange: () => void;
}) {
  return (
    <label
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '10px 12px',
        border: '1px solid var(--border)',
        borderRadius: 'var(--r-md)',
        marginBottom: 8,
        background: value ? 'var(--neg-bg)' : 'var(--surface)',
        cursor: disabled || busy ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.6 : 1,
      }}
    >
      <input
        type="checkbox"
        checked={value}
        disabled={disabled || busy}
        onChange={onChange}
        style={{ accentColor: 'var(--neg)' }}
      />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 500 }}>{label}</div>
        <div className="muted tiny">{hint}</div>
        <div className="tiny-mono" style={{ color: 'var(--fg-3)', marginTop: 2 }}>
          <span style={{ color: enforcement.startsWith('enforced now') ? 'var(--pos)' : 'var(--warn)' }}>●</span>{' '}
          {enforcement}
        </div>
      </div>
      {value && <Badge tone="neg">on</Badge>}
    </label>
  );
}

// ─────────── Danger zone ───────────

function DangerZone({
  t, archived, busy, downloading, onDownload, onArchive,
}: {
  t: ApiTenantDetail;
  archived: boolean;
  busy: string | null;
  downloading: string | null;
  onDownload: (kind: 'export' | 'backup') => void;
  onArchive: () => void;
}) {
  return (
    <div className="card" style={{ marginTop: 14, borderColor: 'var(--neg)' }}>
      <div className="card-hd" style={{ background: 'var(--neg-bg)' }}>
        <h3 style={{ color: 'var(--neg)' }}>Offboarding</h3>
        <span className="card-sub">Generate exports, then archive.</span>
      </div>
      <div className="card-body">
        <ActionRow
          title="Data export"
          desc="JSON bundle of operational data: tenant config, branches, contacts, staff users. Suitable for handing back to the SACCO."
          buttonLabel={downloading === 'export' ? 'Generating…' : 'Download export'}
          icon="arrow_dn"
          disabled={busy !== null || downloading !== null}
          onClick={() => onDownload('export')}
        />
        <ActionRow
          title="Backup generation"
          desc="Full JSON bundle including user→role assignments. Suitable for platform-engineering archival."
          buttonLabel={downloading === 'backup' ? 'Generating…' : 'Download backup'}
          icon="arrow_dn"
          disabled={busy !== null || downloading !== null}
          onClick={() => onDownload('backup')}
        />
        <ActionRow
          title="Archive tenant"
          desc={archived
            ? 'Already archived. Status and restrictions are locked.'
            : 'Off-board for good: status becomes "archived" and every restriction toggle is turned on. Cannot be reversed.'}
          buttonLabel={busy === 'archive' ? 'Archiving…' : 'Archive tenant'}
          icon="trash"
          tone="neg"
          disabled={archived || busy !== null || downloading !== null}
          onClick={onArchive}
        />

        <p className="muted tiny" style={{ marginTop: 8 }}>
          Tenant <span className="mono">{t.slug}</span> · {new Date(t.created_at).toISOString().slice(0, 10)}
        </p>
      </div>
    </div>
  );
}

function ActionRow({
  title, desc, buttonLabel, icon, tone, disabled, onClick,
}: {
  title: string;
  desc: string;
  buttonLabel: string;
  icon: 'arrow_dn' | 'trash';
  tone?: 'neg';
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      gap: 12,
      padding: '10px 0',
      borderBottom: '1px solid var(--border)',
    }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 500 }}>{title}</div>
        <div className="muted tiny">{desc}</div>
      </div>
      <button
        type="button"
        className={tone === 'neg' ? 'btn btn-sm btn-danger' : 'btn btn-sm'}
        disabled={disabled}
        onClick={onClick}
      >
        <Icon name={icon} size={12} /> {buttonLabel}
      </button>
    </div>
  );
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
  const m = path.match(/^\/tenants\/([^/]+)\/?$/);
  return m ? m[1] : '';
}
