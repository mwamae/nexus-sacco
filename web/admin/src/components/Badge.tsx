// Tone-aware badge. Maps semantic intent to the .badge-* CSS classes.

import type { ReactNode } from 'react';

export type Tone = 'pos' | 'neg' | 'warn' | 'info' | 'neutral' | 'accent';

export function Badge({
  tone = 'neutral',
  outline = false,
  children,
}: {
  tone?: Tone;
  outline?: boolean;
  children: ReactNode;
}) {
  return <span className={`badge ${outline ? 'badge-outline' : `badge-${tone}`}`}>{children}</span>;
}

const STATUS_TONE: Record<string, Tone> = {
  // Member statuses
  active: 'pos',
  pending: 'warn',
  suspended: 'neg',
  locked: 'neg',
  closed: 'neutral',
  rejected: 'neg',
  // Tenant statuses (the ones not shared with members)
  trial: 'info',
  expired: 'warn',
  pending_setup: 'warn',
  archived: 'neutral',
};

export function StatusBadge({ status }: { status: string }) {
  // Friendly label: pending_setup → "pending setup"
  const label = status.replace(/_/g, ' ');
  return <Badge tone={STATUS_TONE[status] ?? 'neutral'}>{label}</Badge>;
}
