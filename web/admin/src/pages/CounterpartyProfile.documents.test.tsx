// Smoke tests for the Documents & KYC workstation tab.
//
// Verifies the user-visible properties that aren't backed by tsc:
//   - "Add document" button is gated on members:edit
//   - the KYC banner reports verified / required progress
//   - the Add modal blocks submission when expiry precedes issue
//   - the Reject dialog requires a note before its submit enables
//
// CounterpartyProfile is rendered via a thin harness that exposes only
// DocumentsTab — the page's own URL routing + auth provider are
// expensive to wire up and out of scope for this test.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock the auth context so we can flip canEdit between tests.
let hasPerm = (_p: string) => true;
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: (p: string) => hasPerm(p),
    tenant: { currency_code: 'KES' },
  }),
}));

// Mock the API client surface. Only the helpers DocumentsTab calls
// need to exist; everything else is unused in this test.
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return {
    ...actual,
    getMember: vi.fn(),
    getOrg: vi.fn(),
    listAuditForTarget: vi.fn().mockResolvedValue([]),
    uploadMemberDocument: vi.fn(),
    verifyMemberDocument: vi.fn(),
    deleteMemberDocument: vi.fn(),
    memberDocumentURL: vi.fn().mockReturnValue('http://x/doc'),
    fetchMemberDocument: vi.fn(),
    uploadOrgDocument: vi.fn(),
    verifyOrgDocument: vi.fn(),
    deleteOrgDocument: vi.fn(),
    fetchOrgDocument: vi.fn(),
  };
});

// CounterpartyProfile pulls in AsyncPanel + Tabs + several heavy
// trees; we render DocumentsTab directly via a tiny harness so the
// test stays fast and focused.
import type { ApiDocument, ApiMemberDetail } from '../api/client';

// The DocumentsTab function isn't exported (it's an internal component
// of CounterpartyProfile). For a focused unit test we reach in via the
// shared module — we wrap the same logic in a thin re-render-friendly
// component below.
//
// Because DocumentsTab is private, the test renders the page-level
// component instead and asserts on observable DOM. To keep the test
// hermetic, getMember is the mocked entry point that gates everything
// behind it.

import CounterpartyProfile from './CounterpartyProfile';

function makeMember(overrides: Partial<ApiMemberDetail> = {}, docs: ApiDocument[] = []): ApiMemberDetail {
  const base: ApiMemberDetail = {
    id: 'm-uuid', tenant_id: 't-uuid', member_no: 'M-1', status: 'active',
    full_name: 'Alice Tester', id_doc_kind: 'national_id', id_doc_number: 'ID-1',
    gender: 'female', created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z',
    next_of_kin: null, beneficiaries: [], documents: docs,
    counterparty_id: 'cp-uuid', cp_number: 'CP-2025-00001',
  };
  return { ...base, ...overrides };
}

const VERIFIED_DOC: ApiDocument = {
  id: 'd1', counterparty_id: 'cp-uuid', kind: 'id_front', mime: 'image/png',
  size_bytes: 1024, verification: 'verified', uploaded_at: '2025-05-01T00:00:00Z',
};

beforeEach(() => {
  hasPerm = () => true;
  // The page reads window.location.pathname for the id + kind. We're
  // testing the individual side, so pretend we're under /members/.
  Object.defineProperty(window, 'location', {
    writable: true,
    value: { pathname: '/members/m-uuid', search: '?tab=documents', href: 'http://x/members/m-uuid?tab=documents' },
  });
  // Document.location is read in usePageCrumb / useDocumentTitle; the
  // simplest stub is a no-op title setter.
});

describe('DocumentsTab — workstation', () => {
  it('hides Add document button when the user lacks members:edit', async () => {
    hasPerm = (p) => p !== 'members:edit';
    const { getMember } = await import('../api/client');
    (getMember as ReturnType<typeof vi.fn>).mockResolvedValueOnce(makeMember({}, [VERIFIED_DOC]));

    render(<CounterpartyProfile />);
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Documents & KYC' })).toBeInTheDocument());

    expect(screen.queryByRole('button', { name: /Add document/i })).not.toBeInTheDocument();
  });

  it('shows verified/required progress in the KYC banner', async () => {
    const { getMember } = await import('../api/client');
    (getMember as ReturnType<typeof vi.fn>).mockResolvedValueOnce(makeMember({}, [VERIFIED_DOC]));

    render(<CounterpartyProfile />);
    await waitFor(() => expect(screen.getByText('KYC completion')).toBeInTheDocument());

    // KYC_REQUIRED_INDIVIDUAL has 5 entries; we uploaded id_front
    // (verified). Banner should reflect 1 verified / 1 uploaded of 5.
    expect(screen.getByText('1/5 verified · 1/5 uploaded')).toBeInTheDocument();
  });

  it('blocks Add submission when expiry precedes issue date', async () => {
    const user = userEvent.setup();
    const { getMember } = await import('../api/client');
    (getMember as ReturnType<typeof vi.fn>).mockResolvedValueOnce(makeMember({}, []));

    render(<CounterpartyProfile />);
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Documents & KYC' })).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: /Add document/i }));
    const dialog = await screen.findByRole('heading', { name: 'Add document' });
    const modal = dialog.closest('.card') as HTMLElement;

    // Issue 2025-05-10, expiry 2025-05-01 → invalid. The submit
    // button should refuse to enable until we provide a file too, but
    // even with a file the date check should still bite — assert the
    // alert reaches the DOM after clicking the disabled-because-no-
    // file path would be a different assertion. Here we focus on the
    // *combination* path: we need a valid file first.
    const fileInput = modal.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File(['x'], 'id.png', { type: 'image/png' });
    await user.upload(fileInput, file);

    const dateInputs = modal.querySelectorAll('input[type="date"]');
    await user.type(dateInputs[0] as HTMLInputElement, '2025-05-10');
    await user.type(dateInputs[1] as HTMLInputElement, '2025-05-01');

    await user.click(within(modal).getByRole('button', { name: 'Upload' }));

    await waitFor(() => {
      expect(within(modal).getByText(/Expiry date must be on or after issue date/i)).toBeInTheDocument();
    });
  });

  it('keeps the Reject submit disabled until a note is entered', async () => {
    const user = userEvent.setup();
    const { getMember } = await import('../api/client');
    (getMember as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      makeMember({}, [{ ...VERIFIED_DOC, verification: 'pending' }]),
    );

    render(<CounterpartyProfile />);
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Documents & KYC' })).toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Reject' }));
    const heading = await screen.findByRole('heading', { name: /Reject National ID/i });
    const modal = heading.closest('.card') as HTMLElement;

    const submit = within(modal).getByRole('button', { name: 'Reject' });
    expect(submit).toBeDisabled();

    await user.type(within(modal).getByLabelText(/Reason/i), 'photo is blurry');
    expect(submit).toBeEnabled();
  });
});
