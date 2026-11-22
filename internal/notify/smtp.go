package notify

import (
	"context"
	"fmt"
	"log/slog"
)

// Compile-time interface assertion ensuring the stub stays in sync with NamedNotifier.
var _ NamedNotifier = (*SMTPNotifier)(nil)

// SMTPConfig holds settings for an SMTP / STARTTLS email notification backend.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
}

// SMTPNotifier is a reference stub showing how to implement additional notification channels
// (Email/SMTP, Pushover, Gotify, Discord, Slack, Telegram) for scans-ai-filer.
//
// To implement a new notification provider:
//  1. Copy this file (e.g. internal/notify/pushover.go) and implement NamedNotifier.
//  2. Reuse FormatSummary(items, failedCount) so all channels stay strictly PII-sanitized.
//  3. Add env vars (including optional per-channel *_ON_SUCCESS / *_ON_ERROR overrides) to internal/config/config.go.
//  4. Register the provider inside buildNotifier() in cmd/filer/main.go (wrapped in NewFilteredNotifier).
//  5. Add unit tests using net/http/httptest (see ntfy_test.go and webhook_test.go).
type SMTPNotifier struct {
	cfg    SMTPConfig
	Logger *slog.Logger
}

// NewSMTPNotifier constructs a stub SMTP email notifier.
func NewSMTPNotifier(cfg SMTPConfig, logger *slog.Logger) *SMTPNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &SMTPNotifier{cfg: cfg, Logger: logger}
}

func (s *SMTPNotifier) Name() string { return "smtp" }

func (s *SMTPNotifier) Send(_ context.Context, _ []FiledItem, _ int) error {
	return fmt.Errorf("smtp notifier (host=%q): %w (see internal/notify/smtp.go)", s.cfg.Host, ErrNotImplemented)
}
