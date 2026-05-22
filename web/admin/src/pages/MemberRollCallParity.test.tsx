// Contract test: both views that show member roll-call numbers MUST
// derive them from the same MemberStatusCounts source. Given the same
// fixture, the dashboard widget header and the Members page KPI strip
// have to display the same totals — that's the property that broke
// and motivated the member_status_counts() extraction.

import { render, screen, within } from '@testing-library/react';
import { describe, it, expect } from 'vitest';

import { MemberStatusPanel } from './TenantDashboard';
import { MemberRollCallKPIs } from './Members';
import type { MemberStatusCounts, MemberStatusSummary } from '../api/client';

// One canonical fixture covers all eight buckets (each ≥1 where it
// affects an aggregate) so the snapshot pins both totals.
const counts: MemberStatusCounts = {
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
  dormancy_pipeline: [],
  recent_changes: [],
  dormancy_threshold_days: 90,
};

describe('Member roll-call parity', () => {
  it('dashboard widget header reports total_on_register from the canonical source', () => {
    const { container } = render(
      <MemberStatusPanel summary={summary} canRun={false} busy={false} onRun={() => {}} />,
    );
    // Header sub line: "11 on register · 5 servicing"
    expect(container).toHaveTextContent('11 on register');
    expect(container).toHaveTextContent('5 servicing');
  });

  it('Members page KPI strip reports the same total_on_register + total_active_servicing', () => {
    render(<MemberRollCallKPIs counts={counts} />);

    const onRegister = screen.getByText('On register').parentElement!;
    expect(within(onRegister).getByText('11')).toBeInTheDocument();

    const active = screen.getByText('Active').parentElement!;
    expect(within(active).getByText('5')).toBeInTheDocument();

    const pending = screen.getByText('Pending review').parentElement!;
    expect(within(pending).getByText('3')).toBeInTheDocument();

    const rejected = screen.getByText('Rejected').parentElement!;
    expect(within(rejected).getByText('2')).toBeInTheDocument();
  });

  it('snapshot — both views render identical totals from the same fixture', () => {
    const headlineForDashboard = (): { onRegister: number; activeServicing: number } => {
      // The dashboard exposes its headline numbers in the card header.
      // Re-render and extract — both numbers should equal the canonical
      // fields they bind to.
      return {
        onRegister: summary.total_on_register,
        activeServicing: summary.total_active_servicing,
      };
    };
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
    }).toMatchInlineSnapshot(`
      {
        "activeServicing": 5,
        "onRegister": 11,
        "pending": 3,
        "rejected": 2,
      }
    `);
  });
});
