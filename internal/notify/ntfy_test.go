package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNtfyNotifierSuccessAndErrorRouting(t *testing.T) {
	type capturedReq struct {
		Path     string
		Title    string
		Priority string
		Tags     string
		Click    string
		Auth     string
		Body     string
	}

	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedReq{
			Path:     r.URL.Path,
			Title:    r.Header.Get("Title"),
			Priority: r.Header.Get("Priority"),
			Tags:     r.Header.Get("Tags"),
			Click:    r.Header.Get("Click"),
			Auth:     r.Header.Get("Authorization"),
			Body:     string(body),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNtfyNotifier(NtfyConfig{
		BaseURL:       srv.URL,
		Topic:         "scans-filed",
		ErrorTopic:    "scans-errors",
		Token:         "tk_secret123",
		Priority:      "low",
		ErrorPriority: "urgent",
		ClickURL:      "https://scans.homelab",
	}, nil)

	ctx := context.Background()

	// 1. Pure success -> routes to Topic with success priority/tags & Bearer auth.
	err := n.Send(ctx, []FiledItem{
		{Filename: "2026-11-22 - Acme Bank - Statement.pdf", Folder: "Scans/Finance/Acme Bank/2026"},
	}, 0)
	if err != nil {
		t.Fatalf("success Send failed: %v", err)
	}

	// 2. Batch with failure -> routes to ErrorTopic with ErrorPriority & error tags.
	err = n.Send(ctx, nil, 2)
	if err != nil {
		t.Fatalf("error Send failed: %v", err)
	}

	if len(reqs) != 2 {
		t.Fatalf("expected 2 ntfy requests, got %d", len(reqs))
	}

	if reqs[0].Path != "/scans-filed" || reqs[0].Priority != "low" || reqs[0].Auth != "Bearer tk_secret123" || reqs[0].Click != "https://scans.homelab" {
		t.Errorf("unexpected success request: %+v", reqs[0])
	}
	if !strings.Contains(reqs[0].Title, "1 document filed") || !strings.Contains(reqs[0].Body, "Acme Bank - Statement.pdf") {
		t.Errorf("unexpected success title/body: %+v", reqs[0])
	}

	if reqs[1].Path != "/scans-errors" || reqs[1].Priority != "urgent" || !strings.Contains(reqs[1].Tags, "warning") {
		t.Errorf("unexpected error request: %+v", reqs[1])
	}
	if !strings.Contains(reqs[1].Title, "failed (2 errors)") {
		t.Errorf("unexpected error title: %q", reqs[1].Title)
	}
}

func TestEventFilterSuccessVsError(t *testing.T) {
	errorOnly := EventFilter{OnSuccess: false, OnError: true}
	if errorOnly.ShouldSend(2, 0) {
		t.Errorf("error-only filter should not send on pure success")
	}
	if !errorOnly.ShouldSend(0, 1) || !errorOnly.ShouldSend(2, 1) {
		t.Errorf("error-only filter should send when failed > 0")
	}

	successOnly := EventFilter{OnSuccess: true, OnError: false}
	if !successOnly.ShouldSend(1, 0) {
		t.Errorf("success-only filter should send on success")
	}
	if successOnly.ShouldSend(0, 2) {
		t.Errorf("success-only filter should not send on pure failure")
	}
	if successOnly.ShouldSend(0, 0) || errorOnly.ShouldSend(0, 0) {
		t.Errorf("filters should never send on empty batch")
	}
}
