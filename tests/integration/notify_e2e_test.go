//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/filer"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	localstorage "github.com/scubadam/scans-ai-filer/internal/storage/local"
)

func TestPipelineE2EMultiNotifierSuccessAndErrorRouting(t *testing.T) {
	root := t.TempDir()
	inboxDir := filepath.Join(root, "Inbox")
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}

	var mu sync.Mutex
	var webhookCalls int
	var ntfyPaths []string
	var ntfyPriorities []string

	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		webhookCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		ntfyPaths = append(ntfyPaths, r.URL.Path)
		ntfyPriorities = append(ntfyPriorities, r.Header.Get("Priority"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	var failAI bool
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failAI {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"simulated failure"}`))
			return
		}
		metaBytes, _ := json.Marshal(ai.FilingMetadata{
			DocumentDate:        "2026-11-22",
			Organization:        "Test Utility",
			DocumentType:        "Bill",
			Category:            "Utilities",
			Summary:             "Monthly Bill",
			SuggestedFilename:   "2026-11-22 - Test Utility - Bill.pdf",
			SuggestedFolderPath: "Utilities/Test Utility/2026",
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

	// Configure Webhook for success-only and ntfy for both success + errors (on separate error topic).
	multi := notify.NewMultiNotifier(nil,
		notify.NewFilteredNotifier(notify.NewWebhookNotifier(webhookSrv.URL, nil), notify.EventFilter{
			OnSuccess: true,
			OnError:   false,
		}),
		notify.NewFilteredNotifier(notify.NewNtfyNotifier(notify.NtfyConfig{
			BaseURL:       ntfySrv.URL,
			Topic:         "scans-ok",
			ErrorTopic:    "scans-err",
			Priority:      "default",
			ErrorPriority: "high",
		}, nil), notify.EventFilter{
			OnSuccess: true,
			OnError:   true,
		}),
	)

	store := localstorage.New(root, "Inbox", nil)
	gem := ai.NewGeminiProvider("test-key", "gemini-2.5-flash", nil)
	gem.BaseURL = geminiSrv.URL
	f := &filer.Filer{Storage: store, AI: gem, Notifier: multi}

	// Run 1: Success batch -> both webhook and ntfy (/scans-ok, priority=default) fire.
	if err := os.WriteFile(filepath.Join(inboxDir, "ok.pdf"), []byte("%PDF-1.4 ok"), 0o644); err != nil {
		t.Fatalf("write ok.pdf: %v", err)
	}
	if _, err := f.Run(context.Background(), filer.Options{ScansFolderName: "Scans", BatchSize: 5}); err != nil {
		t.Fatalf("success run failed: %v", err)
	}

	// Run 2: Error batch -> webhook skipped (success-only), ntfy fires on /scans-err with priority=high.
	failAI = true
	if err := os.WriteFile(filepath.Join(inboxDir, "bad.pdf"), []byte("%PDF-1.4 bad"), 0o644); err != nil {
		t.Fatalf("write bad.pdf: %v", err)
	}
	_, _ = f.Run(context.Background(), filer.Options{ScansFolderName: "Scans", BatchSize: 5})

	mu.Lock()
	defer mu.Unlock()
	if webhookCalls != 1 {
		t.Errorf("expected webhook to fire only on success (1 call), got %d", webhookCalls)
	}
	if len(ntfyPaths) != 2 || ntfyPaths[0] != "/scans-ok" || ntfyPaths[1] != "/scans-err" {
		t.Errorf("unexpected ntfy topic routing: %v", ntfyPaths)
	}
	if len(ntfyPriorities) != 2 || ntfyPriorities[0] != "default" || ntfyPriorities[1] != "high" {
		t.Errorf("unexpected ntfy priorities: %v", ntfyPriorities)
	}
}
