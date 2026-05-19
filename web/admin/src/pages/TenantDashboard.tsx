import { useEffect, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import { listUsers, type ApiUser, extractError } from '../api/client';
import SecurityCard from '../components/SecurityCard';

export default function TenantDashboard() {
  const { user, tenant, roles, permissions, hasPermission } = useAuth();
  const [users, setUsers] = useState<ApiUser[] | null>(null);
  const [usersErr, setUsersErr] = useState<string | null>(null);

  useEffect(() => {
    if (!hasPermission('users:view')) return;
    listUsers()
      .then((r) => setUsers(r.users))
      .catch((e) => setUsersErr(extractError(e)));
  }, [hasPermission]);

  return (
    <main className="page">
      <div className="eyebrow">{tenant?.name}</div>
      <h1>Welcome, {user?.full_name.split(' ')[0]}.</h1>
      <p className="muted tiny">
        Roles: {roles.join(', ') || '—'} · {permissions.length} permissions
      </p>

      <div style={{ height: 14 }} />

      <SecurityCard />

      <div className="card">
        <h3>Your tenant</h3>
        <dl className="kv">
          <dt>Name</dt><dd className="mono">{tenant?.name}</dd>
          <dt>Slug</dt><dd className="mono">{tenant?.slug}</dd>
          <dt>Kind</dt><dd className="mono">{tenant?.kind}</dd>
          <dt>Status</dt><dd className="mono">{tenant?.status}</dd>
          <dt>Currency</dt><dd className="mono">{tenant?.currency_code}</dd>
          <dt>Country</dt><dd className="mono">{tenant?.country_code}</dd>
          {tenant?.license_no && (<><dt>License</dt><dd className="mono">{tenant.license_no}</dd></>)}
        </dl>
      </div>

      {hasPermission('users:view') && (
        <div className="card">
          <h3>Users</h3>
          {usersErr && <div className="alert alert-error">{usersErr}</div>}
          {!users && !usersErr && <p className="muted tiny">Loading…</p>}
          {users && (
            <table className="tbl">
              <thead>
                <tr><th>Name</th><th>Email</th><th>Status</th><th>Platform admin</th><th>Joined</th></tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id}>
                    <td>{u.full_name}</td>
                    <td className="mono">{u.email}</td>
                    <td>
                      <span className={u.status === 'active' ? 'badge badge-pos' : 'badge'}>{u.status}</span>
                    </td>
                    <td>{u.is_platform_admin ? 'yes' : '—'}</td>
                    <td className="mono">{new Date(u.created_at).toISOString().slice(0, 10)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </main>
  );
}
