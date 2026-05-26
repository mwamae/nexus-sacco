// Members page — counts reconciliation tests.
//
// Locks in the contract that every number rendered on /members
// (header sub-line, KPI strip, register card-sub, pager footer)
// derives from the same /v1/counterparties/status/counts response
// scoped to the page's active filters. The test mocks the API so we
// can drive the page through a 250-row fixture (180 individuals + 70
// orgs) and assert the surface text + pager behaviour directly.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';

let hasPerm = (_p: string) => true;
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: (p: string) => hasPerm(p),
    tenant: { currency_code: 'KES' },
  }),
}));

// Build mocks for the two endpoints Members.tsx consumes. The mocks
// honour the kind / status / q filters so we can test scope switches
// without changing fixture wiring per test.
type Mix = { individuals: number; institutions: number; perStatus: Record<string, { individuals: number; institutions: number }> };

const FIXTURE: Mix = {
  individuals: 180,
  institutions: 70,
  perStatus: {
    active:      { individuals: 140, institutions: 50 },
    dormant:     { individuals: 20,  institutions: 10 },
    pending:     { individuals: 10,  institutions: 5  },
    suspended:   { individuals: 3,   institutions: 2  },
    blacklisted: { individuals: 2,   institutions: 1  },
    exited:      { individuals: 3,   institutions: 1  },
    deceased:    { individuals: 1,   institutions: 0  },
    rejected:    { individuals: 1,   institutions: 1  },
  },
};

function buildCounts(opts: { kind?: 'all' | 'individual' | 'institutional'; status?: string[]; q?: string }) {
  const kind = opts.kind ?? 'all';
  const statuses = opts.status ?? Object.keys(FIXTURE.perStatus);
  // q filter is honoured by returning zero rows when a known
  // sentinel "no_match" is passed — adequate for the search test.
  if (opts.q === 'no_match') {
    return zeroCounts();
  }
  const pick = (k: 'individuals' | 'institutions') => {
    if (kind === 'individual' && k === 'institutions') return 0;
    if (kind === 'institutional' && k === 'individuals') return 0;
    return statuses.reduce((acc, s) => acc + FIXTURE.perStatus[s][k], 0);
  };
  const individuals = pick('individuals');
  const institutions = pick('institutions');
  const total_directory = individuals + institutions;
  const bucket = (s: string): number => {
    if (!statuses.includes(s)) return 0;
    const i = kind === 'institutional' ? 0 : FIXTURE.perStatus[s].individuals;
    const o = kind === 'individual'    ? 0 : FIXTURE.perStatus[s].institutions;
    return i + o;
  };
  const active = bucket('active');
  const dormant = bucket('dormant');
  return {
    active, dormant,
    pending:     bucket('pending'),
    suspended:   bucket('suspended'),
    blacklisted: bucket('blacklisted'),
    exited:      bucket('exited'),
    deceased:    bucket('deceased'),
    rejected:    bucket('rejected'),
    total_on_register:      bucket('active') + bucket('dormant') + bucket('pending') + bucket('suspended') + bucket('blacklisted'),
    total_active_servicing: active + dormant,
    total_directory,
    individuals,
    institutions,
  };
}

function zeroCounts() {
  return {
    active: 0, dormant: 0, pending: 0, suspended: 0, blacklisted: 0,
    exited: 0, deceased: 0, rejected: 0,
    total_on_register: 0, total_active_servicing: 0,
    total_directory: 0, individuals: 0, institutions: 0,
  };
}

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return {
    ...actual,
    listCounterparties: vi.fn().mockImplementation(async (params: { limit?: number; offset?: number } = {}) => {
      const limit = params.limit ?? 50;
      const offset = params.offset ?? 0;
      const counts = buildCounts(params as Parameters<typeof buildCounts>[0]);
      const rows = Array.from({ length: Math.max(0, Math.min(limit, counts.total_directory - offset)) }).map((_, i) => ({
        id: `cp-${offset + i}`,
        tenant_id: 't',
        cp_number: `CP-2025-${String(offset + i).padStart(5, '0')}`,
        legacy_id: null,
        kind: 'individual' as const,
        display_name: `Fixture Member ${offset + i + 1}`,
        status: 'active' as const,
        kyc_state: 'verified' as const,
        risk_band: 'low' as const,
        registration_no: null,
        individual: { full_name: `Fixture Member ${offset + i + 1}` },
        institution: null,
        contact: {},
        joined_at: '2025-01-01T00:00:00Z',
        closed_at: null,
        created_at: '2025-01-01T00:00:00Z',
        updated_at: '2025-01-01T00:00:00Z',
        legacy_target_id: `m-${offset + i}`,
      }));
      return { counterparties: rows, total: counts.total_directory, individuals: counts.individuals, institutions: counts.institutions, limit, offset };
    }),
    getCounterpartyStatusCounts: vi.fn().mockImplementation(async (p) => buildCounts(p ?? {})),
    // Older fetcher still exported; not called by the new Members.tsx
    // but other components keep importing it.
    getMemberStatusCounts: vi.fn().mockResolvedValue(zeroCounts()),
  };
});

import Members from './Members';

beforeEach(() => {
  hasPerm = () => true;
  // happy-dom enforces same-origin on history.replaceState, so the
  // mocked href must match the test runner origin (localhost:3000).
  Object.defineProperty(window, 'location', {
    writable: true,
    value: { pathname: '/members', search: '', href: 'http://localhost:3000/members' },
  });
});

// flatText returns the entire rendered document text with whitespace
// collapsed — needed because React breaks "Showing rows 1–50 of 250"
// across multiple text nodes (each interpolated number gets its own
// node) and getByText's substring matchers don't walk past a single
// text node. Assertions just substring-match against flatText() so
// the test is robust to React fragmenting the DOM however it likes.
function flatText(): string {
  return (document.body.textContent ?? '').replace(/\s+/g, ' ').trim();
}
function expectText(needle: string | RegExp) {
  const t = flatText();
  if (typeof needle === 'string') {
    expect(t).toContain(needle);
  } else {
    expect(t).toMatch(needle);
  }
}

describe('Members — counts reconciliation', () => {
  it('header, KPI strip, and footer all agree at default scope', async () => {
    render(<Members />);
    await waitFor(() => { expectText(/250 total/); });
    expectText(/180 individuals/);
    expectText(/70 organisations/);

    // KPI "On register (all kinds)" = active+dormant+pending+suspended+blacklisted
    // = 190 + 30 + 15 + 5 + 3 = 243.
    const onRegister = screen.getByText(/On register \(all kinds\)/i).closest('.card');
    expect(within(onRegister as HTMLElement).getByText('243')).toBeInTheDocument();

    // Pager footer mirrors counts (page size 50)
    expectText(/rows 1–50 of 250/);
    expectText(/Page 1 of 5/);
  });

  it('switching kindFilter to institutional updates every number consistently', async () => {
    const user = userEvent.setup();
    render(<Members />);
    await waitFor(() => { expectText(/250 total/); });

    await user.click(screen.getByRole('button', { name: 'institutional' }));

    await waitFor(() => { expectText(/70 organisations in scope/); });
    // KPI strip relabelled to "(organisations)"; on-register for orgs only:
    // 50 active + 10 dormant + 5 pending + 2 suspended + 1 blacklisted = 68
    const onRegisterOrgs = screen.getByText(/On register \(organisations\)/i).closest('.card');
    expect(within(onRegisterOrgs as HTMLElement).getByText('68')).toBeInTheDocument();
  });

  it('switching status chip to pending narrows sub-title + KPI to pending count', async () => {
    const user = userEvent.setup();
    render(<Members />);
    await waitFor(() => { expectText(/250 total/); });

    await user.click(screen.getByRole('button', { name: 'pending' }));

    // Sub-title now reports the pending count (15 across both kinds)
    await waitFor(() => { expectText(/15 pending/); });
    // "Pending review (all kinds)" KPI also reads 15
    const pendingCard = screen.getByText(/Pending review \(all kinds\)/i).closest('.card');
    expect(within(pendingCard as HTMLElement).getByText('15')).toBeInTheDocument();
  });

  it('Next pager button advances offset by PAGE_SIZE', async () => {
    const user = userEvent.setup();
    render(<Members />);
    await waitFor(() => { expectText(/rows 1–50 of 250/); });

    await user.click(screen.getByRole('button', { name: /Next/i }));

    await waitFor(() => { expectText(/rows 51–100 of 250/); });
    expectText(/Page 2 of 5/);
  });
});
