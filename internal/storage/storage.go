package storage

import (
	"context"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// InboxFile represents a single unfiled document discovered in the inbox.
type InboxFile struct {
	ID          string
	Name        string
	MimeType    string
	Size        int64
	CreatedTime time.Time
}

// FileMetadata holds the sanitized, non-PII metadata written when filing a document.
type FileMetadata struct {
	NewName     string
	Description string
	Properties  map[string]string
}

// Provider abstracts the backing scan storage (Google Drive, local NAS, S3, WebDAV, etc.).
type Provider interface {
	// Name returns a human-readable identifier for this storage backend.
	Name() string

	// Init locates or validates the root scans and inbox containers.
	Init(ctx context.Context) error

	// ListInbox returns up to batchSize unfiled documents currently in the inbox.
	ListInbox(ctx context.Context, batchSize int) ([]InboxFile, error)

	// StillInInbox verifies a file has not already been moved by another worker/user.
	StillInInbox(ctx context.Context, fileID string) (bool, error)

	// Download retrieves the raw bytes of an inbox file.
	Download(ctx context.Context, fileID string) ([]byte, error)

	// EnsureFolderPath traverses or creates a nested folder hierarchy under the scans root
	// and returns the destination folder identifier.
	EnsureFolderPath(ctx context.Context, folderPath string) (string, error)

	// ResolveUniqueFilename avoids collisions in destFolderID by appending " (2)", " (3)", etc.
	ResolveUniqueFilename(ctx context.Context, destFolderID, desiredName string) (string, error)

	// FileDocument moves a document from the inbox to destFolderID and applies sanitized metadata.
	FileDocument(ctx context.Context, fileID, destFolderID string, meta FileMetadata) error

	// FileEncryptedDocument uploads encryptedBytes as a brand-new file in destFolderID with
	// sanitized metadata and permanently deletes origFileID from the inbox so no plaintext
	// revision lingers in cloud version history.
	FileEncryptedDocument(ctx context.Context, origFileID, destFolderID string, encryptedBytes []byte, mimeType string, meta FileMetadata) error
}

// LinkProvider is an optional capability implemented by storage providers that
// expose web-accessible URLs for filed documents and folders (e.g. Google Drive,
// Nextcloud/WebDAV, or a self-hosted file browser).
type LinkProvider interface {
	FileURL(fileID string) string
	FolderURL(folderID string) string
}

// FiledDocumentLister is an optional capability implemented by storage providers that
// can walk already-filed documents under the scans root to bootstrap or diff a taxonomy store.
type FiledDocumentLister interface {
	ListFiledDocuments(ctx context.Context) ([]taxonomy.FiledDocument, error)
}

// MaintenanceProvider is an optional capability implemented by storage providers that
// support reorganizing already-filed documents, updating binary content in place (e.g.
// OCR/encryption backfill), and pruning empty folders.
type MaintenanceProvider interface {
	FiledDocumentLister
	MoveFiledDocument(ctx context.Context, fileID, oldParentID, newParentID, newName string, props map[string]string) error
	UpdateFiledContent(ctx context.Context, fileID, newName, mimeType string, content []byte, props map[string]string) error
	PruneEmptyFolders(ctx context.Context) (int, error)
}
