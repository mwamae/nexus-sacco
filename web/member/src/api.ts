// Member-portal API client. Minimal — re-implemented locally so the
// member SPA stays free of admin-app dependencies. Auth lives in
// localStorage as 'member_jwt'.

import axios from 'axios';

export const api = axios.create({ baseURL: '/api' });

api.interceptors.request.use((cfg) => {
  const jwt = localStorage.getItem('member_jwt');
  if (jwt) cfg.headers = { ...cfg.headers, Authorization: `Bearer ${jwt}` };
  return cfg;
});

export async function memberLogin(memberNo: string, password: string): Promise<{ jwt: string; full_name: string }> {
  // Endpoint: identity service exposes /v1/auth/member/login in a
  // follow-up PR. For this scaffold we call /v1/auth/login (the
  // existing staff login) with member_no in the email slot — the
  // identity service treats it as an alias if the email isn't a
  // valid format. This works in dev when a member has been given a
  // user account.
  const r = await axios.post('/api/v1/auth/login', { email: memberNo, password });
  const jwt = r.data?.data?.access_token ?? r.data?.access_token;
  const fullName = r.data?.data?.user?.full_name ?? r.data?.user?.full_name ?? '';
  if (jwt) localStorage.setItem('member_jwt', jwt);
  return { jwt, full_name: fullName };
}

export function memberLogout() {
  localStorage.removeItem('member_jwt');
  window.location.assign('/');
}

export function hasSession(): boolean {
  return !!localStorage.getItem('member_jwt');
}

// Calculator math — pure function, mirrors the savings engine's
// flat-rate amortisation. Real engine uses reducing-balance + grace
// periods; this is a directional preview for the calculator UI only.
export function estimateInstallment(principal: number, ratePctPerAnnum: number, termMonths: number): {
  monthlyInstallment: number;
  totalInterest: number;
  totalPayable: number;
} {
  if (principal <= 0 || termMonths <= 0) return { monthlyInstallment: 0, totalInterest: 0, totalPayable: 0 };
  const monthlyRate = ratePctPerAnnum / 100 / 12;
  let monthly: number;
  if (monthlyRate === 0) {
    monthly = principal / termMonths;
  } else {
    monthly = (principal * monthlyRate) / (1 - Math.pow(1 + monthlyRate, -termMonths));
  }
  const totalPayable = monthly * termMonths;
  return {
    monthlyInstallment: Math.round(monthly * 100) / 100,
    totalInterest: Math.round((totalPayable - principal) * 100) / 100,
    totalPayable: Math.round(totalPayable * 100) / 100,
  };
}
