package ocr

import (
	"bytes"
	"context"
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

func makeTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{R: 230, G: 230, B: 230, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func makeRasterPDF(t *testing.T, pages int) []byte {
	t.Helper()
	jpg := makeTestJPEG(t)
	readers := make([]io.Reader, pages)
	for i := range readers {
		readers[i] = bytes.NewReader(jpg)
	}
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	conf.ValidationMode = model.ValidationRelaxed
	var out bytes.Buffer
	if err := api.ImportImages(context.Background(), nil, &out, readers, nil, conf); err != nil {
		t.Fatalf("import images: %v", err)
	}
	return out.Bytes()
}

func TestHTTPProviderPlainJSONAndPDF(t *testing.T) {
	samplePDF := makeRasterPDF(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ocr":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"Octopus Energy Statement\nDate: 2026-11-19"}`))
		case "/ocr/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(samplePDF)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("Example General Hospital NHS Trust"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := NewHTTPProvider(srv.URL+"/ocr", 5*time.Second)
	txt, err := p.ExtractImageText(context.Background(), []byte("fakeimg"), "image/jpeg")
	if err != nil || !strings.Contains(txt, "Octopus Energy") {
		t.Fatalf("expected JSON OCR text, got %q err=%v", txt, err)
	}

	pdfOut, err := p.ImageToSearchablePDF(context.Background(), []byte("fakeimg"), "image/jpeg")
	if err != nil || len(pdfOut) < 4 || string(pdfOut[:4]) != "%PDF" {
		t.Fatalf("expected valid PDF from /ocr/pdf, got len=%d err=%v", len(pdfOut), err)
	}

	pPlain := NewHTTPProvider(srv.URL+"/plain", 5*time.Second)
	txt, err = pPlain.ExtractImageText(context.Background(), []byte("fakeimg"), "image/jpeg")
	if err != nil || !strings.Contains(txt, "Example General Hospital") {
		t.Fatalf("expected plain OCR text, got %q err=%v", txt, err)
	}
}

type mockPDFBuilder struct {
	singlePagePDF []byte
	calls         atomic.Int32
}

func (m *mockPDFBuilder) ImageToSearchablePDF(_ context.Context, _ []byte, _ string) ([]byte, error) {
	m.calls.Add(1)
	return m.singlePagePDF, nil
}

func TestEmbedSearchableTextLayerMultiPage(t *testing.T) {
	twoPageRaster := makeRasterPDF(t, 2)
	builder := &mockPDFBuilder{singlePagePDF: makeRasterPDF(t, 1)}

	merged, changed, err := EmbedSearchableTextLayer(context.Background(), twoPageRaster, "application/pdf", builder, nil)
	if err != nil {
		t.Fatalf("EmbedSearchableTextLayer failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true for raster PDF")
	}
	if builder.calls.Load() != 2 {
		t.Errorf("expected all 2 pages OCR'd into searchable PDFs, got %d", builder.calls.Load())
	}
	if len(merged) < 4 || string(merged[:4]) != "%PDF" {
		t.Fatalf("expected merged PDF output")
	}
}

func TestParsePDFContentText(t *testing.T) {
	stream := `
BT
/F1 12 Tf
72 720 Td
(Example General Hospital \(NHS\) Trust) Tj
0 -20 Td
[(Appointment ) -10 (Letter)] TJ
ET
`
	got := parsePDFContentText(stream)
	if !strings.Contains(got, "Example General Hospital (NHS) Trust") || !strings.Contains(got, "Appointment") || !strings.Contains(got, "Letter") {
		t.Errorf("unexpected parsed PDF text: %q", got)
	}
}
