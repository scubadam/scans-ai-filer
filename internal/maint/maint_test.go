package maint

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

type mockCompleter struct {
	resp string
}

func (m *mockCompleter) CompleteJSON(_ context.Context, _ string) (string, error) {
	return m.resp, nil
}

type mockMaintStore struct {
	docs        []taxonomy.FiledDocument
	fileBytes   map[string][]byte
	moves       []string
	updates     map[string][]byte
	updateProps map[string]map[string]string
	pruned      int
}

func (m *mockMaintStore) Name() string                                          { return "mock" }
func (m *mockMaintStore) Init(context.Context) error                            { return nil }
func (m *mockMaintStore) ListInbox(context.Context, int) ([]storage.InboxFile, error) { return nil, nil }
func (m *mockMaintStore) StillInInbox(context.Context, string) (bool, error)    { return false, nil }
func (m *mockMaintStore) Download(_ context.Context, id string) ([]byte, error) { return m.fileBytes[id], nil }
func (m *mockMaintStore) EnsureFolderPath(_ context.Context, path string) (string, error) {
	return "folder:" + path, nil
}
func (m *mockMaintStore) ResolveUniqueFilename(_ context.Context, _, name string) (string, error) {
	return name, nil
}
func (m *mockMaintStore) FileDocument(context.Context, string, string, storage.FileMetadata) error { return nil }
func (m *mockMaintStore) FileEncryptedDocument(context.Context, string, string, []byte, string, storage.FileMetadata) error {
	return nil
}
func (m *mockMaintStore) ListFiledDocuments(context.Context) ([]taxonomy.FiledDocument, error) {
	return m.docs, nil
}
func (m *mockMaintStore) MoveFiledDocument(_ context.Context, fileID, _, newParentID, newName string, _ map[string]string) error {
	m.moves = append(m.moves, fileID+"->"+newParentID+"/"+newName)
	return nil
}
func (m *mockMaintStore) UpdateFiledContent(_ context.Context, fileID, _ string, _ string, content []byte, props map[string]string) error {
	if m.updates == nil {
		m.updates = make(map[string][]byte)
		m.updateProps = make(map[string]map[string]string)
	}
	m.updates[fileID] = content
	m.updateProps[fileID] = props
	return nil
}
func (m *mockMaintStore) PruneEmptyFolders(context.Context) (int, error) {
	m.pruned = 1
	return 1, nil
}

func makeMiniPDF(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 220, B: 220, A: 255})
		}
	}
	var jpg bytes.Buffer
	_ = jpeg.Encode(&jpg, img, nil)
	conf, _ := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	conf.ValidationMode = model.ValidationRelaxed
	var pdf bytes.Buffer
	if err := api.ImportImages(context.Background(), nil, &pdf, []io.Reader{bytes.NewReader(jpg.Bytes())}, nil, conf); err != nil {
		t.Fatalf("makeMiniPDF: %v", err)
	}
	return pdf.Bytes()
}

type mockOCRPDF struct {
	pdf []byte
}

func (m *mockOCRPDF) Name() string { return "mock-ocr" }
func (m *mockOCRPDF) ExtractImageText(context.Context, []byte, string) (string, error) {
	return "sample text", nil
}
func (m *mockOCRPDF) ImageToSearchablePDF(context.Context, []byte, string) ([]byte, error) {
	return m.pdf, nil
}

func TestTidyAndReorgAndBackfill(t *testing.T) {
	ctx := context.Background()
	taxStore, err := taxonomy.OpenSQLiteStore(":memory:", nil)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer taxStore.Close()

	_, _ = taxStore.SeedFromRules(ctx, []taxonomy.Rule{
		{Category: "Healthcare", Subcategory: "Residential Care", Organization: "Barchester Healthcare Homes Limited", Keywords: "care home", PathTemplate: "Healthcare/Residential Care/Barchester Healthcare Homes Limited/{year}"},
		{Category: "Personal", Subcategory: "Estate Planning", Organization: "Kingsland House", Keywords: "Letter", PathTemplate: "Personal/Estate Planning/Kingsland House/{year}"},
	}, "storage")

	tidyAI := &mockCompleter{resp: `{
		"actions": [{
			"action": "merge",
			"source_category": "Personal",
			"source_organization": "Kingsland House",
			"target_category": "Healthcare",
			"target_subcategory": "Residential Care",
			"target_organization": "Barchester Healthcare Homes Limited",
			"target_keywords": "care home, Kingsland House",
			"target_path_template": "Healthcare/Residential Care/Barchester Healthcare Homes Limited/{year}",
			"reason": "Kingsland House is a Barchester care home"
		}]
	}`}

	tidyRes, err := TidyTaxonomy(ctx, taxStore, tidyAI, false, nil)
	if err != nil || tidyRes.Applied != 1 {
		t.Fatalf("TidyTaxonomy failed: res=%+v err=%v", tidyRes, err)
	}
	rules, _ := taxStore.ListRules(ctx)
	if len(rules) != 1 || !strings.Contains(rules[0].Keywords, "Kingsland House") {
		t.Fatalf("expected 1 merged rule with Kingsland House keyword, got: %+v", rules)
	}

	// Reorg should move the Kingsland House file to Barchester AND sanitize a PII filename
	mStore := &mockMaintStore{
		docs: []taxonomy.FiledDocument{
			{
				ID:          "doc-1",
				ParentID:    "old-parent",
				MimeType:    "application/pdf",
				FolderPath:  "Personal/Estate Planning/Kingsland House/2026",
				Name:        "2026-11-03 - Kingsland House - Letter - Deposit Confirmation.pdf",
				Description: "Issuer: Kingsland House",
				Properties:  map[string]string{"organization": "Kingsland House", "doc_date": "2026-11-03"},
			},
			{
				ID:         "doc-2",
				ParentID:   "p2",
				MimeType:   "application/pdf",
				FolderPath: "Professional/FactSet/2014",
				Name:       "2014-10-16 - FactSet - Compensation Summary - John Doe.pdf",
				Properties: map[string]string{"encrypted": "aes-256", "searchable_ocr": "true"},
			},
		},
		fileBytes: map[string][]byte{
			"doc-1": makeMiniPDF(t),
			"doc-2": makeMiniPDF(t),
		},
	}

	renameAI := &mockCompleter{resp: `{"renames":[{"id":"doc-2","new_name":"2014-10-16 - FactSet - Compensation Summary - Annual Compensation Summary.pdf","reason":"Removed personal name"}]}`}
	reorgRes, err := ReorgStorage(ctx, mStore, mStore, taxStore, renameAI, ReorgOptions{
		DryRun:            false,
		SanitizeFilenames: true,
		PruneEmpty:        true,
	}, nil)
	if err != nil {
		t.Fatalf("ReorgStorage failed: %v", err)
	}
	if reorgRes.MovedCount != 1 || reorgRes.RenamedCount != 1 || reorgRes.PrunedFolders != 1 {
		t.Fatalf("unexpected reorg result: %+v", reorgRes)
	}

	// Backfill should skip doc-2 (already encrypted+searchable) and upgrade doc-1
	bfRes, err := BackfillPDFs(ctx, mStore, mStore, &mockOCRPDF{pdf: makeMiniPDF(t)}, BackfillOptions{
		DryRun:      false,
		EmbedOCR:    true,
		PDFPassword: "test-password-123",
	}, nil)
	if err != nil {
		t.Fatalf("BackfillPDFs failed: %v", err)
	}
	if bfRes.Processed != 1 || bfRes.Skipped != 1 {
		t.Fatalf("unexpected backfill result: %+v", bfRes)
	}
	if mStore.updateProps["doc-1"]["encrypted"] != "aes-256" || mStore.updateProps["doc-1"]["searchable_ocr"] != "true" {
		t.Errorf("expected encrypted+searchable properties on doc-1, got: %+v", mStore.updateProps["doc-1"])
	}
}

func TestMultiCategoryOrgDisambiguation(t *testing.T) {
	rules := []taxonomy.Rule{
		{Category: "Housing", Subcategory: "Insurance", Organization: "Direct Line", Keywords: "home insurance, buildings, contents", PathTemplate: "Housing/Insurance/Direct Line/{year}"},
		{Category: "Vehicle", Subcategory: "Insurance", Organization: "Direct Line", Keywords: "car insurance, motor, breakdown", PathTemplate: "Vehicle/Insurance/Direct Line/{year}"},
	}

	carDoc := taxonomy.FiledDocument{
		FolderPath: "Vehicle/Insurance/Direct Line/2018",
		Name:       "2018-08-15 - Direct Line - Car Insurance Renewal Notice.pdf",
		Properties: map[string]string{"organization": "Direct Line", "category": "Vehicle"},
	}
	matched, ok := matchDocToRule(carDoc, rules)
	if !ok || matched.Category != "Vehicle" {
		t.Fatalf("expected Vehicle rule for car insurance doc, got: %+v (ok=%v)", matched, ok)
	}

	homeDoc := taxonomy.FiledDocument{
		FolderPath: "Housing/Direct Line/2018",
		Name:       "2018-05-01 - Direct Line - Home Insurance Policy.pdf",
		Properties: map[string]string{"organization": "Direct Line", "category": "Housing"},
	}
	matched, ok = matchDocToRule(homeDoc, rules)
	if !ok || matched.Category != "Housing" {
		t.Fatalf("expected Housing rule for home insurance doc, got: %+v (ok=%v)", matched, ok)
	}
}
