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

// Compile-time interface assertion.
var _ NamedNotifier = (*WebhookNotifier)(nil)

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

func (w *WebhookNotifier) Name() string { return "webhook" }

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

	title, msg, _ := FormatSummary(items, failedCount)

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
