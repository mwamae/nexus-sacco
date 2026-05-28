// Slim platform-status pill for the tenant dashboard.
//
// Fetches /v1/platform-status once on mount — no polling, no
// auto-refresh. Once-per-load is enough; a tenant admin glances at
// it to confirm the platform is healthy and that's it. If we ever
// want richer info, point them at the public status page (TBD).
//
// Renders three states with a single inline pill + a small
// "last checked" muted timestamp. Deliberately exposes no service
// names, no URLs, no worker heartbeats — the backend returns only
// {overall_status, checked_at, message} for the same reason.

import { useEffect, useState } from 'react';

type PlatformStatus = 'ok' | 'degraded' | 'down';

type Response = {
  overall_status: PlatformStatus;
  checked_at: string;
  message: string;
};

const ENDPOINT = '/v1/platform-status';

export function PlatformStatusBadge() {
  const [snap, setSnap] = useState<Response | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        // Hit the endpoint via fetch with credentials so we don't
        // pull in the api client just to do one call. The backend
        // accepts the same JWT cookie used everywhere else.
        const r = await fetch(`/api${ENDPOINT}`, { credentials: 'include' });
        if (!r.ok) {
          if (!cancelled) setErr(`HTTP ${r.status}`);
          return;
        }
        const body = await r.json();
        if (!cancelled) setSnap((body?.data ?? body) as Response);
      } catch (e) {
        if (!cancelled) setErr((e as Error).message ?? 'network error');
      }
    })();
    return () => { cancelled = true; };
  }, []);

  if (err) {
    // Don't loud-fail in the tenant dashboard if the endpoint isn't
    // reachable — the pill stays invisible. Backend wedged is its
    // own kind of "platform not okay," which the rest of the UI will
    // surface via their own request failures.
    return null;
  }

  if (!snap) {
    return (
      <div data-testid="platform-status-badge" className="muted tiny" style={pillBase}>
        <span style={dotBase('neutral')} />
        Checking platform status…
      </div>
    );
  }

  const tone = toneFor(snap.overall_status);
  const checkedAt = new Date(snap.checked_at);
  const checkedAgo = formatAgo(Math.max(0, Math.floor((Date.now() - checkedAt.getTime()) / 1000)));

  return (
    <div
      data-testid="platform-status-badge"
      data-status={snap.overall_status}
      role="status"
      aria-live="polite"
      style={{ ...pillBase, ...toneStyle(tone) }}
    >
      <span style={dotBase(tone)} />
      <span style={{ fontWeight: 500 }}>{snap.message}</span>
      <span className="muted tiny" style={{ marginLeft: 6 }}>
        · last checked {checkedAgo}
      </span>
    </div>
  );
}

type Tone = 'pos' | 'warn' | 'neg' | 'neutral';

function toneFor(s: PlatformStatus): Tone {
  switch (s) {
    case 'ok': return 'pos';
    case 'degraded': return 'warn';
    case 'down': return 'neg';
  }
}

const pillBase: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 8,
  padding: '6px 12px',
  borderRadius: 999,
  border: '1px solid var(--border)',
  background: 'var(--surface-2, #f5f5f5)',
  fontSize: 13,
};

function dotBase(tone: Tone): React.CSSProperties {
  const color =
    tone === 'pos'  ? 'var(--pos, #22c55e)' :
    tone === 'warn' ? 'var(--warn, #f59e0b)' :
    tone === 'neg'  ? 'var(--neg, #ef4444)' :
                      'var(--muted, #9ca3af)';
  return {
    display: 'inline-block',
    width: 8, height: 8,
    borderRadius: '50%',
    background: color,
  };
}

function toneStyle(tone: Tone): React.CSSProperties {
  switch (tone) {
    case 'pos':
      return { background: 'var(--pos-bg, #e6f7ec)', color: 'var(--pos, #15803d)', borderColor: 'transparent' };
    case 'warn':
      return { background: 'var(--warn-bg, #fff4cc)', color: 'var(--warn, #92400e)', borderColor: 'transparent' };
    case 'neg':
      return { background: 'var(--neg-bg, #fee2e2)', color: 'var(--neg, #991b1b)', borderColor: 'transparent' };
    case 'neutral':
      return {};
  }
}

function formatAgo(seconds: number): string {
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}
