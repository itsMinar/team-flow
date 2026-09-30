// Package mailer defines how the application sends transactional email.
//
// The Sender interface keeps the domain services independent of a concrete
// transport. Phase 8 ships a Sender that writes the message to the structured
// log so invitations are observable end to end without an SMTP dependency; the
// background job queue (Phase 10) will add an asynchronous sender, and a real
// provider can be added without touching any calling service.
package mailer

import (
	"context"
	"log/slog"
	"sort"
)

// Message is a single outbound email.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// Link is the single-use action URL, such as an invitation accept link. The
	// log transport writes it so a developer can follow the flow locally; a real
	// sender embeds it in the body instead.
	Link string
	// Metadata carries non-sensitive identifiers that let an operator trace a
	// delivery.
	Metadata map[string]string
}

// Sender delivers an email. Implementations must be safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// LogSender records messages in the structured log instead of sending them.
//
// It is the default transport for local development, where it makes the
// invitation flow followable without an SMTP server. Because it writes live
// invitation links to the log, configuration refuses to run it when APP_ENV is
// production.
type LogSender struct {
	logger *slog.Logger
}

func NewLogSender(logger *slog.Logger) *LogSender {
	return &LogSender{logger: logger}
}

// Send logs the recipient, subject, action link, and metadata.
func (s *LogSender) Send(_ context.Context, msg Message) error {
	attrs := []any{
		slog.String("to", msg.To),
		slog.String("subject", msg.Subject),
		slog.String("link", msg.Link),
	}
	for _, k := range sortedKeys(msg.Metadata) {
		attrs = append(attrs, slog.String("meta."+k, msg.Metadata[k]))
	}
	s.logger.Warn("email delivery via log sender (not sent)", attrs...)
	return nil
}

// sortedKeys keeps log output stable for log-based assertions.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ Sender = (*LogSender)(nil)

// DiscardSender drops every message. It exists so an operator can deliberately
// run with delivery turned off, for example in local development where the
// invitation link is read from the database instead of an inbox.
type DiscardSender struct{}

func (DiscardSender) Send(context.Context, Message) error { return nil }

var _ Sender = DiscardSender{}
