package s3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// ErrNotImplemented is returned by reference storage stubs that are not yet wired to a live backend.
var ErrNotImplemented = errors.New("storage provider not yet implemented")

// Compile-time interface assertions ensuring the stub stays in sync with Provider & MaintenanceProvider.
var (
	_ storage.Provider            = (*Client)(nil)
	_ storage.MaintenanceProvider = (*Client)(nil)
)

// Config holds connection settings for an S3-compatible object storage backend
// (AWS S3, MinIO, Cloudflare R2, Garage, Ceph RGW, etc.).
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string
	InboxFolderName string
	AccessKeyID     string
	SecretAccessKey string
}

// Client is a reference stub showing how to implement an additional storage backend
// (e.g. S3/MinIO/R2, WebDAV/Nextcloud, SMB) for scans-ai-filer.
//
// To implement a new storage provider:
//  1. Copy this package (e.g. internal/storage/webdav) and implement storage.Provider
//     (and optionally storage.MaintenanceProvider for --bootstrap-taxonomy, --reorg-storage,
//     --backfill-pdfs, and the embedded Taxonomy Web UI).
//  2. Add provider configuration env vars to internal/config/config.go (Load + Validate).
//  3. Wire the provider case into buildStorage() in cmd/filer/main.go.
//  4. Add unit tests (see internal/storage/local/local_test.go and internal/storage/gdrive/gdrive_test.go)
//     and an E2E test in tests/integration/.
//
// Design notes for object storage (S3):
//   - Map folder IDs to key prefixes (e.g. "Scans/Finance/Bank/2026/") and file IDs to full object keys.
//   - FileDocument performs CopyObject + DeleteObject; FileEncryptedDocument performs PutObject + DeleteObject.
//   - Store sanitized FileMetadata.Properties as x-amz-meta-* object metadata headers.
type Client struct {
	cfg    Config
	Logger *slog.Logger
}

// New constructs a stub S3-compatible storage provider.
func New(cfg Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, Logger: logger}
}

func (c *Client) Name() string { return "s3" }

func (c *Client) notImpl(op string) error {
	return fmt.Errorf("s3 storage provider %s (bucket=%q): %w (see internal/storage/s3/s3.go)", op, c.cfg.Bucket, ErrNotImplemented)
}

func (c *Client) Init(_ context.Context) error {
	return c.notImpl("Init")
}

func (c *Client) ListInbox(_ context.Context, _ int) ([]storage.InboxFile, error) {
	return nil, c.notImpl("ListInbox")
}

func (c *Client) StillInInbox(_ context.Context, _ string) (bool, error) {
	return false, c.notImpl("StillInInbox")
}

func (c *Client) Download(_ context.Context, _ string) ([]byte, error) {
	return nil, c.notImpl("Download")
}

func (c *Client) EnsureFolderPath(_ context.Context, _ string) (string, error) {
	return "", c.notImpl("EnsureFolderPath")
}

func (c *Client) ResolveUniqueFilename(_ context.Context, _, _ string) (string, error) {
	return "", c.notImpl("ResolveUniqueFilename")
}

func (c *Client) FileDocument(_ context.Context, _, _ string, _ storage.FileMetadata) error {
	return c.notImpl("FileDocument")
}

func (c *Client) FileEncryptedDocument(_ context.Context, _, _ string, _ []byte, _ string, _ storage.FileMetadata) error {
	return c.notImpl("FileEncryptedDocument")
}

func (c *Client) ListFiledDocuments(_ context.Context) ([]taxonomy.FiledDocument, error) {
	return nil, c.notImpl("ListFiledDocuments")
}

func (c *Client) MoveFiledDocument(_ context.Context, _, _, _, _ string, _ map[string]string) error {
	return c.notImpl("MoveFiledDocument")
}

func (c *Client) UpdateFiledContent(_ context.Context, _, _, _ string, _ []byte, _ map[string]string) error {
	return c.notImpl("UpdateFiledContent")
}

func (c *Client) PruneEmptyFolders(_ context.Context) (int, error) {
	return 0, c.notImpl("PruneEmptyFolders")
}
