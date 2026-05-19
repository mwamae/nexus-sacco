# nexusSacco

Multi-tenant enterprise SACCO management platform. Go microservices, React/TS frontend, Postgres.

## Architecture

Tenancy: **shared Postgres database + `tenant_id` column on every tenant-scoped table + Row-Level Security**. Tenants are resolved from the request **subdomain** (e.g. `tujenge.nexussacco.app` → tenant `tujenge`). A reserved `platform.` subdomain (or `admin.`) hosts cross-tenant platform-admin operations.

Services (Phase 1, additive):

| Service | Port | Purpose |
|---|---|---|
| `identity` | 8081 | Tenants, users, RBAC, login, MFA, sessions, audit |
| _member_ | 8082 | Member directory, KYC (next) |
| _savings_ | 8083 | Deposit accounts, interest (next) |
| _loan_ | 8084 | LOS, origination, servicing (next) |
| _payment_ | 8085 | M-Pesa Daraja, bank rails (next) |
| _accounting_ | 8086 | GL, journal, posting engine (next) |

Each service owns its tables and exposes HTTP. Inter-service auth uses validated JWT (issued by `identity`) — services share a small Go `auth` package that verifies tokens and extracts `tenant_id` + `user_id` + permissions.

## Layout

```
nexusSacco/
├── services/
│   └── identity/             # auth service (this milestone)
│       ├── cmd/server/       # main entry point
│       └── internal/
│           ├── config/       # env-based config
│           ├── db/           # pgx pool + embedded migrations
│           ├── domain/       # business entities (Tenant, User, Role…)
│           ├── store/        # data access (one file per aggregate)
│           ├── auth/         # password (argon2id), JWT, refresh tokens
│           ├── middleware/   # tenant resolution, JWT auth, logging
│           ├── httpx/        # response/error helpers
│           └── handler/      # HTTP handlers + routes
├── shared/go/                # cross-service Go packages (TBD)
├── web/admin/                # React + Vite + TS admin portal
├── infra/docker/             # Dockerfiles
├── docker-compose.yml        # local dev stack: postgres, redis, identity
└── Makefile                  # common workflows
```

## Quick start

Prereqs: Docker, Go 1.22+, Node 20+.

```bash
cp .env.example .env

# Bring up Postgres, Redis, identity service
make up

# Run migrations
make migrate

# Create the platform super-admin (reads PLATFORM_ADMIN_* from .env)
make seed

# Start the admin web app
cd web/admin && npm install && npm run dev
```

Add to `/etc/hosts` for subdomain-based tenant routing in dev:

```
127.0.0.1  nexussacco.local platform.nexussacco.local tujenge.nexussacco.local
```

Then:
- Platform admin: http://platform.nexussacco.local:5173/
- Tenant login:   http://tujenge.nexussacco.local:5173/

## Authentication

- **Passwords**: Argon2id (64MB, 3 iterations, 4 lanes), per-user random salt.
- **Tokens**: HS256 JWT access tokens (15 min) carrying `tenant_id`, `user_id`, `roles`, `permissions`. Opaque refresh tokens (720h) stored hashed in Postgres, rotated on every use, revocable per session.
- **MFA**: TOTP scaffolded; enforcement is a follow-up.
- **Tenant isolation**: every authenticated request opens a transaction, sets `SET LOCAL app.tenant_id = '<uuid>'`, and runs queries inside it. RLS policies on tenant-scoped tables enforce isolation even if a query forgets the `WHERE tenant_id =` clause.

## Make targets

```
make up         # docker-compose up -d
make down       # docker-compose down
make logs       # tail identity logs
make migrate    # run pending migrations
make psql       # open a psql shell into the dev DB
make seed       # create platform super-admin from .env
make test       # go test ./...
make build      # build all Go services
```

## Roadmap

- [x] Identity service (auth, tenants, users, RBAC)
- [ ] Member service
- [ ] Savings service
- [ ] Loan service
- [ ] Payment service (M-Pesa Daraja)
- [ ] Accounting service (double-entry GL)
- [ ] Notification service
- [ ] Workflow service
- [ ] Reporting service
