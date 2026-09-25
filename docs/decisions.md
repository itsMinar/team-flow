# Architecture Decision Log

Short records of notable engineering decisions. Newest first within each phase.

## Phase 5 — Teams

### Organization-owned teams with membership invariants

Teams are organization-owned resources with unique names per organization.
Team memberships carry the organization ID and reference both the team and
organization through a composite foreign key, preventing mismatched tenant
IDs even for direct database writes. The service verifies that a user has an
active membership in the organization before adding them to a team.

The `teams.read` and `teams.manage` permissions are stored in PostgreSQL and
resolved at request time. Owner and Admin receive both permissions; Manager,
Member, and Viewer receive read access. Every team operation re-resolves the
tenant and permission in the service layer and runs tenant-scoped SQL inside a
transaction-local RLS context.

## Phase 4 — RBAC

### Database-backed permissions and immutable system roles

Authorization is represented by six stable organization permissions:
`organizations.read`, `organizations.update`, `members.read`,
`members.manage`, `roles.read`, and `roles.manage`. Permissions are stored in
PostgreSQL and assigned to organization-scoped roles through
`role_permissions`; JWTs contain identity only and never carry authorization
state. This keeps permission changes effective without reissuing access tokens.

Every organization receives Owner, Admin, Manager, Member, and Viewer system
roles with seeded permissions. System roles cannot be edited or deleted. Custom
roles can be managed by callers with `roles.manage`, but a role cannot be
deleted while members still reference it. Role assignment requires
`members.manage`, prevents non-Owners from assigning or removing the Owner
role, and prevents demoting the final Owner.

Role and membership authorization is resolved inside the service layer after
server-side tenant membership verification. Tenant-scoped permission queries
run inside the same transaction-local RLS context used by organization data,
so explicit authorization checks and database isolation remain aligned.

## Phase 3 — Multi-tenancy

### RLS backstop requires FORCE, a per-transaction org context, and a non-superuser app role

Migration 8 enabled Row Level Security and defined org-isolation policies, but
three things left it inert: the application connected as a superuser (which
PostgreSQL always exempts from RLS), the owner would be exempt without
`FORCE ROW LEVEL SECURITY`, and nothing set the `app.current_org_id` the policies
read. Migration 9 adds `FORCE ROW LEVEL SECURITY`; migration 10 gives the
`teamflow_app` role (created NOLOGIN in migration 8) login plus the privileges
and default privileges the app needs, and the Docker api/worker now connect as
it while migrations keep running as the owner. The organizations service runs
tenant-scoped statements inside a transaction that calls
`set_config('app.current_org_id', <orgID>, true)`. The policies stay permissive
when the setting is unset or empty, so login, registration, and organization
switching (which span organizations) keep working. RLS is a defense-in-depth
backstop behind explicit `WHERE organization_id = $n` scoping, not a substitute
for it.

### Tenant resolved from membership, cross-tenant returns 404

The active organization is derived server-side by verifying the authenticated
user has an active membership in the requested organization, never from a
client header. Unknown organizations, non-members, and inactive memberships all
return 404 so tenant existence never leaks across tenants; a suspended (but
member-visible) organization returns 403. A `deleted` organization is checked
before the generic non-active branch so it maps to 404 rather than 403.

### Atomic organization provisioning

Creating an organization inserts the organization, all default system roles
(Owner, Admin, Manager, Member, Viewer), and the caller's Owner membership in a
single transaction, so a partially provisioned tenant can never be observed.
Slugs are generated from the name and de-duplicated with a random suffix.

## Phase 2 — Authentication

### Normalize legacy refresh-token IP columns

Some databases applied migration 6 with `ip_address INET`, while the
checked-in schema and generated Go model use nullable `TEXT`. pgx cannot scan
binary `INET` into `*string`, causing session creation and login to fail.
Migration 7 converts the column to `TEXT` without deleting rows and also works
on fresh databases whose column is already text. Its down migration is
intentionally a no-op: `TEXT` matches the checked-in version 6 schema, and
restoring `INET` would reintroduce the runtime mismatch. Restart running APIs
after applying this migration to discard cached query plans and connections.

### Serialized refresh-token rotation

Refresh tokens contain 32 cryptographically random bytes and are stored only
as SHA-256 hashes. Refresh validates the token under a PostgreSQL `FOR UPDATE`
row lock and creates its replacement in the same transaction. Concurrent use
of the same token therefore cannot create two successful rotations. Reuse of a
revoked token commits family-wide revocation before returning unauthorized.
Clients must serialize refresh attempts to avoid revoking their own session.

### Short-lived access tokens and bcrypt passwords

Access tokens use HS256 with a configured issuer and mandatory expiration.
Passwords use bcrypt at cost 12; HTTP validation rejects passwords exceeding
bcrypt's 72-byte limit. Logout revokes refresh tokens, while existing access
tokens remain valid until expiry. Immediate access-token revocation is not
part of this phase.

### Explicit disposable integration databases

Database tests require `TEST_DATABASE_URL` and a database name ending in
`_test`. They never fall back to the application `DATABASE_URL`. Tests truncate
their tables and must only run against a dedicated disposable database with
all migrations applied. The concurrent-refresh regression holds a database
row lock until both requests are waiting, then checks rotation and family
revocation.

## Phase 1 — Foundation

### Modular monolith with two processes

The system ships as a single codebase exposing two entrypoints, `cmd/api` and
`cmd/worker`, that share `internal/*` packages. This keeps deployment and local
development simple while allowing modules to be extracted into services later if
scale demands it. Microservices are intentionally avoided at this stage.

### `net/http` + chi router

`chi` is a thin, idiomatic layer over `net/http` with good middleware ergonomics
and no framework lock-in. It keeps handlers standard-library compatible, which
matters for testability and long-term maintenance.

### pgx over an ORM

We use `pgx`/`pgxpool` with explicit SQL rather than an ORM. Explicit SQL makes
tenant-scoping (`WHERE organization_id = $n`) auditable and predictable, and
`sqlc` (introduced later) will generate type-safe query code without hiding the
SQL. This directly supports the security and tenant-isolation goals.

### Configuration via environment, validated at startup

`internal/config` loads all settings from the environment, applies safe
defaults, and validates required values. The process fails fast with an
aggregated error listing every problem, so misconfiguration is caught before
serving traffic. No global mutable config; the struct is injected.

### Structured logging with `slog`

The standard library's `slog` provides JSON output with no third-party
dependency. A request ID is generated per request and propagated via
`context.Context`, enabling correlation across log lines (and later, jobs).

### Typed errors mapped at the HTTP boundary

`internal/httpx` defines sentinel domain errors and an `APIError` type. Services
return domain errors; the HTTP layer maps them to status codes and client-safe
messages. Internal details (SQL, stack traces, wrapped causes) are logged but
never serialized to clients — important for not leaking implementation detail in
production.

### Liveness vs. readiness separation

`/health` performs no dependency checks so a transient dependency issue never
causes an orchestrator to kill a healthy process. `/ready` checks PostgreSQL and
Redis and returns 503 when a dependency is down, gating traffic appropriately.

### Distroless multi-stage Docker image

The runtime image is `gcr.io/distroless/static-debian12:nonroot`: no compiler,
shell, or source, running as non-root. This minimizes attack surface and image
size. Binaries are built static (`CGO_ENABLED=0`).

### Host port 5433 for PostgreSQL in Compose

To avoid clashing with a developer's local PostgreSQL on 5432, the Compose
service publishes 5433 on the host. Container-to-container traffic still uses the
internal `postgres:5432` address.
