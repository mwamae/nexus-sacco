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

// ─── Organisations (non-individual members) ───

export type OrgKind =
  | 'group' | 'chama' | 'ltd' | 'sole_prop'
  | 'ngo' | 'church' | 'sacco' | 'cooperative' | 'school';

export type OrgStatus = 'pending' | 'active' | 'suspended' | 'closed' | 'rejected' | 'dormant';
export type RiskCategory = 'low' | 'medium' | 'high';
export type KYCReviewStatus = 'not_started' | 'in_review' | 'verified' | 'rejected';
export type SignatoryClass = 'mandatory' | 'optional' | 'alternate';
export type DocVerification = 'pending' | 'verified' | 'rejected';
export type ContactKind = 'primary' | 'finance' | 'hr_payroll' | 'compliance';

export type OrgDocKind =
  | 'registration_certificate' | 'cr12' | 'kra_pin_certificate'
  | 'memorandum_articles' | 'constitution_bylaws' | 'business_permit'
  | 'tax_compliance_certificate' | 'vat_certificate' | 'ngo_certificate'
  | 'cooperative_certificate' | 'proof_of_address' | 'audited_financials'
  | 'bank_statement' | 'board_resolution'
  | 'signatory_appointment_resolution' | 'beneficial_ownership_declaration';

export type OfficialPosition =
  | 'chairperson' | 'vice_chairperson' | 'treasurer' | 'secretary'
  | 'director' | 'trustee' | 'principal' | 'pastor' | 'other';

export type ApiOrg = {
  id: string;
  tenant_id: string;
  org_no: string;
  status: OrgStatus;
  registered_name: string;
  trading_name?: string;
  kind: OrgKind;
  registration_no?: string;
  date_of_registration?: string;
  date_of_operation?: string;
  industry?: string;
  nature_of_business?: string;
  member_count?: number;
  employee_count?: number;
  physical_address?: string;
  postal_address?: string;
  county?: string;
  sub_county?: string;
  ward?: string;
  gps_lat?: number;
  gps_lng?: number;
  branch_id?: string;
  risk_category: RiskCategory;
  kyc_status: KYCReviewStatus;
  blacklisted: boolean;
  blacklist_reason?: string;
  dormant_since?: string;
  approved_at?: string;
  approved_by?: string;
  rejection_reason?: string;
  created_at: string;
  updated_at: string;
};

export type ApiOrgDocument = {
  id: string;
  org_id: string;
  kind: OrgDocKind;
  mime: string;
  size_bytes: number;
  issue_date?: string;
  expiry_date?: string;
  verification: DocVerification;
  verified_by?: string;
  verified_at?: string;
  verification_note?: string;
  uploaded_at: string;
};

export type OfficialFile = { mime: string; size: number; updated_at: string };

export type ApiOfficial = {
  id: string;
  org_id: string;
  full_name: string;
  id_doc_kind: IDDocKind;
  id_doc_number: string;
  kra_pin?: string;
  date_of_birth?: string;
  gender: Gender;
  nationality?: string;
  phone?: string;
  email?: string;
  physical_address?: string;
  occupation?: string;
  position: OfficialPosition;
  position_label?: string;
  appointed_on?: string;
  is_pep: boolean;
  pep_note?: string;
  sanctions_screened_at?: string;
  sanctions_screened_by?: string;
  sanctions_hit: boolean;
  sanctions_note?: string;
  is_beneficial_owner: boolean;
  ownership_percent?: number;
  files: Record<string, OfficialFile>;
  position_order: number;
  created_at: string;
  updated_at: string;
};

export type ApiSignatory = {
  id: string;
  org_id: string;
  official_id: string;
  class: SignatoryClass;
  signing_order: number;
  txn_limit?: number;
  effective_from: string;
};

export type ApiMandate = {
  org_id: string;
  rules: Record<string, unknown>;
  updated_at: string;
};

export type ApiBanking = {
  org_id: string;
  bank_name?: string;
  bank_branch?: string;
  bank_code?: string;
  swift_code?: string;
  account_name?: string;
  account_number?: string;
  paybill?: string;
  till_number?: string;
  mobile_money_phones?: string;
  mobile_settlement_account?: string;
  preferred_disbursement?: string;
  preferred_repayment?: string;
  standing_order_details?: string;
  checkoff_arrangement?: string;
  updated_at?: string;
};

export type ApiOrgContact = {
  id: string;
  org_id: string;
  kind: ContactKind;
  full_name: string;
  role?: string;
  phone?: string;
  email?: string;
  position: number;
};

export type ApiOrgDetail = ApiOrg & {
  documents: ApiOrgDocument[];
  officials: ApiOfficial[];
  signatories: ApiSignatory[];
  mandate?: ApiMandate;
  banking?: ApiBanking;
  contacts: ApiOrgContact[];
};

export type OfficialInput = {
  full_name: string;
  id_doc_kind?: IDDocKind;
  id_doc_number: string;
  kra_pin?: string;
  date_of_birth?: string;
  gender?: Gender;
  nationality?: string;
  phone?: string;
  email?: string;
  physical_address?: string;
  occupation?: string;
  position?: OfficialPosition;
  position_label?: string;
  appointed_on?: string;
  is_pep?: boolean;
  pep_note?: string;
  is_beneficial_owner?: boolean;
  ownership_percent?: number;
  signatory?: { class?: SignatoryClass; signing_order?: number; txn_limit?: number | null };
};

export type OrgContactInput = {
  kind: ContactKind;
  full_name: string;
  role?: string;
  phone?: string;
  email?: string;
};

export type CreateOrgInput = {
  registered_name: string;
  trading_name?: string;
  kind: OrgKind;
  registration_no?: string;
  date_of_registration?: string;
  date_of_operation?: string;
  industry?: string;
  nature_of_business?: string;
  member_count?: number;
  employee_count?: number;
  physical_address?: string;
  postal_address?: string;
  county?: string;
  sub_county?: string;
  ward?: string;
  branch_id?: string;
  risk_category?: RiskCategory;
  officials?: OfficialInput[];
  banking?: Partial<ApiBanking>;
  contacts?: OrgContactInput[];
  mandate?: Record<string, unknown>;
};

export async function listOrgs(p: {
  status?: OrgStatus; kind?: OrgKind; q?: string; limit?: number; offset?: number;
} = {}): Promise<{ orgs: ApiOrg[]; total: number; limit: number; offset: number }> {
  const r = await api.get('/v1/orgs', { params: p });
  return { ...r.data.data, orgs: r.data.data.orgs ?? [] };
}

export async function getOrg(id: string): Promise<ApiOrgDetail> {
  const r = await api.get(`/v1/orgs/${id}`);
  return r.data.data;
}

export async function createOrg(input: CreateOrgInput): Promise<ApiOrg> {
  const r = await api.post('/v1/orgs', input);
  return r.data.data;
}

export async function approveOrg(id: string): Promise<void> {
  await api.post(`/v1/orgs/${id}/approve`);
}

export async function rejectOrg(id: string, reason: string): Promise<void> {
  await api.post(`/v1/orgs/${id}/reject`, { reason });
}

export async function setOrgStatus(id: string, status: 'active' | 'suspended' | 'closed' | 'dormant'): Promise<void> {
  await api.post(`/v1/orgs/${id}/status`, { status });
}

export async function setOrgKYCStatus(id: string, status: KYCReviewStatus): Promise<void> {
  await api.post(`/v1/orgs/${id}/kyc-status`, { status });
}

export async function uploadOrgDocument(
  orgId: string,
  kind: OrgDocKind,
  file: Blob,
  opts: { issue_date?: string; expiry_date?: string } = {},
): Promise<ApiOrgDocument> {
  const form = new FormData();
  form.append('file', file, (file as File).name ?? `${kind}.${(file.type || 'application/pdf').split('/')[1] ?? 'bin'}`);
  const params: Record<string, string> = {};
  if (opts.issue_date) params.issue_date = opts.issue_date;
  if (opts.expiry_date) params.expiry_date = opts.expiry_date;
  const r = await api.post(`/v1/orgs/${orgId}/documents/${kind}`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
    params,
  });
  return r.data.data;
}

export async function fetchOrgDocument(orgId: string, kind: OrgDocKind): Promise<Blob> {
  const r = await api.get(`/v1/orgs/${orgId}/documents/${kind}`, { responseType: 'blob' });
  return r.data as Blob;
}

export async function verifyOrgDocument(orgId: string, kind: OrgDocKind, status: DocVerification, note?: string): Promise<void> {
  await api.post(`/v1/orgs/${orgId}/documents/${kind}/verify`, { status, note: note ?? '' });
}

export async function addOrgOfficial(orgId: string, input: OfficialInput): Promise<ApiOfficial> {
  const r = await api.post(`/v1/orgs/${orgId}/officials`, input);
  return r.data.data;
}

export async function removeOrgOfficial(orgId: string, officialId: string): Promise<void> {
  await api.delete(`/v1/orgs/${orgId}/officials/${officialId}`);
}

export async function screenOfficial(orgId: string, officialId: string, hit: boolean, note?: string): Promise<void> {
  await api.post(`/v1/orgs/${orgId}/officials/${officialId}/sanctions`, { hit, note: note ?? '' });
}

export async function uploadOfficialFile(
  orgId: string, officialId: string,
  kind: 'passport_photo' | 'signature' | 'id_copy' | 'kra_pin_certificate',
  file: Blob,
): Promise<ApiOfficial> {
  const form = new FormData();
  form.append('file', file, (file as File).name ?? `${kind}.png`);
  const r = await api.post(`/v1/orgs/${orgId}/officials/${officialId}/files/${kind}`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export async function fetchOfficialFile(
  orgId: string, officialId: string, kind: string,
): Promise<Blob> {
  const r = await api.get(`/v1/orgs/${orgId}/officials/${officialId}/files/${kind}`, { responseType: 'blob' });
  return r.data as Blob;
}

export async function replaceOrgSignatories(orgId: string, signatories: {
  official_id: string; class: SignatoryClass; signing_order?: number; txn_limit?: number | null;
}[]): Promise<void> {
  await api.post(`/v1/orgs/${orgId}/signatories`, { signatories });
}

export async function setOrgMandate(orgId: string, rules: Record<string, unknown>): Promise<void> {
  await api.post(`/v1/orgs/${orgId}/mandate`, { rules });
}

export async function upsertOrgBanking(orgId: string, banking: Partial<ApiBanking>): Promise<ApiBanking> {
  const r = await api.post(`/v1/orgs/${orgId}/banking`, banking);
  return r.data.data;
}

export async function replaceOrgContacts(orgId: string, contacts: OrgContactInput[]): Promise<void> {
  await api.post(`/v1/orgs/${orgId}/contacts`, { contacts });
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

export type MemberStatus =
  | 'pending' | 'active' | 'dormant' | 'suspended'
  | 'blacklisted' | 'exited' | 'deceased' | 'rejected';

export type MemberStatusReason =
  | 'onboarding_approval' | 'onboarding_rejection'
  | 'dormancy_inactivity' | 'reactivation_request'
  | 'loan_default' | 'compliance_hold' | 'disciplinary_action'
  | 'fraud_investigation' | 'regulatory_directive'
  | 'member_request' | 'admin_action'
  | 'deceased_notification' | 'system_correction' | 'other';
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

// ─── Member status lifecycle (8-status model) ───

export type StatusTransition = {
  From: MemberStatus;
  To: MemberStatus;
  Sensitive: boolean;
  Note: string;
};

export type AllowedAction = { action: string; allowed: boolean };

export type MemberStatusActions = {
  current: MemberStatus;
  system_behavior: string;
  visibility: 'normal' | 'restricted' | 'archive';
  transitions: StatusTransition[];
  open_proposals: MemberStatusProposal[];
  allowed_actions: AllowedAction[];
};

export type MemberStatusChange = {
  id: string;
  member_id: string;
  from_status?: MemberStatus;
  to_status: MemberStatus;
  reason_category: MemberStatusReason;
  reason_note?: string;
  has_supporting_doc: boolean;
  supporting_doc_mime?: string;
  changed_by?: string;
  changed_at: string;
  workflow_instance_id?: string;
  review_date?: string;
};

export type MemberStatusProposal = {
  id: string;
  member_id: string;
  workflow_instance_id: string;
  proposed_status: MemberStatus;
  reason_category: MemberStatusReason;
  reason_note?: string;
  has_supporting_doc: boolean;
  review_date?: string;
  proposed_by?: string;
  proposed_at: string;
  resolved_at?: string;
  resolution?: string;
};

export type StatusChangeResponse = {
  mode: 'applied' | 'proposed';
  member?: ApiMember;
  status_change?: MemberStatusChange;
  proposal?: MemberStatusProposal;
  workflow_instance_id?: string;
};

export type DormancyCandidate = {
  member_id: string;
  member_no: string;
  full_name: string;
  last_activity_at?: string;
  days_inactive: number;
};

export type RecentStatusChange = MemberStatusChange & {
  member_no: string;
  full_name: string;
};

export type MemberStatusSummary = {
  by_status: Partial<Record<MemberStatus, number>>;
  dormancy_pipeline: DormancyCandidate[];
  recent_changes: RecentStatusChange[];
  dormancy_threshold_days: number;
};

export async function getMemberStatusActions(memberId: string): Promise<MemberStatusActions> {
  const r = await api.get(`/v1/members/${memberId}/status-actions`);
  return r.data.data;
}

export async function listMemberStatusHistory(memberId: string): Promise<MemberStatusChange[]> {
  const r = await api.get(`/v1/members/${memberId}/status-history`);
  return r.data.data ?? [];
}

export async function changeMemberStatus(memberId: string, input: {
  target_status: MemberStatus;
  reason_category: MemberStatusReason;
  reason_note?: string;
  review_date?: string;
  supporting_doc_path?: string;
  supporting_doc_mime?: string;
}): Promise<StatusChangeResponse> {
  const r = await api.post(`/v1/members/${memberId}/status-change`, input);
  return r.data.data;
}

export async function uploadStatusSupportingDoc(memberId: string, file: Blob): Promise<{ storage_path: string; mime: string; size_bytes: number }> {
  const form = new FormData();
  form.append('file', file, (file as File).name ?? `support.${(file.type || 'application/pdf').split('/')[1] ?? 'bin'}`);
  const r = await api.post(`/v1/members/${memberId}/status-supporting-doc`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export async function getMemberStatusSummary(): Promise<MemberStatusSummary> {
  const r = await api.get('/v1/members/status/summary');
  return r.data.data;
}

export async function previewDormancyRun(): Promise<{ threshold_days: number; candidates: DormancyCandidate[] }> {
  const r = await api.post('/v1/members/dormancy/preview');
  return r.data.data;
}

export async function runDormancy(): Promise<{ threshold_days: number; candidates: DormancyCandidate[]; applied?: MemberStatusChange[] }> {
  const r = await api.post('/v1/members/dormancy/run');
  return r.data.data;
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

// ─── Workflow engine ───

export type WFStatus =
  | 'pending' | 'in_progress' | 'approved' | 'rejected'
  | 'returned' | 'awaiting_info' | 'escalated' | 'cancelled' | 'expired';

export type WFLevelStatus =
  | 'waiting' | 'in_progress' | 'approved' | 'rejected'
  | 'returned' | 'awaiting_info' | 'escalated' | 'skipped';

export type WFActionKind =
  | 'create' | 'approve' | 'reject' | 'return' | 'request_info'
  | 'resume' | 'escalate' | 'reassign' | 'cancel'
  | 'callback_fired' | 'sla_breached';

export type WFQuorum = 'any_one' | 'all';

export type WFLevelDef = {
  id?: string;
  level_order: number;
  name: string;
  approver_roles: string[];
  approver_user_ids: string[];
  quorum: WFQuorum;
  condition_expr?: unknown;
  sla_hours?: number;
  escalation_role?: string;
  escalation_user_id?: string;
};

export type WFDefinition = {
  id: string;
  tenant_id: string;
  process_kind: string;
  name: string;
  description?: string;
  version: number;
  active: boolean;
  created_at: string;
  updated_at: string;
  created_by?: string;
  levels: WFLevelDef[];
};

export type WFLevelState = {
  order: number;
  name: string;
  status: WFLevelStatus;
  approver_roles: string[];
  approver_user_ids?: string[];
  quorum: WFQuorum;
  condition?: unknown;
  sla_hours?: number;
  sla_due_at?: string;
  approved_by?: string[];
  entered_at?: string;
  completed_at?: string;
  escalation_role?: string;
  escalation_user_id?: string;
};

export type WFInstance = {
  id: string;
  tenant_id: string;
  definition_id: string;
  process_kind: string;
  subject_kind: string;
  subject_id: string;
  status: WFStatus;
  current_level: number;
  context: Record<string, unknown>;
  callback_url?: string;
  callback_status?: string;
  callback_delivered_at?: string;
  initiator_id?: string;
  started_at: string;
  completed_at?: string;
  levels: WFLevelState[];
};

export type WFAction = {
  id: string;
  instance_id: string;
  level_order?: number;
  action: WFActionKind;
  actor_id?: string;
  actor_role?: string;
  comments?: string;
  metadata?: unknown;
  created_at: string;
};

export type WFInstanceDetail = WFInstance & { actions: WFAction[] };

export type WFDashboard = {
  total: number;
  by_status: Partial<Record<WFStatus, number>>;
  by_process_kind: Record<string, number>;
  sla_breach_count: number;
  avg_tat_seconds: number;
};

export async function listWorkflowDefinitions(p: { process_kind?: string; only_active?: boolean } = {}): Promise<WFDefinition[]> {
  const r = await api.get('/v1/workflows', { params: { process_kind: p.process_kind, only_active: p.only_active ? 1 : undefined } });
  return r.data.data ?? [];
}

export async function getWorkflowDefinition(id: string): Promise<WFDefinition> {
  const r = await api.get(`/v1/workflows/${id}`);
  return r.data.data;
}

export type CreateWFDefinitionInput = {
  process_kind: string;
  name: string;
  description?: string;
  active?: boolean;
  levels: Omit<WFLevelDef, 'id' | 'level_order'>[];
};

export async function createWorkflowDefinition(input: CreateWFDefinitionInput): Promise<WFDefinition> {
  const r = await api.post('/v1/workflows', input);
  return r.data.data;
}

export async function setWorkflowActivation(id: string, active: boolean): Promise<void> {
  await api.post(`/v1/workflows/${id}/activation`, { active });
}

export async function getWorkflowDashboard(): Promise<WFDashboard> {
  const r = await api.get('/v1/workflows/dashboard');
  return r.data.data;
}

export async function listWorkflowInstances(p: {
  status?: WFStatus; process_kind?: string; subject_kind?: string; subject_id?: string; limit?: number; offset?: number;
} = {}): Promise<{ instances: WFInstance[]; total: number }> {
  const r = await api.get('/v1/workflow-instances', { params: p });
  return { ...r.data.data, instances: r.data.data.instances ?? [] };
}

export async function getWorkflowInstance(id: string): Promise<WFInstanceDetail> {
  const r = await api.get(`/v1/workflow-instances/${id}`);
  return r.data.data;
}

export type WFActionRequest = {
  action: 'approve' | 'reject' | 'return' | 'request_info' | 'resume' | 'escalate' | 'reassign' | 'cancel';
  comments?: string;
  reassign_to?: string;
  acting_as_role?: string;
};

export async function actOnInstance(id: string, req: WFActionRequest): Promise<WFInstance> {
  const r = await api.post(`/v1/workflow-instances/${id}/actions`, req);
  return r.data.data;
}

export async function createWorkflowInstance(input: {
  process_kind: string;
  definition_id?: string;
  subject_kind: string;
  subject_id: string;
  context?: Record<string, unknown>;
  callback_url?: string;
}): Promise<WFInstance> {
  const r = await api.post('/v1/workflow-instances', input);
  return r.data.data;
}

// ═══════════════════════════════════════════════════════════════════
// Shares sub-module (services/savings)
// ═══════════════════════════════════════════════════════════════════

export type ShareTxnType =
  | 'purchase' | 'transfer_in' | 'transfer_out'
  | 'redemption' | 'adjustment' | 'bonus_issue';

export type SharePaymentChannel =
  | 'cash' | 'mpesa' | 'airtel_money' | 'bank_transfer'
  | 'payroll' | 'standing_order' | 'internal';

export type ShareAccountStatus = 'active' | 'closed';

export type ShareAccount = {
  id: string;
  tenant_id: string;
  member_id: string;
  account_no: string;
  status: ShareAccountStatus;
  shares_held: number;
  shares_pledged: number;
  shares_available: number;
  par_value_at_open: string;
  total_value: string;
  first_purchase_at?: string;
  closed_at?: string;
  created_at: string;
  updated_at: string;
};

export type ShareTransaction = {
  id: string;
  account_id: string;
  member_id: string;
  txn_no: string;
  txn_type: ShareTxnType;
  shares_delta: number;
  par_value_at_txn: string;
  amount: string;
  payment_channel?: SharePaymentChannel;
  payment_ref?: string;
  narration?: string;
  counterparty_account_id?: string;
  counterparty_txn_id?: string;
  balance_after_shares: number;
  balance_after_amount: string;
  initiated_by: string;
  authorized_by?: string;
  authorization_reason?: string;
  posted_at: string;
  created_at: string;
};

export type ShareLien = {
  id: string;
  account_id: string;
  shares_pledged: number;
  reason: string;
  reference_kind?: string;
  reference_id?: string;
  status: 'active' | 'released';
  placed_by: string;
  placed_at: string;
  released_by?: string;
  released_at?: string;
  released_reason?: string;
};

export type ShareCertificate = {
  id: string;
  account_id: string;
  member_id: string;
  certificate_no: string;
  shares_covered: number;
  par_value_at_issue: string;
  total_value: string;
  issued_at: string;
  retired_at?: string;
  supersedes_id?: string;
  issued_by: string;
};

export type SharePolicy = {
  par_value: string;
  min_shares_required: number;
  max_shares_pct_of_capital: string;
  certificate_prefix: string;
};

export type ShareAccountView = {
  account: ShareAccount;
  member: { ID: string; MemberNo: string; FullName: string; Status: string };
  active_liens: ShareLien[];
  current_certificate?: ShareCertificate;
  policy: SharePolicy;
};

export type ShareSummary = {
  total_accounts: number;
  active_accounts: number;
  total_shares_issued: number;
  total_share_capital: string;
  members_below_minimum: number;
  accounts_with_lien: number;
  total_pledged_shares: number;
  par_value: string;
  min_shares_required: number;
};

export type ShareAccountListItem = {
  account: ShareAccount;
  member_no: string;
  full_name: string;
  member_status: string;
};

export type ShareTxnResponse = {
  transaction: ShareTransaction;
  account: ShareAccount;
  certificate?: ShareCertificate;
};

export type ShareTransferResponse = {
  from: ShareTxnResponse;
  to: ShareTxnResponse;
};

export async function getSharePolicy(): Promise<SharePolicy> {
  const r = await api.get('/v1/share-policy');
  return r.data.data;
}

export async function updateSharePolicy(p: SharePolicy): Promise<SharePolicy> {
  const r = await api.put('/v1/share-policy', p);
  return r.data.data;
}

export async function getShareAccountByMember(memberId: string): Promise<ShareAccountView> {
  // Trailing slash matches chi.Route("/share-accounts/by-member/{member_id}").Get("/", ...)
  const r = await api.get(`/v1/share-accounts/by-member/${memberId}/`);
  return r.data.data;
}

export async function listShareTransactions(memberId: string, opts: { limit?: number; offset?: number } = {}): Promise<ShareTransaction[]> {
  const q = new URLSearchParams();
  if (opts.limit) q.set('limit', String(opts.limit));
  if (opts.offset) q.set('offset', String(opts.offset));
  const r = await api.get(`/v1/share-accounts/by-member/${memberId}/transactions${q.toString() ? '?' + q.toString() : ''}`);
  return r.data.data ?? [];
}

export async function getCurrentCertificate(memberId: string): Promise<ShareCertificate | null> {
  try {
    const r = await api.get(`/v1/share-accounts/by-member/${memberId}/certificate`);
    return r.data.data;
  } catch (e: unknown) {
    if (axiosErrStatus(e) === 404) return null;
    throw e;
  }
}

export async function purchaseShares(memberId: string, input: {
  shares: number;
  payment_channel: SharePaymentChannel;
  payment_ref?: string;
  narration?: string;
}): Promise<ShareTxnResponse> {
  const r = await api.post(`/v1/share-accounts/by-member/${memberId}/purchase`, input);
  return r.data.data;
}

export async function transferShares(memberId: string, input: {
  shares: number;
  to_member_id: string;
  reason: string;
  narration?: string;
}): Promise<ShareTransferResponse> {
  const r = await api.post(`/v1/share-accounts/by-member/${memberId}/transfer`, input);
  return r.data.data;
}

export async function redeemShares(memberId: string, input: {
  shares: number;
  reason: string;
  payment_channel?: SharePaymentChannel;
  payment_ref?: string;
  narration?: string;
  acknowledge_below_minimum?: boolean;
}): Promise<ShareTxnResponse> {
  const r = await api.post(`/v1/share-accounts/by-member/${memberId}/redeem`, input);
  return r.data.data;
}

export async function adjustShares(memberId: string, input: {
  shares_delta: number;
  reason: string;
}): Promise<ShareTxnResponse> {
  const r = await api.post(`/v1/share-accounts/by-member/${memberId}/adjust`, input);
  return r.data.data;
}

export async function placeShareLien(memberId: string, input: {
  shares: number;
  reason: string;
  reference_kind?: string;
  reference_id?: string;
}): Promise<ShareLien> {
  const r = await api.post(`/v1/share-accounts/by-member/${memberId}/lien`, input);
  return r.data.data;
}

export async function releaseShareLien(lienId: string, reason: string): Promise<ShareLien> {
  const r = await api.post(`/v1/share-liens/${lienId}/release`, { reason });
  return r.data.data;
}

export async function listShareAccounts(opts: { q?: string; status?: string; below_min?: boolean; limit?: number; offset?: number } = {}): Promise<{ items: ShareAccountListItem[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.q) p.set('q', opts.q);
  if (opts.status) p.set('status', opts.status);
  if (opts.below_min) p.set('below_min', '1');
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get(`/v1/share-accounts${p.toString() ? '?' + p.toString() : ''}`);
  return r.data.data;
}

export async function getShareSummary(): Promise<ShareSummary> {
  const r = await api.get('/v1/share-accounts/summary');
  return r.data.data;
}

export async function bonusShareIssue(input: { pct_of_holding: string; reason: string }): Promise<{ issued_to_count: number; total_bonus_shares: number; pct_applied: string }> {
  const r = await api.post('/v1/share-accounts/bonus-issue', input);
  return r.data.data;
}

function axiosErrStatus(e: unknown): number | undefined {
  if (typeof e === 'object' && e && 'response' in e) {
    const resp = (e as { response?: { status?: number } }).response;
    return resp?.status;
  }
  return undefined;
}
