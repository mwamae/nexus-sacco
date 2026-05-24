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

// Distinguishes the two refresh-failure modes the response interceptor cares
// about. authFailed === true means the refresh endpoint specifically rejected
// our refresh token (401/403) — the session really is dead, tokens have been
// cleared. authFailed === false means refresh couldn't complete for a
// transient reason (network, 5xx, timeout); the existing tokens are
// preserved so a later attempt can still succeed.
type RefreshFailure = { kind: 'failed'; authFailed: boolean };
type RefreshOutcome = Tokens | RefreshFailure;

function isAuthDead(o: RefreshOutcome): boolean {
  return (o as RefreshFailure).kind === 'failed' && (o as RefreshFailure).authFailed;
}

function isFailure(o: RefreshOutcome): o is RefreshFailure {
  return (o as RefreshFailure).kind === 'failed';
}

let refreshingOutcome: Promise<RefreshOutcome> | null = null;

export async function refreshOnce(): Promise<RefreshOutcome> {
  if (refreshingOutcome) return refreshingOutcome;
  refreshingOutcome = (async (): Promise<RefreshOutcome> => {
    const t = loadTokens();
    if (!t?.refreshToken) {
      return { kind: 'failed', authFailed: true };
    }
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
    } catch (err) {
      // Only treat the session as dead if the refresh endpoint itself
      // rejected our refresh token. Network / 5xx / timeout leaves the
      // stored tokens alone so the next page navigation can retry.
      const status = (err as AxiosError)?.response?.status;
      const authFailed = status === 401 || status === 403;
      if (authFailed) {
        clearTokens();
      }
      return { kind: 'failed', authFailed };
    } finally {
      // Release the in-flight slot in a microtask, after callers awaiting
      // the current promise have resolved. We set it to null synchronously
      // on the next tick so a subsequent 401 starts a fresh refresh.
      queueMicrotask(() => { refreshingOutcome = null; });
    }
  })();
  return refreshingOutcome;
}

api.interceptors.response.use(
  (r) => r,
  async (err: AxiosError) => {
    const cfg = err.config as (InternalAxiosRequestConfig & { _retried?: boolean }) | undefined;
    if (err.response?.status === 401 && cfg && !cfg._retried && !cfg.url?.includes('/auth/login')) {
      cfg._retried = true;
      const outcome = await refreshOnce();
      if (!isFailure(outcome)) {
        cfg.headers = cfg.headers ?? {};
        cfg.headers.Authorization = `Bearer ${outcome.accessToken}`;
        return api.request(cfg);
      }
      // Tag the rejection so callers (AuthContext, error boundaries) can
      // tell "session is genuinely dead" apart from "this one endpoint
      // returned 401 / refresh was temporarily unavailable". Without this
      // signal a transient 401 on any endpoint used to silently nuke the
      // session via clearTokens().
      (err as AxiosError & { authDead?: boolean }).authDead = isAuthDead(outcome);
    }
    return Promise.reject(err);
  },
);

// Public helper for callers that need the same auth-dead signal the
// interceptor attaches (e.g. AuthContext deciding whether to drop to
// anonymous state on mount-time fetch failure).
export function isAuthDeadError(err: unknown): boolean {
  return (err as { authDead?: boolean })?.authDead === true;
}

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

// FeatureFlags + the feature_flags field on MeResponse were removed
// in the Phase D drop alongside the unified_counterparties flag —
// the unified register is now the only path. If a future flag is
// needed, reintroduce the bag here.
export type MeResponse = {
  user: ApiUser;
  tenant?: ApiTenant;
  roles: string[];
  permissions: string[];
};

export async function login(email: string, password: string): Promise<LoginResult> {
  // Explicit per-call timeout so a wedged backend can never park the
  // submit button at "Signing in…" indefinitely — even if the shared
  // instance default is later changed.
  const r = await api.post('/v1/auth/login', { email, password }, { timeout: 12000 });
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
  // Optional now — when omitted the owner is created in Pending state
  // and receives an invitation email to set their own password.
  owner_password?: string;
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

// ─────────── Tenant contacts (platform-admin CRUD) ───────────

export type TenantContactInput = {
  full_name: string;
  title?: string;
  email?: string;
  phone?: string;
  // When true, also provision a tenant-side user with role_codes.
  // The contact email becomes the user's login; an invitation email
  // is sent for them to set their own password.
  provision_as_user?: boolean;
  role_codes?: string[];
};

export type AddContactResponse = {
  contact: ApiTenantContact;
  user: ApiUser | null;
};

export async function addTenantContact(tenantId: string, input: TenantContactInput): Promise<AddContactResponse> {
  const r = await api.post(`/v1/platform/tenants/${tenantId}/contacts`, input);
  return r.data.data;
}
export async function updateTenantContact(tenantId: string, contactId: string, input: TenantContactInput): Promise<ApiTenantContact> {
  const r = await api.patch(`/v1/platform/tenants/${tenantId}/contacts/${contactId}`, input);
  return r.data.data;
}
export async function deleteTenantContact(tenantId: string, contactId: string): Promise<void> {
  await api.delete(`/v1/platform/tenants/${tenantId}/contacts/${contactId}`);
}

// ─────────── Tenant users (platform-admin: list + invite to another tenant) ───────────

export type TenantUserRow = {
  user: ApiUser;
  roles: Array<{ id: string; code: string; name: string }>;
};

export async function listTenantUsers(tenantId: string): Promise<{ users: TenantUserRow[]; total: number }> {
  const r = await api.get(`/v1/platform/tenants/${tenantId}/users`);
  return r.data.data;
}

export async function inviteUserToTenant(tenantId: string, input: {
  email: string;
  full_name: string;
  phone?: string;
  role_codes: string[];
}): Promise<{ user: ApiUser; invite_expires: string }> {
  const r = await api.post(`/v1/platform/tenants/${tenantId}/users/invite`, input);
  return r.data.data;
}

// ─────────── Platform-side per-user actions ───────────

export async function resendTenantUserInvite(tenantId: string, userId: string): Promise<{ user: ApiUser; invite_expires: string }> {
  const r = await api.post(`/v1/platform/tenants/${tenantId}/users/${userId}/invite/resend`);
  return r.data.data;
}
export async function suspendTenantUser(tenantId: string, userId: string, reason: string): Promise<void> {
  await api.post(`/v1/platform/tenants/${tenantId}/users/${userId}/suspend`, { reason });
}
export async function reactivateTenantUser(tenantId: string, userId: string): Promise<void> {
  await api.post(`/v1/platform/tenants/${tenantId}/users/${userId}/reactivate`);
}
export async function forceTenantUserPasswordReset(tenantId: string, userId: string): Promise<void> {
  await api.post(`/v1/platform/tenants/${tenantId}/users/${userId}/password-reset`);
}
export async function revokeTenantUser(tenantId: string, userId: string, reason: string): Promise<void> {
  await api.post(`/v1/platform/tenants/${tenantId}/users/${userId}/revoke`, { reason });
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

export type TenantMembership = {
  tenant_id: string;
  collect_registration_fee: boolean;
  registration_fee_individual: number;
  registration_fee_institutional: number;
  accepted_payment_channels: string[];
  fee_refundable_on_rejection: boolean;
  default_deposit_product_id?: string | null;
  updated_at: string;
};

export type TenantSettings = {
  tenant: ApiTenant;
  branding: TenantBranding;
  region: TenantRegion;
  operations: TenantOperations;
  membership: TenantMembership;
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
  // The backend PATCH only accepts editable fields. Pages typically
  // pass the full object including tenant_id + updated_at — strip
  // those server-managed fields here so the JSON decoder doesn't
  // reject the request as "unknown field".
  const { tenant_id: _t, updated_at: _u, ...body } = patch;
  void _t; void _u;
  const r = await api.patch('/v1/tenant/region', body);
  return r.data.data;
}

export async function updateOperations(patch: Partial<TenantOperations>): Promise<TenantOperations> {
  // Same defensive strip as updateRegion — keep server-managed fields
  // out of the wire payload.
  const { tenant_id: _t, updated_at: _u, ...body } = patch;
  void _t; void _u;
  const r = await api.patch('/v1/tenant/operations', body);
  return r.data.data;
}

export async function updateMembership(patch: Partial<TenantMembership>): Promise<TenantMembership> {
  // Strip server-managed fields — the backend rejects unknown JSON keys.
  const { tenant_id: _t, updated_at: _u, ...body } = patch;
  void _t; void _u;
  const r = await api.patch('/v1/tenant/membership', body);
  return r.data.data;
}

// ─────────── Membership applications (unified pipeline) ───────────

export type ApplicationKind = 'individual' | 'institutional';
export type ApplicationStatus =
  | 'submitted'
  | 'under_review'
  | 'returned_for_correction'
  | 'reviewed_pending_approval'
  | 'approved_active'
  | 'declined'
  | 'withdrawn';

export type ApplicantPayload = {
  date_of_birth?: string;
  gender?: string;
  nationality?: string;
  id_doc_kind?: string;
  id_doc_number?: string;
  kra_pin?: string;
  occupation?: string;
  employer?: string;
  monthly_income?: string;
  next_of_kin_name?: string;
  next_of_kin_relation?: string;
  next_of_kin_phone?: string;
  next_of_kin_id_number?: string;

  registered_name?: string;
  trading_name?: string;
  registration_number?: string;
  date_of_registration?: string;
  industry?: string;
  nature_of_business?: string;
  board_resolution_ref?: string;
  beneficial_owners?: string;

  physical_address?: string;
  postal_address?: string;
  county?: string;
  sub_county?: string;
  ward?: string;

  notes?: string;
};

export type MembershipApplication = {
  id: string;
  tenant_id: string;
  application_no: string;
  kind: ApplicationKind;
  status: ApplicationStatus;
  applicant_name: string;
  entity_type?: string | null;
  primary_phone?: string | null;
  primary_email?: string | null;
  branch_id?: string | null;
  applicant_payload: ApplicantPayload;

  fee_required: boolean;
  fee_amount_due: string;
  fee_amount_paid: string;
  fee_payment_channel?: string | null;
  fee_payment_reference?: string | null;
  fee_payment_date?: string | null;
  fee_proof_doc_path?: string | null;
  fee_shortfall_note?: string | null;
  fee_status: 'not_required' | 'paid' | 'shortfall' | 'not_paid' | 'refund_pending' | 'refunded';

  submitted_at: string;
  submitted_by: string;

  reviewer_user_id?: string | null;
  review_started_at?: string | null;
  review_completed_at?: string | null;
  review_summary_note?: string | null;

  approver_user_id?: string | null;
  approved_at?: string | null;
  decline_reason?: string | null;
  approval_conditions?: string | null;
  workflow_instance_id?: string | null;

  withdrawn_at?: string | null;
  withdrawn_by?: string | null;
  withdraw_reason?: string | null;

  // Activation linkage (Phase D, simplified in Phase E C — drops the
  // legacy materialized_member_id / materialized_org_id pair in favour
  // of the canonical counterparty bridge).
  materialized_counterparty_id?: string | null;
  materialized_at?: string | null;
  fee_journal_entry_id?: string | null;
  fee_refund_journal_entry_id?: string | null;

  created_at: string;
  updated_at: string;
  days_in_queue: number;
};

export type ActivationResult = {
  MemberID: string;
  MemberNo: string;
  ShareAccountID: string;
  ShareAccountNo: string;
  DepositAccountID?: string | null;
  DepositAccountNo?: string | null;
};

export type ChecklistItem = {
  id: string;
  kind: ApplicationKind;
  code: string;
  label: string;
  description?: string | null;
  mandatory: boolean;
  display_order: number;
  is_active: boolean;
};

export type ChecklistResponse = {
  id: string;
  application_id: string;
  checklist_code: string;
  response: 'confirmed' | 'flagged' | 'n/a';
  note?: string | null;
  responded_by: string;
  responded_at: string;
};

export type CorrectionEvent = {
  id: string;
  application_id: string;
  event_kind: 'returned' | 'resubmitted';
  actor_user_id: string;
  note: string;
  created_at: string;
};

export type ApplicationDetail = {
  application: MembershipApplication;
  checklist_items: ChecklistItem[];
  checklist_responses: ChecklistResponse[];
  correction_history: CorrectionEvent[];
};

export type ApplicationListFilters = {
  kind?: ApplicationKind | '';
  status?: ApplicationStatus | '';
  fee_status?: string;
  unassigned?: boolean;
  branch_id?: string;
  submitted_by?: string;
  from?: string;
  to?: string;
  q?: string;
  limit?: number;
  offset?: number;
};

export async function createApplication(input: {
  kind: ApplicationKind;
  applicant_name: string;
  entity_type?: string;
  primary_phone?: string;
  primary_email?: string;
  branch_id?: string;
  applicant_payload: ApplicantPayload;
  registration_fee?: {
    amount_paid: string;
    payment_channel: string;
    payment_reference: string;
    payment_date: string;
    proof_doc_path?: string;
    shortfall_note?: string;
  };
}): Promise<MembershipApplication> {
  const r = await api.post('/v1/applications', input);
  return r.data.data;
}

export async function listApplications(f: ApplicationListFilters = {}): Promise<{ items: MembershipApplication[]; total: number }> {
  const q = new URLSearchParams();
  if (f.kind)         q.set('kind', f.kind);
  if (f.status)       q.set('status', f.status);
  if (f.fee_status)   q.set('fee_status', f.fee_status);
  if (f.unassigned)   q.set('unassigned', 'true');
  if (f.branch_id)    q.set('branch_id', f.branch_id);
  if (f.submitted_by) q.set('submitted_by', f.submitted_by);
  if (f.from)         q.set('from', f.from);
  if (f.to)           q.set('to', f.to);
  if (f.q)            q.set('q', f.q);
  if (f.limit)        q.set('limit', String(f.limit));
  if (f.offset)       q.set('offset', String(f.offset));
  const r = await api.get('/v1/applications' + (q.toString() ? '?' + q.toString() : ''));
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function getApplication(id: string): Promise<ApplicationDetail> {
  const r = await api.get(`/v1/applications/${id}`);
  return r.data.data;
}

export async function listApplicationChecklistItems(kind: ApplicationKind): Promise<{ items: ChecklistItem[]; total: number }> {
  const r = await api.get(`/v1/applications/checklist-items?kind=${kind}`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

async function appTransition(id: string, op: string, body: Record<string, string> = {}): Promise<MembershipApplication> {
  const r = await api.post(`/v1/applications/${id}/${op}`, body);
  return r.data.data;
}
export const startReview          = (id: string)                    => appTransition(id, 'start-review');
export const returnForCorrection  = (id: string, note: string)      => appTransition(id, 'return-for-correction', { note });
export const resubmitApplication  = (id: string, note: string)      => appTransition(id, 'resubmit', { note });
export const submitForApproval    = (id: string, note?: string)     => appTransition(id, 'submit-for-approval', note ? { note } : {});
export const declineApplication   = (id: string, reason: string)    => appTransition(id, 'decline', { decline_reason: reason });
export const returnToReviewer     = (id: string, note: string)      => appTransition(id, 'return-to-reviewer', { note });
export const withdrawApplication  = (id: string, reason: string)    => appTransition(id, 'withdraw', { note: reason });

// Approve returns a richer envelope so the UI can show the new member
// number + share/deposit account numbers without an extra fetch.
export async function approveApplication(id: string, conditions?: string): Promise<{ application: MembershipApplication; activation: ActivationResult | null }> {
  const r = await api.post(`/v1/applications/${id}/approve`, conditions ? { conditions } : {});
  return r.data.data;
}

// Unified Inbox (PR #8): submit an application for the member_onboarding
// workflow instead of acting inline. Idempotent — second call returns
// the existing wf_instance.
export type SubmitOnboardingResponse = {
  workflow_instance_id: string;
  status: 'created' | 'existing';
};
export async function submitApplicationForOnboardingDecision(id: string): Promise<SubmitOnboardingResponse> {
  const r = await api.post(`/v1/applications/${id}/submit-for-onboarding-decision`);
  return r.data.data;
}

export async function postRegistrationFeeRefund(id: string): Promise<MembershipApplication> {
  const r = await api.post(`/v1/applications/${id}/post-refund`);
  return r.data.data;
}

export async function respondToChecklist(applicationID: string, input: {
  code: string;
  response: 'confirmed' | 'flagged' | 'n/a';
  note?: string;
}): Promise<ChecklistResponse> {
  const r = await api.post(`/v1/applications/${applicationID}/checklist`, input);
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
  // Phase B bridge — present once migration 0008 has run.
  cp_number?: string | null;
  counterparty_id?: string | null;
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

// inviteAccept now returns access+refresh tokens for auto-login
// after activation. Caller passes the tokens to saveTokens() so the
// user lands on a logged-in session without a separate /login step.
export type InviteAcceptResult = {
  ok: true;
  tokens?: {
    access_token: string;
    refresh_token: string;
    expires_at: string;
    refresh_expires_at: string;
  };
  redirect?: string;
};

export async function inviteAccept(token: string, newPassword: string): Promise<InviteAcceptResult> {
  const r = await api.post('/v1/auth/invite/accept', { token, new_password: newPassword });
  return r.data.data;
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
  counterparty_id: string;
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
  counterparty_id: string;
  kind: DocumentKind;
  mime: string;
  size_bytes: number;
  uploaded_at: string;
};

export type ApiMemberDetail = ApiMember & {
  next_of_kin: ApiRelation | null;
  beneficiaries: ApiRelation[];
  // Backend may legitimately serialise this as null when the member has
  // no documents on file. Callers must coalesce before iterating.
  documents: ApiDocument[] | null;
  // Phase B bridge — present once migration 0008 has run.
  cp_number?: string | null;
  counterparty_id?: string | null;
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
  counterparty_id: string;
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
  counterparty_id: string;
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
  counterparty_id: string;
  member_no: string;
  full_name: string;
  last_activity_at?: string;
  days_inactive: number;
};

export type RecentStatusChange = MemberStatusChange & {
  member_no: string;
  full_name: string;
};

// MemberStatusCounts mirrors the Postgres function
// member_status_counts(tenant_id) — the canonical roll-call source of
// truth. Both the admin dashboard widget and the Members page KPI
// strip MUST consume these numbers (not a client-side tally) so the
// two views can never disagree. See member migration 0006 for full
// bucket-semantic documentation.
export type MemberStatusCounts = {
  active: number;
  dormant: number;
  pending: number;
  suspended: number;
  blacklisted: number;
  exited: number;
  deceased: number;
  rejected: number;
  // active + dormant + pending + suspended + blacklisted
  // (excludes exited / deceased / rejected)
  total_on_register: number;
  // active + dormant (dormant rolled into active for reporting)
  total_active_servicing: number;
};

export type MemberStatusSummary = {
  by_status: Partial<Record<MemberStatus, number>>;
  total_on_register: number;
  total_active_servicing: number;
  dormancy_pipeline: DormancyCandidate[];
  recent_changes: RecentStatusChange[];
  dormancy_threshold_days: number;
};

export async function getMemberStatusActions(counterpartyId: string): Promise<MemberStatusActions> {
  const r = await api.get(`/v1/counterparties/${counterpartyId}/status-actions`);
  return r.data.data;
}

export async function listMemberStatusHistory(counterpartyId: string): Promise<MemberStatusChange[]> {
  const r = await api.get(`/v1/counterparties/${counterpartyId}/status-history`);
  return r.data.data ?? [];
}

export async function changeMemberStatus(counterpartyId: string, input: {
  target_status: MemberStatus;
  reason_category: MemberStatusReason;
  reason_note?: string;
  review_date?: string;
  supporting_doc_path?: string;
  supporting_doc_mime?: string;
}): Promise<StatusChangeResponse> {
  const r = await api.post(`/v1/counterparties/${counterpartyId}/status-change`, input);
  return r.data.data;
}

export async function uploadStatusSupportingDoc(counterpartyId: string, file: Blob): Promise<{ storage_path: string; mime: string; size_bytes: number }> {
  const form = new FormData();
  form.append('file', file, (file as File).name ?? `support.${(file.type || 'application/pdf').split('/')[1] ?? 'bin'}`);
  const r = await api.post(`/v1/counterparties/${counterpartyId}/status-supporting-doc`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export async function getMemberStatusSummary(): Promise<MemberStatusSummary> {
  const r = await api.get('/v1/members/status/summary');
  return r.data.data;
}

// Leaner endpoint for views that just need the roll-call numbers (e.g.
// the Members page KPI strip), so we don't ship the dormancy pipeline
// + recent-changes panels on every navigation. Same underlying source
// function as getMemberStatusSummary.
export async function getMemberStatusCounts(): Promise<MemberStatusCounts> {
  const r = await api.get('/v1/members/status/counts');
  return r.data.data;
}

// ─── Unified counterparties register (Phase B) ───
//
// Reads from the new counterparties table that merges members +
// org_members. Existing /v1/members + /v1/orgs endpoints remain alive
// throughout Phase A/B. Gated client-side on
// tenant.feature_flags.unified_counterparties (defaults off).

export type CounterpartyKind =
  | 'individual' | 'chama' | 'company' | 'ngo' | 'church' | 'school' | 'other';

export type CounterpartyStatus =
  | 'pending' | 'active' | 'dormant' | 'suspended'
  | 'blacklisted' | 'exited' | 'deceased' | 'rejected';

export type CounterpartyKYCState = 'not_started' | 'in_review' | 'verified' | 'rejected';

export type Counterparty = {
  id: string;
  tenant_id: string;
  cp_number: string;
  legacy_id?: string | null;
  kind: CounterpartyKind;
  display_name: string;
  trading_as?: string | null;
  status: CounterpartyStatus;
  kyc_state: CounterpartyKYCState;
  risk_band: 'low' | 'medium' | 'high' | 'n_a';
  registration_no?: string | null;
  // JSON bags — exactly one is populated, never both. See migration 0007.
  individual?: Record<string, unknown> | null;
  institution?: Record<string, unknown> | null;
  contact: Record<string, unknown>;
  joined_at: string;
  closed_at?: string | null;
  created_at: string;
  updated_at: string;
  // The id of the linked legacy row (`members.id` when kind=individual,
  // `org_members.id` otherwise). Populated by backend SELECTs only.
  // Used by Members.tsx to route a unified row to the correct detail
  // page until the detail pages themselves are merged.
  legacy_target_id?: string | null;
};

export type ListCounterpartiesParams = {
  q?: string;
  kind?: CounterpartyKind | CounterpartyKind[];
  status?: CounterpartyStatus | CounterpartyStatus[];
  limit?: number;
  offset?: number;
};

export async function listCounterparties(p: ListCounterpartiesParams = {}): Promise<{
  counterparties: Counterparty[]; total: number; limit: number; offset: number;
}> {
  const q = new URLSearchParams();
  if (p.q) q.set('q', p.q);
  if (p.kind) {
    const ks = Array.isArray(p.kind) ? p.kind : [p.kind];
    for (const k of ks) q.append('kind', k);
  }
  if (p.status) {
    const ss = Array.isArray(p.status) ? p.status : [p.status];
    for (const s of ss) q.append('status', s);
  }
  if (p.limit !== undefined) q.set('limit', String(p.limit));
  if (p.offset !== undefined) q.set('offset', String(p.offset));
  const path = '/v1/counterparties' + (q.toString() ? '?' + q.toString() : '');
  const r = await api.get(path);
  return r.data.data;
}

export async function getCounterparty(id: string): Promise<Counterparty> {
  const r = await api.get(`/v1/counterparties/${id}`);
  return r.data.data;
}

// Static helper — institutional kinds in one place so filter chips
// and route guards don't drift.
export const INSTITUTIONAL_KINDS: CounterpartyKind[] =
  ['chama', 'company', 'ngo', 'church', 'school', 'other'];

export async function previewDormancyRun(): Promise<{ threshold_days: number; candidates: DormancyCandidate[] }> {
  const r = await api.post('/v1/members/dormancy/preview');
  return r.data.data;
}

export async function runDormancy(): Promise<{ threshold_days: number; candidates: DormancyCandidate[]; applied?: MemberStatusChange[] }> {
  const r = await api.post('/v1/members/dormancy/run');
  return r.data.data;
}

export async function uploadMemberDocument(
  counterpartyId: string,
  kind: DocumentKind,
  file: Blob,
  filename?: string,
): Promise<ApiDocument> {
  const form = new FormData();
  form.append('file', file, filename ?? `${kind}.${(file.type || 'image/png').split('/')[1] ?? 'bin'}`);
  const r = await api.post(`/v1/counterparties/${counterpartyId}/documents/${kind}`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export function memberDocumentURL(counterpartyId: string, kind: DocumentKind): string {
  return `${apiBase}/v1/counterparties/${counterpartyId}/documents/${kind}`;
}

// fetchMemberDocument loads the raw bytes (with auth) and returns a Blob
// the caller can convert to an object URL for <img src>.
export async function fetchMemberDocument(counterpartyId: string, kind: DocumentKind): Promise<Blob> {
  const r = await api.get(`/v1/counterparties/${counterpartyId}/documents/${kind}`, { responseType: 'blob' });
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

// ─── Collection Desk (Phase G) ───

export type ReceiptChannel =
  | 'cash' | 'mpesa' | 'airtel_money' | 'bank_transfer' | 'cheque' | 'standing_order';

export type ReceiptLineKind =
  | 'savings_deposit' | 'share_purchase' | 'loan_repayment' | 'fee' | 'welfare';

export type ReceiptStatus = 'draft' | 'posted' | 'voided';
export type ReceiptLineStatus = 'pending' | 'posted' | 'declined' | 'voided';

export type ApiReceiptLine = {
  id: string;
  receipt_id: string;
  line_no: number;
  kind: ReceiptLineKind;
  amount: string;
  target_account_id?: string | null;
  fee_code?: string | null;
  narration?: string | null;
  approval_id?: string | null;
  posted_txn_id?: string | null;
  status: ReceiptLineStatus;
  voided_at?: string | null;
  voided_by?: string | null;
  void_reason?: string | null;
  created_at: string;
  posted_at?: string | null;
};

export type ApiReceipt = {
  id: string;
  tenant_id: string;
  serial: string;
  counterparty_id: string;
  channel: ReceiptChannel;
  channel_ref?: string | null;
  channel_amount: string;
  value_date: string;
  narration?: string | null;
  cashier_user_id: string;
  till_session_id?: string | null;
  virtual_till_id?: string | null;
  status: ReceiptStatus;
  pdf_document_id?: string | null;
  voided_at?: string | null;
  voided_by?: string | null;
  void_reason?: string | null;
  created_at: string;
  posted_at?: string | null;
  updated_at: string;
  // Populated by getReceipt(id) only. Bug 3.1: the list endpoint
  // instead populates line_count + line_summary so the LINES column
  // is meaningful without a per-row detail fetch.
  lines?: ApiReceiptLine[];
  line_count?: number;
  line_summary?: string;
};

export type CurrentTillSession = {
  has_open_session: boolean;
  session_id?: string;
  till_id?: string;
  till_code?: string;
  till_name?: string;
  opened_at?: string;
};

export type LoanArrearSummary = {
  loan_id: string;
  loan_no: string;
  product_code: string;
  arrears_amount: string;
  days_past_due: number;
  classification: string;
};

export type FeeDue = {
  fee_code: string;
  description: string;
  amount: string;
  source_ref?: string;
};

export type ShareShortfallSummary = {
  share_account_id: string;
  shares_held: number;
  min_shares_policy: number;
  shortfall_shares: number;
  par_value: string;
  shortfall_kes: string;
};

export type CounterpartyOutstanding = {
  loan_arrears: LoanArrearSummary[];
  unpaid_fees: FeeDue[];
  share_shortfall?: ShareShortfallSummary | null;
  total_suggested: string;
};

export type CreateReceiptLineInput = {
  kind: ReceiptLineKind;
  amount: string; // decimal as string so we don't lose precision
  target_account_id?: string;
  fee_code?: string;
  narration?: string;
};

export type CreateReceiptInput = {
  counterparty_id: string;
  channel: ReceiptChannel;
  channel_ref?: string;
  channel_amount: string;
  value_date?: string; // YYYY-MM-DD; backend defaults to today
  narration?: string;
  lines: CreateReceiptLineInput[];
};

export async function getCurrentTillSession(): Promise<CurrentTillSession> {
  const r = await api.get(`/v1/till-sessions/current`);
  return r.data.data;
}

export async function getOutstanding(counterpartyId: string): Promise<CounterpartyOutstanding> {
  const r = await api.get(`/v1/counterparties/${counterpartyId}/outstanding`);
  return r.data.data;
}

export async function createReceipt(input: CreateReceiptInput): Promise<ApiReceipt> {
  const r = await api.post(`/v1/receipts`, input);
  return r.data.data;
}

export async function getReceipt(id: string): Promise<ApiReceipt> {
  const r = await api.get(`/v1/receipts/${id}`);
  return r.data.data;
}

export type ListReceiptsParams = {
  till_session_id?: string;
  virtual_till_id?: string;
  cashier_user_id?: string;
  value_date?: string;
  status?: ReceiptStatus;
};

export async function listReceipts(p: ListReceiptsParams = {}): Promise<ApiReceipt[]> {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(p)) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
  }
  const path = '/v1/receipts' + (q.toString() ? '?' + q.toString() : '');
  const r = await api.get(path);
  return Array.isArray(r.data.data) ? r.data.data : [];
}

export async function voidReceiptLine(receiptId: string, lineId: string, reason: string): Promise<void> {
  await api.post(`/v1/receipts/${receiptId}/lines/${lineId}/void`, { reason });
}

export type FeeCatalogEntry = {
  id: string;
  tenant_id: string;
  code: string;
  label: string;
  description?: string | null;
  amount_default: string;
  amount_editable: boolean;
  gl_credit_code: string;
  is_active: boolean;
  sort_order: number;
  created_at: string;
  updated_at: string;
};

export async function listFees(includeInactive = false): Promise<FeeCatalogEntry[]> {
  const q = includeInactive ? '?include_inactive=true' : '';
  const r = await api.get(`/v1/fees${q}`);
  return Array.isArray(r.data.data) ? r.data.data : [];
}

export type RenderReceiptPDFResponse = {
  pdf_document_id: string;
  download_url: string; // backend returns "/v1/pdf-documents/<id>/download"; client.ts prepends /api
};

export async function renderReceiptPDF(receiptId: string): Promise<RenderReceiptPDFResponse> {
  const r = await api.post(`/v1/receipts/${receiptId}/pdf`);
  return r.data.data;
}

// receiptPDFDownloadURL — builds the absolute admin-download URL for
// a generated receipt PDF. Routed to the notification service via
// vite's /api/v1/pdf-documents proxy (same auth + Host header as
// the rest of the SPA's calls).
export function receiptPDFDownloadURL(pdfDocumentID: string): string {
  return `/api/v1/pdf-documents/${pdfDocumentID}/download`;
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
  | 'callback_fired' | 'sla_breached'
  // PR #1 / PR #9 — Unified Inbox additions.
  | 'comment' | 'claim' | 'release';

// PR #1 — majority quorum added alongside any_one / all.
export type WFQuorum = 'any_one' | 'all' | 'majority';

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
  // PR #9 — Unified Inbox UX fields. Backed by the columns added in
  // PR #1's 0002_unified_inbox migration. claimed_* drives the
  // Awaiting-me / My-team / All-in-tenant tab bucketing; summary +
  // source_url drive the per-row card; sla_breach_at is an indexed
  // mirror of the active level's sla_due_at.
  summary?: string;
  source_url?: string;
  claimed_by?: string;
  claimed_at?: string;
  claim_expires?: string;
  sla_breach_at?: string;
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

// PR #9 — Inbox UX. Claim takes a 30-minute lock so two approvers
// in the same role don't double-decide. 409 on contention.
export async function claimWorkflowInstance(id: string): Promise<WFInstance> {
  const r = await api.post(`/v1/workflow-instances/${id}/claim`);
  return r.data.data;
}
export async function releaseWorkflowInstance(id: string): Promise<WFInstance> {
  const r = await api.post(`/v1/workflow-instances/${id}/release`);
  return r.data.data;
}
// Threaded comments — stored as wf_actions rows with action='comment'
// (see services/workflow/.../instances.go Comment handler from PR #1).
export async function addWorkflowInstanceComment(id: string, body: string): Promise<WFAction> {
  const r = await api.post(`/v1/workflow-instances/${id}/comments`, { body });
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

// Note: 'redemption' is kept in the type union for historical ledger
// rows only. New transactions cannot be created with this type —
// share capital is equity in this SACCO and cannot be redeemed; an
// exiting member must transfer their shares to another active member.
export type ShareTxnType =
  | 'purchase' | 'transfer_in' | 'transfer_out'
  | 'adjustment' | 'bonus_issue'
  | 'redemption';

export type SharePaymentChannel =
  | 'cash' | 'mpesa' | 'airtel_money' | 'bank_transfer'
  | 'payroll' | 'standing_order' | 'internal';

export type ShareAccountStatus = 'active' | 'closed';

export type ShareAccount = {
  id: string;
  tenant_id: string;
  counterparty_id: string;
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
  counterparty_id: string;
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
  counterparty_id: string;
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

export async function getShareAccountByMember(counterpartyId: string): Promise<ShareAccountView> {
  // Trailing slash matches chi.Route("/share-accounts/by-counterparty/{counterparty_id}").Get("/", ...)
  const r = await api.get(`/v1/share-accounts/by-counterparty/${counterpartyId}/`);
  return r.data.data;
}

export async function listShareTransactions(counterpartyId: string, opts: { limit?: number; offset?: number } = {}): Promise<ShareTransaction[]> {
  const q = new URLSearchParams();
  if (opts.limit) q.set('limit', String(opts.limit));
  if (opts.offset) q.set('offset', String(opts.offset));
  const r = await api.get(`/v1/share-accounts/by-counterparty/${counterpartyId}/transactions${q.toString() ? '?' + q.toString() : ''}`);
  return r.data.data ?? [];
}

export async function getCurrentCertificate(counterpartyId: string): Promise<ShareCertificate | null> {
  try {
    const r = await api.get(`/v1/share-accounts/by-counterparty/${counterpartyId}/certificate`);
    return r.data.data;
  } catch (e: unknown) {
    if (axiosErrStatus(e) === 404) return null;
    throw e;
  }
}

export async function purchaseShares(counterpartyId: string, input: {
  shares: number;
  payment_channel: SharePaymentChannel;
  payment_ref?: string;
  narration?: string;
}): Promise<CashActionResult<ShareTxnResponse>> {
  const r = await api.post(`/v1/share-accounts/by-counterparty/${counterpartyId}/purchase`, input);
  return unwrapCash(r);
}

export async function transferShares(counterpartyId: string, input: {
  shares: number;
  to_member_id: string;
  reason: string;
  narration?: string;
}): Promise<CashActionResult<ShareTransferResponse>> {
  const r = await api.post(`/v1/share-accounts/by-counterparty/${counterpartyId}/transfer`, input);
  return unwrapCash(r);
}

// Share redemption is intentionally removed — share capital is
// equity in this SACCO. Exiting members must use `transferShares` to
// move their balance to another active member.

export async function adjustShares(counterpartyId: string, input: {
  shares_delta: number;
  reason: string;
}): Promise<ShareTxnResponse> {
  const r = await api.post(`/v1/share-accounts/by-counterparty/${counterpartyId}/adjust`, input);
  return r.data.data;
}

export async function placeShareLien(counterpartyId: string, input: {
  shares: number;
  reason: string;
  reference_kind?: string;
  reference_id?: string;
}): Promise<CashActionResult<ShareLien>> {
  const r = await api.post(`/v1/share-accounts/by-counterparty/${counterpartyId}/lien`, input);
  return unwrapCash(r);
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

export async function bonusShareIssue(input: { pct_of_holding: string; reason: string }): Promise<CashActionResult<{ issued_to_count: number; total_bonus_shares: number; pct_applied: string }>> {
  const r = await api.post('/v1/share-accounts/bonus-issue', input);
  return unwrapCash(r);
}

function axiosErrStatus(e: unknown): number | undefined {
  if (typeof e === 'object' && e && 'response' in e) {
    const resp = (e as { response?: { status?: number } }).response;
    return resp?.status;
  }
  return undefined;
}

// ═══════════════════════════════════════════════════════════════════
// Deposits sub-module (services/savings)
// ═══════════════════════════════════════════════════════════════════

export type DepositProductType =
  | 'ordinary' | 'fixed' | 'junior' | 'holiday'
  | 'goal' | 'emergency' | 'group' | 'member_deposit';

// BOSA = non-withdrawable member-deposit bond (secures loans,
// redeemable on exit). FOSA = withdrawable savings of any product
// type. The two regulatory buckets SASRA cares about; surfaced
// across DepositProducts, Member 360, Collection Desk and the
// SASRA return once the BOSA_FOSA tenant flag is on.
export type DepositSegment = 'bosa' | 'fosa';

export type DepositEligibility =
  | 'individuals' | 'groups' | 'minors' | 'all';

export type MaturityAction =
  | 'none' | 'auto_renew' | 'liquidate_to_ordinary' | 'notify';

export type FeeFrequency =
  | 'none' | 'monthly' | 'quarterly' | 'annual';

export type DepositAccountStatus =
  | 'pending' | 'active' | 'dormant' | 'suspended' | 'matured' | 'closed';

export type DepositTxnType =
  | 'opening_balance' | 'deposit' | 'withdrawal'
  | 'transfer_in' | 'transfer_out'
  | 'interest_credit' | 'fee_debit'
  | 'reversal' | 'adjustment' | 'goal_payout';

export type DepositChannel =
  | 'cash' | 'mpesa' | 'airtel_money' | 'bank_transfer'
  | 'standing_order' | 'direct_debit' | 'payroll' | 'internal';

export type DepositProduct = {
  id: string;
  code: string;
  name: string;
  product_type: DepositProductType;
  description?: string;
  is_active: boolean;
  min_opening_balance: string;
  min_operating_balance: string;
  max_balance?: string;
  min_deposit_amount: string;
  max_deposit_amount?: string;
  min_withdrawal_amount: string;
  max_withdrawal_amount?: string;
  notice_period_days: number;
  max_withdrawals_per_month?: number;
  partial_withdrawal_allowed: boolean;
  large_withdrawal_threshold?: string;
  lock_in_months: number;
  default_term_months?: number;
  maturity_action: MaturityAction;
  eligibility: DepositEligibility;
  requires_approval_to_open: boolean;
  withdrawal_window_start_month?: number;
  withdrawal_window_end_month?: number;
  maintenance_fee: string;
  maintenance_fee_frequency: FeeFrequency;
  early_withdrawal_penalty_pct: string;
  below_min_balance_fee: string;
  dormancy_fee_monthly: string;
  interest_eligible: boolean;
  // BOSA / FOSA. Required by the backend (NOT NULL); old clients
  // that omit it on create get the inferred default — member_deposit
  // → bosa, anything else → fosa. Immutable post-create.
  segment: DepositSegment;
  // Recurring contribution schedule. Only meaningful for BOSA;
  // FOSA products leave both at the defaults (0 / undefined).
  required_monthly_amount: string;
  required_day_of_month?: number;
  created_at: string;
  updated_at: string;
};

export type DepositAccount = {
  id: string;
  counterparty_id: string;
  product_id: string;
  account_no: string;
  status: DepositAccountStatus;
  current_balance: string;
  available_balance: string;
  opened_at?: string;
  matures_at?: string;
  closed_at?: string;
  last_activity_at?: string;
  last_deposit_at?: string;
  last_withdrawal_at?: string;
  fixed_term_months?: number;
  fixed_interest_rate_pct?: string;
  goal_target_amount?: string;
  goal_target_date?: string;
  goal_description?: string;
  guardian_member_id?: string;
  group_org_id?: string;
  withdrawal_notice_given_at?: string;
  withdrawal_notice_amount?: string;
  created_at: string;
  updated_at: string;
};

export type DepositTransaction = {
  id: string;
  account_id: string;
  counterparty_id: string;
  txn_no: string;
  txn_type: DepositTxnType;
  amount: string;
  value_date: string;
  channel?: DepositChannel;
  channel_ref?: string;
  narration?: string;
  counterparty_account_id?: string;
  counterparty_txn_id?: string;
  reverses_txn_id?: string;
  reversed_by_txn_id?: string;
  reversal_reason?: string;
  balance_after: string;
  initiated_by: string;
  authorized_by?: string;
  authorization_reason?: string;
  workflow_instance_id?: string;
  posted_at: string;
  created_at: string;
};

export type DepositAccountView = {
  account: DepositAccount;
  product: DepositProduct;
  member: { ID: string; MemberNo: string; FullName: string; Status: string };
};

export type MemberDepositItem = {
  account: DepositAccount;
  product: DepositProduct;
};

export type DepositAcctListItem = {
  account: DepositAccount;
  member_no: string;
  full_name: string;
  member_status: string;
  product: { code: string; name: string; product_type: DepositProductType };
};

export type DepositsSummary = {
  total_accounts: number;
  active_accounts: number;
  dormant_accounts: number;
  total_balance: string;
  by_product: Array<{
    product_id: string;
    code: string;
    name: string;
    product_type: DepositProductType;
    active_accounts: number;
    total_balance: string;
  }>;
};

export type DepositStatement = {
  account: DepositAccount;
  product: DepositProduct;
  from: string;
  to: string;
  opening_balance: string;
  closing_balance: string;
  transactions: DepositTransaction[];
};

// ─────────── Product CRUD ───────────

// BOSA / FOSA filter is optional — omit to list everything. The
// backend rejects unknown segment values with a typed error, so a
// typo doesn't silently return the world.
export async function listDepositProducts(opts: { includeInactive?: boolean; segment?: DepositSegment } = {}): Promise<DepositProduct[]> {
  const qs = new URLSearchParams();
  if (opts.includeInactive) qs.set('include_inactive', '1');
  if (opts.segment) qs.set('segment', opts.segment);
  const r = await api.get('/v1/deposit-products' + (qs.toString() ? '?' + qs.toString() : ''));
  return r.data.data ?? [];
}

export async function getDepositProduct(id: string): Promise<DepositProduct> {
  const r = await api.get(`/v1/deposit-products/${id}`);
  return r.data.data;
}

export async function createDepositProduct(p: Partial<DepositProduct>): Promise<DepositProduct> {
  const r = await api.post('/v1/deposit-products', p);
  return r.data.data;
}

export async function updateDepositProduct(id: string, p: Partial<DepositProduct>): Promise<DepositProduct> {
  const r = await api.put(`/v1/deposit-products/${id}`, p);
  return r.data.data;
}

export async function deleteDepositProduct(id: string): Promise<void> {
  await api.delete(`/v1/deposit-products/${id}`);
}

// ─────────── Accounts ───────────

export async function listDepositAccounts(opts: { q?: string; status?: string; product_id?: string; limit?: number; offset?: number } = {}): Promise<{ items: DepositAcctListItem[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.q) p.set('q', opts.q);
  if (opts.status) p.set('status', opts.status);
  if (opts.product_id) p.set('product_id', opts.product_id);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/deposit-accounts' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getDepositsSummary(): Promise<DepositsSummary> {
  const r = await api.get('/v1/deposit-accounts/summary');
  return r.data.data;
}

export async function getDepositAccountsByMember(counterpartyId: string): Promise<MemberDepositItem[]> {
  const r = await api.get(`/v1/deposit-accounts/by-counterparty/${counterpartyId}`);
  return r.data.data ?? [];
}

export async function getDepositAccount(id: string): Promise<DepositAccountView> {
  const r = await api.get(`/v1/deposit-accounts/${id}/`);
  return r.data.data;
}

export async function openDepositAccount(input: {
  counterparty_id: string;
  product_id: string;
  opening_deposit: string;
  opening_channel?: DepositChannel;
  opening_channel_ref?: string;
  fixed_term_months?: number;
  fixed_interest_rate_pct?: string;
  goal_target_amount?: string;
  goal_target_date?: string;
  goal_description?: string;
  guardian_member_id?: string;
  group_org_id?: string;
}): Promise<{ account: DepositAccount; product: DepositProduct; opening_transaction?: DepositTransaction }> {
  const r = await api.post('/v1/deposit-accounts', input);
  return r.data.data;
}

export type CashActionResult<T> = { posted?: T; pending?: PendingApproval };

function unwrapCash<T>(r: { status: number; data: { data: any } }): CashActionResult<T> {
  if (r.status === 202 && r.data?.data?.pending) {
    return { pending: r.data.data.pending as PendingApproval };
  }
  return { posted: r.data.data as T };
}

export async function postDeposit(accountId: string, input: {
  amount: string;
  channel: DepositChannel;
  channel_ref?: string;
  narration?: string;
  value_date?: string;
  bypass_duplicate_check?: boolean;
}): Promise<CashActionResult<{ transaction: DepositTransaction; account: DepositAccount }>> {
  const r = await api.post(`/v1/deposit-accounts/${accountId}/deposit`, input);
  return unwrapCash(r);
}

export async function postWithdrawal(accountId: string, input: {
  amount: string;
  channel: DepositChannel;
  channel_ref?: string;
  narration?: string;
  reason?: string;
}): Promise<CashActionResult<{ transaction: DepositTransaction; account: DepositAccount; requires_approval: boolean }>> {
  const r = await api.post(`/v1/deposit-accounts/${accountId}/withdraw`, input);
  return unwrapCash(r);
}

export async function giveWithdrawalNotice(accountId: string, amount: string): Promise<void> {
  await api.post(`/v1/deposit-accounts/${accountId}/withdrawal-notice`, { amount });
}

export async function transferBetweenOwn(accountId: string, input: {
  amount: string;
  to_account_id: string;
  narration?: string;
}): Promise<CashActionResult<{ from: { transaction: DepositTransaction; account: DepositAccount }; to: { transaction: DepositTransaction; account: DepositAccount } }>> {
  const r = await api.post(`/v1/deposit-accounts/${accountId}/transfer`, input);
  return unwrapCash(r);
}

export async function reverseDeposit(accountId: string, txnId: string, reason: string): Promise<{ reversal: DepositTransaction; account: DepositAccount }> {
  const r = await api.post(`/v1/deposit-accounts/${accountId}/reverse`, { txn_id: txnId, reason });
  return r.data.data;
}

export async function adjustDeposit(accountId: string, amount: string, reason: string): Promise<{ transaction: DepositTransaction; account: DepositAccount }> {
  const r = await api.post(`/v1/deposit-accounts/${accountId}/adjust`, { amount, reason });
  return r.data.data;
}

export async function getDepositStatement(accountId: string, from?: string, to?: string): Promise<DepositStatement> {
  const p = new URLSearchParams();
  if (from) p.set('from', from);
  if (to) p.set('to', to);
  const r = await api.get(`/v1/deposit-accounts/${accountId}/statement` + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

// ═══════════════════════════════════════════════════════════════════
// Interest engine (Phase 4)
// ═══════════════════════════════════════════════════════════════════

export type InterestRunStatus =
  | 'draft' | 'computing' | 'preview' | 'approved'
  | 'posting' | 'posted' | 'locked' | 'cancelled';

export type InterestPayoutMethod = 'credit_savings' | 'buy_shares' | 'external';

export type InterestRun = {
  id: string;
  run_no: string;
  financial_year_label: string;
  fy_start: string;
  fy_end: string;
  status: InterestRunStatus;
  agm_rate_pct: string;
  agm_resolution_ref: string;
  agm_resolution_date: string;
  wht_rate_pct: string;
  product_ids: string[];
  member_count?: number;
  total_weighted_balance?: string;
  total_gross_interest?: string;
  total_wht?: string;
  total_net_interest?: string;
  notes?: string;
  created_at: string;
  created_by: string;
  computed_at?: string;
  submitted_at?: string;
  workflow_instance_id?: string;
  approved_at?: string;
  approved_by?: string;
  posted_at?: string;
  posted_by?: string;
  locked_at?: string;
  cancelled_at?: string;
  cancellation_reason?: string;
};

export type InterestRunLine = {
  id: string;
  run_id: string;
  account_id: string;
  counterparty_id: string;
  product_id: string;
  days_in_fy: number;
  days_with_snapshots: number;
  sum_of_daily_balances: string;
  weighted_avg_balance: string;
  rate_applied_pct: string;
  wht_rate_pct: string;
  gross_interest: string;
  wht_amount: string;
  net_interest: string;
  payout_method: InterestPayoutMethod;
  payout_target_account_id?: string;
  payout_external_channel?: string;
  payout_external_ref?: string;
  posted_at?: string;
  posted_txn_id?: string;
  share_txn_id?: string;
};

export type InterestRunDetail = {
  run: InterestRun;
  lines: InterestRunLine[];
};

export type WHTScheduleRow = {
  counterparty_id: string;
  member_no: string;
  member_name: string;
  gross_amount: string;
  wht_amount: string;
};

export type WHTSchedule = {
  fy_label: string;
  rows: WHTScheduleRow[];
  total_wht: string;
};

export type TaxPayableEntry = {
  id: string;
  source_kind: string;
  source_id?: string;
  counterparty_id: string;
  member_no: string;
  member_name: string;
  fy_label: string;
  gross_amount: string;
  wht_rate_pct: string;
  wht_amount: string;
  posted_at: string;
};

export type WHTCertificate = {
  counterparty_id: string;
  fy_label: string;
  entries: TaxPayableEntry[];
  totals: { gross_amount: string; wht_amount: string; net_amount: string };
};

export async function listInterestRuns(opts: { status?: string; fy?: string; limit?: number; offset?: number } = {}): Promise<{ items: InterestRun[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.fy) p.set('fy', opts.fy);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/interest-runs' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function createInterestRun(input: {
  financial_year_label?: string;
  fy_start: string;
  fy_end: string;
  agm_rate_pct: string;
  agm_resolution_ref: string;
  agm_resolution_date: string;
  wht_rate_pct?: string;
  product_ids: string[];
  notes?: string;
}): Promise<InterestRun> {
  const r = await api.post('/v1/interest-runs', input);
  return r.data.data;
}

export async function getInterestRun(id: string): Promise<InterestRunDetail> {
  const r = await api.get(`/v1/interest-runs/${id}`);
  return r.data.data;
}

export async function computeInterestRun(id: string): Promise<InterestRunDetail> {
  const r = await api.post(`/v1/interest-runs/${id}/compute`);
  return r.data.data;
}

export async function updateInterestLine(lineId: string, input: {
  payout_method: InterestPayoutMethod;
  payout_target_account_id?: string;
  payout_external_channel?: string;
  payout_external_ref?: string;
}): Promise<void> {
  await api.patch(`/v1/interest-run-lines/${lineId}`, input);
}

export async function submitInterestRun(id: string): Promise<{ workflow_instance_id: string; status: string }> {
  const r = await api.post(`/v1/interest-runs/${id}/submit`);
  return r.data.data;
}

export async function approveInterestRun(id: string, comment?: string): Promise<InterestRun> {
  const r = await api.post(`/v1/interest-runs/${id}/approve`, { comment: comment ?? '' });
  return r.data.data;
}

export async function postInterestRun(id: string): Promise<InterestRunDetail> {
  const r = await api.post(`/v1/interest-runs/${id}/post`);
  return r.data.data;
}

export async function lockInterestRun(id: string): Promise<InterestRun> {
  const r = await api.post(`/v1/interest-runs/${id}/lock`);
  return r.data.data;
}

export async function cancelInterestRun(id: string, reason: string): Promise<InterestRun> {
  const r = await api.post(`/v1/interest-runs/${id}/cancel`, { reason });
  return r.data.data;
}

export async function getWHTSchedule(fy: string): Promise<WHTSchedule> {
  const r = await api.get(`/v1/wht-schedule?fy=${encodeURIComponent(fy)}`);
  return r.data.data;
}

export async function getWHTCertificate(counterpartyId: string, fy: string): Promise<WHTCertificate> {
  const r = await api.get(`/v1/wht-certificate/${counterpartyId}?fy=${encodeURIComponent(fy)}`);
  return r.data.data;
}

// ═══════════════════════════════════════════════════════════════════
// Dividend engine (Phase 5)
// ═══════════════════════════════════════════════════════════════════

export type DividendRunStatus = InterestRunStatus; // same set
export type DividendCalcMethod = 'closing_balance' | 'average_monthly' | 'pro_rated';

export type DividendRun = {
  id: string;
  run_no: string;
  financial_year_label: string;
  fy_start: string;
  fy_end: string;
  status: DividendRunStatus;
  calc_method: DividendCalcMethod;
  agm_rate_pct: string;
  agm_resolution_ref: string;
  agm_resolution_date: string;
  wht_rate_pct: string;
  member_count?: number;
  total_share_basis?: string;
  total_gross_dividend?: string;
  total_wht?: string;
  total_net_dividend?: string;
  notes?: string;
  created_at: string;
  created_by: string;
  computed_at?: string;
  submitted_at?: string;
  workflow_instance_id?: string;
  approved_at?: string;
  posted_at?: string;
  locked_at?: string;
  cancelled_at?: string;
  cancellation_reason?: string;
};

export type DividendRunLine = {
  id: string;
  run_id: string;
  share_account_id: string;
  counterparty_id: string;
  calc_method: DividendCalcMethod;
  shares_basis: string;
  par_value_at_run: string;
  capital_basis: string;
  days_held_in_fy?: number;
  days_in_fy: number;
  rate_applied_pct: string;
  wht_rate_pct: string;
  gross_dividend: string;
  wht_amount: string;
  net_dividend: string;
  payout_method: InterestPayoutMethod;
  payout_target_account_id?: string;
  payout_external_channel?: string;
  payout_external_ref?: string;
  posted_at?: string;
  posted_deposit_txn_id?: string;
  posted_share_txn_id?: string;
};

export type DividendRunDetail = {
  run: DividendRun;
  lines: DividendRunLine[];
};

export async function listDividendRuns(opts: { status?: string; fy?: string; limit?: number; offset?: number } = {}): Promise<{ items: DividendRun[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.fy) p.set('fy', opts.fy);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/dividend-runs' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function createDividendRun(input: {
  financial_year_label?: string;
  fy_start: string;
  fy_end: string;
  calc_method: DividendCalcMethod;
  agm_rate_pct: string;
  agm_resolution_ref: string;
  agm_resolution_date: string;
  wht_rate_pct?: string;
  notes?: string;
}): Promise<DividendRun> {
  const r = await api.post('/v1/dividend-runs', input);
  return r.data.data;
}

export async function getDividendRun(id: string): Promise<DividendRunDetail> {
  const r = await api.get(`/v1/dividend-runs/${id}`);
  return r.data.data;
}

export async function computeDividendRun(id: string): Promise<DividendRunDetail> {
  const r = await api.post(`/v1/dividend-runs/${id}/compute`);
  return r.data.data;
}

export async function updateDividendLine(lineId: string, input: {
  payout_method: InterestPayoutMethod;
  payout_target_account_id?: string;
  payout_external_channel?: string;
  payout_external_ref?: string;
}): Promise<void> {
  await api.patch(`/v1/dividend-run-lines/${lineId}`, input);
}

export async function submitDividendRun(id: string): Promise<{ workflow_instance_id: string; status: string }> {
  const r = await api.post(`/v1/dividend-runs/${id}/submit`);
  return r.data.data;
}

export async function approveDividendRun(id: string, comment?: string): Promise<DividendRun> {
  const r = await api.post(`/v1/dividend-runs/${id}/approve`, { comment: comment ?? '' });
  return r.data.data;
}

export async function postDividendRun(id: string): Promise<DividendRunDetail> {
  const r = await api.post(`/v1/dividend-runs/${id}/post`);
  return r.data.data;
}

export async function lockDividendRun(id: string): Promise<DividendRun> {
  const r = await api.post(`/v1/dividend-runs/${id}/lock`);
  return r.data.data;
}

export async function cancelDividendRun(id: string, reason: string): Promise<DividendRun> {
  const r = await api.post(`/v1/dividend-runs/${id}/cancel`, { reason });
  return r.data.data;
}

// ═══════════════════════════════════════════════════════════════════
// Lending — products + purpose categories (Phase 6a)
// ═══════════════════════════════════════════════════════════════════

export type LoanCategory = 'short_term' | 'medium_term' | 'long_term' | 'emergency' | 'asset_finance' | 'group';
export type LoanInterestMethod = 'flat_rate' | 'reducing_balance';
export type LoanRepaymentMethod = 'reducing_balance' | 'flat_rate' | 'bullet' | 'interest_only';
export type LoanFeeTiming = 'upfront' | 'added_to_loan' | 'at_each_installment';

export type LoanProductFee = {
  id?: string;
  product_id?: string;
  name: string;
  amount: string;
  is_pct: boolean;
  timing: LoanFeeTiming;
  display_order: number;
};
export type LoanCollateralRequirement = 'required' | 'optional' | 'not_applicable';
// 'deposits' and 'shares_plus_deposits' are kept for backward
// compatibility with existing tenants but are deprecated. New loan
// products should pick 'bosa' or 'bosa_plus_shares' to match SACCO
// prudential practice (FOSA savings don't secure loans).
export type LoanMultiplierBasis =
  | 'none' | 'shares' | 'bosa' | 'bosa_plus_shares'
  | 'deposits' | 'shares_plus_deposits';

export function isLegacyMultiplierBasis(b: LoanMultiplierBasis): boolean {
  return b === 'deposits' || b === 'shares_plus_deposits';
}

export type LoanProduct = {
  id: string;
  code: string;
  name: string;
  category: LoanCategory;
  description?: string;
  is_active: boolean;
  min_amount: string;
  max_amount: string;
  multiplier_basis: LoanMultiplierBasis;
  multiplier_value?: string;
  min_term_months: number;
  max_term_months: number;
  default_term_months?: number;
  grace_period_months: number;
  interest_rate_pct: string;
  interest_method: LoanInterestMethod;
  repayment_method: LoanRepaymentMethod;
  fees: LoanProductFee[];
  penalty_rate_pct: string;
  min_guarantors: number;
  max_guarantor_exposure_pct: string;
  guarantor_must_be_member: boolean;
  collateral_requirement: LoanCollateralRequirement;
  min_membership_months: number;
  min_shares_required: number;
  allow_concurrent: boolean;
  workflow_definition_code?: string;
  auto_approval_threshold?: string;
  auto_approval_min_score?: number;
  allow_topup: boolean;
  allow_refinance: boolean;
  created_at: string;
  updated_at: string;
};

export type LoanPurposeCategory = {
  id: string;
  code: string;
  name: string;
  is_active: boolean;
  created_at: string;
};

export async function listLoanProducts(includeInactive = false): Promise<LoanProduct[]> {
  const r = await api.get('/v1/loan-products' + (includeInactive ? '?include_inactive=1' : ''));
  return r.data.data ?? [];
}

export async function getLoanProduct(id: string): Promise<LoanProduct> {
  const r = await api.get(`/v1/loan-products/${id}`);
  return r.data.data;
}

export async function createLoanProduct(p: Partial<LoanProduct>): Promise<LoanProduct> {
  const r = await api.post('/v1/loan-products', p);
  return r.data.data;
}

export async function updateLoanProduct(id: string, p: Partial<LoanProduct>): Promise<LoanProduct> {
  const r = await api.put(`/v1/loan-products/${id}`, p);
  return r.data.data;
}

export async function deleteLoanProduct(id: string): Promise<void> {
  await api.delete(`/v1/loan-products/${id}`);
}

export async function listLoanPurposeCategories(includeInactive = false): Promise<LoanPurposeCategory[]> {
  const r = await api.get('/v1/loan-purpose-categories' + (includeInactive ? '?include_inactive=1' : ''));
  return r.data.data ?? [];
}

export async function createLoanPurposeCategory(c: { code: string; name: string; is_active?: boolean }): Promise<LoanPurposeCategory> {
  const r = await api.post('/v1/loan-purpose-categories', c);
  return r.data.data;
}

// ═══════════════════════════════════════════════════════════════════
// Loan applications + loans (Phases 6b + 6c)
// ═══════════════════════════════════════════════════════════════════

export type LoanAppStatus =
  | 'draft' | 'pending_validation' | 'pending_guarantor' | 'pending_scoring'
  | 'pending_approval' | 'approved' | 'approved_with_conditions'
  | 'declined' | 'returned_for_info'
  | 'offer_sent' | 'offer_accepted' | 'offer_declined' | 'expired' | 'cancelled' | 'disbursed';

export type LoanEmploymentType =
  | 'salaried' | 'self_employed' | 'business_owner' | 'retired' | 'student' | 'other';

export type LoanCollateralKind =
  | 'title_deed' | 'vehicle_logbook' | 'equipment'
  | 'listed_shares' | 'fixed_deposit_lien' | 'other';

export type LoanGuaranteeStatus =
  | 'pending_consent' | 'accepted' | 'declined' | 'released' | 'called_upon';

export type LoanStatus =
  | 'pending_disbursement' | 'active' | 'in_arrears' | 'defaulted'
  | 'restructured' | 'settled' | 'written_off' | 'closed';

export type LoanApplication = {
  id: string;
  application_no: string;
  counterparty_id: string;
  product_id: string;
  status: LoanAppStatus;
  requested_amount: string;
  requested_term_months: number;
  purpose_category_id?: string;
  purpose_note?: string;
  preferred_disbursement_channel?: string;
  employment_type?: LoanEmploymentType;
  employer_name?: string;
  monthly_net_income: string;
  other_income: string;
  monthly_expenses: string;
  monthly_existing_obligations: string;
  credit_score?: number;
  risk_band?: string;
  affordability_pass?: boolean;
  dti_ratio?: string;
  net_disposable_income?: string;
  computed_max_amount?: string;
  computed_max_installment?: string;
  recommended_amount?: string;
  recommended_term_months?: number;
  scoring_details?: unknown;
  scoring_flags?: unknown;
  scored_at?: string;
  workflow_instance_id?: string;
  approved_amount?: string;
  approved_term_months?: number;
  approved_interest_rate_pct?: string;
  approved_at?: string;
  approved_by?: string;
  approval_conditions?: string;
  decline_category?: string;
  decline_reason?: string;
  offer_letter_path?: string;
  offer_sent_at?: string;
  offer_expires_at?: string;
  offer_accepted_at?: string;
  notes?: string;
  created_at: string;
  updated_at: string;
};

export type LoanGuarantee = {
  id: string;
  application_id: string;
  loan_id?: string;
  guarantor_member_id: string;
  amount_guaranteed: string;
  status: LoanGuaranteeStatus;
  requested_at: string;
  responded_at?: string;
  decline_reason?: string;
};

export type LoanCollateralItem = {
  id: string;
  application_id: string;
  loan_id?: string;
  kind: LoanCollateralKind;
  description: string;
  estimated_value: string;
  forced_sale_value?: string;
  valuation_date?: string;
  status: string;
};

export type LoanScoreFactor = { name: string; score: number; weight: number; note: string };
export type LoanScoreFlag = { severity: 'hard_block' | 'soft_flag' | 'advisory'; code: string; message: string };
export type LoanScoreResult = {
  overall_score: number;
  risk_band: string;
  factors: LoanScoreFactor[];
  flags: LoanScoreFlag[];
  has_hard_block: boolean;
  affordability_pass: boolean;
  dti_ratio: string;
  net_disposable_income: string;
  computed_installment: string;
  computed_max_amount: string;
  computed_max_installment: string;
  recommended_amount: string;
  recommended_term_months: number;
};

export type CreateLoanAppResponse = {
  application: LoanApplication;
  guarantees: LoanGuarantee[];
  collateral: LoanCollateralItem[];
  score: LoanScoreResult;
};

export type LoanAppListItem = {
  application: LoanApplication;
  member_no: string;
  member_name: string;
  product_code: string;
  product_name: string;
};

export type ScheduleFeeLine = {
  name: string;
  amount: string;
  is_pct: boolean;
  rate: string;
  timing: 'upfront' | 'added_to_loan' | 'at_each_installment';
};

export type ScheduleSnapshot = {
  generated_at: string;
  principal: string;
  interest_rate_pct: string;
  term_months: number;
  grace_period_months: number;
  interest_method: string;
  repayment_method: string;
  start_date: string;
  first_due_date: string;
  rows: Array<{
    installment_no: number;
    due_date: string;
    principal_due: string;
    interest_due: string;
    fee_due: string;
    total_due: string;
    outstanding_after: string;
  }>;
  fees: ScheduleFeeLine[] | null;
  total_upfront_fees: string;
  total_recurring_fees: string;
  net_disbursed: string;
  total_principal: string;
  total_interest: string;
  total_payable: string;
  installment: string;
};

export type LoanAppDetail = {
  application: LoanApplication;
  guarantees: LoanGuarantee[];
  collateral: LoanCollateralItem[];
  schedule?: ScheduleSnapshot;
};

export async function listLoanApplications(opts: { status?: string; counterparty_id?: string; product_id?: string; q?: string; limit?: number; offset?: number } = {}): Promise<{ items: LoanAppListItem[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.counterparty_id) p.set('counterparty_id', opts.counterparty_id);
  if (opts.product_id) p.set('product_id', opts.product_id);
  if (opts.q) p.set('q', opts.q);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/loan-applications' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getLoanApplication(id: string): Promise<LoanAppDetail> {
  const r = await api.get(`/v1/loan-applications/${id}`);
  return r.data.data;
}

export async function createLoanApplication(input: {
  counterparty_id: string;
  product_id: string;
  requested_amount: string;
  requested_term_months: number;
  purpose_category_id?: string;
  purpose_note?: string;
  preferred_disbursement_channel?: string;
  employment_type?: LoanEmploymentType;
  employer_name?: string;
  employer_payroll_contact?: string;
  monthly_net_income: string;
  other_income?: string;
  monthly_expenses: string;
  monthly_existing_obligations?: string;
  guarantors: { counterparty_id: string; amount_guaranteed: string }[];
  collateral: { kind: LoanCollateralKind; description: string; estimated_value: string; forced_sale_value?: string; valuation_date?: string; notes?: string }[];
  notes?: string;
}): Promise<CreateLoanAppResponse> {
  const r = await api.post('/v1/loan-applications', input);
  return r.data.data;
}

export async function rescoreLoanApplication(id: string): Promise<CreateLoanAppResponse> {
  const r = await api.post(`/v1/loan-applications/${id}/score`);
  return r.data.data;
}

export async function approveLoanApplication(id: string, input: {
  approved_amount?: string;
  approved_term_months?: number;
  approved_interest_rate_pct?: string;
  approval_conditions?: string;
}): Promise<LoanApplication> {
  const r = await api.post(`/v1/loan-applications/${id}/approve`, input);
  return r.data.data;
}

export async function declineLoanApplication(id: string, category: string, reason: string): Promise<LoanApplication> {
  const r = await api.post(`/v1/loan-applications/${id}/decline`, { category, reason });
  return r.data.data;
}

// Unified Inbox (PR #4): submit a loan application for the credit
// decision workflow instead of acting inline. Idempotent — second
// call returns the existing instance.
export type SubmitForDecisionResponse = {
  workflow_instance_id: string;
  status: 'created' | 'existing';
};

export async function submitLoanApplicationForDecision(id: string): Promise<SubmitForDecisionResponse> {
  const r = await api.post(`/v1/loan-applications/${id}/submit-for-decision`);
  return r.data.data;
}

export async function respondToGuarantee(guaranteeId: string, accept: boolean, declineReason?: string): Promise<LoanGuarantee> {
  const r = await api.post(`/v1/loan-guarantees/${guaranteeId}/respond`, { accept, decline_reason: declineReason });
  return r.data.data;
}

// MemberGuarantorship — a loan-guarantee row joined with the borrower
// + product so the Member Profile People tab can render a row without
// follow-up lookups. Returned by /v1/loan-guarantees/by-counterparty/{id}.
export type MemberGuarantorship = LoanGuarantee & {
  loan_no: string | null;
  application_no: string;
  borrower_member_id: string;
  borrower_full_name: string;
  product_code: string;
  product_name: string;
};

export async function listGuaranteesByMember(counterpartyId: string): Promise<MemberGuarantorship[]> {
  const r = await api.get(`/v1/loan-guarantees/by-counterparty/${counterpartyId}`);
  return r.data.data ?? [];
}

export async function sendLoanOffer(appId: string, input: { expires_at?: string; letter_path?: string } = {}): Promise<LoanApplication> {
  const r = await api.post(`/v1/loan-applications/${appId}/send-offer`, input);
  return r.data.data;
}

export async function acceptLoanOffer(appId: string): Promise<{ application: LoanApplication; loan: Loan }> {
  const r = await api.post(`/v1/loan-applications/${appId}/accept-offer`, { confirmed: true });
  return r.data.data;
}

// ─────────── Loans ───────────

export type Loan = {
  id: string;
  loan_no: string;
  application_id: string;
  counterparty_id: string;
  product_id: string;
  status: LoanStatus;
  principal: string;
  interest_rate_pct: string;
  interest_method: LoanInterestMethod;
  repayment_method: LoanRepaymentMethod;
  term_months: number;
  grace_period_months: number;
  installment_count: number;
  first_due_date?: string;
  disbursement_channel?: string;
  disbursement_target_account_id?: string;
  disbursement_ref?: string;
  total_fees_deducted: string;
  net_disbursed?: string;
  disbursed_at?: string;
  principal_disbursed: string;
  principal_repaid: string;
  principal_balance: string;
  interest_charged: string;
  interest_paid: string;
  interest_balance: string;
  fees_charged: string;
  fees_paid: string;
  fees_balance: string;
  penalty_accrued: string;
  penalty_paid: string;
  penalty_balance: string;
  installments_paid: number;
  next_installment_due_at?: string;
  next_installment_amount?: string;
  days_past_due: number;
  arrears_classification: string;
  last_repayment_at?: string;
  created_at: string;
  updated_at: string;
};

export type LoanInstallment = {
  id: string;
  loan_id: string;
  installment_no: number;
  due_date: string;
  principal_due: string;
  interest_due: string;
  fee_due: string;
  total_due: string;
  principal_paid: string;
  interest_paid: string;
  fee_paid: string;
  status: string;
  paid_at?: string;
  outstanding_after: string;
};

export type LoanTransaction = {
  id: string;
  loan_id: string;
  txn_no: string;
  txn_type: string;
  amount: string;
  principal_component: string;
  interest_component: string;
  fee_component: string;
  penalty_component: string;
  value_date: string;
  channel?: string;
  channel_ref?: string;
  narration?: string;
  posted_at: string;
};

export type LoanDetail = {
  loan: Loan;
  schedule: LoanInstallment[];
  transactions: LoanTransaction[];
  guarantees: LoanGuarantee[];
  collateral: LoanCollateralItem[];
};

export type LoanListItem = {
  loan: Loan;
  member_no: string;
  member_name: string;
  product_code: string;
  product_name: string;
};

export async function listLoans(opts: { status?: string; counterparty_id?: string; product_id?: string; q?: string; limit?: number; offset?: number } = {}): Promise<{ items: LoanListItem[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.counterparty_id) p.set('counterparty_id', opts.counterparty_id);
  if (opts.product_id) p.set('product_id', opts.product_id);
  if (opts.q) p.set('q', opts.q);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/loans' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getLoan(id: string): Promise<LoanDetail> {
  const r = await api.get(`/v1/loans/${id}`);
  return r.data.data;
}

export async function disburseLoan(id: string, input: {
  channel: string;
  target_account_id?: string;
  external_ref?: string;
  value_date?: string;
}): Promise<CashActionResult<{
  loan: Loan;
  schedule: LoanInstallment[];
  fees: LoanTransaction[];
  disbursement: LoanTransaction;
  net_disbursed: string;
}>> {
  const r = await api.post(`/v1/loans/${id}/disburse`, input);
  return unwrapCash(r);
}

// ─────────── Repayment + DPD (Phase 6d) ───────────

export type RepaymentAllocation = {
  Penalty: string;
  Interest: string;
  Principal: string;
  Fees: string;
  Suspense: string;
};

export type DPDResult = {
  LoanID: string;
  DaysPastDue: number;
  Classification: string;
  StatusChanged: boolean;
  PreviousStatus: LoanStatus;
  NewStatus: LoanStatus;
};

export type RepayResponse = {
  transaction: LoanTransaction;
  allocation: RepaymentAllocation;
  loan: Loan;
  dpd: DPDResult;
};

export type LoanPayoff = {
  loan: Loan;
  payoff: string;
  breakdown: {
    principal_balance: string;
    interest_balance: string;
    fees_balance: string;
    penalty_balance: string;
  };
};

export type ArrearsBand = {
  classification: string;
  loan_count: number;
  total_outstanding: string;
};

export type ArrearsSummary = {
  bands: ArrearsBand[];
  total_loans: number;
  total_outstanding: string;
  npl_loan_count: number;
  npl_outstanding: string;
  npl_ratio_pct: string;
};

export async function getLoanPayoff(loanId: string): Promise<LoanPayoff> {
  const r = await api.get(`/v1/loans/${loanId}/payoff`);
  return r.data.data;
}

export async function repayLoan(loanId: string, input: {
  amount: string;
  channel: string;
  channel_ref?: string;
  narration?: string;
  value_date?: string;
  debit_savings_account_id?: string;
}): Promise<CashActionResult<RepayResponse>> {
  const r = await api.post(`/v1/loans/${loanId}/repay`, input);
  return unwrapCash(r);
}

export async function settleLoan(loanId: string, input: {
  channel: string;
  channel_ref?: string;
  narration?: string;
  debit_savings_account_id?: string;
}): Promise<CashActionResult<RepayResponse>> {
  const r = await api.post(`/v1/loans/${loanId}/settle`, input);
  return unwrapCash(r);
}

export async function reverseLoanTxn(txnId: string, reason: string): Promise<CashActionResult<{ reversal: LoanTransaction; loan: Loan }>> {
  const r = await api.post(`/v1/loan-transactions/${txnId}/reverse`, { reason });
  return unwrapCash(r);
}

export async function recalcLoanDPD(loanId: string): Promise<DPDResult> {
  const r = await api.post(`/v1/loans/${loanId}/recalc-dpd`);
  return r.data.data;
}

export async function getLoanArrearsSummary(): Promise<ArrearsSummary> {
  const r = await api.get('/v1/loans/arrears-summary');
  return r.data.data;
}

// ─────────── Collections + restructuring (Phase 6e) ───────────

export type CollectionCaseStatus =
  | 'open' | 'in_progress' | 'paused' | 'escalated_legal'
  | 'closed_recovered' | 'closed_uncollectable';

export type CollectionContactKind = 'call' | 'sms' | 'whatsapp' | 'email' | 'in_person_visit' | 'letter';
export type ContactOutcome =
  | 'reached' | 'no_answer' | 'wrong_number' | 'busy'
  | 'left_message' | 'promise_made' | 'dispute' | 'refused' | 'visited_not_home';
export type PTPStatus = 'open' | 'kept' | 'partial' | 'broken' | 'cancelled';

export type CollectionCase = {
  id: string;
  loan_id: string;
  counterparty_id: string;
  status: CollectionCaseStatus;
  classification_at_open?: string;
  assigned_to?: string;
  assigned_at?: string;
  priority: number;
  total_contacts: number;
  last_contact_at?: string;
  last_action?: string;
  notes?: string;
  opened_at: string;
  closed_at?: string;
  closure_reason?: string;
};

export type CollectionContact = {
  id: string;
  case_id: string;
  kind: CollectionContactKind;
  outcome: ContactOutcome;
  note?: string;
  contacted_at: string;
  contacted_by: string;
};

export type PromiseToPay = {
  id: string;
  case_id: string;
  loan_id: string;
  promised_amount: string;
  promised_date: string;
  promised_channel?: string;
  status: PTPStatus;
  paid_amount: string;
  resolved_at?: string;
  notes?: string;
  created_at: string;
};

export type CollectionCaseListItem = {
  case: CollectionCase;
  loan: Loan;
  member_no: string;
  member_name: string;
  product_code: string;
  open_ptps: number;
};

export type CollectionCaseDetail = {
  case: CollectionCase;
  loan: Loan;
  contacts: CollectionContact[];
  ptps: PromiseToPay[];
};

export async function listCollectionCases(opts: { status?: string; assigned_to?: string; unassigned?: boolean; limit?: number; offset?: number } = {}): Promise<{ items: CollectionCaseListItem[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.assigned_to) p.set('assigned_to', opts.assigned_to);
  if (opts.unassigned) p.set('unassigned', '1');
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/collection-cases' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getCollectionCase(caseId: string): Promise<CollectionCaseDetail> {
  const r = await api.get(`/v1/collection-cases/${caseId}`);
  return r.data.data;
}

export async function assignCollectionCase(caseId: string, assignTo: string): Promise<CollectionCase> {
  const r = await api.post(`/v1/collection-cases/${caseId}/assign`, { assign_to: assignTo });
  return r.data.data;
}

export async function closeCollectionCase(caseId: string, recovered: boolean, reason: string): Promise<CollectionCase> {
  const r = await api.post(`/v1/collection-cases/${caseId}/close`, { recovered, reason });
  return r.data.data;
}

export async function logCollectionContact(caseId: string, input: {
  kind: CollectionContactKind;
  outcome: ContactOutcome;
  note?: string;
}): Promise<CollectionContact> {
  const r = await api.post(`/v1/collection-cases/${caseId}/contacts`, input);
  return r.data.data;
}

export async function createPromiseToPay(caseId: string, input: {
  promised_amount: string;
  promised_date: string;
  promised_channel?: string;
  notes?: string;
}): Promise<PromiseToPay> {
  const r = await api.post(`/v1/collection-cases/${caseId}/promises`, input);
  return r.data.data;
}

export async function resolvePromiseToPay(ptpId: string, status: 'kept' | 'partial' | 'broken' | 'cancelled', paidAmount: string, notes?: string): Promise<PromiseToPay> {
  const r = await api.post(`/v1/promises/${ptpId}/resolve`, { status, paid_amount: paidAmount, notes });
  return r.data.data;
}

// ─────────── Restructuring ───────────

export type RestructuringKind = 'reschedule' | 'topup' | 'refinance' | 'moratorium' | 'settlement_discount';

export type LoanRestructuring = {
  id: string;
  loan_id: string;
  kind: RestructuringKind;
  reason: string;
  previous_principal_balance?: string;
  previous_interest_balance?: string;
  previous_term_months?: number;
  previous_interest_rate_pct?: string;
  previous_status?: LoanStatus;
  new_term_months?: number;
  new_interest_rate_pct?: string;
  topup_amount?: string;
  refinance_new_loan_id?: string;
  moratorium_months?: number;
  moratorium_suspend_interest?: boolean;
  discount_amount?: string;
  authorized_at?: string;
  created_at: string;
};

export async function rescheduleLoan(loanId: string, input: {
  new_term_months: number;
  new_interest_rate_pct?: string;
  new_first_due_date?: string;
  reason: string;
}): Promise<CashActionResult<{ restructuring: LoanRestructuring; loan: Loan }>> {
  const r = await api.post(`/v1/loans/${loanId}/reschedule`, input);
  return unwrapCash(r);
}

export async function moratoriumLoan(loanId: string, input: {
  moratorium_months: number;
  suspend_interest: boolean;
  reason: string;
}): Promise<CashActionResult<{ restructuring: LoanRestructuring; loan: Loan }>> {
  const r = await api.post(`/v1/loans/${loanId}/moratorium`, input);
  return unwrapCash(r);
}

export async function settlementDiscountLoan(loanId: string, input: {
  discount_amount: string;
  reason: string;
}): Promise<CashActionResult<{ restructuring: LoanRestructuring; loan: Loan }>> {
  const r = await api.post(`/v1/loans/${loanId}/settlement-discount`, input);
  return unwrapCash(r);
}

export async function recordTopupIntent(loanId: string, topupAmount: string, reason: string): Promise<LoanRestructuring> {
  const r = await api.post(`/v1/loans/${loanId}/topup-intent`, { topup_amount: topupAmount, reason });
  return r.data.data;
}

export async function listRestructurings(loanId: string): Promise<LoanRestructuring[]> {
  const r = await api.get(`/v1/loans/${loanId}/restructurings`);
  return r.data.data ?? [];
}

// ───────────────────────────── Loan reports (Phase 6f) ─────────────────────────────

export type PortfolioSummary = {
  total_loans_lifetime: number;
  total_disbursed_lifetime: string;
  total_outstanding: string;
  principal_outstanding: string;
  interest_receivable: string;
  fees_receivable: string;
  penalty_receivable: string;
  active_loans: number;
  in_arrears_loans: number;
  restructured_loans: number;
  settled_loans: number;
  written_off_loans: number;
  by_product: Array<{
    product_id: string;
    product_code: string;
    product_name: string;
    active_loans: number;
    total_outstanding: string;
    principal_outstanding: string;
  }>;
  by_status: Array<{ status: string; loan_count: number; outstanding: string }>;
};

export async function getPortfolioSummary(): Promise<PortfolioSummary> {
  const r = await api.get('/v1/loan-reports/portfolio');
  return r.data.data;
}

export type AgingBand = {
  classification: string;
  loan_count: number;
  principal_balance: string;
  interest_balance: string;
  total_outstanding: string;
  provisioning_pct: string;
  provisioning_amount: string;
};

export type AgingReport = {
  bands: AgingBand[] | null;
  total_loans: number;
  total_outstanding: string;
  total_provisioning: string;
  npl_loan_count: number;
  npl_outstanding: string;
  npl_ratio_pct: string;
};

export async function getAgingReport(): Promise<AgingReport> {
  const r = await api.get('/v1/loan-reports/aging');
  return r.data.data;
}

export type MaturingLoan = {
  loan: Loan;
  member_no: string;
  member_name: string;
  product_name: string;
  final_due_date: string;
  days_until_final: number;
};

export async function getMaturingLoans(withinDays = 30): Promise<{ within_days: number; items: MaturingLoan[] }> {
  const r = await api.get(`/v1/loan-reports/maturing?within_days=${withinDays}`);
  return r.data.data;
}

export type RestructuringRegisterEntry = {
  restructuring: LoanRestructuring;
  loan_no: string;
  member_no: string;
  member_name: string;
  product_name: string;
};

export async function getRestructuringRegister(kind = ''): Promise<RestructuringRegisterEntry[]> {
  const qs = kind ? `?kind=${encodeURIComponent(kind)}` : '';
  const r = await api.get(`/v1/loan-reports/restructured${qs}`);
  return r.data.data?.items ?? [];
}

export type LoanWriteoff = {
  id: string;
  loan_id: string;
  counterparty_id: string;
  principal_written_off: string;
  interest_written_off: string;
  fees_written_off: string;
  penalty_written_off: string;
  total_written_off: string;
  reason: string;
  authorized_at: string;
  authorized_by: string;
  writeoff_txn_id?: string;
};

export type WriteoffRegisterEntry = {
  writeoff: LoanWriteoff;
  loan_no: string;
  member_no: string;
  member_name: string;
  recovered_amount: string;
};

export async function getWriteoffRegister(): Promise<WriteoffRegisterEntry[]> {
  const r = await api.get('/v1/loan-reports/writeoffs');
  return r.data.data?.items ?? [];
}

export async function writeOffLoan(loanId: string, reason: string): Promise<CashActionResult<{ writeoff: LoanWriteoff; loan: Loan }>> {
  const r = await api.post(`/v1/loans/${loanId}/writeoff`, { reason });
  return unwrapCash(r);
}

export type CRBRecord = {
  loan_no: string;
  counterparty_id: string;
  member_name: string;
  id_doc_number: string;
  disbursed_at?: string;
  principal_disbursed: string;
  outstanding_balance: string;
  days_past_due: number;
  classification: string;
  is_npl: boolean;
};

export async function getCRBSubmission(): Promise<{ records: CRBRecord[]; record_count: number }> {
  const r = await api.get('/v1/loan-reports/crb-submission');
  return r.data.data;
}

export type MemberLoanHistory = {
  counterparty_id: string;
  total_loans_ever_taken: number;
  active_loans: number;
  total_disbursed: string;
  total_outstanding: string;
  loans: Array<{ loan: Loan; product_code: string; product_name: string }>;
};

export async function getMemberLoanHistory(counterpartyId: string): Promise<MemberLoanHistory> {
  const r = await api.get(`/v1/loan-reports/by-counterparty/${counterpartyId}`);
  return r.data.data;
}

// ─── Unified member ledger ───
//
// Returned by GET /v1/members/{id}/ledger — a single timeline UNION-
// ALL'd across the three transaction tables (deposit / loan / share)
// for cases where staff need a "all the money this member moved"
// view without flipping between three module screens.

export type LedgerSource = 'deposit' | 'loan' | 'share' | 'fee';

export type LedgerRow = {
  source: LedgerSource;
  txn_id: string;
  txn_no: string;
  posted_at: string;
  value_date?: string | null;
  // Per-source txn_type values — see the SQL in
  // services/savings/internal/store/member_ledger_store.go for the
  // full enum lists. Frontend renders this via a chip mapping.
  txn_type: string;
  account_id: string;
  // account_no for deposit / share rows; loan_no for loan rows;
  // fee_code (or 'welfare') for fee rows.
  account_label: string;
  narration?: string | null;
  // Both are non-negative decimal strings. Exactly one is non-zero on
  // cash-flow rows; both are zero on info-only rows (e.g. an interest
  // accrual on a loan, which adds to outstanding but is not a cash
  // movement). Fee rows are always debit-only.
  debit: string;
  credit: string;
  // Source-account balance immediately after this row. For deposits
  // this is deposit_accounts.balance_after; for loans the loan's
  // principal_balance; for shares the share account amount value.
  // Fee rows have no per-account balance — always 0.
  balance_after: string;
  // Set on source='fee' rows so the UI can deep-link to
  // /collect/receipts/{receipt_id} for the printable slip. Null on
  // every other source.
  receipt_id?: string | null;
  // PR 5: BOSA/FOSA chip data. Populated only on source='deposit'
  // rows; null/undefined on loan/share/fee rows. Reads from the
  // joined deposit_products.segment in the ledger query.
  segment?: 'bosa' | 'fosa' | null;
};

export type LedgerPage = {
  rows: LedgerRow[];
  next_cursor?: string;
  has_more: boolean;
};

export async function getMemberLedger(
  counterpartyId: string,
  opts: { limit?: number; before?: string } = {},
): Promise<LedgerPage> {
  const q = new URLSearchParams();
  if (opts.limit) q.set('limit', String(opts.limit));
  if (opts.before) q.set('before', opts.before);
  const path = `/v1/member-ledger/${counterpartyId}${q.toString() ? '?' + q.toString() : ''}`;
  const r = await api.get(path);
  return r.data.data;
}

// ───────────────────────────── Maker-checker (Phase 7b) ─────────────────────────────

export type ApprovalKind =
  | 'deposit' | 'withdrawal' | 'deposit_transfer'
  | 'share_purchase' | 'share_transfer' | 'share_bonus'
  | 'loan_disbursement' | 'loan_repayment' | 'loan_settle' | 'loan_reverse'
  | 'loan_writeoff' | 'loan_reschedule' | 'loan_moratorium' | 'loan_settlement_discount';

export type ApprovalStatus = 'pending' | 'approved' | 'declined' | 'cancelled' | 'execution_error';

export type PendingApproval = {
  id: string;
  tenant_id: string;
  kind: ApprovalKind;
  status: ApprovalStatus;
  title: string;
  subject_member_id?: string;
  subject_account_id?: string;
  subject_loan_id?: string;
  amount?: string;
  payload: string; // base64 JSON in Go, comes through as object after axios JSON parse — kept as any
  maker_user_id: string;
  maker_at: string;
  maker_note?: string;
  checker_user_id?: string;
  checker_at?: string;
  checker_note?: string;
  result_txn_id?: string;
  result_error?: string;
  created_at: string;
  // Set after the Unified Inbox cutover (PR #3): the workflow_instance
  // this row was migrated into. Frontend uses it to deep-link from
  // /cash-approvals to /approvals/{id} once the tenant flag is on.
  workflow_instance_id?: string;
};

// ─── Unified Inbox status probe ───
//
// Backed by GET /v1/inbox-status on the workflow service. Tells the
// frontend whether to render the /cash-approvals deprecation banner
// for the current tenant.

// Despite the legacy name, the endpoint now returns every
// tenant-level boolean toggle the frontend reads on cold start —
// the spec calls these "feature flags". Adding a per-flag endpoint
// per toggle would multiply round-trips, so new flags hang off this
// same response.
export type InboxStatus = {
  unified_inbox_enabled: boolean;
  bosa_fosa_enabled: boolean;
};

export async function getInboxStatus(): Promise<InboxStatus> {
  const r = await api.get('/v1/inbox-status');
  return r.data.data;
}

export type ApprovalToggles = {
  deposit: boolean;
  withdrawal: boolean;
  deposit_transfer: boolean;
  share_purchase: boolean;
  share_transfer: boolean;
  share_bonus: boolean;
  share_lien: boolean;
  loan_disbursement: boolean;
  loan_repayment: boolean;
  loan_settle: boolean;
  loan_reverse: boolean;
  loan_writeoff: boolean;
  loan_reschedule: boolean;
  loan_moratorium: boolean;
  loan_settlement_discount: boolean;
  allow_self: boolean;
};

export async function listPendingApprovals(opts: {
  status?: string;
  kind?: string;
  counterparty_id?: string;
  maker_user_id?: string;
  include_closed?: boolean;
  limit?: number;
  offset?: number;
} = {}): Promise<{ items: PendingApproval[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.kind) p.set('kind', opts.kind);
  if (opts.counterparty_id) p.set('counterparty_id', opts.counterparty_id);
  if (opts.maker_user_id) p.set('maker_user_id', opts.maker_user_id);
  if (opts.include_closed) p.set('include_closed', '1');
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/pending-approvals' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getPendingApproval(id: string): Promise<PendingApproval> {
  const r = await api.get(`/v1/pending-approvals/${id}`);
  return r.data.data;
}

export async function approvePendingApproval(id: string, note?: string): Promise<{ approval: PendingApproval; result: any }> {
  const r = await api.post(`/v1/pending-approvals/${id}/approve`, { note: note ?? '' });
  return r.data.data;
}

export async function declinePendingApproval(id: string, note: string): Promise<PendingApproval> {
  const r = await api.post(`/v1/pending-approvals/${id}/decline`, { note });
  return r.data.data;
}

export async function cancelPendingApproval(id: string, note?: string): Promise<PendingApproval> {
  const r = await api.post(`/v1/pending-approvals/${id}/cancel`, { note: note ?? '' });
  return r.data.data;
}

export async function getApprovalSettings(): Promise<ApprovalToggles> {
  const r = await api.get('/v1/approval-settings');
  return r.data.data;
}

export async function updateApprovalSettings(input: Partial<ApprovalToggles>): Promise<ApprovalToggles> {
  const r = await api.put('/v1/approval-settings', input);
  return r.data.data;
}

// ───────────────────────────── Notifications (Stage 1) ─────────────────────────────

export type NotificationPriority = 'info' | 'success' | 'warning' | 'error';
export type NotificationStatus =
  | 'pending' | 'queued' | 'sent' | 'delivered' | 'read' | 'failed';
export type NotificationChannel = 'in_app' | 'sms' | 'email';

export type NotificationFeedItem = {
  id: string;
  tenant_id: string;
  event_code: string;
  priority: NotificationPriority;
  recipient_member_id?: string;
  recipient_user_id?: string;
  recipient_name: string;
  source_module?: string;
  source_record_id?: string;
  deep_link?: string;
  payload: any;
  created_at: string;
  body: string;
  in_app_status: NotificationStatus;
  read_at?: string;
};

export type NotificationDelivery = {
  id: string;
  notification_id: string;
  channel: NotificationChannel;
  subject?: string;
  body: string;
  status: NotificationStatus;
  attempt_count: number;
  queued_at?: string;
  sent_at?: string;
  delivered_at?: string;
  read_at?: string;
  failed_at?: string;
  failure_reason?: string;
  provider_message_id?: string;
  created_at: string;
};

export type NotificationLogEntry = NotificationFeedItem & {
  deliveries: NotificationDelivery[];
};

export async function getNotificationFeed(unread = false, limit = 50): Promise<{ items: NotificationFeedItem[]; total: number }> {
  const p = new URLSearchParams();
  if (unread) p.set('unread', '1');
  if (limit) p.set('limit', String(limit));
  const r = await api.get('/v1/notifications' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getNotificationUnreadCount(): Promise<number> {
  const r = await api.get('/v1/notifications/unread');
  return r.data.data?.unread ?? 0;
}

export async function markNotificationRead(id: string): Promise<void> {
  await api.post(`/v1/notifications/${id}/read`);
}

export async function markAllNotificationsRead(): Promise<number> {
  const r = await api.post('/v1/notifications/mark-all-read');
  return r.data.data?.marked ?? 0;
}

export async function getNotificationLog(limit = 50, offset = 0): Promise<{ items: NotificationLogEntry[]; total: number }> {
  const p = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  const r = await api.get('/v1/notifications/log?' + p.toString());
  return r.data.data;
}

// ─────────── Shared driver (platform-owned) — Stage 9 ───────────
//
// Tenants no longer configure their own SMTP/SMS provider; the platform
// owns the credentials and tenants consume prepaid credits per send.
// These types are kept so legacy callers compile, but the public
// configuration endpoints are gone — only the platform-admin variants
// at /v1/platform/notification-config/* exist.

export type SMTPEncryption = 'none' | 'starttls' | 'tls';
export type SMSProvider = 'mock' | 'sandbox' | 'production';

export type PlatformSMTPConfig = {
  host: string;
  port: number;
  encryption: SMTPEncryption;
  username: string;
  has_password: boolean;
  from_address: string;
  from_name: string;
  is_enabled: boolean;
  updated_at: string;
  updated_by?: string | null;
};

export type PlatformSMSConfig = {
  provider: SMSProvider;
  username: string;
  has_api_key: boolean;
  sender_id: string;
  rate_per_minute: number;
  has_webhook_secret: boolean;
  is_enabled: boolean;
  updated_at: string;
  updated_by?: string | null;
};

export async function getPlatformSMTP(): Promise<PlatformSMTPConfig> {
  const r = await api.get('/v1/platform/notification-config/smtp');
  return r.data.data;
}
export async function updatePlatformSMTP(input: {
  host: string;
  port: number;
  encryption: SMTPEncryption;
  username?: string;
  password?: string; // omit to keep existing
  from_address: string;
  from_name?: string;
  is_enabled: boolean;
}): Promise<PlatformSMTPConfig> {
  const r = await api.put('/v1/platform/notification-config/smtp', input);
  return r.data.data;
}
export async function testPlatformSMTP(to: string, subject?: string, body?: string): Promise<{ ok: boolean; to?: string; provider_message_id?: string; error?: string }> {
  const r = await api.post('/v1/platform/notification-config/smtp/test', { to, subject, body });
  return r.data.data;
}
export async function getPlatformSMS(): Promise<PlatformSMSConfig> {
  const r = await api.get('/v1/platform/notification-config/sms');
  return r.data.data;
}
export async function updatePlatformSMS(input: {
  provider: SMSProvider;
  username?: string;
  api_key?: string;
  sender_id?: string;
  rate_per_minute?: number;
  webhook_secret?: string;
  is_enabled: boolean;
}): Promise<PlatformSMSConfig> {
  const r = await api.put('/v1/platform/notification-config/sms', input);
  return r.data.data;
}
export async function testPlatformSMS(to: string, body?: string): Promise<{ ok: boolean; to?: string; provider_message_id?: string; error?: string }> {
  const r = await api.post('/v1/platform/notification-config/sms/test', { to, body });
  return r.data.data;
}

// ─────────── Credits — Stage 9 ───────────

export type CreditChannel = 'sms' | 'email';

export type CreditBalance = {
  tenant_id: string;
  channel: CreditChannel;
  balance: number;
  low_balance_threshold: number;
  low_balance_alerted_at?: string | null;
  zero_balance_alerted_at?: string | null;
  last_topup_at?: string | null;
  last_topup_credits?: number | null;
  updated_at: string;
};

export type CreditPricing = {
  tenant_id: string;
  channel: CreditChannel;
  price_per_credit: string;
  currency_code: string;
  updated_at: string;
};

export type CreditMovementType = 'topup' | 'consumption' | 'adjustment' | 'expiry' | 'refund';

export type CreditLedgerEntry = {
  id: string;
  tenant_id: string;
  channel: CreditChannel;
  movement_type: CreditMovementType;
  credits: number;
  balance_after: number;
  notification_id?: string | null;
  delivery_id?: string | null;
  reference?: string | null;
  actioned_by?: string | null;
  notes?: string | null;
  created_at: string;
};

export type TopupStatus = 'pending' | 'fulfilled' | 'rejected' | 'cancelled';
export type TopupRequest = {
  id: string;
  tenant_id: string;
  channel: CreditChannel;
  credits_requested: number;
  status: TopupStatus;
  requested_by?: string | null;
  requested_at: string;
  fulfilled_by?: string | null;
  fulfilled_at?: string | null;
  fulfillment_ledger_id?: string | null;
  notes?: string | null;
  rejection_reason?: string | null;
};

export type CreditsOverview = {
  balances: CreditBalance[];
  pricing: CreditPricing[];
  pending_topups: TopupRequest[];
};

export async function getCreditsOverview(): Promise<CreditsOverview> {
  const r = await api.get('/v1/credits');
  return r.data.data;
}
export async function getCreditsLedger(opts: { channel?: CreditChannel; movement_type?: CreditMovementType; limit?: number; offset?: number } = {}): Promise<{ items: CreditLedgerEntry[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.channel) p.set('channel', opts.channel);
  if (opts.movement_type) p.set('movement_type', opts.movement_type);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/credits/ledger' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function setLowBalanceThreshold(channel: CreditChannel, threshold: number): Promise<void> {
  await api.put(`/v1/credits/threshold/${channel}`, { threshold });
}
export async function createTopupRequest(channel: CreditChannel, credits: number, notes?: string): Promise<TopupRequest> {
  const r = await api.post('/v1/credits/topup-requests', { channel, credits, notes });
  return r.data.data;
}
export async function listTopupRequests(opts: { status?: TopupStatus; channel?: CreditChannel } = {}): Promise<{ items: TopupRequest[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.channel) p.set('channel', opts.channel);
  const r = await api.get('/v1/credits/topup-requests' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function cancelTopupRequest(id: string): Promise<void> {
  await api.post(`/v1/credits/topup-requests/${id}/cancel`);
}
export async function listBlockedDeliveries(channel: CreditChannel): Promise<{ items: string[] }> {
  const r = await api.get(`/v1/credits/blocked?channel=${channel}`);
  return r.data.data;
}
export async function retryBlockedDelivery(id: string): Promise<void> {
  await api.post(`/v1/credits/blocked/${id}/retry`);
}

// Platform-admin tenant credit management
export type PlatformTenantBalance = {
  tenant_id: string;
  slug: string;
  name: string;
  balances: CreditBalance[];
};
export async function listPlatformTenantBalances(): Promise<{ items: PlatformTenantBalance[] }> {
  const r = await api.get('/v1/platform/credits/tenants');
  return r.data.data;
}
export async function getPlatformTenantDetail(tenantId: string): Promise<{ balances: CreditBalance[]; pricing: CreditPricing[] }> {
  const r = await api.get(`/v1/platform/credits/tenants/${tenantId}`);
  return r.data.data;
}
export async function platformTopup(tenantId: string, input: { channel: CreditChannel; credits: number; reference?: string; notes?: string }): Promise<{ new_balance: number; ledger_id: string }> {
  const r = await api.post(`/v1/platform/credits/tenants/${tenantId}/topup`, input);
  return r.data.data;
}
export async function platformLedger(tenantId: string, opts: { channel?: CreditChannel; movement_type?: CreditMovementType; limit?: number } = {}): Promise<{ items: CreditLedgerEntry[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.channel) p.set('channel', opts.channel);
  if (opts.movement_type) p.set('movement_type', opts.movement_type);
  if (opts.limit) p.set('limit', String(opts.limit));
  const r = await api.get(`/v1/platform/credits/tenants/${tenantId}/ledger` + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function platformUpdatePricing(tenantId: string, input: { channel: CreditChannel; price_per_credit: string; currency_code?: string }): Promise<void> {
  await api.put(`/v1/platform/credits/tenants/${tenantId}/pricing`, input);
}
export async function platformListTopupRequests(opts: { status?: TopupStatus; tenant_id?: string } = {}): Promise<{ items: Array<TopupRequest & { tenant_slug?: string; tenant_name?: string }> }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.tenant_id) p.set('tenant_id', opts.tenant_id);
  const r = await api.get('/v1/platform/credits/topup-requests' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function platformFulfillTopupRequest(id: string, input: { reference?: string; notes?: string } = {}): Promise<{ new_balance: number; ledger_id: string }> {
  const r = await api.post(`/v1/platform/credits/topup-requests/${id}/fulfill`, input);
  return r.data.data;
}
export async function platformRejectTopupRequest(id: string, reason?: string): Promise<void> {
  await api.post(`/v1/platform/credits/topup-requests/${id}/reject`, { reason });
}
export async function platformUsageSummary(): Promise<{
  totals: Array<{ channel: string; total_sold: number; total_consumed: number }>;
  zero_balance_tenants: Array<{ tenant_id: string; slug: string; channel: string; balance: number }>;
}> {
  const r = await api.get('/v1/platform/credits/usage-summary');
  return r.data.data;
}

// ─────────── Credit adjustments (maker/checker) ───────────

export type AdjustmentStatus = 'pending_approval' | 'approved' | 'rejected';
export type CreditAdjustment = {
  id: string;
  tenant_id: string;
  channel: CreditChannel;
  credits: number;            // positive or negative
  reason: string;
  status: AdjustmentStatus;
  requested_by: string;
  requested_at: string;
  approved_by?: string | null;
  approved_at?: string | null;
  rejected_by?: string | null;
  rejected_at?: string | null;
  rejection_reason?: string | null;
  applied_ledger_id?: string | null;
};

export async function platformRequestAdjustment(
  tenantId: string,
  input: { channel: CreditChannel; credits: number; reason: string },
): Promise<CreditAdjustment> {
  const r = await api.post(`/v1/platform/credits/tenants/${tenantId}/adjustments`, input);
  return r.data.data;
}
export async function platformListAdjustments(
  opts: { status?: AdjustmentStatus; tenant_id?: string } = {},
): Promise<{ items: Array<CreditAdjustment & { tenant_slug?: string; tenant_name?: string }> }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.tenant_id) p.set('tenant_id', opts.tenant_id);
  const r = await api.get('/v1/platform/credits/adjustments' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function platformApproveAdjustment(id: string): Promise<{ ledger_id: string }> {
  const r = await api.post(`/v1/platform/credits/adjustments/${id}/approve`);
  return r.data.data;
}
export async function platformRejectAdjustment(id: string, reason?: string): Promise<void> {
  await api.post(`/v1/platform/credits/adjustments/${id}/reject`, { reason });
}

// ─────────── OTP (Stage 6) ───────────

export type OTPSettings = {
  tenant_id: string;
  code_length: number;
  expiry_minutes: number;
  max_attempts: number;
  resend_cooldown_seconds: number;
  default_channel: NotificationChannel;
  updated_at: string;
};

export type OTPRequestRow = {
  id: string;
  tenant_id: string;
  purpose: string;
  subject_user_id?: string;
  subject_member_id?: string;
  subject_identifier?: string;
  channel: NotificationChannel;
  destination: string;
  code_length: number;
  status: 'pending' | 'verified' | 'expired' | 'exhausted' | 'cancelled';
  attempts_used: number;
  max_attempts: number;
  generated_at: string;
  expires_at: string;
  verified_at?: string;
  ip_address?: string;
  notification_id?: string;
};

export async function getOTPSettings(): Promise<OTPSettings> {
  const r = await api.get('/v1/otp-settings');
  return r.data.data;
}

export async function updateOTPSettings(input: Partial<Omit<OTPSettings, 'tenant_id' | 'updated_at'>>): Promise<OTPSettings> {
  const r = await api.put('/v1/otp-settings', input);
  return r.data.data;
}

export async function listOTPRequests(opts: { status?: string; purpose?: string; limit?: number; offset?: number } = {}): Promise<{ items: OTPRequestRow[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.purpose) p.set('purpose', opts.purpose);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/otp-requests' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

// ─────────── Notification events + templates (catalog) ───────────

export type NotificationEvent = {
  code: string;
  category: string;
  default_priority: NotificationPriority;
  description: string;
  default_channels: NotificationChannel[];
  allowed_variables: string[];
  has_pdf_attachment: boolean;
  is_active: boolean;
};

export type NotificationTemplate = {
  id: string;
  tenant_id: string;
  event_code: string;
  channel: NotificationChannel;
  subject?: string;
  body: string;
  is_active: boolean;
};

export async function listNotificationEvents(): Promise<NotificationEvent[]> {
  const r = await api.get('/v1/notification-events');
  return r.data.data.items ?? [];
}

export async function listNotificationTemplates(): Promise<NotificationTemplate[]> {
  const r = await api.get('/v1/notification-templates');
  return r.data.data.items ?? [];
}

export async function createNotificationTemplate(input: {
  event_code: string;
  channel: NotificationChannel;
  subject?: string;
  body: string;
  is_active: boolean;
}): Promise<NotificationTemplate> {
  const r = await api.post('/v1/notification-templates', input);
  return r.data.data;
}

export async function getNotificationTemplate(id: string): Promise<NotificationTemplate> {
  const r = await api.get(`/v1/notification-templates/${id}`);
  return r.data.data;
}

export async function updateNotificationTemplate(id: string, input: {
  event_code: string;
  channel: NotificationChannel;
  subject?: string;
  body: string;
  is_active: boolean;
}): Promise<NotificationTemplate> {
  const r = await api.put(`/v1/notification-templates/${id}`, input);
  return r.data.data;
}

export async function deleteNotificationTemplate(id: string): Promise<void> {
  await api.delete(`/v1/notification-templates/${id}`);
}

export async function cloneNotificationTemplate(id: string): Promise<NotificationTemplate> {
  const r = await api.post(`/v1/notification-templates/${id}/clone`);
  return r.data.data;
}

export async function previewNotificationTemplate(input: {
  subject?: string;
  body: string;
  payload?: Record<string, unknown>;
}): Promise<{ subject: string; body: string }> {
  const r = await api.post('/v1/notification-templates/preview', input);
  return r.data.data;
}

// ─────────── Campaigns (Stage 7) ───────────

export type CampaignStatus =
  | 'draft'
  | 'awaiting_approval'
  | 'scheduled'
  | 'sending'
  | 'sent'
  | 'cancelled'
  | 'failed';

export type AudienceFilter =
  | { type: 'all_members' }
  | { type: 'status'; status: 'active' | 'dormant' | 'suspended' }
  | { type: 'active_loans' }
  | { type: 'loan_defaulters'; dpd_min?: number }
  | { type: 'custom_list'; member_ids: string[] };

export type Campaign = {
  id: string;
  tenant_id: string;
  name: string;
  description?: string | null;
  event_code: string;
  channels: NotificationChannel[];
  audience: AudienceFilter;
  payload: Record<string, unknown>;
  status: CampaignStatus;
  scheduled_for?: string | null;
  estimated_recipients: number;
  total_recipients: number;
  dispatched_count: number;
  failed_count: number;
  created_at: string;
  created_by?: string | null;
  approved_at?: string | null;
  approved_by?: string | null;
  sent_at?: string | null;
  cancelled_at?: string | null;
  cancel_reason?: string | null;
  failure_reason?: string | null;
  updated_at: string;
};

export type CampaignPreviewSample = {
  channel: NotificationChannel;
  subject?: string;
  body: string;
};

export type CampaignPreview = {
  campaign_id: string;
  event_code: string;
  estimated_recipients: number;
  samples: CampaignPreviewSample[] | null;
};

export type CampaignSettings = {
  tenant_id: string;
  approval_recipient_threshold: number;
  updated_at: string;
};

export async function listCampaigns(opts: { status?: string; limit?: number; offset?: number } = {}): Promise<{ items: Campaign[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set('status', opts.status);
  if (opts.limit) p.set('limit', String(opts.limit));
  if (opts.offset) p.set('offset', String(opts.offset));
  const r = await api.get('/v1/campaigns' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}

export async function getCampaign(id: string): Promise<Campaign> {
  const r = await api.get(`/v1/campaigns/${id}`);
  return r.data.data;
}

export async function createCampaign(input: {
  name: string;
  description?: string;
  event_code: string;
  channels: NotificationChannel[];
  audience: AudienceFilter;
  payload?: Record<string, unknown>;
  scheduled_for?: string;
}): Promise<Campaign> {
  const r = await api.post('/v1/campaigns', input);
  return r.data.data;
}

export async function previewCampaign(id: string): Promise<CampaignPreview> {
  const r = await api.post(`/v1/campaigns/${id}/preview`);
  return r.data.data;
}

export async function scheduleCampaign(id: string, scheduled_for: string): Promise<void> {
  await api.post(`/v1/campaigns/${id}/schedule`, { scheduled_for });
}

export async function sendCampaign(id: string): Promise<void> {
  await api.post(`/v1/campaigns/${id}/send`);
}

export async function cancelCampaign(id: string, reason?: string): Promise<void> {
  await api.post(`/v1/campaigns/${id}/cancel`, reason ? { reason } : {});
}

export async function getCampaignSettings(): Promise<CampaignSettings> {
  const r = await api.get('/v1/campaign-settings');
  return r.data.data;
}

export async function updateCampaignSettings(approval_recipient_threshold: number): Promise<CampaignSettings> {
  const r = await api.put('/v1/campaign-settings', { approval_recipient_threshold });
  return r.data.data;
}

// ─────────── Scheduled jobs (Stage 7) ───────────

export type ScheduledJob = {
  id: string;
  tenant_id: string;
  job_key: string;
  description?: string | null;
  cron_expr: string;
  is_active: boolean;
  config: Record<string, unknown>;
  last_run_at?: string | null;
  next_run_at?: string | null;
  created_at: string;
  updated_at: string;
  next_computed?: string | null; // server-computed preview
};

export type JobRun = {
  id: string;
  tenant_id: string;
  scheduled_job_id?: string | null;
  job_key: string;
  scheduled_for: string;
  started_at: string;
  finished_at?: string | null;
  records_processed: number;
  records_failed: number;
  status: 'running' | 'succeeded' | 'failed';
  error_message?: string | null;
};

export async function listScheduledJobs(): Promise<{ items: ScheduledJob[] }> {
  const r = await api.get('/v1/scheduled-jobs');
  return r.data.data;
}

export async function getScheduledJob(id: string): Promise<ScheduledJob> {
  const r = await api.get(`/v1/scheduled-jobs/${id}`);
  return r.data.data;
}

export async function updateScheduledJob(id: string, input: { cron_expr: string; is_active?: boolean }): Promise<ScheduledJob> {
  const r = await api.put(`/v1/scheduled-jobs/${id}`, input);
  return r.data.data;
}

export async function runScheduledJob(id: string): Promise<{ run_id: string; status: string; processed: number; failed: number }> {
  const r = await api.post(`/v1/scheduled-jobs/${id}/run`);
  return r.data.data;
}

export async function listJobRuns(jobId: string, limit = 25): Promise<{ items: JobRun[] }> {
  const r = await api.get(`/v1/scheduled-jobs/${jobId}/runs?limit=${limit}`);
  return r.data.data;
}

export async function previewCron(cron_expr: string): Promise<{ next_firings: string[] }> {
  const r = await api.post('/v1/scheduled-jobs/preview-cron', { cron_expr });
  return r.data.data;
}

// ─────────── Accounting & Finance (Stage 11) ───────────

export type AccountClass = 'asset' | 'liability' | 'equity' | 'income' | 'expense';
export type NormalBalance = 'debit' | 'credit';

export type CoAAccount = {
  id: string;
  tenant_id: string;
  code: string;
  name: string;
  class: AccountClass;
  type: string;
  parent_id?: string | null;
  normal_balance: NormalBalance;
  currency_code: string;
  is_active: boolean;
  is_system_locked: boolean;
  description?: string | null;
  created_at: string;
  updated_at: string;
};

export async function listCoA(activeOnly = false): Promise<{ items: CoAAccount[]; total: number }> {
  const q = activeOnly ? '?active=1' : '';
  const r = await api.get('/v1/coa' + q);
  return r.data.data;
}
export async function createCoAAccount(input: {
  code: string;
  name: string;
  class: AccountClass;
  type: string;
  parent_code?: string;
  normal_balance: NormalBalance;
  currency_code?: string;
  is_active: boolean;
  description?: string;
}): Promise<CoAAccount> {
  const r = await api.post('/v1/coa', input);
  return r.data.data;
}
export async function updateCoAAccount(id: string, input: {
  name: string;
  type: string;
  parent_code?: string;
  is_active: boolean;
  description?: string;
}): Promise<CoAAccount> {
  const r = await api.patch(`/v1/coa/${id}`, input);
  return r.data.data;
}

export type JournalEntryStatus = 'draft' | 'pending_approval' | 'posted' | 'rejected';
export type JournalEntryType = 'auto' | 'manual' | 'adjustment' | 'reversal' | 'opening_balance';

export type JournalLine = {
  id: string;
  entry_id: string;
  line_no: number;
  account_id: string;
  account_code?: string;
  account_name?: string;
  debit: string;
  credit: string;
  narration?: string | null;
};

export type JournalEntry = {
  id: string;
  tenant_id: string;
  entry_no?: string | null;
  entry_date: string;
  value_date: string;
  period_year: number;
  period_month: number;
  entry_type: JournalEntryType;
  source_module?: string | null;
  source_ref?: string | null;
  narration: string;
  status: JournalEntryStatus;
  total_debits: string;
  total_credits: string;
  reversal_of?: string | null;
  created_by?: string | null;
  created_at: string;
  posted_by?: string | null;
  posted_at?: string | null;
  rejected_by?: string | null;
  rejected_at?: string | null;
  rejection_reason?: string | null;
  // Set when the Unified Inbox (PR #7) gates the JE through the
  // workflow engine. Drives the deep-link banner + the "Open in
  // Inbox →" affordance.
  workflow_instance_id?: string | null;
  lines?: JournalLine[];
};

export async function listJournalEntries(opts: { status?: string; entry_type?: string; from?: string; to?: string; limit?: number } = {}): Promise<{ items: JournalEntry[]; total: number }> {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(opts)) if (v != null && v !== '') p.set(k, String(v));
  const r = await api.get('/v1/journal-entries' + (p.toString() ? '?' + p.toString() : ''));
  return r.data.data;
}
export async function getJournalEntry(id: string): Promise<JournalEntry> {
  const r = await api.get(`/v1/journal-entries/${id}`);
  return r.data.data;
}
export async function createJournalEntry(input: {
  entry_date: string;
  value_date?: string;
  entry_type?: 'manual' | 'adjustment';
  narration: string;
  lines: Array<{ account_code: string; debit?: string; credit?: string; narration?: string }>;
}): Promise<JournalEntry> {
  const r = await api.post('/v1/journal-entries', input);
  return r.data.data;
}
export async function approveJournalEntry(id: string): Promise<JournalEntry> {
  const r = await api.post(`/v1/journal-entries/${id}/approve`);
  return r.data.data;
}
export async function rejectJournalEntry(id: string, reason?: string): Promise<void> {
  await api.post(`/v1/journal-entries/${id}/reject`, { reason });
}

// Unified Inbox (PR #7): request a reversal of a posted journal
// entry. Backend creates an inverse-lines draft + a journal_reversal
// wf_instance (always Board). Returns the draft entry; the actual
// post fires on workflow approval via the resolve callback.
export async function reverseJournalEntry(id: string, input: {
  reversal_date?: string; // YYYY-MM-DD; defaults to today
  narration?: string;
} = {}): Promise<JournalEntry> {
  const r = await api.post(`/v1/journal-entries/${id}/reverse`, input);
  return r.data.data;
}

export type AccountingPeriod = {
  id: string;
  year: number;
  month: number;
  status: 'open' | 'closed';
  opened_at?: string | null;
  closed_at?: string | null;
  notes?: string | null;
};
export async function listAccountingPeriods(): Promise<{ items: AccountingPeriod[] }> {
  const r = await api.get('/v1/periods');
  return r.data.data;
}
export async function closeAccountingPeriod(id: string, notes?: string): Promise<void> {
  await api.post(`/v1/periods/${id}/close`, { notes });
}
export async function reopenAccountingPeriod(id: string, reason: string): Promise<void> {
  await api.post(`/v1/periods/${id}/reopen`, { reason });
}

export type TrialBalanceRow = {
  account_id: string;
  account_code: string;
  account_name: string;
  class: AccountClass;
  normal_balance: NormalBalance;
  opening_debit: string;
  opening_credit: string;
  period_debits: string;
  period_credits: string;
  closing_debit: string;
  closing_credit: string;
};
export async function trialBalance(from: string, to: string): Promise<{ from: string; to: string; items: TrialBalanceRow[]; total_debits: string; total_credits: string; balanced: boolean }> {
  const r = await api.get(`/v1/reports/trial-balance?from=${from}&to=${to}`);
  return r.data.data;
}

export type GLDetailRow = {
  entry_id: string;
  entry_no?: string | null;
  entry_date: string;
  narration: string;
  line_narration?: string | null;
  debit: string;
  credit: string;
  running_balance: string;
  source_module?: string | null;
  source_ref?: string | null;
};
export async function glDetail(accountId: string, from: string, to: string): Promise<{ account_id: string; from: string; to: string; items: GLDetailRow[] }> {
  const r = await api.get(`/v1/reports/gl-detail/${accountId}?from=${from}&to=${to}`);
  return r.data.data;
}

// ─────────── Balance Sheet + Income Statement (Phase 2) ───────────

export type BalanceSheetRow = {
  account_id?: string | null;
  account_code?: string;
  account_name: string;
  class: AccountClass;
  amount: string;
};

export async function balanceSheet(asOf?: string): Promise<{
  as_of: string;
  items: BalanceSheetRow[];
  total_assets: string;
  total_liabilities: string;
  total_equity: string;
  balanced: boolean;
}> {
  const q = asOf ? `?as_of=${asOf}` : '';
  const r = await api.get('/v1/reports/balance-sheet' + q);
  return r.data.data;
}

export type IncomeStatementRow = {
  account_id?: string | null;
  account_code?: string;
  account_name: string;
  class: AccountClass;
  amount: string;
};

export async function incomeStatement(from: string, to: string): Promise<{
  from: string;
  to: string;
  items: IncomeStatementRow[];
  total_income: string;
  total_expense: string;
  net_surplus: string;
}> {
  const r = await api.get(`/v1/reports/income-statement?from=${from}&to=${to}`);
  return r.data.data;
}

// ─────────── Loan loss provisioning ───────────

export type ProvisionRunStatus = 'pending' | 'computed' | 'posted' | 'failed' | 'superseded';

export type ProvisionRun = {
  id: string;
  tenant_id: string;
  as_of_date: string;
  status: ProvisionRunStatus;
  loans_classified: number;
  total_outstanding: string;
  total_provision: string;
  previous_provision: string;
  movement: string;
  journal_entry_ref?: string | null;
  notes?: string | null;
  computed_at?: string | null;
  posted_at?: string | null;
  posted_by?: string | null;
  created_at: string;
  created_by?: string | null;
  updated_at: string;
};

export type ProvisionRunLine = {
  id: string;
  run_id: string;
  loan_id: string;
  counterparty_id: string;
  loan_no: string;
  days_past_due: number;
  classification: 'performing' | 'watch' | 'substandard' | 'doubtful' | 'loss';
  outstanding: string;
  provision_rate: string;
  provision_amount: string;
  previous_classification?: string | null;
  previous_provision: string;
};

export async function listProvisionRuns(): Promise<{ items: ProvisionRun[]; total: number }> {
  const r = await api.get('/v1/provisioning/runs');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function getProvisionRun(id: string): Promise<{ run: ProvisionRun; lines: ProvisionRunLine[] }> {
  const r = await api.get(`/v1/provisioning/runs/${id}`);
  return r.data.data;
}

export async function createProvisionRun(input: { as_of_date: string; notes?: string }): Promise<ProvisionRun> {
  const r = await api.post('/v1/provisioning/runs', input);
  return r.data.data;
}

export async function postProvisionRun(id: string): Promise<ProvisionRun> {
  const r = await api.post(`/v1/provisioning/runs/${id}/post`);
  return r.data.data;
}

export async function supersedeProvisionRun(id: string): Promise<void> {
  await api.post(`/v1/provisioning/runs/${id}/supersede`);
}

// ─────────── Statement of Changes in Equity ───────────

export type ChangesInEquityRow = {
  account_id?: string | null;
  account_code?: string;
  account_name: string;
  account_type?: string;
  opening: string;
  increase: string;
  decrease: string;
  closing: string;
};

export async function changesInEquity(from: string, to: string): Promise<{
  from: string;
  to: string;
  items: ChangesInEquityRow[];
  total_opening: string;
  total_increase: string;
  total_decrease: string;
  total_closing: string;
  net_surplus: string;
}> {
  const r = await api.get(`/v1/reports/changes-in-equity?from=${from}&to=${to}`);
  return r.data.data;
}

// ─────────── Cash Flow Statement ───────────

export type CashFlowRow = {
  label: string;
  amount: string;
  account_codes?: string[];
};

export type CashFlowSection = {
  name: string;
  rows: CashFlowRow[];
  subtotal: string;
};

export async function cashFlow(from: string, to: string): Promise<{
  from: string;
  to: string;
  net_surplus: string;
  sections: CashFlowSection[];
  net_change_in_cash: string;
  opening_cash: string;
  closing_cash: string;
  reconciles: boolean;
}> {
  const r = await api.get(`/v1/reports/cash-flow?from=${from}&to=${to}`);
  return r.data.data;
}

// ─────────── Fiscal year close ───────────

export type FiscalYearClose = {
  id: string;
  tenant_id: string;
  year: number;
  fy_start: string;
  fy_end: string;
  closing_entry_id: string;
  total_income: string;
  total_expense: string;
  net_surplus: string;
  income_accounts: number;
  expense_accounts: number;
  closed_at: string;
  closed_by: string;
  notes?: string | null;
};

export async function listFiscalYearCloses(): Promise<{ items: FiscalYearClose[]; total: number }> {
  const r = await api.get('/v1/fiscal-years');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function closeFiscalYear(year: number, notes?: string): Promise<FiscalYearClose> {
  const r = await api.post(`/v1/fiscal-years/${year}/close`, notes ? { notes } : {});
  return r.data.data;
}

// Unified Inbox (PR #6): submit a year-end close for Board
// approval instead of executing it inline. Returns the workflow
// instance id; second call returns status='existing' when a pending
// proposal already exists for (tenant, year).
export type SubmitFiscalYearCloseResponse = {
  workflow_instance_id: string;
  proposal_id: string;
  status: 'created' | 'existing';
};

export async function submitFiscalYearForClose(year: number, notes?: string): Promise<SubmitFiscalYearCloseResponse> {
  const r = await api.post(`/v1/fiscal-years/${year}/submit-for-close`, notes ? { notes } : {});
  return r.data.data;
}

// Unified Inbox (PR #6): submit the bulk dormancy detector for
// Board approval. The actual status changes apply only on approval
// via the workflow callback.
export type SubmitDormancyForApprovalResponse = {
  workflow_instance_id: string;
  run_id: string;
  candidate_count: number;
  threshold_days: number;
  status: 'created';
};

export async function submitDormancyForApproval(): Promise<SubmitDormancyForApprovalResponse> {
  const r = await api.post('/v1/members/dormancy/submit-for-approval');
  return r.data.data;
}

// ─────────── Bank reconciliation ───────────

export type BankAccount = {
  id: string;
  tenant_id: string;
  gl_account_code: string;
  bank_name: string;
  account_number: string;
  branch?: string | null;
  currency_code: string;
  is_active: boolean;
  notes?: string | null;
  created_at: string;
  updated_at: string;
};

export type BankStatement = {
  id: string;
  bank_account_id: string;
  statement_date: string;
  period_start?: string | null;
  period_end?: string | null;
  opening_balance?: string | null;
  closing_balance?: string | null;
  total_debits: string;
  total_credits: string;
  line_count: number;
  source_format: string;
  source_filename?: string | null;
  uploaded_at: string;
  uploaded_by?: string | null;
};

export type BankStatementLine = {
  id: string;
  statement_id: string;
  bank_account_id: string;
  line_no: number;
  txn_date: string;
  value_date?: string | null;
  description?: string | null;
  reference?: string | null;
  debit: string;
  credit: string;
  running_balance?: string | null;
  match_status: 'unmatched' | 'matched' | 'manual_match' | 'excluded' | 'adjusted';
  matched_journal_line_id?: string | null;
  matched_at?: string | null;
  match_notes?: string | null;
};

export type MatchCandidate = {
  journal_line_id: string;
  entry_id: string;
  entry_no: string;
  entry_date: string;
  debit: string;
  credit: string;
  narration: string;
  source_module?: string | null;
  source_ref?: string | null;
};

export type ReconciliationReport = {
  bank_account_id: string;
  as_of: string;
  gl_account_code: string;
  gl_balance: string;
  statement_balance?: string | null;
  statement_date?: string | null;
  outstanding_bank_lines: BankStatementLine[];
  outstanding_gl_lines: {
    journal_line_id: string;
    entry_no: string;
    entry_date: string;
    debit: string;
    credit: string;
    narration: string;
  }[];
  outstanding_bank_credit: string;
  outstanding_bank_debit: string;
  outstanding_gl_debit: string;
  outstanding_gl_credit: string;
  adjusted_gl_balance: string;
  variance: string;
  reconciled: boolean;
};

export async function listBankAccounts(): Promise<{ items: BankAccount[]; total: number }> {
  const r = await api.get('/v1/bank-accounts');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function createBankAccount(input: {
  gl_account_code: string;
  bank_name: string;
  account_number: string;
  branch?: string;
  currency_code?: string;
  notes?: string;
}): Promise<BankAccount> {
  const r = await api.post('/v1/bank-accounts', input);
  return r.data.data;
}

export async function getBankAccount(id: string): Promise<BankAccount> {
  const r = await api.get(`/v1/bank-accounts/${id}`);
  return r.data.data;
}

export async function updateBankAccount(id: string, input: Partial<{
  bank_name: string; account_number: string; branch: string; currency_code: string; is_active: boolean; notes: string;
}>): Promise<BankAccount> {
  const r = await api.patch(`/v1/bank-accounts/${id}`, input);
  return r.data.data;
}

export async function listBankStatements(bankAccountID: string): Promise<{ items: BankStatement[]; total: number }> {
  const r = await api.get(`/v1/bank-accounts/${bankAccountID}/statements`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function uploadBankStatement(bankAccountID: string, csv: File): Promise<BankStatement> {
  const fd = new FormData();
  fd.append('file', csv);
  const r = await api.post(`/v1/bank-accounts/${bankAccountID}/statements`, fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return r.data.data;
}

export async function getBankStatement(id: string): Promise<{ statement: BankStatement; lines: BankStatementLine[] }> {
  const r = await api.get(`/v1/bank-statements/${id}`);
  return r.data.data;
}

export async function suggestMatchesForLine(lineID: string, toleranceDays = 5): Promise<{ items: MatchCandidate[]; total: number }> {
  const r = await api.get(`/v1/bank-statement-lines/${lineID}/suggest-matches?tolerance=${toleranceDays}`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function matchBankLine(lineID: string, journalLineID: string, opts?: { manual?: boolean; notes?: string }): Promise<BankStatementLine> {
  const r = await api.post(`/v1/bank-statement-lines/${lineID}/match`, {
    journal_line_id: journalLineID,
    manual: opts?.manual ?? false,
    notes: opts?.notes,
  });
  return r.data.data;
}

export async function unmatchBankLine(lineID: string): Promise<BankStatementLine> {
  const r = await api.post(`/v1/bank-statement-lines/${lineID}/unmatch`);
  return r.data.data;
}

export async function excludeBankLine(lineID: string, reason: string): Promise<BankStatementLine> {
  const r = await api.post(`/v1/bank-statement-lines/${lineID}/exclude`, { reason });
  return r.data.data;
}

export async function postBankAdjustment(lineID: string, input: {
  offset_account_code: string;
  narration?: string;
  notes?: string;
}): Promise<BankStatementLine> {
  const r = await api.post(`/v1/bank-statement-lines/${lineID}/post-adjustment`, input);
  return r.data.data;
}

export async function bankReconciliation(bankAccountID: string, asOf?: string): Promise<ReconciliationReport> {
  const q = asOf ? `?as_of=${asOf}` : '';
  const r = await api.get(`/v1/bank-accounts/${bankAccountID}/reconciliation${q}`);
  return r.data.data;
}

// ─────────── Cash & Float Management ───────────

export type Till = {
  id: string;
  code: string;
  name: string;
  branch?: string | null;
  gl_account_code: string;
  vault_account_code: string;
  variance_account_code: string;
  max_float?: string | null;
  is_active: boolean;
  notes?: string | null;
  created_at: string;
  updated_at: string;
};

export type TillSession = {
  id: string;
  till_id: string;
  teller_user_id: string;
  status: 'open' | 'closed';
  opening_float: string;
  expected_close: string;
  actual_close?: string | null;
  variance: string;
  variance_journal_entry_id?: string | null;
  opened_at: string;
  opened_by: string;
  closed_at?: string | null;
  closed_by?: string | null;
  notes?: string | null;
};

export type CashTransfer = {
  id: string;
  transfer_type: 'vault_to_till' | 'till_to_vault' | 'till_to_till' | 'opening_float' | 'closing_return' | 'variance_adjustment';
  from_till_id?: string | null;
  to_till_id?: string | null;
  session_id?: string | null;
  amount: string;
  reference?: string | null;
  narration?: string | null;
  journal_entry_id?: string | null;
  transferred_at: string;
  transferred_by: string;
};

export type CashPosition = {
  vault_balance: string;
  till_balance: string;
  variance_balance: string;
  grand_total: string;
  till_breakdown: {
    till_id: string;
    till_code: string;
    till_name: string;
    has_open_session: boolean;
    session_id?: string | null;
    teller_user_id?: string | null;
    expected_balance: string;
  }[];
};

export async function listTills(): Promise<{ items: Till[]; total: number }> {
  const r = await api.get('/v1/tills');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function createTill(input: {
  code: string; name: string; branch?: string; max_float?: string; notes?: string;
}): Promise<Till> {
  const r = await api.post('/v1/tills', input);
  return r.data.data;
}

export async function getTillDetail(id: string): Promise<{ till: Till; current_session?: TillSession | null }> {
  const r = await api.get(`/v1/tills/${id}`);
  return r.data.data;
}

export async function listTillSessions(tillID: string): Promise<{ items: TillSession[]; total: number }> {
  const r = await api.get(`/v1/tills/${tillID}/sessions`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function openTillSession(input: {
  till_id: string; teller_user_id: string; opening_float: string; notes?: string;
}): Promise<TillSession> {
  const r = await api.post('/v1/till-sessions', input);
  return r.data.data;
}

export async function getTillSession(id: string): Promise<{ session: TillSession; transfers: CashTransfer[] }> {
  const r = await api.get(`/v1/till-sessions/${id}`);
  return r.data.data;
}

// ─── Virtual tills (savings — non-cash channels) ────────────────────
//
// One row per (tenant, non-cash channel), auto-provisioned by the
// Collection Desk on first use of that channel. ≤5 rows per tenant
// total, so listVirtualTills + module-level cache in TillLabel is
// cheaper than a per-id getter.

export type VirtualTill = {
  id: string;
  tenant_id: string;
  channel: ReceiptChannel;
  gl_account_code: string;
  display_name: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
};

export async function listVirtualTills(): Promise<VirtualTill[]> {
  const r = await api.get('/v1/virtual-tills');
  return r.data.data.items ?? [];
}

// ─── Single-id share account (admin AccountRef resolver) ────────────
// Returns the bare ShareAccount only — for the rich view (liens,
// certificate, policy) use getShareAccountByMember.
export async function getShareAccount(id: string): Promise<ShareAccount> {
  const r = await api.get(`/v1/share-accounts/${id}`);
  return r.data.data;
}

export async function closeTillSession(id: string, actualClose: string, notes?: string): Promise<TillSession> {
  const r = await api.post(`/v1/till-sessions/${id}/close`, { actual_close: actualClose, notes });
  return r.data.data;
}

export async function createCashTransfer(input: {
  transfer_type: 'vault_to_till' | 'till_to_vault' | 'till_to_till';
  from_till_id?: string;
  to_till_id?: string;
  amount: string;
  reference?: string;
  narration?: string;
}): Promise<CashTransfer> {
  const r = await api.post('/v1/cash-transfers', input);
  return r.data.data;
}

export async function listCashTransfers(): Promise<{ items: CashTransfer[]; total: number }> {
  const r = await api.get('/v1/cash-transfers');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function getCashPosition(): Promise<CashPosition> {
  const r = await api.get('/v1/cash-position');
  return r.data.data;
}

// ─────────── Fixed Assets ───────────

export type AssetStatus = 'active' | 'disposed' | 'written_off' | 'fully_depreciated';
export type DepreciationMethod = 'straight_line' | 'none';

export type FixedAsset = {
  id: string;
  tenant_id: string;
  asset_no: string;
  name: string;
  description?: string | null;
  category: string;
  gl_asset_code: string;
  gl_accumulated_code: string;
  gl_expense_code: string;
  purchase_date: string;
  purchase_cost: string;
  salvage_value: string;
  useful_life_months: number;
  depreciation_method: DepreciationMethod;
  location?: string | null;
  custodian?: string | null;
  supplier?: string | null;
  invoice_ref?: string | null;
  acquisition_journal_entry_id?: string | null;
  status: AssetStatus;
  accumulated_depreciation: string;
  last_depreciation_date?: string | null;
  disposal_journal_entry_id?: string | null;
  disposal_proceeds?: string | null;
  disposal_gain_loss?: string | null;
  disposed_at?: string | null;
  notes?: string | null;
  created_at: string;
  updated_at: string;
};

export type DepreciationRun = {
  id: string;
  as_of_date: string;
  period_year: number;
  period_month: number;
  status: 'pending' | 'computed' | 'posted' | 'failed' | 'superseded';
  assets_processed: number;
  total_depreciation: string;
  journal_entry_id?: string | null;
  notes?: string | null;
  computed_at?: string | null;
  posted_at?: string | null;
  created_at: string;
};

export type DepreciationRunLine = {
  id: string;
  run_id: string;
  asset_id: string;
  asset_no: string;
  asset_name: string;
  category: string;
  method: string;
  cost: string;
  salvage: string;
  accumulated_before: string;
  depreciation_amount: string;
  accumulated_after: string;
  book_value_after: string;
  months_depreciated: number;
};

export async function listFixedAssets(opts?: { status?: string; category?: string }): Promise<{ items: FixedAsset[]; total: number }> {
  const q = new URLSearchParams();
  if (opts?.status) q.set('status', opts.status);
  if (opts?.category) q.set('category', opts.category);
  const r = await api.get(`/v1/fixed-assets${q.toString() ? '?' + q.toString() : ''}`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function createFixedAsset(input: {
  asset_no: string;
  name: string;
  description?: string;
  category: string;
  gl_asset_code: string;
  gl_accumulated_code?: string;
  gl_expense_code?: string;
  purchase_date: string;
  purchase_cost: string;
  salvage_value?: string;
  useful_life_months: number;
  depreciation_method?: DepreciationMethod;
  location?: string;
  custodian?: string;
  supplier?: string;
  invoice_ref?: string;
  funded_from_code?: string;
  notes?: string;
}): Promise<FixedAsset> {
  const r = await api.post('/v1/fixed-assets', input);
  return r.data.data;
}

export async function getFixedAsset(id: string): Promise<FixedAsset> {
  const r = await api.get(`/v1/fixed-assets/${id}`);
  return r.data.data;
}

export async function disposeFixedAsset(id: string, input: {
  disposal_date?: string;
  proceeds?: string;
  proceeds_account?: string;
  gain_account?: string;
  loss_account?: string;
  notes?: string;
}): Promise<FixedAsset> {
  const r = await api.post(`/v1/fixed-assets/${id}/dispose`, input);
  return r.data.data;
}

export async function listDepreciationRuns(): Promise<{ items: DepreciationRun[]; total: number }> {
  const r = await api.get('/v1/depreciation-runs');
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function createDepreciationRun(input: { as_of_date: string; notes?: string }): Promise<DepreciationRun> {
  const r = await api.post('/v1/depreciation-runs', input);
  return r.data.data;
}

export async function getDepreciationRun(id: string): Promise<{ run: DepreciationRun; lines: DepreciationRunLine[] }> {
  const r = await api.get(`/v1/depreciation-runs/${id}`);
  return r.data.data;
}

export async function postDepreciationRun(id: string): Promise<DepreciationRun> {
  const r = await api.post(`/v1/depreciation-runs/${id}/post`);
  return r.data.data;
}

// ─────────── Budgets ───────────

export type BudgetStatus = 'draft' | 'submitted' | 'approved' | 'archived';

export type Budget = {
  id: string;
  name: string;
  fiscal_year: number;
  period_start: string;
  period_end: string;
  status: BudgetStatus;
  total_income_budget: string;
  total_expense_budget: string;
  net_surplus_budget: string;
  notes?: string | null;
  submitted_at?: string | null;
  approved_at?: string | null;
  archived_at?: string | null;
  created_at: string;
  updated_at: string;
};

export type BudgetLine = {
  id: string;
  budget_id: string;
  account_id: string;
  account_code: string;
  account_class: 'income' | 'expense';
  period_month: number;
  amount: string;
  notes?: string | null;
};

export type VarianceRow = {
  account_id: string;
  account_code: string;
  account_name: string;
  account_class: 'income' | 'expense';
  budget: string;
  actual: string;
  variance: string;
  variance_pct: string;
  favourable: boolean;
};

export type VarianceReport = {
  budget_id: string;
  from: string;
  to: string;
  rows: VarianceRow[];
  total_income_budget: string;
  total_income_actual: string;
  total_expense_budget: string;
  total_expense_actual: string;
  net_surplus_budget: string;
  net_surplus_actual: string;
  net_surplus_variance: string;
};

export async function listBudgets(year?: number): Promise<{ items: Budget[]; total: number }> {
  const q = year ? `?year=${year}` : '';
  const r = await api.get(`/v1/budgets${q}`);
  return { items: r.data.data.items ?? [], total: r.data.data.total ?? 0 };
}

export async function createBudget(input: {
  name: string;
  fiscal_year: number;
  period_start?: string;
  period_end?: string;
  notes?: string;
}): Promise<Budget> {
  const r = await api.post('/v1/budgets', input);
  return r.data.data;
}

export async function getBudget(id: string): Promise<{ budget: Budget; lines: BudgetLine[] }> {
  const r = await api.get(`/v1/budgets/${id}`);
  return r.data.data;
}

export async function bulkUpsertBudgetLines(id: string, lines: { account_code: string; period_month: number; amount: string; notes?: string }[]): Promise<{ budget: Budget; lines: BudgetLine[] }> {
  const r = await api.post(`/v1/budgets/${id}/lines/bulk-upsert`, { lines });
  return r.data.data;
}

export async function submitBudget(id: string): Promise<Budget> {
  const r = await api.post(`/v1/budgets/${id}/submit`);
  return r.data.data;
}

export async function approveBudget(id: string): Promise<Budget> {
  const r = await api.post(`/v1/budgets/${id}/approve`);
  return r.data.data;
}

export async function archiveBudget(id: string): Promise<Budget> {
  const r = await api.post(`/v1/budgets/${id}/archive`);
  return r.data.data;
}

export async function budgetVariance(id: string, from: string, to: string): Promise<VarianceReport> {
  const r = await api.get(`/v1/budgets/${id}/variance?from=${from}&to=${to}`);
  return r.data.data;
}

// ─────────── SASRA Regulatory Return ───────────

export type SASRARatio = {
  label: string;
  numerator: string;
  denominator: string;
  ratio: string;
  threshold: string;
  operator: 'min' | 'max';
  compliant: boolean;
  notes?: string;
};

export type SASRAReturn = {
  as_of: string;
  fiscal_year: number;
  position: { total_assets: string; total_liabilities: string; total_equity: string };
  income_statement: {
    total_income: string;
    total_expense: string;
    net_surplus: string;
    from_date: string;
    to_date: string;
  };
  capital: {
    share_capital: string;
    retained_earnings: string;
    statutory_reserve: string;
    general_reserves: string;
    institutional_capital_acct: string;
    intangible_assets: string;
    core_capital: string;
    institutional_capital: string;
  };
  loan_portfolio: {
    gross_loans: string;
    interest_receivable: string;
    provisions: string;
    net_loans: string;
    provision_coverage_pct: string;
  };
  // PR 4: BOSA/FOSA split. The pre-PR-4 shape was
  // `{ member_savings, fixed_deposits, total }`; renamed so a
  // grep for `member_deposits_bosa` / `member_savings_fosa`
  // surfaces every consumer.
  deposits: {
    member_savings_fosa: string;
    member_deposits_bosa: string;
    total: string;
  };
  borrowings: string;
  liquid_assets: string;
  short_term_liabilities: string;
  ratios: SASRARatio[];
  all_compliant: boolean;
  warnings: { code: string; message: string }[];
};

export async function sasraReturn(asOf?: string): Promise<SASRAReturn> {
  const q = asOf ? `?as_of=${asOf}` : '';
  const r = await api.get('/v1/reports/sasra-return' + q);
  return r.data.data;
}

// ─────────── Management KPI Dashboard ───────────

export type DashboardKPIs = {
  total_assets: string;
  total_liabilities: string;
  total_equity: string;
  total_deposits: string;
  gross_loans: string;
  net_loans: string;
  provisions: string;
  cash_position: string;
  net_surplus_ytd: string;
  total_income_ytd: string;
  total_expense_ytd: string;
  core_capital: string;
  liquidity_ratio_pct: string;
  loan_to_deposit_ratio_pct: string;
  core_capital_ratio_pct: string;
  cost_to_income_ratio_pct: string;
  provision_coverage_pct: string;
};

export type MonthPoint = {
  month: string;
  total_assets: string;
  total_deposits: string;
  gross_loans: string;
  income: string;
  expense: string;
  net_surplus: string;
};

export type TopAccount = { code: string; name: string; amount: string };

export type Dashboard = {
  as_of: string;
  fiscal_year: number;
  kpis: DashboardKPIs;
  monthly_trend: MonthPoint[];
  top_income_ytd: TopAccount[];
  top_expense_ytd: TopAccount[];
};

export async function getFinanceDashboard(asOf?: string): Promise<Dashboard> {
  const q = asOf ? `?as_of=${asOf}` : '';
  const r = await api.get('/v1/reports/dashboard' + q);
  return r.data.data;
}

// ─────────── Member 360° Statement ───────────

export type MemberStatement = {
  counterparty_id: string;
  generated_at: string;
  member: {
    id: string;
    member_no: string;
    full_name: string;
    phone?: string | null;
    email?: string | null;
    status: string;
    joined_at?: string | null;
  };
  shares?: {
    account_id: string;
    shares_held: number;
    par_value: string;
    book_value: string;
    certificate_no?: string | null;
    certificate_issued_at?: string | null;
  } | null;
  deposits: {
    total_balance: string;
    account_count: number;
    accounts: {
      account_id: string;
      account_no: string;
      product_code: string;
      product_name: string;
      status: string;
      balance: string;
      available_balance: string;
      opened_at: string;
    }[];
  };
  loans: {
    total_loans_ever_taken: number;
    active_loans: number;
    total_disbursed: string;
    total_outstanding: string;
    loans: {
      loan: {
        id: string;
        loan_no: string;
        status: string;
        principal: string;
        principal_balance: string;
        interest_balance: string;
        days_past_due: number;
        arrears_classification: string;
        next_installment_due_at?: string | null;
        disbursed_at?: string | null;
        closed_at?: string | null;
      };
      product_code: string;
      product_name: string;
    }[];
  };
  recent_activity: {
    posted_at: string;
    module: 'shares' | 'deposits' | 'loans';
    type: string;
    txn_no: string;
    reference?: string;
    description: string;
    amount: string;
    narration?: string | null;
  }[];
  total_financial_position: string;
};

export async function getMemberStatement(memberID: string): Promise<MemberStatement> {
  const r = await api.get(`/v1/member-statements/${memberID}`);
  return r.data.data;
}

// ─────────── Report XLSX exports ───────────

// Reports are exported via /v1/exports/{report}.xlsx?...query params...
// The backend writes the binary XLSX directly to the response; we
// receive it as a Blob and trigger a browser download.
export async function downloadReport(report: string, query: Record<string, string> = {}): Promise<void> {
  const qs = new URLSearchParams(query).toString();
  const r = await api.get(`/v1/exports/${report}.xlsx${qs ? '?' + qs : ''}`, {
    responseType: 'blob',
  });
  // Extract filename from Content-Disposition header if present.
  let filename = `${report}.xlsx`;
  const dispo: string | undefined = r.headers['content-disposition'];
  if (dispo) {
    const m = dispo.match(/filename="?([^"]+)"?/);
    if (m) filename = m[1];
  }
  const blob = new Blob([r.data], {
    type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}
