import '@testing-library/jest-dom/vitest';

// Vitest 4 + happy-dom/jsdom don't reliably expose window.localStorage as
// a working global to test code (Node 22's experimental localStorage
// shadows it; happy-dom's getter doesn't survive vitest's populateGlobal).
// Provide a deterministic in-memory shim so token-storage code under test
// just works.
if (typeof localStorage === 'undefined' || typeof localStorage?.clear !== 'function') {
  const store = new Map<string, string>();
  const shim: Storage = {
    get length() { return store.size; },
    clear: () => store.clear(),
    getItem: (k) => (store.has(k) ? store.get(k)! : null),
    setItem: (k, v) => { store.set(k, String(v)); },
    removeItem: (k) => { store.delete(k); },
    key: (i) => Array.from(store.keys())[i] ?? null,
  };
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    writable: true,
    value: shim,
  });
}
