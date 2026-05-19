// Auth context — owns the current session (user, tenant, perms).
// Components consume via useAuth().

import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import {
  fetchMe,
  login as apiLogin,
  verifyMFA as apiVerifyMFA,
  logout as apiLogout,
  saveTokens,
  loadTokens,
  clearTokens,
  isMFARequired,
  type ApiTenant,
  type ApiUser,
  type MeResponse,
  type MFARequiredResponse,
} from '../api/client';

type AuthState = {
  status: 'loading' | 'anonymous' | 'authenticated';
  user: ApiUser | null;
  tenant: ApiTenant | null;
  roles: string[];
  permissions: string[];
};

type LoginOutcome =
  | { kind: 'authenticated' }
  | { kind: 'mfa_required'; mfa: MFARequiredResponse };

type AuthContextValue = AuthState & {
  login: (email: string, password: string) => Promise<LoginOutcome>;
  completeMFA: (mfaToken: string, code: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
  hasPermission: (perm: string) => boolean;
};

const initial: AuthState = {
  status: 'loading',
  user: null,
  tenant: null,
  roles: [],
  permissions: [],
};

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>(initial);

  const applyMe = useCallback((me: MeResponse) => {
    setState({
      status: 'authenticated',
      user: me.user,
      tenant: me.tenant ?? null,
      roles: me.roles ?? [],
      permissions: me.permissions ?? [],
    });
  }, []);

  const refresh = useCallback(async () => {
    if (!loadTokens()) {
      setState({ ...initial, status: 'anonymous' });
      return;
    }
    try {
      const me = await fetchMe();
      applyMe(me);
    } catch {
      clearTokens();
      setState({ ...initial, status: 'anonymous' });
    }
  }, [applyMe]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(async (email: string, password: string): Promise<LoginOutcome> => {
    const resp = await apiLogin(email, password);
    if (isMFARequired(resp)) {
      return { kind: 'mfa_required', mfa: resp };
    }
    saveTokens({
      accessToken: resp.access_token,
      refreshToken: resp.refresh_token,
      expiresAt: resp.expires_at,
      refreshExpiresAt: resp.refresh_expires_at,
    });
    const me = await fetchMe();
    applyMe(me);
    return { kind: 'authenticated' };
  }, [applyMe]);

  const completeMFA = useCallback(async (mfaToken: string, code: string) => {
    const resp = await apiVerifyMFA(mfaToken, code);
    saveTokens({
      accessToken: resp.access_token,
      refreshToken: resp.refresh_token,
      expiresAt: resp.expires_at,
      refreshExpiresAt: resp.refresh_expires_at,
    });
    const me = await fetchMe();
    applyMe(me);
  }, [applyMe]);

  const logout = useCallback(async () => {
    await apiLogout();
    setState({ ...initial, status: 'anonymous' });
  }, []);

  const hasPermission = useCallback(
    (perm: string) => state.user?.is_platform_admin === true || state.permissions.includes(perm),
    [state.permissions, state.user?.is_platform_admin],
  );

  return (
    <AuthContext.Provider value={{ ...state, login, completeMFA, logout, refresh, hasPermission }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
}
