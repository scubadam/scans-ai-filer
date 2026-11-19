package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// FiledItem holds strictly sanitized filing info for notifications (no sensitive PII/details).
type FiledItem struct {
	Filename string
	Folder   string
}

// Notifier dispatches batch completion notifications.
type Notifier interface {
	Send(ctx context.Context, items []FiledItem, failedCount int) error
}

// WebhookNotifier sends JSON summaries to an HTTP webhook (e.g. Home Assistant webhook).
type WebhookNotifier struct {
	URL        string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewWebhookNotifier creates a WebhookNotifier. If url is empty, Send is a no-op.
func NewWebhookNotifier(url string, logger *slog.Logger) *WebhookNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &WebhookNotifier{
		URL:        strings.TrimSpace(url),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Logger:     logger,
	}
}

type webhookPayload struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	Failed  int    `json:"failed"`
}

// Send posts a privacy-safe filing summary to the configured webhook URL.
func (w *WebhookNotifier) Send(ctx context.Context, items []FiledItem, failedCount int) error {
	if w.URL == "" {
		return nil
	}
	succeeded := len(items)
	if succeeded == 0 && failedCount == 0 {
		return nil
	}

	var title string
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

	msg := strings.Join(lines, "\n")
	if msg == "" {
		msg = fmt.Sprintf("%d document(s) failed to process.", failedCount)
	}

	body, err := json.Marshal(webhookPayload{
		Title:   title,
		Message: msg,
		Count:   succeeded,
		Failed:  failedCount,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
