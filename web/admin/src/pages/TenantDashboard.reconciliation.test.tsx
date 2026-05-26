// TenantDashboard — Members panel reconciliation test.
//
// Locks in the contract that the dashboard widget's headline numbers
// match what /v1/counterparties/status/counts (?kind=all) would
// return for the same tenant. MemberStatusPanel is exported in
// isolation so we can hand it a hand-rolled summary that mirrors a
// known counts response, then assert the rendered text reflects
// every relevant field — including the new individuals /
// institutions / total_directory additions from migration 0022.

import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';

import { MemberStatusPanel } from './TenantDashboard';
import type { MemberStatusSummary } from '../api/client';

function flat(): string {
  return (document.body.textContent ?? '').replace(/\s+/g, ' ').trim();
}

function summary(): MemberStatusSummary {
  return {
    by_status: {
      active: 190, dormant: 30, pending: 15, suspended: 5, blacklisted: 3,
      exited: 4, deceased: 1, rejected: 2,
    },
    total_on_register: 243,        // active+dormant+pending+suspended+blacklisted
    total_active_servicing: 220,   // active+dormant
    total_directory: 250,
    individuals: 180,
    institutions: 70,
    dormancy_pipeline: [],
    recent_changes: [],
    dormancy_threshold_days: 365,
  };
}

describe('MemberStatusPanel — dashboard reconciliation', () => {
  it('headline numbers track the counts contract', () => {
    render(
      <MemberStatusPanel summary={summary()} canRun={false} busy={false} onRun={() => {}} />,
    );
    const text = flat();
    // Renamed heading
    expect(text).toContain('Counterparties — status overview');
    // Sub-line carries the headline totals + new kind split + total directory
    expect(text).toMatch(/243 on register/);
    expect(text).toMatch(/220 servicing/);
    expect(text).toMatch(/180 individuals/);
    expect(text).toMatch(/70 organisations/);
    expect(text).toMatch(/250 total in directory/);
  });

  it('exposes the per-status chips with deep-links carrying kind=all', () => {
    render(
      <MemberStatusPanel summary={summary()} canRun={false} busy={false} onRun={() => {}} />,
    );
    // Pick the pending chip — it's the one operators click most.
    const pendingLink = screen.getAllByRole('link').find(
      (a) => a.getAttribute('href')?.includes('status=pending'),
    ) as HTMLAnchorElement | undefined;
    expect(pendingLink).toBeDefined();
    expect(pendingLink!.getAttribute('href')).toBe('/members?status=pending&kind=all');
    // The chip text shows the count
    expect(pendingLink!.textContent ?? '').toContain('15');
  });

  it('exposes the Organisations deep-link in the panel header', () => {
    render(
      <MemberStatusPanel summary={summary()} canRun={false} busy={false} onRun={() => {}} />,
    );
    const orgsLink = screen.getByRole('link', { name: /Organisations/i });
    expect(orgsLink.getAttribute('href')).toBe('/members?kind=institutional');
  });

  it('renders without the new optional fields (older service builds)', () => {
    const older = summary();
    delete (older as Partial<MemberStatusSummary>).individuals;
    delete (older as Partial<MemberStatusSummary>).institutions;
    delete (older as Partial<MemberStatusSummary>).total_directory;
    render(
      <MemberStatusPanel summary={older} canRun={false} busy={false} onRun={() => {}} />,
    );
    const text = flat();
    expect(text).toContain('Counterparties — status overview');
    expect(text).toMatch(/243 on register/);
    // Kind split + directory line drop out cleanly when the fields are absent.
    expect(text).not.toMatch(/individuals/);
    expect(text).not.toMatch(/in directory/);
  });
});
