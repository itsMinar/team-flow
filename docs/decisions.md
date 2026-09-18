# Architecture Decision Log

Short records of notable engineering decisions. Newest first within each phase.

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
