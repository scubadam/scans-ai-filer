//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/filer"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	"github.com/scubadam/scans-ai-filer/internal/pdfenc"
	localstorage "github.com/scubadam/scans-ai-filer/internal/storage/local"
)

func TestPipelineE2ELocalStorageEncryptedAndWebhook(t *testing.T) {
	root := t.TempDir()
	inboxDir := filepath.Join(root, "Inbox")
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}

	// Generate a real PNG scan in Inbox
	img := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 120, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	origPath := filepath.Join(inboxDir, "receipt_scan.png")
	if err := os.WriteFile(origPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write inbox png: %v", err)
	}

	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metaBytes, _ := json.Marshal(ai.FilingMetadata{
			DocumentDate:        "2026-11-22",
			Organization:        "Local Hardware Co",
			DocumentType:        "Receipt",
			Category:            "Home",
			Subcategory:         "Maintenance",
			Summary:             "Tool Purchase",
			SuggestedFilename:   "2026-11-22 - Local Hardware Co - Receipt - Tool Purchase.png",
			SuggestedFolderPath: "Home/Local Hardware Co/2026",
			IsTaxonomyMatch:     true,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": string(metaBytes)}},
					},
				},
			},
		})
	}))
	defer geminiSrv.Close()

	var webhookMu sync.Mutex
	var webhookPayload map[string]any
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		webhookMu.Lock()
		webhookPayload = p
		webhookMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	store := localstorage.New(root, "Inbox", nil)
	gem := ai.NewGeminiProvider("test-key", "gemini-2.5-flash", nil)
	gem.BaseURL = geminiSrv.URL
	notifier := notify.NewWebhookNotifier(webhookSrv.URL, nil)

	const pdfPass = "LocalVaultPass2026!"
	f := &filer.Filer{
		Storage:  store,
		AI:       gem,
		Notifier: notifier,
	}

	res, err := f.Run(context.Background(), filer.Options{
		ScansFolderName: "Scans",
		BatchSize:       5,
		PDFPassword:     pdfPass,
	})
	if err != nil {
		t.Fatalf("Filer.Run with local storage failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Plaintext original must be removed from Inbox.
	if _, err := os.Stat(origPath); !os.IsNotExist(err) {
		t.Errorf("expected plaintext inbox file to be deleted, stat err=%v", err)
	}

	// Filed encrypted PDF must exist in Home/Local Hardware Co/2026 and decrypt only with pdfPass.
	filedDocs, err := store.ListFiledDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListFiledDocuments failed: %v", err)
	}
	if len(filedDocs) != 1 {
		t.Fatalf("expected 1 filed document, got %d: %+v", len(filedDocs), filedDocs)
	}
	doc := filedDocs[0]
	if doc.FolderPath != "Home/Local Hardware Co/2026" {
		t.Errorf("unexpected folder path: %q", doc.FolderPath)
	}
	if !strings.HasSuffix(doc.Name, ".pdf") {
		t.Errorf("expected wrapped encrypted .pdf filename, got %q", doc.Name)
	}
	encBytes, err := store.Download(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("Download filed doc failed: %v", err)
	}
	if _, err := pdfenc.DecryptDocument(context.Background(), encBytes, "wrong-password"); err == nil {
		t.Errorf("expected wrong password to fail decryption")
	}
	if _, err := pdfenc.DecryptDocument(context.Background(), encBytes, pdfPass); err != nil {
		t.Errorf("expected valid password to decrypt filed PDF: %v", err)
	}

	webhookMu.Lock()
	defer webhookMu.Unlock()
	if webhookPayload == nil || webhookPayload["count"] != float64(1) {
		t.Errorf("expected webhook payload with count=1, got %+v", webhookPayload)
	}
}
