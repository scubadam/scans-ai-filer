package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPProviderPlainAndJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"Octopus Energy Statement\nDate: 2026-11-19"}`))
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("Western Sussex Hospitals NHS Trust"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pJSON := NewHTTPProvider(srv.URL+"/json", 5*time.Second)
	txt, err := pJSON.ExtractImageText(context.Background(), []byte("fakeimg"), "image/jpeg")
	if err != nil || !strings.Contains(txt, "Octopus Energy") {
		t.Fatalf("expected JSON OCR text, got %q err=%v", txt, err)
	}

	pPlain := NewHTTPProvider(srv.URL+"/plain", 5*time.Second)
	txt, err = pPlain.ExtractImageText(context.Background(), []byte("fakeimg"), "image/jpeg")
	if err != nil || !strings.Contains(txt, "Western Sussex") {
		t.Fatalf("expected plain OCR text, got %q err=%v", txt, err)
	}
}

func TestParsePDFContentText(t *testing.T) {
	stream := `
BT
/F1 12 Tf
72 720 Td
(Western Sussex Hospitals \(NHS\) Trust) Tj
0 -20 Td
[(Appointment ) -10 (Letter)] TJ
ET
`
	got := parsePDFContentText(stream)
	if !strings.Contains(got, "Western Sussex Hospitals (NHS) Trust") || !strings.Contains(got, "Appointment") || !strings.Contains(got, "Letter") {
		t.Errorf("unexpected parsed PDF text: %q", got)
	}
}
