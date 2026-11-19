package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Provider turns text into a dense float32 embedding vector.
type Provider interface {
	Name() string
	Embed(ctx context.Context, text string) ([]float32, error)
}

// CosineSimilarity computes the cosine similarity in [-1, 1] between two equal-length vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// EncodeVector serializes a float32 slice as compact little-endian IEEE 754 bytes for SQLite BLOB storage.
func EncodeVector(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeVector deserializes little-endian IEEE 754 bytes back into a float32 slice.
func DecodeVector(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// OllamaEmbedder calls an OpenAI-compatible /embeddings endpoint (Ollama /v1/embeddings, vLLM, OpenAI, LM Studio).
type OllamaEmbedder struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewOllamaEmbedder creates an OllamaEmbedder with sensible defaults (nomic-embed-text).
func NewOllamaEmbedder(baseURL, apiKey, model string, timeout time.Duration) *OllamaEmbedder {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "http://ollama-service.default.svc.cluster.local:11434/v1"
	}
	if strings.TrimSpace(model) == "" {
		model = "nomic-embed-text"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &OllamaEmbedder{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		Model:      strings.TrimSpace(model),
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (o *OllamaEmbedder) Name() string { return "ollama:" + o.Model }

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("cannot embed empty text")
	}
	payload := map[string]any{
		"model": o.Model,
		"input": text,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/embeddings", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode ollama embed response: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("ollama embed returned empty embedding")
	}
	return parsed.Data[0].Embedding, nil
}

// GeminiEmbedder calls the Google Gemini embedContent REST API (default model: text-embedding-004).
type GeminiEmbedder struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

// NewGeminiEmbedder creates a GeminiEmbedder with sensible defaults.
func NewGeminiEmbedder(apiKey, model string, timeout time.Duration) *GeminiEmbedder {
	if strings.TrimSpace(model) == "" {
		model = "text-embedding-004"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &GeminiEmbedder{
		APIKey:     strings.TrimSpace(apiKey),
		Model:      strings.TrimSpace(model),
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta",
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (g *GeminiEmbedder) Name() string { return "gemini:" + g.Model }

func (g *GeminiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("cannot embed empty text")
	}
	payload := map[string]any{
		"content": map[string]any{
			"parts": []map[string]string{{"text": text}},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/models/%s:embedContent", strings.TrimRight(g.BaseURL, "/"), g.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.APIKey != "" {
		req.Header.Set("x-goog-api-key", g.APIKey)
	}

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini embed request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini embed returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode gemini embed response: %w", err)
	}
	if len(parsed.Embedding.Values) == 0 {
		return nil, fmt.Errorf("gemini embed returned empty embedding")
	}
	return parsed.Embedding.Values, nil
}
