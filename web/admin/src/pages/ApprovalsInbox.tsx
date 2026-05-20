// Approvals Inbox — entity-agnostic queue of pending workflow
// instances. Each row shows the subject, current level, KPIs and the
// five action buttons. The KPI strip at the top shows what's mine to
// action vs the tenant-wide queue.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  actOnInstance,
  getWorkflowDashboard,
  listWorkflowInstances,
  extractError,
  type WFActionRequest,
  type WFDashboard,
  type WFInstance,
  type WFLevelState,
  type WFStatus,
} from '../api/client';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

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
  const [filter, setFilter] = useState<WFStatus | 'all'>('in_progress');
  const [instances, setInstances] = useState<WFInstance[] | null>(null);
  const [dash, setDash] = useState<WFDashboard | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [acting, setActing] = useState<WFInstance | null>(null);

  async function reload() {
    setErr(null);
    try {
      const [list, d] = await Promise.all([
        listWorkflowInstances({ status: filter === 'all' ? undefined : filter, limit: 200 }),
        getWorkflowDashboard(),
      ]);
      setInstances(list.instances);
      setDash(d);
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); }, [filter]);

  // Bucket: instances where the current level is one the caller can act on.
  const mine = useMemo(() => {
    if (!instances || !user) return [];
    return instances.filter((i) => canActOn(i, user.id, roles));
  }, [instances, user, roles]);

  const overdue = useMemo(() => (instances ?? []).filter((i) => {
    const lvl = i.levels?.[i.current_level];
    if (!lvl?.sla_due_at) return false;
    return new Date(lvl.sla_due_at).getTime() < Date.now() && isOpen(i.status);
  }), [instances]);

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
        <KPI label="Awaiting me" value={mine.length} tone="warn" />
        <KPI label="Open across tenant" value={openCount(dash)} />
        <KPI label="Overdue (SLA breach)" value={overdue.length} tone={overdue.length > 0 ? 'neg' : 'pos'} />
        <KPI label="Avg turnaround" value={fmtTAT(dash?.avg_tat_seconds)} />
      </div>

      {dash && Object.keys(dash.by_process_kind).length > 0 && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd">
            <h3>By process</h3>
            <span className="card-sub">{dash.total} total instances</span>
          </div>
          <div className="card-body">
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              {Object.entries(dash.by_process_kind).map(([kind, n]) => (
                <Badge key={kind} tone="neutral">{kind.replace(/_/g, ' ')}: {n}</Badge>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="card">
        <div className="card-hd">
          <h3>{filter === 'all' ? 'All' : filter.replace('_', ' ')} ({instances?.length ?? 0})</h3>
          <span className="card-sub">{mine.length} awaiting your action</span>
          <div className="card-hd-actions">
            <div className="fchips">
              {STATUS_FILTERS.map((f) => (
                <button key={f.v} type="button" className="fchip" data-active={filter === f.v || undefined} onClick={() => setFilter(f.v)}>
                  {f.label}
                </button>
              ))}
            </div>
          </div>
        </div>
        <div className="card-body flush">
          {!instances && !err && <div className="empty">Loading…</div>}
          {instances && instances.length === 0 && <div className="empty">No instances.</div>}
          {instances && instances.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Subject</th>
                  <th>Process</th>
                  <th>Level</th>
                  <th>Quorum</th>
                  <th>SLA</th>
                  <th>Status</th>
                  <th>Started</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {instances.map((i) => (
                  <InstanceRow
                    key={i.id}
                    instance={i}
                    isMine={!!user && canActOn(i, user.id, roles)}
                    onAct={() => setActing(i)}
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

function InstanceRow({ instance: i, isMine, onAct }: { instance: WFInstance; isMine: boolean; onAct: () => void }) {
  const level = i.levels[i.current_level];
  const overdue = level?.sla_due_at ? new Date(level.sla_due_at).getTime() < Date.now() : false;
  return (
    <tr style={isMine && isOpen(i.status) ? { background: 'var(--accent-bg)' } : undefined}>
      <td>
        <div style={{ fontWeight: 500 }}>{i.subject_kind.replace('_', ' ')}</div>
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
      <td className="tiny-mono">{i.started_at.slice(0, 16).replace('T', ' ')}</td>
      <td>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          <a className="btn btn-sm" href={`/approvals/${i.id}`} title="Open"><Icon name="eye" size={12} /></a>
          {isOpen(i.status) && (
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

          <KVS>
            <Row k="Level" v={`${level?.name ?? '—'} (#${instance.current_level + 1})`} />
            <Row k="Status" v={<StatusBadge status={instance.status} />} />
            <Row k="Quorum" v={level?.quorum ?? '—'} />
            {level?.sla_due_at && <Row k="SLA due" v={<span className="mono">{new Date(level.sla_due_at).toISOString().slice(0, 16).replace('T', ' ')}</span>} />}
            {Object.keys(instance.context ?? {}).length > 0 && (
              <Row k="Context" v={<pre className="mono tiny" style={{ background: 'var(--surface-2)', padding: 6, borderRadius: 4, margin: 0 }}>{JSON.stringify(instance.context, null, 2)}</pre>} />
            )}
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
