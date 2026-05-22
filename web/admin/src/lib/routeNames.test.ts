// Verifies the route-name registry never lets a raw URL path leak
// into the header. Every route reachable from App.tsx must resolve to
// a human label — that's the contract that the original bug
// ("/loans" rendered verbatim in the breadcrumb) violated.

import { describe, it, expect } from 'vitest';
import { trailFor, humanizeSegment, ROUTES } from './routeNames';

// Mirrors every routable path in App.tsx — both the literal sidebar
// destinations and one representative instance for each dynamic
// shape. Update this list (and routeNames.ts) when a new route lands.
const STATIC_PATHS: string[] = [
  '/',
  '/users',
  '/roles',
  '/applications',
  '/applications/new',
  '/members',
  '/members/new',
  '/orgs',
  '/orgs/new',
  '/shares',
  '/deposits',
  '/deposit-products',
  '/loans',
  '/loan-products',
  '/loan-reports',
  '/collections',
  '/provisioning',
  '/interest-runs',
  '/dividend-runs',
  '/cash-approvals',
  '/approvals',
  '/workflows',
  '/notifications',
  '/credits',
  '/campaigns',
  '/notification-templates',
  '/scheduled-jobs',
  '/settings',
  '/accounting/dashboard',
  '/accounting/chart-of-accounts',
  '/accounting/journal-entries',
  '/accounting/trial-balance',
  '/accounting/balance-sheet',
  '/accounting/income-statement',
  '/accounting/changes-in-equity',
  '/accounting/cash-flow',
  '/accounting/year-end-close',
  '/accounting/sasra-return',
  '/bank-accounts',
  '/cash-management',
  '/fixed-assets',
  '/budgets',
  '/platform/credits',
  '/tenants/new',
];

// Dynamic paths exercised at least once per registered match() so the
// fallback suffix kicks in for paths without a supplied dynamic crumb.
const DYNAMIC_PATHS: string[] = [
  '/applications/abc-123',
  '/members/abc-123',
  '/members/abc-123/statement',
  '/orgs/abc-123',
  '/shares/abc-123',
  '/deposits/abc-123',
  '/loans/abc-123',
  '/collections/abc-123',
  '/loan-reports/abc-123',
  '/provisioning/abc-123',
  '/interest-runs/abc-123',
  '/dividend-runs/abc-123',
  '/cash-approvals/abc-123',
  '/approvals/abc-123',
  '/workflows/abc-123',
  '/notifications/abc-123',
  '/credits/abc-123',
  '/campaigns/abc-123',
  '/notification-templates/abc-123',
  '/scheduled-jobs/abc-123',
  '/accounting/journal-entries/abc-123',
  '/bank-accounts/abc-123',
  '/fixed-assets/abc-123',
  '/budgets/abc-123',
  '/budgets/abc-123/variance',
  '/platform/credits/abc-123',
  '/tenants/abc-123',
  '/deposit-products/abc-123',
  '/loan-products/abc-123',
];

// The cardinal property: nothing the registry returns may start with
// a slash. If a new route ships without a registry entry the catch-all
// humaniser still wins ("loan-reports" → "Loan reports"), but a literal
// "/loan-reports" would be a regression and this test would fail.
function assertNoRawPath(trail: string[]) {
  for (const seg of trail) {
    expect(seg).not.toMatch(/^\//);
    expect(seg).not.toMatch(/\s\//);
  }
}

describe('routeNames registry', () => {
  it.each(STATIC_PATHS)('static path %s resolves to a human trail', (p) => {
    const trail = trailFor(p);
    expect(trail.length).toBeGreaterThan(0);
    assertNoRawPath(trail);
  });

  it.each(DYNAMIC_PATHS)('dynamic path %s resolves with a fallback suffix', (p) => {
    const trail = trailFor(p);
    expect(trail.length).toBeGreaterThan(0);
    assertNoRawPath(trail);
  });

  it('a dynamic-crumb suffix replaces the registry fallback', () => {
    expect(trailFor('/members/abc-123', 'Jane Doe')).toEqual(['Members', 'Jane Doe']);
    expect(trailFor('/orgs/x', 'Acme SACCO')).toEqual(['Organisations', 'Acme SACCO']);
    expect(trailFor('/applications/x', 'APP-2026-000004 · Smoke Test')).toEqual(
      ['Member onboarding', 'APP-2026-000004 · Smoke Test'],
    );
  });

  it('the four explicitly-cited bug paths render their intended labels', () => {
    expect(trailFor('/loans')).toEqual(['Lending', 'Loans']);
    expect(trailFor('/deposits')).toEqual(['Deposits']);
    expect(trailFor('/shares')).toEqual(['Shares']);
    // Bonus: a totally unregistered path doesn't leak either.
    expect(trailFor('/totally-new-feature')).toEqual(['Totally New Feature']);
  });

  it('humanizeSegment turns kebab-case into Title Case', () => {
    expect(humanizeSegment('loan-reports')).toBe('Loan Reports');
    expect(humanizeSegment('fixed-assets')).toBe('Fixed Assets');
    expect(humanizeSegment('')).toBe('');
  });

  it('every route in the registry has a non-empty trail / base', () => {
    for (const r of ROUTES) {
      if (r.kind === 'static') {
        expect(r.trail.length).toBeGreaterThan(0);
      } else {
        expect(r.baseTrail.length).toBeGreaterThan(0);
      }
    }
  });
});
