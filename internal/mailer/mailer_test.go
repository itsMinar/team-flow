package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLogSenderRecordsRecipientAndLink(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err := NewLogSender(logger).Send(context.Background(), Message{
		To:      "invitee@example.com",
		Subject: "You have been invited",
		Link:    "http://localhost:3000/invitations/accept?token=abc123",
		Metadata: map[string]string{
			"invitation_id":   "11111111-1111-1111-1111-111111111111",
			"organization_id": "22222222-2222-2222-2222-222222222222",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %s (%v)", buf.String(), err)
	}
	if record["to"] != "invitee@example.com" || record["link"] != "http://localhost:3000/invitations/accept?token=abc123" {
		t.Fatalf("unexpected record: %v", record)
	}
	if record["msg"] == nil {
		t.Fatalf("expected a log message, got %v", record)
	}
}

func TestLogSenderDoesNotLogMessageBodies(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	// The body repeats the link and nothing else sensitive, but a transport must
	// not dump arbitrary message content into the log sink.
	secret := "super-secret-body-content"
	if err := NewLogSender(logger).Send(context.Background(), Message{
		To: "invitee@example.com", Subject: "Invitation", Text: secret, HTML: secret,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("log sender leaked the message body: %s", buf.String())
	}
}

func TestDiscardSenderIsANoop(t *testing.T) {
	if err := (DiscardSender{}).Send(context.Background(), Message{To: "x@example.com"}); err != nil {
		t.Fatalf("discard send: %v", err)
	}
}
