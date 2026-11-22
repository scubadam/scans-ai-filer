package local

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// Compile-time interface assertions.
var (
	_ storage.Provider            = (*Client)(nil)
	_ storage.MaintenanceProvider = (*Client)(nil)
)

// Client implements storage.Provider and storage.MaintenanceProvider backed by a
// local filesystem directory (including mounted NFS/SMB NAS shares or Syncthing folders).
type Client struct {
	rootDir         string
	inboxFolderName string
	Logger          *slog.Logger

	inboxRelPath string
	initialized  bool
}

// New creates a local filesystem storage provider rooted at rootDir with the given inbox subfolder.
func New(rootDir, inboxFolder string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		rootDir:         filepath.Clean(rootDir),
		inboxFolderName: strings.TrimSpace(inboxFolder),
		Logger:          logger,
	}
}

func (c *Client) Name() string { return "local" }

// safeJoin resolves a slash-separated relative path inside c.rootDir and rejects path traversal.
func (c *Client) safeJoin(rel string) (string, error) {
	cleaned := path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	abs := filepath.Join(c.rootDir, filepath.FromSlash(cleaned))

	relCheck, err := filepath.Rel(c.rootDir, abs)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", rel, err)
	}
	if relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root directory", rel)
	}
	return abs, nil
}

func isIgnoredFilename(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tmp") ||
		strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".crdownload")
}

func detectMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".heic", ".heif":
		return "image/heic"
	case ".tif", ".tiff":
		return "image/tiff"
	}
	if m := mime.TypeByExtension(ext); m != "" {
		if idx := strings.Index(m, ";"); idx > 0 {
			return strings.TrimSpace(m[:idx])
		}
		return m
	}
	return "application/octet-stream"
}

// Init validates that the root scans directory and inbox subfolder exist.
func (c *Client) Init(_ context.Context) error {
	if c.initialized {
		return nil
	}
	if c.rootDir == "" || c.rootDir == "." {
		return fmt.Errorf("local storage root directory must be configured")
	}
	info, err := os.Stat(c.rootDir)
	if err != nil {
		return fmt.Errorf("stat local scans root %q: %w", c.rootDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local scans root %q is not a directory", c.rootDir)
	}

	inboxName := c.inboxFolderName
	if inboxName == "" {
		inboxName = "Inbox"
	}

	// Find inbox directory (exact or case-insensitive match under rootDir).
	entries, err := os.ReadDir(c.rootDir)
	if err != nil {
		return fmt.Errorf("read local scans root %q: %w", c.rootDir, err)
	}
	var matchedInbox string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == inboxName {
			matchedInbox = e.Name()
			break
		}
		if matchedInbox == "" && strings.EqualFold(e.Name(), inboxName) {
			matchedInbox = e.Name()
		}
	}
	if matchedInbox == "" {
		return fmt.Errorf("inbox folder %q not found under %q", inboxName, c.rootDir)
	}

	c.inboxRelPath = matchedInbox
	c.initialized = true
	c.Logger.Info("initialized local storage", "root", c.rootDir, "inbox", c.inboxRelPath)
	return nil
}

// ListInbox returns up to batchSize regular, non-hidden files from the inbox directory.
func (c *Client) ListInbox(_ context.Context, batchSize int) ([]storage.InboxFile, error) {
	if !c.initialized {
		return nil, fmt.Errorf("local storage not initialized; call Init first")
	}
	inboxAbs, err := c.safeJoin(c.inboxRelPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(inboxAbs)
	if err != nil {
		if os.IsNotExist(err) {
			c.initialized = false
		}
		return nil, fmt.Errorf("list inbox directory %q: %w", inboxAbs, err)
	}

	var files []storage.InboxFile
	for _, e := range entries {
		if e.IsDir() || isIgnoredFilename(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		relID := path.Join(c.inboxRelPath, e.Name())
		files = append(files, storage.InboxFile{
			ID:          relID,
			Name:        e.Name(),
			MimeType:    detectMimeType(e.Name()),
			Size:        info.Size(),
			CreatedTime: info.ModTime().UTC(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if !files[i].CreatedTime.Equal(files[j].CreatedTime) {
			return files[i].CreatedTime.Before(files[j].CreatedTime)
		}
		return files[i].Name < files[j].Name
	})

	if batchSize > 0 && len(files) > batchSize {
		files = files[:batchSize]
	}
	return files, nil
}

// StillInInbox verifies that fileID still exists directly inside the inbox directory.
func (c *Client) StillInInbox(_ context.Context, fileID string) (bool, error) {
	if !c.initialized {
		return false, fmt.Errorf("local storage not initialized; call Init first")
	}
	cleanID := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(fileID, `\`, "/")), "/")
	if path.Dir(cleanID) != c.inboxRelPath {
		return false, nil
	}
	abs, err := c.safeJoin(cleanID)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// Download reads the raw bytes of a file relative to the scans root.
func (c *Client) Download(_ context.Context, fileID string) ([]byte, error) {
	abs, err := c.safeJoin(fileID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", fileID, err)
	}
	return data, nil
}

func sanitizeDirSegment(s string) string {
	const invalidChars = `\/:*?"<>|`
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(invalidChars, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// EnsureFolderPath traverses or creates nested folders under rootDir using
// case-insensitive and normalized name matching (matching gdrive behavior).
func (c *Client) EnsureFolderPath(_ context.Context, folderPath string) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("local storage not initialized; call Init first")
	}
	rawParts := strings.Split(strings.Trim(folderPath, "/"), "/")
	curRel := ""

	for _, rawPart := range rawParts {
		part := sanitizeDirSegment(rawPart)
		if part == "" || part == "." || part == ".." {
			continue
		}

		curAbs, err := c.safeJoin(curRel)
		if err != nil {
			return "", err
		}
		entries, err := os.ReadDir(curAbs)
		if err != nil {
			return "", fmt.Errorf("read directory %q: %w", curRel, err)
		}

		var matchedName string
		// 1. Exact case-insensitive match
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(e.Name()), part) {
				matchedName = e.Name()
				break
			}
		}
		// 2. Normalized match (skip for 4-digit years)
		if matchedName == "" && !taxonomy.IsYear(part) {
			normPart := taxonomy.NormalizeName(part)
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				if taxonomy.NormalizeName(e.Name()) == normPart {
					matchedName = e.Name()
					break
				}
			}
		}

		if matchedName == "" {
			newAbs, err := c.safeJoin(path.Join(curRel, part))
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(newAbs, 0o755); err != nil {
				return "", fmt.Errorf("create directory %q: %w", path.Join(curRel, part), err)
			}
			matchedName = part
			c.Logger.Info("created local folder", "name", matchedName, "parent", curRel)
		}

		if curRel == "" {
			curRel = matchedName
		} else {
			curRel = path.Join(curRel, matchedName)
		}
	}

	return curRel, nil
}

// ResolveUniqueFilename avoids filename collisions in destFolderID by appending " (2)", " (3)", etc.
func (c *Client) ResolveUniqueFilename(_ context.Context, destFolderID, desiredName string) (string, error) {
	destAbs, err := c.safeJoin(destFolderID)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(destAbs)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read destination directory %q: %w", destFolderID, err)
	}

	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		existing[strings.ToLower(e.Name())] = true
	}

	ext := filepath.Ext(desiredName)
	base := strings.TrimSuffix(desiredName, ext)
	candidate := desiredName
	for counter := 2; counter <= 1000; counter++ {
		if !existing[strings.ToLower(candidate)] {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, counter, ext)
	}
	return candidate, nil
}

// FileDocument moves a document from the inbox to destFolderID with meta.NewName.
func (c *Client) FileDocument(_ context.Context, fileID, destFolderID string, meta storage.FileMetadata) error {
	srcAbs, err := c.safeJoin(fileID)
	if err != nil {
		return err
	}
	destRel := path.Join(destFolderID, meta.NewName)
	destAbs, err := c.safeJoin(destRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return fmt.Errorf("ensure destination dir for %q: %w", destRel, err)
	}
	return moveFile(srcAbs, destAbs)
}

// FileEncryptedDocument writes encryptedBytes atomically into destFolderID/meta.NewName
// and permanently removes origFileID from the inbox.
func (c *Client) FileEncryptedDocument(_ context.Context, origFileID, destFolderID string, encryptedBytes []byte, _ string, meta storage.FileMetadata) error {
	origAbs, err := c.safeJoin(origFileID)
	if err != nil {
		return err
	}
	destRel := path.Join(destFolderID, meta.NewName)
	destAbs, err := c.safeJoin(destRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return fmt.Errorf("ensure destination dir for %q: %w", destRel, err)
	}
	if err := writeFileAtomic(destAbs, encryptedBytes, 0o644); err != nil {
		return fmt.Errorf("write encrypted file %q: %w", destRel, err)
	}
	if origAbs != destAbs {
		if err := os.Remove(origAbs); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove plaintext inbox file %q: %w", origFileID, err)
		}
	}
	return nil
}

// ListFiledDocuments walks the scans directory hierarchy (skipping top-level Inbox* and Unsorted)
// and returns all already-filed documents.
func (c *Client) ListFiledDocuments(_ context.Context) ([]taxonomy.FiledDocument, error) {
	if !c.initialized {
		return nil, fmt.Errorf("local storage not initialized; call Init first")
	}

	type queueItem struct {
		relPath string
	}
	queue := []queueItem{{relPath: ""}}
	var out []taxonomy.FiledDocument

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		curAbs, err := c.safeJoin(cur.relPath)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(curAbs)
		if err != nil {
			return nil, fmt.Errorf("list folder %q: %w", cur.relPath, err)
		}

		for _, e := range entries {
			if e.IsDir() {
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				lower := strings.ToLower(strings.TrimSpace(e.Name()))
				if cur.relPath == "" && (strings.HasPrefix(lower, "inbox") || lower == "unsorted") {
					continue
				}
				nextRel := e.Name()
				if cur.relPath != "" {
					nextRel = path.Join(cur.relPath, e.Name())
				}
				queue = append(queue, queueItem{relPath: nextRel})
				continue
			}
			if cur.relPath == "" || isIgnoredFilename(e.Name()) {
				continue
			}
			fileRel := path.Join(cur.relPath, e.Name())
			out = append(out, taxonomy.FiledDocument{
				ID:         fileRel,
				ParentID:   cur.relPath,
				MimeType:   detectMimeType(e.Name()),
				FolderPath: cur.relPath,
				Name:       e.Name(),
			})
		}
	}
	return out, nil
}

// MoveFiledDocument moves and/or renames an already-filed document.
func (c *Client) MoveFiledDocument(_ context.Context, fileID, oldParentID, newParentID, newName string, _ map[string]string) error {
	srcAbs, err := c.safeJoin(fileID)
	if err != nil {
		return err
	}
	targetParent := newParentID
	if targetParent == "" {
		targetParent = oldParentID
	}
	targetName := strings.TrimSpace(newName)
	if targetName == "" {
		targetName = path.Base(fileID)
	}
	destRel := path.Join(targetParent, targetName)
	destAbs, err := c.safeJoin(destRel)
	if err != nil {
		return err
	}
	if srcAbs == destAbs {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return fmt.Errorf("ensure destination dir for %q: %w", destRel, err)
	}
	return moveFile(srcAbs, destAbs)
}

// UpdateFiledContent atomically replaces an already-filed document's binary content in place.
func (c *Client) UpdateFiledContent(_ context.Context, fileID, newName, _ string, content []byte, _ map[string]string) error {
	origAbs, err := c.safeJoin(fileID)
	if err != nil {
		return err
	}
	destAbs := origAbs
	if trimmed := strings.TrimSpace(newName); trimmed != "" {
		parentRel := path.Dir(strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(fileID, `\`, "/")), "/"))
		destRel := path.Join(parentRel, trimmed)
		destAbs, err = c.safeJoin(destRel)
		if err != nil {
			return err
		}
	}
	if err := writeFileAtomic(destAbs, content, 0o644); err != nil {
		return fmt.Errorf("update filed content %q: %w", fileID, err)
	}
	if destAbs != origAbs {
		if err := os.Remove(origAbs); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old filed document %q: %w", fileID, err)
		}
	}
	return nil
}

// PruneEmptyFolders walks the filed folder hierarchy bottom-up (skipping rootDir, Inbox*, and Unsorted)
// and removes any empty directories.
func (c *Client) PruneEmptyFolders(_ context.Context) (int, error) {
	if !c.initialized {
		return 0, fmt.Errorf("local storage not initialized; call Init first")
	}

	type dirNode struct {
		relPath string
		depth   int
	}
	queue := []dirNode{{relPath: "", depth: 0}}
	var dirs []dirNode

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		curAbs, err := c.safeJoin(cur.relPath)
		if err != nil {
			return 0, err
		}
		entries, err := os.ReadDir(curAbs)
		if err != nil {
			return 0, fmt.Errorf("list folder %q: %w", cur.relPath, err)
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			lower := strings.ToLower(strings.TrimSpace(e.Name()))
			if cur.depth == 0 && (strings.HasPrefix(lower, "inbox") || lower == "unsorted") {
				continue
			}
			nextRel := e.Name()
			if cur.relPath != "" {
				nextRel = path.Join(cur.relPath, e.Name())
			}
			node := dirNode{relPath: nextRel, depth: cur.depth + 1}
			queue = append(queue, node)
			dirs = append(dirs, node)
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].depth > dirs[j].depth
	})

	pruned := 0
	for _, d := range dirs {
		abs, err := c.safeJoin(d.relPath)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil || len(entries) > 0 {
			continue
		}
		if err := os.Remove(abs); err == nil {
			pruned++
			c.Logger.Info("pruned empty local folder", "path", d.relPath)
		}
	}
	return pruned, nil
}

func writeFileAtomic(destPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".scans-filer-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-device fallback: copy to temp file in dst dir, rename, then remove src.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %q: %w", src, err)
	}
	defer in.Close()

	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read source %q: %w", src, err)
	}
	if err := writeFileAtomic(dst, data, 0o644); err != nil {
		return fmt.Errorf("write destination %q: %w", dst, err)
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove source %q after copy: %w", src, err)
	}
	return nil
}
