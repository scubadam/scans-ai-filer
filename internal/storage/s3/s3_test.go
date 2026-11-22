package s3

import (
	"context"
	"errors"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/storage"
)

func TestS3StorageStub(t *testing.T) {
	c := New(Config{Bucket: "test-scans"}, nil)
	if c.Name() != "s3" {
		t.Fatalf("expected Name() == 's3', got %q", c.Name())
	}

	ctx := context.Background()
	if err := c.Init(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from Init, got %v", err)
	}
	if _, err := c.ListInbox(ctx, 10); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from ListInbox, got %v", err)
	}
	if _, err := c.StillInInbox(ctx, "k"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from StillInInbox, got %v", err)
	}
	if _, err := c.Download(ctx, "k"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from Download, got %v", err)
	}
	if _, err := c.EnsureFolderPath(ctx, "Finance/2026"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from EnsureFolderPath, got %v", err)
	}
	if _, err := c.ResolveUniqueFilename(ctx, "Finance/2026", "a.pdf"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from ResolveUniqueFilename, got %v", err)
	}
	if err := c.FileDocument(ctx, "k", "dest", storage.FileMetadata{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from FileDocument, got %v", err)
	}
	if err := c.FileEncryptedDocument(ctx, "k", "dest", nil, "application/pdf", storage.FileMetadata{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from FileEncryptedDocument, got %v", err)
	}
	if _, err := c.ListFiledDocuments(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from ListFiledDocuments, got %v", err)
	}
	if err := c.MoveFiledDocument(ctx, "k", "old", "new", "a.pdf", nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from MoveFiledDocument, got %v", err)
	}
	if err := c.UpdateFiledContent(ctx, "k", "a.pdf", "application/pdf", nil, nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from UpdateFiledContent, got %v", err)
	}
	if _, err := c.PruneEmptyFolders(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from PruneEmptyFolders, got %v", err)
	}
}
