// useDocumentTitle — sets document.title for the lifetime of the
// component and restores the previous title on unmount.
//
// Bug 3.3: detail pages used to show raw UUIDs in the browser tab
// (e.g. "/collect/receipts/9c4dc64b-..."). Top-level pages had no
// per-page title at all. Pages now opt-in via this hook and we
// suffix every title with " · nexusSacco" so the brand stays in
// the tab regardless of which page is focused.
//
// Pass null while the data needed to compose the title is still
// loading; the hook is a no-op until a real string arrives. This
// avoids a flash of "null · nexusSacco" or a half-formed title
// like "Receipt undefined".

import { useEffect } from 'react';

export function useDocumentTitle(title: string | null) {
  useEffect(() => {
    if (!title) return;
    const prev = document.title;
    document.title = `${title} · nexusSacco`;
    return () => {
      document.title = prev;
    };
  }, [title]);
}
