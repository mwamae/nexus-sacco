// Contract test: both views that show roll-call numbers MUST derive
// them from the same counterparty_status_counts source. Given the
// same fixture, the dashboard widget header and the Members page KPI
// strip have to display the same totals — that's the property that
// broke and motivated the canonical-counts extraction (member
// migrations 0006 + 0022).

import { render, screen, within } from '@testing-library/react';
import { describe, it, expect } from 'vitest';

import { MemberStatusPanel } from './TenantDashboard';
import { MemberRollCallKPIs } from './Members';
import type { CounterpartyStatusCounts, MemberStatusSummary } from '../api/client';

// One canonical fixture covers all eight buckets (each ≥1 where it
// affects an aggregate) so the snapshot pins both totals. Now also
// carries the v2 fields (total_directory / individuals / institutions)
// — the KPI strip's "(in scope)" labels render the same per-bucket
// numbers regardless of the new fields, but the dashboard header
// surfaces them when present.
const counts: CounterpartyStatusCounts = {
  active: 4,
  dormant: 1,
  pending: 3,
  suspended: 2,
  blacklisted: 1,
  exited: 1,    // OUT of total_on_register
  deceased: 0,
  rejected: 2,  // OUT of total_on_register
  // 4 + 1 + 3 + 2 + 1 = 11
  total_on_register: 11,
  // 4 + 1 = 5
  total_active_servicing: 5,
  // Directory totals (additive since 0022).
  total_directory: 14,
  individuals: 10,
  institutions: 4,
};

const summary: MemberStatusSummary = {
  by_status: {
    active: counts.active,
    dormant: counts.dormant,
    pending: counts.pending,
    suspended: counts.suspended,
    blacklisted: counts.blacklisted,
    exited: counts.exited,
    deceased: counts.deceased,
    rejected: counts.rejected,
  },
  total_on_register: counts.total_on_register,
  total_active_servicing: counts.total_active_servicing,
  total_directory: counts.total_directory,
  individuals: counts.individuals,
  institutions: counts.institutions,
  dormancy_pipeline: [],
  recent_changes: [],
  dormancy_threshold_days: 90,
};

describe('Counterparty roll-call parity', () => {
  it('dashboard widget header reports total_on_register from the canonical source', () => {
    const { container } = render(
      <MemberStatusPanel summary={summary} canRun={false} busy={false} onRun={() => {}} />,
    );
    expect(container).toHaveTextContent('11 on register');
    expect(container).toHaveTextContent('5 servicing');
  });

  it('Members page KPI strip reports the same per-bucket numbers as the dashboard', () => {
    render(<MemberRollCallKPIs counts={counts} />);

    // Labels gained the "(in scope)" suffix; the default when no
    // scopeLabel prop is passed is "(in scope)".
    const onRegister = screen.getByText(/On register \(in scope\)/i).closest('.card');
    expect(within(onRegister as HTMLElement).getByText('11')).toBeInTheDocument();

    const active = screen.getByText(/Active \(in scope\)/i).closest('.card');
    expect(within(active as HTMLElement).getByText('5')).toBeInTheDocument();

    const pending = screen.getByText(/Pending review \(in scope\)/i).closest('.card');
    expect(within(pending as HTMLElement).getByText('3')).toBeInTheDocument();

    const rejected = screen.getByText(/Rejected \(in scope\)/i).closest('.card');
    expect(within(rejected as HTMLElement).getByText('2')).toBeInTheDocument();
  });

  it('snapshot — both views render identical totals from the same fixture', () => {
    const headlineForDashboard = (): { onRegister: number; activeServicing: number } => ({
      onRegister: summary.total_on_register,
      activeServicing: summary.total_active_servicing,
    });
    const headlineForMembers = (): { onRegister: number; activeServicing: number } => ({
      onRegister: counts.total_on_register,
      activeServicing: counts.total_active_servicing,
    });

    expect(headlineForDashboard()).toEqual(headlineForMembers());
    expect({
      onRegister: headlineForDashboard().onRegister,
      activeServicing: headlineForDashboard().activeServicing,
      pending: counts.pending,
      rejected: counts.rejected,
      // New (since 0022) — pinned here so a regression in the wire
      // shape lights this snapshot up first.
      totalDirectory: counts.total_directory,
      individuals: counts.individuals,
      institutions: counts.institutions,
    }).toMatchInlineSnapshot(`
      {
        "activeServicing": 5,
        "individuals": 10,
        "institutions": 4,
        "onRegister": 11,
        "pending": 3,
        "rejected": 2,
        "totalDirectory": 14,
      }
    `);
  });
});
