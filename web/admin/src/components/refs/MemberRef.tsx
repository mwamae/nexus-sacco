// MemberRef — renders a member's display name + CP number, linked
// to /members/<id> (individuals) or /orgs/<id> (institutions).
//
// Resolves counterparty by id via getCounterparty + a module-level
// promise cache so a 50-row table fires at most one fetch per
// distinct id. Failure path: never throws — falls back to the
// slice-8 UUID with a title="Failed to resolve …" so the visual
// regression is small and the engineer can still grep logs.

import { useEffect, useMemo, useState } from 'react';
import { getCounterparty, type Counterparty } from '../../api/client';
import { createCache } from './cache';

const cache = createCache<Counterparty>('member', getCounterparty);

export function MemberRef({
  counterpartyId,
  counterparty,
  fallback = '—',
  asLink = true,
}: {
  // Either pass the id (we resolve) OR an already-loaded counterparty
  // (we skip the fetch — useful in lists that already have the data).
  counterpartyId?: string | null;
  counterparty?: Counterparty | null;
  fallback?: string;
  // Some hosts (badge stacks) don't want the wrapping anchor.
  asLink?: boolean;
}) {
  const [resolved, setResolved] = useState<Counterparty | null>(counterparty ?? null);

  useEffect(() => {
    if (counterparty) {
      setResolved(counterparty);
      return;
    }
    if (!counterpartyId) {
      setResolved(null);
      return;
    }
    let cancelled = false;
    cache.resolve(counterpartyId).then((v) => {
      if (!cancelled) setResolved(v);
    });
    return () => { cancelled = true; };
  }, [counterpartyId, counterparty]);

  if (!counterpartyId && !counterparty) {
    return <span className="muted">{fallback}</span>;
  }
  if (!resolved) {
    // Loading or failed-to-resolve. Slice-8 with title gives the
    // operator a hint they can paste back to engineering without
    // crashing the row.
    const slice = (counterpartyId ?? '').slice(0, 8) || '—';
    return (
      <span className="tiny-mono muted" title={`Resolving member ${counterpartyId ?? ''}`}>
        {slice}…
      </span>
    );
  }

  const target = resolved.legacy_target_id ?? resolved.id;
  const href = resolved.kind === 'individual' ? `/members/${target}` : `/orgs/${target}`;
  const label = (
    <span title={resolved.legacy_id ? `Legacy: ${resolved.legacy_id}` : resolved.cp_number}>
      <span>{resolved.display_name}</span>
      <span className="muted" style={{ marginLeft: 6, fontSize: '90%' }}>· {resolved.cp_number}</span>
    </span>
  );
  if (!asLink) return label;
  return (
    <a className="tbl-link" href={href}>
      {label}
    </a>
  );
}

// Test-only — see MemberRef.test.tsx.
export const __memberRefCache = cache;

// Re-export the cache hook for AccountRef which also needs to know
// kind-by-counterparty for share-account routing.
export function useMemberRef(counterpartyId: string | null | undefined): Counterparty | null {
  const [v, setV] = useState<Counterparty | null>(null);
  const id = counterpartyId ?? '';
  useMemo(() => id, [id]); // dep marker
  useEffect(() => {
    if (!id) { setV(null); return; }
    let cancelled = false;
    cache.resolve(id).then((c) => { if (!cancelled) setV(c); });
    return () => { cancelled = true; };
  }, [id]);
  return v;
}
