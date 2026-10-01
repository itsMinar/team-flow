# Architecture Decision Log

Short records of notable engineering decisions. Newest first within each phase.

## Phase 12 — Audit and observability

### The audit log is a separate table from the activity log, not a view over it

Activity logs answer "what happened to this project" for people working in the
product. The audit log answers "who authenticated, who changed access, and which
credentials exist" for someone answering an incident or a compliance question.
Those differ in reader, in retention, and in what must never change: an activity
row is created and left alone, but an audit row has to be provably unrewritable
by anyone, and it has to survive the deletion of the tenant it describes.

Merging them would have forced one table to serve both audiences, which means
either letting a tenant-scoped reader see authentication history that belongs to
no tenant, or weakening the append-only guarantee for activity rows.

### Append-only is enforced by grants, not by convention

The application role receives `SELECT` and `INSERT` on `audit_logs` and no
`UPDATE` or `DELETE`. That is the only guarantee that survives a bug, a
compromised service, and a person at a terminal with psql. Application-level "we
never update these" is a comment.

The consequence worth accepting is that mistakes are permanent. A failed login
that recorded an address which turned out to be a proxy cannot be deleted, so the
recorder stores only what it is confident about: never a password, never a token,
and for API keys only the name and prefix. Retention stays a future operational
decision instead of something the schema silently assumes.

### Audit rows outlive their organization, on purpose

`organization_id` carries no foreign key. Every other tenant table cascades, and
following that pattern here would have been consistent and wrong: an owner who
wants to erase the record of who they promoted and demoted would delete the
organization and take the evidence with it.

Deleting an organization therefore leaves its audit rows behind, reachable only
through operator queries. That trades storage and an intentional asymmetry for the
property that an access trail cannot be erased by the person it implicates. The
API never exposes such rows, because the read query always filters on
organization.

### Events belong to no organization when they are not about one

A login attempt against an unknown address has no tenant: the address is not an
account, and inventing an organization for it would both corrupt the tenant model
and let a scan against random addresses write into an arbitrary tenant's history.
Those rows are stored with a null organization and are visible only to an
operator.

The read API filters on `organization_id` inside a tenant transaction, so the
permission check, the query, and the RLS clause all agree on the same scope.

### RLS treats an empty tenant setting the same as an absent one

A session that has run a tenant transaction reports `app.current_org_id` as an
empty string afterwards, not as unset. A policy that only checks for "no tenant
context" therefore stops matching after the first transaction on a pooled
connection, and the application sees zero rows and failed inserts without any
error in its own code. Both forms are treated as no tenant, and the policies
follow the same permissive shape as the rest of the schema: tenant-scoped when a
context is present, unrestricted when it is not, with the explicit
`organization_id` filter as the scoping mechanism.

This is invisible while tests run as the table owner, because the owner bypasses
RLS. It surfaced only when the suite ran as `teamflow_app`, which is the role the
API actually uses.

### Credential and invitation events are recorded inside the write transaction

A generated API key is shown exactly once. If its audit row were written after
the transaction committed and the process died in between, a live credential
would exist with no record of it. API key and invitation events are therefore
written with the same transaction that mints or consumes them.

Everything else is recorded best effort. A failed audit write is logged and
counted but never returned to the caller, because refusing a completed login
because the audit table was briefly unavailable turns a logging dependency into
an availability dependency on the whole product.

### The recorder takes an interface so audit stays out of the dependency graph

`auth`, `organizations`, `invitations`, and `apikeys` all record events, and
`audit` needs the authorization stack to enforce `audit.read`. Wiring that
directly would be a cycle. The read service therefore depends on a one-method
`Authorizer` interface declared in `audit` and satisfied by the real authorizer,
which keeps the dependency pointing one way and lets each feature take a
`Recorder` interface with a no-op implementation, so tests do not need a database
to assert on unrelated behaviour.

### Metrics live in a private registry, not the default one

`prometheus.DefaultRegisterer` is process-global, so registering collectors at
init makes a second registry panic and leaks state across tests. The project uses
its own `prometheus.Registry`, which also means the exposition contains exactly
the project's own metrics plus the Go runtime and process collectors, and nothing
from a library that happened to register a default.

### Route labels use chi route patterns, never raw paths

Labelling with `r.URL.Path` would mint one series per project, task, and user, so
a single busy tenant could exhaust a Prometheus server's memory. The chi route
pattern is a fixed, bounded set, and the test asserts that two requests to
different resource IDs produce the same label value. The obvious corollary is
that a metric cannot tell you which tenant is slow; tenant detail belongs in the
audit log, not in a metric dimension.

### Trace identifiers propagate, but nothing is exported

Each request adopts a valid incoming W3C `traceparent` or generates a trace id,
echoes it back as `X-Trace-Id`, and attaches both it and the request id to every
log line. No exporter is registered and no sampler runs, so there is no cost and
no data leaving the process until an operator asks for it.

Shipping a collector by default would have meant choosing a vendor, a sampling
rate, and a retention policy as part of a phase whose job is to make the system
observable. Every later exporter can be added without changing this middleware,
because the identifiers are already correlated end to end.

## Phase 11 — Rate limiting

### Token buckets evaluated in Redis, not counted in the application

Each policy is a token bucket whose refill rate is derived from a limit and a
period, evaluated by a Lua script so the refill and the consumption happen in one
atomic step. The algorithm matters less than that atomicity: with a GET followed
by a SET, a burst of concurrent requests would all read the same balance and all
succeed, which is exactly the burst a limiter exists to stop. The concurrency test
asserts that N simultaneous requests yield exactly the configured number of
allowances.

A sliding window or a fixed counter would also work, but the bucket needs one key
per caller and no per-request bookkeeping, which matters when the key space is
every user id and every API key.

### Three budgets, keyed by who the caller actually is

The unauthenticated surface is keyed by client IP, session traffic by user, and
machine traffic by API key. The router applies them in one place, after
authentication, which has two consequences worth stating.

First, a leaked API key cannot exhaust a person's session budget, and a chatty
browser session cannot starve an integration. Sharing one budget across credential
types would let either one degrade the other.

Second, limiting is mounted centrally rather than inside each feature module, so a
new module is covered by default instead of depending on its author remembering
to add a limiter. Making `auth.Middleware.RequireAuth` idempotent is what allows
this: the router authenticates once at the top of the group, mounts the limiter
after it, and feature routers that still call `RequireAuth` themselves become
no-ops instead of parsing the token twice.

Because the limiter runs after authentication, a request rejected for a missing
credential never consumes anyone's budget.

### Identifiers are hashed into the key

A bucket key contains a truncated SHA-256 of the caller, not the caller. Key
names are readable by anyone with Redis access, and a user id or an address in
plaintext there is a small, permanent leak of identifiers into backup dumps and
monitoring output. The policy name stays readable on purpose, so an operator can
tell which budget a key belongs to.

### Fail open, loudly

When the limiter is unreachable the request is allowed through by default and the
failure is logged with the policy and request id. Turning a Redis hiccup into a
total API outage is a worse outcome than briefly unenforced limits, and Redis is
already a readiness dependency, so an operator sees the outage through `/ready`
rather than through failed logins. `RATE_LIMIT_FAIL_OPEN=false` reverses the
decision for deployments that would rather reject than serve unenforced traffic.

Enabling is also environment dependent: on by default when `APP_ENV=production`,
off elsewhere, so a developer is not throttled by the shipping defaults while a
deployment is protected without anyone having to remember a flag.

### One client address, one implementation

Keying an IP limit off an unvalidated `X-Forwarded-For` is a classic bypass: a
caller sets the header and gets a fresh bucket per request. The project already
had two private copies of `clientIP`, and they disagreed — one forwarded the raw
comma-separated header, the other returned the address with its port, which would
have made every bucket key slightly different per connection. Both now delegate to
`httpx.ClientIP`, which parses the first forwarded entry and strips the port. It
is still only trustworthy when the edge strips client-supplied forwarding headers,
which is called out on the function.

## Phase 10 — Background jobs

### Redis Streams with a consumer group, not a list

The queue is a Redis Stream with a consumer group. Streams give the three
properties a job queue needs and that would otherwise have to be rebuilt by hand:
an explicit acknowledgement step, a pending-entries list that identifies messages
a consumer took but never finished, and `XAUTOCLAIM` to take that work back after
a consumer dies. A plain list gives at-most-once delivery with no way to recover
lost work; a sorted set needs a separate visibility scheme for the same reason.

Delivery is therefore at least once, and handlers are written to be safe to run
twice. The invitation email handler is: it reloads the invitation, skips it when
the invitation is no longer pending, and records `notified_at` so a second run
does not produce a duplicate send.

Delayed work is held in a sorted set scored by due time and promoted into the
stream by a maintainer goroutine on a short interval. The alternative, sleeping
in the consumer, would hold a worker slot for the length of the backoff and delay
unrelated work behind it.

### Payloads are encrypted, because they carry credentials

The invitation email job must carry the raw invitation token: only its hash is
stored, precisely so the token cannot be recovered. Putting that token in a Redis
payload therefore moves a live credential into Redis, so payloads are sealed with
AES-GCM before they are written.

There is no unencrypted mode. A queue that silently writes credentials in the
clear when a key is missing is a queue someone will run in production without
realizing what is in it. `JOB_ENCRYPTION_KEY` is the explicit setting, and
deriving the key from `JWT_SECRET` when it is unset is documented as a convenience
with the cost of rotating both together.

A payload that fails authentication is dead-lettered, not retried: a wrong key or
tampered bytes will never become readable.

### Retries, dead letters, and reclaiming

A failing handler schedules the job again with exponential backoff and jitter,
bounded by `WORKER_RETRY_BASE_DELAY` and `WORKER_RETRY_MAX_DELAY`, up to
`WORKER_MAX_ATTEMPTS`. Jitter keeps a burst of failing jobs from retrying in
lockstep. Every attempt acknowledges the original stream entry, so a failing job
cannot be redelivered forever.

Failures that will never succeed — a deleted target, a malformed payload, an
unknown job type — are marked permanent and skip the retry budget entirely.
Everything else ends in the dead-letter stream with its last error, which is a
plain Redis stream an operator can inspect and, later, replay.

A separate maintainer pass promotes due retries and reclaims jobs idle longer
than `WORKER_STALE_AFTER`, so a worker crash mid-job does not strand work.

### Shutdown drains; it does not abandon

Worker loops follow the shutdown signal and stop claiming work immediately, but
the job currently in flight runs on a context detached from that signal, bounded
by `WORKER_SHUTDOWN_TIMEOUT`. Cancelling a half-sent email or a half-applied
sweep is worse than taking a moment longer to exit, and anything genuinely
unfinished stays in the pending-entries list for another worker to reclaim.

This is also why the maintainer goroutine follows the loop context rather than the
job context: maintenance work must stop as soon as the signal arrives, not idle
until the drain timeout expires.

### Deliveries that were never queued are reconciled by a sweep

Queueing an invitation email after the transaction commits can fail if Redis is
down, and an invitation that silently never arrives is worse than one that was
delivered twice. Two mechanisms cover it: the API falls back to inline delivery
when the enqueue fails, and `invitations.redeliver` re-queues anything still
pending with `notified_at` unset.

The sweep rotates the token before re-queueing. The previous link was never
delivered, so invalidating it costs nothing, and it keeps the "only the hash is
stored" property: the sweep mints a new token instead of trying to recover one.
`delivery_attempts` bounds the rotation so a permanently broken transport does not
generate mail forever.

## Phase 9 — API keys

### A key is a credential for one (user, organization) pair, not a role

An API key stores the organization it belongs to and the user who minted it, and
nothing else. It carries no role and no permission list.

The alternative designs were a key with its own role, or a key with a permission
scope. Both add a second authorization model that must be kept consistent with the
existing RBAC: who may grant a key an Owner or Admin role, what happens when a
key's permissions are stale, and how a revoked membership interacts with a key
that still carries permissions.

Binding a key to a user instead means every key request resolves exactly like a
session request: active user, active membership, live role, live permissions. It
also inherits the properties the codebase already relies on — a demotion takes
effect on the next request, authorization is never stale, and a removed member
loses access instantly. The cost is that keys cannot be scoped more narrowly than
their creator, which is why the creator must be an Owner or Admin: minting a
credential is itself a privileged act.

A composite foreign key from `(created_by, organization_id)` to
`organization_memberships` makes this a database invariant, so removing a
membership deletes the keys it backed instead of leaving an orphan credential.

### Keys are always expiring, and never returned twice

An API key is 256 bits of entropy stored only as a SHA-256 digest, with a
12-character prefix and the last four characters stored separately so an operator
can identify a key in code without the secret being derivable.

There is deliberately no non-expiring key: `API_KEY_DEFAULT_TTL` applies when a
request does not ask for a lifetime, and `API_KEY_MAX_TTL` caps what a caller may
ask for. A credential that never expires is a permanent hole in the
organization, and rotating one is a manual task nobody schedules.

The secret is returned only by the creation response. Revoking is the only remedy
otherwise, since the stored hash cannot be reversed — which is the intended
trade-off and why the response carries an explicit warning.

### Authentication stays in one middleware, with the key pinned to its organization

`auth.Middleware` gained an `APIKeyAuthenticator` interface rather than a
dependency on the API key package, so credential resolution stays free of import
cycles and can be tested with a stub. `RequireAuth` now resolves a bearer token
first and then an API key, so every organization-scoped route accepts either
credential without per-feature changes.

The middleware enforces the organization pin itself, by comparing the key's
organization with the `{orgID}` path parameter and rejecting the request when
they differ or when the route is not organization-scoped. Doing this in the
middleware rather than in each service means a new feature cannot accidentally
accept a key in the wrong tenant, and it keeps `organizations.Service.Authorize`
unchanged.

Keys are refused on `/auth/me` and `/api/v1/organizations` for the same reason: a
key is an organization credential, not a session, and those routes are not.

### Failures are uniform and usage recording is throttled

An unknown, expired, or revoked key all produce the same `401`, so a caller
cannot probe which keys exist. The service-level error code is `INVALID_API_KEY`;
at the HTTP boundary it surfaces as the standard `UNAUTHORIZED`, matching how a
bad bearer token is reported.

`last_used_at` is written by a single statement guarded to fire at most once a
minute per key. Recording usage on every authenticated request would double the
write load of read traffic for a field that only needs to be roughly accurate.

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
