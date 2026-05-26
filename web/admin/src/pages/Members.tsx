// Members — the unified counterparty register.
//
// Every visible number on this page (header total, KPI strip, per-status
// chip badge, "X of Y" footer) derives from a single
// /v1/counterparties/status/counts call scoped to the page's active
// kind / status / search filters. The table rows themselves come from
// /v1/counterparties via listCounterparties — the two endpoints
// share the SAME filter predicates (migration 0022's SQL function
// mirrors CounterpartyStore.ListTx) so the counts and the table can't
// drift even when the page is filtered.
//
// Kind filter (all / individual / institutional) and status chip are
// driven by URL params (?kind= / ?status=) so dashboard tiles can
// deep-link into pre-scoped views.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  getCounterpartyStatusCounts,
  listCounterparties,
  extractError,
  INSTITUTIONAL_KINDS,
  type Counterparty,
  type CounterpartyKind,
  type CounterpartyKindFilter,
  type CounterpartyStatusCounts,
  type MemberStatus,
} from '../api/client';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';
import { useDocumentTitle } from '../lib/useDocumentTitle';

type Filter = 'all' | MemberStatus;
type KindFilter = CounterpartyKindFilter;

// Page size for the register pager. Picked at 50 to keep "Showing
// 1–50 of 250 · Page 1 of 5" feeling like a real pager rather than
// the previous limit:100 cap that silently truncated.
const PAGE_SIZE = 50;

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

  // Initial kind + status filters — pre-applied via ?kind= and
  // ?status= so dashboard chips can deep-link directly into a scoped
  // view. URL parsing happens once on mount; subsequent changes push
  // back into the URL via replaceState.
  const initialKind = useMemo<KindFilter>(() => {
    const v = new URLSearchParams(window.location.search).get('kind');
    if (v === 'institutional' || v === 'individual') return v;
    return 'all';
  }, []);
  const initialFilter = useMemo<Filter>(() => {
    const v = new URLSearchParams(window.location.search).get('status');
    const allowed: Filter[] = [
      'all', 'pending', 'active', 'dormant', 'suspended',
      'blacklisted', 'exited', 'deceased', 'rejected',
    ];
    return allowed.includes(v as Filter) ? (v as Filter) : 'all';
  }, []);

  const [kindFilter, setKindFilter] = useState<KindFilter>(initialKind);
  const [filter, setFilter] = useState<Filter>(initialFilter);
  useDocumentTitle(
    kindFilter === 'institutional' ? 'Organisations'
      : kindFilter === 'individual' ? 'Members'
        : 'Members & organisations',
  );

  const [rows, setRows] = useState<RegisterRow[] | null>(null);
  const [q, setQ] = useState('');
  const [qDebounced, setQDebounced] = useState('');
  const [offset, setOffset] = useState(0);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [showHowCounted, setShowHowCounted] = useState(false);
  // Canonical roll-call numbers from /v1/counterparties/status/counts
  // scoped to the SAME filters the list query uses. Every visible
  // number on the page is derived from this.
  const [counts, setCounts] = useState<CounterpartyStatusCounts | null>(null);

  // Debounce the search input so we don't fire a counts+list pair on
  // every keystroke. 250ms is fast enough that the user perceives
  // "live" feedback without DDOSing the backend.
  useEffect(() => {
    const t = setTimeout(() => setQDebounced(q), 250);
    return () => clearTimeout(t);
  }, [q]);

  // Reset pagination whenever the filter set changes so the user
  // doesn't end up parked at page 4 of a 1-page result set.
  useEffect(() => { setOffset(0); }, [kindFilter, filter, qDebounced]);

  const reload = useCallback(async () => {
    setLoadErr(null);
    try {
      const kinds: CounterpartyKind[] | undefined =
        kindFilter === 'individual'    ? ['individual'] :
        kindFilter === 'institutional' ? INSTITUTIONAL_KINDS :
        undefined;
      const statusArg: MemberStatus[] | undefined =
        filter === 'all' ? undefined : [filter as MemberStatus];
      const [r, c] = await Promise.all([
        listCounterparties({
          kind: kinds,
          status: filter === 'all' ? undefined : (filter as Counterparty['status']),
          q: qDebounced || undefined,
          limit: PAGE_SIZE,
          offset,
        }),
        getCounterpartyStatusCounts({
          kind: kindFilter,
          status: statusArg,
          q: qDebounced || undefined,
        }),
      ]);
      setRows(r.counterparties.map(cpToRow));
      setCounts(c);
      // Dev-mode drift sentinel — if the list endpoint and the counts
      // function ever disagree about the same filter set, surface it
      // loudly in the console so we catch the regression before it
      // reaches users.
      if (import.meta.env.DEV && r.total !== c.total_directory) {
        // eslint-disable-next-line no-console
        console.warn(
          '[Members] count drift: list.total =', r.total,
          'counts.total_directory =', c.total_directory,
          '— filters:', { kindFilter, filter, q: qDebounced },
        );
      }
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }, [kindFilter, filter, qDebounced, offset]);

  useEffect(() => { void reload(); }, [reload]);

  // Keep the URL in sync with the kind + status chips so a copied
  // link reproduces the user's view.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (kindFilter === 'all') url.searchParams.delete('kind');
    else url.searchParams.set('kind', kindFilter);
    if (filter === 'all') url.searchParams.delete('status');
    else url.searchParams.set('status', filter);
    window.history.replaceState({}, '', url);
  }, [kindFilter, filter]);

  // Derived counts — every one of them comes from the single counts
  // response, so the page header, KPI strip, sub-line, and pager
  // footer can never disagree with each other.
  const totalDirectory = counts?.total_directory ?? 0;
  const individualsCount = counts?.individuals ?? 0;
  const institutionsCount = counts?.institutions ?? 0;
  const statusBucketCount = filter === 'all'
    ? totalDirectory
    : pickStatusBucket(counts, filter as MemberStatus);
  const scopeLabel: string =
    kindFilter === 'individual' ? 'individuals'
      : kindFilter === 'institutional' ? 'organisations'
        : 'all kinds';
  const pageCount = Math.max(1, Math.ceil(totalDirectory / PAGE_SIZE));
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const shownStart = rows && rows.length > 0 ? offset + 1 : 0;
  const shownEnd = rows ? offset + rows.length : 0;


  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Members · Directory</div>
          <h1>Members</h1>
          <div className="page-sub">
            {/* Scope-aware sub-title. Always derived from the same
                counts response that feeds the KPI strip so the two
                lines tell the same story. */}
            <SubTitle
              kindFilter={kindFilter}
              filter={filter}
              totalDirectory={totalDirectory}
              individuals={individualsCount}
              institutions={institutionsCount}
              scopeLabel={scopeLabel}
              statusBucketCount={statusBucketCount}
            />
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

      <MemberRollCallKPIs counts={counts} scopeLabel={scopeLabel} />
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: -6, marginBottom: 10 }}>
        <button
          type="button"
          className="btn btn-sm btn-ghost"
          onClick={() => setShowHowCounted((v) => !v)}
          aria-expanded={showHowCounted}
        >
          {showHowCounted ? 'Hide' : 'How these are counted'}
        </button>
      </div>
      {showHowCounted && (
        <div className="card" style={{ marginBottom: 12 }}>
          <div className="card-body">
            <p style={{ marginTop: 0 }}>
              These numbers come from the canonical{' '}
              <code>counterparty_status_counts(tenant, kind, status, q)</code>{' '}
              Postgres function (member migration 0022). Every chip on this
              page passes the same filters to that one source, so the header,
              KPI strip, and table can never disagree with each other.
            </p>
            <ul style={{ marginBottom: 0 }}>
              <li>
                <strong>On register</strong> = active + dormant + pending +
                suspended + blacklisted. Blacklisted members stay on the
                register for regulatory reporting; exited / deceased /
                rejected do not.
              </li>
              <li>
                <strong>Active (servicing)</strong> = active + dormant. Dormant
                is rolled in because dormancy is an inactivity-driven
                UI/risk filter, not a deregistration.
              </li>
              <li>
                <strong>Pending review</strong>, <strong>Rejected</strong>:
                raw per-status counts for the active scope.
              </li>
              <li>
                <strong>Total directory</strong> = every counterparty in
                scope regardless of status (matches the table footer's
                &quot;of N&quot;).
              </li>
            </ul>
          </div>
        </div>
      )}

      <div className="card">
        <div className="card-hd">
          <h3>Register</h3>
          <span className="card-sub">
            {/* "Showing 1–50 of 250" — both numbers come from the same
                counts source the KPI strip uses, so the user never
                wonders whether some rows were silently hidden. */}
            {rows && rows.length > 0
              ? <>Showing rows {shownStart}–{shownEnd} of {totalDirectory.toLocaleString()}</>
              : <>No matches</>}
            {kindFilter !== 'all' && (
              <> · scope: <strong>{scopeLabel}</strong>
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

      {totalDirectory > PAGE_SIZE && (
        <div
          className="card"
          style={{ marginTop: 12, display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: 12 }}
        >
          <span className="muted tiny" aria-live="polite">
            Page <strong>{currentPage}</strong> of <strong>{pageCount}</strong>
            {' · '}rows {shownStart.toLocaleString()}–{shownEnd.toLocaleString()} of {totalDirectory.toLocaleString()}
          </span>
          <div style={{ display: 'flex', gap: 6 }}>
            <button
              type="button"
              className="btn btn-sm"
              disabled={offset <= 0}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            >
              ← Previous
            </button>
            <button
              type="button"
              className="btn btn-sm"
              disabled={offset + PAGE_SIZE >= totalDirectory}
              onClick={() => setOffset(offset + PAGE_SIZE)}
            >
              Next →
            </button>
          </div>
        </div>
      )}

      {filter === 'pending' && (rows?.length ?? 0) > 0 && (
        <p className="muted tiny" style={{ marginTop: 8 }}>
          Pending members need approval from a user with the <Badge tone="accent">members:approve</Badge> permission.
        </p>
      )}
    </div>
  );
}

// SubTitle picks the right wording for the page sub-line based on
// the active scope so the number it shows is always consistent with
// what's in the table. Pulled out as its own component so the
// branching reads as a small lookup table.
function SubTitle({
  kindFilter, filter, totalDirectory, individuals, institutions, scopeLabel, statusBucketCount,
}: {
  kindFilter: KindFilter;
  filter: Filter;
  totalDirectory: number;
  individuals: number;
  institutions: number;
  scopeLabel: string;
  statusBucketCount: number;
}) {
  if (kindFilter === 'all' && filter === 'all') {
    return (
      <>
        <strong>{totalDirectory.toLocaleString()}</strong> total
        {' · '}{individuals.toLocaleString()} individual{individuals === 1 ? '' : 's'}
        {' · '}{institutions.toLocaleString()} organisation{institutions === 1 ? '' : 's'}
      </>
    );
  }
  if (filter !== 'all') {
    return (
      <>
        <strong>{statusBucketCount.toLocaleString()}</strong>{' '}
        {filter}{statusBucketCount === 1 ? '' : ''} · scope: <strong>{scopeLabel}</strong>
      </>
    );
  }
  // kindFilter !== 'all', filter === 'all'
  const inScope = kindFilter === 'individual' ? individuals : institutions;
  return (
    <>
      <strong>{inScope.toLocaleString()}</strong> {scopeLabel} in scope ·{' '}
      {totalDirectory.toLocaleString()} total in directory
    </>
  );
}

// pickStatusBucket maps a status enum value to the matching numeric
// field on CounterpartyStatusCounts. Kept here because the bucket
// names are paint-by-numbers and a Record lookup is the cheapest way
// to keep this consistent with the SQL function.
function pickStatusBucket(c: CounterpartyStatusCounts | null, s: MemberStatus): number {
  if (!c) return 0;
  switch (s) {
    case 'active':      return c.active;
    case 'dormant':     return c.dormant;
    case 'pending':     return c.pending;
    case 'suspended':   return c.suspended;
    case 'blacklisted': return c.blacklisted;
    case 'exited':      return c.exited;
    case 'deceased':    return c.deceased;
    case 'rejected':    return c.rejected;
  }
}

// MemberRollCallKPIs renders the canonical roll-call numbers from
// counterparty_status_counts. The dashboard widget consumes the same
// underlying source so the two views display identical numbers for
// the same fixture by construction. The `(in scope)` suffix +
// tooltips spell out the bucket semantics so non-finance users don't
// have to read migration 0006/0022 to interpret the cards.
export function MemberRollCallKPIs({
  counts, scopeLabel,
}: {
  counts: CounterpartyStatusCounts | null;
  // Display label for the page's current kind scope. Echoed inside
  // the card title so the user is never confused about which
  // universe they're counting.
  scopeLabel?: string;
}) {
  const inScope = scopeLabel ? ` (${scopeLabel})` : ' (in scope)';
  return (
    <div className="grid-4" style={{ marginBottom: 14 }}>
      <KPICard
        label={`On register${inScope}`}
        value={counts?.total_on_register ?? 0}
        tooltip="active + dormant + pending + suspended + blacklisted. Excludes exited / deceased / rejected. Blacklisted members are barred from operations but stay on the books for regulatory reporting."
      />
      <KPICard
        label={`Active${inScope}`}
        value={counts?.total_active_servicing ?? 0}
        tone="pos"
        tooltip="active + dormant. Dormant is rolled into active because dormancy is an inactivity flag, not a deregistration — a dormant counterparty still has money on the books."
      />
      <KPICard
        label={`Pending review${inScope}`}
        value={counts?.pending ?? 0}
        tone="warn"
        tooltip="Membership applications awaiting approval from a user with the members:approve permission."
      />
      <KPICard
        label={`Rejected${inScope}`}
        value={counts?.rejected ?? 0}
        tone="neg"
        tooltip="Applications that were declined. Excluded from the on-register total."
      />
    </div>
  );
}

function KPICard({ label, value, tone, tooltip }: {
  label: string;
  value: number;
  tone?: 'pos' | 'neg' | 'warn';
  tooltip?: string;
}) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card" title={tooltip}>
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
