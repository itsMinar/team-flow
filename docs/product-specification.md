# TeamFlow Product Specification

## Product

TeamFlow is a production-grade, multi-tenant SaaS backend for project and
employee management. Independent organizations share infrastructure while
their data remains strictly isolated. The initial architecture is a modular
monolith with a Go API and a separate Go worker process.

The project prioritizes clean architecture, security, tenant isolation,
transaction safety, concurrency safety, testability, observability,
horizontal scalability, and production-ready error handling.

## Architecture

- Go, `net/http`, chi, PostgreSQL 16+, pgx, sqlc, Redis, Docker, and Compose.
- `cmd/api` serves HTTP; `cmd/worker` processes asynchronous jobs.
- Handlers validate HTTP input and delegate to services. Services enforce
  authorization and business rules. PostgreSQL remains the source of truth.
- SQL is explicit and generated through sqlc. ORMs are intentionally avoided.
- Dependencies are constructed in entrypoints and injected into handlers and
  services. Global mutable state is avoided.

## Security and Tenancy

Every tenant-owned table has an `organization_id` and every query scopes by
organization. PostgreSQL RLS is a defense-in-depth backstop, not a substitute
for explicit service checks and tenant predicates.

The server resolves the requested organization from authenticated membership;
client-supplied organization headers are never trusted. Unknown, cross-tenant,
inactive, and deleted organizations must not disclose tenant existence.

Authorization is resolved from current database state, not stale JWT claims.
Passwords, tokens, API keys, invitation tokens, and authorization headers are
never logged or returned in unsafe responses.

## Core Domain

- Organizations have a unique slug and active, suspended, or deleted status.
- Users have normalized unique email addresses and bcrypt/Argon2id password
  hashes that never appear in API responses.
- Organization memberships connect users to organization-scoped roles.
- Roles include Owner, Admin, Manager, Member, and Viewer plus custom roles.
- Teams belong to one organization and contain users who already have active
  memberships in that organization.
- Projects belong to organizations and optionally teams.
- Tasks belong to projects and validate project, organization, assignee, and
  permission relationships.
- Invitations, API keys, audit logs, activity logs, and jobs are later phases.

## API Conventions

The versioned API lives under `/api/v1`. Collection endpoints support bounded
pagination where their phase requires it. Successful responses use a `data`
envelope; errors use a stable code, safe message, and request ID. Services
return typed domain errors which the HTTP layer maps to status codes.

## Phased Delivery

1. Foundation: configuration, infrastructure, HTTP, logging, health, Docker,
   migrations, and project structure.
2. Authentication: users, passwords, JWT access tokens, refresh rotation, and
   logout/revocation.
3. Multi-tenancy: organizations, memberships, tenant context, switching, and
   RLS isolation.
4. RBAC: roles, permissions, authorization, and role management.
5. Teams: teams, team memberships, and team authorization.
6. Projects: organization/team-scoped projects, pagination, filtering, sorting,
   authorization, and activity logging.
7. Tasks: assignment, statuses, priorities, due dates, and task activity.
8. Invitations: secure hashed tokens, expiration, acceptance, and email jobs.
9. API keys: one-time display, hashing, authentication, expiration, and
   revocation.
10. Background jobs: Redis queue, worker pool, retries, backoff, and dead-letter
    handling.
11. Rate limiting: Redis-backed limits for authentication, users, and API keys.
12. Audit and observability: append-only audit logs, Prometheus metrics,
    structured logging with request and trace correlation, and trace propagation.
13. Testing: unit, integration, security, tenant-isolation, and race testing.
14. Production hardening: security, indexes, transactions, deployment,
    configuration, and documentation review.

## Phase 5 Definition of Done

- Teams have organization-scoped records with names, descriptions, creator,
  timestamps, uniqueness constraints, indexes, and RLS.
- Active organization members can be added to teams and removed from teams.
- A user cannot be added to a team without an active membership in its
  organization.
- `teams.read` protects reads; `teams.manage` protects creation, updates,
  deletion, and membership changes.
- Every team operation re-resolves tenant membership and authorization in the
  service layer.
- Cross-tenant IDs return safe not-found or forbidden errors and never expose
  data.
- Unit tests, integration coverage, formatting, static analysis, and race
  tests are run before the phase is marked complete.

## Phase 6 Definition of Done

- Projects belong to one organization and may reference only a team from that
  organization.
- Project fields include name, description, status, priority, dates, creator,
  and timestamps with database constraints and indexes.
- Project lists support bounded pagination, status/priority/team filtering, and
  a whitelisted sort field and order.
- `projects.read`, `projects.create`, `projects.update`, and
  `projects.delete` are resolved from current organization RBAC state.
- Project writes and activity records commit atomically in one tenant-scoped
  transaction.
- Project activity is append-only, tenant-isolated, paginated, and safe for
  clients to read.
- Cross-tenant resources return safe errors without leaking project data.
- Unit, integration, RLS, authorization, migration, formatting, static, and
  race checks pass before Phase 6 is marked complete.

## Phase 7 Definition of Done

- Tasks belong to one project of one organization and can only reference a
  project from that organization.
- Task fields include title, description, status, priority, optional assignee,
  due date, creator, and timestamps with database constraints and indexes.
- An assignee must be an active member of the same organization; removing a
  membership unassigns its tasks and deleting a project deletes its tasks.
- Task lists support bounded pagination, project, status, priority, assignee,
  and unassigned filtering, and a whitelisted sort field and order.
- `tasks.read`, `tasks.create`, `tasks.update`, and `tasks.delete` are resolved
  from current organization RBAC state.
- Task writes and activity records commit atomically in one tenant-scoped
  transaction.
- Task activity is append-only, tenant-isolated, paginated, and safe for
  clients to read.
- Cross-tenant resources return safe errors without leaking task data.
- Unit, integration, HTTP, RLS, authorization, migration, formatting, static,
  and race checks pass before Phase 7 is marked complete.

## Phase 8 Definition of Done

- Invitations carry the organization, the invited email, and the role that will
  be granted, with only the SHA-256 hash of a 256-bit token stored.
- Invitation tokens are single use, expire after a bounded lifetime, and are
  never returned by an API response.
- Invitation management requires `members.manage`, resolves the tenant in the
  service layer, and never discloses another organization's invitations.
- Resend rotates the token and invalidates the previous link; revoke invalidates
  a pending invitation.
- The accept flow is public, previews only what the emailed invitee already
  knows, and either creates the account or redeems with an existing session.
- Acceptance creates the account, membership, and accepted invitation in one
  tenant-scoped transaction, then issues a session.
- An authenticated caller cannot redeem an invitation issued to another email
  address, and a duplicate pending invitation for one address is rejected.
- Transactional email goes through a transport interface, the development log
  transport cannot run in production, and a delivery failure does not lose the
  invitation.
- Unit, integration, HTTP, RLS, authorization, migration, formatting, static,
  and race checks pass before Phase 8 is marked complete.

## Phase 9 Definition of Done

- API keys belong to one organization and one creator, and are stored only as
  SHA-256 hashes.
- The secret is returned exactly once, at creation, and never appears in listings
  or logs; a prefix and the last four characters remain visible for
  identification.
- Every key expires within a configurable default and maximum lifetime, and
  non-expiring keys are not possible.
- Requests authenticate with an API key through a dedicated header, and the key
  is refused outside the organization it was created in.
- A key authenticates as its creator and resolves the creator's current
  membership and permissions on every request, so role changes take effect
  immediately.
- Removing the creator's membership deletes the key.
- Keys can be listed with filtering, sorting, and pagination, revoked
  immediately, and report last usage.
- Managing keys requires a dedicated permission held only by Owner and Admin.
- Unit, integration, HTTP, RLS, authorization, migration, formatting, static, and
  race checks pass before Phase 9 is marked complete.

## Phase 10 Definition of Done

- Jobs are queued in Redis and processed by a separate worker process with a
  configurable pool of consumers.
- Delivery is at least once with explicit acknowledgement, and work abandoned by a
  failed worker is reclaimed rather than lost.
- Job payloads are encrypted before they are written to Redis, and a payload that
  cannot be authenticated is never retried.
- Failed jobs are retried with exponential backoff and jitter, bounded by
  configuration, and exhausted or permanently rejected jobs are dead-lettered with
  their last error.
- Shutdown stops claiming work immediately and drains the in-flight job within a
  configured timeout.
- Invitation emails are delivered by the worker rather than in the request, with
  delivery recorded so an invitation that never reached the queue is re-queued.
- Maintenance jobs run tenant-scoped work inside each organization's own
  transaction.
- Unit, integration, HTTP, migration, formatting, static, and race checks pass
  before Phase 10 is marked complete.

## Phase 11 Definition of Done

- Unauthenticated endpoints are limited by client IP, session traffic by user, and
  API key traffic by key, each with its own budget.
- Limits are token buckets evaluated atomically in Redis, so concurrent requests
  cannot overspend a budget.
- Every limited response reports the limit, remaining budget, and reset time; a
  rejection returns HTTP 429 with `Retry-After` and a stable error code.
- Bucket keys are derived from a hash, so no user identifier or address is stored
  in Redis in the clear.
- Limiting is enabled by default in production and configurable, and an
  unreachable limiter fails open by default while remaining observable.
- Limits are applied centrally so a new feature module is covered by default, and
  requests rejected before authentication consume no budget.
- Unit, integration, HTTP, formatting, static, and race checks pass before Phase 11
  is marked complete.

## Phase 12 Definition of Done

- Security-relevant events are recorded with actor, organization, action,
  outcome, target, address, user agent, request ID, trace ID, and metadata:
  registration, login success and failure, token refresh and refresh-token
  reuse, logout, organization create and rename, role create, update and delete,
  member role assignment, invitation lifecycle, and API key lifecycle.
- Audit rows are append-only: the application role can insert and read but can
  neither update nor delete them, and the database rejects both.
- Audit rows survive organization deletion, so an access trail cannot be erased
  by the tenant owner, and events that belong to no tenant are never returned by
  the tenant-scoped API.
- Credential and invitation events are written inside the transaction that makes
  the change; other audit writes are best effort and are never allowed to fail a
  request.
- Metadata never contains a secret: no password, no token, and for API keys only
  the name and prefix.
- The audit read API is paginated, filterable by action, outcome, actor, and time
  range, requires `audit.read`, and is scoped by organization in a tenant
  transaction.
- Prometheus metrics cover HTTP request count, latency, in-flight requests, audit
  writes, job outcomes, job latency, and queue depth, and are served by both the
  API and the worker.
- HTTP metrics are labelled by route pattern rather than by raw path, so the
  label set stays bounded no matter how many resources exist.
- Every request has a trace identifier that is adopted from a valid `traceparent`
  when supplied, returned as `X-Trace-Id`, and attached to every log line.
- Metrics and tracing are configurable and disabled by configuration alone, with
  no vendor chosen and no data leaving the process by default.
- Unit, integration, HTTP, migration, formatting, static, and race checks pass
  before Phase 12 is marked complete.

## Definition of Done for the Product

The product is complete only when the API, worker, migrations, seed data,
authentication, tenant isolation, RBAC, teams, projects, tasks, invitations,
API keys, jobs, rate limits, audit/activity logs, health checks, metrics,
Docker workflow, tests, race detector, OpenAPI documentation, and README are
implemented and verified. A process merely starting is not sufficient.
