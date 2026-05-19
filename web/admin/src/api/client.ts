// Axios client with auth + auto-refresh interceptor.
//
// Endpoints under /api/* are proxied by Vite to the identity service.
// The browser sends the request to its current host (tenant.nexussacco.local:5173)
// and Vite preserves Host so the backend sees the right subdomain for
// tenant resolution.

import axios, { AxiosError, type InternalAxiosRequestConfig } from 'axios';

export const apiBase = '/api';

type Tokens = {
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
  refreshExpiresAt: string;
};

const STORAGE_KEY = 'nx.tokens.v1';

export function loadTokens(): Tokens | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as Tokens) : null;
  } catch {
    return null;
  }
}

export function saveTokens(t: Tokens) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(t));
}

export function clearTokens() {
  localStorage.removeItem(STORAGE_KEY);
}

export const api = axios.create({ baseURL: apiBase, timeout: 15000 });

// Attach Authorization header from current tokens.
api.interceptors.request.use((cfg: InternalAxiosRequestConfig) => {
  const t = loadTokens();
  if (t?.accessToken) {
    cfg.headers = cfg.headers ?? {};
    cfg.headers.Authorization = `Bearer ${t.accessToken}`;
  }
  return cfg;
});

// Single in-flight refresh so a burst of 401s doesn't hammer /auth/refresh.
let refreshing: Promise<Tokens | null> | null = null;

async function refreshOnce(): Promise<Tokens | null> {
  if (refreshing) return refreshing;
  refreshing = (async () => {
    const t = loadTokens();
    if (!t?.refreshToken) return null;
    try {
      const resp = await axios.post(
        `${apiBase}/v1/auth/refresh`,
        { refresh_token: t.refreshToken },
        { headers: { 'Content-Type': 'application/json' } },
      );
      const next: Tokens = {
        accessToken: resp.data.data.access_token,
        refreshToken: resp.data.data.refresh_token,
        expiresAt: resp.data.data.expires_at,
        refreshExpiresAt: resp.data.data.refresh_expires_at,
      };
      saveTokens(next);
      return next;
    } catch {
      clearTokens();
      return null;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

api.interceptors.response.use(
  (r) => r,
  async (err: AxiosError) => {
    const cfg = err.config as (InternalAxiosRequestConfig & { _retried?: boolean }) | undefined;
    if (err.response?.status === 401 && cfg && !cfg._retried && !cfg.url?.includes('/auth/login')) {
      cfg._retried = true;
      const next = await refreshOnce();
      if (next) {
        cfg.headers = cfg.headers ?? {};
        cfg.headers.Authorization = `Bearer ${next.accessToken}`;
        return api.request(cfg);
      }
    }
    return Promise.reject(err);
  },
);

// ─── Typed endpoints ───

export type ApiUser = {
  id: string;
  tenant_id: string;
  email: string;
  phone?: string;
  full_name: string;
  status: 'pending' | 'active' | 'suspended' | 'locked' | 'closed';
  is_platform_admin: boolean;
  email_verified_at?: string;
  mfa_enabled: boolean;
  mfa_method?: string;
  last_login_at?: string;
  created_at: string;
  updated_at: string;
};

export type ApiTenant = {
  id: string;
  slug: string;
  name: string;
  legal_name?: string;
  kind: string;
  status: string;
  country_code: string;
  currency_code: string;
  license_no?: string;
};

export type LoginResponse = {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_at: string;
  refresh_expires_at: string;
  user: ApiUser;
  tenant?: ApiTenant;
};

export type MFARequiredResponse = {
  mfa_required: true;
  mfa_token: string;
  mfa_expires_at: string;
  method: string;
  delivery_hint: string;
};

export type LoginResult = LoginResponse | MFARequiredResponse;

export function isMFARequired(r: LoginResult): r is MFARequiredResponse {
  return (r as MFARequiredResponse).mfa_required === true;
}

export type MeResponse = {
  user: ApiUser;
  tenant?: ApiTenant;
  roles: string[];
  permissions: string[];
};

export async function login(email: string, password: string): Promise<LoginResult> {
  const r = await api.post('/v1/auth/login', { email, password });
  return r.data.data;
}

export async function verifyMFA(mfaToken: string, code: string): Promise<LoginResponse> {
  const r = await api.post('/v1/auth/mfa/verify', { mfa_token: mfaToken, code });
  return r.data.data;
}

export async function startMFAEnable(): Promise<MFARequiredResponse> {
  const r = await api.post('/v1/auth/mfa/email/enable');
  return r.data.data;
}

export async function confirmMFAEnable(mfaToken: string, code: string): Promise<{ mfa_enabled: true; method: string }> {
  const r = await api.post('/v1/auth/mfa/email/enable/confirm', { mfa_token: mfaToken, code });
  return r.data.data;
}

export async function disableMFA(password: string): Promise<{ mfa_enabled: false }> {
  const r = await api.post('/v1/auth/mfa/disable', { password });
  return r.data.data;
}

export async function passwordForgot(email: string): Promise<void> {
  await api.post('/v1/auth/password/forgot', { email });
}

export async function passwordReset(token: string, newPassword: string): Promise<void> {
  await api.post('/v1/auth/password/reset', { token, new_password: newPassword });
}

export async function passwordChange(currentPassword: string, newPassword: string): Promise<void> {
  await api.post('/v1/auth/password/change', {
    current_password: currentPassword,
    new_password: newPassword,
  });
}

export async function logout(): Promise<void> {
  const t = loadTokens();
  if (t?.refreshToken) {
    try {
      await api.post('/v1/auth/logout', { refresh_token: t.refreshToken });
    } catch {
      // best-effort
    }
  }
  clearTokens();
}

export async function fetchMe(): Promise<MeResponse> {
  const r = await api.get('/v1/auth/me');
  return r.data.data;
}

export async function listTenants(): Promise<ApiTenant[]> {
  const r = await api.get('/v1/platform/tenants');
  return r.data.data ?? [];
}

export type CreateTenantInput = {
  slug: string;
  name: string;
  legal_name?: string;
  kind?: string;
  country_code?: string;
  currency_code?: string;
  license_no?: string;
  owner_email: string;
  owner_name: string;
  owner_phone?: string;
  owner_password: string;
};

export async function createTenant(input: CreateTenantInput): Promise<{ tenant: ApiTenant; owner: ApiUser }> {
  const r = await api.post('/v1/platform/tenants', input);
  return r.data.data;
}

export async function listUsers(): Promise<{ users: ApiUser[]; total: number }> {
  const r = await api.get('/v1/users');
  return r.data.data;
}

export type ApiError = {
  code: string;
  message: string;
  details?: unknown;
};

export function extractError(e: unknown, fallback = 'Something went wrong'): string {
  const err = e as AxiosError<{ error?: ApiError }>;
  return err?.response?.data?.error?.message || err?.message || fallback;
}
