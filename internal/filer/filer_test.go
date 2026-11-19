package filer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	"github.com/scubadam/scans-ai-filer/internal/storage"
)

type mockStorage struct {
	files      []storage.InboxFile
	filedCalls int
	lastMeta   storage.FileMetadata
}

func (m *mockStorage) Name() string                      { return "mock" }
func (m *mockStorage) Init(context.Context) error        { return nil }
func (m *mockStorage) ListInbox(context.Context, int) ([]storage.InboxFile, error) {
	return m.files, nil
}
func (m *mockStorage) StillInInbox(context.Context, string) (bool, error) { return true, nil }
func (m *mockStorage) Download(context.Context, string) ([]byte, error)   { return []byte("pdf"), nil }
func (m *mockStorage) EnsureFolderPath(context.Context, string) (string, error) {
	return "dest-folder-1", nil
}
func (m *mockStorage) ResolveUniqueFilename(_ context.Context, _, name string) (string, error) {
	return name, nil
}
func (m *mockStorage) FileDocument(_ context.Context, _, _ string, meta storage.FileMetadata) error {
	m.filedCalls++
	m.lastMeta = meta
	return nil
}

type mockAI struct{}

func (m *mockAI) Name() string { return "mock-ai" }
func (m *mockAI) AnalyzeDocument(_ context.Context, _ ai.DocumentInput) (*ai.FilingMetadata, error) {
	return &ai.FilingMetadata{
		DocumentDate:        "2026-11-19",
		Organization:        "Example Energy",
		DocumentType:        "Utility Bill",
		Category:            "Utilities",
		Subcategory:         "Energy",
		Summary:             "Quarterly Statement",
		SuggestedFilename:   "2026-11-19 - Example Energy - Utility Bill - Quarterly Statement.pdf",
		SuggestedFolderPath: "Utilities/Energy/Example Energy/2026",
		IsTaxonomyMatch:     true,
	}, nil
}

type mockNotifier struct {
	sent int
}

func (m *mockNotifier) Send(_ context.Context, items []notify.FiledItem, _ int) error {
	m.sent += len(items)
	return nil
}

func TestFilerRunAndDryRun(t *testing.T) {
	store := &mockStorage{
		files: []storage.InboxFile{
			{ID: "f1", Name: "scan_2026-11-19.pdf", MimeType: "application/pdf", Size: 100, CreatedTime: time.Now()},
		},
	}
	notifier := &mockNotifier{}
	f := &Filer{Storage: store, AI: &mockAI{}, Notifier: notifier}

	// Dry run should not mutate storage or notify
	res, err := f.Run(context.Background(), Options{ScansFolderName: "Scans", BatchSize: 5, DryRun: true})
	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}
	if res.Succeeded != 1 || store.filedCalls != 0 || notifier.sent != 0 {
		t.Fatalf("DryRun mutated state: res=%+v filed=%d sent=%d", res, store.filedCalls, notifier.sent)
	}

	// Real run should file with sanitized metadata (no Details field) and notify
	res, err = f.Run(context.Background(), Options{ScansFolderName: "Scans", BatchSize: 5, DryRun: false})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.Succeeded != 1 || store.filedCalls != 1 || notifier.sent != 1 {
		t.Fatalf("unexpected Run result: res=%+v filed=%d sent=%d", res, store.filedCalls, notifier.sent)
	}
	if strings.Contains(store.lastMeta.Description, "Details:") {
		t.Errorf("sanitized description must not include Details line: %s", store.lastMeta.Description)
	}
	if store.lastMeta.Properties["processed_by"] != "scans-ai-filer" {
		t.Errorf("unexpected properties: %+v", store.lastMeta.Properties)
	}
}
