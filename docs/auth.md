# Authentication & Identity

The identity service is the single source of truth for tenants, users, roles, permissions, and sessions across nexusSacco. All other services receive their tenant + user context by validating JWTs that identity issues — no service-to-service callbacks per request.

## Multi-tenancy

- **Isolation**: shared Postgres database, `tenant_id` column on every tenant-scoped table, Postgres Row-Level Security policies enforce isolation at the DB layer.
- **Runtime role**: the app connects as a non-superuser role (`nexus_app`) created by migration 0001. Superusers bypass RLS even when `FORCE` is on; downgrading via `SET ROLE nexus_app` on every connection keeps the policies effective.
- **Tenant binding**: every authenticated request opens a transaction, sets `app.tenant_id` via `set_config(..., true)` (LOCAL), and runs queries inside it. RLS policies check `tenant_id = current_tenant_id()`. If a query forgets the `WHERE tenant_id =` clause, RLS still silently filters it.
- **Tenant resolution**: backend extracts the leftmost subdomain of the `Host` header and looks it up in `tenants`. Reserved subdomains (`www`, `api`, `platform`, `admin`, `app`, empty) are treated as the platform host.

## Platform vs tenant roles

A reserved pseudo-tenant `slug='platform'` holds platform super-admins (users with `is_platform_admin = true`, granted the `platform_admin` system role). They authenticate on the platform host (e.g. `platform.nexussacco.local`) and can:

- create / list tenants
- bootstrap the initial owner user of each tenant

Tenant users authenticate on their tenant subdomain and never see anything outside it.

## Token model

| | Algorithm | TTL | Storage |
|---|---|---|---|
| Access | HS256 JWT | 15 min (configurable) | Stateless; carries `tid`, `tslug`, `sub`, `email`, `name`, `roles`, `perms`, `platform` |
| Refresh | Opaque 32-byte random | 720 h | Hashed (SHA-256) in `refresh_tokens` |

- Refresh tokens are rotated on every use. `parent_id` links the chain.
- **Theft detection**: presenting an already-revoked refresh token revokes the entire descendant chain (`refresh_tokens.revoked_reason = 'reuse_detected'`). The legitimate user gets logged out and must sign in again; the attacker's still-valid child token stops working.
- Passwords are hashed with Argon2id (64 MiB, 3 iterations, 4 lanes; tunable per-deployment).

## Two-factor authentication

Email-based 6-digit OTPs.

- A user with `mfa_enabled=true` + `mfa_method='email'` cannot complete `/auth/login` with just a password. Instead they receive `{ mfa_required: true, mfa_token, mfa_expires_at, delivery_hint }` and an email with the code.
- The client submits `{ mfa_token, code }` to **POST `/v1/auth/mfa/verify`** to receive the real access + refresh tokens.
- Challenge lifetime: 10 minutes. Maximum wrong-code attempts per challenge: 5 (after which the challenge is locked).
- Codes and the opaque `mfa_token` are hashed (SHA-256) at rest in `mfa_challenges`; only the recipient sees the raw code via email.
- Replay protection: every challenge has a single `used_at` write; the same `mfa_token` cannot be used twice.
- The token returned from `/auth/login` for an MFA-required user is **not** an access token — it cannot be sent in `Authorization: Bearer`.

### Enable / disable

- **POST `/v1/auth/mfa/email/enable`** (authenticated, no body) → emails a confirmation code, returns `{ mfa_token }`.
- **POST `/v1/auth/mfa/email/enable/confirm`** `{ mfa_token, code }` → flips `users.mfa_enabled = true`, `users.mfa_method = 'email'`.
- **POST `/v1/auth/mfa/disable`** `{ password }` → requires the user to re-enter their password before clearing the MFA fields.

### Email (SMTP)

The identity service speaks plain SMTP. In dev we point it at [MailHog](https://github.com/mailhog/MailHog) (no auth, inbox at http://localhost:8025). Config:

```
SMTP_HOST=localhost
SMTP_PORT=1025
SMTP_FROM=no-reply@nexussacco.local
SMTP_FROM_NAME=nexusSacco
SMTP_USE_TLS=false          # set true + provide SMTP_USER/SMTP_PASSWORD for production
```

If `SMTP_HOST` is empty, email features fall back to logging the code (development-only safety net; never enable in prod).

### Audit events

- `user.login.success` — first-factor + (if MFA disabled) tokens issued
- `user.login.failed` — bad password
- `user.login.mfa_required` — first factor OK, challenge issued
- `user.login.mfa_verified` — challenge verified, tokens issued
- `user.mfa.enabled` / `user.mfa.disabled`

## Endpoints

All endpoints prefixed `/v1`. Request bodies are JSON; responses are `{"data": ...}` for success and `{"error": {"code": "...", "message": "..."}}` for errors.

| Method | Path | Host | Auth | Purpose |
|---|---|---|---|---|
| GET | `/healthz` | any | none | Health check |
| POST | `/auth/login` | tenant or platform | none | Email + password → access + refresh tokens |
| POST | `/auth/refresh` | tenant or platform | none | Rotate refresh token, issue new access |
| POST | `/auth/logout` | tenant or platform | none | Revoke refresh token |
| GET | `/auth/me` | tenant or platform | bearer | Current user + tenant + perms |
| GET | `/tenant` | tenant | bearer | Current tenant |
| GET | `/users` | tenant | bearer + `users:view` | List users |
| POST | `/users` | tenant | bearer + `users:invite` | Create / invite a user |
| GET | `/roles` | tenant | bearer + `roles:view` | List assignable roles |
| GET | `/platform/tenants` | platform | bearer (platform admin) | List all tenants |
| POST | `/platform/tenants` | platform | bearer (platform admin) | Create tenant + initial owner |
| POST | `/auth/mfa/verify` | tenant or platform | none | Submit OTP for an issued mfa_token |
| POST | `/auth/mfa/email/enable` | tenant or platform | bearer | Begin enabling email 2FA |
| POST | `/auth/mfa/email/enable/confirm` | tenant or platform | bearer | Confirm with OTP, flip mfa_enabled |
| POST | `/auth/mfa/disable` | tenant or platform | bearer + password | Disable 2FA |

### Login

```http
POST /v1/auth/login
Host: tujenge.nexussacco.local
Content-Type: application/json

{ "email": "owner@tujenge.test", "password": "TujengeSecure123!" }
```

Response (`200 OK`):

```json
{
  "data": {
    "access_token": "eyJhbGc...",
    "token_type": "Bearer",
    "expires_at": "2026-05-19T16:08:20Z",
    "refresh_token": "vAJTqCIp...",
    "refresh_expires_at": "2026-06-18T15:53:20Z",
    "user":   { "id": "...", "email": "owner@tujenge.test", ... },
    "tenant": { "id": "...", "slug": "tujenge", "name": "Tujenge SACCO Society", ... }
  }
}
```

Failures:
- `401 unauthorized` — bad credentials or non-existent email (timing-safe; we still spend CPU on a dummy hash to avoid leaking existence)
- `403 forbidden` — account locked (10 consecutive failures → 15-minute lockout) or status not `active`

### Refresh

```http
POST /v1/auth/refresh
Host: tujenge.nexussacco.local
Content-Type: application/json

{ "refresh_token": "vAJTqCIp..." }
```

On success the old token is marked `revoked_reason='rotated'` and a new pair is returned. On replay of a revoked token, `401 unauthorized` and the chain is revoked.

### Platform: create tenant

```http
POST /v1/platform/tenants
Host: platform.nexussacco.local
Authorization: Bearer <platform-admin token>
Content-Type: application/json

{
  "slug": "tujenge",
  "name": "Tujenge SACCO Society",
  "kind": "sacco",
  "owner_email": "owner@tujenge.test",
  "owner_name": "Tujenge Owner",
  "owner_password": "TujengeSecure123!"
}
```

Returns `201 Created` with the new tenant and the owner user.

## Permissions

System permissions are listed in `migrations/0002_seed_rbac.up.sql`. The default role → permission mapping:

| Role | Permissions |
|---|---|
| `platform_admin` | all (filtered to `platform:*` in practice) |
| `tenant_owner` | everything except `platform:*` |
| `sacco_admin` | tenant settings, users, roles, member approval, view of savings/loans/collections/accounting, reports, audit |
| `branch_manager` | member & savings approve, loan approve, view ops, reports |
| `credit_officer` | originate/underwrite/approve loans, collections |
| `teller` | members create/view, savings transact, loans view |
| `accountant` | accounting view/post/close, reports |
| `auditor` | read-only across operational data + audit |
| `collections_officer` | members/loans view, collections act |
| `member` | self-service only (member-facing endpoints, not in admin) |

## Audit log

`audit_log` records `user.login.success`, `user.login.failed`, `tenant.created`, `user.invited` (more as services come online). It is append-only and **not** under RLS — `tenant_id` is just a column. The `audit:view` permission gates the (future) read endpoint.

## Local dev cheat sheet

```bash
# /etc/hosts
127.0.0.1  nexussacco.local platform.nexussacco.local tujenge.nexussacco.local

# Bring up postgres + identity
make up

# First-time DB setup
make migrate
make seed   # creates the platform pseudo-tenant + super-admin from .env

# Verify
curl -s http://localhost:8081/healthz
```

Open the admin web:

```bash
make web-dev
# http://platform.nexussacco.local:5173/ → platform admin
# http://tujenge.nexussacco.local:5173/  → tenant admin (after creating via platform)
```
