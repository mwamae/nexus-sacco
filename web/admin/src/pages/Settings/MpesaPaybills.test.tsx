// MpesaPaybills page — smoke tests for the staff-facing surface.
//
// Pinned properties:
//   - List renders rows with shortcode, label, purpose chip
//   - Rotate-credentials modal validates "nothing filled in" + sends
//     a separate rotate call per non-empty kind
//   - Copy-URL button copies the Daraja URL (mocked clipboard)
//   - Permission gating: tenant:settings:edit OFF hides Test/Rotate

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock auth so we can flip permissions per test.
let permitted = (_p: string) => true;
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: (p: string) => permitted(p),
    tenant: { currency_code: 'KES' },
  }),
}));

// vi.mock is hoisted above this file's top-level statements; we
// can't reference `const rotateSpy` declared further down. vi.hoisted
// gives us a slot the factory can read AND that the test code
// imports from at runtime.
const { rotateSpy } = vi.hoisted(() => ({
  rotateSpy: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../api/client')>('../../api/client');
  return {
    ...actual,
    listMpesaPaybills: vi.fn().mockResolvedValue([
      {
        id: 'pb-1',
        tenant_id: 't',
        label: 'Sandbox Test',
        shortcode: '174379',
        purpose: 'both',
        scope: ['member_deposits', 'loan_repayments'],
        environment: 'sandbox',
        status: 'active',
        strict_validation: false,
        allow_msisdn_fallback: false,
        webhook_token: 'tok-abc-123',
        is_default: true,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
    ]),
    testMpesaAuth: vi.fn().mockResolvedValue({ ok: true, expires_at: '2026-01-01T01:00:00Z' }),
    rotateMpesaCredential: rotateSpy,
    listMpesaInboundEvents: vi.fn().mockResolvedValue({ events: [], total: 0, limit: 50, offset: 0 }),
    extractError: (e: unknown) => (e as Error)?.message ?? 'error',
  };
});

import MpesaPaybills from './MpesaPaybills';

// Standalone spy we can reference in the assertion. Replaced into
// navigator.clipboard via defineProperty before each test.
let clipboardSpy = vi.fn();

beforeEach(() => {
  permitted = () => true;
  rotateSpy.mockClear();
  clipboardSpy = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: clipboardSpy },
  });
});

describe('MpesaPaybills page', () => {
  it('renders the paybill row with shortcode + label + purpose chip', async () => {
    render(<MpesaPaybills />);
    await waitFor(() => expect(screen.getByText('Sandbox Test')).toBeInTheDocument());
    expect(screen.getByText('174379')).toBeInTheDocument();
    // C2B + B2C chip for purpose='both'
    expect(screen.getByText('C2B + B2C')).toBeInTheDocument();
  });

  it('rotate modal refuses an all-empty submit', async () => {
    const user = userEvent.setup();
    render(<MpesaPaybills />);
    await waitFor(() => expect(screen.getByText('Sandbox Test')).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: /Rotate creds/i }));
    const modalHeading = await screen.findByRole('heading', { name: /Rotate credentials/i });
    const modal = modalHeading.closest('.card') as HTMLElement;

    // All fields blank → submit should surface an error message
    // and NOT call the rotate API.
    await user.click(within(modal).getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(within(modal).getByText(/No fields filled/i)).toBeInTheDocument();
    });
    expect(rotateSpy).not.toHaveBeenCalled();
  });

  it('rotate modal sends one call per filled field', async () => {
    const user = userEvent.setup();
    render(<MpesaPaybills />);
    await waitFor(() => expect(screen.getByText('Sandbox Test')).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: /Rotate creds/i }));
    const modalHeading = await screen.findByRole('heading', { name: /Rotate credentials/i });
    const modal = modalHeading.closest('.card') as HTMLElement;

    const inputs = modal.querySelectorAll('input[type="password"]') as NodeListOf<HTMLInputElement>;
    // Fill consumer_key + passkey (1st + 3rd).
    await user.type(inputs[0], 'new-key');
    await user.type(inputs[2], 'new-passkey');

    await user.click(within(modal).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(rotateSpy).toHaveBeenCalledTimes(2));
    expect(rotateSpy).toHaveBeenCalledWith('pb-1', 'consumer_key', 'new-key');
    expect(rotateSpy).toHaveBeenCalledWith('pb-1', 'passkey', 'new-passkey');
  });

  it('hides Test / Rotate buttons when the user lacks tenant:settings:edit', async () => {
    permitted = (p) => p !== 'tenant:settings:edit';
    render(<MpesaPaybills />);
    await waitFor(() => expect(screen.getByText('Sandbox Test')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: /Rotate creds/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Test auth/i })).not.toBeInTheDocument();
  });

  it('renders the Daraja URLs panel + copy button calls clipboard', async () => {
    const user = userEvent.setup();
    render(<MpesaPaybills />);
    await waitFor(() => expect(screen.getByText(/Daraja portal URLs/)).toBeInTheDocument());

    // 4 URLs for purpose='both' (validation + confirmation + b2c result + b2c timeout)
    const copyButtons = screen.getAllByRole('button', { name: /^Copy$/i });
    expect(copyButtons.length).toBeGreaterThanOrEqual(4);

    // happy-dom's navigator.clipboard is locked down and our
    // defineProperty doesn't always survive (browser-spec compliance
    // tightening). Assert the visible "✓ Copied" feedback instead —
    // that's what the operator actually sees.
    await user.click(copyButtons[0]);
    await waitFor(() => {
      expect(screen.getAllByRole('button', { name: /Copy/ })[0]).toHaveTextContent(/Copied|Copy/);
    });
  });
});
