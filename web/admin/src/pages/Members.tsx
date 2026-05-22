// Members list — pending review queue + active register, with approve /
// reject inline actions. "Onboard member" link routes to the wizard.

import { useEffect, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  approveMember,
  getMemberStatusCounts,
  listMembers,
  rejectMember,
  extractError,
  type ApiMember,
  type MemberStatus,
  type MemberStatusCounts,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

type Filter = 'all' | MemberStatus;

export default function Members() {
  const { hasPermission } = useAuth();
  const canCreate = hasPermission('members:create');
  const canApprove = hasPermission('members:approve');

  const [members, setMembers] = useState<ApiMember[] | null>(null);
  const [total, setTotal] = useState(0);
  const [filter, setFilter] = useState<Filter>('all');
  const [q, setQ] = useState('');
  const [loadErr, setLoadErr] = useState<string | null>(null);
  // Canonical KPI numbers — fetched from /v1/members/status/counts so
  // they match the dashboard widget exactly. The previous implementation
  // computed these client-side from the loaded page of members, which
  // (a) skewed under pagination and (b) silently absorbed members in
  // "off-bucket" statuses (blacklisted / dormant / etc.) into a
  // mis-labelled "total" line.
  const [counts, setCounts] = useState<MemberStatusCounts | null>(null);

  async function reload() {
    setLoadErr(null);
    try {
      const [r, c] = await Promise.all([
        listMembers({
          status: filter === 'all' ? undefined : filter,
          q: q || undefined,
          limit: 100,
        }),
        getMemberStatusCounts(),
      ]);
      setMembers(r.members);
      setTotal(r.total);
      setCounts(c);
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }

  useEffect(() => { void reload(); }, [filter]);

  async function onApprove(m: ApiMember) {
    if (!confirm(`Approve ${m.full_name} (${m.member_no})?`)) return;
    try {
      await approveMember(m.id);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }

  async function onReject(m: ApiMember) {
    const reason = prompt(`Reject ${m.full_name}. Reason?`);
    if (!reason) return;
    try {
      await rejectMember(m.id, reason);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }


  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Members · Directory</div>
          <h1>Members</h1>
          <div className="page-sub">
            Individual savings & credit cooperative members.
            {total > 0 && <> · {total.toLocaleString()} total</>}
          </div>
        </div>
        <div className="page-hd-actions">
          {canCreate && (
            <a href="/members/new" className="btn btn-sm btn-accent">
              <Icon name="plus" size={13} /> Onboard member
            </a>
          )}
        </div>
      </div>

      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      <MemberRollCallKPIs counts={counts} />

      <div className="card">
        <div className="card-hd">
          <h3>Register</h3>
          <span className="card-sub">{members?.length ?? 0} shown</span>
          <div className="card-hd-actions">
            <div className="fchips">
              {(['all', 'pending', 'active', 'dormant', 'suspended', 'blacklisted', 'exited', 'deceased', 'rejected'] as Filter[]).map((f) => (
                <button
                  key={f}
                  type="button"
                  className="fchip"
                  data-active={filter === f || undefined}
                  onClick={() => setFilter(f)}
                >
                  {f}
                </button>
              ))}
            </div>
            <form
              onSubmit={(e) => { e.preventDefault(); void reload(); }}
              style={{ display: 'flex', gap: 4 }}
            >
              <input
                className="input"
                style={{ height: 26, fontSize: 12, width: 200 }}
                placeholder="Search name / member #"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
          </div>
        </div>
        <div className="card-body flush">
          {!members && !loadErr && <div className="empty">Loading…</div>}
          {members && members.length === 0 && (
            <div className="empty">
              {filter === 'all' ? 'No members yet. ' : `No members with status "${filter}". `}
              {canCreate && filter === 'all' && (
                <a href="/members/new" style={{ color: 'var(--accent)' }}>Onboard the first one →</a>
              )}
            </div>
          )}
          {members && members.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 44 }}></th>
                  <th>Member</th>
                  <th>ID / KRA PIN</th>
                  <th>Contact</th>
                  <th>Status</th>
                  <th>Joined</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {members.map((m) => (
                  <tr key={m.id}>
                    <td><Avatar name={m.full_name} size="sm" /></td>
                    <td>
                      <div style={{ fontWeight: 500 }}>
                        <a href={`/members/${m.id}`} className="tbl-link">{m.full_name}</a>
                      </div>
                      <div className="tiny-mono">{m.member_no}</div>
                    </td>
                    <td>
                      <div className="tiny-mono">{m.id_doc_number}</div>
                      {m.kra_pin && <div className="muted tiny mono">{m.kra_pin}</div>}
                    </td>
                    <td>
                      {m.phone && <div className="tiny-mono">{m.phone}</div>}
                      {m.email && <div className="muted tiny">{m.email}</div>}
                    </td>
                    <td>
                      <StatusBadge status={m.status} />
                      {m.status === 'rejected' && m.rejection_reason && (
                        <div className="muted tiny" title={m.rejection_reason}>
                          {m.rejection_reason.length > 30 ? m.rejection_reason.slice(0, 30) + '…' : m.rejection_reason}
                        </div>
                      )}
                    </td>
                    <td className="tiny-mono">{new Date(m.created_at).toISOString().slice(0, 10)}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                        {canApprove && m.status === 'pending' && (
                          <>
                            <button
                              className="btn btn-sm"
                              style={{ color: 'var(--pos)' }}
                              title="Approve"
                              onClick={() => void onApprove(m)}
                            >
                              <Icon name="check" size={12} />
                            </button>
                            <button
                              className="btn btn-sm"
                              style={{ color: 'var(--neg)' }}
                              title="Reject"
                              onClick={() => void onReject(m)}
                            >
                              <Icon name="x" size={12} />
                            </button>
                          </>
                        )}
                        <a className="btn btn-sm" href={`/members/${m.id}`} title="View">
                          <Icon name="eye" size={12} />
                        </a>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {filter === 'pending' && (members?.length ?? 0) > 0 && (
        <p className="muted tiny" style={{ marginTop: 8 }}>
          Pending members need approval from a user with the <Badge tone="accent">members:approve</Badge> permission.
        </p>
      )}
    </div>
  );
}

// MemberRollCallKPIs renders the four canonical roll-call numbers from
// member_status_counts(tenant_id). The dashboard widget consumes the
// same underlying fields, so the two views display identical numbers
// for the same fixture by construction. See AsyncPanel-style discipline
// note in api/client.ts MemberStatusCounts.
export function MemberRollCallKPIs({ counts }: { counts: MemberStatusCounts | null }) {
  return (
    <div className="grid-4" style={{ marginBottom: 14 }}>
      <KPICard label="On register"    value={counts?.total_on_register ?? 0} />
      <KPICard label="Active"         value={counts?.total_active_servicing ?? 0} tone="pos" />
      <KPICard label="Pending review" value={counts?.pending ?? 0} tone="warn" />
      <KPICard label="Rejected"       value={counts?.rejected ?? 0} tone="neg" />
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
