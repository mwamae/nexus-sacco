// Staff user admin. Lists users in the current tenant (or the platform
// pseudo-tenant when on the platform host), and lets admins with
// users:invite invite new ones via email.

import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  listUsers,
  listRoles,
  inviteUser,
  resendInvite,
  setUserStatus,
  assignUserRole,
  unassignUserRole,
  extractError,
  type ApiUserWithRoles,
  type ApiRole,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

export default function Users() {
  const { hasPermission, user: me } = useAuth();
  const canInvite = hasPermission('users:invite');
  const canSuspend = hasPermission('users:suspend');
  const canEditRoles = hasPermission('roles:edit');

  const [users, setUsers] = useState<ApiUserWithRoles[] | null>(null);
  const [roles, setRoles] = useState<ApiRole[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [showInvite, setShowInvite] = useState(false);
  const [editingRoles, setEditingRoles] = useState<ApiUserWithRoles | null>(null);

  async function reload() {
    setLoadErr(null);
    try {
      const [u, r] = await Promise.all([listUsers(), listRoles()]);
      setUsers(u.users);
      setRoles(r);
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, []);

  async function toggleStatus(u: ApiUserWithRoles) {
    const next = u.status === 'active' ? 'suspended' : 'active';
    if (!confirm(`Set ${u.email} to "${next}"?`)) return;
    try {
      await setUserStatus(u.id, next);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }

  async function onResend(u: ApiUserWithRoles) {
    try {
      await resendInvite(u.id);
      alert(`Invite re-sent to ${u.email}`);
    } catch (e) {
      alert(extractError(e));
    }
  }

  const tally = useMemo(() => {
    const acc = { total: 0, active: 0, pending: 0, suspended: 0 };
    for (const u of users ?? []) {
      acc.total++;
      if (u.status === 'active') acc.active++;
      else if (u.status === 'pending') acc.pending++;
      else if (u.status === 'suspended') acc.suspended++;
    }
    return acc;
  }, [users]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Administration · Staff</div>
          <h1>Staff users</h1>
          <div className="page-sub">
            Operations staff (admins, tellers, accountants). Not SACCO members. Invite by email; users set their own password.
          </div>
        </div>
        <div className="page-hd-actions">
          {canInvite && (
            <button className="btn btn-sm btn-accent" onClick={() => setShowInvite((s) => !s)}>
              <Icon name={showInvite ? 'x' : 'plus'} size={13} />
              {showInvite ? 'Cancel' : 'Invite staff'}
            </button>
          )}
        </div>
      </div>

      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPICard label="Total" value={tally.total} />
        <KPICard label="Active" value={tally.active} tone="pos" />
        <KPICard label="Pending invite" value={tally.pending} tone="warn" />
        <KPICard label="Suspended" value={tally.suspended} tone="neg" />
      </div>

      {showInvite && roles && (
        <InviteForm
          roles={roles.filter((r) => r.code !== 'member')}
          onClose={() => setShowInvite(false)}
          onInvited={async () => {
            setShowInvite(false);
            await reload();
          }}
        />
      )}

      {editingRoles && roles && (
        <RoleEditor
          user={editingRoles}
          allRoles={roles.filter((r) => r.code !== 'member')}
          onClose={() => setEditingRoles(null)}
          onChanged={async () => { await reload(); }}
        />
      )}

      <div className="card">
        <div className="card-hd">
          <h3>All staff</h3>
          <span className="card-sub">{users?.length ?? 0} total</span>
        </div>
        <div className="card-body flush">
          {!users && !loadErr && <div className="empty">Loading…</div>}
          {users && users.length === 0 && <div className="empty">No staff yet.</div>}
          {users && users.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 44 }}></th>
                  <th>Name</th>
                  <th>Roles</th>
                  <th>Status</th>
                  <th>Joined</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id}>
                    <td><Avatar name={u.full_name} size="sm" /></td>
                    <td>
                      <div style={{ fontWeight: 500 }}>
                        {u.full_name}
                        {u.is_platform_admin && (
                          <Badge tone="accent">platform</Badge>
                        )}
                      </div>
                      <div className="tiny-mono">{u.email}</div>
                    </td>
                    <td className="tiny">
                      {(u.roles || []).length === 0 ? (
                        <span className="muted">—</span>
                      ) : (
                        <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 4 }}>
                          {(u.roles || []).map((r) => (
                            <Badge key={r.id} tone={r.is_system ? 'neutral' : 'accent'}>{r.code}</Badge>
                          ))}
                        </span>
                      )}
                    </td>
                    <td><StatusBadge status={u.status} /></td>
                    <td className="tiny-mono">{new Date(u.created_at).toISOString().slice(0, 10)}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                        {canEditRoles && u.id !== me?.id && (
                          <button className="btn btn-sm" title="Edit roles" onClick={() => setEditingRoles(u)}>
                            <Icon name="key" size={12} />
                          </button>
                        )}
                        {canInvite && u.status === 'pending' && (
                          <button className="btn btn-sm" title="Resend invite" onClick={() => void onResend(u)}>
                            <Icon name="mail" size={12} />
                          </button>
                        )}
                        {canSuspend && u.id !== me?.id && u.status !== 'pending' && (
                          <button
                            className="btn btn-sm"
                            title={u.status === 'active' ? 'Suspend' : 'Reactivate'}
                            style={u.status === 'active' ? { color: 'var(--neg)' } : { color: 'var(--pos)' }}
                            onClick={() => void toggleStatus(u)}
                          >
                            <Icon name={u.status === 'active' ? 'lock' : 'check'} size={12} />
                          </button>
                        )}
                      </div>
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

function KPICard({ label, value, tone }: { label: string; value: number; tone?: 'pos' | 'neg' | 'warn' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
      </div>
    </div>
  );
}

function InviteForm({
  roles,
  onClose,
  onInvited,
}: {
  roles: ApiRole[];
  onClose: () => void;
  onInvited: () => void | Promise<void>;
}) {
  const [email, setEmail] = useState('');
  const [fullName, setFullName] = useState('');
  const [phone, setPhone] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function toggle(code: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(code)) next.delete(code); else next.add(code);
      return next;
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    if (selected.size === 0) {
      setErr('Select at least one role.');
      return;
    }
    setBusy(true);
    try {
      await inviteUser({ email, full_name: fullName, phone, role_codes: Array.from(selected) });
      await onInvited();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card" style={{ borderColor: 'var(--accent)', marginBottom: 14 }}>
      <div className="card-hd">
        <h3>Invite staff</h3>
        <span className="card-sub">They'll receive an email with a link to set their password.</span>
        <div className="card-hd-actions">
          <button className="btn btn-sm btn-ghost" onClick={onClose} aria-label="Close">
            <Icon name="x" size={13} />
          </button>
        </div>
      </div>
      <form onSubmit={submit} className="card-body">
        {err && <div className="alert alert-error">{err}</div>}
        <div className="grid-3">
          <div className="field">
            <label className="field-label">Email <span className="req">*</span></label>
            <input className="input" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </div>
          <div className="field">
            <label className="field-label">Full name <span className="req">*</span></label>
            <input className="input" required value={fullName} onChange={(e) => setFullName(e.target.value)} />
          </div>
          <div className="field">
            <label className="field-label">Phone</label>
            <input className="input mono" value={phone} onChange={(e) => setPhone(e.target.value)} />
          </div>
        </div>

        <div className="divider" />
        <div className="h-sec">Roles ({selected.size} selected)</div>
        <div className="fchips">
          {roles.map((r) => (
            <button
              type="button"
              key={r.id}
              className="fchip"
              data-active={selected.has(r.code) || undefined}
              onClick={() => toggle(r.code)}
            >
              {selected.has(r.code) && <Icon name="check" size={11} />}
              <span>{r.name}</span>
              <span className="tiny-mono" style={{ opacity: 0.7 }}>{r.code}</span>
            </button>
          ))}
        </div>

        <div className="row" style={{ marginTop: 16, gap: 8 }}>
          <button type="submit" className="btn btn-accent" disabled={busy}>
            <Icon name="mail" size={13} />
            {busy ? 'Sending invite…' : 'Send invite'}
          </button>
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        </div>
      </form>
    </div>
  );
}

function RoleEditor({
  user,
  allRoles,
  onClose,
  onChanged,
}: {
  user: ApiUserWithRoles;
  allRoles: ApiRole[];
  onClose: () => void;
  onChanged: () => void | Promise<void>;
}) {
  const assignedIds = useMemo(() => new Set((user.roles || []).map((r) => r.id)), [user.roles]);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function toggle(role: ApiRole) {
    setErr(null);
    setBusy(role.id);
    try {
      if (assignedIds.has(role.id)) {
        await unassignUserRole(user.id, role.id);
      } else {
        await assignUserRole(user.id, role.code);
      }
      await onChanged();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="card" style={{ borderColor: 'var(--accent)', marginBottom: 14 }}>
      <div className="card-hd">
        <h3>Roles · {user.full_name}</h3>
        <span className="card-sub">{user.email}</span>
        <div className="card-hd-actions">
          <button className="btn btn-sm btn-ghost" onClick={onClose} aria-label="Close">
            <Icon name="x" size={13} />
          </button>
        </div>
      </div>
      <div className="card-body">
        {err && <div className="alert alert-error">{err}</div>}
        <div className="col" style={{ gap: 6 }}>
          {allRoles.map((r) => {
            const on = assignedIds.has(r.id);
            return (
              <label
                key={r.id}
                style={{
                  display: 'flex', alignItems: 'center', justifyContent: 'space-between',
                  padding: '8px 12px', border: '1px solid var(--border)',
                  borderRadius: 'var(--r-md)',
                  background: on ? 'var(--accent-bg)' : 'var(--surface)',
                  cursor: busy ? 'wait' : 'pointer',
                }}
              >
                <div>
                  <div>
                    <strong>{r.name}</strong>
                    <span className="tiny-mono" style={{ marginLeft: 8, color: 'var(--accent-fg)' }}>{r.code}</span>
                    {r.is_system && <span style={{ marginLeft: 8 }}><Badge tone="neutral">system</Badge></span>}
                  </div>
                  {r.description && <div className="muted tiny">{r.description}</div>}
                </div>
                <input
                  type="checkbox"
                  checked={on}
                  disabled={busy === r.id}
                  onChange={() => void toggle(r)}
                  style={{ accentColor: 'var(--accent)' }}
                />
              </label>
            );
          })}
        </div>
      </div>
    </div>
  );
}
