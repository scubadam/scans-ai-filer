package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	defaultOllamaBaseURL     = "http://ollama-service.default.svc.cluster.local:11434/v1"
	defaultOllamaModel       = "qwen2.5vl:3b"
	defaultOllamaMaxPages    = 5
	defaultOllamaMaxImageDim = 1536
)

const pageExtractionPrompt = `Read this scanned document page carefully and extract key filing metadata in plain text:
- Document issue date: ONLY the letter date, statement date, invoice date, or passport/ID Date of Issue (include the 4-digit year if visible, and label it clearly e.g. "Date of Issue: YYYY-MM-DD"). NEVER extract Date of Birth (DOB), expiry dates, or personal ID numbers.
- Sender / Organization / Hospital / Company / Government body (exact letterhead name).
- Document type (e.g. Appointment Letter, Bank Statement, Invoice, Policy Schedule, Tax Notice, Passport Bio Page).
- Key headings, subject, and a concise factual summary of what the document actually is.
Do not invent information or speculate (e.g. do not call a plain passport scan a "renewal" unless the page explicitly says renewal). Keep the response concise and structured.`

// OllamaOpenAIProvider implements Provider for local Ollama or any OpenAI-compatible
// /v1/chat/completions vision endpoint (Ollama, vLLM, LM Studio, LocalAI).
//
// It uses a two-stage Map-Reduce pipeline so multi-page high-DPI scans stay within
// modest local GPU VRAM budgets (e.g. 4 GB) while preserving OCR accuracy:
//  1. Stage 1 (Map): Extract each PDF page image (up to MaxPages), downscale to MaxImageDim
//     for the vision encoder only, and run a focused per-page OCR/summary call one page at a time.
//  2. Stage 2 (Reduce): Send the combined extracted document text with the taxonomy catalog
//     in a text-only classification call (zero vision VRAM) to produce strict FilingMetadata JSON.
type OllamaOpenAIProvider struct {
	BaseURL     string
	APIKey      string
	Model       string
	TextModel   string
	MaxPages    int
	MaxImageDim int
	HTTPClient  *http.Client
	Logger      *slog.Logger
}

// NewOllamaOpenAIProvider creates an OllamaOpenAIProvider with sensible defaults.
func NewOllamaOpenAIProvider(baseURL, apiKey, modelName string, timeout time.Duration, logger *slog.Logger) *OllamaOpenAIProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOllamaBaseURL
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = defaultOllamaModel
	}
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OllamaOpenAIProvider{
		BaseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:      strings.TrimSpace(apiKey),
		Model:       strings.TrimSpace(modelName),
		MaxPages:    defaultOllamaMaxPages,
		MaxImageDim: defaultOllamaMaxImageDim,
		HTTPClient:  &http.Client{Timeout: timeout},
		Logger:      logger,
	}
}

func (o *OllamaOpenAIProvider) Name() string { return "ollama:" + o.Model }

func (o *OllamaOpenAIProvider) textModel() string {
	if strings.TrimSpace(o.TextModel) != "" {
		return strings.TrimSpace(o.TextModel)
	}
	return o.Model
}

func (o *OllamaOpenAIProvider) maxPages() int {
	if o.MaxPages > 0 {
		return o.MaxPages
	}
	return defaultOllamaMaxPages
}

// AnalyzeDocument runs the two-stage Map-Reduce vision + text classification flow.
func (o *OllamaOpenAIProvider) AnalyzeDocument(ctx context.Context, input DocumentInput) (*FilingMetadata, error) {
	extractedText, err := o.extractDocumentText(ctx, input)
	if err != nil {
		return nil, err
	}

	classifyPrompt := BuildClassificationPrompt(input) + `

EXTRACTED DOCUMENT CONTENT:
` + extractedText + `

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

	content, err := o.chatCompletion(ctx, o.textModel(), []map[string]any{
		{"type": "text", "text": classifyPrompt},
	}, true)
	if err != nil {
		return nil, fmt.Errorf("local AI classification stage failed: %w", err)
	}

	rawJSON := extractJSONObject(content)
	var meta FilingMetadata
	if err := json.Unmarshal([]byte(rawJSON), &meta); err != nil {
		return nil, fmt.Errorf("decode local AI JSON metadata (%q): %w", rawJSON, err)
	}
	if strings.TrimSpace(meta.Category) == "" && strings.TrimSpace(meta.Organization) == "" && strings.TrimSpace(meta.SuggestedFilename) == "" {
		return nil, fmt.Errorf("local AI returned empty classification JSON (%q)", rawJSON)
	}
	return &meta, nil
}

func (o *OllamaOpenAIProvider) extractDocumentText(ctx context.Context, input DocumentInput) (string, error) {
	mime := strings.ToLower(strings.TrimSpace(input.MimeType))

	switch {
	case strings.HasPrefix(mime, "text/"):
		return string(input.Bytes), nil

	case strings.HasPrefix(mime, "image/"):
		imgBytes, imgMime := downscaleImage(input.Bytes, mime, o.MaxImageDim)
		pageText, err := o.extractPageText(ctx, imgBytes, imgMime, 1, 1)
		if err != nil {
			return "", fmt.Errorf("local VLM image OCR failed: %w", err)
		}
		return "--- PAGE 1 ---\n" + pageText, nil

	case mime == "application/pdf":
		imgs, err := extractPDFImages(ctx, input.Bytes, o.maxPages())
		if err != nil || len(imgs) == 0 {
			return "", fmt.Errorf("PDF contains no extractable scanned images for local VLM (err=%v)", err)
		}
		var sb strings.Builder
		for i, img := range imgs {
			imgBytes, imgMime := downscaleImage(img.data, img.mimeType, o.MaxImageDim)
			pageText, err := o.extractPageText(ctx, imgBytes, imgMime, i+1, len(imgs))
			if err != nil {
				return "", fmt.Errorf("local VLM OCR failed on page %d/%d: %w", i+1, len(imgs), err)
			}
			if i > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(fmt.Sprintf("--- PAGE %d ---\n%s", i+1, strings.TrimSpace(pageText)))
		}
		return sb.String(), nil

	default:
		return "", fmt.Errorf("unsupported mime type for local VLM: %q", input.MimeType)
	}
}

func (o *OllamaOpenAIProvider) extractPageText(ctx context.Context, imgBytes []byte, imgMime string, pageNum, totalPages int) (string, error) {
	dataURL := fmt.Sprintf("data:%s;base64,%s", imgMime, base64.StdEncoding.EncodeToString(imgBytes))
	parts := []map[string]any{
		{"type": "text", "text": pageExtractionPrompt},
		{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
	}
	text, err := o.chatCompletion(ctx, o.Model, parts, false)
	if err != nil {
		return "", err
	}
	o.Logger.Info("local VLM extracted page",
		"page", pageNum,
		"total_pages", totalPages,
		"model", o.Model,
		"chars", len(strings.TrimSpace(text)),
	)
	return strings.TrimSpace(text), nil
}

func (o *OllamaOpenAIProvider) chatCompletion(ctx context.Context, modelName string, contentParts []map[string]any, forceJSON bool) (string, error) {
	payload := map[string]any{
		"model": modelName,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": contentParts,
			},
		},
		"temperature": 0.1,
		"stream":      false,
	}
	if forceJSON {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal ollama request: %w", err)
	}

	endpoint := o.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("local AI request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read local AI response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("local AI endpoint returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return "", fmt.Errorf("decode local AI envelope: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("local AI endpoint returned no choices")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// downscaleImage resizes an image proportionally so its longest edge does not exceed maxDim,
// using high-quality Catmull-Rom resampling to keep scanned text sharp while bounding VLM VRAM usage.
// If maxDim <= 0, decoding fails, or the image is already within bounds, it returns the original bytes untouched.
func downscaleImage(data []byte, mimeType string, maxDim int) ([]byte, string) {
	if maxDim <= 0 {
		return data, mimeType
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mimeType
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return data, mimeType
	}

	scale := float64(maxDim) / float64(max(w, h))
	newW := max(1, int(math.Round(float64(w)*scale)))
	newH := max(1, int(math.Round(float64(h)*scale)))

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
		return data, mimeType
	}
	return buf.Bytes(), "image/jpeg"
}

type extractedImage struct {
	mimeType string
	data     []byte
}

func extractPDFImages(ctx context.Context, pdfBytes []byte, maxPages int) ([]extractedImage, error) {
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
		// Pick the largest non-thumbnail image on each page so multi-image scans don't duplicate pages
		var best extractedImage
		var bestPixels int
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
			pixels := img.Width * img.Height
			if best.data == nil || pixels > bestPixels {
				best = extractedImage{mimeType: mime, data: raw}
				bestPixels = pixels
			}
		}
		if best.data != nil {
			out = append(out, best)
			if len(out) >= maxPages {
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
