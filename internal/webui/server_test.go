package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

func TestWebUIRulesMergeReviewAndExport(t *testing.T) {
	store, err := taxonomy.OpenSQLiteStore(":memory:", nil)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.UpsertRuleWithConfidence(ctx, taxonomy.Rule{
		Category: "Healthcare", Subcategory: "Residential Care",
		Organization: "Barchester Healthcare", Keywords: "care home",
		PathTemplate: "Healthcare/Residential Care/Barchester Healthcare/{year}",
	}, "csv", 1.0, true)
	_ = store.UpsertRuleWithConfidence(ctx, taxonomy.Rule{
		Category: "Personal", Subcategory: "Estate Planning",
		Organization: "Kingsland House", Keywords: "Letter",
		PathTemplate: "Personal/Estate Planning/Kingsland House/{year}",
	}, "learned", 0.45, false)
	_ = store.RecordFiling(ctx, taxonomy.FilingExample{
		Category: "Personal", Organization: "Kingsland House",
		DocumentType: "Letter", Summary: "Deposit Confirmation",
		FolderPath: "Personal/Estate Planning/Kingsland House/{year}",
		Confidence: 0.45, ConfidenceReason: "Sparse deposit slip", Reviewed: false,
	})

	scanTriggered := false
	srv := &Server{
		Store: store,
		Hooks: Hooks{TriggerScan: func() bool { scanTriggered = true; return true }},
	}
	h := srv.Handler()

	// 1. Stats should report 1 unconfirmed rule and 1 low-confidence unreviewed filing
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unconfirmed_rules":1`) || !strings.Contains(w.Body.String(), `"low_conf_unreviewed":1`) {
		t.Fatalf("unexpected stats response: code=%d body=%s", w.Code, w.Body.String())
	}

	// 2. List rules and merge Kingsland House -> Barchester Healthcare
	req = httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var listResp struct {
		Rules []taxonomy.RuleRecord `json:"rules"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(listResp.Rules))
	}
	// First rule should be the unconfirmed low-confidence one
	if listResp.Rules[0].Organization != "Kingsland House" {
		t.Errorf("expected unconfirmed Kingsland House sorted first, got %q", listResp.Rules[0].Organization)
	}

	mergeBody, _ := json.Marshal(map[string]int64{
		"source_id": listResp.Rules[0].ID,
		"target_id": listResp.Rules[1].ID,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/rules/merge", bytes.NewReader(mergeBody))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("merge failed: code=%d body=%s", w.Code, w.Body.String())
	}

	// 3. Export CSV should contain Barchester with Kingsland House in keywords
	req = httptest.NewRequest(http.MethodGet, "/api/export.csv", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Kingsland House") {
		t.Fatalf("expected merged alias in CSV export, got: %s", w.Body.String())
	}

	// 4. Scan-now trigger
	req = httptest.NewRequest(http.MethodPost, "/api/actions/scan-now", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !scanTriggered {
		t.Fatalf("scan-now failed: code=%d triggered=%v", w.Code, scanTriggered)
	}
}
