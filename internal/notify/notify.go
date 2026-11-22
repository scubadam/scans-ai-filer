package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ErrNotImplemented is returned by reference notification stubs that are not yet wired to a live backend.
var ErrNotImplemented = errors.New("notification provider not yet implemented")

// FiledItem holds strictly sanitized filing info for notifications (no sensitive PII/details).
type FiledItem struct {
	Filename  string `json:"filename"`
	Folder    string `json:"folder"`
	FileURL   string `json:"file_url,omitempty"`
	FolderURL string `json:"folder_url,omitempty"`
}

// Notifier dispatches batch completion notifications.
type Notifier interface {
	Send(ctx context.Context, items []FiledItem, failedCount int) error
}

// NamedNotifier is an optional interface implemented by providers that expose a display name for logging.
type NamedNotifier interface {
	Notifier
	Name() string
}

// EventFilter controls whether a notification channel fires on successful filings, errors, or both.
type EventFilter struct {
	OnSuccess bool
	OnError   bool
}

// ShouldSend reports whether a batch with succeeded/failed counts matches this filter.
func (f EventFilter) ShouldSend(succeeded, failed int) bool {
	if succeeded == 0 && failed == 0 {
		return false
	}
	if failed > 0 && f.OnError {
		return true
	}
	if succeeded > 0 && f.OnSuccess {
		return true
	}
	return false
}

// FilteredNotifier wraps any Notifier with an EventFilter so channels can be configured
// independently for success-only, error-only, or all batch events.
type FilteredNotifier struct {
	Inner  Notifier
	Filter EventFilter
}

// NewFilteredNotifier wraps inner with filter.
func NewFilteredNotifier(inner Notifier, filter EventFilter) *FilteredNotifier {
	return &FilteredNotifier{Inner: inner, Filter: filter}
}

func (f *FilteredNotifier) Name() string {
	if named, ok := f.Inner.(NamedNotifier); ok {
		return named.Name()
	}
	return "filtered"
}

func (f *FilteredNotifier) Send(ctx context.Context, items []FiledItem, failedCount int) error {
	if f.Inner == nil {
		return nil
	}
	if !f.Filter.ShouldSend(len(items), failedCount) {
		return nil
	}
	return f.Inner.Send(ctx, items, failedCount)
}

// MultiNotifier fans out batch completion notifications to all configured channels.
// A failure on one channel is logged and recorded without preventing delivery to remaining channels.
type MultiNotifier struct {
	Notifiers []Notifier
	Logger    *slog.Logger
}

// NewMultiNotifier creates a fan-out notifier over the given non-nil notifiers.
func NewMultiNotifier(logger *slog.Logger, notifiers ...Notifier) *MultiNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	active := make([]Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n != nil {
			active = append(active, n)
		}
	}
	return &MultiNotifier{Notifiers: active, Logger: logger}
}

func (m *MultiNotifier) Name() string { return "multi" }

// Send dispatches to every configured notifier and returns any combined errors.
func (m *MultiNotifier) Send(ctx context.Context, items []FiledItem, failedCount int) error {
	if len(m.Notifiers) == 0 || (len(items) == 0 && failedCount == 0) {
		return nil
	}
	var errs []error
	for _, n := range m.Notifiers {
		if err := n.Send(ctx, items, failedCount); err != nil {
			name := "notifier"
			if named, ok := n.(NamedNotifier); ok {
				name = named.Name()
			}
			m.Logger.Warn("notification channel failed", "provider", name, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// FormatSummary builds a strictly PII-sanitized title and message body shared across providers.
func FormatSummary(items []FiledItem, failedCount int) (title, message string, hasErrors bool) {
	succeeded := len(items)
	hasErrors = failedCount > 0

	switch {
	case succeeded > 0 && failedCount == 0:
		unit := "documents"
		if succeeded == 1 {
			unit = "document"
		}
		title = fmt.Sprintf("Scans: %d %s filed", succeeded, unit)
	case succeeded > 0 && failedCount > 0:
		title = fmt.Sprintf("Scans: %d filed, %d failed", succeeded, failedCount)
	default:
		title = fmt.Sprintf("Scans: Filing batch failed (%d errors)", failedCount)
	}

	var lines []string
	limit := min(succeeded, 10)
	for i := 0; i < limit; i++ {
		lines = append(lines, fmt.Sprintf("• %s\n  %s", items[i].Filename, items[i].Folder))
	}
	if succeeded > 10 {
		lines = append(lines, fmt.Sprintf("... and %d more.", succeeded-10))
	}
	if failedCount > 0 {
		lines = append(lines, fmt.Sprintf("%d document(s) encountered errors.", failedCount))
	}

	message = strings.Join(lines, "\n")
	if message == "" {
		message = fmt.Sprintf("%d document(s) failed to process.", failedCount)
	}
	return title, message, hasErrors
}
