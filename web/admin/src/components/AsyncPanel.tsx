// Async data panel — one consistent way to render "loading vs data vs
// empty vs error" across the app.
//
// Why this exists: ad-hoc `if (!data) return <div>Loading…</div>` spread
// across pages had no timeout (so a wedged backend parked the panel at
// "Loading…" forever), no retry affordance, and inconsistent empty /
// error UX. AsyncPanel solves all four in one place.

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

type State<T> =
  | { kind: 'loading' }
  | { kind: 'data'; value: T }
  | { kind: 'empty' }
  | { kind: 'error'; error: Error };

export const DEFAULT_TIMEOUT_MS = 10_000;
export const DEFAULT_SKELETON_DELAY_MS = 600;

type UseAsyncPanelOptions<T> = {
  isEmpty?: (v: T) => boolean;
  timeoutMs?: number;
};

class TimeoutError extends Error {
  constructor() { super('request timed out'); this.name = 'TimeoutError'; }
}

export function isTimeoutError(e: unknown): e is TimeoutError {
  return e instanceof Error && e.name === 'TimeoutError';
}

// Hook form — useful when the caller wants to render around the panel
// (e.g. show different toolbars in loading vs data state).
export function useAsyncPanel<T>(
  fetcher: () => Promise<T>,
  deps: unknown[],
  options: UseAsyncPanelOptions<T> = {},
): { state: State<T>; retry: () => void } {
  const { isEmpty, timeoutMs = DEFAULT_TIMEOUT_MS } = options;
  const [state, setState] = useState<State<T>>({ kind: 'loading' });
  // Each mount + dep-change + retry bumps the generation. In-flight
  // fetches whose generation no longer matches are silently dropped so a
  // slow request doesn't clobber a newer one.
  const generation = useRef(0);
  const [nonce, setNonce] = useState(0);

  const retry = useCallback(() => {
    setState({ kind: 'loading' });
    setNonce((n) => n + 1);
  }, []);

  useEffect(() => {
    const mine = ++generation.current;
    setState({ kind: 'loading' });

    let timeoutId: ReturnType<typeof setTimeout> | undefined;
    const timeoutPromise = new Promise<never>((_, reject) => {
      timeoutId = setTimeout(() => reject(new TimeoutError()), timeoutMs);
    });

    Promise.race([fetcher(), timeoutPromise])
      .then((value) => {
        if (generation.current !== mine) return;
        if (isEmpty?.(value)) {
          setState({ kind: 'empty' });
        } else {
          setState({ kind: 'data', value });
        }
      })
      .catch((err: unknown) => {
        if (generation.current !== mine) return;
        const error = err instanceof Error ? err : new Error(String(err));
        setState({ kind: 'error', error });
      })
      .finally(() => {
        if (timeoutId !== undefined) clearTimeout(timeoutId);
      });

    return () => {
      if (timeoutId !== undefined) clearTimeout(timeoutId);
      // Bumping the generation here drops any in-flight result for this
      // effect, so a delayed resolution after unmount or dep-change is a
      // no-op rather than a state-update-on-unmounted warning.
      generation.current = mine + 1;
    };
    // We intentionally re-run on deps change and retry; fetcher is
    // assumed to be a stable closure or recreated alongside deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, timeoutMs]);

  return { state, retry };
}

export type AsyncPanelProps<T> = {
  fetcher: () => Promise<T>;
  deps?: unknown[];

  // Detect empty values (e.g. items.length === 0). When omitted, the
  // value is always treated as data.
  isEmpty?: (v: T) => boolean;

  // Override the 10-second default for slow endpoints.
  timeoutMs?: number;
  // Override the 600 ms grace window before the skeleton appears.
  skeletonDelayMs?: number;

  // Skeleton shown after `skeletonDelayMs` while the fetcher is in
  // flight. Defaults to a muted "Loading…" line. Pass your own for a
  // shape-matched shimmer.
  skeleton?: ReactNode;

  // Rendered when isEmpty(value) returns true. REQUIRED — write a
  // domain-specific message ("No status changes recorded yet.").
  empty: ReactNode;

  // Rendered on fetch failure / timeout. Either a string or a function
  // that derives a message from the error (e.g. distinguish 403 from
  // network). REQUIRED — write a domain-specific message.
  errorMessage: string | ((err: Error) => string);
  // Optional title above the error message ("Couldn't load accounts").
  errorTitle?: string;

  // Render successful data.
  children: (value: T) => ReactNode;
};

export function AsyncPanel<T>(props: AsyncPanelProps<T>) {
  const {
    fetcher, deps = [], isEmpty, timeoutMs,
    skeletonDelayMs = DEFAULT_SKELETON_DELAY_MS,
    skeleton, empty, errorMessage, errorTitle,
    children,
  } = props;

  const { state, retry } = useAsyncPanel(fetcher, deps, { isEmpty, timeoutMs });

  // Skeleton-flash suppression: only mount the skeleton once the loading
  // state has lasted longer than skeletonDelayMs. Most fetches resolve
  // before then and render nothing in the interim, which feels snappier
  // than a flash of skeleton.
  const [showSkeleton, setShowSkeleton] = useState(false);
  useEffect(() => {
    if (state.kind !== 'loading') { setShowSkeleton(false); return; }
    const id = setTimeout(() => setShowSkeleton(true), skeletonDelayMs);
    return () => clearTimeout(id);
  }, [state.kind, skeletonDelayMs]);

  if (state.kind === 'loading') {
    if (!showSkeleton) return null;
    return <>{skeleton ?? <div className="muted tiny" role="status">Loading…</div>}</>;
  }
  if (state.kind === 'empty') return <>{empty}</>;
  if (state.kind === 'error') {
    const msg = typeof errorMessage === 'function' ? errorMessage(state.error) : errorMessage;
    return (
      <div className="alert alert-error" role="alert" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
        <div style={{ flex: 1 }}>
          {errorTitle && <div style={{ fontWeight: 600, marginBottom: 2 }}>{errorTitle}</div>}
          <div>{msg}</div>
        </div>
        <button className="btn btn-sm" onClick={retry}>Retry</button>
      </div>
    );
  }
  return <>{children(state.value)}</>;
}
