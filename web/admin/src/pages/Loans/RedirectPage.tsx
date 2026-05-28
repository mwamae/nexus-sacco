// Tiny redirect-on-mount component for the legacy Loans paths that
// got reorganized in Phase 1. We keep the old paths registered for
// one release so external bookmarks + email links continue to
// resolve, then delete them in Phase 2 once analytics show the
// redirect traffic dried up.
//
// Used by:
//   /loan-products → /loans/products
//   /loan-reports  → /loans/reports
//   /provisioning  → /loans/provisioning
//   /loans (old register-shaped page) → /loans/register (when an
//          ?id= query param is present) or /loans (dashboard) otherwise
//
// window.location.replace preserves the back-button — the user can
// navigate away normally rather than re-trigger the redirect on
// every back-press.

import { useEffect } from 'react';

export default function RedirectPage({ to, label }: { to: string; label?: string }) {
  useEffect(() => {
    window.location.replace(to);
  }, [to]);
  return (
    <div className="page">
      <div className="empty">
        Redirecting to {label ?? to}…
        <div className="muted tiny" style={{ marginTop: 8 }}>
          This page moved in the Loans Phase 1 reorganization. Update bookmarks if you
          land here often.
        </div>
      </div>
    </div>
  );
}
