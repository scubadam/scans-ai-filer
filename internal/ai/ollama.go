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

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	defaultOllamaBaseURL = "http://ollama-service.default.svc.cluster.local:11434/v1"
	defaultOllamaModel   = "llava-phi3:latest"
	maxPDFPagesForVLM    = 2
	maxImagesForVLM      = 2
)

// OllamaOpenAIProvider implements Provider for local Ollama or any OpenAI-compatible
// /v1/chat/completions vision endpoint (Ollama, vLLM, LM Studio, LocalAI).
type OllamaOpenAIProvider struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewOllamaOpenAIProvider creates an OllamaOpenAIProvider with sensible defaults.
func NewOllamaOpenAIProvider(baseURL, apiKey, model string, timeout time.Duration, logger *slog.Logger) *OllamaOpenAIProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOllamaBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = defaultOllamaModel
	}
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OllamaOpenAIProvider{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		Model:      strings.TrimSpace(model),
		HTTPClient: &http.Client{Timeout: timeout},
		Logger:     logger,
	}
}

func (o *OllamaOpenAIProvider) Name() string { return "ollama:" + o.Model }

// AnalyzeDocument prepares images (extracting embedded scanned pages from PDFs in pure Go)
// and queries the OpenAI-compatible vision chat completions endpoint.
func (o *OllamaOpenAIProvider) AnalyzeDocument(ctx context.Context, input DocumentInput) (*FilingMetadata, error) {
	prompt := BuildClassificationPrompt(input) + `

IMPORTANT: Return ONLY a single valid JSON object (no markdown fences, no commentary) with exact keys:
{
  "document_date": "YYYY-MM-DD",
  "organization": "Canonical Organization Name",
  "document_type": "Document Type",
  "category": "Category",
  "subcategory": "Subcategory",
  "summary": "2 to 5 word generic PII-free summary",
  "suggested_filename": "YYYY-MM-DD - Organization - Document Type - Summary.pdf",
  "suggested_folder_path": "Category/Organization/YYYY",
  "is_taxonomy_match": false
}`

	contentParts, err := buildVisionContentParts(ctx, input, prompt)
	if err != nil {
		return nil, fmt.Errorf("prepare document for local VLM: %w", err)
	}

	payload := map[string]any{
		"model": o.Model,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": contentParts,
			},
		},
		"temperature": 0.1,
		"stream":      false,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	endpoint := o.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local AI request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read local AI response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local AI endpoint returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("decode local AI envelope: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("local AI endpoint returned no choices")
	}

	rawJSON := extractJSONObject(chatResp.Choices[0].Message.Content)
	var meta FilingMetadata
	if err := json.Unmarshal([]byte(rawJSON), &meta); err != nil {
		return nil, fmt.Errorf("decode local AI JSON metadata (%q): %w", rawJSON, err)
	}
	if strings.TrimSpace(meta.Category) == "" && strings.TrimSpace(meta.Organization) == "" && strings.TrimSpace(meta.SuggestedFilename) == "" {
		return nil, fmt.Errorf("local AI returned empty classification JSON (%q)", rawJSON)
	}
	return &meta, nil
}

func buildVisionContentParts(ctx context.Context, input DocumentInput, prompt string) ([]map[string]any, error) {
	mime := strings.ToLower(strings.TrimSpace(input.MimeType))
	parts := []map[string]any{
		{"type": "text", "text": prompt},
	}

	switch {
	case strings.HasPrefix(mime, "image/"):
		dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(input.Bytes))
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": dataURL},
		})
		return parts, nil

	case strings.HasPrefix(mime, "text/"):
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "DOCUMENT TEXT:\n" + string(input.Bytes),
		})
		return parts, nil

	case mime == "application/pdf":
		imgs, err := extractPDFImages(ctx, input.Bytes, maxPDFPagesForVLM, maxImagesForVLM)
		if err == nil && len(imgs) > 0 {
			for _, img := range imgs {
				dataURL := fmt.Sprintf("data:%s;base64,%s", img.mimeType, base64.StdEncoding.EncodeToString(img.data))
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]string{"url": dataURL},
				})
			}
			return parts, nil
		}
		// Fallback for text-only digital PDFs without embedded scanned images
		return nil, fmt.Errorf("PDF contains no extractable scanned images for local VLM (err=%v)", err)

	default:
		return nil, fmt.Errorf("unsupported mime type for local VLM: %q", input.MimeType)
	}
}

type extractedImage struct {
	mimeType string
	data     []byte
}

func extractPDFImages(ctx context.Context, pdfBytes []byte, maxPages, maxImages int) ([]extractedImage, error) {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, err
	}
	conf.ValidationMode = model.ValidationRelaxed

	pageSel := []string{fmt.Sprintf("1-%d", maxPages)}
	pageMaps, err := api.ExtractImagesRaw(ctx, bytes.NewReader(pdfBytes), pageSel, conf)
	if err != nil && len(pageMaps) == 0 {
		return nil, err
	}

	var out []extractedImage
	for _, pageMap := range pageMaps {
		for _, img := range pageMap {
			if img.Thumb || img.IsImgMask {
				continue
			}
			if img.Width > 0 && img.Width < 50 && img.Height > 0 && img.Height < 50 {
				continue
			}
			raw, readErr := io.ReadAll(img)
			if readErr != nil || len(raw) == 0 {
				continue
			}
			mime := fileTypeToMime(img.FileType)
			if mime == "" {
				continue
			}
			out = append(out, extractedImage{mimeType: mime, data: raw})
			if len(out) >= maxImages {
				return out, nil
			}
		}
	}
	return out, nil
}

func fileTypeToMime(ft string) string {
	switch strings.ToLower(strings.TrimSpace(ft)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

// extractJSONObject strips markdown code fences or prose around a JSON object returned by smaller VLMs.
func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
