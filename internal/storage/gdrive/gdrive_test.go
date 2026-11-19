package gdrive

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/storage"
)

func generateTestSAJSON(t *testing.T, tokenURL string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	b, _ := json.Marshal(ServiceAccountKey{
		ClientEmail: "test-sa@example.iam.gserviceaccount.com",
		PrivateKey:  string(pemBytes),
		TokenURI:    tokenURL,
	})
	return b
}

func TestGDriveWorkflow(t *testing.T) {
	var patchedBody map[string]any
	var patchedQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"test-tok","expires_in":3600}`))
			return

		case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet:
			q := r.URL.Query().Get("q")
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(q, "name = 'Scans'"):
				_, _ = w.Write([]byte(`{"files":[{"id":"scans-id","name":"Scans"}]}`))
			case strings.Contains(q, "'scans-id' in parents") && strings.Contains(q, "name = 'Inbox'"):
				_, _ = w.Write([]byte(`{"files":[{"id":"inbox-id","name":"Inbox"}]}`))
			case strings.Contains(q, "'inbox-id' in parents"):
				_, _ = w.Write([]byte(`{"files":[{"id":"file-1","name":"scan001.pdf","mimeType":"application/pdf","size":"12","createdTime":"2026-11-19T10:00:00Z"}]}`))
			case strings.Contains(q, "'scans-id' in parents") && strings.Contains(q, "mimeType = 'application/vnd.google-apps.folder'"):
				_, _ = w.Write([]byte(`{"files":[{"id":"fin-id","name":"Finance"}]}`))
			case strings.Contains(q, "'fin-id' in parents") && strings.Contains(q, "mimeType = 'application/vnd.google-apps.folder'"):
				_, _ = w.Write([]byte(`{"files":[]}`))
			case strings.Contains(q, "name = '2026-11-19 - Bank - Statement.pdf'"):
				_, _ = w.Write([]byte(`{"files":[{"id":"dup-id"}]}`))
			default:
				_, _ = w.Write([]byte(`{"files":[]}`))
			}
			return

		case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"yr-2026-id","name":"2026"}`))
			return

		case r.URL.Path == "/drive/v3/files/file-1" && r.Method == http.MethodGet:
			if r.URL.Query().Get("alt") == "media" {
				_, _ = w.Write([]byte("%PDF-1.4 mock"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"file-1","parents":["inbox-id"]}`))
			return

		case r.URL.Path == "/drive/v3/files/file-1" && r.Method == http.MethodPatch:
			patchedQuery = r.URL.RawQuery
			_ = json.NewDecoder(r.Body).Decode(&patchedBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"file-1","name":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	saJSON := generateTestSAJSON(t, srv.URL+"/token")
	client, err := NewFromJSON(saJSON, "Scans", "Inbox", nil)
	if err != nil {
		t.Fatalf("NewFromJSON failed: %v", err)
	}
	client.BaseURL = srv.URL + "/drive/v3"

	ctx := context.Background()
	if err := client.Init(ctx); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	files, err := client.ListInbox(ctx, 10)
	if err != nil || len(files) != 1 || files[0].ID != "file-1" {
		t.Fatalf("ListInbox unexpected: files=%+v err=%v", files, err)
	}

	ok, err := client.StillInInbox(ctx, "file-1")
	if err != nil || !ok {
		t.Fatalf("StillInInbox expected true, got %v err=%v", ok, err)
	}

	raw, err := client.Download(ctx, "file-1")
	if err != nil || string(raw) != "%PDF-1.4 mock" {
		t.Fatalf("Download unexpected: %q err=%v", string(raw), err)
	}

	destID, err := client.EnsureFolderPath(ctx, "Finance/2026")
	if err != nil || destID != "yr-2026-id" {
		t.Fatalf("EnsureFolderPath unexpected: id=%q err=%v", destID, err)
	}

	unique, err := client.ResolveUniqueFilename(ctx, destID, "2026-11-19 - Bank - Statement.pdf")
	if err != nil || unique != "2026-11-19 - Bank - Statement (2).pdf" {
		t.Fatalf("ResolveUniqueFilename unexpected: %q err=%v", unique, err)
	}

	err = client.FileDocument(ctx, "file-1", destID, storage.FileMetadata{
		NewName:     unique,
		Description: "Category: Finance",
		Properties:  map[string]string{"category": "Finance"},
	})
	if err != nil {
		t.Fatalf("FileDocument failed: %v", err)
	}
	if !strings.Contains(patchedQuery, "addParents=yr-2026-id") || !strings.Contains(patchedQuery, "removeParents=inbox-id") {
		t.Errorf("unexpected patch query: %s", patchedQuery)
	}
	if patchedBody["name"] != unique {
		t.Errorf("unexpected patched name: %v", patchedBody["name"])
	}
}
