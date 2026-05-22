/**
 * @vitest-environment happy-dom
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import MockAdapter from 'axios-mock-adapter';

import {
  api,
  saveTokens,
  loadTokens,
  refreshOnce,
  isAuthDeadError,
} from './client';

// Helper — make refreshOnce stateless across tests by stuffing fresh
// tokens before each run. The module's in-flight gate is released via
// queueMicrotask, so we await a microtask after each call below.
function seedTokens() {
  saveTokens({
    accessToken: 'access-old',
    refreshToken: 'refresh-old',
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    refreshExpiresAt: new Date(Date.now() + 600_000).toISOString(),
  });
}

const mock = new MockAdapter(api);

// The /auth/refresh request bypasses the api instance — it uses bare
// axios so the response interceptor doesn't recursively trigger refresh
// on its own 401s. So we mock axios.defaults.adapter via a second mock.
import axios from 'axios';
const refreshMock = new MockAdapter(axios);

describe('refreshOnce — session preservation on transient failure', () => {
  beforeEach(() => {
    mock.reset();
    refreshMock.reset();
    localStorage.clear();
    // Let the in-flight gate from any prior test drop.
    return new Promise<void>((r) => queueMicrotask(r));
  });

  it('preserves tokens when /auth/refresh fails with a network error', async () => {
    seedTokens();
    refreshMock.onPost('/api/v1/auth/refresh').networkError();

    const outcome = await refreshOnce();

    expect(outcome).toEqual({ kind: 'failed', authFailed: false });
    // Critical: tokens are still there. The user's session must survive
    // a flaky /auth/refresh — only a 401 from refresh should kill it.
    expect(loadTokens()).not.toBeNull();
    expect(loadTokens()?.refreshToken).toBe('refresh-old');
  });

  it('preserves tokens when /auth/refresh returns 500', async () => {
    seedTokens();
    refreshMock.onPost('/api/v1/auth/refresh').reply(500, { error: 'upstream' });

    const outcome = await refreshOnce();

    expect(outcome).toEqual({ kind: 'failed', authFailed: false });
    expect(loadTokens()).not.toBeNull();
  });

  it('clears tokens only when /auth/refresh returns 401', async () => {
    seedTokens();
    refreshMock.onPost('/api/v1/auth/refresh').reply(401, { error: 'token expired' });

    const outcome = await refreshOnce();

    expect(outcome).toEqual({ kind: 'failed', authFailed: true });
    expect(loadTokens()).toBeNull();
  });
});

describe('response interceptor — non-auth 401 does not nuke the session', () => {
  beforeEach(() => {
    mock.reset();
    refreshMock.reset();
    localStorage.clear();
    return new Promise<void>((r) => queueMicrotask(r));
  });

  it('marks the rejection as authDead=false when refresh fails on network', async () => {
    seedTokens();
    // A random tenant endpoint returns 401.
    mock.onGet('/v1/notifications').reply(401, { error: { code: 'unauthorized' } });
    // Refresh attempt fails on the network.
    refreshMock.onPost('/api/v1/auth/refresh').networkError();

    await expect(api.get('/v1/notifications')).rejects.toMatchObject({
      response: { status: 401 },
    });

    // Re-issue and capture for the authDead assertion.
    let captured: unknown = null;
    try {
      await api.get('/v1/notifications');
    } catch (err) {
      captured = err;
    }

    expect(isAuthDeadError(captured)).toBe(false);
    // And — the key invariant — tokens survived.
    expect(loadTokens()).not.toBeNull();
  });

  it('marks the rejection as authDead=true when refresh itself returns 401', async () => {
    seedTokens();
    mock.onGet('/v1/notifications').reply(401, { error: { code: 'unauthorized' } });
    refreshMock.onPost('/api/v1/auth/refresh').reply(401, { error: 'token expired' });

    let captured: unknown = null;
    try {
      await api.get('/v1/notifications');
    } catch (err) {
      captured = err;
    }

    expect(isAuthDeadError(captured)).toBe(true);
    // Genuine session death — tokens cleared, next page load lands on Login.
    expect(loadTokens()).toBeNull();
  });

  it('retries the original request transparently when refresh succeeds', async () => {
    seedTokens();
    // First call to /v1/notifications: 401. Second call (after refresh): 200.
    mock.onGet('/v1/notifications')
      .replyOnce(401, { error: { code: 'unauthorized' } })
      .onGet('/v1/notifications')
      .reply(200, { data: { items: [], total: 0 } });

    refreshMock.onPost('/api/v1/auth/refresh').reply(200, {
      data: {
        access_token: 'access-new',
        refresh_token: 'refresh-new',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
        refresh_expires_at: new Date(Date.now() + 600_000).toISOString(),
      },
    });

    const r = await api.get('/v1/notifications');
    expect(r.status).toBe(200);
    expect(loadTokens()?.accessToken).toBe('access-new');
  });
});

// Silence the console.error from the interceptor's expected rejection path.
beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {});
});
