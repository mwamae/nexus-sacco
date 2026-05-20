import { useEffect, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  getMemberStatusSummary, listUsers, runDormancy,
  type ApiUserWithRoles, type MemberStatus, type MemberStatusSummary,
  extractError,
} from '../api/client';
import SecurityCard from '../components/SecurityCard';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';

export default function TenantDashboard() {
  const { user, tenant, roles, permissions, hasPermission } = useAuth();
  const [users, setUsers] = useState<ApiUserWithRoles[] | null>(null);
  const [usersErr, setUsersErr] = useState<string | null>(null);
  const [statusSummary, setStatusSummary] = useState<MemberStatusSummary | null>(null);
  const [dormancyBusy, setDormancyBusy] = useState(false);
  const canViewMembers = hasPermission('members:view');
  const canEditMembers = hasPermission('members:edit');

  useEffect(() => {
    if (!hasPermission('users:view')) return;
    listUsers()
      .then((r) => setUsers(r.users))
      .catch((e) => setUsersErr(extractError(e)));
  }, [hasPermission]);

  useEffect(() => {
    if (!canViewMembers) return;
    void getMemberStatusSummary().then(setStatusSummary).catch(() => {});
  }, [canViewMembers]);

  async function onRunDormancy() {
    if (!confirm('Run the dormancy detector now? Active members above the inactivity threshold will be moved to dormant.')) return;
    setDormancyBusy(true);
    try {
      const r = await runDormancy();
      alert(`Marked ${r.applied?.length ?? 0} member${r.applied?.length === 1 ? '' : 's'} as dormant (threshold ${r.threshold_days} days).`);
      const s = await getMemberStatusSummary();
      setStatusSummary(s);
    } catch (e) {
      alert(extractError(e));
    } finally {
      setDormancyBusy(false);
    }
  }

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

      {canViewMembers && statusSummary && (
        <MemberStatusPanel
          summary={statusSummary}
          canRun={canEditMembers}
          busy={dormancyBusy}
          onRun={onRunDormancy}
        />
      )}

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

      {/* Staff card kept below */}
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

// ─────────── Member status panel (KPIs + dormancy pipeline + recent changes) ───────────

const STATUS_ORDER: MemberStatus[] = [
  'active', 'dormant', 'pending', 'suspended',
  'blacklisted', 'exited', 'deceased', 'rejected',
];

function MemberStatusPanel({ summary, canRun, busy, onRun }: {
  summary: MemberStatusSummary;
  canRun: boolean;
  busy: boolean;
  onRun: () => void | Promise<void>;
}) {
  const total = Object.values(summary.by_status).reduce((acc, n) => acc + (n ?? 0), 0);
  const pipelineCount = summary.dormancy_pipeline.length;
  return (
    <>
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Members — status overview</h3>
          <span className="card-sub">{total} on register</span>
          <div className="card-hd-actions">
            <a className="btn btn-sm" href="/members">Open register →</a>
            {canRun && (
              <button className="btn btn-sm" disabled={busy} onClick={() => void onRun()}>
                {busy ? 'Running dormancy…' : 'Run dormancy detector'}
              </button>
            )}
          </div>
        </div>
        <div className="card-body">
          <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
            {STATUS_ORDER.map((s) => (
              <a
                key={s}
                href={`/members?status=${s}`}
                style={{
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                  padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 'var(--r-md)',
                  background: 'var(--surface)', textDecoration: 'none',
                }}
              >
                <StatusBadge status={s} />
                <strong className="mono">{summary.by_status[s] ?? 0}</strong>
              </a>
            ))}
          </div>
          <p className="muted tiny" style={{ marginTop: 12 }}>
            Dormancy threshold: <strong>{summary.dormancy_threshold_days} days</strong>{' '}
            of inactivity. Configurable in tenant Settings → Operations.
          </p>
        </div>
      </div>

      {pipelineCount > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Approaching dormancy</h3>
            <span className="card-sub">{pipelineCount} active member{pipelineCount === 1 ? '' : 's'} within 30 days of the threshold</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Member</th>
                  <th>Last activity</th>
                  <th style={{ textAlign: 'right' }}>Days inactive</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {summary.dormancy_pipeline.map((c) => (
                  <tr key={c.member_id}>
                    <td>
                      <a href={`/members/${c.member_id}`} className="tbl-link">{c.full_name}</a>
                      <div className="tiny-mono">{c.member_no}</div>
                    </td>
                    <td className="tiny-mono">{c.last_activity_at ? c.last_activity_at.slice(0, 10) : '— never'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{c.days_inactive}</td>
                    <td><a className="btn btn-sm" href={`/members/${c.member_id}?tab=profile`}>View</a></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {summary.recent_changes.length > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Recent status changes</h3>
            <span className="card-sub">{summary.recent_changes.length} latest</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Member</th>
                  <th>Change</th>
                  <th>Reason</th>
                  <th>When</th>
                </tr>
              </thead>
              <tbody>
                {summary.recent_changes.slice(0, 10).map((c) => (
                  <tr key={c.id}>
                    <td>
                      <a href={`/members/${c.member_id}`} className="tbl-link">{c.full_name}</a>
                      <div className="tiny-mono">{c.member_no}</div>
                    </td>
                    <td>
                      {c.from_status && <><StatusBadge status={c.from_status} /> → </>}
                      <StatusBadge status={c.to_status} />
                      {c.workflow_instance_id && <span> · <Badge tone="accent">approval</Badge></span>}
                    </td>
                    <td className="tiny">
                      <div>{c.reason_category.replace(/_/g, ' ')}</div>
                      {c.reason_note && <div className="muted tiny">{c.reason_note}</div>}
                    </td>
                    <td className="tiny-mono">{new Date(c.changed_at).toISOString().replace('T', ' ').slice(0, 16)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </>
  );
}
