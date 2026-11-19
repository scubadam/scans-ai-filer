package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiProvider implements Provider using the Gemini generateContent REST API.
type GeminiProvider struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	MaxRetries int
	Logger     *slog.Logger
}

// NewGeminiProvider creates a GeminiProvider with sensible defaults.
func NewGeminiProvider(apiKey, model string, logger *slog.Logger) *GeminiProvider {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &GeminiProvider{
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    defaultGeminiBaseURL,
		HTTPClient: &http.Client{Timeout: 90 * time.Second},
		MaxRetries: 3,
		Logger:     logger,
	}
}

func (g *GeminiProvider) Name() string { return "gemini:" + g.Model }

// AnalyzeDocument sends the document bytes and sanitized taxonomy prompt to Gemini.
func (g *GeminiProvider) AnalyzeDocument(ctx context.Context, input DocumentInput) (*FilingMetadata, error) {
	prompt := BuildClassificationPrompt(input)
	b64Data := base64.StdEncoding.EncodeToString(input.Bytes)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{
						"inline_data": map[string]any{
							"mime_type": input.MimeType,
							"data":      b64Data,
						},
					},
					{
						"text": prompt,
					},
				},
			},
		},
		"generationConfig": map[string]any{
			"response_mime_type": "application/json",
			"response_schema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"document_date":         map[string]string{"type": "STRING"},
					"organization":          map[string]string{"type": "STRING"},
					"document_type":         map[string]string{"type": "STRING"},
					"category":              map[string]string{"type": "STRING"},
					"subcategory":           map[string]string{"type": "STRING"},
					"summary":               map[string]string{"type": "STRING"},
					"suggested_filename":    map[string]string{"type": "STRING"},
					"suggested_folder_path": map[string]string{"type": "STRING"},
					"is_taxonomy_match":     map[string]string{"type": "BOOLEAN"},
				},
				"required": []string{
					"document_date", "organization", "document_type",
					"category", "summary", "suggested_filename",
					"suggested_folder_path", "is_taxonomy_match",
				},
			},
		},
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		strings.TrimRight(g.BaseURL, "/"), g.Model, g.APIKey)

	retries := g.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	backoff := 2 * time.Second

	var respBytes []byte
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := g.HTTPClient.Do(req)
		if err != nil {
			if attempt == retries {
				return nil, fmt.Errorf("gemini API network error: %w", err)
			}
			g.Logger.Warn("network error calling Gemini API; retrying", "attempt", attempt, "err", err)
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}

		respBytes, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read gemini response: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}
		if isRetryableStatus(resp.StatusCode) && attempt < retries {
			g.Logger.Warn("transient Gemini API error; retrying", "status", resp.StatusCode, "attempt", attempt)
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		return nil, fmt.Errorf("gemini API returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var apiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("decode gemini envelope: %w", err)
	}
	if len(apiResp.Candidates) == 0 || len(apiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini API returned no candidates")
	}

	var meta FilingMetadata
	text := apiResp.Candidates[0].Content.Parts[0].Text
	if err := json.Unmarshal([]byte(text), &meta); err != nil {
		return nil, fmt.Errorf("decode gemini JSON metadata: %w", err)
	}
	return &meta, nil
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusInternalServerError ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
