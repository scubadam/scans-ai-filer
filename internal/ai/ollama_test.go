package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func makeSampleJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 220, B: 220, A: 255})
		}
	}
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpg: %v", err)
	}
	return jpgBuf.Bytes()
}

func makeSampleScannedPDF(t *testing.T, pages int) []byte {
	t.Helper()
	jpg := makeSampleJPEG(t, 120, 120)
	readers := make([]io.Reader, 0, pages)
	for i := 0; i < pages; i++ {
		readers = append(readers, bytes.NewReader(jpg))
	}

	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		t.Fatalf("load pdfcpu stateless config: %v", err)
	}
	conf.ValidationMode = model.ValidationRelaxed

	var pdfBuf bytes.Buffer
	if err := api.ImportImages(context.Background(), nil, &pdfBuf, readers, nil, conf); err != nil {
		t.Fatalf("import images to pdf: %v", err)
	}
	return pdfBuf.Bytes()
}

func TestOllamaOpenAIProviderMapReduceMultiPagePDF(t *testing.T) {
	var callCount atomic.Int32
	var visionCalls atomic.Int32
	var classifyModel string
	var classifyPrompt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		n := callCount.Add(1)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		reqModel, _ := req["model"].(string)
		msgs := req["messages"].([]any)
		content := msgs[0].(map[string]any)["content"].([]any)

		hasImage := false
		var textCombined strings.Builder
		for _, part := range content {
			pm := part.(map[string]any)
			if pm["type"] == "image_url" {
				u := pm["image_url"].(map[string]any)["url"].(string)
				if strings.HasPrefix(u, "data:image/jpeg;base64,") {
					hasImage = true
				}
			}
			if pm["type"] == "text" {
				textCombined.WriteString(pm["text"].(string))
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if hasImage {
			visionCalls.Add(1)
			if reqModel != "qwen2.5vl:3b" {
				t.Errorf("stage 1 expected vision model qwen2.5vl:3b, got %q", reqModel)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Date: 2026-11-19\nSender: Local Clinic\nPage ` + string(rune('0'+n)) + ` appointment details."}}]}`))
			return
		}

		// Stage 2: text-only classification
		classifyModel = reqModel
		classifyPrompt = textCombined.String()
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "Here is the classification:\n` + "```json\\n" + `{\"document_date\":\"2026-11-19\",\"organization\":\"Local Clinic\",\"document_type\":\"Medical Letter\",\"category\":\"Healthcare\",\"subcategory\":\"Medical Records\",\"summary\":\"Appointment Confirmation\",\"suggested_filename\":\"2026-11-19 - Local Clinic - Medical Letter - Appointment Confirmation.pdf\",\"suggested_folder_path\":\"Healthcare/Medical Records/Local Clinic/2026\",\"is_taxonomy_match\":false}` + "\\n```" + `"
				}
			}]
		}`))
	}))
	defer srv.Close()

	provider := NewOllamaOpenAIProvider(srv.URL, "", "qwen2.5vl:3b", 10*time.Second, nil)
	provider.TextModel = "qwen2.5:3b"
	meta, err := provider.AnalyzeDocument(context.Background(), DocumentInput{
		Bytes:            makeSampleScannedPDF(t, 2),
		MimeType:         "application/pdf",
		OriginalFilename: "scan_private.pdf",
		FallbackExt:      ".pdf",
		FallbackDate:     "2026-11-19",
	})
	if err != nil {
		t.Fatalf("AnalyzeDocument failed: %v", err)
	}
	if visionCalls.Load() != 2 {
		t.Errorf("expected 2 per-page vision calls, got %d", visionCalls.Load())
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 total calls (2 vision + 1 classify), got %d", callCount.Load())
	}
	if classifyModel != "qwen2.5:3b" {
		t.Errorf("expected stage 2 to use TextModel qwen2.5:3b, got %q", classifyModel)
	}
	if !strings.Contains(classifyPrompt, "--- PAGE 1 ---") || !strings.Contains(classifyPrompt, "--- PAGE 2 ---") {
		t.Errorf("expected combined page markers in stage 2 prompt, got: %q", classifyPrompt)
	}
	if meta.Organization != "Local Clinic" || meta.Category != "Healthcare" {
		t.Errorf("unexpected metadata: %+v", meta)
	}
}

func TestDownscaleImage(t *testing.T) {
	large := makeSampleJPEG(t, 2400, 1600)
	scaled, mime := downscaleImage(large, "image/jpeg", 1200)
	if mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", mime)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(scaled))
	if err != nil {
		t.Fatalf("decode scaled image: %v", err)
	}
	if cfg.Width != 1200 || cfg.Height != 800 {
		t.Errorf("expected 1200x800, got %dx%d", cfg.Width, cfg.Height)
	}

	// Small image should pass through untouched
	small := makeSampleJPEG(t, 400, 300)
	untouched, _ := downscaleImage(small, "image/jpeg", 1200)
	if !bytes.Equal(small, untouched) {
		t.Errorf("expected small image to pass through untouched")
	}

	// maxDim=0 disables scaling
	unscaled, _ := downscaleImage(large, "image/jpeg", 0)
	if !bytes.Equal(large, unscaled) {
		t.Errorf("expected maxDim=0 to disable scaling")
	}
}

type mockOCR struct {
	text  string
	calls atomic.Int32
}

func (m *mockOCR) Name() string { return "mock-ocr" }
func (m *mockOCR) ExtractImageText(_ context.Context, _ []byte, _ string) (string, error) {
	m.calls.Add(1)
	return m.text, nil
}

func TestOllamaHybridOCRPlusVisualGlance(t *testing.T) {
	var visionCalls atomic.Int32
	var classifyPrompt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		msgs := req["messages"].([]any)
		content := msgs[0].(map[string]any)["content"].([]any)
		hasImage := false
		var textCombined strings.Builder
		for _, part := range content {
			pm := part.(map[string]any)
			if pm["type"] == "image_url" {
				hasImage = true
			}
			if pm["type"] == "text" {
				textCombined.WriteString(pm["text"].(string))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if hasImage {
			visionCalls.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"UK passport photo bio page with portrait photo and HMPO crest."}}]}`))
			return
		}
		classifyPrompt = textCombined.String()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"document_date\":\"2022-05-10\",\"organization\":\"HMPO\",\"document_type\":\"Passport\",\"category\":\"Personal\",\"subcategory\":\"Identification\",\"summary\":\"Passport Bio Page\",\"suggested_filename\":\"2022-05-10 - HMPO - Passport - Passport Bio Page.pdf\",\"suggested_folder_path\":\"Personal/Identification/HMPO/2022\",\"is_taxonomy_match\":true}"}}]}`))
	}))
	defer srv.Close()

	ocrMock := &mockOCR{text: "HM PASSPORT OFFICE UNITED KINGDOM PASSPORT\nDate of Issue: 10 MAY 2022\nAuthority: HMPO\nStandard full resolution OCR extracted text exceeding minimum threshold."}
	provider := NewOllamaOpenAIProvider(srv.URL, "", "qwen2.5vl:3b", 10*time.Second, nil)
	provider.OCR = ocrMock
	provider.VisualGlanceDim = 640

	meta, err := provider.AnalyzeDocument(context.Background(), DocumentInput{
		Bytes:            makeSampleScannedPDF(t, 2),
		MimeType:         "application/pdf",
		OriginalFilename: "passport.pdf",
		FallbackExt:      ".pdf",
		FallbackDate:     "2026-11-20",
	})
	if err != nil {
		t.Fatalf("AnalyzeDocument hybrid failed: %v", err)
	}
	if ocrMock.calls.Load() != 2 {
		t.Errorf("expected deterministic OCR on both full-res pages, got %d calls", ocrMock.calls.Load())
	}
	if visionCalls.Load() != 1 {
		t.Errorf("expected only 1 low-res visual glance call on page 1, got %d", visionCalls.Load())
	}
	if !strings.Contains(classifyPrompt, "VISUAL DOCUMENT OVERVIEW:") || !strings.Contains(classifyPrompt, "FULL-RESOLUTION OCR TEXT:") {
		t.Errorf("expected both visual overview and full-res OCR text in stage 2 prompt, got: %q", classifyPrompt)
	}
	if meta.Organization != "HMPO" || meta.Summary != "Passport Bio Page" {
		t.Errorf("unexpected metadata: %+v", meta)
	}
}
