// Smoke tests for the slim PlatformStatusBadge on the tenant dashboard.
//
// Pinned properties:
//   * Fetches /api/v1/platform-status (the dev proxy maps /api → identity).
//   * Renders the three states (ok / degraded / down) with the right
//     message + status data attribute.
//   * Never exposes service-level fields (services / workers /
//     infrastructure). Tenant admins must see "platform is up/degraded"
//     and nothing more.

import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';

import { PlatformStatusBadge } from '../components/PlatformStatusBadge';

let lastFetchURL = '';
let fetchResolve!: (r: Response) => void;

beforeEach(() => {
  lastFetchURL = '';
  globalThis.fetch = vi.fn().mockImplementation((url: string) => {
    lastFetchURL = url;
    return new Promise<Response>((res) => { fetchResolve = res; });
  }) as unknown as typeof fetch;
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('PlatformStatusBadge', () => {
  it('fetches /api/v1/platform-status on mount', async () => {
    render(<PlatformStatusBadge />);
    expect(lastFetchURL).toBe('/api/v1/platform-status');
  });

  it('renders the OK state with the operational message', async () => {
    render(<PlatformStatusBadge />);
    fetchResolve(jsonResponse({
      overall_status: 'ok',
      checked_at: new Date().toISOString(),
      message: 'All systems operational',
    }));

    await waitFor(() => {
      const badge = screen.getByTestId('platform-status-badge');
      expect(badge.getAttribute('data-status')).toBe('ok');
      expect(badge).toHaveTextContent(/all systems operational/i);
    });
  });

  it('renders the degraded state with the operations-aware message', async () => {
    render(<PlatformStatusBadge />);
    fetchResolve(jsonResponse({
      overall_status: 'degraded',
      checked_at: new Date().toISOString(),
      message: 'Some non-critical systems are degraded — operations team has visibility',
    }));

    await waitFor(() => {
      const badge = screen.getByTestId('platform-status-badge');
      expect(badge.getAttribute('data-status')).toBe('degraded');
      expect(badge).toHaveTextContent(/operations team has visibility/i);
    });
  });

  it('renders the down state with the outage message', async () => {
    render(<PlatformStatusBadge />);
    fetchResolve(jsonResponse({
      overall_status: 'down',
      checked_at: new Date().toISOString(),
      message: 'An outage is in progress — operations team is engaged',
    }));

    await waitFor(() => {
      const badge = screen.getByTestId('platform-status-badge');
      expect(badge.getAttribute('data-status')).toBe('down');
      expect(badge).toHaveTextContent(/outage is in progress/i);
    });
  });

  it('never renders any service-level data', async () => {
    render(<PlatformStatusBadge />);
    fetchResolve(jsonResponse({
      overall_status: 'ok',
      checked_at: new Date().toISOString(),
      message: 'All systems operational',
    }));
    await waitFor(() => expect(screen.getByTestId('platform-status-badge')).toBeInTheDocument());

    // Hard guard: even if a future bug leaked richer data into the
    // tenant payload, the component code should not render any of
    // these terms inside its pill.
    const badge = screen.getByTestId('platform-status-badge');
    for (const leak of [/identity/i, /savings/i, /mpesa/i, /postgres/i, /worker/i, /heartbeat/i]) {
      expect(badge.textContent ?? '').not.toMatch(leak);
    }
  });

  it('renders nothing when the endpoint fails (does not crash the dashboard)', async () => {
    render(<PlatformStatusBadge />);
    fetchResolve(new Response('boom', { status: 500 }));

    await waitFor(() => {
      // The component returns null on error so it has no role; assert
      // the testid is gone (loading placeholder used the same testid).
      expect(screen.queryByTestId('platform-status-badge')).not.toBeInTheDocument();
    });
  });
});
