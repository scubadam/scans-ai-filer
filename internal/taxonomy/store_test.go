package taxonomy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

type mockEmbedder struct {
	calls atomic.Int32
}

func (m *mockEmbedder) Name() string { return "mock-embed" }

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.calls.Add(1)
	t := strings.ToLower(text)
	// Simple 3D topic vectors: [energy, healthcare, identity]
	var v [3]float32
	if strings.Contains(t, "octopus") || strings.Contains(t, "energy") || strings.Contains(t, "electric") || strings.Contains(t, "gas") {
		v[0] = 1.0
	}
	if strings.Contains(t, "nhs") || strings.Contains(t, "hospital") || strings.Contains(t, "radiology") || strings.Contains(t, "medical") {
		v[1] = 1.0
	}
	if strings.Contains(t, "passport") || strings.Contains(t, "hmpo") || strings.Contains(t, "identity") {
		v[2] = 1.0
	}
	if v[0] == 0 && v[1] == 0 && v[2] == 0 {
		v[0], v[1], v[2] = 0.1, 0.1, 0.1
	}
	return v[:], nil
}

func TestSQLiteStoreSeedCacheAndVectorSearch(t *testing.T) {
	emb := &mockEmbedder{}
	store, err := OpenSQLiteStore(":memory:", emb)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer store.Close()

	rules := []Rule{
		{Category: "Utilities", Subcategory: "Energy", Organization: "Octopus Energy", Keywords: "electric, gas, tariff", PathTemplate: "Utilities/Energy/Octopus Energy/{year}"},
		{Category: "Healthcare", Subcategory: "Medical Records", Organization: "Western Sussex NHS", Keywords: "hospital, radiology, appointment", PathTemplate: "Healthcare/Medical Records/Western Sussex NHS/{year}"},
		{Category: "Personal", Subcategory: "Identification", Organization: "HMPO", Keywords: "passport, identity", PathTemplate: "Personal/Identification/HMPO/{year}"},
	}

	ctx := context.Background()
	n, err := store.SeedFromRules(ctx, rules, "csv")
	if err != nil || n != 3 {
		t.Fatalf("SeedFromRules: n=%d err=%v", n, err)
	}
	firstCalls := emb.calls.Load()
	if firstCalls != 3 {
		t.Fatalf("expected 3 embed calls on initial seed, got %d", firstCalls)
	}

	// Re-seeding identical rules should hit the SQLite embedding cache (0 new embed calls)
	if _, err := store.SeedFromRules(ctx, rules, "csv"); err != nil {
		t.Fatalf("second SeedFromRules: %v", err)
	}
	if emb.calls.Load() != firstCalls {
		t.Errorf("expected cached embeddings on re-seed, embed calls jumped from %d to %d", firstCalls, emb.calls.Load())
	}

	// Record a PII-free past filing
	if err := store.RecordFiling(ctx, FilingExample{
		Category:     "Healthcare",
		Subcategory:  "Medical Records",
		Organization: "Western Sussex NHS",
		DocumentType: "Appointment Letter",
		Summary:      "Radiology Department Appointment",
		FolderPath:   "Healthcare/Medical Records/Western Sussex NHS/{year}",
	}); err != nil {
		t.Fatalf("RecordFiling: %v", err)
	}

	// Semantic search for an NHS letter should rank Western Sussex NHS #1
	res, err := store.Search(ctx, "Western Sussex Hospital Radiology appointment letter", 2, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Rules) == 0 || res.Rules[0].Organization != "Western Sussex NHS" {
		t.Fatalf("expected Western Sussex NHS ranked #1, got: %+v", res.Rules)
	}
	if len(res.Examples) != 1 || res.Examples[0].Organization != "Western Sussex NHS" {
		t.Fatalf("expected 1 matching past filing example, got: %+v", res.Examples)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.RuleCount != 3 || stats.HistoryCount != 1 || stats.EmbeddedDocs != 3 {
		t.Errorf("unexpected stats: %+v", stats)
	}
}

func TestDeriveRulesFromFiledDocsAndDiff(t *testing.T) {
	docs := []FiledDocument{
		{
			FolderPath: "Utilities/Energy/Octopus Energy/2026",
			Name:       "2026-01-15 - Octopus Energy - Utility Bill - Monthly Statement.pdf",
			Description: "Category: Utilities\nSubcategory: Energy\nIssuer: Octopus Energy\nType: Utility Bill\nSummary: Monthly Statement\nDetails: Account 12345678 Balance £142.50",
		},
		{
			FolderPath: "Healthcare/Medical Records/Western Sussex NHS/2018",
			Name:       "2018-10-31 - Western Sussex NHS - Appointment Letter - Radiology.pdf",
			Properties: map[string]string{
				"category":     "Healthcare",
				"subcategory":  "Medical Records",
				"organization": "Western Sussex NHS",
				"doc_type":     "Appointment Letter",
			},
		},
		{
			FolderPath: "Inbox-Local",
			Name:       "ignored.pdf",
		},
	}

	derived, examples := DeriveRulesFromFiledDocs(docs)
	if len(derived) != 2 {
		t.Fatalf("expected 2 derived rules, got %d: %+v", len(derived), derived)
	}
	for _, ex := range examples {
		if strings.Contains(ex.Summary, "12345678") || strings.Contains(ex.Summary, "£142.50") {
			t.Errorf("legacy Details PII leaked into FilingExample: %+v", ex)
		}
	}

	csvRules := []Rule{
		{Category: "Utilities", Subcategory: "Energy", Organization: "Octopus Energy", PathTemplate: "Utilities/Octopus Energy/{year}"},
		{Category: "Finance", Subcategory: "Banking", Organization: "Barclays", PathTemplate: "Finance/Banking/Barclays/{year}"},
	}
	diff := DiffRules(csvRules, derived)
	if len(diff.OnlyInStorage) != 1 || diff.OnlyInStorage[0].Organization != "Western Sussex NHS" {
		t.Errorf("expected Western Sussex NHS only in storage, got %+v", diff.OnlyInStorage)
	}
	if len(diff.OnlyInCSV) != 1 || diff.OnlyInCSV[0].Organization != "Barclays" {
		t.Errorf("expected Barclays only in CSV, got %+v", diff.OnlyInCSV)
	}
	if len(diff.ChangedPaths) != 1 || diff.ChangedPaths[0].Organization != "Octopus Energy" {
		t.Errorf("expected Octopus Energy path diff, got %+v", diff.ChangedPaths)
	}
}
