# TeamFlow

TeamFlow is a production-grade, multi-tenant SaaS backend for project and
employee management. Multiple independent organizations share the same
infrastructure while their data stays strictly isolated.

This repository is being built incrementally, phase by phase. **Phase 1
(Foundation) is complete.** See [Roadmap](#roadmap) for what is done and what
comes next.

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
  cache/       Redis client
  config/      Environment configuration + validation
  database/    PostgreSQL (pgx) connection pool
  health/      Liveness and readiness handlers
  httpx/       Response envelope + typed error mapping
  middleware/  Reusable HTTP middleware
  observability/ Structured logging (and later metrics/tracing)
migrations/    Versioned SQL migrations (golang-migrate)
queries/       SQL for sqlc (later phases)
```

## Technology stack

| Technology | Why |
|------------|-----|
| **Go** | Fast, statically compiled, excellent concurrency for API + workers |
| **PostgreSQL 16** | Strong relational integrity, constraints, and Row Level Security for tenant isolation |
| **Redis** | Rate limiting, caching, and the background job queue |
| **pgx** | High-performance PostgreSQL driver and pool (no ORM; explicit SQL) |
| **chi** | Lightweight, idiomatic `net/http` router |
| **slog** | Structured JSON logging from the standard library |
| **golang-migrate** | Versioned, reversible schema migrations |
| **Docker / Compose** | Reproducible local environment and production images |

An ORM is intentionally avoided in favor of explicit SQL (`sqlc` is introduced
in a later phase) for predictable queries and easier tenant-scoping review.

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

Run the worker in another shell:

```bash
make worker
```

## Testing

```bash
make test        # all unit tests
make test-race   # race detector (requires cgo / a C compiler)
make cover       # coverage summary
```

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
Required values (`DATABASE_URL`, `REDIS_URL`) are validated at startup and the
process **fails fast** with a clear message if anything is missing or invalid.
Secrets are never committed; `.env` is gitignored.

## Observability

- **Structured JSON logs** via `slog`, one line per request with method, path,
  status, duration, and a correlating `request_id`.
- Every request is assigned an `X-Request-ID` (honored if the client supplies
  one) and it is propagated through `context.Context`.
- Metrics and optional tracing are added in a later phase.

## Health checks

- `GET /health` — liveness; always 200 if the process is up (no dependency
  checks, so a slow dependency never triggers a restart).
- `GET /ready` — readiness; verifies PostgreSQL and Redis, returns 503 if any
  dependency is unavailable, without leaking infrastructure detail.

## Graceful shutdown

On `SIGINT`/`SIGTERM` the API stops accepting new connections, drains in-flight
requests within `HTTP_SHUTDOWN_TIMEOUT`, then closes Redis and PostgreSQL and
exits cleanly. The worker follows the same pattern.

## Multi-tenancy, auth, RBAC, jobs

These are implemented in later phases; see the roadmap. The design principle:
every tenant-owned row carries `organization_id`, queries are always
tenant-scoped, and the active tenant is derived from trusted auth context —
never from a client-supplied header.

## Roadmap

- [x] **Phase 1 — Foundation:** config, logging, PostgreSQL, Redis, HTTP server,
  router, request ID / recovery / logging / security middleware, health &
  readiness, migrations, Docker, Compose, Makefile.
- [ ] Phase 2 — Authentication (users, JWT, refresh tokens)
- [ ] Phase 3 — Multi-tenancy (organizations, memberships, RLS)
- [ ] Phase 4 — RBAC
- [ ] Phase 5 — Teams
- [ ] Phase 6 — Projects
- [ ] Phase 7 — Tasks
- [ ] Phase 8 — Invitations
- [ ] Phase 9 — API keys
- [ ] Phase 10 — Background jobs
- [ ] Phase 11 — Rate limiting
- [ ] Phase 12 — Audit & observability
- [ ] Phase 13 — Testing
- [ ] Phase 14 — Production hardening

Architecture decisions are recorded in [`docs/decisions.md`](docs/decisions.md).
