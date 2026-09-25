# TeamFlow Project Guide

This document explains what TeamFlow can do today, how its parts fit together, how to run it locally, and what remains to be built.

## 1. What TeamFlow Is

TeamFlow is a Go backend for a multi-tenant team and project-management SaaS product. It is designed for multiple organizations to share one deployment and database while keeping organization data isolated.

The project is currently an API foundation with five completed phases:

1. Foundation and infrastructure
2. Authentication and session security
3. Organizations, memberships, and tenant isolation
4. Permission-based role management
5. Teams and team memberships

The codebase is a modular monolith. It has one repository and two executable processes:

- `cmd/api`: HTTP API server
- `cmd/worker`: background-worker process scaffold

The current release is useful for building and testing secure SaaS foundations. It can create users and organizations, authenticate users, manage memberships, enforce tenant boundaries, and manage organization teams. Projects and tasks remain planned.

## 2. What You Can Do Today

### Authentication

- Register a new user and organization together.
- Automatically create the first Owner membership during registration.
- Log in with email and password.
- Receive a short-lived JWT access token and a long-lived refresh token.
- Refresh sessions with refresh-token rotation.
- Log out one refresh-token family.
- Log out all refresh sessions for the current user.
- Read the authenticated user's profile with `/me`.
- Validate JWT signature, issuer, token type, and expiration.
- Store only SHA-256 hashes of refresh tokens in PostgreSQL.
- Detect refresh-token reuse and revoke the entire token family.

### Organizations and memberships

- List organizations that the authenticated user belongs to.
- Create another organization for the current user.
- Automatically provision default roles when an organization is created:
  - Owner
  - Admin
  - Manager
  - Member
  - Viewer
- Automatically create the creator's Owner membership.
- Read an organization visible to the current user.
- Rename an organization as an Owner or Admin.
- List members of an organization.
- List roles and their permissions.
- Create, update, and delete custom roles.
- Assign roles to active organization members.
- Enforce permissions for organization, member, and role operations.
- Return `404 Not Found` for unknown or cross-tenant organizations so their existence is not exposed.
- Treat inactive memberships as unable to access tenant data.

### Teams

- Create, read, update, and delete organization teams.
- List team members and add or remove active organization members.
- Enforce `teams.read` for reads and `teams.manage` for mutations.
- Reject duplicate team names within an organization.
- Keep team data isolated with explicit organization scope, composite foreign
  key protection, and PostgreSQL RLS.

### Operations and reliability

- Liveness endpoint for process checks.
- Readiness endpoint that checks PostgreSQL and Redis.
- Structured JSON logs using Go's `slog`.
- Request IDs through the `X-Request-ID` header and request context.
- Recovery, logging, security-header, and request-body-size middleware.
- Graceful API shutdown on `SIGINT` and `SIGTERM`.
- PostgreSQL connection pooling through `pgxpool`.
- Redis connectivity for the API and worker dependency graph.
- Reproducible local infrastructure through Docker Compose.

## 3. Current HTTP API

The API listens on `http://localhost:8080` by default.

All successful responses use this envelope:

```json
{
  "data": {}
}
```

Errors use this envelope:

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable message",
    "request_id": "request-id"
  }
}
```

The `request_id` helps correlate a client error with server logs.

### Operational endpoints

| Method | Path      | Auth   | Description                                                                             |
| ------ | --------- | ------ | --------------------------------------------------------------------------------------- |
| `GET`  | `/health` | Public | Liveness check. Returns success if the process is running.                              |
| `GET`  | `/ready`  | Public | Readiness check for PostgreSQL and Redis. Returns `503` if a dependency is unavailable. |

### Authentication endpoints

All authentication routes are under `/api/v1/auth`.

| Method | Path          | Auth                       | Description                                                                 |
| ------ | ------------- | -------------------------- | --------------------------------------------------------------------------- |
| `POST` | `/register`   | Public                     | Creates a user, organization, default roles, Owner membership, and session. |
| `POST` | `/login`      | Public                     | Verifies credentials and creates a session.                                 |
| `POST` | `/refresh`    | Refresh token in JSON body | Rotates the refresh token and returns a new access token.                   |
| `POST` | `/logout`     | Refresh token in JSON body | Revokes the presented refresh-token family.                                 |
| `POST` | `/logout-all` | Bearer access token        | Revokes all refresh tokens for the current user.                            |
| `GET`  | `/me`         | Bearer access token        | Returns the current user's profile.                                         |

#### Register example

```bash
curl -sS -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{
    "email": "owner@example.com",
    "password": "correct-horse-123",
    "first_name": "Ada",
    "last_name": "Owner",
    "organization_name": "Example Company"
  }'
```

A successful response contains `access_token`, `refresh_token`, `token_type`, `expires_in`, and a safe user object. The password hash is never returned.

#### Login example

```bash
curl -sS -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{
    "email": "owner@example.com",
    "password": "correct-horse-123"
  }'
```

#### Authenticated request example

```bash
curl -sS http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

#### Refresh example

```bash
curl -sS -X POST http://localhost:8080/api/v1/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refresh_token":"YOUR_REFRESH_TOKEN"}'
```

Clients should serialize refresh requests. Sending the same refresh token concurrently can be interpreted as token reuse and revoke the token family.

### Organization endpoints

All organization routes require a valid bearer access token and are under `/api/v1/organizations`.

| Method   | Path                                                 | Authorization          | Description                                              |
| -------- | ---------------------------------------------------- | ---------------------- | -------------------------------------------------------- |
| `GET`    | `/organizations`                                     | Any authenticated user | Lists organizations where the user has a membership.     |
| `POST`   | `/organizations`                                     | Any authenticated user | Creates an organization and assigns the caller as Owner. |
| `GET`    | `/organizations/{orgID}`                             | Active member          | Reads organization details and the caller's role.        |
| `PATCH`  | `/organizations/{orgID}`                             | `organizations.update` | Renames an organization.                                 |
| `GET`    | `/organizations/{orgID}/members`                     | `members.read`         | Lists members of the organization.                       |
| `PATCH`  | `/organizations/{orgID}/members/{membershipID}/role` | `members.manage`       | Assigns an active member a role.                         |
| `GET`    | `/organizations/{orgID}/roles`                       | `roles.read`           | Lists roles and permissions.                             |
| `POST`   | `/organizations/{orgID}/roles`                       | `roles.manage`         | Creates a custom role.                                   |
| `PATCH`  | `/organizations/{orgID}/roles/{roleID}`              | `roles.manage`         | Updates a custom role and its permissions.               |
| `DELETE` | `/organizations/{orgID}/roles/{roleID}`              | `roles.manage`         | Deletes an unused custom role.                           |

### Team endpoints

| Method   | Path                                                           | Authorization  | Description                                   |
| -------- | -------------------------------------------------------------- | -------------- | --------------------------------------------- |
| `GET`    | `/organizations/{orgID}/teams`                                 | `teams.read`   | Lists teams in the organization.              |
| `POST`   | `/organizations/{orgID}/teams`                                 | `teams.manage` | Creates a team.                               |
| `GET`    | `/organizations/{orgID}/teams/{teamID}`                        | `teams.read`   | Reads a team.                                 |
| `PATCH`  | `/organizations/{orgID}/teams/{teamID}`                        | `teams.manage` | Updates a team.                               |
| `DELETE` | `/organizations/{orgID}/teams/{teamID}`                        | `teams.manage` | Deletes a team and its memberships.           |
| `GET`    | `/organizations/{orgID}/teams/{teamID}/members`                | `teams.read`   | Lists team members.                           |
| `POST`   | `/organizations/{orgID}/teams/{teamID}/members`                | `teams.manage` | Adds an active organization member to a team. |
| `DELETE` | `/organizations/{orgID}/teams/{teamID}/members/{teamMemberID}` | `teams.manage` | Removes a team membership.                    |

Team create and update requests use `{"name":"Engineering","description":"Platform work"}`.
Team membership requests use `{"user_id":"<organization-member-uuid>"}`.

#### Create an organization

```bash
curl -sS -X POST http://localhost:8080/api/v1/organizations \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Second Company"}'
```

#### List members

```bash
curl -sS http://localhost:8080/api/v1/organizations/$ORG_ID/members \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

Organization IDs are UUIDs returned by the organization endpoints.

Role create and update requests use this shape:

```json
{
  "name": "Project Lead",
  "description": "Can manage project work",
  "permissions": ["organizations.read", "members.read"]
}
```

The built-in permissions are `organizations.read`, `organizations.update`,
`members.read`, `members.manage`, `roles.read`, and `roles.manage`. Owner and
Admin receive all permissions by default. Manager, Member, and Viewer receive
read permissions. System roles are immutable, and a role with assigned members
cannot be deleted.

## 4. Architecture

```mermaid
graph TD
    Client[HTTP client] --> API[cmd/api]
    API --> PG[(PostgreSQL)]
    API --> Redis[(Redis)]
    Worker[cmd/worker] --> PG
    Worker --> Redis
```

The API starts by loading configuration, creating the logger, connecting to PostgreSQL and Redis, wiring services and handlers, and starting the HTTP server.

The request middleware chain is:

```text
Recovery -> Request ID -> Request logging -> Security headers -> Max body size -> Handler
```

The intended application layering is:

```text
HTTP handler -> Service/use case -> SQL repository/generated queries -> PostgreSQL
```

Handlers decode requests and write responses. Services contain business rules and authorization decisions. SQL is kept explicit and generated database access is kept in `internal/db`.

### Important packages

| Package                  | Responsibility                                                                    |
| ------------------------ | --------------------------------------------------------------------------------- |
| `internal/api`           | Top-level router and dependency wiring.                                           |
| `internal/auth`          | Registration, login, password hashing, JWTs, refresh tokens, and auth middleware. |
| `internal/authctx`       | Stores the authenticated principal and resolved tenant in request context.        |
| `internal/cache`         | Redis client creation and connectivity.                                           |
| `internal/config`        | Environment loading, defaults, and startup validation.                            |
| `internal/database`      | PostgreSQL pool creation and database health checks.                              |
| `internal/db`            | SQLC-generated queries, models, and database interfaces.                          |
| `internal/health`        | Liveness and readiness handlers.                                                  |
| `internal/httpx`         | JSON envelopes, decoding, typed errors, and client-safe error mapping.            |
| `internal/middleware`    | Shared HTTP middleware.                                                           |
| `internal/observability` | Structured logging and request ID support.                                        |
| `internal/organizations` | Organization services, handlers, routes, and tenant resolution.                   |
| `internal/teams`         | Team services, handlers, routes, authorization, and team memberships.             |
| `internal/validation`    | Reusable request validation.                                                      |

## 5. Repository Layout

```text
cmd/
  api/main.go              API entrypoint and graceful shutdown
  worker/main.go           Worker entrypoint; job processing is planned

internal/
  api/                     Router wiring
  auth/                    Authentication and sessions
  authctx/                 Request authentication context
  cache/                   Redis client
  config/                  Environment configuration
  database/                PostgreSQL pool
  db/                      Generated SQLC code and models
  health/                  Health and readiness checks
  httpx/                   HTTP response and error helpers
  middleware/              Shared HTTP middleware
  observability/            JSON logging and request IDs
  organizations/            Organization and tenant logic
  teams/                    Team and team-membership logic
  validation/              Request validation

migrations/                Versioned PostgreSQL migrations
queries/                   SQL source files used by SQLC
scripts/                   Project scripts

tests/                     Database-backed tenant isolation tests
docs/                      Architecture decisions and project documentation
Dockerfile                 Multi-stage non-root runtime image
docker-compose.yml         Local PostgreSQL, Redis, API, and worker services
Makefile                   Development and maintenance commands
sqlc.yaml                 SQLC configuration
go.mod                    Go module and dependencies
```

## 6. Local Development

### Prerequisites

Install or have available:

- Go 1.26 or newer
- Docker and Docker Compose
- GNU Make
- A shell such as Bash

### Initial setup

```bash
cp .env.example .env
make docker-up
make migrate-up
```

The Compose stack uses these host ports:

- PostgreSQL: `5433`
- Redis: `6379`
- API: `8080`

The host `.env` uses PostgreSQL port `5433` because port `5432` may already be used by a local PostgreSQL installation.

### Run the API locally

```bash
make dev
```

This runs `go run ./cmd/api` on the host and reads values from `.env`.

If the Compose API is already running, it owns port `8080`. Stop only that service before running the host API:

```bash
docker compose stop api
make dev
```

To use the containerized API again:

```bash
docker compose start api
```

Do not run both API modes on the same port.

### Run the worker locally

In a second terminal:

```bash
make worker
```

The worker currently connects to PostgreSQL and Redis, logs that it is ready, and waits for shutdown. Actual background-job processing is planned for a later phase.

### Run everything in Docker

```bash
make docker-up
```

This builds and starts PostgreSQL, Redis, API, and worker containers. View status with:

```bash
docker compose ps
```

View logs with:

```bash
docker compose logs -f api
```

Stop the stack with:

```bash
make docker-down
```

## 7. Configuration

Configuration is loaded from environment variables at startup. Invalid configuration causes the process to exit before it serves traffic.

| Variable                      | Default       | Purpose                                                               |
| ----------------------------- | ------------- | --------------------------------------------------------------------- |
| `APP_ENV`                     | `development` | Runtime environment: `development`, `test`, or `production`.          |
| `APP_PORT`                    | `8080`        | HTTP port for the API.                                                |
| `DATABASE_URL`                | none          | PostgreSQL connection URL. Required.                                  |
| `DATABASE_MAX_CONNS`          | `20`          | Maximum PostgreSQL pool connections.                                  |
| `DATABASE_MIN_CONNS`          | `2`           | Minimum PostgreSQL pool connections.                                  |
| `DATABASE_MAX_CONN_LIFETIME`  | `1h`          | Maximum connection lifetime.                                          |
| `DATABASE_MAX_CONN_IDLE_TIME` | `30m`         | Maximum idle connection time.                                         |
| `REDIS_URL`                   | none          | Redis connection URL. Required.                                       |
| `HTTP_READ_TIMEOUT`           | `15s`         | HTTP request read timeout.                                            |
| `HTTP_WRITE_TIMEOUT`          | `15s`         | HTTP response write timeout.                                          |
| `HTTP_IDLE_TIMEOUT`           | `60s`         | Keep-alive idle timeout.                                              |
| `HTTP_SHUTDOWN_TIMEOUT`       | `15s`         | Graceful shutdown timeout.                                            |
| `HTTP_MAX_BODY_BYTES`         | `1048576`     | Maximum request body size, 1 MiB by default.                          |
| `LOG_LEVEL`                   | `info`        | `debug`, `info`, `warn`, or `error`.                                  |
| `JWT_SECRET`                  | none          | Signing secret. Required; production requires at least 32 characters. |
| `JWT_ISSUER`                  | `teamflow`    | JWT issuer claim.                                                     |
| `JWT_ACCESS_TTL`              | `15m`         | Access-token lifetime.                                                |
| `JWT_REFRESH_TTL`             | `720h`        | Refresh-token lifetime and must exceed access TTL.                    |

Never commit `.env` or production secrets. The example JWT secret is for local development only.

## 8. Database and Tenant Isolation

### Schema and migrations

Database changes are versioned in `migrations/` as paired `.up.sql` and `.down.sql` files. Apply them with the Dockerized migration CLI:

```bash
make migrate-up
make migrate-down
make migrate-create name=add_feature
```

Do not edit an already-applied migration in a shared or production database. Add a new migration instead.

The current migrations cover:

- PostgreSQL extensions
- Organizations
- Users
- Roles
- Memberships
- Refresh tokens
- Refresh-token IP normalization
- Row Level Security policies
- Forced RLS for table owners
- The non-superuser `teamflow_app` login role and privileges
- Permissions and role-permission assignments for RBAC
- Teams and team memberships with composite organization constraints and RLS

### How tenant access works

1. The access token identifies the user.
2. The organization ID comes from the URL path.
3. The server checks that the user has an active membership in that organization.
4. The service resolves the tenant and stores it in request context where needed.
5. Tenant-scoped transactions set `app.current_org_id` for PostgreSQL.
6. Explicit SQL organization filters and PostgreSQL RLS both constrain the data.

The active organization is never trusted from a client-supplied header. Cross-tenant and unknown organization lookups return `404` to avoid leaking whether another organization exists.

The API and worker should connect as a non-superuser application role. PostgreSQL superusers bypass RLS, so using the database owner for application traffic would defeat the RLS backstop. In the Compose stack, `teamflow_app` is used by the API and worker while migrations use the owner connection.

Organization creation is atomic: the organization, default roles, and Owner membership are created in one transaction. A partial organization cannot be observed if provisioning fails.

## 9. Security Model

### Passwords

- Passwords are hashed with bcrypt at cost 12.
- Validation requires letters and digits and enforces the configured minimum policy.
- Passwords over bcrypt's 72-byte UTF-8 limit are rejected.
- Password hashes are never included in API responses.

### Access tokens

- Access tokens use HS256.
- The configured issuer must match.
- The token type must be an access token.
- Expiration is required and checked.
- Protected endpoints require `Authorization: Bearer <token>`.

### Refresh tokens

- Tokens are generated from cryptographically secure random bytes.
- Only SHA-256 hashes are stored.
- Rotation is protected by a PostgreSQL row lock and transaction.
- A revoked-token reuse attempt revokes the whole token family.
- Logout revokes refresh sessions but does not invalidate already-issued access tokens; those remain valid until expiry.

### HTTP protections

- Panic recovery prevents a handler panic from crashing the server.
- Request bodies are size-limited.
- Security headers are added by middleware.
- Internal database and implementation details are logged server-side but not returned to clients.

### Authorization

- Permissions are stored in PostgreSQL and assigned to organization-scoped roles.
- Memberships reference one role per organization; the same user can have a
  different role in another organization.
- Authorization is resolved from the database, never from JWT claims.
- System roles are immutable, custom roles cannot be deleted while assigned, and
  the organization must always retain at least one Owner.
- Teams require `teams.read` or `teams.manage`; a team member must already be
  an active member of the same organization.

## 10. Testing

Run the unit and package tests with:

```bash
make test
```

Run with the race detector:

```bash
make test-race
```

Generate a coverage summary:

```bash
make cover
```

Run static checks:

```bash
make lint
```

The test suite covers configuration validation, middleware, health checks, HTTP routing, validation, password rules, JWT behavior, refresh-token rotation, token reuse, logout behavior, authentication handlers, organization services, RBAC permission mapping and validation, team validation and tenant-scoped team workflows, and tenant isolation.

### Database integration tests

Authentication integration tests require a dedicated database URL in `TEST_DATABASE_URL`. The test database name must end with `_test`; tests never fall back to `DATABASE_URL`.

Example setup:

```bash
docker compose exec postgres createdb -U teamflow teamflow_test
export TEST_DATABASE_URL='postgres://teamflow:teamflow@localhost:5433/teamflow_test?sslmode=disable'
make migrate-up DATABASE_URL="$TEST_DATABASE_URL"
go test -race -count=1 ./...
unset TEST_DATABASE_URL
```

The integration tests truncate authentication and organization tables. Never point `TEST_DATABASE_URL` at a development, staging, or production database. Do not run separate test processes concurrently against the same test database.

## 11. Useful Make Commands

| Command                        | Purpose                                        |
| ------------------------------ | ---------------------------------------------- |
| `make help`                    | Show available commands.                       |
| `make dev`                     | Run the API locally.                           |
| `make worker`                  | Run the worker locally.                        |
| `make build`                   | Build API and worker binaries in `bin/`.       |
| `make test`                    | Run all tests.                                 |
| `make test-race`               | Run tests with the race detector.              |
| `make cover`                   | Produce coverage summary.                      |
| `make fmt`                     | Format Go code and tidy modules.               |
| `make lint`                    | Run `go vet` and `golangci-lint` if installed. |
| `make migrate-up`              | Apply all migrations.                          |
| `make migrate-down`            | Roll back one migration.                       |
| `make migrate-create name=...` | Create a migration scaffold.                   |
| `make sqlc`                    | Generate SQLC code when SQLC is installed.     |
| `make docker-up`               | Build and start the Compose stack.             |
| `make docker-down`             | Stop and remove the Compose stack.             |

## 12. Troubleshooting

### `bind: address already in use`

Another process is already listening on `APP_PORT`, commonly the Compose API container.

```bash
ss -ltnp | grep ':8080'
docker compose ps
docker compose stop api
```

Alternatively, choose another port for the local API:

```bash
APP_PORT=8081 make dev
```

### API cannot connect to PostgreSQL

Check that the database container is healthy and that the host URL uses port `5433`:

```bash
docker compose ps postgres
psql 'postgres://teamflow:teamflow@localhost:5433/teamflow?sslmode=disable'
```

### API cannot connect to Redis

Check the Redis container and URL:

```bash
docker compose ps redis
redis-cli -u redis://localhost:6379/0 ping
```

### Configuration fails at startup

Check that `.env` exists and includes at least:

```dotenv
DATABASE_URL=postgres://teamflow:teamflow@localhost:5433/teamflow?sslmode=disable
REDIS_URL=redis://localhost:6379/0
JWT_SECRET=use-a-long-local-development-secret
```

### Container API starts but local API fails

The container API publishes port `8080`. Stop it before `make dev`, or set a different `APP_PORT`. The API, worker, and migrations also use different database URLs depending on whether they run on the host or inside Compose; host processes use `localhost`, while containers use service names such as `postgres` and `redis`.

## 13. Current Limitations and Roadmap

The following capabilities are planned and should not be assumed to exist yet:

- Projects
- Tasks and task workflows
- Invitations and email-based onboarding
- API keys
- Actual background-job processing
- Redis-backed rate limiting
- Audit and activity logging
- Metrics and optional tracing
- Further production hardening

The planned roadmap is:

1. Foundation - complete
2. Authentication - complete
3. Multi-tenancy - complete
4. RBAC - complete
5. Teams - complete
6. Projects - planned
7. Tasks - planned
8. Invitations - planned
9. API keys - planned
10. Background jobs - planned
11. Rate limiting - planned
12. Audit and observability - planned
13. Testing expansion - planned
14. Production hardening - planned

Architecture decisions and the reasoning behind major security and infrastructure choices are recorded in [docs/decisions.md](decisions.md).

## 14. Suggested Next Development Steps

A practical order for continuing the project is:

1. Add membership management: invite, activate, suspend, and remove members.
2. Add projects with explicit organization and team scoping.
3. Add tasks, statuses, assignments, and due dates.
4. Add background jobs for invitations, notifications, and other asynchronous work.
5. Add rate limiting and audit events before exposing the API publicly.
6. Expand OpenAPI or Postman documentation as each endpoint is added.
7. Add production deployment configuration, secret management, metrics, tracing, backups, and migration runbooks.

## 15. Related Files

- [README.md](../README.md): concise project overview and quick start.
- [docs/decisions.md](decisions.md): architecture decision records.
- [docs/TeamFlow.postman_collection.json](TeamFlow.postman_collection.json): Postman collection.
- [.env.example](../.env.example): local environment template.
- [docker-compose.yml](../docker-compose.yml): local service definitions.
- [Makefile](../Makefile): development commands.
