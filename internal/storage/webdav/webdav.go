package webdav

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

// Config holds connection settings for a WebDAV / Nextcloud / ownCloud storage backend.
type Config struct {
	BaseURL         string
	Username        string
	Password        string
	RootPath        string
	InboxFolderName string
}

// Client is a reference stub showing how to implement a WebDAV / Nextcloud storage provider
// for scans-ai-filer.
//
// Design notes for WebDAV / Nextcloud:
//   - Use PROPFIND (Depth: 1) for ListInbox and EnsureFolderPath directory discovery.
//   - Use MKCOL to create missing nested folders under RootPath.
//   - Use GET for Download, MOVE (with Destination header) for FileDocument / MoveFiledDocument,
//     and PUT + DELETE for FileEncryptedDocument / UpdateFiledContent.
//   - See CONTRIBUTING.md for wiring steps into internal/config/config.go and cmd/filer/main.go.
type Client struct {
	cfg    Config
	Logger *slog.Logger
}

// New constructs a stub WebDAV / Nextcloud storage provider.
func New(cfg Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, Logger: logger}
}

func (c *Client) Name() string { return "webdav" }

func (c *Client) notImpl(op string) error {
	return fmt.Errorf("webdav storage provider %s (url=%q): %w (see internal/storage/webdav/webdav.go)", op, c.cfg.BaseURL, ErrNotImplemented)
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
