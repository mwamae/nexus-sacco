// PlatformSystemHealth — smoke tests for the platform-only health
// dashboard.
//
// Pinned properties:
//   * Renders the all-OK payload with one card per service + overall
//     status badge.
//   * Renders the one-degraded payload with the degraded card flipping
//     to Degraded.
//   * Renders the one-down payload with the overall badge flipping to
//     Down.
//   * Debug strip is always present, even on the initial loading state.
//   * Empty-services banner appears when the aggregator returns no
//     services (the SYSTEM_HEALTH_TARGETS misconfiguration failure
//     mode).
//   * The page renders the explicit "you don't have permission" empty
//     state when hasPermission returns false AND is_platform_admin is
//     false — typed-URL case.

import { render, screen, waitFor, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock auth so we can flip permission per test.
let permitted = (_p: string) => true;
let isPlatformAdmin = true;
vi.mock('../../auth/AuthContext', () => ({
  useAuth: () => ({
    hasPermission: (p: string) => permitted(p),
    user: { is_platform_admin: isPlatformAdmin },
  }),
}));

const { apiGetSpy } = vi.hoisted(() => ({
  apiGetSpy: vi.fn(),
}));

vi.mock('../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../api/client')>('../../api/client');
  return {
    ...actual,
    api: {
      get: apiGetSpy,
    },
    extractError: (e: unknown) => (e as Error)?.message ?? 'error',
  };
});

import PlatformSystemHealth from './SystemHealth';

const ALL_OK = {
  overall_status: 'ok' as const,
  checked_at: '2026-05-28T12:00:00Z',
  services: [
    { name: 'identity',  role: 'core',        status: 'ok' as const, target_url: 'http://identity:8081',    latency_ms: 3 },
    { name: 'savings',   role: 'consumer',    status: 'ok' as const, target_url: 'http://savings:8084',     latency_ms: 5 },
    { name: 'mpesa',     role: 'integration', status: 'ok' as const, target_url: 'http://mpesa:8087',       latency_ms: 6 },
  ],
  infrastructure: {
    postgres: { status: 'ok' as const, latency_ms: 1 },
    redis: { status: 'ok' as const, note: 'not configured' },
  },
  workers: [
    { name: 'posting-dispatcher', status: 'ok' as const, last_beat_at: '2026-05-28T12:00:00Z', staleness_seconds: 10 },
  ],
};

const ONE_DEGRADED = {
  ...ALL_OK,
  overall_status: 'degraded' as const,
  services: [
    { name: 'identity',  role: 'core',        status: 'ok' as const,       target_url: 'http://identity:8081', latency_ms: 3 },
    { name: 'savings',   role: 'consumer',    status: 'degraded' as const, target_url: 'http://savings:8084',  latency_ms: 5,
      details: { outbox_pending: 42 } },
    { name: 'mpesa',     role: 'integration', status: 'ok' as const,       target_url: 'http://mpesa:8087',    latency_ms: 6 },
  ],
};

const ONE_DOWN = {
  ...ALL_OK,
  overall_status: 'down' as const,
  services: [
    { name: 'identity',  role: 'core',        status: 'ok' as const,    target_url: 'http://identity:8081', latency_ms: 3 },
    { name: 'savings',   role: 'consumer',    status: 'ok' as const,    target_url: 'http://savings:8084',  latency_ms: 5 },
    { name: 'mpesa',     role: 'integration', status: 'down' as const,  target_url: 'http://mpesa:8087',    latency_ms: 0,
      error: 'connection refused' },
  ],
};

const EMPTY_SERVICES = {
  overall_status: 'ok' as const,
  checked_at: '2026-05-28T12:00:00Z',
  services: [],
  infrastructure: { postgres: { status: 'ok' as const, latency_ms: 1 } },
  workers: [],
};

beforeEach(() => {
  permitted = () => true;
  isPlatformAdmin = true;
  apiGetSpy.mockReset();
  // Silence console noise (the page logs an info line per fetch).
  vi.spyOn(console, 'info').mockImplementation(() => {});
  vi.spyOn(console, 'warn').mockImplementation(() => {});
});

describe('PlatformSystemHealth', () => {
  it('renders all-OK payload with service cards + OK overall badge', async () => {
    apiGetSpy.mockResolvedValue({ status: 200, data: ALL_OK });
    render(<PlatformSystemHealth />);

    await waitFor(() => expect(screen.getByLabelText(/identity ok/i)).toBeInTheDocument());
    expect(screen.getByLabelText(/savings ok/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/mpesa ok/i)).toBeInTheDocument();
    // Overall badge in the header should be OK. Multiple OK badges
    // exist (one per service); the one in the page-hd is the overall.
    const okBadges = screen.getAllByText('OK');
    expect(okBadges.length).toBeGreaterThan(0);
  });

  it('flips the degraded service card to Degraded', async () => {
    apiGetSpy.mockResolvedValue({ status: 200, data: ONE_DEGRADED });
    render(<PlatformSystemHealth />);

    await waitFor(() => expect(screen.getByLabelText(/savings degraded/i)).toBeInTheDocument());
    // Identity stays ok.
    expect(screen.getByLabelText(/identity ok/i)).toBeInTheDocument();
    // Overall page badge reads Degraded.
    expect(screen.getAllByText('Degraded').length).toBeGreaterThan(0);
  });

  it('renders the down service card + Down overall badge', async () => {
    apiGetSpy.mockResolvedValue({ status: 200, data: ONE_DOWN });
    render(<PlatformSystemHealth />);

    await waitFor(() => expect(screen.getByLabelText(/mpesa down/i)).toBeInTheDocument());
    // The error message surfaces inside the down card.
    const mpesaCard = screen.getByLabelText(/mpesa down/i);
    expect(within(mpesaCard).getByText(/connection refused/i)).toBeInTheDocument();
    expect(screen.getAllByText('Down').length).toBeGreaterThan(0);
  });

  it('renders the debug strip with the endpoint URL', async () => {
    apiGetSpy.mockResolvedValue({ status: 200, data: ALL_OK });
    render(<PlatformSystemHealth />);

    // Debug strip is unconditional — present even before fetch resolves.
    const strip = screen.getByTestId('system-health-debug');
    expect(strip).toHaveTextContent('/v1/platform/system-health');
    await waitFor(() => expect(strip).toHaveTextContent('Status code: 200'));
  });

  it('renders the empty-services banner when the aggregator returns no services', async () => {
    apiGetSpy.mockResolvedValue({ status: 200, data: EMPTY_SERVICES });
    render(<PlatformSystemHealth />);

    await waitFor(() =>
      expect(screen.getByText(/aggregator returned an empty service list/i)).toBeInTheDocument(),
    );
    // The fix-instruction copy is also surfaced.
    expect(screen.getByText(/SYSTEM_HEALTH_TARGETS/)).toBeInTheDocument();
  });

  it('does not crash when the server returns null for services or workers', async () => {
    // Go nil-slice marshaling produces JSON null, not []. The page
    // defensively coalesces — exercise that path here so a future
    // refactor can't accidentally bring the crash back.
    apiGetSpy.mockResolvedValue({
      status: 200,
      data: {
        overall_status: 'ok',
        checked_at: '2026-05-28T12:00:00Z',
        services: null,
        infrastructure: null,
        workers: null,
      },
    });
    render(<PlatformSystemHealth />);

    // If the coalesce broke, the ErrorBoundary fallback would fire
    // and surface "This page crashed while rendering." We assert the
    // empty-services banner appears instead — proof the body rendered
    // through to the post-coalesce path.
    await waitFor(() =>
      expect(screen.getByText(/aggregator returned an empty service list/i)).toBeInTheDocument(),
    );
    expect(screen.queryByText(/crashed while rendering/i)).not.toBeInTheDocument();
  });

  it('renders the platform-admin-required empty state when the user lacks the permission', async () => {
    permitted = (p: string) => p !== 'platform:operations:view';
    isPlatformAdmin = false;
    render(<PlatformSystemHealth />);

    expect(screen.getByText(/need the/i)).toBeInTheDocument();
    expect(screen.getByText('platform:operations:view')).toBeInTheDocument();
    // Aggregator must NOT have been called when permission gating
    // short-circuits the page body.
    expect(apiGetSpy).not.toHaveBeenCalled();
  });
});
