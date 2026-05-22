// Members — the unified counterparty register. Reads exclusively
// from /v1/counterparties (the dual-mode legacy listMembers branch
// was removed in the Phase D drop). Kind filter (all / individual /
// institutional) is driven by the URL's ?kind= param so /orgs can
// deep-link via /members?kind=institutional.

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  getMemberStatusCounts,
  listCounterparties,
  extractError,
  INSTITUTIONAL_KINDS,
  type Counterparty,
  type CounterpartyKind,
  type MemberStatus,
  type MemberStatusCounts,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

type Filter = 'all' | MemberStatus;
type KindFilter = 'all' | 'individual' | 'institutional';

// RegisterRow is the post-mapping shape the table renders. The
// dual-mode shape from earlier (where legacy ApiMember could also
// produce a row) was collapsed alongside the listMembers fallback.
type RegisterRow = {
  // detailHref — kind-aware destination URL. Routes to /members/<id>
  // for individuals and /orgs/<id> for institutions so the unified
  // CounterpartyProfile can pick the right legacy-target lookup.
  id: string;          // table key only — NEVER linked to directly
  detailHref: string;
  displayName: string;
  kind: CounterpartyKind;
  status: string;
  cpNumber?: string | null;
  legacyId?: string | null;
  phone?: string | null;
  email?: string | null;
  idDocNumber?: string | null;
  kraPIN?: string | null;
  joinedAt: string;
};

export default function Members() {
  const { hasPermission } = useAuth();
  const canCreate = hasPermission('members:create');

  // Initial kind filter — pre-applied via ?kind= so /orgs can deep-link.
  const initialKind = useMemo<KindFilter>(() => {
    const v = new URLSearchParams(window.location.search).get('kind');
    if (v === 'institutional' || v === 'individual') return v;
    return 'all';
  }, []);
  const [kindFilter, setKindFilter] = useState<KindFilter>(initialKind);

  const [rows, setRows] = useState<RegisterRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [filter, setFilter] = useState<Filter>('all');
  const [q, setQ] = useState('');
  const [loadErr, setLoadErr] = useState<string | null>(null);
  // Canonical KPI numbers — fetched from /v1/members/status/counts so
  // they match the dashboard widget exactly.
  const [counts, setCounts] = useState<MemberStatusCounts | null>(null);

  async function reload() {
    setLoadErr(null);
    try {
      const kinds: CounterpartyKind[] | undefined =
        kindFilter === 'individual'    ? ['individual'] :
        kindFilter === 'institutional' ? INSTITUTIONAL_KINDS :
        undefined;
      const [r, c] = await Promise.all([
        listCounterparties({
          kind: kinds,
          status: filter === 'all' ? undefined : (filter as Counterparty['status']),
          q: q || undefined,
          limit: 100,
        }),
        getMemberStatusCounts(),
      ]);
      setRows(r.counterparties.map(cpToRow));
      setTotal(r.total);
      setCounts(c);
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }

  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filter, kindFilter]);

  // Keep the URL in sync with the kind chip so a copied link
  // reproduces the user's view.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (kindFilter === 'all') url.searchParams.delete('kind');
    else url.searchParams.set('kind', kindFilter);
    window.history.replaceState({}, '', url);
  }, [kindFilter]);


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
            <a href="/applications/new" className="btn btn-sm btn-accent">
              <Icon name="plus" size={13} /> Onboard counterparty
            </a>
          )}
        </div>
      </div>

      {loadErr && <div className="alert alert-error">{loadErr}</div>}

      <MemberRollCallKPIs counts={counts} />

      <div className="card">
        <div className="card-hd">
          <h3>Register</h3>
          <span className="card-sub">
            {rows?.length ?? 0} shown
            {kindFilter !== 'all' && (
              <> · scope: <strong>{kindFilter === 'individual' ? 'individuals' : 'organisations'}</strong>
                <button
                  className="btn btn-sm"
                  style={{ marginLeft: 6 }}
                  onClick={() => setKindFilter('all')}
                  title="Show all kinds"
                >clear</button>
              </>
            )}
          </span>
          <div className="card-hd-actions">
            {/* Kind chip strip — drives ?kind=… on the URL so /orgs
                and /members?kind=… both reproduce the user's view. */}
            <div className="fchips">
              {(['all', 'individual', 'institutional'] as KindFilter[]).map((k) => (
                <button
                  key={k}
                  type="button"
                  className="fchip"
                  data-active={kindFilter === k || undefined}
                  onClick={() => setKindFilter(k)}
                >
                  {k === 'all' ? 'all kinds' : k}
                </button>
              ))}
            </div>
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
                placeholder="Search name / CP# / legacy #"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
          </div>
        </div>
        <div className="card-body flush">
          {!rows && !loadErr && <div className="empty">Loading…</div>}
          {rows && rows.length === 0 && (
            <div className="empty">
              {filter === 'all' && kindFilter === 'all' ? 'No counterparties yet. ' : 'No matches. '}
              {canCreate && filter === 'all' && kindFilter === 'all' && (
                <a href="/applications/new" style={{ color: 'var(--accent)' }}>Start an application →</a>
              )}
            </div>
          )}
          {rows && rows.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 44 }}></th>
                  <th>Name</th>
                  <th>Kind</th>
                  <th>CP # / Legacy</th>
                  <th>Contact</th>
                  <th>Status</th>
                  <th>Joined</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((m) => (
                  <tr key={m.id}>
                    <td><Avatar name={m.displayName} size="sm" /></td>
                    <td>
                      <div style={{ fontWeight: 500 }}>
                        <a href={m.detailHref} className="tbl-link">{m.displayName}</a>
                      </div>
                    </td>
                    <td><KindBadge kind={m.kind} /></td>
                    <td>
                      {m.cpNumber && <div className="tiny-mono">{m.cpNumber}</div>}
                      {m.legacyId && <div className="muted tiny mono">{m.legacyId}</div>}
                    </td>
                    <td>
                      {m.phone && <div className="tiny-mono">{m.phone}</div>}
                      {m.email && <div className="muted tiny">{m.email}</div>}
                    </td>
                    <td>
                      <StatusBadge status={m.status} />
                    </td>
                    <td className="tiny-mono">{m.joinedAt.slice(0, 10)}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                        {/* Approve/reject row actions moved to the
                            CounterpartyProfile status workflow card. */}
                        <a className="btn btn-sm" href={m.detailHref} title="View">
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

      {filter === 'pending' && (rows?.length ?? 0) > 0 && (
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

// ─────────── Row mapper + helpers ───────────

function cpToRow(c: Counterparty): RegisterRow {
  const ind = (c.individual ?? {}) as Record<string, unknown>;
  const ct  = (c.contact     ?? {}) as Record<string, unknown>;
  const str = (v: unknown): string | null => (typeof v === 'string' && v ? v : null);
  // Route via the bridge id, not the counterparty id. Individuals
  // land in /members/<member.id>; institutions in /orgs/<org.id>.
  // Both paths render through CounterpartyProfile.
  const target = c.legacy_target_id ?? c.id;
  const detailHref =
    c.kind === 'individual' ? `/members/${target}` : `/orgs/${target}`;
  return {
    id: c.id,
    detailHref,
    displayName: c.display_name,
    kind: c.kind,
    status: c.status,
    cpNumber: c.cp_number,
    legacyId: c.legacy_id ?? null,
    phone: str(ct['phone']),
    email: str(ct['email']),
    idDocNumber: str(ind['id_doc_number']),
    kraPIN: str(ind['kra_pin']),
    joinedAt: c.joined_at,
  };
}

// Kind label/tone for the kind column. Individual gets a neutral
// pill; institutional kinds use the accent palette so the eye picks
// them out in a mixed register.
function KindBadge({ kind }: { kind: CounterpartyKind }) {
  if (kind === 'individual') {
    return <Badge tone="neutral">Individual</Badge>;
  }
  const labels: Record<Exclude<CounterpartyKind, 'individual'>, string> = {
    chama: 'Chama', company: 'Company', ngo: 'NGO',
    church: 'Church', school: 'School', other: 'Org',
  };
  return <Badge tone="accent">{labels[kind]}</Badge>;
}
