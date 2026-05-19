// Extract the tenant slug from the browser's current host.
// Mirrors the backend's reserved-subdomain logic in middleware/tenant.go.

const RESERVED = new Set(['', 'www', 'api', 'platform', 'admin', 'app']);

export function tenantSlugFromHost(host: string, appDomain: string): string | null {
  const h = host.toLowerCase().replace(/:.*$/, '');
  const d = appDomain.toLowerCase();
  if (!h || h === d) return null;
  const suffix = '.' + d;
  if (!h.endsWith(suffix)) {
    // localhost / ip / unknown — treat as platform
    return null;
  }
  const sub = h.slice(0, -suffix.length).split('.')[0];
  if (RESERVED.has(sub)) return null;
  return sub;
}

export function currentTenantSlug(): string | null {
  const appDomain = (import.meta.env.VITE_APP_DOMAIN as string | undefined) ?? 'nexussacco.local';
  return tenantSlugFromHost(window.location.host, appDomain);
}

export function isPlatformHost(): boolean {
  return currentTenantSlug() === null;
}
