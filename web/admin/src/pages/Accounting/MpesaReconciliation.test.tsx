// MpesaReconciliation page — smoke tests for the staff-facing
// reconciliation surface.
//
// Pinned properties:
//   - KPI cards render with the per-status counts
//   - Status chip filter narrows the table (assert mock receives the
//     status param)
//   - Re-run button only shows for failed rows AND only when the
//     user has tenant:settings:edit

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';

let permitted = (_p: string) => true;
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: (p: string) => permitted(p),
    tenant: { currency_code: 'KES' },
  }),
}));

const events = [
  {
    id: 'e1', tenant_id: 't', paybill_id: 'pb-1', shortcode: '174379',
    transaction_id: 'RKT1', transaction_time: null, amount: '100',
    msisdn: '254712345678', bill_ref: 'M-001', raw_payload: {},
    status: 'received', resolved_member_id: null, resolved_via: 'unallocated',
    workflow_instance_id: null, received_at: '2026-05-26T10:00:00Z',
  },
  {
    id: 'e2', tenant_id: 't', paybill_id: 'pb-1', shortcode: '174379',
    transaction_id: 'RKT2', transaction_time: null, amount: '500',
    msisdn: '254700000000', bill_ref: 'M-002', raw_payload: {},
    status: 'distributed', resolved_member_id: 'cp-1', resolved_via: 'member_no',
    workflow_instance_id: null, received_at: '2026-05-26T11:00:00Z',
  },
  {
    id: 'e3', tenant_id: 't', paybill_id: 'pb-1', shortcode: '174379',
    transaction_id: 'RKT3', transaction_time: null, amount: '250',
    msisdn: '254700000000', bill_ref: 'M-003', raw_payload: {},
    status: 'failed', resolved_member_id: null, resolved_via: null,
    workflow_instance_id: null, received_at: '2026-05-26T12:00:00Z',
  },
];

const listSpy = vi.hoisted(() => ({ fn: vi.fn() }));

vi.mock('../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../api/client')>('../../api/client');
  return {
    ...actual,
    listMpesaPaybills: vi.fn().mockResolvedValue([
      { id: 'pb-1', tenant_id: 't', label: 'Sandbox', shortcode: '174379',
        purpose: 'collection', scope: [], environment: 'sandbox', status: 'active',
        strict_validation: false, allow_msisdn_fallback: false,
        webhook_token: 'tok', created_at: '', updated_at: '' },
    ]),
    listMpesaInboundEvents: listSpy.fn,
    extractError: (e: unknown) => (e as Error)?.message ?? 'error',
  };
});

import MpesaReconciliation from './MpesaReconciliation';

beforeEach(() => {
  permitted = () => true;
  listSpy.fn.mockReset();
  listSpy.fn.mockImplementation(async (params: any) => {
    // Filter by status when supplied (mimics backend behaviour).
    const filtered = params?.status
      ? events.filter((e) => e.status === params.status)
      : events;
    return { events: filtered, total: filtered.length, limit: 200, offset: 0 };
  });
});

describe('MpesaReconciliation page', () => {
  it('renders KPI cards from the full window', async () => {
    render(<MpesaReconciliation />);
    await waitFor(() => expect(screen.getByText(/Inbound \(received\)/)).toBeInTheDocument());

    // Headline counts are computed locally from the unfiltered window —
    // 1 received, 1 distributed, 1 failed, 1 unallocated.
    expect(screen.getByText('Inbound (received)').closest('.card')!.textContent).toMatch(/1/);
    expect(screen.getByText('Distributed').closest('.card')!.textContent).toMatch(/1/);
    expect(screen.getByText('Failed').closest('.card')!.textContent).toMatch(/1/);
    expect(screen.getByText('Unallocated').closest('.card')!.textContent).toMatch(/1/);
  });

  it('clicking the failed chip narrows the table to failed rows', async () => {
    const user = userEvent.setup();
    render(<MpesaReconciliation />);
    await waitFor(() => expect(screen.getByText('RKT1')).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'failed' }));

    // After re-fetch, the table shows only RKT3.
    await waitFor(() => {
      expect(screen.queryByText('RKT1')).not.toBeInTheDocument();
      expect(screen.queryByText('RKT2')).not.toBeInTheDocument();
      expect(screen.getByText('RKT3')).toBeInTheDocument();
    });

    // The list call was made with status=failed at least once.
    const statuses = listSpy.fn.mock.calls.map((c) => c[0]?.status);
    expect(statuses).toContain('failed');
  });

  it('hides the Re-run button when the user lacks tenant:settings:edit', async () => {
    permitted = (p) => p !== 'tenant:settings:edit';
    const user = userEvent.setup();
    render(<MpesaReconciliation />);
    await waitFor(() => expect(screen.getByText('RKT3')).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: 'failed' }));
    await waitFor(() => expect(screen.getByText('RKT3')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: /Re-run/i })).not.toBeInTheDocument();
  });
});
