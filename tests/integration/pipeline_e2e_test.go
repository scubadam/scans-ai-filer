//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/scubadam/scans-ai-filer/internal/ai"
	"github.com/scubadam/scans-ai-filer/internal/filer"
	"github.com/scubadam/scans-ai-filer/internal/notify"
	"github.com/scubadam/scans-ai-filer/internal/storage/gdrive"
	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

type fakeDriveNode struct {
	ID          string
	Name        string
	MimeType    string
	Parents     []string
	Bytes       []byte
	Description string
	Properties  map[string]string
}

type fakeDriveState struct {
	mu      sync.Mutex
	nextID  int
	nodes   map[string]*fakeDriveNode
	created []string
}

func newFakeDriveState() *fakeDriveState {
	s := &fakeDriveState{
		nextID: 100,
		nodes:  make(map[string]*fakeDriveNode),
	}
	s.nodes["scans-root"] = &fakeDriveNode{ID: "scans-root", Name: "Scans", MimeType: "application/vnd.google-apps.folder"}
	s.nodes["inbox-dir"] = &fakeDriveNode{ID: "inbox-dir", Name: "Inbox", MimeType: "application/vnd.google-apps.folder", Parents: []string{"scans-root"}}
	// Pre-existing file to exercise duplicate "(2)" filename resolution
	s.nodes["fin-dir"] = &fakeDriveNode{ID: "fin-dir", Name: "Finance", MimeType: "application/vnd.google-apps.folder", Parents: []string{"scans-root"}}
	s.nodes["bank-dir"] = &fakeDriveNode{ID: "bank-dir", Name: "Example Bank", MimeType: "application/vnd.google-apps.folder", Parents: []string{"fin-dir"}}
	s.nodes["yr-2026"] = &fakeDriveNode{ID: "yr-2026", Name: "2026", MimeType: "application/vnd.google-apps.folder", Parents: []string{"bank-dir"}}
	s.nodes["existing-doc"] = &fakeDriveNode{
		ID:       "existing-doc",
		Name:     "2026-11-19 - Example Bank - Bank Statement - Monthly Statement.pdf",
		MimeType: "application/pdf",
		Parents:  []string{"yr-2026"},
	}
	// Two inbox documents to file
	s.nodes["inbox-1"] = &fakeDriveNode{
		ID:       "inbox-1",
		Name:     "scan_001.pdf",
		MimeType: "application/pdf",
		Parents:  []string{"inbox-dir"},
		Bytes:    []byte("%PDF-1.4 bank statement content"),
	}
	s.nodes["inbox-2"] = &fakeDriveNode{
		ID:       "inbox-2",
		Name:     "scan_002.pdf",
		MimeType: "application/pdf",
		Parents:  []string{"inbox-dir"},
		Bytes:    []byte("%PDF-1.4 utility bill content"),
	}
	return s
}

func (s *fakeDriveState) handler() http.Handler {
	parentRe := regexp.MustCompile(`'([^']+)' in parents`)
	nameRe := regexp.MustCompile(`name = '([^']+)'`)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"integration-tok","expires_in":3600}`))
			return

		case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet:
			q := r.URL.Query().Get("q")
			s.mu.Lock()
			defer s.mu.Unlock()

			var wantParent, wantName string
			if m := parentRe.FindStringSubmatch(q); len(m) == 2 {
				wantParent = m[1]
			}
			if m := nameRe.FindStringSubmatch(q); len(m) == 2 {
				wantName = m[1]
			}
			onlyFolders := strings.Contains(q, "mimeType = 'application/vnd.google-apps.folder'")
			excludeFolders := strings.Contains(q, "mimeType != 'application/vnd.google-apps.folder'")

			var matched []map[string]any
			for _, n := range s.nodes {
				if onlyFolders && n.MimeType != "application/vnd.google-apps.folder" {
					continue
				}
				if excludeFolders && n.MimeType == "application/vnd.google-apps.folder" {
					continue
				}
				if wantParent != "" && !contains(n.Parents, wantParent) {
					continue
				}
				if wantName != "" && n.Name != wantName {
					continue
				}
				matched = append(matched, map[string]any{
					"id":          n.ID,
					"name":        n.Name,
					"mimeType":    n.MimeType,
					"size":        fmt.Sprintf("%d", len(n.Bytes)),
					"createdTime": "2026-11-19T10:00:00Z",
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": matched})
			return

		case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodPost:
			var body struct {
				Name     string   `json:"name"`
				MimeType string   `json:"mimeType"`
				Parents  []string `json:"parents"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.nextID++
			id := fmt.Sprintf("gen-%d", s.nextID)
			s.nodes[id] = &fakeDriveNode{ID: id, Name: body.Name, MimeType: body.MimeType, Parents: body.Parents}
			s.created = append(s.created, body.Name)
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body.Name})
			return

		case strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			s.mu.Lock()
			n := s.nodes[id]
			s.mu.Unlock()
			if n == nil {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("alt") == "media" {
				_, _ = w.Write(n.Bytes)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": n.ID, "parents": n.Parents})
			return

		case strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && r.Method == http.MethodPatch:
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			var body struct {
				Name        string            `json:"name"`
				Description string            `json:"description"`
				Properties  map[string]string `json:"properties"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addParent := r.URL.Query().Get("addParents")
			removeParent := r.URL.Query().Get("removeParents")

			s.mu.Lock()
			n := s.nodes[id]
			if n != nil {
				n.Name = body.Name
				n.Description = body.Description
				n.Properties = body.Properties
				var newParents []string
				for _, p := range n.Parents {
					if p != removeParent {
						newParents = append(newParents, p)
					}
				}
				if addParent != "" {
					newParents = append(newParents, addParent)
				}
				n.Parents = newParents
			}
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": body.Name})
			return
		}
		http.NotFound(w, r)
	})
}

func contains(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func generateTestSA(t *testing.T, tokenURL string) []byte {
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
	b, _ := json.Marshal(gdrive.ServiceAccountKey{
		ClientEmail: "integration@example.iam.gserviceaccount.com",
		PrivateKey:  string(pemBytes),
		TokenURI:    tokenURL,
	})
	return b
}

func TestPipelineE2EStatefulDriveGeminiWebhook(t *testing.T) {
	driveState := newFakeDriveState()
	driveSrv := httptest.NewServer(driveState.handler())
	defer driveSrv.Close()

	// Mock Gemini server returning different classifications per call
	var aiCalls int
	var aiMu sync.Mutex
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiMu.Lock()
		aiCalls++
		callNum := aiCalls
		aiMu.Unlock()

		var meta ai.FilingMetadata
		if callNum == 1 {
			meta = ai.FilingMetadata{
				DocumentDate:        "2026-11-19",
				Organization:        "Example Bank",
				DocumentType:        "Bank Statement",
				Category:            "Finance",
				Subcategory:         "Banking",
				Summary:             "Monthly Statement",
				SuggestedFilename:   "2026-11-19 - Example Bank - Bank Statement - Monthly Statement.pdf",
				SuggestedFolderPath: "Finance/Example Bank/2026",
				IsTaxonomyMatch:     true,
			}
		} else {
			meta = ai.FilingMetadata{
				DocumentDate:        "2026-11-19",
				Organization:        "Example Energy",
				DocumentType:        "Utility Bill",
				Category:            "Utilities",
				Subcategory:         "Energy",
				Summary:             "Quarterly Statement",
				SuggestedFilename:   "2026-11-19 - Example Energy - Utility Bill - Quarterly Statement.pdf",
				SuggestedFolderPath: "Utilities/Example Energy/2026",
				IsTaxonomyMatch:     false,
			}
		}
		metaBytes, _ := json.Marshal(meta)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": string(metaBytes)}},
					},
				},
			},
		})
	}))
	defer geminiSrv.Close()

	var webhookPayload map[string]any
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&webhookPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	store, err := gdrive.NewFromJSON(generateTestSA(t, driveSrv.URL+"/token"), "Scans", "Inbox", nil)
	if err != nil {
		t.Fatalf("NewFromJSON: %v", err)
	}
	store.BaseURL = driveSrv.URL + "/drive/v3"

	gem := ai.NewGeminiProvider("test-key", "gemini-2.5-flash", nil)
	gem.BaseURL = geminiSrv.URL

	notifier := notify.NewWebhookNotifier(webhookSrv.URL, nil)

	f := &filer.Filer{Storage: store, AI: gem, Notifier: notifier}
	res, err := f.Run(context.Background(), filer.Options{
		ScansFolderName: "Scans",
		BatchSize:       10,
		Taxonomy: []taxonomy.Rule{
			{Category: "Finance", Subcategory: "Banking", Organization: "Example Bank", PathTemplate: "Finance/Example Bank/{year}"},
		},
	})
	if err != nil {
		t.Fatalf("Filer.Run failed: %v", err)
	}
	if res.Succeeded != 2 || res.Failed != 0 {
		t.Fatalf("expected 2 succeeded / 0 failed, got %+v", res)
	}

	// Verify duplicate collision resolution "(2)" occurred on the Bank Statement
	var sawDup, sawEnergy bool
	for _, id := range []string{"inbox-1", "inbox-2"} {
		n := driveState.nodes[id]
		if contains(n.Parents, "inbox-dir") {
			t.Errorf("file %s still in Inbox after filing", id)
		}
		if strings.Contains(n.Description, "Details:") {
			t.Errorf("file %s description leaked Details field: %s", id, n.Description)
		}
		if strings.HasSuffix(n.Name, " (2).pdf") {
			sawDup = true
		}
		if strings.Contains(n.Name, "Example Energy") {
			sawEnergy = true
		}
	}
	if !sawDup {
		t.Errorf("expected duplicate collision resolution ' (2).pdf' for Example Bank statement")
	}
	if !sawEnergy {
		t.Errorf("expected Example Energy document to be filed")
	}
	if webhookPayload == nil || webhookPayload["count"] != float64(2) {
		t.Errorf("unexpected webhook payload: %+v", webhookPayload)
	}
}
