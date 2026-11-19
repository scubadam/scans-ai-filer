package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiProviderAnalyzeAndPromptSanitization(t *testing.T) {
	var capturedPrompt string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		contents := req["contents"].([]any)
		parts := contents[0].(map[string]any)["parts"].([]any)
		capturedPrompt = parts[1].(map[string]any)["text"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{
						"text": "{\"document_date\":\"2026-11-19\",\"organization\":\"Example Bank\",\"document_type\":\"Bank Statement\",\"category\":\"Finance\",\"subcategory\":\"Banking\",\"summary\":\"Monthly Statement\",\"suggested_filename\":\"2026-11-19 - Example Bank - Bank Statement - Monthly Statement.pdf\",\"suggested_folder_path\":\"Finance/Banking/Example Bank/2026\",\"is_taxonomy_match\":true}"
					}]
				}
			}]
		}`))
	}))
	defer srv.Close()

	provider := NewGeminiProvider("test-key", "gemini-2.5-flash", nil)
	provider.BaseURL = srv.URL

	meta, err := provider.AnalyzeDocument(context.Background(), DocumentInput{
		Bytes:            []byte("%PDF-1.4 fake"),
		MimeType:         "application/pdf",
		OriginalFilename: "scan.pdf",
		FallbackExt:      ".pdf",
		FallbackDate:     "2026-11-19",
	})
	if err != nil {
		t.Fatalf("AnalyzeDocument failed: %v", err)
	}
	if meta.Organization != "Example Bank" || !meta.IsTaxonomyMatch {
		t.Errorf("unexpected metadata: %+v", meta)
	}
	if !strings.Contains(capturedPrompt, "NEVER include sensitive PII") {
		t.Errorf("expected PII sanitization guardrails in prompt")
	}
}
