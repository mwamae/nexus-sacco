// Two-pane shell — sidebar nav on the left, topbar above the main area.
// On a tenant subdomain we fetch the tenant's branding once and apply
// the logo + primary color live so admins see their changes immediately.

import { Fragment, useEffect, useState, type ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';
import { isPlatformHost } from '../auth/tenant';
import { fetchTenantLogo, getTenantSettings } from '../api/client';
import { trailFor } from '../lib/routeNames';
import { usePageCrumbValue } from '../lib/pageCrumb';
import { Avatar } from './Avatar';
import { Icon, type IconName } from './Icon';
import { NotificationBell } from './NotificationBell';
import { CreditBanner } from './CreditBanner';

type NavItem = {
  href: string;
  label: string;
  icon: IconName;
  // show=true → render as a normal link.
  // show=false + lockedHint set → render disabled, with a lock icon and
  //   tooltip explaining the missing permission. Use this for items
  //   where it's better for the user to know the feature exists than to
  //   silently hide it.
  // show=false + no lockedHint → omit from the nav entirely (default).
  show: boolean;
  lockedHint?: string;
};

type NavGroup = {
  section: string;
  items: NavItem[];
};

export default function AppShell({ children }: { children: ReactNode }) {
  const { user, tenant, logout, hasPermission, roles } = useAuth();
  const path = window.location.pathname;
  const onPlatform = isPlatformHost();

  const branding = useTenantBranding(!onPlatform && hasPermission('tenant:settings:view'));

  const groups: NavGroup[] = [
    {
      section: 'Overview',
      items: [
        { href: '/', label: 'Home', icon: 'home', show: true },
      ],
    },
    {
      section: 'Servicing',
      items: [
        { href: '/applications', label: 'Applications', icon: 'user', show: hasPermission('members:view') && !onPlatform },
        { href: '/members', label: 'Members', icon: 'user', show: hasPermission('members:view') && !onPlatform },
        { href: '/orgs', label: 'Organisations', icon: 'building', show: hasPermission('members:view') && !onPlatform },
        { href: '/shares', label: 'Shares', icon: 'bank', show: hasPermission('shares:view') && !onPlatform },
        { href: '/deposits', label: 'Deposits', icon: 'bank', show: hasPermission('savings:view') && !onPlatform },
        { href: '/loans', label: 'Loans', icon: 'bank', show: hasPermission('loans:view') && !onPlatform },
        { href: '/collections', label: 'Collections', icon: 'bell', show: hasPermission('collections:view') && !onPlatform },
        { href: '/loan-reports', label: 'Loan reports', icon: 'chart', show: hasPermission('loans:view') && !onPlatform },
        { href: '/provisioning', label: 'Provisioning', icon: 'chart', show: hasPermission('loans:view') && !onPlatform },
        { href: '/cash-approvals', label: 'Cash approvals', icon: 'check', show: hasPermission('approvals:view') && !onPlatform },
        { href: '/interest-runs', label: 'Interest runs', icon: 'refresh', show: hasPermission('interest:view') && !onPlatform },
        { href: '/dividend-runs', label: 'Dividend runs', icon: 'refresh', show: hasPermission('dividends:view') && !onPlatform },
      ],
    },
    {
      section: 'Approvals',
      items: [
        { href: '/approvals', label: 'Inbox', icon: 'check', show: hasPermission('workflow:view') && !onPlatform },
        { href: '/workflows', label: 'Definitions', icon: 'settings', show: hasPermission('workflow:configure') && !onPlatform },
      ],
    },
    {
      section: 'Finance',
      items: [
        { href: '/accounting/dashboard',         label: 'Dashboard',         icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/chart-of-accounts', label: 'Chart of Accounts', icon: 'bank',  show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/journal-entries',   label: 'Journal Entries',   icon: 'bank',  show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/trial-balance',     label: 'Trial Balance',     icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/balance-sheet',     label: 'Balance Sheet',     icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/income-statement',  label: 'Income Statement',  icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/changes-in-equity', label: 'Changes in Equity', icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/cash-flow',         label: 'Cash Flow',         icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/year-end-close',    label: 'Year-end close',    icon: 'check', show: !onPlatform && hasPermission('tenant:settings:edit') },
        { href: '/bank-accounts',                label: 'Bank reconciliation', icon: 'bank', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/cash-management',              label: 'Cash & float',      icon: 'bank',  show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/fixed-assets',                 label: 'Fixed assets',      icon: 'bank',  show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/budgets',                      label: 'Budgets & variance', icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/accounting/sasra-return',      label: 'SASRA return',      icon: 'chart', show: !onPlatform && hasPermission('tenant:settings:view') },
      ],
    },
    {
      section: 'Engagement',
      items: [
        { href: '/notifications', label: 'Notifications', icon: 'bell', show: !onPlatform },
        { href: '/credits', label: 'Credits', icon: 'bank', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/campaigns', label: 'Campaigns', icon: 'bell', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/notification-templates', label: 'Templates', icon: 'settings', show: !onPlatform && hasPermission('tenant:settings:view') },
        { href: '/scheduled-jobs', label: 'Scheduled jobs', icon: 'refresh', show: !onPlatform && hasPermission('tenant:settings:view') },
      ],
    },
    {
      section: 'Administration',
      items: [
        { href: '/users', label: 'Staff', icon: 'users', show: hasPermission('users:view') },
        { href: '/roles', label: 'Roles & permissions', icon: 'key', show: hasPermission('roles:view') },
        { href: '/deposit-products', label: 'Deposit products', icon: 'settings', show: !onPlatform && hasPermission('deposits:configure') },
        { href: '/loan-products', label: 'Loan products', icon: 'settings', show: !onPlatform && hasPermission('loans:configure') },
        { href: '/settings', label: 'Settings', icon: 'settings', show: !onPlatform && hasPermission('tenant:settings:view') },
      ],
    },
  ];

  if (onPlatform && user?.is_platform_admin) {
    groups.push({
      section: 'Platform',
      items: [
        // Single entry — Tenants list, credit operations, and shared
        // driver configuration are all tabs within the dashboard.
        { href: '/', label: 'Tenants & credits', icon: 'building', show: true },
      ],
    });
  }

  const dynamicCrumb = usePageCrumbValue();
  const crumbs = [
    tenant?.name ?? (onPlatform ? 'Platform' : 'Tenant'),
    ...trailFor(path, dynamicCrumb),
  ];
  const primaryRole = roles.find((r) => r !== 'platform_admin') ?? roles[0] ?? 'staff';

  return (
    <div className="app" style={branding.fontFamily ? { fontFamily: branding.fontFamily } : undefined}>
      <aside className="sidebar">
        <div className="sb-brand">
          {branding.logoURL ? (
            <img
              src={branding.logoURL}
              alt={tenant?.name ?? 'Logo'}
              style={{ width: 22, height: 22, objectFit: 'contain', borderRadius: 5 }}
            />
          ) : (
            <div className="sb-brand-mark">N</div>
          )}
          <div className="sb-brand-name">{tenant?.name ?? 'nexusSacco'}</div>
          <span className="sb-brand-tag">v1</span>
        </div>

        <div className="sb-tenant">
          <div className="sb-tenant-mark">{(tenant?.slug ?? 'P').charAt(0).toUpperCase()}</div>
          <div className="sb-tenant-info">
            <div className="sb-tenant-name">{tenant?.name ?? 'Platform'}</div>
            <div className="sb-tenant-sub">{tenant?.slug ?? 'platform'} · {tenant?.currency_code ?? '—'}</div>
          </div>
          <Icon name="chevron_dn" size={12} />
        </div>

        <nav className="sb-nav">
          {groups.map((g) => {
            // Render an item if it's visible OR if it's locked-with-hint.
            // Plain hidden items (show=false, no hint) are filtered out.
            const renderable = g.items.filter((i) => i.show || i.lockedHint);
            if (renderable.length === 0) return null;
            return (
              <Fragment key={g.section}>
                <div className="sb-section">{g.section}</div>
                {renderable.map((item) => {
                  const active =
                    item.href === '/' ? path === '/' : path === item.href || path.startsWith(item.href + '/');
                  if (!item.show && item.lockedHint) {
                    return (
                      <span
                        key={item.href}
                        className="sb-item"
                        title={item.lockedHint}
                        aria-disabled="true"
                        style={{ opacity: 0.55, cursor: 'not-allowed' }}
                      >
                        <span className="sb-item-ico"><Icon name={item.icon} size={14} /></span>
                        <span>{item.label}</span>
                        <span className="sb-item-ico" style={{ marginLeft: 'auto' }}>
                          <Icon name="key" size={12} />
                        </span>
                      </span>
                    );
                  }
                  return (
                    <a
                      key={item.href}
                      href={item.href}
                      className="sb-item"
                      data-active={active || undefined}
                    >
                      <span className="sb-item-ico"><Icon name={item.icon} size={14} /></span>
                      <span>{item.label}</span>
                    </a>
                  );
                })}
              </Fragment>
            );
          })}
        </nav>

        <div className="sb-user">
          <Avatar name={user?.full_name ?? user?.email ?? '?'} size="sm" />
          <div className="sb-user-info">
            <div className="sb-user-name">{user?.full_name}</div>
            <div className="sb-user-role">{primaryRole}</div>
          </div>
          <button
            className="tb-icon-btn"
            title="Sign out"
            onClick={() => void logout()}
          >
            <Icon name="logout" size={14} />
          </button>
        </div>
      </aside>

      <header className="topbar">
        <div className="tb-crumbs">
          {crumbs.map((c, i) => (
            <Fragment key={i}>
              {i > 0 && <Icon name="chevron_r" size={11} />}
              <span className={i === crumbs.length - 1 ? 'tb-crumb-active' : ''}>{c}</span>
            </Fragment>
          ))}
        </div>
        <div className="spacer" />
        <NotificationBell />
        <div className="tb-status" style={{ marginLeft: 12 }}>
          <span className="tb-status-dot" />
          <span>{user?.email}</span>
        </div>
      </header>

      <main className="main">
        <CreditBanner enabled={!onPlatform && hasPermission('tenant:settings:view')} />
        {children}
      </main>
    </div>
  );
}

type BrandingState = {
  logoURL: string | null;
  primaryColor: string | null;
  fontFamily: string | null;
};

/** Loads tenant branding once and applies it. Side-effects:
 *    * sets --accent + --accent-fg CSS vars on documentElement
 *    * resolves the logo bytes into a blob URL for inline rendering
 *  Returns the resolved values so the AppShell can render them too. */
function useTenantBranding(enabled: boolean): BrandingState {
  const [state, setState] = useState<BrandingState>({ logoURL: null, primaryColor: null, fontFamily: null });

  useEffect(() => {
    if (!enabled) return;
    let revoked = false;
    let objectUrl: string | null = null;

    void (async () => {
      try {
        const settings = await getTenantSettings();
        const b = settings.branding;

        // Apply colors as CSS vars on the document root. Reverted on unmount
        // so the Tweaks panel still wins when the user navigates away.
        if (b.primary_color) {
          document.documentElement.style.setProperty('--accent', b.primary_color);
        }

        let logoURL: string | null = null;
        if (b.has_logo) {
          const blob = await fetchTenantLogo();
          if (blob && !revoked) {
            objectUrl = URL.createObjectURL(blob);
            logoURL = objectUrl;
          }
        }
        if (revoked) return;
        setState({
          logoURL,
          primaryColor: b.primary_color || null,
          fontFamily: b.font_family || null,
        });
      } catch {
        // Branding is best-effort; ignore failures so the shell still renders.
      }
    })();

    return () => {
      revoked = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      document.documentElement.style.removeProperty('--accent');
    };
    // We intentionally only react to `enabled` changing — tenant id is
    // implicit via the request host and never changes within a session.
  }, [enabled]);

  return state;
}

// Breadcrumb resolution now lives in src/lib/routeNames.ts so all
// routes (sidebar-anchored and dynamic alike) get a sane label —
// including the ones that used to fall through to `trail.push(path)`
// and render "/loans" or "/deposits" verbatim in the header.
