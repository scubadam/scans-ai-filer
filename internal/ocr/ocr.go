package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
