# TeamFlow

TeamFlow is a production-grade, multi-tenant SaaS backend for project and
employee management. Multiple independent organizations share the same
infrastructure while their data stays strictly isolated.

This repository is being built incrementally, phase by phase. **Phases 1
(Foundation), 2 (Authentication), 3 (Multi-tenancy), 4 (RBAC), 5 (Teams), 6
(Projects), 7 (Tasks), 8 (Invitations), 9 (API keys), 10 (Background jobs), and
11 (Rate limiting), and 12 (Audit and observability) are complete.** See
[Roadmap](#roadmap) for what is done and what comes next.

## Overview

TeamFlow lets organizations manage teams, projects, and tasks with role-based
access control, invitations, API keys, audit/activity logging, background jobs,
and rate limiting. The system is a **modular monolith** with two processes:

- `cmd/api` — the HTTP API
- `cmd/worker` — the background job worker

Both share the same internal packages and dependency graph.

## Architecture

```mermaid
graph TD
    Client[Client] --> API[Go API cmd/api]
    API --> PG[(PostgreSQL)]
    API --> RD[(Redis)]
    RD --> Worker[Go Worker cmd/worker]
    Worker --> PG
```

Request flow through the API middleware chain:

```mermaid
graph TD
    R[Request] --> Rec[Recovery]
    Rec --> RID[Request ID]
    RID --> Log[Logging]
    Log --> Sec[Security Headers]
    Sec --> Body[Max Body Bytes]
    Body --> H[Handler]
```

Handlers stay thin; business logic lives in services and data access in
repositories (added in later phases):

```
Handler -> Service / Use Case -> Repository -> PostgreSQL
```

### Project layout

```text
cmd/
  api/         HTTP API entrypoint (graceful shutdown, DI wiring)
  worker/      Background worker entrypoint
internal/
  api/         Router and middleware wiring
  apikeys/     API keys and API-key authentication
  auth/        Registration, login, JWTs, refresh tokens, auth middleware
  authctx/     Authenticated principal in request context
  cache/       Redis client
  config/      Environment configuration + validation
  database/    PostgreSQL (pgx) connection pool
  db/          Generated sqlc queries and models
  fieldtypes/  Shared calendar-date and partial-update field types
  health/      Liveness and readiness handlers
  audit/       Append-only security audit log and its read API
  jobs/        Redis job queue, worker pool, retries, dead letters
  metrics/     Prometheus registry, HTTP metrics, and the exposition handler
  ratelimit/   Redis token-bucket limiter and its HTTP middleware
  httpx/       Response envelope + typed error mapping
  invitations/ Organization invitations, acceptance, and token lifecycle
  permissions/  Permission keys and default role grants
  mailer/      Transactional email sender interface and transports
  middleware/  Reusable HTTP middleware
  observability/ Structured logging, request correlation, trace propagation
  organizations/ Organizations, memberships, roles, and tenant resolution
  projects/    Organization-scoped projects and project activity
  tasks/       Project-scoped tasks, assignment, and task activity
  teams/       Organization-scoped teams and team memberships
  validation/  Reusable request validation
migrations/    Versioned SQL migrations (golang-migrate)
queries/       SQL source for sqlc
```

## Technology stack

| Technology           | Why                                                                                   |
| -------------------- | ------------------------------------------------------------------------------------- |
| **Go**               | Fast, statically compiled, excellent concurrency for API + workers                    |
| **PostgreSQL 16**    | Strong relational integrity, constraints, and Row Level Security for tenant isolation |
| **Redis**            | Rate limiting and the background job queue                                            |
| **pgx**              | High-performance PostgreSQL driver and pool (no ORM; explicit SQL)                    |
| **chi**              | Lightweight, idiomatic `net/http` router                                              |
| **slog**             | Structured JSON logging from the standard library                                     |
| **golang-migrate**   | Versioned, reversible schema migrations                                               |
| **Docker / Compose** | Reproducible local environment and production images                                  |

An ORM is intentionally avoided in favor of explicit SQL and generated `sqlc`
queries for predictable behavior and easier tenant-scoping review.

## Local setup

Prerequisites: Docker, Go 1.26+, and `make`.

```bash
cp .env.example .env
make docker-up        # starts postgres + redis (and builds api/worker images)
make migrate-up       # applies migrations
make dev              # runs the API locally against the containers
```

> The compose PostgreSQL is published on host port **5433** to avoid clashing
> with a local PostgreSQL on 5432. `.env.example` is already set accordingly.

Verify the server:

```bash
curl -i localhost:8080/health   # 200 {"data":{"status":"ok"}}
curl -s localhost:8080/ready     # verifies PostgreSQL + Redis
```

Run the worker in another shell. It consumes the same queue the API writes to:

```bash
make worker
```

## Testing

```bash
make test        # all unit tests
make test-race   # race detector (requires cgo / a C compiler)
make cover       # coverage summary
```

Auth integration tests run only when `TEST_DATABASE_URL` is set. They never
fall back to `DATABASE_URL`, and the database name must end in `_test`.
**Tests truncate auth and organization tables: use a dedicated disposable
database, never an application database.** Apply all migrations first:

```bash
docker compose exec postgres createdb -U teamflow teamflow_test
export TEST_DATABASE_URL='postgres://teamflow:teamflow@localhost:5433/teamflow_test?sslmode=disable'
make migrate-up DATABASE_URL="$TEST_DATABASE_URL"
go test -race -count=1 ./...
unset TEST_DATABASE_URL
```

Queue and rate limiter tests need a Redis instance and are skipped without it:

```bash
export TEST_REDIS_URL='redis://localhost:6379/15'
go test -race -count=1 ./internal/jobs ./internal/ratelimit
unset TEST_REDIS_URL
```

They use database 15 by convention so a development Redis is not disturbed, and
they delete only the `teamflow:jobs:*` and `teamflow:ratelimit:*` keys.

Do not run separate test processes against the same test database concurrently.
The suite covers token rotation and reuse, concurrent refresh attempts, logout,
logout-all, JWT expiration requirements, HTTP authentication/validation, and
cross-tenant isolation (reads, member listing, and updates across organizations
must fail without leaking existence). It also covers team, project, and task
authorization, filtering, pagination, activity recording, tenant-scoped foreign
keys, and Row Level Security, plus the invitation lifecycle: delivery, expiry,
single use, resend rotation, revocation, acceptance for new and existing
accounts, and the guarantee that the token never appears in an API response.

## Database / migrations

Migrations live in `migrations/` as timestamped `.up.sql` / `.down.sql` pairs
and are applied with the dockerized `golang-migrate` CLI:

```bash
make migrate-up                          # apply all
make migrate-down                        # roll back one
make migrate-create name=create_users    # scaffold a new migration
```

Never modify applied migrations or the production schema by hand.

## Configuration

All configuration comes from environment variables (see `.env.example`).
Required values (`DATABASE_URL`, `REDIS_URL`, `JWT_SECRET`) are validated at
startup and the process **fails fast** with a clear message if anything is
missing or invalid. Compose explicitly passes JWT settings from `.env` into
both application services and rejects a missing `JWT_SECRET` before startup.
Use a long, random secret in production (at least 32 characters); the example
secret is for local development only. Access tokens default to 15 minutes and
refresh tokens to 720 hours. Secrets are never committed; `.env` is gitignored.

## Authentication

All auth endpoints are under `/api/v1/auth`:

| Method | Path          | Authentication             | Purpose                                                        |
| ------ | ------------- | -------------------------- | -------------------------------------------------------------- |
| POST   | `/register`   | Public                     | Create user, organization, Owner role, membership, and session |
| POST   | `/login`      | Public                     | Verify credentials and issue a session                         |
| POST   | `/refresh`    | Refresh token in JSON body | Rotate the refresh token and issue an access token             |
| POST   | `/logout`     | Refresh token in JSON body | Revoke that session's refresh-token family                     |
| POST   | `/logout-all` | Bearer access token        | Revoke all of the user's refresh tokens                        |
| GET    | `/me`         | Bearer access token        | Return the authenticated user's profile                        |

Registration requires `email`, `password`, `first_name`, `last_name`, and
`organization_name`. Login requires `email` and `password`; refresh and logout
require `refresh_token`. Passwords must include letters and digits, meet the
minimum length policy, and not exceed bcrypt's **72-byte** limit, including
UTF-8 bytes. Invalid registration passwords return HTTP 400.

Register, login, and refresh return `data` containing `access_token`,
`refresh_token`, `token_type`, `expires_in`, and `user`. Protected requests use
`Authorization: Bearer <access_token>`. Access tokens require a valid HS256
signature, the configured issuer, the access-token type, and an expiration.

Only SHA-256 hashes of random refresh tokens are stored. Rotation locks the
presented token inside a transaction so concurrent requests cannot both rotate
it. Reusing a revoked token revokes its entire family, including the replacement;
clients should serialize refresh requests. Logout does not immediately revoke
already-issued access tokens: those remain valid until their expiration.

## Observability

- **Structured JSON logs** via `slog`, one line per request with method, path,
  status, duration, and a correlating `request_id`.
- Every request is assigned an `X-Request-ID` (honored if the client supplies
  one) and it is propagated through `context.Context`.
- Every log line carries the request ID and the trace ID of its request.
- Prometheus metrics are exposed at `/metrics`; see
  [Audit and observability](#audit-and-observability).

## Health checks

- `GET /health` — liveness; always 200 if the process is up (no dependency
  checks, so a slow dependency never triggers a restart).
- `GET /ready` — readiness; verifies PostgreSQL and Redis, returns 503 if any
  dependency is unavailable, without leaking infrastructure detail.

## Graceful shutdown

On `SIGINT`/`SIGTERM` the API stops accepting new connections, drains in-flight
requests within `HTTP_SHUTDOWN_TIMEOUT`, then closes Redis and PostgreSQL and
exits cleanly. The worker stops claiming jobs, finishes the job it holds within
`WORKER_SHUTDOWN_TIMEOUT`, and exits.

## Multi-tenancy, RBAC, jobs

Multiple organizations share the same database while their data stays isolated.
All organization endpoints are under `/api/v1/organizations` and require a
Bearer access token:

| Method | Path                                                 | Authorization          | Purpose                                                  |
| ------ | ---------------------------------------------------- | ---------------------- | -------------------------------------------------------- |
| GET    | `/organizations`                                     | Any authenticated user | List organizations the caller belongs to (org switching) |
| POST   | `/organizations`                                     | Any authenticated user | Create an organization with default roles and an Owner   |
| GET    | `/organizations/{orgID}`                             | Active member          | Read an organization, including the caller's role        |
| PATCH  | `/organizations/{orgID}`                             | `organizations.update` | Rename the organization                                  |
| GET    | `/organizations/{orgID}/members`                     | `members.read`         | List the organization's members                          |
| PATCH  | `/organizations/{orgID}/members/{membershipID}/role` | `members.manage`       | Assign a role to an active member                        |
| GET    | `/organizations/{orgID}/roles`                       | `roles.read`           | List roles and permissions                               |
| POST   | `/organizations/{orgID}/roles`                       | `roles.manage`         | Create a custom role                                     |
| PATCH  | `/organizations/{orgID}/roles/{roleID}`              | `roles.manage`         | Update a custom role and permissions                     |
| DELETE | `/organizations/{orgID}/roles/{roleID}`              | `roles.manage`         | Delete an unused custom role                             |
| GET    | `/organizations/{orgID}/audit-logs`                   | `audit.read`           | Read the organization's append-only audit log           |

Teams are organization-scoped and use the `teams.read` and `teams.manage`
permissions:

| Method | Path                                                           | Authorization  | Purpose                           |
| ------ | -------------------------------------------------------------- | -------------- | --------------------------------- |
| GET    | `/organizations/{orgID}/teams`                                 | `teams.read`   | List teams                        |
| POST   | `/organizations/{orgID}/teams`                                 | `teams.manage` | Create a team                     |
| GET    | `/organizations/{orgID}/teams/{teamID}`                        | `teams.read`   | Read a team                       |
| PATCH  | `/organizations/{orgID}/teams/{teamID}`                        | `teams.manage` | Update a team                     |
| DELETE | `/organizations/{orgID}/teams/{teamID}`                        | `teams.manage` | Delete a team                     |
| GET    | `/organizations/{orgID}/teams/{teamID}/members`                | `teams.read`   | List team members                 |
| POST   | `/organizations/{orgID}/teams/{teamID}/members`                | `teams.manage` | Add an active organization member |
| DELETE | `/organizations/{orgID}/teams/{teamID}/members/{teamMemberID}` | `teams.manage` | Remove a team member              |

Projects are organization-scoped and optionally belong to an organization
team. They use `projects.read`, `projects.create`, `projects.update`, and
`projects.delete` permissions:

| Method | Path                                                   | Authorization     | Purpose                              |
| ------ | ------------------------------------------------------ | ----------------- | ------------------------------------ |
| GET    | `/organizations/{orgID}/projects`                      | `projects.read`   | List filtered and paginated projects |
| POST   | `/organizations/{orgID}/projects`                      | `projects.create` | Create a project                     |
| GET    | `/organizations/{orgID}/projects/{projectID}`          | `projects.read`   | Read a project                       |
| PATCH  | `/organizations/{orgID}/projects/{projectID}`          | `projects.update` | Update a project                     |
| DELETE | `/organizations/{orgID}/projects/{projectID}`          | `projects.delete` | Delete a project                     |
| GET    | `/organizations/{orgID}/projects/{projectID}/activity` | `projects.read`   | Read project activity                |

Project lists accept `page`, `page_size` (maximum 100), `status`, `priority`,
`team_id`, `sort`, and `order` (`asc` or `desc`). Supported sort fields are
`created_at`, `updated_at`, `name`, `due_date`, and `priority`.

Tasks belong to a project of the organization and support assignment to active
organization members. They use the `tasks.read`, `tasks.create`,
`tasks.update`, and `tasks.delete` permissions:

| Method | Path                                                         | Authorization   | Purpose                                  |
| ------ | ------------------------------------------------------------ | --------------- | ---------------------------------------- |
| GET    | `/organizations/{orgID}/tasks`                                | `tasks.read`    | List filtered and paginated tasks        |
| GET    | `/organizations/{orgID}/tasks/{taskID}`                       | `tasks.read`    | Read a task                              |
| PATCH  | `/organizations/{orgID}/tasks/{taskID}`                       | `tasks.update`  | Update a task, including assignment      |
| DELETE | `/organizations/{orgID}/tasks/{taskID}`                       | `tasks.delete`  | Delete a task                            |
| GET    | `/organizations/{orgID}/tasks/{taskID}/activity`              | `tasks.read`    | Read task activity                       |
| GET    | `/organizations/{orgID}/projects/{projectID}/tasks`           | `tasks.read`    | List the tasks of one project            |
| POST   | `/organizations/{orgID}/projects/{projectID}/tasks`           | `tasks.create`  | Create a task in a project               |

Task list query parameters are `page`, `page_size` (maximum 100), `project_id`,
`status`, `priority`, `assignee_id`, `unassigned`, `sort`, and `order`. Valid
statuses are `todo`, `in_progress`, `blocked`, `in_review`, `done`, and
`cancelled`; supported sorts are `created_at`, `updated_at`, `title`,
`due_date`, `priority`, and `status`. `assignee_id` and `unassigned` cannot be
combined. An assignee must be an active member of the same organization;
removing a membership unassigns its tasks, and deleting a project deletes its
tasks.

Invitations let someone outside an organization join it with a role the
inviter chooses. They are managed with the `members.manage` permission:

| Method | Path                                                          | Authorization  | Purpose                             |
| ------ | ------------------------------------------------------------- | -------------- | ----------------------------------- |
| GET    | `/organizations/{orgID}/invitations`                          | `members.manage` | List invitations                    |
| POST   | `/organizations/{orgID}/invitations`                          | `members.manage` | Invite an email address             |
| POST   | `/organizations/{orgID}/invitations/{invitationID}/resend`    | `members.manage` | Issue a new link for a pending invite |
| POST   | `/organizations/{orgID}/invitations/{invitationID}/revoke`    | `members.manage` | Revoke a pending invitation         |

The accept flow is public because the invitation token is itself the credential:

| Method | Path                        | Authentication                | Purpose                                     |
| ------ | --------------------------- | ----------------------------- | ------------------------------------------- |
| GET    | `/invitations/{token}`      | Public                        | Preview the organization, role, and expiry  |
| POST   | `/invitations/accept`       | Optional (Bearer if present) | Create the account or join with a session    |

Invitation list query parameters are `page`, `page_size` (maximum 100),
`status`, `email`, `sort`, and `order`. Supported sorts are `created_at`,
`expires_at`, and `email`.

Invitations are single-use and expire after `INVITATION_TTL` (168 hours by
default, capped at 720 hours). Only the SHA-256 hash of the token is stored and
the token is never returned by the API: it is emailed, so a caller holding
`members.manage` cannot redeem an invitation addressed to someone else.
Resending rotates the token and invalidates the previous link; revoking makes it
unusable. Acceptance runs in one transaction that creates the membership with
the invited role and marks the invitation accepted, then issues a session so the
invitee is signed in immediately. An authenticated caller redeeming someone
else's invitation is refused with `INVITATION_EMAIL_MISMATCH`, and an anonymous
caller whose email already has an account gets `ACCOUNT_EXISTS`.

Transactional email goes through the `mailer` package. `MAIL_TRANSPORT=log`
writes the invitation link to the application log for local development and is
rejected when `APP_ENV=production`, so a live invitation link can never end up
in a production log sink. `MAIL_TRANSPORT=none` disables delivery entirely.
Phase 10 queues that email on the worker (see
[Background jobs](#background-jobs)); the log transport is still what actually
sends it in development, so production needs a real transport implementation.

API keys are a second credential for server-to-server and automation calls. They
are managed with the new `api_keys.manage` permission, which is granted to Owner
and Admin only:

| Method | Path                                                | Authorization      | Purpose                       |
| ------ | --------------------------------------------------- | ------------------ | ----------------------------- |
| GET    | `/organizations/{orgID}/api-keys`                   | `api_keys.manage`  | List API keys                 |
| POST   | `/organizations/{orgID}/api-keys`                   | `api_keys.manage`  | Create an API key             |
| DELETE | `/organizations/{orgID}/api-keys/{apiKeyID}`        | `api_keys.manage`  | Revoke an API key             |

An API key authenticates a request with the `X-API-Key` header instead of a
bearer token:

```bash
curl -sS http://localhost:8080/api/v1/organizations/$ORG_ID/projects \
  -H "X-API-Key: tfk_your_key_here"
```

Key properties:

- **One-time display.** The secret appears only in the creation response; only
  its SHA-256 hash is stored. Listings return a 12-character prefix and the last
  four characters so a key can be identified in code.
- **Always expiring.** A key is valid for `API_KEY_DEFAULT_TTL` (90 days) unless
  `expires_in_days` requests less, and can never exceed `API_KEY_MAX_TTL`
  (365 days). There is no non-expiring key.
- **Pinned to one organization.** A key only works on routes under the
  organization it was minted in, and it authenticates as the user who created it.
- **No authorization of its own.** Every request re-resolves the creator's
  current membership and permissions, so a role change or a demotion takes effect
  immediately, and removing the membership deletes the key through a composite
  foreign key.
- **Revocable and observable.** Revocation is immediate, `last_used_at` is
  recorded (at most once a minute per key), and listing hides revoked keys unless
  `include_revoked=true` is passed.

An invalid, expired, or revoked key is rejected as a plain `401`
authentication failure, so a caller cannot tell which of the three it was.

The active tenant is derived server-side by verifying the authenticated user has
an **active membership** in the requested organization; it is never taken from a
client-supplied header. Unknown or cross-tenant organizations return **404**
(not 403) so tenant existence never leaks. Creating an organization provisions
the default system roles (Owner, Admin, Manager, Member, Viewer), their default
permissions, and the Owner membership atomically in one transaction. The
available permissions are `organizations.read`, `organizations.update`,
`members.read`, `members.manage`, `roles.read`, `roles.manage`, `teams.read`,
`teams.manage`, `projects.read`, `projects.create`, `projects.update`, and
`projects.delete`. Owner and Admin receive all twelve; the other default roles
receive permissions appropriate to their role. Team memberships and project
teams can only reference active resources in the same organization.
System roles cannot be edited or deleted, and the last Owner cannot be demoted.

Tenant-scoped statements run inside a transaction that sets
`app.current_org_id`, and PostgreSQL Row Level Security policies (with
`FORCE ROW LEVEL SECURITY`) confine those rows to the active organization as a
defense-in-depth backstop behind the application's explicit
`WHERE organization_id = $n` scoping. Because superusers and table owners bypass
RLS, the api/worker connect as the non-superuser `teamflow_app` role (the Docker
stack is configured this way; run the app as a non-superuser in production too),
while migrations run as the owner. When no tenant context is set (login,
registration, organization switching), the policies remain permissive so those
flows keep working. Background work runs in `cmd/worker`; see
[Background jobs](#background-jobs).

## Audit and observability

**Audit log.** `GET /api/v1/organizations/{orgID}/audit-logs` returns an
organization's security history, newest first, paginated and filterable by
`action`, `outcome`, `actor_user_id`, and `since` (bounded to 90 days). It
requires the new `audit.read` permission, granted to Owner and Admin.

Recorded events: registration, login success and failure, token refresh and
refresh-token reuse, logout and logout-all, organization create and rename, role
create, update and delete, member role assignment, invitation create, resend,
revoke and accept, and API key create and revoke.

- **Append-only.** The application role gets `SELECT` and `INSERT` only;
  `UPDATE` and `DELETE` are revoked, so neither an operator nor a bug can rewrite
  history.
- **No cascade delete.** `organization_id` has no foreign key, so deleting an
  organization cannot erase the record of who changed access while it existed.
- **Authentication events belong to no organization**, because a failed login is
  not tied to a tenant. They are stored for an operator and are never returned by
  the tenant-scoped API.
- **Recorded where the change happens.** Credential and invitation events are
  written inside the transaction that makes the change, so a minted API key can
  never exist without a record of it. Everything else is written best effort: a
  failed audit write is logged and counted, never returned to the caller.

**Metrics.** `GET /metrics` serves the Prometheus text format, and the worker
serves the same endpoint on `METRICS_ADDR` (default `:9091`) because it has no
API port. Exported series:

| Metric                                   | Labels                    | Meaning                                  |
| ---------------------------------------- | ------------------------- | ---------------------------------------- |
| `teamflow_http_requests_total`           | method, route, status     | Request count by route pattern           |
| `teamflow_http_request_duration_seconds` | method, route             | Latency histogram                        |
| `teamflow_http_requests_in_flight`       | —                         | Requests being served                    |
| `teamflow_audit_events_total`            | action, outcome           | Audit events recorded                    |
| `teamflow_jobs_processed_total`          | type, result              | Job outcomes, including dead-lettered    |
| `teamflow_jobs_duration_seconds`         | type                      | Job execution time                       |
| `teamflow_jobs_queue_depth`              | queue                     | Ready, retrying, and dead-lettered depth |

Route labels are chi route patterns, never raw paths, so a metric cannot explode
into one series per resource ID. Go runtime and process metrics are included.
Restrict `/metrics` to the monitoring network at the edge.

**Tracing.** Every request adopts a W3C `traceparent` when a valid one is
supplied and generates a trace ID otherwise, echoes it back as `X-Trace-Id`, and
attaches both the request ID and the trace ID to every log line. No exporter is
registered, so nothing is sampled or shipped unless an operator asks for it; an
OpenTelemetry exporter can be added later without changing any of this.

## Rate limiting

Rate limits are token buckets evaluated atomically inside Redis with a Lua
script, so a burst of concurrent requests cannot overspend a budget. Each surface
gets its own bucket so one caller cannot starve another:

| Surface                                   | Keyed by  | Default limit |
| ----------------------------------------- | --------- | ------------- |
| `/api/v1/auth/*` and `/api/v1/invitations/{token}`, `/api/v1/invitations/accept` | client IP | 10 per minute |
| Organization-scoped routes, bearer session | user      | 300 per minute |
| Organization-scoped routes, API key        | API key   | 600 per minute |

- **Enabled by default in production**, off elsewhere, so a local checkout is not
  throttled while a deployment is protected without configuration.
- **Headers on every limited response:** `X-RateLimit-Limit`,
  `X-RateLimit-Remaining`, and `X-RateLimit-Reset`. A rejected request also gets
  `Retry-After` and the standard `RATE_LIMITED` error envelope with HTTP 429.
- **Buckets are keyed by a hash**, so a user id or IP address never appears in a
  Redis key.
- **A Redis outage fails open by default** (`RATE_LIMIT_FAIL_OPEN=true`): the
  failure is logged and the request proceeds, because a limiter outage must not
  become an API outage. Set it to `false` to reject instead.

Limits are applied centrally in the router, so a new feature module is covered by
default. Requests rejected before authentication (a missing credential) do not
consume budget.

## Background jobs

`cmd/worker` consumes jobs from a Redis-backed queue and runs them outside the
request path. The queue is built on Redis Streams with a consumer group, which
gives at-least-once delivery, an explicit acknowledgement step, and a
pending-entries list that identifies work abandoned by a crashed worker.

- **Encrypted payloads.** Job payloads are sealed with AES-GCM before they reach
  Redis, because a payload can carry a credential (the invitation email job
  carries the one-time token). Set `JOB_ENCRYPTION_KEY`, or let it be derived
  from `JWT_SECRET`. There is no unencrypted mode.
- **Retries with backoff.** A failed job is rescheduled with exponential backoff
  and jitter between `WORKER_RETRY_BASE_DELAY` and `WORKER_RETRY_MAX_DELAY`, up
  to `WORKER_MAX_ATTEMPTS` attempts. Delayed work is held in a sorted set and
  promoted by the workers, so nothing blocks or polls per job.
- **Dead letters.** A job that is permanently rejected or out of attempts is moved
  to the Redis stream `teamflow:jobs:dead` with its last error, and every attempt
  logs the job id and type.
- **Safe shutdown.** On `SIGINT`/`SIGTERM` the workers stop claiming work and
  drain the job in flight within `WORKER_SHUTDOWN_TIMEOUT`. Unfinished work stays
  pending and is reclaimed after `WORKER_STALE_AFTER`.
- **Tenant-scoped handlers.** Maintenance jobs enumerate active organizations and
  run their statements inside each organization's own transaction.

Job types:

| Job type                  | Runs on | Purpose                                                        |
| ------------------------- | ------- | -------------------------------------------------------------- |
| `invitation.email`        | API     | Delivers one invitation email; records `notified_at` on success |
| `invitations.redeliver`   | Worker  | Re-queues invitations never handed to the queue, rotating a token |
| `api_keys.expire_sweep`   | Worker  | Revokes API keys that expired more than 30 days ago            |

`invitations.redeliver` and `api_keys.expire_sweep` re-schedule themselves after
each run. If the queue is unreachable when an invitation is created, the API
falls back to delivering the email inline and records the delivery, so a Redis
outage never loses an invitation.

Inspect queue state with `redis-cli`:

```bash
redis-cli XLEN  teamflow:jobs:stream   # ready to be claimed
redis-cli XPENDING teamflow:jobs:stream teamflow-workers
redis-cli ZCARD  teamflow:jobs:retry   # delayed retries
redis-cli XLEN  teamflow:jobs:dead     # dead-lettered jobs
```

## Roadmap

- [x] **Phase 1 — Foundation:** config, logging, PostgreSQL, Redis, HTTP server,
      router, request ID / recovery / logging / security middleware, health &
      readiness, migrations, Docker, Compose, Makefile.
- [x] Phase 2 — Authentication (users, JWT, refresh tokens)
- [x] **Phase 3 — Multi-tenancy (organizations, memberships, RLS)**
- [x] **Phase 4 — RBAC:** permissions, custom role management, and member role assignment
- [x] Phase 5 — Teams: organization-scoped teams, team memberships, and authorization
- [x] Phase 6 — Projects: filtering, sorting, pagination, authorization, and activity logging
- [x] Phase 7 — Tasks: assignment, statuses, priorities, due dates, and task activity
- [x] Phase 8 — Invitations: hashed single-use tokens, expiration, acceptance,
      and email delivery
- [x] Phase 9 — API keys: one-time display, hashed storage, authentication,
      expiration, and revocation
- [x] Phase 10 — Background jobs: Redis queue, worker pool, retries, backoff, and
      dead-letter handling
- [x] Phase 11 — Rate limiting: Redis token buckets for authentication, users, and
      API keys
- [x] Phase 12 — Audit and observability: append-only audit log, Prometheus
      metrics, and trace propagation
- [ ] Phase 13 — Testing
- [ ] Phase 14 — Production hardening

Architecture decisions are recorded in [`docs/decisions.md`](docs/decisions.md).
