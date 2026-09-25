# TeamFlow AI Integration Plan

## Purpose

TeamFlow will add AI only after the core product is stable: authentication,
organizations, memberships, RBAC, teams, projects, tasks, invitations, API
keys, jobs, rate limiting, audit logging, and observability should be designed
and verified first.

AI is an assistive capability. It must never replace deterministic
authorization, tenant isolation, database constraints, or human confirmation
for consequential mutations.

## Recommended First Feature

### Generate Tasks From Project Requirements

A user submits project requirements, meeting notes, or a short description. AI
returns a structured draft containing:

- Task title
- Description
- Suggested priority
- Suggested due date, when enough information exists
- Suggested team or assignee, when confidence is sufficient
- Questions or missing information

The API stores the result as a draft. The user reviews, edits, and confirms the
individual tasks before TeamFlow creates them. AI never silently creates,
deletes, assigns, or reprioritizes production data.

This feature is a good first integration because it is useful, bounded, easy to
review, and directly connected to the project and task domain.

## Future AI Features

- Project, team, and task summaries
- Weekly progress and delivery-risk reports
- Natural-language search over tenant-authorized work data
- Meeting-note and document task extraction
- Workload and stalled-project insights
- Organization support assistant using tenant-scoped knowledge
- Duplicate-task and similar-project detection
- Security and operational anomaly detection

## Delivery Prerequisites

AI work begins after these foundations are complete and tested:

1. Authentication and refresh-token security
2. Organization membership and tenant isolation
3. RBAC and service-layer authorization
4. Teams, projects, and tasks
5. Invitations and API keys
6. Redis-backed jobs with retries and idempotency
7. Rate limiting
8. Audit logs, metrics, structured logging, and request IDs

The AI plan should not block core CRUD, authorization, or workflow delivery.

## Architecture

```text
HTTP API
  |
  v
AI application service
  |
  +-- Authorization and tenant context
  +-- Prompt/template registry
  +-- Provider interface
  +-- Structured output validation
  +-- Usage and cost recorder
  |
  v
Redis job queue ----> Worker process ----> AI provider
  |
  +-- PostgreSQL: requests, drafts, usage, audit records
  +-- Optional vector store: tenant-scoped embeddings
```

### Provider interface

The application should depend on an interface rather than a vendor SDK:

```go
type Provider interface {
    Generate(ctx context.Context, request Request) (Response, error)
}
```

The provider adapter owns authentication, timeouts, retries allowed by the
provider contract, request serialization, response parsing, and provider
specific errors. The domain service owns authorization, prompt inputs,
validation, persistence, and confirmation rules.

The initial implementation should support one provider and a fake provider for
unit tests. A second provider can be added later without changing handlers or
domain services.

## API Design

All AI endpoints are under `/api/v1/organizations/{orgID}/ai` and require a
valid bearer token plus active organization membership.

Initial endpoints:

| Method   | Path                                | Permission                      | Purpose                            |
| -------- | ----------------------------------- | ------------------------------- | ---------------------------------- |
| `POST`   | `/projects/{projectID}/task-drafts` | project read/manage policy      | Queue task-draft generation        |
| `GET`    | `/ai/requests/{requestID}`          | requester or authorized reader  | Read generation status and result  |
| `POST`   | `/ai/requests/{requestID}/confirm`  | task creation permission        | Confirm selected drafts into tasks |
| `DELETE` | `/ai/requests/{requestID}`          | requester or authorized manager | Cancel a pending request           |

The exact permission names should be finalized with the Projects and Tasks
phases. AI must not invent a permission bypass; it must use the same project,
team, task, and organization authorization services as non-AI operations.

Responses should expose safe request status such as `queued`, `running`,
`completed`, `failed`, `cancelled`, or `expired`. Provider prompts, raw
credentials, and internal stack traces are never returned to clients.

## Data Model

A first migration may add:

### `ai_requests`

- `id`
- `organization_id`
- `requested_by`
- `feature`
- `status`
- `input_hash`
- `prompt_version`
- `provider`
- `model`
- `error_code`
- `created_at`
- `started_at`
- `completed_at`
- `expires_at`

### `ai_task_drafts`

- `id`
- `ai_request_id`
- `organization_id`
- `title`
- `description`
- `priority`
- `due_date`
- `team_id`
- `assignee_id`
- `confidence`
- `selected`
- `created_at`

Every AI-owned table must include `organization_id`, tenant indexes, foreign
keys, appropriate check constraints, and RLS policies. Drafts must reference
resources from the same organization through service validation and, where
possible, composite foreign keys.

Raw prompts and model responses should not be stored by default. If product
requirements require retention for debugging, retention must be explicit,
redacted, encrypted where appropriate, access-controlled, and time-limited.

## Tenant Isolation and Privacy

Every AI request must:

1. Authenticate the caller.
2. Resolve the organization from active membership.
3. Check the current database permission.
4. Validate every project, team, task, assignee, and document ID in that
   organization.
5. Set transaction-local `app.current_org_id` for tenant-scoped database work.
6. Send only the minimum authorized data to the provider.
7. Persist results with the same organization ID and RLS protections.

Never use a global embedding index or cache key for tenant data. Use keys and
vector namespaces that include the organization ID. A user switching
organizations must not retain access to a previous organization's AI context.

Do not send passwords, tokens, API keys, invitation tokens, authorization
headers, or unrelated personal data to an AI provider. Document provider data
retention, training usage, regional processing, and deletion behavior before
production enablement.

## Prompt and Output Management

- Store prompts as versioned templates in the repository or a controlled
  registry.
- Include a prompt version in every AI request and audit record.
- Use structured JSON output with schema validation.
- Reject malformed, incomplete, oversized, or unsafe output.
- Treat model output as untrusted input and run normal validation before any
  database write.
- Set explicit input, output, and context-size limits.
- Do not allow model-generated SQL, shell commands, authorization decisions, or
  arbitrary tool calls.
- Separate user content from system instructions to reduce prompt injection.
- Label generated content as AI-generated until a user confirms it.

## Jobs and Reliability

AI generation should run through the worker, not block an HTTP request.

Required job behavior:

- Idempotency key per user request
- Context cancellation and provider timeout
- Bounded retries for transient provider failures
- Exponential backoff
- Maximum attempt count
- Dead-letter or permanently failed status
- Safe cancellation
- Duplicate completion protection
- Expiration and cleanup of old requests and drafts
- No retry for invalid input, authorization failure, or malformed output

The API should return a request ID quickly. Clients poll the status endpoint or
use a later notification mechanism when that capability exists.

## Cost, Rate, and Abuse Controls

- Apply separate per-user and per-organization AI rate limits.
- Enforce maximum input size, output size, and context size.
- Record input tokens, output tokens, model, provider, latency, and estimated
  cost where the provider exposes them.
- Support organization-level budgets and a kill switch.
- Reject new work when the budget or rate limit is exhausted.
- Do not allow client requests to select arbitrary expensive models.
- Add provider circuit breaking for repeated failures.
- Expose usage only to authorized organization administrators.

## Observability and Audit

Record structured events without sensitive prompt contents:

- AI request accepted
- AI request denied
- AI request queued, started, completed, failed, cancelled, or expired
- Provider timeout or retry
- Output validation failure
- Draft confirmed into tasks
- Budget or rate limit exceeded

Useful metrics include:

- `ai_requests_total`
- `ai_requests_failed_total`
- `ai_request_duration_seconds`
- `ai_tokens_input_total`
- `ai_tokens_output_total`
- `ai_estimated_cost_total`
- `ai_queue_depth`
- `ai_output_validation_failures_total`

Audit records should include request ID, organization ID, actor ID, feature,
prompt version, provider, model, result status, and resource IDs, but not raw
secrets or unrestricted prompt text.

## Testing Strategy

### Unit tests

- Provider adapter request and response mapping
- Fake provider behavior
- Prompt version selection
- Structured output validation
- Input and output size limits
- Cost and rate-limit calculations
- Retry classification
- Idempotency behavior
- Permission checks

### Integration tests

- AI request creation by an authorized member
- Authorization denial for unauthorized roles
- Cross-tenant resource rejection
- RLS behavior as `teamflow_app`
- Queue-to-worker lifecycle
- Retry and permanent failure behavior
- Draft confirmation creates only selected valid tasks
- Rollback when task creation partially fails
- Expiration and cleanup

### Security tests

- Prompt injection resistance
- Data minimization and redaction
- Cross-tenant cache/vector isolation
- No secret leakage in logs or provider payloads
- Budget and rate-limit enforcement
- Replay and duplicate confirmation protection
- Malformed model output handling

## Rollout Plan

1. Add provider interface and fake provider.
2. Add AI request and draft schema with RLS and migration tests.
3. Add prompt versioning and structured output validation.
4. Add worker job and status API with the fake provider.
5. Verify tenant isolation, authorization, retries, and idempotency.
6. Add a real provider behind configuration and a disabled-by-default flag.
7. Enable the feature for internal development organizations.
8. Monitor cost, latency, failures, privacy, and user confirmation rates.
9. Add budgets, operational runbooks, and a provider outage procedure.
10. Expand to summaries and semantic search only after the first workflow is
    stable.

## AI Definition of Done

AI is production-ready only when:

- The first feature has a clear human-confirmation workflow.
- Provider access is behind an injected interface and configurable adapter.
- All requests enforce authentication, active membership, current permission,
  tenant RLS, and resource ownership.
- AI data is minimized, redacted, retention-controlled, and never used as a
  security authority.
- Jobs are idempotent, cancellable, retry-safe, observable, and bounded.
- Structured model output is validated before persistence.
- Rate limits, budgets, model restrictions, and a kill switch exist.
- Audit events and metrics are available without logging sensitive content.
- Unit, integration, tenant-isolation, and security tests pass.
- Provider retention and compliance behavior are documented.
- README, project guide, architecture decisions, API documentation, and
  operational runbooks are updated.
