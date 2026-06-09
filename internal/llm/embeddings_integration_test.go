package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- TestEmbedderIntegration_EndToEnd ---------------------------------------

// Mocks a real OpenAI /v1/embeddings endpoint and verifies the full
// request/response cycle including batch fan-out, response indexing,
// and float32 precision round-trip.
func TestEmbedderIntegration_EndToEnd(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer itest" {
			t.Errorf("Authorization = %q, want Bearer itest", r.Header.Get("Authorization"))
		}

		var req oaiEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Return vectors with deterministic values: index*0.1.
		items := make([]oaiEmbeddingItem, len(req.Input))
		for i := range req.Input {
			v := make([]float32, 4)
			for j := range v {
				v[j] = float32(i)*0.1 + float32(j)*0.01
			}
			items[i] = oaiEmbeddingItem{Object: "embedding", Index: i, Embedding: v}
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{
			Object: "list",
			Data:   items,
			Model:  req.Model,
		})
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:     "itest",
		Endpoint:   server.URL,
		Model:      "text-embedding-3-small",
		BatchSize:  3,
		Dimensions: 4,
	})

	vecs, err := e.Embed(context.Background(), []string{"first", "second", "third", "fourth", "fifth"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 5 {
		t.Fatalf("len(vecs) = %d, want 5", len(vecs))
	}
	// 5 inputs / batch 3 → 2 requests.
	if requestCount != 2 {
		t.Errorf("requestCount = %d, want 2", requestCount)
	}
	// First vector is index 0 → first element is 0.0; subsequent
	// elements are j*0.01 = 0.01, 0.02, 0.03.
	if vecs[0][0] != 0 {
		t.Errorf("vec[0][0] = %f, want 0", vecs[0][0])
	}
	if vecs[0][1] < 0.009 || vecs[0][1] > 0.011 {
		t.Errorf("vec[0][1] = %f, want ≈0.01", vecs[0][1])
	}
	// Second vector is index 1 → first element ≈ 0.1.
	if vecs[1][0] < 0.099 || vecs[1][0] > 0.101 {
		t.Errorf("vec[1][0] = %f, want ≈0.1", vecs[1][0])
	}
	// 5 inputs / batch 3 → 2 requests. The embedder preserves input
	// order across batches: vecs[i] is the i-th input's vector.
	// Within a batch, the mock returns v[j] = i*0.1 + j*0.01 where
	// i is the position within the batch. So vecs[4] comes from the
	// second batch's index 1 → first element ≈ 0.1, second ≈ 0.11.
	if vecs[4][0] < 0.099 || vecs[4][0] > 0.101 {
		t.Errorf("vec[4][0] = %f, want ≈0.1 (from second batch's index 1)", vecs[4][0])
	}
	if vecs[4][1] < 0.109 || vecs[4][1] > 0.111 {
		t.Errorf("vec[4][1] = %f, want ≈0.11 (from second batch's index 1)", vecs[4][1])
	}
}

// --- TestSummarizerIntegration_WithMockProvider ------------------------------

// Verifies the Summarizer end-to-end: NeedsSummarization gates
// correctly, the split point is right, the summary message is
// prepended, and the kept messages are in original order.
func TestSummarizerIntegration_WithMockProvider(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{
		TriggerAtRatio:         0.5,
		KeepRecentRatio:        0.5,
		MinMessagesToSummarize: 4,
		MaxTokens:              256,
		Temperature:            0.0,
	})
	p.completeResp = "ok"

	msgs := []Message{
		{Role: "user", Content: "recon target example.com"},
		{Role: "assistant", Content: "found 3 subdomains"},
		{Role: "user", Content: "run nuclei on each"},
		{Role: "assistant", Content: "scanned 3 found 1 high vuln"},
		{Role: "user", Content: "exploit the cve"},
		{Role: "assistant", Content: "got a session"},
		{Role: "user", Content: "what next"},
		{Role: "assistant", Content: "write report"},
	}
	// 8 msgs * 40 chars = ~80 tokens < 50% of 1000 → does NOT need summarization
	if s.NeedsSummarization(msgs) {
		t.Skip("messages under threshold; skipping actual summarise call")
	}

	// Force the trigger by using a tiny window.
	p2 := &mockProvider{contextWindow: 50, modelName: "test"}
	p2.completeResp = "ok" // shared default mock would return "summary text"
	s2 := NewSummarizer(p2, SummarizerConfig{
		TriggerAtRatio:         0.5,
		KeepRecentRatio:        0.5,
		MinMessagesToSummarize: 4,
	})
	if !s2.NeedsSummarization(msgs) {
		t.Fatal("expected NeedsSummarization to be true with tiny window")
	}

	out, err := s2.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// 8 × 0.5 = 4 kept + 1 summary = 5.
	if len(out) != 5 {
		t.Errorf("len(out) = %d, want 5", len(out))
	}
	if !strings.Contains(out[0].Content, "ok") {
		t.Errorf("summary = %q, want it to contain the mock response", out[0].Content)
	}
	// The 4 kept messages are the last 4 of the input.
	for i, want := range []string{"exploit the cve", "got a session", "what next", "write report"} {
		if out[1+i].Content != want {
			t.Errorf("kept[%d] = %q, want %q", i, out[1+i].Content, want)
		}
	}

	// Provider received the correct request.
	if p2.lastReq == nil {
		t.Fatal("provider not called")
	}
	if !strings.Contains(p2.lastReq.SystemPrompt, "summarization") {
		t.Errorf("system prompt = %q, want it to mention summarization", p2.lastReq.SystemPrompt)
	}
}

// --- TestEmbedderIntegration_OpenAIError_ReturnsError -----------------------

// Permanent 4xx errors must NOT be retried — the embedder returns the
// error after a single attempt.
func TestEmbedderIntegration_OpenAIError_ReturnsError(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad input"}}`))
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:     "k",
		Endpoint:   server.URL,
		Model:      "m",
		MaxRetries: 3,
	})
	_, err := e.Embed(context.Background(), []string{"bad"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (4xx must not retry)", attempts)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
}

// --- TestEmbedderIntegration_OpenAIRateLimited -----------------------------

// 429 should retry until success, with backoff between attempts.
func TestEmbedderIntegration_OpenAIRateLimited(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{
			Data: []oaiEmbeddingItem{{Index: 0, Embedding: []float32{0.42}}},
		})
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:     "k",
		Endpoint:   server.URL,
		Model:      "m",
		MaxRetries: 3,
		Timeout:    2 * time.Second,
	})
	vecs, err := e.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if len(vecs) != 1 || vecs[0][0] != 0.42 {
		t.Errorf("vecs = %v, want [[0.42]]", vecs)
	}
}
