package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrNotImplemented is returned by reference provider stubs that are not yet wired to a live API.
var ErrNotImplemented = errors.New("provider not yet implemented")

// Compile-time interface assertions ensuring the stub stays in sync with Provider & TextCompleter.
var (
	_ Provider      = (*AnthropicProvider)(nil)
	_ TextCompleter = (*AnthropicProvider)(nil)
)

// AnthropicProvider is a reference stub showing how to implement an additional cloud AI
// backend (e.g. Anthropic Claude, AWS Bedrock, Azure OpenAI) for scans-ai-filer.
//
// To implement a new AI provider:
//  1. Copy this file (e.g. internal/ai/myprovider.go) and implement AnalyzeDocument (and optionally CompleteJSON).
//  2. Reuse BuildClassificationPrompt(input) so taxonomy rules and PII sanitization remain consistent.
//  3. Add any new environment variables to internal/config/config.go (Load + Validate).
//  4. Wire the provider case into buildAI() in cmd/filer/main.go.
//  5. Add unit tests using net/http/httptest (see gemini_test.go and ollama_test.go).
type AnthropicProvider struct {
	APIKey string
	Model  string
	Logger *slog.Logger
}

// NewAnthropicProvider constructs a stub Anthropic Claude AI provider.
func NewAnthropicProvider(apiKey, model string, logger *slog.Logger) *AnthropicProvider {
	if model == "" {
		model = "claude-sonnet-4-5"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicProvider{
		APIKey: apiKey,
		Model:  model,
		Logger: logger,
	}
}

// Name returns the provider identifier used in logs.
func (p *AnthropicProvider) Name() string {
	return "anthropic:" + p.Model
}

// AnalyzeDocument classifies a scanned document into PII-sanitized FilingMetadata.
//
// Implementation sketch:
//   - Build the prompt with BuildClassificationPrompt(input).
//   - Send input.Bytes (base64-encoded PDF document block or image block) + prompt to
//     POST https://api.anthropic.com/v1/messages with x-api-key and anthropic-version headers.
//   - Unmarshal the returned JSON into *FilingMetadata and validate required fields.
func (p *AnthropicProvider) AnalyzeDocument(_ context.Context, _ DocumentInput) (*FilingMetadata, error) {
	return nil, fmt.Errorf("anthropic AI provider (%s): %w (see internal/ai/anthropic.go)", p.Model, ErrNotImplemented)
}

// CompleteJSON runs a text-only structured JSON completion (used by AI Taxonomy Gardener
// and filename PII sanitization audits).
func (p *AnthropicProvider) CompleteJSON(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("anthropic text completer (%s): %w (see internal/ai/anthropic.go)", p.Model, ErrNotImplemented)
}
