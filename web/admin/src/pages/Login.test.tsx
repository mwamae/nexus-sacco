import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi } from 'vitest';

// Mock collaborators so we can render <Login /> in isolation. The real
// AuthProvider would fire fetchMe() on mount; useAuth() is stubbed
// instead so the only side-effect under test is the password submit.
const mockLogin = vi.fn();
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    login: mockLogin,
    completeMFA: vi.fn(),
  }),
}));

vi.mock('../auth/tenant', () => ({
  currentTenantSlug: () => 'tujenge',
  isPlatformHost: () => false,
}));

// Login.tsx imports extractError/passwordForgot from ../api/client. The
// forgot-password handler is not exercised here, but the module evaluates
// on import — keep it minimal so we don't pull axios into the test bundle.
vi.mock('../api/client', () => ({
  extractError: (_e: unknown, fallback: string) => fallback,
  passwordForgot: vi.fn().mockResolvedValue(undefined),
}));

import Login from './Login';

describe('Login — submit-button recovery on rejection', () => {
  it('re-enables the Sign in button after the login promise rejects', async () => {
    const user = userEvent.setup();

    // Simulate a timeout/5xx — no .response → message bucket is the
    // network/connection one. The exact bucket isn't what we're
    // asserting here; we only care that the button comes back.
    mockLogin.mockRejectedValueOnce(new Error('network timeout'));

    render(<Login />);

    const button = screen.getByRole('button', { name: 'Sign in' });
    expect(button).toBeEnabled();

    await user.type(screen.getByLabelText('Email'), 'someone@example.com');
    await user.type(screen.getByLabelText('Password'), 'wrong-password');
    await user.click(button);

    // After the rejection settles, the button must reappear in its
    // enabled "Sign in" state — not parked at "Signing in…".
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
    });

    // The email is preserved; only the password is cleared.
    expect(screen.getByLabelText('Email')).toHaveValue('someone@example.com');
    expect(screen.getByLabelText('Password')).toHaveValue('');
  });
});
