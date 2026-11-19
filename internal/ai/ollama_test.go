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
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func makeSampleScannedPDF(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 220, B: 220, A: 255})
		}
	}
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpg: %v", err)
	}

	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		t.Fatalf("load pdfcpu stateless config: %v", err)
	}
	conf.ValidationMode = model.ValidationRelaxed

	var pdfBuf bytes.Buffer
	if err := api.ImportImages(context.Background(), nil, &pdfBuf, []io.Reader{bytes.NewReader(jpgBuf.Bytes())}, nil, conf); err != nil {
		t.Fatalf("import image to pdf: %v", err)
	}
	return pdfBuf.Bytes()
}

func TestOllamaOpenAIProviderWithScannedPDF(t *testing.T) {
	var sawImageURL bool
	var capturedModel string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedModel, _ = req["model"].(string)
		msgs := req["messages"].([]any)
		content := msgs[0].(map[string]any)["content"].([]any)
		for _, part := range content {
			pm := part.(map[string]any)
			if pm["type"] == "image_url" {
				u := pm["image_url"].(map[string]any)["url"].(string)
				if strings.HasPrefix(u, "data:image/jpeg;base64,") {
					sawImageURL = true
				}
			}
		}

		// Return JSON wrapped in markdown fences to verify resilient extraction
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "Here is the classification:\n` + "```json\\n" + `{\"document_date\":\"2026-11-19\",\"organization\":\"Local Clinic\",\"document_type\":\"Medical Letter\",\"category\":\"Healthcare\",\"subcategory\":\"Medical Records\",\"summary\":\"Appointment Confirmation\",\"suggested_filename\":\"2026-11-19 - Local Clinic - Medical Letter - Appointment Confirmation.pdf\",\"suggested_folder_path\":\"Healthcare/Medical Records/Local Clinic/2026\",\"is_taxonomy_match\":false}` + "\\n```" + `"
				}
			}]
		}`))
	}))
	defer srv.Close()

	provider := NewOllamaOpenAIProvider(srv.URL, "", "llava-phi3:latest", 10*time.Second, nil)
	meta, err := provider.AnalyzeDocument(context.Background(), DocumentInput{
		Bytes:            makeSampleScannedPDF(t),
		MimeType:         "application/pdf",
		OriginalFilename: "scan_private.pdf",
		FallbackExt:      ".pdf",
		FallbackDate:     "2026-11-19",
	})
	if err != nil {
		t.Fatalf("AnalyzeDocument failed: %v", err)
	}
	if !sawImageURL {
		t.Errorf("expected PDF embedded JPEG to be extracted and sent as data:image/jpeg;base64")
	}
	if capturedModel != "llava-phi3:latest" {
		t.Errorf("unexpected model: %q", capturedModel)
	}
	if meta.Organization != "Local Clinic" || meta.Category != "Healthcare" {
		t.Errorf("unexpected metadata: %+v", meta)
	}
}
