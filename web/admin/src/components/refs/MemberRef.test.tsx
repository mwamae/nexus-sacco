import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { MemberRef, __memberRefCache } from './MemberRef';
import type { Counterparty } from '../../api/client';

// Mock the API module — every test controls what getCounterparty
// returns and how many times it's called.
vi.mock('../../api/client', async () => {
  return {
    getCounterparty: vi.fn(),
  };
});

import { getCounterparty } from '../../api/client';
const mocked = getCounterparty as ReturnType<typeof vi.fn>;

function makeCounterparty(over: Partial<Counterparty> = {}): Counterparty {
  return {
    id: 'cp-1111-2222-3333-4444',
    tenant_id: 'tenant-1',
    cp_number: 'CP-2026-00003',
    legacy_id: 'M-2026-00003',
    kind: 'individual',
    display_name: 'Esther Wanjiru Waringa',
    status: 'active',
    kyc_state: 'verified',
    onboarded_at: null,
    legacy_target_id: 'member-7777',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    individual: null,
    institutional: null,
    contact: null,
    address: null,
    flags: null,
    ...over,
  } as Counterparty;
}

beforeEach(() => {
  mocked.mockReset();
  __memberRefCache._reset();
});

describe('<MemberRef>', () => {
  it('renders display name + CP number for an individual + links to /members/<legacy_target_id>', async () => {
    mocked.mockResolvedValueOnce(makeCounterparty());
    render(<MemberRef counterpartyId="cp-1111-2222-3333-4444" />);
    // Initial paint shows the slice-8 loading fallback.
    expect(screen.getByText(/cp-1111-/)).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByText('Esther Wanjiru Waringa')).toBeTruthy();
    });
    expect(screen.getByText(/CP-2026-00003/)).toBeTruthy();
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toBe('/members/member-7777');
  });

  it('links institutions to /orgs/<legacy_target_id> not /members/', async () => {
    mocked.mockResolvedValueOnce(makeCounterparty({
      kind: 'company',
      display_name: 'Acme Investments Ltd',
      legacy_target_id: 'org-9999',
    }));
    render(<MemberRef counterpartyId="cp-aaaa-bbbb-cccc-dddd" />);
    await waitFor(() => {
      expect(screen.getByText('Acme Investments Ltd')).toBeTruthy();
    });
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toBe('/orgs/org-9999');
  });

  it('falls back to the slice-8 uuid with a hint when the API errors', async () => {
    mocked.mockRejectedValueOnce(new Error('boom'));
    render(<MemberRef counterpartyId="cp-fail-xxxx-yyyy-zzzz" />);
    // After the rejected promise settles the resolver remains null;
    // we expect to keep seeing the loading-style slice fallback rather
    // than a thrown exception killing the row.
    await waitFor(() => {
      expect(screen.getByText(/cp-fail/)).toBeTruthy();
    });
    // Crucially: no link element — we don't pretend the lookup worked.
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('dedupes concurrent resolves for the same id (cache)', async () => {
    mocked.mockResolvedValue(makeCounterparty());
    // Render three independent MemberRefs all asking for the same id —
    // they should collapse to ONE underlying fetch.
    render(
      <>
        <MemberRef counterpartyId="cp-1111-2222-3333-4444" />
        <MemberRef counterpartyId="cp-1111-2222-3333-4444" />
        <MemberRef counterpartyId="cp-1111-2222-3333-4444" />
      </>,
    );
    await waitFor(() => {
      // All three rendered the same resolved member.
      expect(screen.getAllByText('Esther Wanjiru Waringa').length).toBe(3);
    });
    expect(mocked).toHaveBeenCalledTimes(1);
  });

  it('renders fallback "—" when no id is given', () => {
    render(<MemberRef />);
    expect(screen.getByText('—')).toBeTruthy();
    expect(mocked).not.toHaveBeenCalled();
  });
});
