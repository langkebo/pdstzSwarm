package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- TestOpenAIEmbedder_BuildsCorrectRequest ---------------------------------

// Verifies the wire-level request to /v1/embeddings: method, path,
// auth header, and the JSON body (model, input list, optional
// dimensions).
func TestOpenAIEmbedder_BuildsCorrectRequest(t *testing.T) {
	var capturedMethod, capturedPath, capturedAuth string
	var capturedReq oaiEmbeddingRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&capturedReq); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		// Return one vector per input — the OpenAI spec says responses
		// arrive in the same order as inputs.
		items := make([]oaiEmbeddingItem, len(capturedReq.Input))
		for i := range capturedReq.Input {
			items[i] = oaiEmbeddingItem{
				Object:    "embedding",
				Index:     i,
				Embedding: []float32{0.1, 0.2, 0.3},
			}
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{
			Object: "list",
			Data:   items,
			Model:  "text-embedding-3-small",
		})
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:     "test-key",
		Endpoint:   server.URL,
		Model:      "text-embedding-3-small",
		Dimensions: 3,
	})
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len(vecs) = %d, want 2", len(vecs))
	}
	if capturedMethod != "POST" {
		t.Errorf("method = %q, want POST", capturedMethod)
	}
	if capturedPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", capturedPath)
	}
	if capturedAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", capturedAuth)
	}
	if capturedReq.Model != "text-embedding-3-small" {
		t.Errorf("model = %q", capturedReq.Model)
	}
	if len(capturedReq.Input) != 2 || capturedReq.Input[0] != "hello" || capturedReq.Input[1] != "world" {
		t.Errorf("input = %v, want [hello world]", capturedReq.Input)
	}
	if capturedReq.Dimensions != 3 {
		t.Errorf("dimensions = %d, want 3", capturedReq.Dimensions)
	}
}

// --- TestOpenAIEmbedder_HandlesBatching --------------------------------------

// With BatchSize=2 and 5 input texts, the embedder should issue three
// requests: [a,b], [c,d], [e]. We count the calls and verify the result
// order matches the input order across batches.
func TestOpenAIEmbedder_HandlesBatching(t *testing.T) {
	var calls int32
	var capturedBatches [][]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req oaiEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		capturedBatches = append(capturedBatches, req.Input)

		items := make([]oaiEmbeddingItem, len(req.Input))
		for i, _ := range req.Input {
			items[i] = oaiEmbeddingItem{
				Object:    "embedding",
				Index:     i,
				Embedding: []float32{float32(i), float32(i) * 0.5},
			}
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{Data: items})
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:    "k",
		Endpoint:  server.URL,
		Model:     "m",
		BatchSize: 2,
	})

	vecs, err := e.Embed(context.Background(), []string{"a", "b", "c", "d", "e"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
	if len(vecs) != 5 {
		t.Fatalf("len(vecs) = %d, want 5", len(vecs))
	}
	wantBatches := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	for i, want := range wantBatches {
		if i >= len(capturedBatches) {
			t.Fatalf("only saw %d batches, want %d", len(capturedBatches), len(wantBatches))
		}
		if !equalStringSlices(capturedBatches[i], want) {
			t.Errorf("batch %d = %v, want %v", i, capturedBatches[i], want)
		}
	}
}

// --- TestOpenAIEmbedder_RetriesOn5xx ----------------------------------------

// 5xx (and 429) responses must trigger a retry with exponential
// backoff; the second attempt should succeed.
func TestOpenAIEmbedder_RetriesOn5xx(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{
			Data: []oaiEmbeddingItem{{Index: 0, Embedding: []float32{0.5}}},
		})
	}))
	defer server.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{
		APIKey:     "k",
		Endpoint:   server.URL,
		Model:      "m",
		MaxRetries: 3,
		// Disable backoff jitter for fast tests by setting Timeout low.
		Timeout: 5 * time.Second,
	})

	vecs, err := e.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 1 || vecs[0][0] != 0.5 {
		t.Errorf("vecs = %v, want [[0.5]]", vecs)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2 (one 5xx + one success)", got)
	}
}

// --- TestCachedEmbedder_HitsOnDuplicate -------------------------------------

// Two calls with the same input: the second call must not hit the inner
// embedder. We assert by counting inner calls and by checking Len().
func TestCachedEmbedder_HitsOnDuplicate(t *testing.T) {
	var innerCalls int32
	inner := &countingEmbedder{
		dim: 4,
		embed: func(texts []string) ([][]float32, error) {
			atomic.AddInt32(&innerCalls, int32(len(texts)))
			out := make([][]float32, len(texts))
			for i := range texts {
				out[i] = []float32{1, 2, 3, 4}
			}
			return out, nil
		},
	}
	cached := NewCachedEmbedder(inner, 16)

	if _, err := cached.Embed(context.Background(), []string{"alpha", "beta"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := cached.Embed(context.Background(), []string{"alpha", "beta"}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := atomic.LoadInt32(&innerCalls); got != 2 {
		t.Errorf("inner calls = %d, want 2 (one per first call, none on the second)", got)
	}
	if cached.Len() != 2 {
		t.Errorf("cache size = %d, want 2", cached.Len())
	}
}

// --- TestNoopEmbedder_ZeroVector --------------------------------------------

// A Noop embedder of dim 4 must return a 4-zero vector per input.
func TestNoopEmbedder_ZeroVector(t *testing.T) {
	e := NewNoopEmbedder(4)
	if e.Dimensions() != 4 {
		t.Errorf("Dimensions = %d, want 4", e.Dimensions())
	}
	if e.ModelName() != "noop" {
		t.Errorf("ModelName = %q, want noop", e.ModelName())
	}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len = %d, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 4 {
			t.Errorf("vec %d: len = %d, want 4", i, len(v))
		}
		for j, x := range v {
			if x != 0 {
				t.Errorf("vec %d[%d] = %f, want 0", i, j, x)
			}
		}
	}
	// Zero-dim Noop falls back to 1536 to match the pgvector column.
	if NewNoopEmbedder(0).Dimensions() != 1536 {
		t.Errorf("zero-dim Noop should fall back to 1536")
	}
}

// --- TestFactory_DispatchesProvider -----------------------------------------

// The string-dispatching factory must route preset names to the right
// backend and report a useful error for unknown providers.
func TestFactory_DispatchesProvider(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		e, err := NewEmbedder("openai", "k", "https://example.com/v1", "text-embedding-3-small", 1536)
		if err != nil {
			t.Fatalf("NewEmbedder: %v", err)
		}
		if _, ok := e.(*OpenAIEmbedder); !ok {
			t.Errorf("type = %T, want *OpenAIEmbedder", e)
		}
	})
	t.Run("deepseek", func(t *testing.T) {
		e, err := NewEmbedder("deepseek", "k", "", "", 1536)
		if err != nil {
			t.Fatalf("NewEmbedder: %v", err)
		}
		oe := e.(*OpenAIEmbedder)
		if !strings.Contains(oe.endpoint, "deepseek.com") {
			t.Errorf("endpoint = %q, want deepseek.com", oe.endpoint)
		}
		if oe.model != "deepseek-embedding" {
			t.Errorf("model = %q, want default deepseek-embedding", oe.model)
		}
	})
	t.Run("ollama", func(t *testing.T) {
		e, err := NewEmbedder("ollama", "", "", "nomic-embed-text", 768)
		if err != nil {
			t.Fatalf("NewEmbedder: %v", err)
		}
		if _, ok := e.(*OllamaEmbedder); !ok {
			t.Errorf("type = %T, want *OllamaEmbedder", e)
		}
	})
	t.Run("noop", func(t *testing.T) {
		e, err := NewEmbedder("noop", "", "", "", 0)
		if err != nil {
			t.Fatalf("NewEmbedder: %v", err)
		}
		if _, ok := e.(*NoopEmbedder); !ok {
			t.Errorf("type = %T, want *NoopEmbedder", e)
		}
		if e.Dimensions() != 1536 {
			t.Errorf("Noop default dim = %d, want 1536", e.Dimensions())
		}
	})
	t.Run("unknown", func(t *testing.T) {
		_, err := NewEmbedder("fictional", "k", "", "", 0)
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
		if !strings.Contains(err.Error(), "unknown embedder") {
			t.Errorf("error should mention unknown, got: %v", err)
		}
	})
	t.Run("openai_missing_key", func(t *testing.T) {
		_, err := NewEmbedder("openai", "", "", "", 0)
		if err == nil {
			t.Fatal("expected error for missing api_key")
		}
	})
}

// --- TestNewEmbedderFromConfig_BlankProviderYieldsNoop ---------------------

// Config struct with empty provider should produce a graceful Noop
// embedder rather than a startup error.
func TestNewEmbedderFromConfig_BlankProviderYieldsNoop(t *testing.T) {
	e, err := NewEmbedderFromConfig(EmbedderConfig{Dimensions: 768})
	if err != nil {
		t.Fatalf("NewEmbedderFromConfig: %v", err)
	}
	if _, ok := e.(*NoopEmbedder); !ok {
		t.Errorf("type = %T, want *NoopEmbedder", e)
	}
	if e.Dimensions() != 768 {
		t.Errorf("dim = %d, want 768", e.Dimensions())
	}
}

// --- helpers ----------------------------------------------------------------

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- TestLocalStubEmbedder_DeterministicAndL2Normalised ---------------------

// Identical inputs must produce byte-identical vectors; the vectors
// must be L2-normalised (unit length) so cosine similarity behaves
// intuitively; dimensions must match the configured width.
func TestLocalStubEmbedder_DeterministicAndL2Normalised(t *testing.T) {
	e := NewLocalStubEmbedder(LocalStubConfig{Dimensions: 16, Model: "test-stub"})
	if e.Dimensions() != 16 {
		t.Errorf("Dimensions = %d, want 16", e.Dimensions())
	}
	if e.ModelName() != "test-stub" {
		t.Errorf("ModelName = %q, want test-stub", e.ModelName())
	}
	if err := e.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}

	vecs, err := e.Embed(context.Background(), []string{"hello", "world", "hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("len = %d, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 16 {
			t.Errorf("vec[%d] len = %d, want 16", i, len(v))
		}
	}
	// Identical inputs ("hello" at i=0 and i=2) → identical vectors.
	for j := 0; j < 16; j++ {
		if vecs[0][j] != vecs[2][j] {
			t.Errorf("vec[0][%d] != vec[2][%d] (expected identical): %f vs %f",
				j, j, vecs[0][j], vecs[2][j])
		}
	}
	// Different inputs ("hello" vs "world") → different vectors.
	var diff float64
	for j := 0; j < 16; j++ {
		d := float64(vecs[0][j] - vecs[1][j])
		diff += d * d
	}
	if diff == 0 {
		t.Error("hello and world produced identical vectors (hash collision — extremely unlikely)")
	}
	// Each vector should be L2-normalised to ~1.0.
	for i, v := range vecs {
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if sum < 0.99 || sum > 1.01 {
			t.Errorf("vec[%d] L2 norm squared = %f, want ≈1.0", i, sum)
		}
	}
}

// --- TestLocalStubEmbedder_DefaultDimensions --------------------------------

// A zero-value LocalStubConfig must fall back to 384 (the canonical
// small-embedding-model dimensionality).
func TestLocalStubEmbedder_DefaultDimensions(t *testing.T) {
	e := NewLocalStubEmbedder(LocalStubConfig{})
	if e.Dimensions() != 384 {
		t.Errorf("default Dimensions = %d, want 384", e.Dimensions())
	}
	if e.ModelName() != "local-stub" {
		t.Errorf("default ModelName = %q, want local-stub", e.ModelName())
	}
}

// --- TestNewEmbedder_LocalStub ----------------------------------------------

// The string-dispatching factory must recognise the local_stub alias
// and return a LocalStubEmbedder at the configured dimensionality.
func TestNewEmbedder_LocalStub(t *testing.T) {
	e, err := NewEmbedder("local_stub", "", "", "", 256)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	if _, ok := e.(*LocalStubEmbedder); !ok {
		t.Errorf("type = %T, want *LocalStubEmbedder", e)
	}
	if e.Dimensions() != 256 {
		t.Errorf("dim = %d, want 256", e.Dimensions())
	}
}

// --- TestOpenAIEmbedder_HealthCheck -----------------------------------------

// An httptest server returning 200 → nil; 401 → 401 error.
func TestOpenAIEmbedder_HealthCheck(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Errorf("path = %q, want /models", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "k", Endpoint: srv.URL, Model: "m"})
		if err := e.HealthCheck(context.Background()); err != nil {
			t.Errorf("HealthCheck: %v", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "bad", Endpoint: srv.URL, Model: "m"})
		err := e.HealthCheck(context.Background())
		if err == nil {
			t.Fatal("expected 401 error")
		}
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("error should mention 401, got: %v", err)
		}
	})
}

// --- TestEmbedRequest_HonoursModelOverride ----------------------------------

// Per-call Model override must change the model in the request body;
// per-call Dimensions override is similarly wired.
func TestEmbedRequest_HonoursModelOverride(t *testing.T) {
	var capturedModel string
	var capturedDim int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req oaiEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		capturedModel = req.Model
		capturedDim = req.Dimensions
		items := make([]oaiEmbeddingItem, len(req.Input))
		for i := range req.Input {
			items[i] = oaiEmbeddingItem{Index: i, Embedding: []float32{0.1, 0.2, 0.3}}
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{Data: items, Model: req.Model})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "k", Endpoint: srv.URL, Model: "default-model"})
	resp, err := e.EmbedRequest(context.Background(), EmbedRequest{
		Input:      []string{"a", "b"},
		Model:      "override-model",
		Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("EmbedRequest: %v", err)
	}
	if capturedModel != "override-model" {
		t.Errorf("model = %q, want override-model", capturedModel)
	}
	if capturedDim != 3 {
		t.Errorf("dimensions = %d, want 3", capturedDim)
	}
	if len(resp.Vectors) != 2 {
		t.Errorf("len(Vectors) = %d, want 2", len(resp.Vectors))
	}
	if resp.Model != "override-model" {
		t.Errorf("resp.Model = %q, want override-model", resp.Model)
	}
}

// --- TestEmbedRequest_NoOverrideUsesDefaults --------------------------------

// With no Model / Dimensions override the call must hit the embedder
//'s normal fast path.
func TestEmbedRequest_NoOverrideUsesDefaults(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req oaiEmbeddingRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		items := make([]oaiEmbeddingItem, len(req.Input))
		for i := range req.Input {
			items[i] = oaiEmbeddingItem{Index: i, Embedding: []float32{0.5}}
		}
		_ = json.NewEncoder(w).Encode(oaiEmbeddingResponse{Data: items, Model: req.Model})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(OpenAIEmbedderConfig{APIKey: "k", Endpoint: srv.URL, Model: "m"})
	resp, err := e.EmbedRequest(context.Background(), EmbedRequest{Input: []string{"x"}})
	if err != nil {
		t.Fatalf("EmbedRequest: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if resp.Model != "m" {
		t.Errorf("resp.Model = %q, want m", resp.Model)
	}
}

// countingEmbedder wraps an embed function with a per-call counter for
// the cache hit-rate test.
type countingEmbedder struct {
	dim   int
	model string
	embed func([]string) ([][]float32, error)
}

func (c *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(texts)
}
func (c *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(texts)
}
func (c *countingEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	vecs, err := c.embed(req.Input)
	if err != nil {
		return nil, err
	}
	return &EmbedResponse{Vectors: vecs, Model: c.ModelName()}, nil
}
func (c *countingEmbedder) HealthCheck(ctx context.Context) error { return nil }
func (c *countingEmbedder) Dimensions() int                       { return c.dim }
func (c *countingEmbedder) ModelName() string {
	if c.model == "" {
		return "counting"
	}
	return c.model
}

// ensure no unused import in helpers section
var _ = fmt.Sprintf
