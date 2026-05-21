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
  addTenantContact,
  deleteTenantContact,
  updateTenantContact,
  forceTenantUserPasswordReset,
  inviteUserToTenant,
  listTenantUsers,
  reactivateTenantUser,
  resendTenantUserInvite,
  revokeTenantUser,
  suspendTenantUser,
  type ApiTenantContact,
  type ApiTenantDetail,
  type TenantContactInput,
  type TenantStatus,
  type TenantUserRow,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';
import {
  getPlatformTenantDetail,
  platformLedger,
  type CreditBalance,
  type CreditChannel,
  type CreditLedgerEntry,
  type CreditPricing,
} from '../api/client';
import {
  AdjustmentForm,
  LedgerTable,
  PricingForm,
  TopupForm,
} from './PlatformCredits';

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
  const [usersRefreshTick, setUsersRefreshTick] = useState(0);

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

          <ContactsCard
            tenantID={t.id}
            initial={t.contacts}
            disabled={archived}
            onUserAdded={() => setUsersRefreshTick((n) => n + 1)}
          />
          <UsersCard
            tenantID={t.id}
            disabled={archived}
            refreshTick={usersRefreshTick}
          />

          <StatusCard t={t} busy={busy} onChange={onStatusChange} disabled={archived} />
          <RestrictionsCard t={t} busy={busy} onToggle={onToggle} disabled={archived} />
          <TenantCreditsCard tenantID={t.id} />

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

// ─────────── Contacts card (editable) ───────────
//
// Replaces the read-only contacts table that lived inline. Supports
// add / edit / delete via the platform-admin endpoints on the identity
// service.

function ContactsCard({
  tenantID, initial, disabled, onUserAdded,
}: {
  tenantID: string;
  initial: ApiTenantContact[];
  disabled: boolean;
  onUserAdded?: () => void; // notify UsersCard to refresh when a contact provisions a user
}) {
  const [contacts, setContacts] = useState<ApiTenantContact[]>(initial);
  const [editingID, setEditingID] = useState<string | 'new' | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);

  useEffect(() => { setContacts(initial); }, [initial]);

  async function handleAdd(input: TenantContactInput) {
    setErr(null); setInfo(null);
    try {
      const r = await addTenantContact(tenantID, input);
      setContacts((cs) => [...cs, r.contact]);
      setEditingID(null);
      if (r.user) {
        setInfo(`Invite sent to ${r.user.email}. They'll receive an email to set their password.`);
        onUserAdded?.();
      }
    } catch (e) {
      setErr(extractError(e));
      throw e;
    }
  }
  async function handleUpdate(id: string, input: TenantContactInput) {
    setErr(null);
    try {
      const c = await updateTenantContact(tenantID, id, input);
      setContacts((cs) => cs.map((x) => (x.id === id ? c : x)));
      setEditingID(null);
    } catch (e) {
      setErr(extractError(e));
      throw e;
    }
  }
  async function handleDelete(id: string) {
    if (!window.confirm('Delete this contact?')) return;
    setErr(null);
    try {
      await deleteTenantContact(tenantID, id);
      setContacts((cs) => cs.filter((x) => x.id !== id));
    } catch (e) {
      setErr(extractError(e));
    }
  }

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Contacts</h3>
        <span className="card-sub">{contacts.length} on file</span>
        {!disabled && (
          <div className="card-hd-actions">
            <button className="btn btn-sm" disabled={editingID !== null} onClick={() => setEditingID('new')}>
              <Icon name="plus" size={12} /> Add contact
            </button>
          </div>
        )}
      </div>
      <div className="card-body flush">
        {err && <div className="alert alert-error" style={{ margin: 10 }}>{err}</div>}
        {info && <div className="alert alert-info" style={{ margin: 10 }}>{info}</div>}
        {contacts.length === 0 && editingID !== 'new' && (
          <div className="empty">
            No contacts recorded.{!disabled && (
              <> Click <strong>Add contact</strong> to record one.</>
            )}
          </div>
        )}
        {(contacts.length > 0 || editingID === 'new') && (
          <table className="tbl">
            <thead><tr>
              <th style={{ width: 44 }}></th><th>Name</th><th>Title</th><th>Email</th><th>Phone</th>
              <th style={{ width: 1 }}></th>
            </tr></thead>
            <tbody>
              {contacts.map((c) => {
                if (editingID === c.id) {
                  return (
                    <ContactEditRow
                      key={c.id}
                      initial={c}
                      onSave={(v) => handleUpdate(c.id, v)}
                      onCancel={() => setEditingID(null)}
                    />
                  );
                }
                return (
                  <tr key={c.id}>
                    <td><Avatar name={c.full_name} size="sm" /></td>
                    <td>{c.full_name}</td>
                    <td>{c.title || <span className="muted">—</span>}</td>
                    <td className="tiny-mono">{c.email || <span className="muted">—</span>}</td>
                    <td className="mono">{c.phone || <span className="muted">—</span>}</td>
                    <td>
                      {!disabled && (
                        <div className="row" style={{ gap: 4 }}>
                          <button
                            className="btn btn-sm btn-ghost"
                            disabled={editingID !== null}
                            onClick={() => setEditingID(c.id)}
                          >
                            Edit
                          </button>
                          <button
                            className="btn btn-sm btn-danger"
                            disabled={editingID !== null}
                            onClick={() => void handleDelete(c.id)}
                          >
                            Delete
                          </button>
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
              {editingID === 'new' && (
                <ContactEditRow
                  onSave={handleAdd}
                  onCancel={() => setEditingID(null)}
                />
              )}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// Roles offered when "provision as user" is checked on a new contact.
// Free-text override is always available (server validates against the
// tenant's role catalogue).
const PROVISION_ROLE_OPTIONS = [
  'tenant_owner', 'sacco_admin', 'accountant', 'credit_officer',
  'teller', 'branch_manager', 'auditor', 'collections_officer',
];

function ContactEditRow({
  initial, onSave, onCancel,
}: {
  initial?: ApiTenantContact;
  onSave: (v: TenantContactInput) => Promise<void>;
  onCancel: () => void;
}) {
  const isNew = !initial;
  const [fullName, setFullName] = useState(initial?.full_name ?? '');
  const [title, setTitle] = useState(initial?.title ?? '');
  const [email, setEmail] = useState(initial?.email ?? '');
  const [phone, setPhone] = useState(initial?.phone ?? '');
  const [provision, setProvision] = useState(false);
  const [role, setRole] = useState('tenant_owner');
  const [busy, setBusy] = useState(false);
  async function submit() {
    if (!fullName.trim()) return;
    if (provision && !email.trim()) return;
    setBusy(true);
    try {
      const payload: TenantContactInput = { full_name: fullName, title, email, phone };
      if (isNew && provision) {
        payload.provision_as_user = true;
        payload.role_codes = [role];
      }
      await onSave(payload);
    } catch { /* parent surfaces err */ }
    finally { setBusy(false); }
  }
  return (
    <>
      <tr style={{ background: 'var(--surface-2)' }}>
        <td><Avatar name={fullName || '?'} size="sm" /></td>
        <td>
          <input
            value={fullName}
            onChange={(e) => setFullName(e.target.value)}
            placeholder="Full name *"
            style={{ width: '100%' }}
            autoFocus
          />
        </td>
        <td>
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Title" style={{ width: '100%' }} />
        </td>
        <td>
          <input
            type="email" value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={provision ? 'login@example.com *' : 'email@example.com'}
            style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
          />
        </td>
        <td>
          <input
            value={phone} onChange={(e) => setPhone(e.target.value)}
            placeholder="+254..."
            style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
          />
        </td>
        <td>
          <div className="row" style={{ gap: 4 }}>
            <button
              className="btn btn-sm btn-primary"
              disabled={busy || !fullName.trim() || (provision && !email.trim())}
              onClick={() => void submit()}
            >
              {busy ? '…' : 'Save'}
            </button>
            <button className="btn btn-sm btn-ghost" disabled={busy} onClick={onCancel}>Cancel</button>
          </div>
        </td>
      </tr>
      {isNew && (
        <tr style={{ background: 'var(--surface-2)' }}>
          <td></td>
          <td colSpan={5} style={{ paddingTop: 0 }}>
            <label className="row" style={{ gap: 6, alignItems: 'center' }}>
              <input
                type="checkbox"
                checked={provision}
                onChange={(e) => setProvision(e.target.checked)}
              />
              <span>Also provision as a tenant user (email becomes their login; they'll receive an invite)</span>
            </label>
            {provision && (
              <div className="row" style={{ gap: 6, marginTop: 6, alignItems: 'center' }}>
                <label className="muted tiny" style={{ marginRight: 4 }}>Role:</label>
                <select value={role} onChange={(e) => setRole(e.target.value)}>
                  {PROVISION_ROLE_OPTIONS.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
                <span className="muted tiny">(Tenant Super Admin = <code>tenant_owner</code>)</span>
              </div>
            )}
          </td>
        </tr>
      )}
    </>
  );
}

// ─────────── Users card (staff in this tenant) ───────────
//
// Lists every staff user in the tenant and lets the platform admin
// invite new ones without leaving the dashboard. Role codes are free
// text in the form because tenants can define custom roles — the
// server validates against the role catalogue.

function UsersCard({ tenantID, disabled, refreshTick }: { tenantID: string; disabled: boolean; refreshTick?: number }) {
  const [users, setUsers] = useState<TenantUserRow[] | null>(null);
  const [inviting, setInviting] = useState(false);
  const [email, setEmail] = useState('');
  const [fullName, setFullName] = useState('');
  const [roleCodes, setRoleCodes] = useState('sacco_admin');
  const [busy, setBusy] = useState(false);
  const [rowBusy, setRowBusy] = useState<string | null>(null); // user_id of the row currently performing an action
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await listTenantUsers(tenantID);
      setUsers(r.users ?? []);
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [tenantID, refreshTick]);

  async function doAction(userID: string, label: string, fn: () => Promise<unknown>) {
    setErr(null); setInfo(null);
    setRowBusy(userID);
    try {
      await fn();
      setInfo(`${label} succeeded.`);
      await load();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setRowBusy(null);
    }
  }
  async function onResend(u: TenantUserRow) {
    await doAction(u.user.id, `Invite resent to ${u.user.email}`,
      () => resendTenantUserInvite(tenantID, u.user.id));
  }
  async function onSuspend(u: TenantUserRow) {
    const reason = prompt(`Reason for suspending ${u.user.email}:`) ?? '';
    if (!reason.trim()) return;
    await doAction(u.user.id, `${u.user.email} suspended`,
      () => suspendTenantUser(tenantID, u.user.id, reason));
  }
  async function onReactivate(u: TenantUserRow) {
    await doAction(u.user.id, `${u.user.email} reactivated`,
      () => reactivateTenantUser(tenantID, u.user.id));
  }
  async function onForceReset(u: TenantUserRow) {
    if (!confirm(`Force a password reset for ${u.user.email}? They'll receive a reset email and any active session is killed.`)) return;
    await doAction(u.user.id, `Reset email sent to ${u.user.email}`,
      () => forceTenantUserPasswordReset(tenantID, u.user.id));
  }
  async function onRevoke(u: TenantUserRow) {
    const reason = prompt(`Reason for permanently revoking access for ${u.user.email}:`) ?? '';
    if (!reason.trim()) return;
    if (!confirm(`Permanently revoke access for ${u.user.email}? They can no longer log in.`)) return;
    await doAction(u.user.id, `${u.user.email} access revoked`,
      () => revokeTenantUser(tenantID, u.user.id, reason));
  }

  async function submitInvite() {
    setErr(null); setInfo(null);
    const codes = roleCodes.split(',').map((c) => c.trim()).filter(Boolean);
    if (!email.trim() || !fullName.trim() || codes.length === 0) {
      setErr('Email, full name, and at least one role are required.');
      return;
    }
    setBusy(true);
    try {
      const r = await inviteUserToTenant(tenantID, {
        email: email.trim(),
        full_name: fullName.trim(),
        role_codes: codes,
      });
      setInfo(`Invited ${r.user.email}. Invite link valid until ${new Date(r.invite_expires).toLocaleString()}.`);
      setEmail(''); setFullName(''); setRoleCodes('sacco_admin');
      setInviting(false);
      await load();
    } catch (e) {
      setErr(extractError(e));
    } finally { setBusy(false); }
  }

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Staff users</h3>
        <span className="card-sub">{users?.length ?? '—'} on file</span>
        {!disabled && (
          <div className="card-hd-actions">
            <button className="btn btn-sm" disabled={inviting} onClick={() => setInviting(true)}>
              <Icon name="plus" size={12} /> Invite user
            </button>
          </div>
        )}
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error" style={{ marginBottom: 8 }}>{err}</div>}
        {info && <div className="alert alert-info" style={{ marginBottom: 8 }}>{info}</div>}

        {inviting && (
          <div className="card" style={{ marginBottom: 12 }}>
            <div className="card-body">
              <h4 style={{ marginTop: 0 }}>Invite a user into this tenant</h4>
              <div className="grid-2">
                <label>
                  <div className="muted tiny" style={{ marginBottom: 4 }}>Email *</div>
                  <input
                    type="email" value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    placeholder="user@example.com"
                    style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
                  />
                </label>
                <label>
                  <div className="muted tiny" style={{ marginBottom: 4 }}>Full name *</div>
                  <input
                    value={fullName}
                    onChange={(e) => setFullName(e.target.value)}
                    placeholder="Jane Doe"
                    style={{ width: '100%' }}
                  />
                </label>
                <label style={{ gridColumn: 'span 2' }}>
                  <div className="muted tiny" style={{ marginBottom: 4 }}>
                    Role codes (comma-separated)
                  </div>
                  <input
                    value={roleCodes}
                    onChange={(e) => setRoleCodes(e.target.value)}
                    placeholder="sacco_admin, accountant"
                    style={{ width: '100%', fontFamily: 'var(--font-mono)' }}
                  />
                  <div className="muted tiny" style={{ marginTop: 4 }}>
                    Examples: <code>sacco_admin</code>, <code>tenant_owner</code>, <code>accountant</code>,
                    <code> credit_officer</code>, <code>teller</code>, <code>branch_manager</code>, <code>auditor</code>.
                  </div>
                </label>
              </div>
              <div className="row" style={{ gap: 8, marginTop: 10 }}>
                <button className="btn btn-primary" disabled={busy} onClick={() => void submitInvite()}>
                  {busy ? 'Sending…' : 'Send invite'}
                </button>
                <button className="btn btn-ghost" disabled={busy} onClick={() => { setInviting(false); setErr(null); }}>
                  Cancel
                </button>
              </div>
            </div>
          </div>
        )}

        {users === null && <div className="empty">Loading…</div>}
        {users !== null && users.length === 0 && (
          <div className="empty">No staff users yet. Click <strong>Invite user</strong> to add one.</div>
        )}
        {users !== null && users.length > 0 && (
          <table className="tbl">
            <thead><tr>
              <th style={{ width: 44 }}></th>
              <th>Name</th>
              <th>Email</th>
              <th>Status</th>
              <th>Roles</th>
              {!disabled && <th style={{ width: 1 }}>Actions</th>}
            </tr></thead>
            <tbody>
              {users.map((row) => {
                const isSuperAdmin = (row.roles ?? []).some((r) => r.code === 'tenant_owner');
                const status = row.user.status;
                return (
                  <tr key={row.user.id}>
                    <td><Avatar name={row.user.full_name || row.user.email} size="sm" /></td>
                    <td>
                      {row.user.full_name || <span className="muted">—</span>}
                      {isSuperAdmin && (
                        <Badge tone="warn" style={{ marginLeft: 6 }}>Super Admin</Badge>
                      )}
                    </td>
                    <td className="tiny-mono">{row.user.email}</td>
                    <td>
                      <Badge tone={
                        status === 'active'    ? 'pos' :
                        status === 'pending'   ? 'warn' :
                        status === 'suspended' ? 'neg' :
                        status === 'closed'    ? 'neg' : 'neutral'
                      }>
                        {status === 'closed' ? 'revoked' : status}
                      </Badge>
                    </td>
                    <td className="tiny">
                      {(row.roles ?? []).map((r) => r.code).join(', ') || <span className="muted">—</span>}
                    </td>
                    {!disabled && (
                      <td>
                        <UserActionMenu
                          row={row}
                          busy={rowBusy === row.user.id}
                          onResend={() => onResend(row)}
                          onSuspend={() => onSuspend(row)}
                          onReactivate={() => onReactivate(row)}
                          onForceReset={() => onForceReset(row)}
                          onRevoke={() => onRevoke(row)}
                        />
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// Tiny dropdown of per-user actions. Buttons are filtered by status:
// pending users see only Resend invite + Revoke; active users see
// Suspend + Reset password + Revoke; suspended users see Reactivate +
// Revoke; closed (revoked) users see nothing.
function UserActionMenu({
  row, busy, onResend, onSuspend, onReactivate, onForceReset, onRevoke,
}: {
  row: TenantUserRow;
  busy: boolean;
  onResend: () => void;
  onSuspend: () => void;
  onReactivate: () => void;
  onForceReset: () => void;
  onRevoke: () => void;
}) {
  const s = row.user.status;
  if (s === 'closed') return <span className="muted tiny">—</span>;
  return (
    <div className="row" style={{ gap: 4 }}>
      {s === 'pending' && (
        <button className="btn btn-sm" disabled={busy} onClick={onResend}>Resend invite</button>
      )}
      {s === 'active' && (
        <>
          <button className="btn btn-sm" disabled={busy} onClick={onForceReset}>Reset password</button>
          <button className="btn btn-sm btn-ghost" disabled={busy} onClick={onSuspend}>Suspend</button>
        </>
      )}
      {s === 'suspended' && (
        <button className="btn btn-sm btn-primary" disabled={busy} onClick={onReactivate}>Reactivate</button>
      )}
      <button className="btn btn-sm btn-danger" disabled={busy} onClick={onRevoke}>Revoke</button>
    </div>
  );
}

// ─────────── Credits card ───────────
//
// Inline replacement for the old PlatformCredits modal. Reuses the
// per-channel TopupForm / AdjustmentForm / PricingForm components
// (now exported from PlatformCredits.tsx) and a fresh ledger view.
// Tabbed so the long ledger doesn't push the danger zone off-screen.

const CHANNEL_LABEL: Record<CreditChannel, string> = { sms: 'SMS', email: 'Email' };

function TenantCreditsCard({ tenantID }: { tenantID: string }) {
  type View = 'top-up' | 'adjust' | 'ledger' | 'pricing';
  const [view, setView] = useState<View>('top-up');
  const [detail, setDetail] = useState<{ balances: CreditBalance[]; pricing: CreditPricing[] } | null>(null);
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([]);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const [d, l] = await Promise.all([
        getPlatformTenantDetail(tenantID),
        platformLedger(tenantID, { limit: 50 }),
      ]);
      setDetail(d);
      setLedger(l.items ?? []);
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [tenantID]);

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Notification credits</h3>
        <span className="card-sub">SMS + email prepaid balance, ledger, pricing, top-up &amp; adjustment actions.</span>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error" style={{ marginBottom: 10 }}>{err}</div>}

        {/* Balance + last-topup summary */}
        {detail && (
          <div className="row" style={{ gap: 20, flexWrap: 'wrap', marginBottom: 14 }}>
            {detail.balances.map((b) => {
              const zero = b.balance < 1;
              const low = !zero && b.low_balance_threshold > 0 && b.balance <= b.low_balance_threshold;
              return (
                <div key={b.channel} style={{ minWidth: 140 }}>
                  <div className="muted tiny" style={{ marginBottom: 2 }}>{CHANNEL_LABEL[b.channel]} balance</div>
                  <div style={{
                    fontSize: 28, fontWeight: 700, lineHeight: 1,
                    color: zero ? 'var(--neg)' : low ? 'var(--warn)' : undefined,
                  }}>
                    {b.balance.toLocaleString()}
                  </div>
                  {b.last_topup_at && (
                    <div className="muted tiny" style={{ marginTop: 4 }}>
                      Last top-up {new Date(b.last_topup_at).toLocaleDateString()}
                      {b.last_topup_credits != null && ` (+${b.last_topup_credits})`}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}

        {/* Sub-tabs for the four action surfaces */}
        <div className="tabs" style={{ padding: 0, marginBottom: 10 }}>
          {[
            { id: 'top-up' as const,   label: 'Top up' },
            { id: 'adjust' as const,   label: 'Adjust (maker)' },
            { id: 'ledger' as const,   label: 'Ledger' },
            { id: 'pricing' as const,  label: 'Pricing' },
          ].map((v) => (
            <div
              key={v.id}
              className="tab"
              data-active={view === v.id || undefined}
              onClick={() => setView(v.id)}
            >
              {v.label}
            </div>
          ))}
        </div>

        {view === 'top-up'  && <TopupForm tenantID={tenantID} onCompleted={load} />}
        {view === 'adjust'  && <AdjustmentForm tenantID={tenantID} onCompleted={load} />}
        {view === 'ledger'  && <LedgerTable entries={ledger} />}
        {view === 'pricing' && detail && (
          <PricingForm tenantID={tenantID} pricing={detail.pricing} onSaved={load} />
        )}
      </div>
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
