// PageCrumb context — lets a dynamic-route page (e.g. MemberProfile)
// tell the AppShell what the trailing breadcrumb segment should say
// once it has loaded its data ("Jane Doe" instead of the fallback
// "Profile"). The shell reads the value at render time; the page
// pushes the value with usePageCrumb(value).

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';

type Ctx = {
  suffix: string | null;
  setSuffix: (s: string | null) => void;
};

const PageCrumbContext = createContext<Ctx>({
  suffix: null,
  setSuffix: () => { /* no-op outside provider */ },
});

export function PageCrumbProvider({ children }: { children: ReactNode }) {
  const [suffix, setSuffix] = useState<string | null>(null);
  return (
    <PageCrumbContext.Provider value={{ suffix, setSuffix }}>
      {children}
    </PageCrumbContext.Provider>
  );
}

// Reads the current dynamic suffix. The shell calls this; pages
// shouldn't need to.
export function usePageCrumbValue(): string | null {
  return useContext(PageCrumbContext).suffix;
}

// usePageCrumb(value) — called from a page when its data resolves.
// Pass null/undefined while loading; the registry's fallbackSuffix
// (e.g. "Profile") shows up until the value lands. Cleans up on
// unmount so the next page doesn't inherit a stale suffix.
export function usePageCrumb(value: string | null | undefined): void {
  const { setSuffix } = useContext(PageCrumbContext);
  useEffect(() => {
    setSuffix(value && value.trim() ? value.trim() : null);
    return () => setSuffix(null);
  }, [value, setSuffix]);
}
