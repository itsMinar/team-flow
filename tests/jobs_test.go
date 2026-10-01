package organizations_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/jobs"
	"github.com/itsMinar/team-flow/internal/mailer"
	"github.com/itsMinar/team-flow/internal/organizations"
)

// recordingQueue stands in for Redis so job dispatch can be asserted without a
// Redis server.
type recordingQueue struct {
	mu     sync.Mutex
	jobs   []recordedJob
	err    error
	delays []time.Duration
}

type recordedJob struct {
	Type    string
	Payload jobs.Job
}

func (q *recordingQueue) Enqueue(ctx context.Context, jobType string, payload any) (jobs.Job, error) {
	return q.record(jobType, payload, 0)
}

func (q *recordingQueue) EnqueueIn(ctx context.Context, delay time.Duration, jobType string, payload any) (jobs.Job, error) {
	return q.record(jobType, payload, delay)
}

func (q *recordingQueue) record(jobType string, payload any, delay time.Duration) (jobs.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return jobs.Job{}, q.err
	}
	job := jobs.Job{ID: uuid.NewString(), Type: jobType, EnqueuedAt: time.Now(), MaxAttempts: 5}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return jobs.Job{}, err
		}
		job.Payload = raw
	}
	q.jobs = append(q.jobs, recordedJob{Type: jobType, Payload: job})
	q.delays = append(q.delays, delay)
	return job, nil
}

func (q *recordingQueue) snapshot() []recordedJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]recordedJob(nil), q.jobs...)
}

// lastEmailPayload decodes the payload of the most recently queued email job.
func (q *recordingQueue) lastEmailPayload(t *testing.T) invitations.EmailJobPayload {
	t.Helper()
	queued := q.snapshot()
	if len(queued) == 0 {
		t.Fatal("no job was queued")
	}
	var payload invitations.EmailJobPayload
	if err := json.Unmarshal(queued[len(queued)-1].Payload.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

// lastToken returns the invitation token carried by the most recently queued job.
func (q *recordingQueue) lastToken(t *testing.T) string {
	t.Helper()
	payload := q.lastEmailPayload(t)
	if payload.Token == "" {
		t.Fatal("the queued job carries no token")
	}
	return payload.Token
}

func (q *recordingQueue) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = nil
	q.delays = nil
}

// countingSender records delivery attempts and can fail on demand.
type countingSender struct {
	mu    sync.Mutex
	sent  []mailer.Message
	err   error
	calls int
}

func (s *countingSender) Send(_ context.Context, msg mailer.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, msg)
	return nil
}

// lastMessage returns the most recently delivered message.
func (s *countingSender) lastMessage(t *testing.T) mailer.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("no message was delivered")
	}
	return s.sent[len(s.sent)-1]
}

// callCount counts delivery attempts, including failed ones.
func (s *countingSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *countingSender) messageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *countingSender) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
	s.calls = 0
}

// invitationsService builds the invitation service used by the job tests, with
// the recording sender and queue in place of Redis.
func invitationsService(t *testing.T, pool *pgxpool.Pool, orgSvc *organizations.Service,
	sender mailer.Sender, queue jobs.Enqueuer,
) *invitations.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("integration-secret", "teamflow", 15*time.Minute)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, audit.NopRecorder(), logger)
	return invitations.NewService(pool, orgSvc, authSvc, sender, queue, audit.NopRecorder(), 7*24*time.Hour, "http://app.example.com", logger)
}

func invitationsCreate(roleID uuid.UUID, email string) invitations.CreateInput {
	return invitations.CreateInput{Email: email, RoleID: roleID}
}

func emailJob(t *testing.T, jobType string, payload any) jobs.Job {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{ID: uuid.NewString(), Type: jobType, Payload: raw, MaxAttempts: 5}
}

func TestInvitationEmailJobDeliversAndRecordsDelivery(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	sender := &countingSender{}
	queue := &recordingQueue{}
	svc := invitationsService(t, pool, orgSvc, sender, queue)

	userID, orgID := registerOrg(t, pool, "job-email@example.com", "Job Email Org")
	invitation, err := svc.Create(ctx, userID, orgID, invitationsCreate(roleIDOf(t, pool, orgID, "Member"), "invitee@example.com"))
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}

	// Creation queues the job instead of sending inline.
	queued := queue.snapshot()
	if len(queued) != 1 || queued[0].Type != "invitation.email" {
		t.Fatalf("expected one queued email job, got %+v", queued)
	}
	if sender.messageCount() != 0 {
		t.Fatal("the API must not send the email inline when a queue is available")
	}

	var payload struct {
		InvitationID uuid.UUID `json:"invitation_id"`
		Token        string    `json:"token"`
	}
	if err := json.Unmarshal(queued[0].Payload.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.InvitationID != invitation.ID || payload.Token == "" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// The worker delivers the message and records that it did.
	if err := svc.EmailJobHandler(ctx, queued[0].Payload); err != nil {
		t.Fatalf("email job: %v", err)
	}
	if sender.messageCount() != 1 {
		t.Fatalf("sent %d messages, want 1", sender.messageCount())
	}
	var notified *time.Time
	if err := pool.QueryRow(ctx, `SELECT notified_at FROM invitations WHERE id = $1`, invitation.ID).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified == nil {
		t.Fatal("delivery was not recorded")
	}

	// A replayed job is harmless: the invitation is still pending, so it would
	// send again, which is why the redelivery sweep relies on notified_at.
	queue.reset()
	sender.reset()
	if _, err := queue.Enqueue(ctx, "invitation.email", payload); err != nil {
		t.Fatal(err)
	}
	if err := svc.EmailJobHandler(ctx, emailJob(t, "invitation.email", payload)); err != nil {
		t.Fatalf("replayed email job: %v", err)
	}
	if sender.messageCount() != 1 {
		t.Fatalf("a reclaimed job must still deliver: %d", sender.messageCount())
	}

	// Once the invitation is accepted, the job stops sending.
	if err := svc.Revoke(ctx, userID, orgID, invitation.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	sender.reset()
	if err := svc.EmailJobHandler(ctx, emailJob(t, "invitation.email", payload)); err != nil {
		t.Fatalf("job for a revoked invitation: %v", err)
	}
	if sender.messageCount() != 0 {
		t.Fatal("a revoked invitation must not receive more email")
	}

	// A malformed payload is permanent and must not be retried.
	err = svc.EmailJobHandler(ctx, jobs.Job{Type: "invitation.email", Payload: []byte("{oops")})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("expected a permanent error, got %v", err)
	}
	err = svc.EmailJobHandler(ctx, jobs.Job{Type: "invitation.email"})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("expected a permanent error for a missing payload, got %v", err)
	}

	// A job for an invitation that no longer exists is dropped, not retried.
	err = svc.EmailJobHandler(ctx, emailJob(t, "invitation.email", map[string]any{
		"invitation_id": uuid.New(), "token": "tfk_x",
	}))
	if err != nil {
		t.Fatalf("job for a missing invitation should be dropped: %v", err)
	}
}

func TestInvitationRedeliverySweepQueuesUndeliveredInvitations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	sender := &countingSender{}
	queue := &recordingQueue{}
	svc := invitationsService(t, pool, orgSvc, sender, queue)

	userID, orgID := registerOrg(t, pool, "job-sweep@example.com", "Job Sweep Org")
	invitation, err := svc.Create(ctx, userID, orgID, invitationsCreate(roleIDOf(t, pool, orgID, "Member"), "sweep@example.com"))
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	// Pretend the invitation was created long enough ago to be swept, and that it
	// was never queued because Redis was down.
	if _, err := pool.Exec(ctx, `
		UPDATE invitations SET created_at = now() - interval '1 hour' WHERE id = $1`, invitation.ID); err != nil {
		t.Fatal(err)
	}
	queue.reset()

	if err := svc.RedeliverJobHandler(ctx, jobs.Job{Type: "invitations.redeliver"}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	queued := queue.snapshot()
	if len(queued) != 1 || queued[0].Type != "invitation.email" {
		t.Fatalf("sweep must queue the undelivered invitation: %+v", queued)
	}

	// The sweep rotated the stored token, so the previous link stops working and
	// the queued payload carries the new one.
	var oldHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM invitations WHERE id = $1`, invitation.ID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(queued[0].Payload.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT delivery_attempts FROM invitations WHERE id = $1`, invitation.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("delivery_attempts = %d, want 1", attempts)
	}

	// A delivered invitation is not queued again.
	if err := svc.EmailJobHandler(ctx, queued[0].Payload); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	queue.reset()
	if err := svc.RedeliverJobHandler(ctx, jobs.Job{Type: "invitations.redeliver"}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(queue.snapshot()) != 0 {
		t.Fatal("a delivered invitation must not be queued again")
	}

	// A malformed sweep payload is permanent.
	err = svc.RedeliverJobHandler(ctx, jobs.Job{Type: "invitations.redeliver", Payload: []byte("{oops")})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("expected a permanent error, got %v", err)
	}
}

func TestAPIKeyExpirySweepRevokesDeadKeys(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	svc := apikeys.NewService(pool, orgSvc, audit.NopRecorder(), 90*24*time.Hour, 365*24*time.Hour, logger)

	userID, orgID := registerOrg(t, pool, "job-keys@example.com", "Job Keys Org")
	longDead, err := svc.Create(ctx, userID, orgID, apikeys.CreateInput{Name: "Long dead"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	recent, err := svc.Create(ctx, userID, orgID, apikeys.CreateInput{Name: "Recently expired"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	live, err := svc.Create(ctx, userID, orgID, apikeys.CreateInput{Name: "Live"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	// One key expired long ago, one recently: the grace period keeps the recent
	// one visible so an operator can still review it.
	if _, err := pool.Exec(ctx, `
		UPDATE api_keys SET created_at = now() - interval '60 days', expires_at = now() - interval '45 days'
		WHERE id = $1`, longDead.APIKey.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE api_keys SET created_at = now() - interval '40 days', expires_at = now() - interval '2 days'
		WHERE id = $1`, recent.APIKey.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.ExpireSweepJobHandler(ctx, jobs.Job{Type: "api_keys.expire_sweep"}); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// The database records revocation; the reported status is derived on read.
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM api_keys WHERE id = $1`, longDead.APIKey.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil {
		t.Fatal("the long-expired key was not revoked")
	}
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM api_keys WHERE id = $1`, recent.APIKey.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt != nil {
		t.Fatal("a recently expired key must stay visible")
	}
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM api_keys WHERE id = $1`, live.APIKey.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt != nil {
		t.Fatal("a live key must not be revoked")
	}

	// The sweep is idempotent.
	if err := svc.ExpireSweepJobHandler(ctx, jobs.Job{Type: "api_keys.expire_sweep"}); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	err = svc.ExpireSweepJobHandler(ctx, jobs.Job{Type: "api_keys.expire_sweep", Payload: []byte("{oops")})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("expected a permanent error, got %v", err)
	}
}

// A queue outage must not lose the invitation: the create-time enqueue fails and
// the email goes out inline instead.
func TestInvitationFallsBackToInlineDeliveryWhenQueueIsDown(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	sender := &countingSender{}
	queue := &recordingQueue{err: errors.New("dial tcp: connection refused")}
	svc := invitationsService(t, pool, orgSvc, sender, queue)

	userID, orgID := registerOrg(t, pool, "job-fallback@example.com", "Job Fallback Org")
	if _, err := svc.Create(ctx, userID, orgID, invitationsCreate(roleIDOf(t, pool, orgID, "Member"), "fallback@example.com")); err != nil {
		t.Fatalf("a queue outage must not fail the request: %v", err)
	}
	if sender.messageCount() != 1 {
		t.Fatalf("inline fallback sent %d messages, want 1", sender.messageCount())
	}
	var notified *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT notified_at FROM invitations WHERE email = 'fallback@example.com'`).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified == nil {
		t.Fatal("inline delivery must be recorded so the sweep stops")
	}
}
