import { useEffect, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import { listUsers, type ApiUserWithRoles, extractError } from '../api/client';
import SecurityCard from '../components/SecurityCard';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';

export default function TenantDashboard() {
  const { user, tenant, roles, permissions, hasPermission } = useAuth();
  const [users, setUsers] = useState<ApiUserWithRoles[] | null>(null);
  const [usersErr, setUsersErr] = useState<string | null>(null);

  useEffect(() => {
    if (!hasPermission('users:view')) return;
    listUsers()
      .then((r) => setUsers(r.users))
      .catch((e) => setUsersErr(extractError(e)));
  }, [hasPermission]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name}</div>
          <h1>Welcome, {user?.full_name?.split(' ')[0] ?? 'there'}.</h1>
          <div className="page-sub">
            {roles.join(' · ') || 'no roles'} · {permissions.length} permissions
          </div>
        </div>
      </div>

      <SecurityCard />

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Tenant profile</h3>
          <span className="card-sub">{tenant?.slug}</span>
        </div>
        <div className="card-body">
          <dl className="kvs">
            <dt>Name</dt><dd>{tenant?.name}</dd>
            <dt>Slug</dt><dd>{tenant?.slug}</dd>
            <dt>Kind</dt><dd>{tenant?.kind}</dd>
            <dt>Status</dt><dd><StatusBadge status={tenant?.status ?? '—'} /></dd>
            <dt>Currency</dt><dd>{tenant?.currency_code}</dd>
            <dt>Country</dt><dd>{tenant?.country_code}</dd>
            {tenant?.license_no && (<><dt>License</dt><dd>{tenant.license_no}</dd></>)}
          </dl>
        </div>
      </div>

      {hasPermission('users:view') && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Staff</h3>
            <span className="card-sub">{users?.length ?? 0} total</span>
            <div className="card-hd-actions">
              <a href="/users" className="btn btn-sm">Manage staff →</a>
            </div>
          </div>
          <div className="card-body flush">
            {usersErr && <div style={{ padding: 12 }}><div className="alert alert-error">{usersErr}</div></div>}
            {!users && !usersErr && <div className="empty">Loading…</div>}
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
                  </tr>
                </thead>
                <tbody>
                  {users.slice(0, 8).map((u) => (
                    <tr key={u.id}>
                      <td><Avatar name={u.full_name} size="sm" /></td>
                      <td>
                        <div style={{ fontWeight: 500 }}>{u.full_name}</div>
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
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
