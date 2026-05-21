// Persistent banner shown to tenant admins when an SMS or email
// credit balance reaches zero. Spec: cannot be dismissed; must
// remain visible until the balance is topped up.
//
// Polls /v1/credits every 30s while mounted; cheap, RLS-scoped.

import { useEffect, useState } from 'react';
import { getCreditsOverview, type CreditBalance } from '../api/client';

export function CreditBanner({ enabled }: { enabled: boolean }) {
  const [balances, setBalances] = useState<CreditBalance[] | null>(null);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const load = async () => {
      try {
        const r = await getCreditsOverview();
        if (!cancelled) setBalances(r.balances);
      } catch { /* ignore — banner is best-effort */ }
    };
    void load();
    const t = setInterval(() => void load(), 30_000);
    return () => { cancelled = true; clearInterval(t); };
  }, [enabled]);

  if (!enabled || !balances) return null;
  const zero = balances.filter((b) => b.balance < 1);
  if (zero.length === 0) return null;

  const channels = zero.map((b) => b.channel === 'sms' ? 'SMS' : 'Email').join(' & ');
  return (
    <div
      role="alert"
      style={{
        background: 'var(--neg-bg, #fee)',
        borderBottom: '1px solid var(--neg)',
        color: 'var(--neg)',
        padding: '8px 16px',
        fontSize: 13,
        display: 'flex',
        gap: 10,
        alignItems: 'center',
      }}
    >
      <strong>⚠ Credits exhausted —</strong>
      <span>
        Your {channels} credit{zero.length > 1 ? 's have' : ' has'} reached zero.
        Notifications on {zero.length > 1 ? 'these channels are' : 'this channel is'} currently suspended.
      </span>
      <a
        href="/credits"
        style={{ marginLeft: 'auto', color: 'var(--neg)', fontWeight: 600, textDecoration: 'underline' }}
      >
        Request a top-up →
      </a>
    </div>
  );
}
