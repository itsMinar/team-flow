# Architecture Decision Log

Short records of notable engineering decisions. Newest first within each phase.

## Phase 8 — Invitations

### Invitation tokens are hashed, single use, and never returned by the API

An invitation token is 256 bits of entropy, stored only as a SHA-256 hex digest,
and looked up by that digest. This mirrors refresh tokens: a fast one-way digest
is sufficient for a high-entropy secret and keeps lookup an indexed operation,
which a slow KDF would prevent.

The important decision is that **the token and the accept link are never part of
an API response.** They exist once, at creation time, and are emailed. Returning
them would let anyone holding `members.manage` redeem an invitation addressed to
somebody else and take that membership for themselves. The emailed link is also
the only recovery path for a lost invitation, which is why resend exists and why
it is safe: resend rotates the token hash on the same pending row, so the old
link stops working immediately and a token is never recoverable from the
database.

### Acceptance creates the membership, not the invitation

Acceptance runs in one tenant-scoped transaction that creates the account (when
needed), creates the membership with the role carried by the invitation, and
marks the invitation accepted. A membership therefore can never exist without a
matching accepted invitation, and a rolled-back acceptance leaves neither.

The alternative, pre-creating a membership with status `invited` at invitation
time, was rejected: it gives two sources of truth for "who has been invited", and
the membership row would have to be updated or revoked on every invitation
lifecycle transition.

An authenticated caller may only redeem an invitation issued to their own email
address (`INVITATION_EMAIL_MISMATCH`); an anonymous caller whose email already
has an account is told to sign in instead (`ACCOUNT_EXISTS`). Neither path can
occupy an address that the token was not issued to.

### Expiry is enforced lazily and persisted on use

A pending invitation past `expires_at` is reported as `expired` in previews and
lists without being written, and the transition is persisted when acceptance
first observes it — outside the failing transaction, so the update commits. This
avoids a sweep job in a phase that has no worker yet, while still letting the
database state converge. The expiry is also bounded by configuration
(`INVITATION_TTL`, at most 720 hours) because an invitation link that never
expires is a standing credential sitting in a mailbox.

### Email delivery is behind an interface, and the log transport cannot ship

`internal/mailer` defines a `Sender` so domain services never depend on a
transport. Phase 8 ships two: a log sender that writes the invitation link so
the flow is followable in local development, and a discard sender.

Because the log sender writes a live invitation link, configuration **rejects
it when `APP_ENV=production`**. That converts an easy mistake (shipping a dev
transport) into a startup failure instead of a leaked credential, and it makes
the missing piece explicit: production needs a real sender, which arrives with
the Phase 10 job queue.

Delivery happens after the invitation transaction commits, so an email can never
reference an invitation that was rolled back. A delivery failure is logged
rather than returned, because the invitation exists and can be resent; returning
an error would suggest the invite was never created.

### The accept endpoint uses optional authentication

`auth.Middleware.OptionalAuth` populates the principal when a valid access token
is present and otherwise passes the request through. This lets one public
endpoint serve both paths — create an account, or redeem with an existing session
— without the ambiguity of accepting credentials and tokens in one request. An
invalid or missing token is never an error there, so the middleware is only
suitable for endpoints where the token is the real credential.

## Phase 7 — Tasks

### Tasks are organization-scoped and reached through their project

Tasks carry `organization_id` and reference their project through a composite
`(project_id, organization_id)` foreign key, mirroring the project/team
invariant from Phase 6. Deleting a project cascades to its tasks, because a task
without a project has no meaning, whereas a task only optionally has an
assignee.

The assignee is stored as `assignee_id` with a composite foreign key to
`organization_memberships (user_id, organization_id)`. This makes two guarantees
database-enforced rather than application-only: a task can never be assigned
across tenants, and removing a membership (`ON DELETE SET NULL`) unassigns the
member's tasks instead of deleting the work. The service additionally requires
the membership status to be `active`, which a foreign key cannot express.

The service pre-checks the project and the assignee and returns
`PROJECT_NOT_FOUND` (404) and `INVALID_ASSIGNEE` (422) with safe messages;
concurrent deletions that slip past those checks are mapped from the
`23503` constraint names `tasks_project_fkey` and `tasks_assignee_fkey` to the
same errors, so a race cannot surface a raw database error.

### Two task collections, one filter

Task lists are exposed both per project and per organization. Both call the same
service method with the same filter type; the per-project route overwrites
`project_id` from the URL, so a query parameter can never widen a project's
collection. Filters are status, priority, assignee, and unassigned; the two
assignee filters are mutually exclusive and validated at the HTTP boundary.
Sort keys are fixed SQL `CASE` branches guarded by a service allowlist, exactly
as in Phase 6.

`tasks.read`, `tasks.create`, `tasks.update`, and `tasks.delete` mirror the
project permissions. Manager can create and update tasks but not delete them;
Member and Viewer keep read-only access. Task writes record an append-only
activity event in the same tenant transaction, recording status and assignee
transitions as structured metadata.

### Shared field types instead of per-resource copies

`internal/fieldtypes` now owns the calendar-date and partial-update
(`Optional[T]`) JSON types plus pointer helpers. Phase 6 had them in the
`projects` package, and copying them into `tasks` would have duplicated the
subtle omitted-versus-explicit-null semantics across resources. The `projects`
package keeps `Date` and `Optional` as type aliases, so its API and tests are
unchanged.

## Phase 6 — Projects

### Tenant-scoped projects with transactional activity

Projects carry an organization ID and may reference a team only through a
composite `(team_id, organization_id)` foreign key. This prevents direct SQL
writes from attaching a project to a team in another organization. Deleting a
team clears the optional project association rather than deleting the project.

Project list sorting uses fixed SQL `CASE` branches and a service-validated
allowlist, so user input is never interpolated into SQL. Collection responses
use bounded page and page-size values and return total metadata.

Project mutations resolve current RBAC permissions in the service layer and
write an append-only activity event in the same tenant transaction as the
project change. Activity rows have RLS and application-level UPDATE/DELETE
privileges are revoked.

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
