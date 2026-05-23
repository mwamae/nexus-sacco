import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  getMemberStatusSummary, listUsers, runDormancy,
  type ApiUserWithRoles, type MemberStatus, type MemberStatusSummary,
  extractError,
} from '../api/client';
import SecurityCard from '../components/SecurityCard';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { roleLabel } from '../lib/roleLabels';

export default function TenantDashboard() {
  const { user, tenant, roles, hasPermission } = useAuth();
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
    const load = () => { void getMemberStatusSummary().then(setStatusSummary).catch(() => {}); };
    load();
    // Refetch when the tab regains focus / becomes visible. Without this,
    // a snapshot taken on mount drifts whenever members are created or
    // approved in another tab — the source of the "dashboard says 7,
    // Members page says 8" divergence the bug report flagged.
    const onVisible = () => { if (document.visibilityState === 'visible') load(); };
    window.addEventListener('focus', load);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.removeEventListener('focus', load);
      document.removeEventListener('visibilitychange', onVisible);
    };
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

  // Show a one-time welcome banner when the user lands here straight
  // after activating their account (URL has ?welcome=1, set by the
  // AcceptInvite page after auto-login).
  const showWelcome = new URLSearchParams(window.location.search).get('welcome') === '1';

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name}</div>
          <h1>Welcome, {user?.full_name?.split(' ')[0] ?? 'there'}.</h1>
          <div className="page-sub">
            <RoleStrip roles={roles} canManage={hasPermission('roles:view')} />
          </div>
        </div>
      </div>

      {showWelcome && (
        <div className="alert alert-info" style={{ marginBottom: 14 }}>
          <strong>🎉 Your account is active.</strong> You're now signed in as the
          Tenant Super Admin for {tenant?.name}. Complete your SACCO setup from
          the <a href="/settings" style={{ color: 'var(--accent)', fontWeight: 600 }}>
            Settings page
          </a> — start with branding + region, then add staff under <a href="/users"
          style={{ color: 'var(--accent)', fontWeight: 600 }}>Users</a>.
        </div>
      )}

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

// ─────────── Role strip (welcome banner) ───────────
//
// Renders the current user's roles as humanised labels. Shows the
// first three inline; collapses the rest behind "+N more" that
// expands on click. Replaces the old "X permissions" count with a
// link to the roles page for users who can manage them.

const ROLE_PREVIEW_COUNT = 3;

function RoleStrip({ roles, canManage }: { roles: string[]; canManage: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const labels = useMemo(() => roles.map(roleLabel), [roles]);

  if (labels.length === 0) {
    return (
      <>
        <span className="muted">No roles assigned</span>
        {canManage && (
          <>
            {' · '}
            <a href="/roles" style={{ color: 'var(--accent)' }}>Manage roles &amp; permissions →</a>
          </>
        )}
      </>
    );
  }

  const head = labels.slice(0, ROLE_PREVIEW_COUNT);
  const tailCount = labels.length - head.length;
  const visible = expanded ? labels : head;

  return (
    <>
      {visible.join(' · ')}
      {!expanded && tailCount > 0 && (
        <>
          {' '}
          <button
            type="button"
            onClick={() => setExpanded(true)}
            className="link-btn"
            style={{
              background: 'none', border: 'none', padding: 0,
              color: 'var(--accent)', cursor: 'pointer', font: 'inherit',
            }}
            aria-label={`Show ${tailCount} more role${tailCount === 1 ? '' : 's'}`}
          >
            +{tailCount} more
          </button>
        </>
      )}
      {canManage && (
        <>
          {' · '}
          <a href="/roles" style={{ color: 'var(--accent)' }}>Manage roles &amp; permissions →</a>
        </>
      )}
    </>
  );
}

// ─────────── Member status panel (KPIs + dormancy pipeline + recent changes) ───────────

const STATUS_ORDER: MemberStatus[] = [
  'active', 'dormant', 'pending', 'suspended',
  'blacklisted', 'exited', 'deceased', 'rejected',
];

export function MemberStatusPanel({ summary, canRun, busy, onRun }: {
  summary: MemberStatusSummary;
  canRun: boolean;
  busy: boolean;
  onRun: () => void | Promise<void>;
}) {
  // total_on_register comes from member_status_counts(tenant_id), the
  // single source of truth shared with the Members page KPI strip.
  // It deliberately excludes exited / deceased / rejected.
  const total = summary.total_on_register;
  const pipelineCount = summary.dormancy_pipeline.length;
  return (
    <>
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Members — status overview</h3>
          <span className="card-sub">{total} on register · {summary.total_active_servicing} servicing</span>
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
                  <tr key={c.counterparty_id}>
                    <td>
                      <a href={`/members/${c.counterparty_id}`} className="tbl-link">{c.full_name}</a>
                      <div className="tiny-mono">{c.member_no}</div>
                    </td>
                    <td className="tiny-mono">{c.last_activity_at ? c.last_activity_at.slice(0, 10) : '— never'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{c.days_inactive}</td>
                    <td><a className="btn btn-sm" href={`/members/${c.counterparty_id}?tab=profile`}>View</a></td>
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
                      <a href={`/members/${c.counterparty_id}`} className="tbl-link">{c.full_name}</a>
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
