package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Provider abstracts deterministic (non-AI) OCR over a single full-resolution page image.
type Provider interface {
	Name() string
	ExtractImageText(ctx context.Context, imgBytes []byte, mimeType string) (string, error)
}

// PDFBuilder is an optional capability implemented by OCR providers that can render
// a single-page searchable PDF (original scan image + invisible OCR text overlay).
type PDFBuilder interface {
	ImageToSearchablePDF(ctx context.Context, imgBytes []byte, mimeType string) ([]byte, error)
}

// AutoProvider selects an OCR backend according to mode ("auto", "http", "tesseract", "none").
func AutoProvider(mode, endpointURL, lang string, timeout time.Duration) Provider {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "none" || mode == "off" || mode == "false" {
		return nil
	}
	if mode == "" || mode == "auto" {
		if strings.TrimSpace(endpointURL) != "" {
			return NewHTTPProvider(endpointURL, timeout)
		}
		if _, err := exec.LookPath("tesseract"); err == nil {
			return NewTesseractProvider(lang, timeout)
		}
		return nil
	}
	switch mode {
	case "http":
		if strings.TrimSpace(endpointURL) == "" {
			return nil
		}
		return NewHTTPProvider(endpointURL, timeout)
	case "tesseract":
		return NewTesseractProvider(lang, timeout)
	default:
		return nil
	}
}

// HTTPProvider posts full-resolution image bytes to an external HTTP OCR microservice
// (e.g. Tesseract/PaddleOCR/Surya sidecar) and accepts either plain text or JSON {"text":"..."}.
type HTTPProvider struct {
	EndpointURL string
	HTTPClient  *http.Client
}

// NewHTTPProvider creates an HTTPProvider with a sensible default timeout.
func NewHTTPProvider(endpointURL string, timeout time.Duration) *HTTPProvider {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &HTTPProvider{
		EndpointURL: strings.TrimSpace(endpointURL),
		HTTPClient:  &http.Client{Timeout: timeout},
	}
}

func (h *HTTPProvider) Name() string { return "http-ocr" }

func (h *HTTPProvider) ExtractImageText(ctx context.Context, imgBytes []byte, mimeType string) (string, error) {
	if len(imgBytes) == 0 {
		return "", nil
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.EndpointURL, bytes.NewReader(imgBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Accept", "application/json, text/plain")

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http ocr request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http ocr returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			Text   string `json:"text"`
			Result string `json:"result"`
			Output string `json:"output"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil {
			for _, candidate := range []string{parsed.Text, parsed.Result, parsed.Output} {
				if t := strings.TrimSpace(candidate); t != "" {
					return t, nil
				}
			}
		}
	}
	return trimmed, nil
}

// ImageToSearchablePDF posts raw image bytes to {EndpointURL}/pdf (e.g. /ocr -> /ocr/pdf)
// and returns a single-page searchable PDF with an invisible OCR text layer.
func (h *HTTPProvider) ImageToSearchablePDF(ctx context.Context, imgBytes []byte, mimeType string) ([]byte, error) {
	if len(imgBytes) == 0 {
		return nil, fmt.Errorf("empty image bytes")
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	pdfEndpoint := strings.TrimRight(h.EndpointURL, "/")
	if !strings.HasSuffix(pdfEndpoint, "/pdf") {
		pdfEndpoint += "/pdf"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pdfEndpoint, bytes.NewReader(imgBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Accept", "application/pdf")

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http ocr pdf request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http ocr pdf returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	if len(body) < 4 || string(body[:4]) != "%PDF" {
		return nil, fmt.Errorf("http ocr pdf did not return valid PDF bytes")
	}
	return body, nil
}

// TesseractProvider invokes the local `tesseract` CLI via stdin/stdout (zero CGO).
type TesseractProvider struct {
	Lang    string
	Timeout time.Duration
}

// NewTesseractProvider creates a TesseractProvider (defaults to English, 60s timeout).
func NewTesseractProvider(lang string, timeout time.Duration) *TesseractProvider {
	if strings.TrimSpace(lang) == "" {
		lang = "eng"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &TesseractProvider{Lang: strings.TrimSpace(lang), Timeout: timeout}
}

func (t *TesseractProvider) Name() string { return "tesseract:" + t.Lang }

func (t *TesseractProvider) ExtractImageText(ctx context.Context, imgBytes []byte, _ string) (string, error) {
	if len(imgBytes) == 0 {
		return "", nil
	}
	execCtx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "tesseract", "stdin", "stdout", "-l", t.Lang, "--psm", "3")
	cmd.Stdin = bytes.NewReader(imgBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ImageToSearchablePDF invokes `tesseract stdin stdout -l <lang> --psm 3 pdf` to produce
// a single-page searchable PDF with an invisible OCR text overlay.
func (t *TesseractProvider) ImageToSearchablePDF(ctx context.Context, imgBytes []byte, _ string) ([]byte, error) {
	if len(imgBytes) == 0 {
		return nil, fmt.Errorf("empty image bytes")
	}
	execCtx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "tesseract", "stdin", "stdout", "-l", t.Lang, "--psm", "3", "pdf")
	cmd.Stdin = bytes.NewReader(imgBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract pdf failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// EmbedSearchableTextLayer converts a raster image or raster-only PDF into a full-document
// searchable PDF (embedding an invisible OCR text layer on ALL pages via builder and merging
// with pdfcpu). Digital PDFs that already contain an embedded text layer are returned untouched.
func EmbedSearchableTextLayer(ctx context.Context, data []byte, mimeType string, builder PDFBuilder, logger *slog.Logger) ([]byte, bool, error) {
	if builder == nil || len(data) == 0 {
		return data, false, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	mime := strings.ToLower(strings.TrimSpace(mimeType))

	if strings.HasPrefix(mime, "image/") {
		pdfBytes, err := builder.ImageToSearchablePDF(ctx, data, mime)
		if err != nil {
			return nil, false, fmt.Errorf("build searchable PDF from image: %w", err)
		}
		logger.Info("embedded searchable OCR layer into image PDF", "pdf_bytes", len(pdfBytes))
		return pdfBytes, true, nil
	}

	if mime != "application/pdf" {
		return data, false, nil
	}

	// Skip PDFs that already have an embedded text layer (digital PDFs / pre-OCR'd scans)
	if existing, err := ExtractPDFTextLayer(ctx, data, 2); err == nil && len(strings.TrimSpace(existing)) >= 40 {
		logger.Info("PDF already has embedded text layer; skipping searchable OCR embedding", "existing_chars", len(existing))
		return data, false, nil
	}

	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return nil, false, err
	}
	conf.ValidationMode = model.ValidationRelaxed

	// Extract primary image from ALL pages (nil page selection = all pages)
	pageMaps, err := api.ExtractImagesRaw(ctx, bytes.NewReader(data), nil, conf)
	if err != nil && len(pageMaps) == 0 {
		return data, false, fmt.Errorf("extract PDF pages for searchable OCR: %w", err)
	}

	var pagePDFs [][]byte
	for pageIdx, pageMap := range pageMaps {
		var bestBytes []byte
		var bestMime string
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
			imgMime := imgFileTypeToMime(img.FileType)
			if imgMime == "" {
				continue
			}
			pixels := img.Width * img.Height
			if bestBytes == nil || pixels > bestPixels {
				bestBytes = raw
				bestMime = imgMime
				bestPixels = pixels
			}
		}
		if bestBytes == nil {
			continue
		}
		pagePDF, err := builder.ImageToSearchablePDF(ctx, bestBytes, bestMime)
		if err != nil {
			return nil, false, fmt.Errorf("searchable OCR failed on page %d: %w", pageIdx+1, err)
		}
		pagePDFs = append(pagePDFs, pagePDF)
	}

	if len(pagePDFs) == 0 {
		return data, false, nil
	}
	if len(pagePDFs) == 1 {
		logger.Info("embedded searchable OCR layer into 1-page PDF", "pdf_bytes", len(pagePDFs[0]))
		return pagePDFs[0], true, nil
	}

	readers := make([]io.ReadSeeker, len(pagePDFs))
	for i, b := range pagePDFs {
		readers[i] = bytes.NewReader(b)
	}
	var merged bytes.Buffer
	if err := api.MergeRaw(ctx, readers, &merged, false, conf); err != nil {
		return nil, false, fmt.Errorf("merge %d searchable OCR PDF pages: %w", len(pagePDFs), err)
	}
	logger.Info("embedded searchable OCR layer into multi-page PDF", "pages", len(pagePDFs), "pdf_bytes", merged.Len())
	return merged.Bytes(), true, nil
}

func imgFileTypeToMime(ft string) string {
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

// ExtractPDFTextLayer extracts embedded text streams from digital or pre-OCR'd PDFs
// in pure Go (<50ms, zero external dependencies). Returns empty string for pure raster scans.
func ExtractPDFTextLayer(ctx context.Context, pdfBytes []byte, maxPages int) (string, error) {
	if len(pdfBytes) == 0 {
		return "", nil
	}
	if maxPages <= 0 {
		maxPages = 5
	}
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return "", err
	}
	conf.ValidationMode = model.ValidationRelaxed

	pageSel := []string{fmt.Sprintf("1-%d", maxPages)}
	var sb strings.Builder
	err = api.ExtractContent(ctx, bytes.NewReader(pdfBytes), pageSel, func(r io.Reader, pageNr int) error {
		if r == nil {
			return nil
		}
		raw, readErr := io.ReadAll(r)
		if readErr != nil || len(raw) == 0 {
			return nil
		}
		pageText := parsePDFContentText(string(raw))
		if pageText == "" {
			return nil
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "--- PAGE %d ---\n%s", pageNr, pageText)
		return nil
	}, conf)
	if err != nil && sb.Len() == 0 {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

var (
	btEtBlockRe = regexp.MustCompile(`(?s)\bBT\b(.*?)\bET\b`)
	pdfLitStrRe = regexp.MustCompile(`\((?:\\.|[^\\()])*\)`)
	multiWSRe   = regexp.MustCompile(`[ \t]+`)
)

// parsePDFContentText extracts readable literal strings inside BT...ET text blocks.
func parsePDFContentText(stream string) string {
	blocks := btEtBlockRe.FindAllStringSubmatch(stream, -1)
	if len(blocks) == 0 {
		return ""
	}
	var lines []string
	for _, b := range blocks {
		matches := pdfLitStrRe.FindAllString(b[1], -1)
		if len(matches) == 0 {
			continue
		}
		var parts []string
		for _, m := range matches {
			inner := unescapePDFString(m[1 : len(m)-1])
			if inner != "" {
				parts = append(parts, inner)
			}
		}
		if len(parts) > 0 {
			line := strings.TrimSpace(multiWSRe.ReplaceAllString(strings.Join(parts, " "), " "))
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func unescapePDFString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '(', ')', '\\':
				b.WriteByte(s[i])
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		// Keep printable ASCII and common UTF-8 bytes
		if s[i] >= 32 || s[i] == '\n' || s[i] == '\t' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
