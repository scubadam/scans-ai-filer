package ai

import (
	"context"
	"errors"
	"testing"
)

func TestAnthropicProviderStub(t *testing.T) {
	p := NewAnthropicProvider("test-key", "", nil)
	if p.Name() != "anthropic:claude-sonnet-4-5" {
		t.Fatalf("unexpected Name(): %q", p.Name())
	}

	if _, err := p.AnalyzeDocument(context.Background(), DocumentInput{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from AnalyzeDocument, got %v", err)
	}
	if _, err := p.CompleteJSON(context.Background(), "test"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented from CompleteJSON, got %v", err)
	}
}
