// UserName — resolves a user_id to "Full Name" (falls back to email).
// Used wherever a backend struct exposes a raw uuid for the actor
// (cashier_user_id, posted_by, etc.).

import { useEffect, useState } from 'react';
import { getUser, type ApiUser } from '../../api/client';
import { createCache } from './cache';

// getUser returns { user, roles } — we only need the user.
const cache = createCache<ApiUser>('user', async (id) => {
  const r = await getUser(id);
  return r.user;
});

export function UserName({
  userId,
  fallback = '—',
}: {
  userId?: string | null;
  fallback?: string;
}) {
  const [u, setU] = useState<ApiUser | null>(null);
  useEffect(() => {
    if (!userId) { setU(null); return; }
    let cancelled = false;
    cache.resolve(userId).then((v) => { if (!cancelled) setU(v); });
    return () => { cancelled = true; };
  }, [userId]);

  if (!userId) return <span className="muted">{fallback}</span>;
  if (!u) {
    return (
      <span className="tiny-mono muted" title={`Resolving user ${userId}`}>
        {userId.slice(0, 8)}…
      </span>
    );
  }
  // Prefer full name, fall back to email so we never show a blank cell.
  const label = u.full_name?.trim() || u.email || `User ${userId.slice(0, 8)}`;
  return <span title={u.email ?? ''}>{label}</span>;
}

export const __userNameCache = cache;
