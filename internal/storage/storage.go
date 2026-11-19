package storage

import (
	"context"
	"time"
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
