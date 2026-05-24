// TillLabel — renders a human label for either a cash till_session_id
// (cash channel) or a virtual_till_id (M-Pesa / bank / cheque /
// standing-order / airtel).
//
// Resolution strategy:
//   cash (till_session_id)   → fetch the session, then fetch the till
//                              for its `code`. Renders as:
//                                "T2 · opened 10:00"
//                              (the till session table has no per-till
//                              session number, so we use opened_at.)
//   virtual (virtual_till_id) → look up in the cached
//                              listVirtualTills() (≤5 per tenant)
//                              and render the display_name, e.g.
//                                "M-Pesa virtual till"
//
// Failure path mirrors the other Refs: degraded slice-8 with a
// title attribute, never a thrown error.

import { useEffect, useState } from 'react';
import {
  getTillDetail,
  getTillSession,
  listVirtualTills,
  type Till,
  type TillSession,
  type VirtualTill,
} from '../../api/client';
import { createCache } from './cache';

const tillCache = createCache<Till>('till', async (id) => {
  const r = await getTillDetail(id);
  return r.till;
});

const sessionCache = createCache<TillSession>('till_session', async (id) => {
  const r = await getTillSession(id);
  return r.session;
});

// Virtual tills are list-once, never-changes (per tenant per session).
// We populate the cache lazily on first use of TillLabel and reuse
// for every subsequent virtual_till_id lookup in the page.
const virtualCache = createCache<VirtualTill>('virtual_till', async () => {
  throw new Error('not used — populated via seed');
});
let virtualListLoad: Promise<void> | null = null;
function ensureVirtualTillsLoaded(): Promise<void> {
  if (virtualListLoad) return virtualListLoad;
  virtualListLoad = listVirtualTills()
    .then((items) => {
      items.forEach((v) => virtualCache.seed(v.id, v));
    })
    .catch(() => {
      // Leave the cache empty; per-id resolves will fall through to
      // the "unresolved" branch and the user sees the slice-8 hint.
    });
  return virtualListLoad;
}

export function TillLabel({
  tillSessionId,
  virtualTillId,
  fallback = '—',
}: {
  tillSessionId?: string | null;
  virtualTillId?: string | null;
  fallback?: string;
}) {
  const [session, setSession] = useState<TillSession | null>(null);
  const [till, setTill] = useState<Till | null>(null);
  const [virt, setVirt] = useState<VirtualTill | null>(null);

  // Cash session path — chain session → till.
  useEffect(() => {
    if (!tillSessionId) { setSession(null); setTill(null); return; }
    let cancelled = false;
    sessionCache.resolve(tillSessionId).then((s) => {
      if (cancelled) return;
      setSession(s);
      if (s?.till_id) {
        tillCache.resolve(s.till_id).then((t) => { if (!cancelled) setTill(t); });
      }
    });
    return () => { cancelled = true; };
  }, [tillSessionId]);

  // Virtual till path — list-then-pick from cache.
  useEffect(() => {
    if (!virtualTillId) { setVirt(null); return; }
    let cancelled = false;
    ensureVirtualTillsLoaded().then(() => {
      virtualCache.resolve(virtualTillId).then((v) => { if (!cancelled) setVirt(v); });
    });
    return () => { cancelled = true; };
  }, [virtualTillId]);

  if (!tillSessionId && !virtualTillId) {
    return <span className="muted">{fallback}</span>;
  }

  if (tillSessionId) {
    if (!session || !till) {
      return (
        <span className="tiny-mono muted" title={`Resolving till session ${tillSessionId}`}>
          {tillSessionId.slice(0, 8)}…
        </span>
      );
    }
    const openedLocal = new Date(session.opened_at).toLocaleTimeString(undefined, {
      hour: '2-digit', minute: '2-digit',
    });
    const stateChip = session.status === 'open' ? 'open' : 'closed';
    return (
      <span title={`Session ${session.id}`}>
        <span>{till.code}</span>
        <span className="muted" style={{ marginLeft: 6, fontSize: '90%' }}>
          · opened {openedLocal} · {stateChip}
        </span>
      </span>
    );
  }

  // virtual_till_id
  if (!virt) {
    return (
      <span className="tiny-mono muted" title={`Resolving virtual till ${virtualTillId}`}>
        {virtualTillId!.slice(0, 8)}…
      </span>
    );
  }
  return (
    <span title={`${virt.channel} · GL ${virt.gl_account_code}`}>
      {virt.display_name}
    </span>
  );
}

export const __tillCaches = { tillCache, sessionCache, virtualCache };
