// Smoke test for the Loan Application Detail action-bar visibility.
//
// Pinned property: each loan_application_status surfaces only the
// action buttons in ACTIONS_PER_STATUS — pending_approval shows
// approve / decline / return / counter; terminal statuses show none.

import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../../../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: () => true, // approve perm on; lets the action bar render
    tenant: { currency_code: 'KES' },
  }),
}));

const { getSpy } = vi.hoisted(() => ({ getSpy: vi.fn() }));

vi.mock('../../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../../api/client')>('../../../api/client');
  return { ...actual, getLoanApplication: getSpy };
});

import LoanApplicationDetail from './LoanApplicationDetail';

function appFixture(status: string) {
  return {
    application: {
      id: 'a1', application_no: 'APP-1',
      counterparty_id: 'c1', product_id: 'p1',
      status,
      requested_amount: '50000', requested_term_months: 12,
      monthly_net_income: '40000', other_income: '0',
      monthly_expenses: '20000', monthly_existing_obligations: '0',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    },
    guarantees: [],
    collateral: [],
  };
}

beforeEach(() => {
  getSpy.mockReset();
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { pathname: '/loans/applications/a1' },
  });
});

describe('LoanApplicationDetail action bar', () => {
  it('pending_approval shows approve + decline + return + counter buttons', async () => {
    getSpy.mockResolvedValue(appFixture('pending_approval'));
    render(<LoanApplicationDetail />);
    await waitFor(() => expect(screen.getByRole('button', { name: /Approve as-is/i })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Decline/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Return for info/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Counter-offer/i })).toBeInTheDocument();
  });

  it('terminal status (declined) shows no action buttons', async () => {
    getSpy.mockResolvedValue(appFixture('declined'));
    render(<LoanApplicationDetail />);
    await waitFor(() => expect(screen.getAllByText('APP-1').length).toBeGreaterThan(0));
    expect(screen.queryByRole('button', { name: /Approve as-is/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Decline/i })).not.toBeInTheDocument();
  });

  it('renders all 9 tab labels', async () => {
    getSpy.mockResolvedValue(appFixture('pending_approval'));
    render(<LoanApplicationDetail />);
    await waitFor(() => expect(screen.getAllByText('APP-1').length).toBeGreaterThan(0));
    for (const tab of ['Overview', 'Income', 'Guarantors', 'Collateral', 'Documents', 'Score', 'Schedule preview', 'Timeline', 'Comments']) {
      expect(screen.getByRole('button', { name: tab })).toBeInTheDocument();
    }
  });
});
