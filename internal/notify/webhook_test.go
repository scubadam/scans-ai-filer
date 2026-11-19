package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookNotifierSanitizedPayload(t *testing.T) {
	var got webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, nil)
	err := n.Send(context.Background(), []FiledItem{
		{Filename: "2026-11-19 - Example Bank - Bank Statement.pdf", Folder: "Scans/Finance/Banking/Example Bank/2026"},
	}, 0)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if got.Count != 1 || got.Failed != 0 {
		t.Errorf("unexpected counts: %+v", got)
	}
	if !strings.Contains(got.Message, "Example Bank - Bank Statement.pdf") {
		t.Errorf("message missing filename: %s", got.Message)
	}
}
