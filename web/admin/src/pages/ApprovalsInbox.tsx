// Approvals Inbox — entity-agnostic queue of pending workflow
// instances. Each row shows the subject, current level, KPIs and the
// five action buttons. The KPI strip at the top shows what's mine to
// action vs the tenant-wide queue.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  actOnInstance,
  addWorkflowInstanceComment,
  claimWorkflowInstance,
  getWorkflowDashboard,
  getWorkflowInstance,
  listWorkflowInstances,
  releaseWorkflowInstance,
  extractError,
  type WFAction,
  type WFActionRequest,
  type WFDashboard,
  type WFInstance,
  type WFInstanceDetail,
  type WFLevelState,
  type WFStatus,
} from '../api/client';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

// PR #9 — Inbox UX tabs.
type TabKey = 'awaiting_me' | 'my_team' | 'all_in_tenant';

// PR #9 — bulk-decide allow-list. Only kinds where every instance
// shape is structurally identical + the GL impact is bounded.
// Anything that touches share capital, equity, or member lifecycle
// is excluded — those should always be decided one-at-a-time.
const BULK_DECIDE_KINDS = new Set<string>([
  'cash_deposit',
  'cash_account_transfer',
]);

const STATUS_FILTERS: { v: WFStatus | 'all'; label: string }[] = [
  { v: 'all', label: 'all' },
  { v: 'in_progress', label: 'in progress' },
  { v: 'returned', label: 'returned' },
  { v: 'awaiting_info', label: 'awaiting info' },
  { v: 'escalated', label: 'escalated' },
  { v: 'approved', label: 'approved' },
  { v: 'rejected', label: 'rejected' },
];

const ACTION_LABEL = {
  approve: 'Approve',
  reject: 'Reject',
  return: 'Return for correction',
  request_info: 'Request more info',
  escalate: 'Escalate',
  reassign: 'Reassign',
  cancel: 'Cancel',
  resume: 'Resume',
} as const;

type ActionKind = keyof typeof ACTION_LABEL;

export default function ApprovalsInbox() {
  const { user, roles } = useAuth();
  const [tab, setTab] = useState<TabKey>('awaiting_me');
  const [instances, setInstances] = useState<WFInstance[] | null>(null);
  const [dash, setDash] = useState<WFDashboard | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [acting, setActing] = useState<WFInstance | null>(null);
  // PR #9 — bulk-decide multi-select. Keyed by instance id. Reset on
  // tab change so a stale set doesn't leak across buckets.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);

  async function reload() {
    setErr(null);
    try {
      // We fetch the wider OPEN set (all in-flight statuses) once and
      // bucket client-side. Cheaper than refetching per tab swap.
      const [list, d] = await Promise.all([
        listWorkflowInstances({ limit: 500 }),
        getWorkflowDashboard(),
      ]);
      setInstances(list.instances);
      setDash(d);
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, []);
  useEffect(() => { setSelected(new Set()); }, [tab]);

  // ─── Bucketing ────────────────────────────────────────────────
  // "Awaiting me"   = open + I can act + unclaimed (or claim expired)
  // "My team"       = open + I can act + claimed by someone else
  //                   (read-only — only the claimant can decide)
  // "All in tenant" = every instance regardless of who can act
  const now = Date.now();
  const buckets = useMemo(() => {
    const u = user?.id ?? '';
    const allOpen = (instances ?? []).filter((i) => isOpen(i.status));
    const lockHeldByOther = (i: WFInstance) =>
      !!i.claimed_by && i.claimed_by !== u &&
      (!i.claim_expires || new Date(i.claim_expires).getTime() > now);
    const lockHeldByMe = (i: WFInstance) =>
      !!i.claimed_by && i.claimed_by === u &&
      (!i.claim_expires || new Date(i.claim_expires).getTime() > now);
    return {
      awaiting_me: allOpen.filter((i) => canActOn(i, u, roles) && !lockHeldByOther(i)),
      my_team:     allOpen.filter((i) => canActOn(i, u, roles) && lockHeldByOther(i)),
      all_in_tenant: instances ?? [],
      mine_claimed: allOpen.filter(lockHeldByMe).length,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [instances, user, roles, tab]);

  const visible = buckets[tab];

  const overdue = useMemo(() => (instances ?? []).filter((i) => {
    const lvl = i.levels?.[i.current_level];
    if (!lvl?.sla_due_at) return false;
    return new Date(lvl.sla_due_at).getTime() < Date.now() && isOpen(i.status);
  }), [instances]);

  // Bulk-decide eligibility: every selected instance must be a
  // bulk-allowed kind + I must be able to act on it + unclaimed-or-mine.
  const u = user?.id ?? '';
  const selectedItems = visible.filter((i) => selected.has(i.id));
  const bulkEligible = selectedItems.length > 0 &&
    selectedItems.every((i) =>
      BULK_DECIDE_KINDS.has(i.process_kind) &&
      canActOn(i, u, roles) &&
      (!i.claimed_by || i.claimed_by === u));

  async function bulkDecide(action: 'approve' | 'reject') {
    if (!bulkEligible) return;
    const note = action === 'reject'
      ? prompt('Reason for bulk decline (applied to every selected item):') ?? ''
      : prompt('Optional note (applied to every selected item):') ?? '';
    if (action === 'reject' && !note.trim()) return;
    if (!confirm(`${action === 'approve' ? 'Approve' : 'Decline'} ${selectedItems.length} item${selectedItems.length === 1 ? '' : 's'}?`)) return;
    setBulkBusy(true);
    try {
      // Sequential rather than Promise.all so a failure halfway
      // leaves a clear stopping point + an obvious error.
      for (const i of selectedItems) {
        await actOnInstance(i.id, { action, comments: note || undefined });
      }
    } catch (e) {
      alert(extractError(e));
    } finally {
      setBulkBusy(false);
      setSelected(new Set());
      await reload();
    }
  }

  async function onClaim(id: string) {
    try {
      await claimWorkflowInstance(id);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }
  async function onRelease(id: string) {
    try {
      await releaseWorkflowInstance(id);
      await reload();
    } catch (e) {
      alert(extractError(e));
    }
  }

  const tabsConfig: { k: TabKey; label: string; count: number; tone?: 'pos' | 'neg' | 'warn' }[] = [
    { k: 'awaiting_me',   label: 'Awaiting me',   count: buckets.awaiting_me.length, tone: 'warn' },
    { k: 'my_team',       label: 'My team',       count: buckets.my_team.length },
    { k: 'all_in_tenant', label: 'All in tenant', count: buckets.all_in_tenant.length },
  ];

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Approvals · Inbox</div>
          <h1>Approvals</h1>
          <div className="page-sub">Cross-system queue of items waiting on a human decision.</div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href="/workflows"><Icon name="settings" size={12} /> Definitions</a>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Awaiting me" value={buckets.awaiting_me.length} tone="warn" />
        <KPI label="Claimed by me" value={buckets.mine_claimed} />
        <KPI label="Overdue (SLA breach)" value={overdue.length} tone={overdue.length > 0 ? 'neg' : 'pos'} />
        <KPI label="Avg turnaround" value={fmtTAT(dash?.avg_tat_seconds)} />
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Inbox</h3>
          <div className="card-hd-actions">
            <div className="fchips">
              {tabsConfig.map((t) => (
                <button
                  key={t.k}
                  type="button"
                  className="fchip"
                  data-active={tab === t.k || undefined}
                  onClick={() => setTab(t.k)}
                >
                  {t.label} ({t.count})
                </button>
              ))}
            </div>
          </div>
        </div>

        {/* Bulk-decide action bar — only on Awaiting-me, only when
            at least one bulk-allowed item is selected. Pinned at
            the top so the action surface is visible without
            scrolling past the rows. */}
        {tab === 'awaiting_me' && selectedItems.length > 0 && (
          <div
            className="alert"
            style={{ margin: 8, padding: 10, background: 'var(--accent-bg)', display: 'flex', alignItems: 'center', gap: 12 }}
          >
            <strong>{selectedItems.length} selected</strong>
            {bulkEligible ? (
              <>
                <button className="btn btn-sm btn-accent" disabled={bulkBusy} onClick={() => void bulkDecide('approve')}>
                  Approve all
                </button>
                <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={bulkBusy} onClick={() => void bulkDecide('reject')}>
                  Decline all
                </button>
              </>
            ) : (
              <span className="muted tiny">
                Bulk decide is only available for cash deposits + account transfers when every selected item is unclaimed or yours.
              </span>
            )}
            <button className="btn btn-sm btn-ghost" onClick={() => setSelected(new Set())}>Clear selection</button>
          </div>
        )}

        <div className="card-body flush">
          {!instances && !err && <div className="empty">Loading…</div>}
          {instances && visible.length === 0 && (
            <div className="empty">
              {tab === 'awaiting_me' && "Nothing's awaiting you right now."}
              {tab === 'my_team' && "No items currently claimed by teammates in roles you share."}
              {tab === 'all_in_tenant' && 'No instances.'}
            </div>
          )}
          {instances && visible.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  {tab === 'awaiting_me' && <th style={{ width: 1 }}></th>}
                  <th>Subject</th>
                  <th>Process</th>
                  <th>Level</th>
                  <th>Quorum</th>
                  <th>SLA</th>
                  <th>Status</th>
                  <th>Claim</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {visible.map((i) => (
                  <InstanceRow
                    key={i.id}
                    instance={i}
                    isMine={!!user && canActOn(i, user.id, roles)}
                    me={u}
                    showCheckbox={tab === 'awaiting_me'}
                    checked={selected.has(i.id)}
                    onToggle={() => {
                      setSelected((cur) => {
                        const next = new Set(cur);
                        if (next.has(i.id)) next.delete(i.id); else next.add(i.id);
                        return next;
                      });
                    }}
                    onAct={() => setActing(i)}
                    onClaim={() => onClaim(i.id)}
                    onRelease={() => onRelease(i.id)}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {acting && (
        <ActionModal
          instance={acting}
          actorRoles={roles}
          onClose={() => setActing(null)}
          onActed={async () => { setActing(null); await reload(); }}
        />
      )}
    </div>
  );
}

function InstanceRow({
  instance: i, isMine, me, showCheckbox, checked, onToggle, onAct, onClaim, onRelease,
}: {
  instance: WFInstance;
  isMine: boolean;
  me: string;
  showCheckbox: boolean;
  checked: boolean;
  onToggle: () => void;
  onAct: () => void;
  onClaim: () => void;
  onRelease: () => void;
}) {
  const level = i.levels[i.current_level];
  const overdue = level?.sla_due_at ? new Date(level.sla_due_at).getTime() < Date.now() : false;
  // Claim state — three flavours: unclaimed, claimed by me, claimed by teammate.
  const claimAlive = i.claim_expires ? new Date(i.claim_expires).getTime() > Date.now() : false;
  const claimedByMe = i.claimed_by === me && claimAlive;
  const claimedByOther = !!i.claimed_by && i.claimed_by !== me && claimAlive;

  return (
    <tr style={isMine && isOpen(i.status) ? { background: 'var(--accent-bg)' } : undefined}>
      {showCheckbox && (
        <td>
          {/* Only allow selecting items the caller can actually decide
              on + that aren't held by someone else. */}
          {isMine && !claimedByOther && (
            <input type="checkbox" checked={checked} onChange={onToggle} aria-label="Select for bulk decision" />
          )}
        </td>
      )}
      <td>
        <div style={{ fontWeight: 500 }}>
          {i.summary ?? i.subject_kind.replace('_', ' ')}
        </div>
        <div className="tiny-mono">{i.subject_id}</div>
      </td>
      <td><Badge tone="neutral">{i.process_kind.replace(/_/g, ' ')}</Badge></td>
      <td>
        <div>{level?.name ?? '—'}</div>
        <div className="tiny muted">
          {level?.approver_roles?.length ? level.approver_roles.join(' / ') : 'direct user'}
        </div>
      </td>
      <td>{level?.quorum ?? '—'}</td>
      <td>
        {level?.sla_due_at ? (
          overdue ? <Badge tone="neg">breached</Badge> : <span className="tiny-mono">{slaIn(level.sla_due_at)}</span>
        ) : <span className="muted tiny">—</span>}
      </td>
      <td><StatusBadge status={i.status} /></td>
      <td>
        {claimedByMe && (
          <button className="btn btn-sm btn-ghost" title="Release the lock so someone else can claim" onClick={onRelease}>
            <Icon name="x" size={11} /> Release
          </button>
        )}
        {claimedByOther && (
          <span title={`Locked by ${i.claimed_by?.slice(0, 8)}… until ${i.claim_expires?.slice(11, 16) ?? '—'}`}>
            <Badge tone="warn">Locked</Badge>
          </span>
        )}
        {!i.claimed_by && isMine && isOpen(i.status) && (
          <button className="btn btn-sm" title="Lock this item for 30 minutes" onClick={onClaim}>
            <Icon name="check" size={11} /> Claim
          </button>
        )}
      </td>
      <td>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          {/* If the workflow carries a source_url, surface a quick
              jump back to the originating page so the approver can
              read the underlying record before deciding. */}
          {i.source_url && (
            <a className="btn btn-sm btn-ghost" href={i.source_url} title="Open source page" onClick={(e) => e.stopPropagation()}>
              <Icon name="eye" size={11} />
            </a>
          )}
          {isOpen(i.status) && isMine && !claimedByOther && (
            <button className={`btn btn-sm ${isMine ? 'btn-accent' : ''}`} title="Take action" onClick={onAct}>
              <Icon name="check" size={12} /> Act
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

function ActionModal({
  instance, actorRoles, onClose, onActed,
}: {
  instance: WFInstance;
  actorRoles: string[];
  onClose: () => void;
  onActed: () => void | Promise<void>;
}) {
  const level = instance.levels[instance.current_level];
  const [chosen, setChosen] = useState<ActionKind>('approve');
  const [comments, setComments] = useState('');
  const [reassignTo, setReassignTo] = useState('');
  const [actingAs, setActingAs] = useState(() => {
    // Pick a role the caller has that matches the level.
    return actorRoles.find((r) => (level?.approver_roles ?? []).includes(r)) ?? actorRoles[0] ?? '';
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // PR #9 — threaded comments. We hydrate the full instance on open
  // to get the wf_actions audit trail; new comments append in-place
  // without a full re-fetch so the thread stays responsive.
  const [detail, setDetail] = useState<WFInstanceDetail | null>(null);
  const [newComment, setNewComment] = useState('');
  const [commentBusy, setCommentBusy] = useState(false);

  useEffect(() => {
    getWorkflowInstance(instance.id)
      .then(setDetail)
      .catch(() => setDetail(null));
  }, [instance.id]);

  async function postComment() {
    if (!newComment.trim()) return;
    setCommentBusy(true);
    try {
      const a = await addWorkflowInstanceComment(instance.id, newComment.trim());
      setDetail((cur) => cur ? { ...cur, actions: [...cur.actions, a] } : cur);
      setNewComment('');
    } catch (e) {
      alert(extractError(e));
    } finally {
      setCommentBusy(false);
    }
  }

  const allowedActions: ActionKind[] = useMemo(() => {
    if (instance.status === 'returned' || instance.status === 'awaiting_info') {
      return ['resume', 'cancel'];
    }
    return ['approve', 'reject', 'return', 'request_info', 'escalate', 'reassign', 'cancel'];
  }, [instance.status]);

  async function submit() {
    setErr(null);
    setBusy(true);
    try {
      const req: WFActionRequest = { action: chosen, comments: comments || undefined, acting_as_role: actingAs || undefined };
      if (chosen === 'reassign') req.reassign_to = reassignTo;
      if ((chosen === 'reject' || chosen === 'return') && !comments.trim()) {
        setErr('Comments are required for this action.');
        setBusy(false);
        return;
      }
      await actOnInstance(instance.id, req);
      await onActed();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,.45)',
        display: 'grid', placeItems: 'center',
      }}
      onClick={onClose}
    >
      <div
        className="card"
        style={{ width: 560, maxWidth: '90vw', maxHeight: '90vh', overflow: 'auto' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="card-hd">
          <h3>Act on {instance.process_kind.replace(/_/g, ' ')}</h3>
          <span className="card-sub">{instance.subject_kind}: {instance.subject_id}</span>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={13} /></button>
          </div>
        </div>
        <div className="card-body">
          {err && <div className="alert alert-error">{err}</div>}

          <SubjectMiniCard instance={instance} />

          <KVS>
            <Row k="Level" v={`${level?.name ?? '—'} (#${instance.current_level + 1})`} />
            <Row k="Status" v={<StatusBadge status={instance.status} />} />
            <Row k="Quorum" v={level?.quorum ?? '—'} />
            {level?.sla_due_at && <Row k="SLA due" v={<span className="mono">{new Date(level.sla_due_at).toISOString().slice(0, 16).replace('T', ' ')}</span>} />}
          </KVS>

          <div className="divider" />

          <div className="h-sec">Action</div>
          <div className="fchips" style={{ marginBottom: 10 }}>
            {allowedActions.map((a) => (
              <button key={a} type="button" className="fchip" data-active={chosen === a || undefined} onClick={() => setChosen(a)}>
                {ACTION_LABEL[a]}
              </button>
            ))}
          </div>

          {chosen === 'reassign' && (
            <div className="field">
              <label className="field-label">Reassign to (user UUID)</label>
              <input className="input mono" value={reassignTo} onChange={(e) => setReassignTo(e.target.value)} placeholder="e.g. 3a7dec5c-…" />
            </div>
          )}

          {actorRoles.length > 1 && (
            <div className="field">
              <label className="field-label">Acting as role</label>
              <select className="select" value={actingAs} onChange={(e) => setActingAs(e.target.value)}>
                {actorRoles.map((r) => <option key={r} value={r}>{r}</option>)}
              </select>
            </div>
          )}

          <div className="field">
            <label className="field-label">
              Comments
              {(chosen === 'reject' || chosen === 'return') && <span className="req"> *</span>}
            </label>
            <textarea
              className="input"
              style={{ minHeight: 60, padding: 8, fontFamily: 'inherit', resize: 'vertical' }}
              value={comments}
              onChange={(e) => setComments(e.target.value)}
              placeholder={chosen === 'reject' ? 'Reason for rejection' : chosen === 'return' ? 'What needs correcting' : 'Optional notes'}
            />
          </div>

          <div className="row" style={{ gap: 8 }}>
            <button className="btn btn-sm btn-accent" disabled={busy} onClick={() => void submit()}>
              <Icon name="check" size={12} /> {busy ? 'Submitting…' : ACTION_LABEL[chosen]}
            </button>
            <button className="btn btn-sm btn-ghost" disabled={busy} onClick={onClose}>Cancel</button>
          </div>

          {/* ─── PR #9 — Threaded comments ─── */}
          <div className="divider" />
          <div className="h-sec">Discussion</div>
          <CommentsThread actions={detail?.actions ?? null} />
          <div className="field" style={{ marginTop: 8 }}>
            <textarea
              className="input"
              style={{ minHeight: 50, padding: 8, fontFamily: 'inherit', resize: 'vertical' }}
              value={newComment}
              onChange={(e) => setNewComment(e.target.value)}
              placeholder="Add a comment…"
            />
            <div className="row" style={{ gap: 6, marginTop: 6 }}>
              <button className="btn btn-sm" disabled={commentBusy || !newComment.trim()} onClick={() => void postComment()}>
                {commentBusy ? 'Posting…' : 'Add comment'}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

// ─────────── helpers ───────────

function canActOn(i: WFInstance, userID: string, roles: string[]): boolean {
  if (!isOpen(i.status)) return false;
  const lvl = i.levels?.[i.current_level];
  if (!lvl) return false;
  // direct user listing
  if ((lvl.approver_user_ids ?? []).includes(userID)) return true;
  // role overlap
  for (const r of roles) {
    if ((lvl.approver_roles ?? []).includes(r)) return true;
  }
  return false;
}

function isOpen(s: WFStatus): boolean {
  return s === 'pending' || s === 'in_progress' || s === 'returned' || s === 'awaiting_info' || s === 'escalated';
}

function openCount(d: WFDashboard | null): number {
  if (!d) return 0;
  return (
    (d.by_status.pending ?? 0) +
    (d.by_status.in_progress ?? 0) +
    (d.by_status.returned ?? 0) +
    (d.by_status.awaiting_info ?? 0) +
    (d.by_status.escalated ?? 0)
  );
}

function slaIn(due: string): string {
  const ms = new Date(due).getTime() - Date.now();
  const hrs = Math.ceil(ms / (1000 * 60 * 60));
  if (hrs < 1) {
    const mins = Math.ceil(ms / (1000 * 60));
    return `${mins}m`;
  }
  return `${hrs}h`;
}

function fmtTAT(seconds?: number): string {
  if (!seconds || seconds <= 0) return '—';
  if (seconds < 60) return `${seconds.toFixed(0)}s`;
  const mins = seconds / 60;
  if (mins < 60) return `${mins.toFixed(1)}m`;
  const hrs = mins / 60;
  if (hrs < 24) return `${hrs.toFixed(1)}h`;
  return `${(hrs / 24).toFixed(1)}d`;
}

function KPI({ label, value, tone }: { label: string; value: number | string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="m360-stat">
        <span className="m360-stat-label">{label}</span>
        <span className="m360-stat-value" style={{ color }}>{value}</span>
      </div>
    </div>
  );
}

function KVS({ children }: { children: ReactNode }) { return <dl className="kvs">{children}</dl>; }
function Row({ k, v }: { k: ReactNode; v: ReactNode }) { return (<><dt>{k}</dt><dd>{v}</dd></>); }

// ─────────── PR #9: SubjectMiniCard ───────────
//
// Renders a small subject-context card at the top of the action
// modal so the approver sees the most relevant 3-5 fields of the
// underlying entity without having to click out to the source page.
// We dispatch on process_kind family — every kind in the same
// family carries the same context shape (set by the originating
// service when it created the wf_instance).

function SubjectMiniCard({ instance }: { instance: WFInstance }) {
  const ctx = instance.context ?? {};
  const summary = instance.summary;
  const family = processKindFamily(instance.process_kind);

  // Common header: process kind chip + summary line + source link.
  const header = (
    <div style={{ marginBottom: 6 }}>
      <Badge tone="neutral">{instance.process_kind.replace(/_/g, ' ')}</Badge>
      {summary && <span style={{ marginLeft: 8, fontWeight: 500 }}>{summary}</span>}
      {instance.source_url && (
        <a className="btn btn-sm btn-ghost" href={instance.source_url} style={{ marginLeft: 8 }}>
          <Icon name="eye" size={11} /> Open source
        </a>
      )}
    </div>
  );

  // Per-family field bag. Each family pulls a small, focused set of
  // context fields and renders them as a tight grid. Missing fields
  // are silently skipped so older instances created before a context
  // field was added still render cleanly.
  let body: ReactNode = null;
  switch (family) {
    case 'cash':
      body = (
        <MiniGrid items={[
          ['Counterparty', str(ctx['counterparty_id'])],
          ['Amount', money(ctx['amount'])],
          ['Channel', str(ctx['channel'])],
          ['Reference', str(ctx['channel_ref'])],
        ]} />
      );
      break;
    case 'loan':
      body = (
        <MiniGrid items={[
          ['Loan / App', str(ctx['loan_no'] ?? ctx['application_no'])],
          ['Requested', money(ctx['requested_amount'])],
          ['Term (months)', str(ctx['requested_term'])],
          ['Credit score', str(ctx['credit_score'])],
          ['DTI', str(ctx['dti_ratio'])],
        ]} />
      );
      break;
    case 'member':
      body = (
        <MiniGrid items={[
          ['Member no', str(ctx['member_no'])],
          ['Name', str(ctx['member_name'])],
          ['From', str(ctx['from_status'])],
          ['To', str(ctx['to_status'])],
          ['Reason', str(ctx['reason'])],
        ]} />
      );
      break;
    case 'batch':
      body = (
        <MiniGrid items={[
          ['Financial year', str(ctx['financial_year_label'] ?? ctx['year'])],
          ['Member count', str(ctx['member_count'] ?? ctx['candidate_count'])],
          ['Total gross', money(ctx['total_gross_interest'] ?? ctx['total_gross_dividend'])],
          ['Total WHT', money(ctx['total_wht'])],
        ]} />
      );
      break;
    case 'journal':
      body = (
        <MiniGrid items={[
          ['Amount', money(ctx['amount'])],
          ['Narration', str(ctx['narration'])],
          ['Affects equity', bool(ctx['affects_equity'])],
          ['Entry type', str(ctx['entry_type'])],
        ]} />
      );
      break;
    default:
      body = (
        <pre className="mono tiny" style={{ background: 'var(--surface-2)', padding: 6, borderRadius: 4, margin: 0, maxHeight: 120, overflow: 'auto' }}>
          {JSON.stringify(ctx, null, 2)}
        </pre>
      );
  }
  return (
    <div className="card" style={{ background: 'var(--surface-2)', padding: 10, marginBottom: 10 }}>
      {header}
      {body}
    </div>
  );
}

function processKindFamily(kind: string): 'cash' | 'loan' | 'member' | 'batch' | 'journal' | 'other' {
  if (kind.startsWith('cash_') || kind.startsWith('share_')) return 'cash';
  if (kind.startsWith('loan_')) return 'loan';
  if (kind.startsWith('member_')) return 'member';
  if (kind === 'interest_run' || kind === 'dividend_run' || kind === 'year_end_close' || kind === 'bulk_dormancy_run') return 'batch';
  if (kind === 'manual_journal_entry' || kind === 'journal_reversal') return 'journal';
  return 'other';
}

function MiniGrid({ items }: { items: Array<[string, string | null]> }) {
  const rows = items.filter(([, v]) => v && v !== '—');
  if (rows.length === 0) return <div className="muted tiny">No context fields set.</div>;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4, fontSize: 12 }}>
      {rows.map(([k, v]) => (
        <div key={k} style={{ display: 'flex', gap: 6 }}>
          <span className="muted tiny" style={{ minWidth: 90 }}>{k}</span>
          <span className="mono">{v}</span>
        </div>
      ))}
    </div>
  );
}

function str(v: unknown): string | null {
  if (v === undefined || v === null || v === '') return null;
  return String(v);
}
function money(v: unknown): string | null {
  const s = str(v);
  if (!s) return null;
  const n = parseFloat(s);
  if (isNaN(n)) return s;
  return `KES ${n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}
function bool(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  return v ? 'yes' : 'no';
}

// ─────────── PR #9: CommentsThread ───────────

function CommentsThread({ actions }: { actions: WFAction[] | null }) {
  if (actions === null) return <div className="muted tiny">Loading…</div>;
  // Show comments + a small set of state-change events that are
  // useful context (claim/release, level transitions). Hides the
  // create + callback noise.
  const visible = actions.filter((a) =>
    a.action === 'comment' || a.action === 'approve' || a.action === 'reject' ||
    a.action === 'return' || a.action === 'request_info' || a.action === 'escalate' ||
    a.action === 'claim' || a.action === 'release');
  if (visible.length === 0) return <div className="muted tiny">No discussion yet — start the thread with the composer below.</div>;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 220, overflow: 'auto' }}>
      {visible.map((a) => (
        <div key={a.id} style={{ background: 'var(--surface-2)', padding: 6, borderRadius: 4, fontSize: 12 }}>
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <span>
              <strong>{a.action}</strong>
              {a.actor_role && <span className="muted tiny"> · {a.actor_role}</span>}
              {a.actor_id && <span className="tiny-mono"> · {a.actor_id.slice(0, 8)}</span>}
            </span>
            <span className="tiny-mono muted">{a.created_at.slice(0, 16).replace('T', ' ')}</span>
          </div>
          {a.comments && <div style={{ marginTop: 3 }}>{a.comments}</div>}
        </div>
      ))}
    </div>
  );
}
