package embed

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCosineAndEncoding(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}

	if sim := CosineSimilarity(a, b); math.Abs(float64(sim-1.0)) > 1e-6 {
		t.Errorf("expected 1.0, got %f", sim)
	}
	if sim := CosineSimilarity(a, c); math.Abs(float64(sim)) > 1e-6 {
		t.Errorf("expected 0.0, got %f", sim)
	}

	orig := []float32{0.125, -0.5, 3.14159, 0}
	roundTrip := DecodeVector(EncodeVector(orig))
	if len(roundTrip) != len(orig) {
		t.Fatalf("length mismatch: %d vs %d", len(roundTrip), len(orig))
	}
	for i := range orig {
		if roundTrip[i] != orig[i] {
			t.Errorf("index %d mismatch: %f != %f", i, roundTrip[i], orig[i])
		}
	}
}

func TestOllamaAndGeminiEmbedders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/embeddings":
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
		case "/models/text-embedding-004:embedContent":
			_, _ = w.Write([]byte(`{"embedding":{"values":[0.4,0.5,0.6]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ol := NewOllamaEmbedder(srv.URL, "", "nomic-embed-text", 5*time.Second)
	vec, err := ol.Embed(context.Background(), "Octopus Energy electricity bill")
	if err != nil || len(vec) != 3 || vec[0] != 0.1 {
		t.Fatalf("unexpected ollama embed result: vec=%v err=%v", vec, err)
	}

	gm := NewGeminiEmbedder("test-key", "text-embedding-004", 5*time.Second)
	gm.BaseURL = srv.URL
	gvec, err := gm.Embed(context.Background(), "NHS appointment letter")
	if err != nil || len(gvec) != 3 || gvec[0] != 0.4 {
		t.Fatalf("unexpected gemini embed result: vec=%v err=%v", gvec, err)
	}
}
