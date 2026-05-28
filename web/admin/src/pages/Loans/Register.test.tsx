// Smoke tests for the Loans Register page.

import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({ hasPermission: () => true, tenant: { currency_code: 'KES' } }),
}));

const { listSpy, productsSpy } = vi.hoisted(() => ({
  listSpy: vi.fn(),
  productsSpy: vi.fn(),
}));

vi.mock('../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../api/client')>('../../api/client');
  return {
    ...actual,
    listLoans: listSpy,
    listLoanProducts: productsSpy,
  };
});

import LoansRegister from './Register';

const LOAN_ROWS = {
  items: [
    {
      loan: {
        id: 'l1',
        loan_no: 'LN-1001',
        application_id: 'a1',
        counterparty_id: 'c1',
        product_id: 'p1',
        status: 'active',
        principal: '50000',
        interest_rate_pct: '15',
        interest_method: 'reducing_balance',
        repayment_method: 'equal_installments',
        term_months: 12,
        grace_period_months: 0,
        installment_count: 12,
        first_due_date: '2027-01-01',
        total_fees_deducted: '0',
        principal_disbursed: '50000',
        principal_balance: '40000',
        interest_balance: '500',
        fees_balance: '0',
        penalty_balance: '0',
      },
      member_no: 'M001',
      member_name: 'Alice Wanjiku',
      product_code: 'DEV',
      product_name: 'Development loan',
    },
  ],
  total: 1,
};

beforeEach(() => {
  listSpy.mockReset();
  productsSpy.mockReset().mockResolvedValue([]);
});

describe('LoansRegister', () => {
  it('renders the table with the loan row + key columns', async () => {
    listSpy.mockResolvedValue(LOAN_ROWS);
    render(<LoansRegister />);
    await waitFor(() => expect(screen.getByText('LN-1001')).toBeInTheDocument());
    expect(screen.getByText('Alice Wanjiku')).toBeInTheDocument();
    expect(screen.getByText('Development loan')).toBeInTheDocument();
    // Outstanding = principal_balance + interest_balance + fees + penalty = 40500
    expect(screen.getByText(/KES 40,500\.00/)).toBeInTheDocument();
  });

  it('navigates to the detail page on row click', async () => {
    listSpy.mockResolvedValue(LOAN_ROWS);
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { href: '/loans/register', pathname: '/loans/register', search: '' },
    });

    render(<LoansRegister />);
    await waitFor(() => expect(screen.getByText('LN-1001')).toBeInTheDocument());

    fireEvent.click(screen.getByText('LN-1001').closest('tr')!);
    expect(window.location.href).toBe('/loans/register/l1');
  });

  it('renders empty state when no loans match', async () => {
    listSpy.mockResolvedValue({ items: [], total: 0 });
    render(<LoansRegister />);
    await waitFor(() => expect(screen.getByText(/No loans match the filter/i)).toBeInTheDocument());
  });
});
