// Officer-PWA API client. Uses the same JWT login as the staff admin
// app — officers sign in with their staff credentials. Stored under
// 'officer_jwt' to keep separate from any other apps on the same
// device.

import axios from 'axios';

export const api = axios.create({ baseURL: '/api' });

api.interceptors.request.use((cfg) => {
  const jwt = localStorage.getItem('officer_jwt');
  if (jwt) cfg.headers = { ...cfg.headers, Authorization: `Bearer ${jwt}` };
  return cfg;
});

export async function officerLogin(email: string, password: string): Promise<string> {
  const r = await axios.post('/api/v1/auth/login', { email, password });
  const jwt = r.data?.data?.access_token ?? r.data?.access_token;
  if (jwt) localStorage.setItem('officer_jwt', jwt);
  return jwt;
}

export function officerLogout() {
  localStorage.removeItem('officer_jwt');
  window.location.assign('/');
}

export function hasSession(): boolean {
  return !!localStorage.getItem('officer_jwt');
}

// Queue — re-uses the existing /v1/loans/collections/queue endpoint
// from Phase 4. Filter on officer_id = me (server-side).
export type QueueRow = {
  loan_id: string;
  loan_no: string;
  member_name: string;
  outstanding_total: string;
  dpd_days: number;
  classification: string;
  last_event_kind?: string | null;
  last_event_at?: string | null;
};

export async function myQueue(officerID: string): Promise<QueueRow[]> {
  const r = await api.get(`/v1/loans/collections/queue?officer_id=${officerID}`);
  return r.data?.data?.items ?? [];
}

export async function logCall(loanID: string, outcome: string, note: string): Promise<void> {
  await api.post(`/v1/loans/${loanID}/collections/calls`, { outcome, note });
}

export async function logVisit(loanID: string, outcome: string, note: string, geoLat?: string, geoLng?: string): Promise<void> {
  await api.post(`/v1/loans/${loanID}/collections/visits`, { outcome, note, geo_lat: geoLat, geo_lng: geoLng });
}
