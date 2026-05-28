// Smoke test for the CashApprovals → /approvals redirect.
//
// Pinned properties:
//   * window.location.replace is called on mount with /approvals as
//     the base path.
//   * The query string contains every cash family kind (so the
//     inbox lands pre-filtered to what users previously saw on this
//     page).
//   * The body shows a "Redirecting…" message so a tester who lands
//     here mid-redirect knows what's happening rather than seeing a
//     blank page.

import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';

import CashApprovals from './CashApprovals';

let replaceCalls: string[] = [];

beforeEach(() => {
  replaceCalls = [];
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      replace: (url: string) => { replaceCalls.push(url); },
      pathname: '/cash-approvals',
      href: 'http://localhost/cash-approvals',
    },
  });
});

describe('CashApprovals redirect', () => {
  it('calls window.location.replace with /approvals on mount', () => {
    render(<CashApprovals />);
    expect(replaceCalls.length).toBe(1);
    expect(replaceCalls[0]).toMatch(/^\/approvals\?kinds=/);
  });

  it('includes every cash family kind in the query string', () => {
    render(<CashApprovals />);
    const target = replaceCalls[0];

    // Every kind that was previously surfaced on /cash-approvals
    // must be in the filter so the user lands on the same scope.
    const requiredKinds = [
      'cash_deposit', 'cash_withdrawal', 'cash_account_transfer',
      'share_purchase', 'share_transfer', 'share_bonus_issue', 'share_lien',
      'loan_disbursement', 'loan_repayment', 'loan_settle', 'loan_reverse',
      'loan_write_off', 'loan_reschedule', 'loan_moratorium', 'loan_settlement_discount',
      'fee_posting', 'welfare_posting', 'application_fee', 'member_bosa_exit',
    ];
    for (const kind of requiredKinds) {
      expect(target).toContain(kind);
    }
  });

  it('renders a "Redirecting…" message in the meantime', () => {
    render(<CashApprovals />);
    expect(screen.getByText(/redirecting to the unified approvals inbox/i)).toBeInTheDocument();
  });

  it('explains the move in muted helper copy', () => {
    render(<CashApprovals />);
    expect(screen.getByText(/moved to/i)).toBeInTheDocument();
    expect(screen.getByText(/\/approvals/)).toBeInTheDocument();
  });
});
