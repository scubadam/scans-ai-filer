package gdrive

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scubadam/scans-ai-filer/internal/storage"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

const (
	defaultDriveBaseURL       = "https://www.googleapis.com/drive/v3"
	defaultDriveUploadBaseURL = "https://www.googleapis.com/upload/drive/v3"
	defaultTokenURL           = "https://oauth2.googleapis.com/token"
	driveScope                = "https://www.googleapis.com/auth/drive"
	defaultFolderCacheTTL     = 6 * time.Hour
)

// ServiceAccountKey mirrors the JSON fields of a Google Service Account key file.
type ServiceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// Client implements storage.Provider backed by Google Drive API v3 using pure Go stdlib.
type Client struct {
	saKey           ServiceAccountKey
	rsaKey          *rsa.PrivateKey
	scansFolderName string
	inboxFolderName string

	BaseURL        string
	UploadBaseURL  string
	TokenURL       string
	HTTPClient     *http.Client
	Logger         *slog.Logger
	FolderCacheTTL time.Duration

	folderMu          sync.RWMutex
	scansFolderID     string
	inboxFolderID     string
	foldersResolvedAt time.Time

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewFromFile loads service account credentials from a JSON file path.
func NewFromFile(path, scansFolder, inboxFolder string, logger *slog.Logger) (*Client, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read service account file %q: %w", path, err)
	}
	return NewFromJSON(data, scansFolder, inboxFolder, logger)
}

// NewFromJSON loads service account credentials from raw JSON bytes.
func NewFromJSON(raw []byte, scansFolder, inboxFolder string, logger *slog.Logger) (*Client, error) {
	var key ServiceAccountKey
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("parse service account JSON: %w", err)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, fmt.Errorf("service account JSON missing client_email or private_key")
	}

	rsaKey, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse service account private key: %w", err)
	}

	tokenURL := key.TokenURI
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		saKey:           key,
		rsaKey:          rsaKey,
		scansFolderName: scansFolder,
		inboxFolderName: inboxFolder,
		BaseURL:         defaultDriveBaseURL,
		UploadBaseURL:   defaultDriveUploadBaseURL,
		TokenURL:        tokenURL,
		HTTPClient:      &http.Client{Timeout: 60 * time.Second},
		Logger:          logger,
		FolderCacheTTL:  defaultFolderCacheTTL,
	}, nil
}

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private_key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func (c *Client) Name() string { return "gdrive" }

// FileURL returns the Google Drive web viewer URL for fileID (implements storage.LinkProvider).
func (c *Client) FileURL(fileID string) string {
	if strings.TrimSpace(fileID) == "" {
		return ""
	}
	return "https://drive.google.com/file/d/" + url.PathEscape(fileID) + "/view"
}

// FolderURL returns the Google Drive web folder URL for folderID (implements storage.LinkProvider).
func (c *Client) FolderURL(folderID string) string {
	if strings.TrimSpace(folderID) == "" {
		return ""
	}
	return "https://drive.google.com/drive/folders/" + url.PathEscape(folderID)
}

// EscapeQuery escapes single quotes and backslashes for Google Drive q parameters.
func EscapeQuery(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-2*time.Minute)) {
		return c.accessToken, nil
	}

	now := time.Now().Unix()
	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claimsJSON, _ := json.Marshal(map[string]any{
		"iss":   c.saKey.ClientEmail,
		"scope": driveScope,
		"aud":   c.TokenURL,
		"exp":   now + 3600,
		"iat":   now,
	})

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign OAuth2 JWT: %w", err)
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in OAuth2 response")
	}

	c.accessToken = tokenResp.AccessToken
	if tokenResp.ExpiresIn <= 0 {
		tokenResp.ExpiresIn = 3600
	}
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, reqBody any, out any) error {
	tok, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	fullURL := strings.TrimRight(c.BaseURL, "/") + endpoint
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("drive API %s %s returned HTTP %d: %s", method, endpoint, resp.StatusCode, string(respBytes))
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return fmt.Errorf("decode drive API response: %w", err)
		}
	}
	return nil
}

type driveFile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	MimeType    string   `json:"mimeType"`
	Size        string   `json:"size"`
	CreatedTime string   `json:"createdTime"`
	Parents     []string `json:"parents"`
}

type driveListResp struct {
	Files []driveFile `json:"files"`
}

func (c *Client) listFiles(ctx context.Context, q, fields string, pageSize int) ([]driveFile, error) {
	params := url.Values{}
	params.Set("q", q)
	params.Set("fields", fields)
	params.Set("supportsAllDrives", "true")
	params.Set("includeItemsFromAllDrives", "true")
	if pageSize > 0 {
		params.Set("pageSize", strconv.Itoa(pageSize))
	}

	var out driveListResp
	if err := c.doJSON(ctx, http.MethodGet, "/files", params, nil, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// InvalidateFolderCache clears cached root/inbox folder IDs so the next Init re-resolves them.
func (c *Client) InvalidateFolderCache() {
	c.folderMu.Lock()
	c.scansFolderID = ""
	c.inboxFolderID = ""
	c.foldersResolvedAt = time.Time{}
	c.folderMu.Unlock()
}

func (c *Client) getScansFolderID() string {
	c.folderMu.RLock()
	defer c.folderMu.RUnlock()
	return c.scansFolderID
}

func (c *Client) getInboxFolderID() string {
	c.folderMu.RLock()
	defer c.folderMu.RUnlock()
	return c.inboxFolderID
}

// Init locates the root scans folder and the inbox subfolder.
// Resolved folder IDs are cached for FolderCacheTTL (default 6h) so repeated
// daemon poll cycles do not re-query Drive on every interval.
func (c *Client) Init(ctx context.Context) error {
	c.folderMu.RLock()
	cached := c.scansFolderID != "" && c.inboxFolderID != "" &&
		(c.FolderCacheTTL <= 0 || time.Since(c.foldersResolvedAt) < c.FolderCacheTTL)
	c.folderMu.RUnlock()
	if cached {
		return nil
	}

	qScans := fmt.Sprintf("name = '%s' and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
		EscapeQuery(c.scansFolderName))
	scans, err := c.listFiles(ctx, qScans, "files(id, name)", 10)
	if err != nil {
		return fmt.Errorf("locate scans root folder %q: %w", c.scansFolderName, err)
	}
	if len(scans) == 0 {
		return fmt.Errorf("root folder %q not found in Google Drive", c.scansFolderName)
	}
	scansID := scans[0].ID
	c.Logger.Info("located scans root folder", "name", c.scansFolderName, "id", scansID)

	qInbox := fmt.Sprintf("'%s' in parents and name = '%s' and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
		EscapeQuery(scansID), EscapeQuery(c.inboxFolderName))
	inboxes, err := c.listFiles(ctx, qInbox, "files(id, name)", 10)
	if err != nil {
		return fmt.Errorf("locate inbox folder %q: %w", c.inboxFolderName, err)
	}
	if len(inboxes) == 0 {
		c.Logger.Warn("inbox folder not directly under scans root; searching anywhere", "inbox", c.inboxFolderName)
		qAny := fmt.Sprintf("name = '%s' and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
			EscapeQuery(c.inboxFolderName))
		inboxes, err = c.listFiles(ctx, qAny, "files(id, name)", 10)
		if err != nil {
			return fmt.Errorf("search inbox folder %q: %w", c.inboxFolderName, err)
		}
	}
	if len(inboxes) == 0 {
		return fmt.Errorf("inbox folder %q not found in Google Drive", c.inboxFolderName)
	}
	inboxID := inboxes[0].ID
	c.Logger.Info("located inbox folder", "name", c.inboxFolderName, "id", inboxID)

	c.folderMu.Lock()
	c.scansFolderID = scansID
	c.inboxFolderID = inboxID
	c.foldersResolvedAt = time.Now()
	c.folderMu.Unlock()
	return nil
}

// ListInbox lists up to batchSize non-folder files in the inbox.
func (c *Client) ListInbox(ctx context.Context, batchSize int) ([]storage.InboxFile, error) {
	inboxID := c.getInboxFolderID()
	q := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed = false",
		EscapeQuery(inboxID))
	raw, err := c.listFiles(ctx, q, "files(id, name, mimeType, size, createdTime)", batchSize)
	if err != nil {
		if strings.Contains(err.Error(), "returned HTTP 404") {
			c.InvalidateFolderCache()
		}
		return nil, err
	}

	out := make([]storage.InboxFile, 0, len(raw))
	for _, f := range raw {
		sz, _ := strconv.ParseInt(f.Size, 10, 64)
		var ct time.Time
		if f.CreatedTime != "" {
			ct, _ = time.Parse(time.RFC3339, f.CreatedTime)
		}
		mime := f.MimeType
		if mime == "" {
			mime = "application/pdf"
		}
		name := f.Name
		if name == "" {
			name = "Untitled"
		}
		out = append(out, storage.InboxFile{
			ID:          f.ID,
			Name:        name,
			MimeType:    mime,
			Size:        sz,
			CreatedTime: ct,
		})
	}
	return out, nil
}

// StillInInbox checks whether fileID is still parented under the inbox folder.
func (c *Client) StillInInbox(ctx context.Context, fileID string) (bool, error) {
	params := url.Values{}
	params.Set("fields", "parents")
	params.Set("supportsAllDrives", "true")

	var f driveFile
	if err := c.doJSON(ctx, http.MethodGet, "/files/"+url.PathEscape(fileID), params, nil, &f); err != nil {
		return false, err
	}
	inboxID := c.getInboxFolderID()
	for _, p := range f.Parents {
		if p == inboxID {
			return true, nil
		}
	}
	return false, nil
}

// Download fetches raw bytes of a Drive file.
func (c *Client) Download(ctx context.Context, fileID string) ([]byte, error) {
	tok, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("alt", "media")
	params.Set("supportsAllDrives", "true")

	fullURL := strings.TrimRight(c.BaseURL, "/") + "/files/" + url.PathEscape(fileID) + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download file %q returned HTTP %d: %s", fileID, resp.StatusCode, string(data))
	}
	return data, nil
}

// EnsureFolderPath traverses or creates nested folders under scansFolderID using normalized matching.
func (c *Client) EnsureFolderPath(ctx context.Context, folderPath string) (string, error) {
	rawParts := strings.Split(strings.Trim(folderPath, "/"), "/")
	currentParent := c.getScansFolderID()

	for _, rawPart := range rawParts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			continue
		}

		q := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
			EscapeQuery(currentParent))
		existing, err := c.listFiles(ctx, q, "files(id, name)", 200)
		if err != nil {
			return "", fmt.Errorf("list subfolders under %q: %w", currentParent, err)
		}

		var matchedID string
		// 1. Exact case-insensitive match
		for _, f := range existing {
			if strings.EqualFold(strings.TrimSpace(f.Name), part) {
				matchedID = f.ID
				break
			}
		}
		// 2. Normalized match (skip for 4-digit years)
		if matchedID == "" && !taxonomy.IsYear(part) {
			normPart := taxonomy.NormalizeName(part)
			for _, f := range existing {
				if taxonomy.NormalizeName(f.Name) == normPart {
					matchedID = f.ID
					break
				}
			}
		}

		if matchedID != "" {
			currentParent = matchedID
			continue
		}

		// Create missing folder
		params := url.Values{}
		params.Set("fields", "id, name")
		params.Set("supportsAllDrives", "true")

		var created driveFile
		body := map[string]any{
			"name":     part,
			"mimeType": "application/vnd.google-apps.folder",
			"parents":  []string{currentParent},
		}
		if err := c.doJSON(ctx, http.MethodPost, "/files", params, body, &created); err != nil {
			return "", fmt.Errorf("create folder %q: %w", part, err)
		}
		c.Logger.Info("created folder", "name", part, "id", created.ID)
		currentParent = created.ID
	}

	return currentParent, nil
}

// ResolveUniqueFilename appends " (2)", " (3)", etc. if a file with desiredName already exists in destFolderID.
func (c *Client) ResolveUniqueFilename(ctx context.Context, destFolderID, desiredName string) (string, error) {
	base, ext := taxonomy.SplitExt(desiredName)
	candidate := desiredName
	counter := 1

	for {
		q := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false",
			EscapeQuery(destFolderID), EscapeQuery(candidate))
		files, err := c.listFiles(ctx, q, "files(id)", 1)
		if err != nil {
			return "", err
		}
		if len(files) == 0 {
			return candidate, nil
		}
		counter++
		candidate = fmt.Sprintf("%s (%d)%s", base, counter, ext)
	}
}

// FileDocument moves fileID from inboxFolderID to destFolderID and updates sanitized metadata.
func (c *Client) FileDocument(ctx context.Context, fileID, destFolderID string, meta storage.FileMetadata) error {
	params := url.Values{}
	params.Set("fields", "id, name, parents")
	params.Set("supportsAllDrives", "true")
	inboxID := c.getInboxFolderID()
	params.Set("removeParents", inboxID)
	if destFolderID != "" && destFolderID != inboxID {
		params.Set("addParents", destFolderID)
	}

	body := map[string]any{
		"name":        meta.NewName,
		"description": meta.Description,
		"properties":  meta.Properties,
	}
	return c.doJSON(ctx, http.MethodPatch, "/files/"+url.PathEscape(fileID), params, body, nil)
}

// FileEncryptedDocument overwrites origFileID in-place with encryptedBytes via a multipart
// PATCH (so it uses the file owner's My Drive quota rather than the Service Account's 0 GB
// quota), moves it to destFolderID with sanitized metadata, and permanently deletes all
// prior plaintext revisions so no unencrypted version lingers in Google Drive history.
func (c *Client) FileEncryptedDocument(ctx context.Context, origFileID, destFolderID string, encryptedBytes []byte, mimeType string, meta storage.FileMetadata) error {
	tok, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	const boundary = "scans_ai_filer_multipart_boundary_7f9a2c"
	metadataJSON, err := json.Marshal(map[string]any{
		"name":        meta.NewName,
		"mimeType":    mimeType,
		"description": meta.Description,
		"properties":  meta.Properties,
	})
	if err != nil {
		return fmt.Errorf("marshal upload metadata: %w", err)
	}

	var body bytes.Buffer
	fmt.Fprintf(&body, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n", boundary)
	body.Write(metadataJSON)
	fmt.Fprintf(&body, "\r\n--%s\r\nContent-Type: %s\r\n\r\n", boundary, mimeType)
	body.Write(encryptedBytes)
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)

	uploadParams := url.Values{}
	uploadParams.Set("uploadType", "multipart")
	uploadParams.Set("supportsAllDrives", "true")
	uploadParams.Set("fields", "id, name, parents")
	inboxID := c.getInboxFolderID()
	uploadParams.Set("removeParents", inboxID)
	if destFolderID != "" && destFolderID != inboxID {
		uploadParams.Set("addParents", destFolderID)
	}

	uploadURL := strings.TrimRight(c.UploadBaseURL, "/") + "/files/" + url.PathEscape(origFileID) + "?" + uploadParams.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, uploadURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("multipart encrypted update failed: %w", err)
	}
	respBytes, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read upload response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("multipart encrypted update returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	// Purge all prior (plaintext) revisions so only the encrypted head revision remains
	if err := c.purgePriorRevisions(ctx, origFileID); err != nil {
		c.Logger.Warn("encrypted file saved, but could not purge prior plaintext revision(s)", "file_id", origFileID, "err", err)
	}
	return nil
}

func (c *Client) purgePriorRevisions(ctx context.Context, fileID string) error {
	params := url.Values{}
	params.Set("fields", "revisions(id)")
	var revList struct {
		Revisions []struct {
			ID string `json:"id"`
		} `json:"revisions"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/files/"+url.PathEscape(fileID)+"/revisions", params, nil, &revList); err != nil {
		return err
	}
	if len(revList.Revisions) <= 1 {
		return nil
	}
	// Delete every revision except the newest (last) encrypted revision
	for _, rev := range revList.Revisions[:len(revList.Revisions)-1] {
		if err := c.doJSON(ctx, http.MethodDelete, "/files/"+url.PathEscape(fileID)+"/revisions/"+url.PathEscape(rev.ID), nil, nil, nil); err != nil {
			return fmt.Errorf("delete revision %q: %w", rev.ID, err)
		}
		c.Logger.Info("purged plaintext revision", "file_id", fileID, "revision_id", rev.ID)
	}
	return nil
}

// ListFiledDocuments walks the Scans folder hierarchy on Google Drive (skipping Inbox* and Unsorted)
// and returns all already-filed documents with their relative folder paths and metadata.
func (c *Client) ListFiledDocuments(ctx context.Context) ([]taxonomy.FiledDocument, error) {
	scansID := c.getScansFolderID()
	if scansID == "" {
		return nil, fmt.Errorf("gdrive client not initialized; call Init first")
	}

	type queueItem struct {
		id      string
		relPath string
	}
	queue := []queueItem{{id: scansID, relPath: ""}}
	var out []taxonomy.FiledDocument

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		pageToken := ""
		for {
			params := url.Values{}
			params.Set("q", fmt.Sprintf("'%s' in parents and trashed = false", EscapeQuery(cur.id)))
			params.Set("fields", "nextPageToken, files(id, name, mimeType, description, properties)")
			params.Set("pageSize", "200")
			params.Set("supportsAllDrives", "true")
			params.Set("includeItemsFromAllDrives", "true")
			if pageToken != "" {
				params.Set("pageToken", pageToken)
			}

			var resp struct {
				NextPageToken string `json:"nextPageToken"`
				Files         []struct {
					ID          string            `json:"id"`
					Name        string            `json:"name"`
					MimeType    string            `json:"mimeType"`
					Description string            `json:"description"`
					Properties  map[string]string `json:"properties"`
				} `json:"files"`
			}
			if err := c.doJSON(ctx, http.MethodGet, "/files", params, nil, &resp); err != nil {
				return nil, fmt.Errorf("list folder %q: %w", cur.relPath, err)
			}

			for _, f := range resp.Files {
				if f.MimeType == "application/vnd.google-apps.folder" {
					lower := strings.ToLower(strings.TrimSpace(f.Name))
					if cur.relPath == "" && (strings.HasPrefix(lower, "inbox") || lower == "unsorted") {
						continue
					}
					nextPath := f.Name
					if cur.relPath != "" {
						nextPath = cur.relPath + "/" + f.Name
					}
					queue = append(queue, queueItem{id: f.ID, relPath: nextPath})
					continue
				}
				if cur.relPath == "" {
					continue
				}
				out = append(out, taxonomy.FiledDocument{
					ID:          f.ID,
					ParentID:    cur.id,
					MimeType:    f.MimeType,
					FolderPath:  cur.relPath,
					Name:        f.Name,
					Description: f.Description,
					Properties:  f.Properties,
				})
			}

			if resp.NextPageToken == "" {
				break
			}
			pageToken = resp.NextPageToken
		}
	}
	return out, nil
}

// MoveFiledDocument moves and/or renames an already-filed document and updates its structured properties
// without altering its description or binary contents.
func (c *Client) MoveFiledDocument(ctx context.Context, fileID, oldParentID, newParentID, newName string, props map[string]string) error {
	params := url.Values{}
	params.Set("supportsAllDrives", "true")
	params.Set("fields", "id, name, parents")
	if newParentID != "" && newParentID != oldParentID {
		params.Set("addParents", newParentID)
		if oldParentID != "" {
			params.Set("removeParents", oldParentID)
		}
	}
	body := map[string]any{}
	if strings.TrimSpace(newName) != "" {
		body["name"] = newName
	}
	if len(props) > 0 {
		body["properties"] = props
	}
	return c.doJSON(ctx, http.MethodPatch, "/files/"+url.PathEscape(fileID), params, body, nil)
}

// UpdateFiledContent replaces an already-filed document's binary content in place via multipart PATCH
// (e.g. embedding searchable OCR + AES-256 encryption) and updates its name/properties.
func (c *Client) UpdateFiledContent(ctx context.Context, fileID, newName, mimeType string, content []byte, props map[string]string) error {
	tok, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}
	if mimeType == "" {
		mimeType = "application/pdf"
	}

	boundary := fmt.Sprintf("scans_filer_upd_%d", time.Now().UnixNano())
	metaMap := map[string]any{"mimeType": mimeType}
	if strings.TrimSpace(newName) != "" {
		metaMap["name"] = newName
	}
	if len(props) > 0 {
		metaMap["properties"] = props
	}
	metadataJSON, err := json.Marshal(metaMap)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	fmt.Fprintf(&body, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n", boundary)
	body.Write(metadataJSON)
	fmt.Fprintf(&body, "\r\n--%s\r\nContent-Type: %s\r\n\r\n", boundary, mimeType)
	body.Write(content)
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)

	uploadParams := url.Values{}
	uploadParams.Set("uploadType", "multipart")
	uploadParams.Set("supportsAllDrives", "true")
	uploadParams.Set("fields", "id, name")

	uploadURL := strings.TrimRight(c.UploadBaseURL, "/") + "/files/" + url.PathEscape(fileID) + "?" + uploadParams.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, uploadURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("multipart content update failed: %w", err)
	}
	respBytes, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("multipart content update returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil
}

// PruneEmptyFolders walks the Scans folder tree bottom-up (skipping Inbox* and Unsorted)
// and trashes/deletes empty folders left behind after reorganization.
func (c *Client) PruneEmptyFolders(ctx context.Context) (int, error) {
	scansID := c.getScansFolderID()
	if scansID == "" {
		return 0, fmt.Errorf("gdrive client not initialized")
	}

	type folderNode struct {
		id      string
		relPath string
		depth   int
	}
	queue := []folderNode{{id: scansID, relPath: "", depth: 0}}
	var folders []folderNode

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		params := url.Values{}
		params.Set("q", fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", EscapeQuery(cur.id)))
		params.Set("fields", "files(id, name)")
		params.Set("pageSize", "200")
		params.Set("supportsAllDrives", "true")
		params.Set("includeItemsFromAllDrives", "true")

		var resp struct {
			Files []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"files"`
		}
		if err := c.doJSON(ctx, http.MethodGet, "/files", params, nil, &resp); err != nil {
			return 0, err
		}
		for _, f := range resp.Files {
			low := strings.ToLower(strings.TrimSpace(f.Name))
			if cur.depth == 0 && (strings.HasPrefix(low, "inbox") || low == "unsorted") {
				continue
			}
			rel := f.Name
			if cur.relPath != "" {
				rel = cur.relPath + "/" + f.Name
			}
			node := folderNode{id: f.ID, relPath: rel, depth: cur.depth + 1}
			folders = append(folders, node)
			queue = append(queue, node)
		}
	}

	// Process deepest folders first so newly emptied parents can also be pruned
	pruned := 0
	for i := len(folders) - 1; i >= 0; i-- {
		node := folders[i]
		params := url.Values{}
		params.Set("q", fmt.Sprintf("'%s' in parents and trashed = false", EscapeQuery(node.id)))
		params.Set("fields", "files(id)")
		params.Set("pageSize", "1")
		params.Set("supportsAllDrives", "true")
		params.Set("includeItemsFromAllDrives", "true")

		var resp struct {
			Files []struct {
				ID string `json:"id"`
			} `json:"files"`
		}
		if err := c.doJSON(ctx, http.MethodGet, "/files", params, nil, &resp); err != nil {
			continue
		}
		if len(resp.Files) == 0 {
			// Trash the empty folder (safe, recoverable, and works for shared Editor SAs)
			trashParams := url.Values{}
			trashParams.Set("supportsAllDrives", "true")
			if err := c.doJSON(ctx, http.MethodPatch, "/files/"+url.PathEscape(node.id), trashParams, map[string]any{"trashed": true}, nil); err != nil {
				c.Logger.Warn("failed to prune empty folder", "folder", node.relPath, "err", err)
				continue
			}
			c.Logger.Info("pruned empty folder", "folder", node.relPath)
			pruned++
		}
	}
	return pruned, nil
}
