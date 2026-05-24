// Module-level promise cache for Ref/Name/Label components.
//
// Why a single shared util: each resolver component needs the same
// behaviour — concurrent renders for the same id collapse to ONE
// fetch, successes are cached forever (component lifetime), failures
// are remembered as null so we don't re-fetch a known-bad id every
// re-render. Replacing this with SWR or React Query was tempting but
// felt heavy for what's essentially "name lookups in a table".

export type Resolver<T> = (id: string) => Promise<T>;

type Entry<T> = {
  // The promise itself. null means we've resolved successfully but
  // got back nothing — distinct from "not yet fetched".
  promise: Promise<T | null>;
};

export function createCache<T>(name: string, fetcher: Resolver<T>) {
  const store = new Map<string, Entry<T>>();
  return {
    name,
    // resolve returns the cached promise OR starts a fresh fetch.
    // Errors resolve to null so callers can fall back to a degraded
    // display without try/catching every consumer.
    resolve(id: string): Promise<T | null> {
      if (!id) return Promise.resolve(null);
      const existing = store.get(id);
      if (existing) return existing.promise;
      const p = fetcher(id)
        .then((v) => v ?? null)
        .catch(() => null);
      store.set(id, { promise: p });
      return p;
    },
    // Pre-seed (used when a list endpoint returns N items in one go
    // — e.g. listVirtualTills) so subsequent per-id lookups hit the
    // cache instead of round-tripping.
    seed(id: string, value: T | null) {
      store.set(id, { promise: Promise.resolve(value) });
    },
    // Test-only.
    _reset() { store.clear(); },
  };
}
