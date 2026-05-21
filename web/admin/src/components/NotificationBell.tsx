// Topbar bell: shows unread count, opens a dropdown with the latest
// notifications. Polls every 30s for stage 1; stage 4 swaps the poll
// for an SSE subscription.
//
// Clicking a notification marks it read and navigates to its
// deep_link (if any). The dropdown also includes "Mark all read" and
// a link to the full /notifications page.

import { useEffect, useRef, useState } from 'react';
import {
  getNotificationFeed,
  getNotificationUnreadCount,
  markAllNotificationsRead,
  markNotificationRead,
  type NotificationFeedItem,
  type NotificationPriority,
} from '../api/client';
import { Icon } from './Icon';

const POLL_INTERVAL_MS = 30_000;

const PRIORITY_DOT: Record<NotificationPriority, string> = {
  info:    'var(--accent)',
  success: 'var(--pos)',
  warning: 'var(--warn)',
  error:   'var(--neg)',
};

export function NotificationBell() {
  const [unread, setUnread] = useState(0);
  const [items, setItems] = useState<NotificationFeedItem[]>([]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);

  async function refresh() {
    try {
      const [count, feed] = await Promise.all([
        getNotificationUnreadCount(),
        getNotificationFeed(false, 10),
      ]);
      setUnread(count);
      setItems(feed.items);
    } catch {
      // Notification service might be down — silently keep last state.
    }
  }

  useEffect(() => {
    void refresh();
    const id = window.setInterval(() => void refresh(), POLL_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, []);

  // Close on outside-click.
  useEffect(() => {
    function onClick(e: MouseEvent) {
      if (!open) return;
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener('mousedown', onClick);
    return () => document.removeEventListener('mousedown', onClick);
  }, [open]);

  async function onItemClick(it: NotificationFeedItem) {
    if (it.in_app_status !== 'read') {
      try { await markNotificationRead(it.id); } catch { /* keep UI responsive */ }
    }
    if (it.deep_link) {
      window.location.href = it.deep_link;
    } else {
      void refresh();
    }
  }

  async function onMarkAll() {
    setBusy(true);
    try { await markAllNotificationsRead(); await refresh(); }
    finally { setBusy(false); }
  }

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button
        className="btn btn-sm btn-ghost"
        aria-label={`Notifications${unread > 0 ? `, ${unread} unread` : ''}`}
        onClick={() => { setOpen((v) => !v); if (!open) void refresh(); }}
        style={{ position: 'relative', padding: '4px 8px' }}
      >
        <Icon name="bell" size={16} />
        {unread > 0 && (
          <span style={{
            position: 'absolute', top: -4, right: -4,
            minWidth: 16, height: 16, padding: '0 4px',
            borderRadius: 8, fontSize: 10, lineHeight: '16px',
            background: 'var(--neg)', color: 'white', fontWeight: 600,
            display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
          }}>
            {unread > 99 ? '99+' : unread}
          </span>
        )}
      </button>

      {open && (
        <div style={{
          position: 'absolute', right: 0, top: 'calc(100% + 6px)',
          width: 380, maxHeight: 480, overflow: 'auto',
          background: 'var(--surface)', border: '1px solid var(--border)',
          borderRadius: 6, boxShadow: '0 8px 24px rgba(0,0,0,0.12)',
          zIndex: 100,
        }}>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '8px 12px', borderBottom: '1px solid var(--border)',
          }}>
            <strong style={{ flex: 1 }}>Notifications</strong>
            <span className="muted tiny">{unread} unread</span>
            <button className="btn btn-sm btn-ghost" disabled={busy || unread === 0} onClick={() => void onMarkAll()}>
              Mark all read
            </button>
          </div>

          {items.length === 0 ? (
            <div className="empty" style={{ padding: 24 }}>
              You're all caught up.
            </div>
          ) : (
            items.map((it) => {
              const unreadRow = it.in_app_status !== 'read';
              return (
                <div
                  key={it.id}
                  onClick={() => void onItemClick(it)}
                  style={{
                    display: 'flex', gap: 10, padding: '10px 12px', cursor: 'pointer',
                    borderBottom: '1px solid var(--border)',
                    background: unreadRow ? 'var(--surface-2)' : 'transparent',
                  }}
                >
                  <span style={{
                    flexShrink: 0, marginTop: 6,
                    width: 8, height: 8, borderRadius: 4,
                    background: PRIORITY_DOT[it.priority] ?? 'var(--muted)',
                  }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="tiny muted" style={{ marginBottom: 2 }}>
                      {it.event_code.replace(/_/g, ' ').toLowerCase()}
                      {' · '}
                      {timeAgo(it.created_at)}
                    </div>
                    <div style={{ fontSize: 13, lineHeight: 1.4 }}>{it.body}</div>
                  </div>
                </div>
              );
            })
          )}

          <div style={{ padding: '8px 12px', borderTop: '1px solid var(--border)', textAlign: 'center' }}>
            <a href="/notifications" className="muted tiny" style={{ textDecoration: 'none' }}>
              View all notifications →
            </a>
          </div>
        </div>
      )}
    </div>
  );
}

function timeAgo(iso: string): string {
  const t = new Date(iso).getTime();
  const diff = (Date.now() - t) / 1000;
  if (diff < 60)        return 'just now';
  if (diff < 3600)      return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400)     return `${Math.floor(diff / 3600)}h ago`;
  if (diff < 86400 * 7) return `${Math.floor(diff / 86400)}d ago`;
  return new Date(iso).toISOString().slice(0, 10);
}
