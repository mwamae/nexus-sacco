// Full notification log / inbox.
//
// Two views via tabs:
//   • My inbox — the current user's notifications (read + unread).
//   • Audit log — every notification fired in the tenant, with the
//     per-channel delivery breakdown (in-app + queued sms/email rows
//     that will start delivering once stages 2-3 ship).

import { useEffect, useState } from 'react';
import {
  getNotificationFeed,
  getNotificationLog,
  markAllNotificationsRead,
  markNotificationRead,
  type NotificationFeedItem,
  type NotificationLogEntry,
  type NotificationPriority,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';

// Classify load failures so the page can render a clear, non-scary
// message instead of leaking raw axios error text. A 401 here means the
// backend said no on a specific endpoint — NOT that the session is
// dead (that case is handled centrally in api/client.ts).
function describeLoadError(e: unknown): string {
  const status = (e as { response?: { status?: number } })?.response?.status;
  if (status === 401 || status === 403) {
    return "You don't have access to this resource.";
  }
  return e instanceof Error ? e.message : 'Failed to load notifications.';
}

type Tab = 'inbox' | 'log';

const TABS: Array<{ id: Tab; label: string; hint: string }> = [
  { id: 'inbox', label: 'My inbox', hint: 'Notifications addressed to you.' },
  { id: 'log',   label: 'Audit log', hint: 'Every notification fired in this tenant + per-channel delivery status.' },
];

const PRIORITY_DOT: Record<NotificationPriority, string> = {
  info:    'var(--accent)',
  success: 'var(--pos)',
  warning: 'var(--warn)',
  error:   'var(--neg)',
};

export default function NotificationsPage() {
  const { tenant, hasPermission } = useAuth();
  const canViewLog = hasPermission('audit:view') || hasPermission('tenant:settings:view');
  const [tab, setTab] = useState<Tab>('inbox');

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Notifications</div>
          <h1>Notifications</h1>
          <div className="page-sub">In-app delivery is live. SMS and email channels ship in upcoming stages.</div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <div className="tabs" style={{ padding: '0 14px' }}>
          {TABS.filter((t) => t.id !== 'log' || canViewLog).map((t) => (
            <div key={t.id} className="tab" data-active={tab === t.id || undefined} onClick={() => setTab(t.id)}>
              {t.label}
            </div>
          ))}
        </div>
        <div style={{ padding: 14 }}>
          <p className="muted tiny" style={{ margin: '0 0 14px' }}>{TABS.find((t) => t.id === tab)?.hint}</p>
          {tab === 'inbox' && <InboxTab />}
          {tab === 'log' && canViewLog && <LogTab />}
        </div>
      </div>
    </div>
  );
}

// ─────────── Inbox ───────────

function InboxTab() {
  const [items, setItems] = useState<NotificationFeedItem[] | null>(null);
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await getNotificationFeed(unreadOnly, 100);
      setItems(r.items);
    } catch (e) {
      setErr(describeLoadError(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [unreadOnly]);

  async function onMarkAll() {
    setBusy(true);
    try { await markAllNotificationsRead(); await load(); }
    finally { setBusy(false); }
  }

  if (err) return <div className="alert alert-error">{err}</div>;
  if (items === null) return <div className="empty">Loading…</div>;

  return (
    <>
      <div className="row" style={{ gap: 8, marginBottom: 12 }}>
        <label className="row" style={{ gap: 4, alignItems: 'center' }}>
          <input type="checkbox" checked={unreadOnly} onChange={(e) => setUnreadOnly(e.target.checked)} />
          <span className="tiny muted">unread only</span>
        </label>
        <div className="spacer" />
        <button className="btn btn-sm" onClick={() => void load()}>Refresh</button>
        <button className="btn btn-sm" disabled={busy || items.length === 0} onClick={() => void onMarkAll()}>
          Mark all as read
        </button>
      </div>
      {items.length === 0 ? (
        <div className="empty">No notifications.</div>
      ) : (
        <div style={{ border: '1px solid var(--border)', borderRadius: 4 }}>
          {items.map((it) => {
            const unread = it.in_app_status !== 'read';
            return (
              <div key={it.id}
                style={{
                  display: 'flex', gap: 10, padding: 12, cursor: it.deep_link ? 'pointer' : 'default',
                  borderBottom: '1px solid var(--border)',
                  background: unread ? 'var(--surface-2)' : 'transparent',
                }}
                onClick={async () => {
                  if (unread) await markNotificationRead(it.id);
                  if (it.deep_link) window.location.href = it.deep_link;
                  else await load();
                }}>
                <span style={{
                  flexShrink: 0, marginTop: 6,
                  width: 8, height: 8, borderRadius: 4,
                  background: PRIORITY_DOT[it.priority] ?? 'var(--muted)',
                }} />
                <div style={{ flex: 1 }}>
                  <div className="row" style={{ alignItems: 'center', gap: 8 }}>
                    <span className="tiny muted">{it.event_code.replace(/_/g, ' ').toLowerCase()}</span>
                    <span className="tiny muted">·</span>
                    <span className="tiny muted">{new Date(it.created_at).toLocaleString()}</span>
                    {unread && <span className="tiny" style={{ color: 'var(--accent)', fontWeight: 600 }}>NEW</span>}
                  </div>
                  <div style={{ fontSize: 14, marginTop: 4 }}>{it.body}</div>
                  {it.deep_link && <div className="tiny muted" style={{ marginTop: 4 }}>→ {it.deep_link}</div>}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}

// ─────────── Audit log ───────────

const CHANNEL_LABEL: Record<string, string> = {
  in_app: 'In-app',
  sms: 'SMS',
  email: 'Email',
};

function LogTab() {
  const [items, setItems] = useState<NotificationLogEntry[] | null>(null);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getNotificationLog(50, 0)
      .then((r) => { setItems(r.items); setTotal(r.total); })
      .catch((e) => setErr(describeLoadError(e)));
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (items === null) return <div className="empty">Loading…</div>;

  return (
    <div className="card-body flush">
      <div className="muted tiny" style={{ marginBottom: 8 }}>{total} notifications</div>
      <table className="tbl">
        <thead>
          <tr>
            <th>When</th>
            <th>Event</th>
            <th>Recipient</th>
            <th>Deliveries</th>
            <th>Source</th>
          </tr>
        </thead>
        <tbody>
          {items.length === 0 && (
            <tr><td colSpan={5} className="al-c muted">No notifications fired yet.</td></tr>
          )}
          {items.map((it) => (
            <tr key={it.id}>
              <td className="mono tiny">{new Date(it.created_at).toLocaleString()}</td>
              <td>
                <div>{it.event_code}</div>
                <div className="tiny muted">{it.body.slice(0, 80)}{it.body.length > 80 ? '…' : ''}</div>
              </td>
              <td className="tiny">{it.recipient_name || '—'}</td>
              <td>
                <div className="row" style={{ gap: 4, flexWrap: 'wrap' }}>
                  {(it.deliveries ?? []).map((d) => (
                    <span key={d.id} className="tiny mono"
                      title={`${d.channel} · ${d.status}${d.failure_reason ? ' · ' + d.failure_reason : ''}`}
                      style={{
                        padding: '1px 6px', borderRadius: 3,
                        background: statusBg(d.status), color: statusFg(d.status),
                        border: '1px solid var(--border)',
                      }}>
                      {CHANNEL_LABEL[d.channel] ?? d.channel} · {d.status}
                    </span>
                  ))}
                </div>
              </td>
              <td className="tiny muted">{it.source_module || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function statusBg(s: string): string {
  switch (s) {
    case 'delivered':
    case 'read':
      return 'var(--pos-bg, #e8f5e9)';
    case 'queued':
    case 'pending':
      return 'var(--surface-2)';
    case 'failed':
      return 'var(--neg-bg, #ffebee)';
    default:
      return 'transparent';
  }
}
function statusFg(s: string): string {
  switch (s) {
    case 'delivered':
    case 'read':
      return 'var(--pos)';
    case 'failed':
      return 'var(--neg)';
    default:
      return 'var(--fg)';
  }
}
