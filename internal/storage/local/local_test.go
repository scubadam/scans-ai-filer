package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/storage"
)

func TestLocalStorageWorkflow(t *testing.T) {
	root := t.TempDir()
	inboxDir := filepath.Join(root, "Inbox")
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}

	// Pre-create an existing category/org directory with slightly different casing/suffix
	// to verify case-insensitive and normalized matching in EnsureFolderPath.
	existingOrgDir := filepath.Join(root, "Finance", "Acme Bank Ltd")
	if err := os.MkdirAll(existingOrgDir, 0o755); err != nil {
		t.Fatalf("mkdir existing org: %v", err)
	}

	// Seed inbox with 2 real files + hidden/temp files that must be ignored.
	scan1 := filepath.Join(inboxDir, "scan001.pdf")
	scan2 := filepath.Join(inboxDir, "scan002.jpg")
	if err := os.WriteFile(scan1, []byte("%PDF-1.4 scan1"), 0o644); err != nil {
		t.Fatalf("write scan1: %v", err)
	}
	if err := os.WriteFile(scan2, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatalf("write scan2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inboxDir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inboxDir, "uploading.pdf.tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	// Set deterministic mtimes so scan1 is older than scan2.
	t1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	_ = os.Chtimes(scan1, t1, t1)
	_ = os.Chtimes(scan2, t2, t2)

	client := New(root, "Inbox", nil)
	if client.Name() != "local" {
		t.Fatalf("expected Name() == 'local', got %q", client.Name())
	}

	ctx := context.Background()
	if err := client.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	// Second Init should be a fast no-op.
	if err := client.Init(ctx); err != nil {
		t.Fatalf("second Init failed: %v", err)
	}

	files, err := client.ListInbox(ctx, 10)
	if err != nil {
		t.Fatalf("ListInbox failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 inbox files (ignoring hidden/tmp), got %d: %+v", len(files), files)
	}
	if files[0].Name != "scan001.pdf" || files[0].MimeType != "application/pdf" {
		t.Errorf("unexpected first file: %+v", files[0])
	}
	if files[1].Name != "scan002.jpg" || files[1].MimeType != "image/jpeg" {
		t.Errorf("unexpected second file: %+v", files[1])
	}

	// Batch limit check.
	limited, err := client.ListInbox(ctx, 1)
	if err != nil || len(limited) != 1 || limited[0].Name != "scan001.pdf" {
		t.Fatalf("batchSize=1 unexpected: %+v err=%v", limited, err)
	}

	ok, err := client.StillInInbox(ctx, files[0].ID)
	if err != nil || !ok {
		t.Fatalf("StillInInbox expected true, got %v err=%v", ok, err)
	}

	raw, err := client.Download(ctx, files[0].ID)
	if err != nil || string(raw) != "%PDF-1.4 scan1" {
		t.Fatalf("Download unexpected: %q err=%v", string(raw), err)
	}

	// EnsureFolderPath should match "finance" -> "Finance" (case-insensitive)
	// and "Acme Bank" -> "Acme Bank Ltd" (normalized match), then create "2026".
	destID, err := client.EnsureFolderPath(ctx, "finance/Acme Bank/2026")
	if err != nil {
		t.Fatalf("EnsureFolderPath failed: %v", err)
	}
	if destID != "Finance/Acme Bank Ltd/2026" {
		t.Fatalf("expected normalized path 'Finance/Acme Bank Ltd/2026', got %q", destID)
	}

	// FileDocument moves scan1 into destID.
	err = client.FileDocument(ctx, files[0].ID, destID, storage.FileMetadata{
		NewName: "2026-11-20 - Acme Bank - Statement.pdf",
	})
	if err != nil {
		t.Fatalf("FileDocument failed: %v", err)
	}

	// Original should no longer be in Inbox.
	still, err := client.StillInInbox(ctx, files[0].ID)
	if err != nil || still {
		t.Fatalf("expected scan1 to be moved out of inbox, still=%v err=%v", still, err)
	}

	// ResolveUniqueFilename should detect collision and return " (2)".
	unique, err := client.ResolveUniqueFilename(ctx, destID, "2026-11-20 - Acme Bank - Statement.pdf")
	if err != nil {
		t.Fatalf("ResolveUniqueFilename failed: %v", err)
	}
	if unique != "2026-11-20 - Acme Bank - Statement (2).pdf" {
		t.Fatalf("expected collision suffix (2), got %q", unique)
	}

	// FileEncryptedDocument writes encrypted bytes and removes plaintext scan2 from Inbox.
	encBytes := []byte("%PDF-1.7 encrypted-payload")
	err = client.FileEncryptedDocument(ctx, files[1].ID, destID, encBytes, "application/pdf", storage.FileMetadata{
		NewName: unique,
	})
	if err != nil {
		t.Fatalf("FileEncryptedDocument failed: %v", err)
	}

	if _, err := os.Stat(scan2); !os.IsNotExist(err) {
		t.Errorf("expected plaintext scan2 to be removed from Inbox, stat err=%v", err)
	}
	gotEnc, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(destID), unique))
	if err != nil || string(gotEnc) != string(encBytes) {
		t.Errorf("encrypted file mismatch: got=%q err=%v", string(gotEnc), err)
	}
}

func TestLocalMaintenanceProvider(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"Inbox",
		"Inbox-Local",
		"Unsorted",
		"Utilities/Old Energy/2025",
		"EmptyCat/EmptySub",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Seed an inbox file (should be skipped by ListFiledDocuments) and a filed doc.
	_ = os.WriteFile(filepath.Join(root, "Inbox", "unfiled.pdf"), []byte("inbox"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "Unsorted", "unsorted.pdf"), []byte("unsorted"), 0o644)
	filedRel := "Utilities/Old Energy/2025/2025-05-01 - Old Energy - Bill.pdf"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(filedRel)), []byte("old-pdf"), 0o644); err != nil {
		t.Fatalf("write filed doc: %v", err)
	}

	client := New(root, "Inbox", nil)
	ctx := context.Background()
	if err := client.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	docs, err := client.ListFiledDocuments(ctx)
	if err != nil {
		t.Fatalf("ListFiledDocuments failed: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != filedRel || docs[0].FolderPath != "Utilities/Old Energy/2025" {
		t.Fatalf("unexpected filed docs: %+v", docs)
	}

	// MoveFiledDocument to a new canonical folder + renamed filename.
	newDest, err := client.EnsureFolderPath(ctx, "Utilities/New Energy/2025")
	if err != nil {
		t.Fatalf("EnsureFolderPath failed: %v", err)
	}
	err = client.MoveFiledDocument(ctx, docs[0].ID, docs[0].ParentID, newDest, "2025-05-01 - New Energy - Bill.pdf", nil)
	if err != nil {
		t.Fatalf("MoveFiledDocument failed: %v", err)
	}

	movedID := newDest + "/2025-05-01 - New Energy - Bill.pdf"
	// UpdateFiledContent in place with new bytes.
	if err := client.UpdateFiledContent(ctx, movedID, "", "application/pdf", []byte("ocr-encrypted-pdf"), nil); err != nil {
		t.Fatalf("UpdateFiledContent failed: %v", err)
	}
	updated, err := client.Download(ctx, movedID)
	if err != nil || string(updated) != "ocr-encrypted-pdf" {
		t.Fatalf("expected updated content, got %q err=%v", string(updated), err)
	}

	// PruneEmptyFolders should remove Utilities/Old Energy/2025, Utilities/Old Energy,
	// EmptyCat/EmptySub, EmptyCat — while keeping Inbox, Inbox-Local, Unsorted, and Utilities/New Energy/2025.
	pruned, err := client.PruneEmptyFolders(ctx)
	if err != nil {
		t.Fatalf("PruneEmptyFolders failed: %v", err)
	}
	if pruned != 4 {
		t.Errorf("expected 4 empty folders pruned, got %d", pruned)
	}
	for _, kept := range []string{"Inbox", "Inbox-Local", "Unsorted", "Utilities/New Energy/2025"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(kept))); err != nil {
			t.Errorf("expected %q to be preserved, stat err=%v", kept, err)
		}
	}
}

func TestLocalPathTraversalAndMissingInbox(t *testing.T) {
	root := t.TempDir()
	client := New(root, "Inbox-Local", nil)
	err := client.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "inbox folder") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'inbox folder ... not found' error, got %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "Inbox-Local"), 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	if err := client.Init(context.Background()); err != nil {
		t.Fatalf("Init after creating inbox failed: %v", err)
	}

	// Path traversal attempts must not escape rootDir.
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	_ = os.WriteFile(outside, []byte("top-secret"), 0o644)
	defer os.Remove(outside)

	if _, err := client.Download(context.Background(), "../secret.txt"); err == nil {
		t.Errorf("expected error reading outside rootDir")
	}
}
