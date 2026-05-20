// Organisations register — pending review queue + active roster for
// non-individual members (groups, chamas, LtdCos, sole props, NGOs,
// churches, sister SACCOs, cooperatives, schools).

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  approveOrg,
  listOrgs,
  rejectOrg,
  extractError,
  type ApiOrg,
  type OrgKind,
  type OrgStatus,
} from '../api/client';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

type Filter = 'all' | OrgStatus;

const KIND_LABEL: Record<OrgKind, string> = {
  group: 'Group', chama: 'Chama', ltd: 'Ltd Co', sole_prop: 'Sole Prop',
  ngo: 'NGO', church: 'Church', sacco: 'SACCO', cooperative: 'Cooperative', school: 'School',
};

const RISK_TONE: Record<string, 'pos' | 'warn' | 'neg' | 'neutral'> = {
  low: 'pos', medium: 'warn', high: 'neg',
};

const KYC_TONE: Record<string, 'neutral' | 'warn' | 'pos' | 'neg'> = {
  not_started: 'neutral', in_review: 'warn', verified: 'pos', rejected: 'neg',
};

export default function Organizations() {
  const { hasPermission } = useAuth();
  const canCreate = hasPermission('members:create');
  const canApprove = hasPermission('members:approve');

  const [orgs, setOrgs] = useState<ApiOrg[] | null>(null);
  const [total, setTotal] = useState(0);
  const [filter, setFilter] = useState<Filter>('all');
  const [q, setQ] = useState('');
  const [loadErr, setLoadErr] = useState<string | null>(null);

  async function reload() {
    setLoadErr(null);
    try {
      const r = await listOrgs({
        status: filter === 'all' ? undefined : filter,
        q: q || undefined,
        limit: 200,
      });
      setOrgs(r.orgs);
      setTotal(r.total);
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, [filter]);

  async function onApprove(o: ApiOrg) {
    if (!confirm(`Approve ${o.registered_name} (${o.org_no})?`)) return;
    try { await approveOrg(o.id); await reload(); }
    catch (e) { alert(extractError(e)); }
  }
  async function onReject(o: ApiOrg) {
    const reason = prompt(`Reject ${o.registered_name}. Reason?`);
    if (!reason) return;
    try { await rejectOrg(o.id, reason); await reload(); }
    catch (e) { alert(extractError(e)); }
  }

  const tally = useMemo(() => {
    const acc = { total: 0, active: 0, pending: 0, rejected: 0, suspended: 0, dormant: 0 };
    for (const o of orgs ?? []) {
      acc.total++;
      const k = o.status as keyof typeof acc;
      if (k in acc) acc[k]++;
    }
    return acc;
  }, [orgs]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Members · Organisations</div>
          <h1>Organisations</h1>
          <div className="page-sub">
            Groups, chamas, companies, NGOs, churches, schools and other non-individual members.
            {total > 0 && <> · {total.toLocaleString()} total</>}
          </div>
        </div>
        <div className="page-hd-actions">
          {canCreate && (
            <a href="/orgs/new" className="btn btn-sm btn-accent">
              <Icon name="plus" size={13} /> Onboard organisation
            </a>
          )}
        </div>
      </div>

      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPICard label="On register" value={tally.total} />
        <KPICard label="Active" value={tally.active} tone="pos" />
        <KPICard label="Pending review" value={tally.pending} tone="warn" />
        <KPICard label="Suspended / dormant" value={tally.suspended + tally.dormant} tone="neg" />
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Register</h3>
          <span className="card-sub">{orgs?.length ?? 0} shown</span>
          <div className="card-hd-actions">
            <div className="fchips">
              {(['all', 'pending', 'active', 'suspended', 'rejected', 'dormant'] as Filter[]).map((f) => (
                <button key={f} type="button" className="fchip" data-active={filter === f || undefined} onClick={() => setFilter(f)}>
                  {f === 'all' ? f : f.replace('_', ' ')}
                </button>
              ))}
            </div>
            <form onSubmit={(e) => { e.preventDefault(); void reload(); }} style={{ display: 'flex', gap: 4 }}>
              <input
                className="input"
                style={{ height: 26, fontSize: 12, width: 220 }}
                placeholder="Search name / org # / registration"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
          </div>
        </div>
        <div className="card-body flush">
          {!orgs && !loadErr && <div className="empty">Loading…</div>}
          {orgs && orgs.length === 0 && (
            <div className="empty">
              {filter === 'all' ? 'No organisations yet. ' : `No organisations with status "${filter}". `}
              {canCreate && filter === 'all' && (
                <a href="/orgs/new" style={{ color: 'var(--accent)' }}>Onboard the first one →</a>
              )}
            </div>
          )}
          {orgs && orgs.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Org #</th>
                  <th>Name</th>
                  <th>Kind</th>
                  <th>Registration</th>
                  <th>Status</th>
                  <th>KYC</th>
                  <th>Risk</th>
                  <th>Joined</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {orgs.map((o) => (
                  <tr key={o.id}>
                    <td className="mono">
                      <a href={`/orgs/${o.id}`} className="tbl-link">{o.org_no}</a>
                    </td>
                    <td>
                      <div style={{ fontWeight: 500 }}>{o.registered_name}</div>
                      {o.trading_name && o.trading_name !== o.registered_name && (
                        <div className="muted tiny">t/a {o.trading_name}</div>
                      )}
                    </td>
                    <td><Badge tone="neutral">{KIND_LABEL[o.kind]}</Badge></td>
                    <td className="tiny-mono">{o.registration_no || <span className="muted">—</span>}</td>
                    <td><StatusBadge status={o.status} /></td>
                    <td><Badge tone={KYC_TONE[o.kyc_status] ?? 'neutral'}>{o.kyc_status.replace('_', ' ')}</Badge></td>
                    <td><Badge tone={RISK_TONE[o.risk_category] ?? 'neutral'}>{o.risk_category}</Badge></td>
                    <td className="tiny-mono">{new Date(o.created_at).toISOString().slice(0, 10)}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                        {canApprove && o.status === 'pending' && (
                          <>
                            <button className="btn btn-sm" style={{ color: 'var(--pos)' }} title="Approve" onClick={() => void onApprove(o)}>
                              <Icon name="check" size={12} />
                            </button>
                            <button className="btn btn-sm" style={{ color: 'var(--neg)' }} title="Reject" onClick={() => void onReject(o)}>
                              <Icon name="x" size={12} />
                            </button>
                          </>
                        )}
                        <a className="btn btn-sm" href={`/orgs/${o.id}`} title="View">
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
