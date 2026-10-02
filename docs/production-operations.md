# Production Operations

This is a vendor-neutral deployment checklist and recovery runbook. The root
`docker-compose.yml` is for local development only; it publishes services on
loopback with development credentials and is not a production deployment
manifests file.

## Deployment Requirements

- Terminate HTTPS at a maintained ingress or load balancer. Forwarded client-IP
  headers are used for authentication rate limiting, so the trusted edge must
  strip client-supplied `Forwarded` and `X-Forwarded-For` headers and set its own.
- Run API and worker images as the provided non-root user. Pin image digests in
  the deployment system, scan images before release, and use a read-only root
  filesystem with dropped Linux capabilities and `no-new-privileges` where the
  runtime supports them.
- Keep PostgreSQL and Redis on private networks. Use PostgreSQL `sslmode=verify-full`
  (or `verify-ca`) and a `rediss://` Redis URL. Do not publish database or cache
  ports to the public internet.
- Configure the API and worker with `APP_ENV=production`. Startup rejects
  malformed typed environment variables, unverified database transport,
  non-TLS Redis, non-HTTPS invitation origins, the sample JWT secret, and an
  absent, weak, or JWT-reused job encryption key.
- Run migrations with a dedicated schema-owner credential. The API and worker
  must use a separate role that is neither a PostgreSQL superuser nor has
  `BYPASSRLS`; production startup checks those role attributes.
- Keep `/metrics` and the worker metrics listener private to the monitoring
  network. Do not expose them through the public ingress. Alert on readiness
  failures, worker queue depth, dead letters, and sustained job failures.
- Set `INVITATION_BASE_URL` to the HTTPS web-client origin. `MAIL_TRANSPORT=none`
  disables invitation delivery; the log transport is rejected in production.
  A production email transport must be installed before invitation workflows
  are enabled for customers.

## Secrets

Provision secrets through the deployment platform's secret manager, not a
committed `.env` file or image build argument. At minimum, keep separate values
for:

- `JWT_SECRET`: at least 32 characters and generated randomly.
- `JOB_ENCRYPTION_KEY`: at least 32 characters and independent from the JWT
  secret. Retain the current key while encrypted Redis jobs may still exist.
- PostgreSQL migration-owner and application-role credentials.
- Redis authentication credentials, if required by the managed service.

Changing `JWT_SECRET` invalidates outstanding access tokens. Changing
`JOB_ENCRYPTION_KEY` makes queued payloads encrypted under the previous key
unreadable; drain or deliberately expire queued jobs before rotation. API key
secrets are only shown at creation, so revoke and replace a compromised key
rather than attempting to recover it.

## Release Procedure

1. Verify the release image and configuration in staging, including database
   TLS, Redis TLS, the non-superuser role, email delivery, and private metrics.
2. Take or verify a restorable PostgreSQL backup before applying migrations.
3. Apply migrations using the schema-owner URL, separately from the application
   deployment. Check the migration version and logs before rolling out binaries.
4. Deploy the API and worker. Use expand/contract changes: first add compatible
   schema, then deploy code that uses it, and only remove old schema in a later
   release after old binaries are gone.
5. Check `/health`, `/ready`, a representative authenticated request, worker
   processing, audit writes, and metrics from the private monitoring network.
6. Watch error rates, queue depth, dead letters, database saturation, and
   invitation delivery before increasing traffic.

Prefer a forward fix over rolling back a migration after new code has written
data in the new format. A migration's down file is useful for development and
tests; it is not a production recovery plan.

## Backups and Restore

- Choose and document PostgreSQL recovery point and recovery time objectives
  before launch. Use managed point-in-time recovery or scheduled encrypted
  backups, and retain backups outside the database failure domain.
- Store Redis persistence according to the queue recovery objective. Jobs are
  at-least-once; preserve the job encryption key alongside the recovery
  procedure, but keep that key in the secret manager rather than in a database
  backup.
- Run a restore exercise in an isolated environment before launch and on a
  regular schedule. Validate migrations, tenant isolation, API startup, worker
  startup, and a sample business workflow after restore.
- For a logical backup where the provider does not supply point-in-time
  recovery, use a protected PostgreSQL service definition and restricted
  credential file rather than placing passwords in shell history:

  ```bash
  pg_dump --format=custom --file="$BACKUP_FILE" teamflow
  pg_restore --list "$BACKUP_FILE"
  ```

- Restore into a new isolated database first. Verify the dump and application
  behavior there before any controlled recovery cutover; never test restore by
  overwriting the live database.

## Incident Checks

- **Database readiness failure:** check provider status, TLS certificates,
  connection limits, and the application role. Do not switch the API to a
  superuser to bypass a role or RLS failure.
- **Redis readiness failure:** inspect connectivity and queue health. The rate
  limiter intentionally fails open by default and emits a warning; decide
  explicitly whether a deployment should set `RATE_LIMIT_FAIL_OPEN=false`.
- **Dead-letter growth:** inspect job type and safe last-error metadata. Do not
  print or copy decrypted job payloads into tickets or logs.
- **Credential exposure:** revoke API keys, rotate JWT signing material through
  the normal rollout, and follow the queue-drain requirement before rotating
  its encryption key.
- **Tenant access concern:** preserve audit records and logs, restrict access,
  and reproduce using the non-superuser application role. Superuser tests do not
  demonstrate that RLS is effective.

## API Contract

The machine-readable OpenAPI contract is [`openapi.yaml`](openapi.yaml). The
Postman collection remains available for interactive examples at
[`TeamFlow.postman_collection.json`](TeamFlow.postman_collection.json). Review
both when adding or changing routes, request fields, response envelopes, auth,
or query parameters.

## Current Deployment Gaps

This repository does not select a cloud provider, provision managed databases,
include a real email transport, configure alerting dashboards, or export
OpenTelemetry traces. Those choices require an operator's infrastructure and
retention requirements; the API propagates trace identifiers but exports no
telemetry by default.
