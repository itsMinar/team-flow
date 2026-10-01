// Package audit implements Phase 12's append-only security audit log.
//
// The audit log answers "who did what, from where, and when" for
// security-relevant actions. It is intentionally separate from the activity log,
// which describes what happened to a resource: activity is about a project, the
// audit log is about who authenticated, who changed access, and which credentials
// were minted.
//
// Recording is best effort by design. A request must not fail because its audit
// row could not be written, so failures are logged and counted rather than
// returned to the caller — except where an event describes a change made inside a
// transaction, in which case the row commits or rolls back with the change.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
)

// Event outcomes.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// Target types, describing what an action was aimed at.
const (
	TargetUser         = "user"
	TargetSession      = "session"
	TargetRefreshToken = "refresh_token"
	TargetOrganization = "organization"
	TargetMembership   = "membership"
	TargetRole         = "role"
	TargetInvitation   = "invitation"
	TargetAPIKey       = "api_key"
)

// Actions. The names are stable strings: they appear in stored rows, in metrics,
// and in whatever the customer later exports them into, so they are grouped by
// subject and never renamed.
const (
	AuthRegistered         = "auth.registered"
	AuthLoginSucceeded     = "auth.login.succeeded"
	AuthLoginFailed        = "auth.login.failed"
	AuthTokensRefreshed    = "auth.tokens.refreshed"
	AuthTokenReuseDetected = "auth.token.reuse_detected"
	AuthLoggedOut          = "auth.logged_out"
	AuthLoggedOutAll       = "auth.logged_out_all"

	OrganizationCreated = "organization.created"
	OrganizationRenamed = "organization.updated"
	RoleCreated         = "role.created"
	RoleUpdated         = "role.updated"
	RoleDeleted         = "role.deleted"
	MemberRoleAssigned  = "member.role_assigned"

	InvitationCreated  = "invitation.created"
	InvitationSent     = "invitation.sent"
	InvitationResent   = "invitation.resent"
	InvitationRevoked  = "invitation.revoked"
	InvitationAccepted = "invitation.accepted"

	APIKeyCreated = "apikey.created"
	APIKeyRevoked = "apikey.revoked"
)

// maxMetadataLength bounds stored metadata so a bug cannot write a huge row.
const maxMetadataLength = 4000

// Event is one audit record.
type Event struct {
	// OrganizationID is nil for events that happen before a tenant is known, such
	// as a login attempt against an email that does not exist.
	OrganizationID *uuid.UUID
	ActorUserID    *uuid.UUID
	Action         string
	Outcome        string
	TargetType     string
	TargetID       string
	IPAddress      string
	UserAgent      string
	RequestID      string
	TraceID        string
	// Metadata carries small, non-sensitive facts about the event. Secrets must
	// never be placed here: an audit log is exported, backed up, and read by
	// people who are not the original actors.
	Metadata map[string]any
}

// WithOrganization returns a copy of the event scoped to an organization.
func (e Event) WithOrganization(orgID uuid.UUID) Event {
	e.OrganizationID = &orgID
	return e
}

// WithActor returns a copy of the event attributed to a user.
func (e Event) WithActor(userID uuid.UUID) Event {
	e.ActorUserID = &userID
	return e
}

// WithRequest returns a copy of the event carrying the caller's network and
// request identity.
func (e Event) WithRequest(ip, userAgent, requestID, traceID string) Event {
	e.IPAddress = ip
	e.UserAgent = userAgent
	e.RequestID = requestID
	e.TraceID = traceID
	return e
}

// WithTarget returns a copy of the event aimed at a resource.
func (e Event) WithTarget(targetType, targetID string) Event {
	e.TargetType = targetType
	e.TargetID = targetID
	return e
}

// WithMetadata returns a copy of the event carrying additional facts.
func (e Event) WithMetadata(metadata map[string]any) Event {
	e.Metadata = metadata
	return e
}

// Failed returns a copy of the event with a failure or denial outcome.
func (e Event) Failed() Event {
	e.Outcome = OutcomeFailure
	return e
}

func (e Event) normalize() Event {
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}
	if e.Action == "" {
		e.Outcome = OutcomeFailure
	}
	return e
}

func (e Event) params() (db.CreateAuditLogParams, error) {
	event := e.normalize()
	if event.Action == "" {
		return db.CreateAuditLogParams{}, fmt.Errorf("audit: an action is required")
	}
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return db.CreateAuditLogParams{}, fmt.Errorf("audit: encode metadata: %w", err)
	}
	if len(raw) > maxMetadataLength {
		metadata = map[string]any{"truncated": true, "bytes": len(raw)}
		if raw, err = json.Marshal(metadata); err != nil {
			return db.CreateAuditLogParams{}, fmt.Errorf("audit: encode metadata: %w", err)
		}
	}
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	return db.CreateAuditLogParams{
		OrganizationID: event.OrganizationID,
		ActorUserID:    event.ActorUserID,
		Action:         event.Action,
		Outcome:        event.Outcome,
		TargetType:     optional(event.TargetType),
		TargetID:       optional(event.TargetID),
		IpAddress:      optional(event.IPAddress),
		UserAgent:      optional(event.UserAgent),
		RequestID:      optional(event.RequestID),
		TraceID:        optional(event.TraceID),
		Metadata:       raw,
	}, nil
}

// Recorder writes audit events. The interface exists so callers can be tested
// without a database.
type Recorder interface {
	// Record writes an event on its own. It never fails the caller's operation.
	Record(ctx context.Context, event Event)
	// RecordTx writes an event inside an existing transaction, so the event and the
	// change it describes commit together. It returns the error so the caller's
	// transaction decides.
	RecordTx(ctx context.Context, q *db.Queries, event Event) error
}

// Counter observes recorded events for metrics. A nil counter is allowed so tests
// and simple callers do not have to provide one.
type Counter interface {
	CountAudit(action, outcome string)
}

// SQLRecorder writes audit events with sqlc.
type SQLRecorder struct {
	q       *db.Queries
	counter Counter
	logger  *slog.Logger
}

// NewSQLRecorder builds a recorder over the generated queries.
func NewSQLRecorder(q *db.Queries, counter Counter, logger *slog.Logger) *SQLRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SQLRecorder{q: q, counter: counter, logger: logger}
}

// Record writes an event outside any caller transaction.
//
// It never returns an error: a request must not fail because its audit row could
// not be written. The failure is logged and counted, so an operator can see a
// silently degraded audit trail through metrics instead of through missing data.
func (r *SQLRecorder) Record(ctx context.Context, event Event) {
	event = event.normalize()
	params, err := event.params()
	if err != nil {
		r.logger.Error("audit event rejected", slog.String("action", event.Action), slog.Any("error", err))
		return
	}
	if _, err := r.q.CreateAuditLog(ctx, params); err != nil {
		r.logger.Error("failed to record audit event",
			slog.String("action", event.Action),
			slog.String("outcome", event.Outcome),
			slog.Any("error", err),
		)
	}
	r.count(event)
}

// RecordTx writes an event inside the caller's transaction, so the record and the
// change it describes commit or roll back together.
func (r *SQLRecorder) RecordTx(ctx context.Context, q *db.Queries, event Event) error {
	params, err := event.params()
	if err != nil {
		return err
	}
	if _, err := q.CreateAuditLog(ctx, params); err != nil {
		return fmt.Errorf("audit: record %s: %w", event.Action, err)
	}
	r.count(event)
	return nil
}

func (r *SQLRecorder) count(event Event) {
	if r.counter != nil {
		r.counter.CountAudit(event.Action, event.Outcome)
	}
}

var _ Recorder = (*SQLRecorder)(nil)

// Entry is the client-safe representation of an audit row.
type Entry struct {
	ID          uuid.UUID       `json:"id"`
	Action      string          `json:"action"`
	Outcome     string          `json:"outcome"`
	ActorUserID *uuid.UUID      `json:"actor_user_id,omitempty"`
	TargetType  *string         `json:"target_type,omitempty"`
	TargetID    *string         `json:"target_id,omitempty"`
	IPAddress   *string         `json:"ip_address,omitempty"`
	UserAgent   *string         `json:"user_agent,omitempty"`
	RequestID   *string         `json:"request_id,omitempty"`
	TraceID     *string         `json:"trace_id,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ToEntry maps a stored row to its client representation.
func ToEntry(row db.ListAuditLogsRow) Entry {
	return Entry{
		ID: row.ID, Action: row.Action, Outcome: row.Outcome, ActorUserID: row.ActorUserID,
		TargetType: row.TargetType, TargetID: row.TargetID, IPAddress: optional(row.IpAddress),
		UserAgent: row.UserAgent, RequestID: row.RequestID, TraceID: row.TraceID,
		Metadata: json.RawMessage(row.Metadata), CreatedAt: row.CreatedAt,
	}
}

// optional turns the empty text used for a missing address back into nil so the
// field is omitted from the response.
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// NopRecorder discards events. It lets callers keep the same call sites without a
// nil check, which is what tests and single-purpose commands use.
func NopRecorder() Recorder { return nopRecorder{} }

type nopRecorder struct{}

func (nopRecorder) Record(context.Context, Event) {}

func (nopRecorder) RecordTx(context.Context, *db.Queries, Event) error { return nil }

var _ Recorder = nopRecorder{}
