package notify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Compile-time interface assertion.
var _ NamedNotifier = (*NtfyNotifier)(nil)

// NtfyConfig configures native push notifications to a self-hosted or public ntfy server.
type NtfyConfig struct {
	BaseURL       string // e.g. "http://ntfy.ntfy.svc.cluster.local" or "https://ntfy.sh"
	Topic         string // Default topic for success notifications (and errors if ErrorTopic is empty)
	ErrorTopic    string // Optional separate topic for batches with errors
	Token         string // Optional Bearer access token (tk_...)
	Username      string // Optional Basic auth username (used when Token is empty)
	Password      string // Optional Basic auth password
	Priority      string // Default: "default" (1=min..5=max or min/low/default/high/urgent)
	ErrorPriority string // Default: "high"
	ClickURL      string // Optional URL opened when tapping the notification (e.g. Taxonomy PWA)
	SuccessTags   string // Default: "page_facing_up,white_check_mark"
	ErrorTags     string // Default: "warning,x"
}

// NtfyNotifier dispatches PII-sanitized notifications to an ntfy server via pure HTTP POST.
type NtfyNotifier struct {
	cfg        NtfyConfig
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewNtfyNotifier constructs an NtfyNotifier with sensible defaults.
func NewNtfyNotifier(cfg NtfyConfig, logger *slog.Logger) *NtfyNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Topic = strings.Trim(strings.TrimSpace(cfg.Topic), "/")
	cfg.ErrorTopic = strings.Trim(strings.TrimSpace(cfg.ErrorTopic), "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.ClickURL = strings.TrimSpace(cfg.ClickURL)
	if strings.TrimSpace(cfg.Priority) == "" {
		cfg.Priority = "default"
	}
	if strings.TrimSpace(cfg.ErrorPriority) == "" {
		cfg.ErrorPriority = "high"
	}
	if strings.TrimSpace(cfg.SuccessTags) == "" {
		cfg.SuccessTags = "page_facing_up,white_check_mark"
	}
	if strings.TrimSpace(cfg.ErrorTags) == "" {
		cfg.ErrorTags = "warning,x"
	}
	return &NtfyNotifier{
		cfg:        cfg,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Logger:     logger,
	}
}

func (n *NtfyNotifier) Name() string { return "ntfy" }

// Send publishes a privacy-safe summary to ntfy using topic/priority/tags matched to success vs error.
func (n *NtfyNotifier) Send(ctx context.Context, items []FiledItem, failedCount int) error {
	if n.cfg.BaseURL == "" {
		return nil
	}
	if len(items) == 0 && failedCount == 0 {
		return nil
	}

	title, msg, hasErrors := FormatSummary(items, failedCount)

	topic := n.cfg.Topic
	priority := n.cfg.Priority
	tags := n.cfg.SuccessTags
	if hasErrors {
		if n.cfg.ErrorTopic != "" {
			topic = n.cfg.ErrorTopic
		}
		priority = n.cfg.ErrorPriority
		tags = n.cfg.ErrorTags
	}
	if topic == "" {
		return fmt.Errorf("ntfy topic is not configured")
	}

	endpoint := n.cfg.BaseURL + "/" + url.PathEscape(topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if n.cfg.ClickURL != "" {
		req.Header.Set("Click", n.cfg.ClickURL)
	}
	if n.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.Token)
	} else if n.cfg.Username != "" {
		req.SetBasicAuth(n.cfg.Username, n.cfg.Password)
	}

	resp, err := n.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ntfy returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
