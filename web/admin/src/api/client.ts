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

export type BillingPlan = 'starter' | 'standard' | 'premium' | 'enterprise';
export type BranchKind = 'hq' | 'branch' | 'agency';
export type TenantStatus = 'active' | 'trial' | 'suspended' | 'expired' | 'pending_setup' | 'archived';

export type TenantRestrictions = {
  operations_frozen: boolean;
  users_locked: boolean;
  transactions_disabled: boolean;
};

export type ApiTenant = {
  id: string;
  slug: string;
  name: string;
  legal_name?: string;
  kind: string;
  status: TenantStatus;
  country_code: string;
  currency_code: string;
  license_no?: string;
  registration_no?: string;
  tax_pin?: string;
  billing_plan: BillingPlan;
  created_at: string;
  updated_at: string;
};

export type ApiTenantBranch = {
  id: string;
  tenant_id: string;
  code: string;
  name: string;
  kind: BranchKind;
  county?: string;
  sub_county?: string;
  physical_address?: string;
  phone?: string;
  position: number;
};

export type ApiTenantContact = {
  id: string;
  tenant_id: string;
  full_name: string;
  title?: string;
  email?: string;
  phone?: string;
  position: number;
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

export type BranchInput = {
  code: string;
  name: string;
  kind?: BranchKind;
  county?: string;
  sub_county?: string;
  physical_address?: string;
  phone?: string;
};

export type ContactInput = {
  full_name: string;
  title?: string;
  email?: string;
  phone?: string;
};

export type CreateTenantInput = {
  slug: string;
  name: string;
  legal_name?: string;
  kind?: string;
  country_code?: string;
  currency_code?: string;
  license_no?: string;
  registration_no?: string;
  tax_pin?: string;
  billing_plan?: BillingPlan;
  owner_email: string;
  owner_name: string;
  owner_phone?: string;
  owner_password: string;
  branches?: BranchInput[];
  contacts?: ContactInput[];
};

export async function createTenant(input: CreateTenantInput): Promise<{
  tenant: ApiTenant;
  owner: ApiUser;
  branches?: ApiTenantBranch[];
  contacts?: ApiTenantContact[];
}> {
  const r = await api.post('/v1/platform/tenants', input);
  return r.data.data;
}

export type ApiTenantDetail = ApiTenant & {
  restrictions: TenantRestrictions;
  branches: ApiTenantBranch[];
  contacts: ApiTenantContact[];
};

export async function getTenant(id: string): Promise<ApiTenantDetail> {
  const r = await api.get(`/v1/platform/tenants/${id}`);
  return r.data.data;
}

export async function setTenantStatus(id: string, status: Exclude<TenantStatus, 'archived'>): Promise<void> {
  await api.post(`/v1/platform/tenants/${id}/status`, { status });
}

export async function setTenantRestrictions(
  id: string,
  patch: Partial<TenantRestrictions>,
): Promise<void> {
  await api.post(`/v1/platform/tenants/${id}/restrictions`, patch);
}

export async function archiveTenant(id: string): Promise<void> {
  await api.post(`/v1/platform/tenants/${id}/archive`);
}

// Triggers a browser download by hitting the export/backup endpoint with
// the auth header, then handing the blob off to a synthetic <a download>.
async function downloadBundle(id: string, kind: 'export' | 'backup'): Promise<void> {
  const r = await api.get(`/v1/platform/tenants/${id}/${kind}`, { responseType: 'blob' });
  const blob = r.data as Blob;
  const url = URL.createObjectURL(blob);
  // Try to pull the filename out of Content-Disposition; fall back if absent.
  const cd = (r.headers as Record<string, string>)['content-disposition'] || '';
  const match = /filename="([^"]+)"/.exec(cd);
  const a = document.createElement('a');
  a.href = url;
  a.download = match ? match[1] : `${kind}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

export const exportTenant = (id: string) => downloadBundle(id, 'export');
export const backupTenant = (id: string) => downloadBundle(id, 'backup');

// ─── Tenant settings (tenant-side admin) ───

export type InterestMethod = 'flat' | 'reducing_balance' | 'declining_balance';
export type DividendFrequency = 'annual' | 'semi_annual' | 'quarterly';

export type TenantBranding = {
  tenant_id: string;
  has_logo: boolean;
  logo_mime?: string;
  logo_size_bytes?: number;
  logo_updated_at?: string;
  primary_color: string;
  accent_color: string;
  font_family: string;
  email_from_name?: string;
  sms_sender_id?: string;
  custom_domain?: string;
  updated_at: string;
};

export type TenantRegion = {
  tenant_id: string;
  timezone: string;
  language: string;
  date_format: string;
  regulator?: string;
  jurisdiction?: string;
  vat_rate: number;
  withholding_tax_rate: number;
  updated_at: string;
};

export type TenantOperations = {
  tenant_id: string;
  loan_min_amount: number;
  loan_max_amount: number;
  loan_max_term_months: number;
  default_interest_method: InterestMethod;
  default_interest_rate: number;
  savings_min_opening_bal: number;
  savings_min_running_bal: number;
  savings_withdrawal_fee: number;
  dividend_rate: number;
  dividend_frequency: DividendFrequency;
  penalty_late_fee_rate: number;
  penalty_grace_period_days: number;
  guarantor_min_count: number;
  guarantor_self_max_amount: number;
  approval_branch_limit: number;
  approval_credit_limit: number;
  approval_board_limit: number;
  updated_at: string;
};

export type TenantSettings = {
  tenant: ApiTenant;
  branding: TenantBranding;
  region: TenantRegion;
  operations: TenantOperations;
};

export async function getTenantSettings(): Promise<TenantSettings> {
  const r = await api.get('/v1/tenant/settings');
  return r.data.data;
}

export async function updateBranding(patch: Partial<{
  primary_color: string;
  accent_color: string;
  font_family: string;
  email_from_name: string;
  sms_sender_id: string;
  custom_domain: string;
}>): Promise<TenantBranding> {
  const r = await api.patch('/v1/tenant/branding', patch);
  return r.data.data;
}

export async function uploadLogo(file: Blob, filename?: string): Promise<TenantBranding> {
  const form = new FormData();
  form.append('file', file, filename ?? `logo.${(file.type || 'image/png').split('/')[1] ?? 'png'}`);
  const r = await api.post('/v1/tenant/branding/logo', form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export async function clearLogo(): Promise<void> {
  await api.delete('/v1/tenant/branding/logo');
}

// Loads the logo bytes with auth; returns a Blob the caller can turn into
// an object URL for <img src=...>. Returns null when no logo is set.
export async function fetchTenantLogo(): Promise<Blob | null> {
  try {
    const r = await api.get('/v1/tenant/branding/logo', { responseType: 'blob' });
    return r.data as Blob;
  } catch (e) {
    const status = (e as { response?: { status?: number } })?.response?.status;
    if (status === 404) return null;
    throw e;
  }
}

export async function updateRegion(patch: Partial<TenantRegion>): Promise<TenantRegion> {
  const r = await api.patch('/v1/tenant/region', patch);
  return r.data.data;
}

export async function updateOperations(patch: Partial<TenantOperations>): Promise<TenantOperations> {
  const r = await api.patch('/v1/tenant/operations', patch);
  return r.data.data;
}

// ─── Audit log lookup ───

export type AuditEntry = {
  id: number;
  tenant_id?: string;
  actor_id?: string;
  action: string;
  target_kind?: string;
  target_id?: string;
  ip?: string;
  user_agent?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
};

export async function listAuditForTarget(kind: string, id: string, limit = 100): Promise<AuditEntry[]> {
  const r = await api.get(`/v1/audit/by-target/${kind}/${id}`, { params: { limit } });
  return r.data.data?.entries ?? [];
}

export type ApiRoleSummary = {
  id: string;
  tenant_id?: string;
  code: string;
  name: string;
  description?: string;
  is_system: boolean;
};

export type ApiRole = ApiRoleSummary & {
  permissions: string[];
};

export type ApiUserWithRoles = ApiUser & {
  roles: ApiRoleSummary[];
};

export type ApiPermission = {
  code: string;
  description: string;
  category: string;
};

export async function listUsers(): Promise<{ users: ApiUserWithRoles[]; total: number }> {
  const r = await api.get('/v1/users');
  return r.data.data;
}

export async function getUser(id: string): Promise<{ user: ApiUser; roles: ApiRoleSummary[] }> {
  const r = await api.get(`/v1/users/${id}`);
  return r.data.data;
}

export type InviteUserInput = {
  email: string;
  full_name: string;
  phone?: string;
  role_codes: string[];
};

export async function inviteUser(input: InviteUserInput): Promise<ApiUser> {
  const r = await api.post('/v1/users/invite', input);
  return r.data.data;
}

export async function resendInvite(userId: string): Promise<void> {
  await api.post(`/v1/users/${userId}/invite/resend`);
}

export async function setUserStatus(userId: string, status: 'active' | 'suspended'): Promise<void> {
  await api.post(`/v1/users/${userId}/status`, { status });
}

export async function updateUser(userId: string, patch: { full_name?: string; phone?: string }): Promise<ApiUser> {
  const r = await api.patch(`/v1/users/${userId}`, patch);
  return r.data.data;
}

export async function assignUserRole(userId: string, roleCode: string): Promise<void> {
  await api.post(`/v1/users/${userId}/roles`, { role_code: roleCode });
}

export async function unassignUserRole(userId: string, roleId: string): Promise<void> {
  await api.delete(`/v1/users/${userId}/roles/${roleId}`);
}

export async function listRoles(): Promise<ApiRole[]> {
  const r = await api.get('/v1/roles');
  return r.data.data ?? [];
}

export async function getRole(id: string): Promise<ApiRole> {
  const r = await api.get(`/v1/roles/${id}`);
  return r.data.data;
}

export type CreateRoleInput = {
  code: string;
  name: string;
  description?: string;
  permissions: string[];
};

export async function createRole(input: CreateRoleInput): Promise<ApiRole> {
  const r = await api.post('/v1/roles', input);
  return r.data.data;
}

export async function updateRole(id: string, patch: { name?: string; description?: string; permissions?: string[] }): Promise<ApiRole> {
  const r = await api.patch(`/v1/roles/${id}`, patch);
  return r.data.data;
}

export async function deleteRole(id: string): Promise<void> {
  await api.delete(`/v1/roles/${id}`);
}

export async function listPermissions(): Promise<ApiPermission[]> {
  const r = await api.get('/v1/permissions');
  return r.data.data ?? [];
}

export async function inviteAccept(token: string, newPassword: string): Promise<void> {
  await api.post('/v1/auth/invite/accept', { token, new_password: newPassword });
}

// ─── Members ───
// /api/v1/members* is proxied to the member service by Vite.

export type MemberStatus = 'pending' | 'active' | 'suspended' | 'closed' | 'rejected';
export type IDDocKind = 'national_id' | 'passport' | 'alien_id';
export type Gender = 'male' | 'female' | 'other' | 'undisclosed';
export type RelationKind = 'next_of_kin' | 'beneficiary';
export type DocumentKind = 'signature' | 'passport_photo' | 'id_front' | 'id_back';

export type ApiMember = {
  id: string;
  tenant_id: string;
  member_no: string;
  status: MemberStatus;

  full_name: string;
  id_doc_kind: IDDocKind;
  id_doc_number: string;
  kra_pin?: string;
  gender: Gender;
  date_of_birth?: string;

  phone?: string;
  email?: string;
  county?: string;
  sub_county?: string;
  physical_address?: string;

  employment_status?: string;
  employer?: string;
  payroll_no?: string;
  job_title?: string;

  approved_at?: string;
  approved_by?: string;
  rejection_reason?: string;
  created_at: string;
  updated_at: string;
};

export type ApiRelation = {
  id: string;
  member_id: string;
  kind: RelationKind;
  full_name: string;
  relationship: string;
  phone?: string;
  email?: string;
  id_doc_number?: string;
  share_percent?: number;
  position: number;
};

export type ApiDocument = {
  id: string;
  member_id: string;
  kind: DocumentKind;
  mime: string;
  size_bytes: number;
  uploaded_at: string;
};

export type ApiMemberDetail = ApiMember & {
  next_of_kin: ApiRelation | null;
  beneficiaries: ApiRelation[];
  documents: ApiDocument[];
};

export type RelationInput = {
  kind?: RelationKind;
  full_name: string;
  relationship: string;
  phone?: string;
  email?: string;
  id_doc_number?: string;
  share_percent?: number;
};

export type CreateMemberInput = {
  full_name: string;
  id_doc_kind: IDDocKind;
  id_doc_number: string;
  kra_pin?: string;
  gender?: Gender;
  date_of_birth?: string;
  phone?: string;
  email?: string;
  county?: string;
  sub_county?: string;
  physical_address?: string;
  employment_status?: string;
  employer?: string;
  payroll_no?: string;
  job_title?: string;
  next_of_kin?: RelationInput | null;
  beneficiaries?: RelationInput[];
};

export type ListMembersParams = {
  status?: MemberStatus;
  q?: string;
  limit?: number;
  offset?: number;
};

export async function listMembers(p: ListMembersParams = {}): Promise<{ members: ApiMember[]; total: number; limit: number; offset: number }> {
  const r = await api.get('/v1/members', { params: p });
  return { ...r.data.data, members: r.data.data.members ?? [] };
}

export async function getMember(id: string): Promise<ApiMemberDetail> {
  const r = await api.get(`/v1/members/${id}`);
  return r.data.data;
}

export async function createMember(input: CreateMemberInput): Promise<ApiMember> {
  const r = await api.post('/v1/members', input);
  return r.data.data;
}

export async function approveMember(id: string): Promise<void> {
  await api.post(`/v1/members/${id}/approve`);
}

export async function rejectMember(id: string, reason: string): Promise<void> {
  await api.post(`/v1/members/${id}/reject`, { reason });
}

export async function setMemberStatus(id: string, status: 'active' | 'suspended' | 'closed'): Promise<void> {
  await api.post(`/v1/members/${id}/status`, { status });
}

export async function uploadMemberDocument(
  memberId: string,
  kind: DocumentKind,
  file: Blob,
  filename?: string,
): Promise<ApiDocument> {
  const form = new FormData();
  form.append('file', file, filename ?? `${kind}.${(file.type || 'image/png').split('/')[1] ?? 'bin'}`);
  const r = await api.post(`/v1/members/${memberId}/documents/${kind}`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export function memberDocumentURL(memberId: string, kind: DocumentKind): string {
  return `${apiBase}/v1/members/${memberId}/documents/${kind}`;
}

// fetchMemberDocument loads the raw bytes (with auth) and returns a Blob
// the caller can convert to an object URL for <img src>.
export async function fetchMemberDocument(memberId: string, kind: DocumentKind): Promise<Blob> {
  const r = await api.get(`/v1/members/${memberId}/documents/${kind}`, { responseType: 'blob' });
  return r.data as Blob;
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
