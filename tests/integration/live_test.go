//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/storage/gdrive"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// TestLiveGeminiClassification runs against the real Gemini API when TEST_GEMINI_API_KEY is set.
func TestLiveGeminiClassification(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("TEST_GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("Skipping live Gemini test: TEST_GEMINI_API_KEY not set")
	}

	model := os.Getenv("TEST_GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	provider := ai.NewGeminiProvider(apiKey, model, nil)
	syntheticDoc := []byte("Example Energy Provider\nDate: 2026-11-19\nQuarterly Electricity Bill\nAccount Number: 99887766\nAmount Due: £142.50\n")

	meta, err := provider.AnalyzeDocument(context.Background(), ai.DocumentInput{
		Bytes:            syntheticDoc,
		MimeType:         "text/plain",
		OriginalFilename: "test_bill.txt",
		FallbackExt:      ".pdf",
		FallbackDate:     "2026-11-19",
		Taxonomy: []taxonomy.Rule{
			{
				Category:     "Utilities",
				Subcategory:  "Energy",
				Organization: "Example Energy Provider",
				Keywords:     "electricity,energy,bill",
				PathTemplate: "Utilities/Energy/Example Energy Provider/{year}",
			},
		},
	})
	if err != nil {
		t.Fatalf("Live Gemini classification failed: %v", err)
	}
	if meta.Category != "Utilities" {
		t.Errorf("expected Category=Utilities, got %q", meta.Category)
	}
	// Assert PII sanitization guardrails held
	for _, sensitive := range []string{"99887766", "142.50"} {
		if strings.Contains(meta.Summary, sensitive) || strings.Contains(meta.SuggestedFilename, sensitive) {
			t.Errorf("PII leaked into metadata (%q): summary=%q filename=%q", sensitive, meta.Summary, meta.SuggestedFilename)
		}
	}
}

// TestLiveGDriveReadOnly runs read-only Init + ListInbox when TEST_GDRIVE_SA_PATH is set.
func TestLiveGDriveReadOnly(t *testing.T) {
	saPath := strings.TrimSpace(os.Getenv("TEST_GDRIVE_SA_PATH"))
	if saPath == "" {
		t.Skip("Skipping live Google Drive test: TEST_GDRIVE_SA_PATH not set")
	}

	store, err := gdrive.NewFromFile(saPath, "Scans", "Inbox", nil)
	if err != nil {
		t.Fatalf("NewFromFile failed: %v", err)
	}
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Drive Init failed: %v", err)
	}
	files, err := store.ListInbox(ctx, 5)
	if err != nil {
		t.Fatalf("Drive ListInbox failed: %v", err)
	}
	t.Logf("Live Drive connectivity verified: %d file(s) currently in Inbox", len(files))
}
