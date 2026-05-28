-- platform:operations:view — gates the platform-admin System Health
-- dashboard. The page moved off the tenant context (gated on
-- tenant:operations:view in migration 0023) because service health
-- is platform-wide, not tenant-scoped, and the on-call audience is
-- platform admins.
--
-- tenant:operations:view is left in the permission catalog
-- intentionally. It currently has no other callers (only the
-- legacy /v1/system-health route used it), but removing it would
-- require a destructive migration that revokes role grants on
-- live tenants; we'd rather leave one orphan permission than risk
-- breaking the role-permission audit trail for tenants that have
-- audited their RBAC. A follow-up can clean it up once we're sure
-- nothing in the wild references it.

INSERT INTO permissions (code, description, category)
VALUES ('platform:operations:view',
        'View the platform System Health dashboard',
        'operations')
ON CONFLICT (code) DO NOTHING;

-- Grant to platform_admin (the canonical platform-level role,
-- seeded in 0002 at id …001). Tenants don't get this permission;
-- platform admins also have the IsPlatformAdmin claim short-circuit
-- in RequirePermission so they'd bypass it anyway, but the explicit
-- grant keeps the audit clean and lets us flip the short-circuit
-- off in future without losing access.
INSERT INTO role_permissions (role_id, permission_code)
SELECT id, 'platform:operations:view'
  FROM roles
 WHERE code = 'platform_admin'
ON CONFLICT (role_id, permission_code) DO NOTHING;
