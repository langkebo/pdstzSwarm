package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Embedder produces dense vector representations of text. The returned
// vectors are intended to be persisted to pgvector and used for
// similarity search across swarm findings.
//
// Implementations are safe for concurrent use. Inputs are typically short
// documents (finding titles + descriptions), and the batch API is the
// hot path. Implementations MUST validate the dimensions() they advertise
// match what the backing model actually returns — callers rely on the
// declared dimensionality for pgvector column width.
//
// The interface also exposes the higher-level EmbedRequest/EmbedResponse
// shape for callers that need to pass per-call model overrides or
// read back token usage. The default Embed(texts) path is sugar for
// EmbedRequest{Input: texts}.
type Embedder interface {
	// Embed returns one vector per input text, in the same order.
	// Implementations MAY batch transparently — callers do not need to
	// split inputs. An empty input slice returns an empty result.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// EmbedBatch is a convenience alias for Embed. Provided so the
	// signature is self-documenting at call sites.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// EmbedRequest is the request-shape variant. Implementations MUST
	// honour Input, Model (empty → implementation default), and
	// Dimensions (0 → implementation default). EncodingFormat / User
	// are advisory and MAY be ignored by implementations that do not
	// speak the OpenAI extras (Anthropic, Cohere, etc.).
	EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)

	// Dimensions returns the vector dimensionality the embedder produces.
	// Must match the pgvector column width configured in the schema
	// (currently 1536 for text-embedding-3-small / Ollama nomic-embed-text).
	Dimensions() int

	// ModelName returns the underlying model identifier (for observability
	// and cache key namespaces).
	ModelName() string

	// HealthCheck verifies the embedder is reachable and configured
	// correctly (key valid, model installed). Returns nil for in-memory
	// embedders (Noop / LocalStub / CachedEmbedder) and live endpoints
	// that responded with 2xx.
	HealthCheck(ctx context.Context) error
}

// EmbedRequest is the higher-level call shape. The lower-level
// Embed(texts) is sugar for EmbedRequest{Input: texts}.
type EmbedRequest struct {
	// Input is the list of texts to embed. Order is preserved in
	// the response.
	Input []string
	// Model overrides the default model for this call only. Empty
	// → use the embedder's default model.
	Model string
	// Dimensions overrides the default vector width. 0 → use the
	// embedder's default.
	Dimensions int
	// EncodingFormat is "float" (default, returns []float32) or
	// "base64" (encodes the float32 vector as a base64 string). The
	// base64 path is reserved for OpenAI; other implementations
	// return []float32 even when this is set.
	EncodingFormat string
	// User is an OpenAI abuse-tracking identifier. Optional.
	User string
}

// EmbedResponse is the per-call response.
type EmbedResponse struct {
	// Vectors is parallel to EmbedRequest.Input.
	Vectors [][]float32
	// Usage is the token accounting for the call. Implementations
	// that don't track tokens leave this as zero values.
	Usage EmbedUsage
	// Model echoes the model that actually served the call.
	Model string
}

// EmbedUsage is the token accounting for a single Embed call.
type EmbedUsage struct {
	// PromptTokens is the number of input tokens billed by the
	// embedder (for OpenAI: sum of tokenised input strings).
	PromptTokens int
	// TotalTokens is the total billed tokens. For OpenAI it equals
	// PromptTokens (no output tokens). Implementations that don't
	// track tokens leave this zero.
	TotalTokens int
}

// --- OpenAI-compatible Embedder (covers OpenAI / DeepSeek / GLM / Kimi / Qwen) ---

// OpenAIEmbedderConfig configures the OpenAI-compatible embeddings client.
// The same `/v1/embeddings` endpoint is spoken by every major domestic
// Chinese LLM provider when OpenAI compatibility mode is enabled, so this
// one struct + one client covers them all.
type OpenAIEmbedderConfig struct {
	APIKey     string
	Endpoint   string        // base URL, e.g. https://api.openai.com/v1
	Model      string        // text-embedding-3-small, Qwen/Qwen3-Embedding-8B, etc.
	Dimensions int           // 0 → do not pass `dimensions` (use model default)
	BatchSize  int           // texts per HTTP request; 0 → 32
	Timeout    time.Duration // 0 → 30s
	MaxRetries int           // 0 → 3
}

// OpenAIEmbedder speaks the OpenAI `/v1/embeddings` protocol. The same
// request/response shape is used by DeepSeek, Zhipu GLM, Moonshot Kimi and
// Alibaba Qwen when running in OpenAI compatibility mode, so adding a new
// vendor is a base-URL change.
type OpenAIEmbedder struct {
	endpoint   string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	timeout    time.Duration
	maxRetries int
	httpClient *http.Client
}

// NewOpenAIEmbedder builds the embedder and fills zero-value defaults
// with sensible production values.
func NewOpenAIEmbedder(cfg OpenAIEmbedderConfig) *OpenAIEmbedder {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.openai.com/v1"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Model == "" {
		cfg.Model = "text-embedding-3-small"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	return &OpenAIEmbedder{
		endpoint:   cfg.Endpoint,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  cfg.BatchSize,
		timeout:    cfg.Timeout,
		maxRetries: cfg.MaxRetries,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

// oaiEmbeddingRequest mirrors the OpenAI /v1/embeddings payload.
type oaiEmbeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type oaiEmbeddingResponse struct {
	Object string             `json:"object"`
	Data   []oaiEmbeddingItem `json:"data"`
	Model  string             `json:"model"`
	Usage  struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type oaiEmbeddingItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func (e *OpenAIEmbedder) Dimensions() int { return e.dimensions }
func (e *OpenAIEmbedder) ModelName() string { return e.model }

// EmbedBatch is sugar for Embed. Provided so the Embedder interface
// stays self-documenting at call sites that want to emphasise "this
// is a batch" vs "this is one of many calls in a row".
func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}

// EmbedRequest is the request-shape variant. Per-call Model / Dimensions
// overrides are honoured by re-issuing a one-off embedder with the
// override values; this is the simplest correct behaviour since the
// underlying http client was bound to a single model/dim at construction
// time.
func (e *OpenAIEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	// Fast path: no per-call overrides → reuse the cached client.
	if req.Model == "" && (req.Dimensions == 0 || req.Dimensions == e.dimensions) {
		vecs, err := e.Embed(ctx, req.Input)
		if err != nil {
			return nil, err
		}
		return &EmbedResponse{Vectors: vecs, Model: e.model}, nil
	}
	override := *e
	if req.Model != "" {
		override.model = req.Model
	}
	if req.Dimensions > 0 {
		override.dimensions = req.Dimensions
	}
	vecs, err := override.Embed(ctx, req.Input)
	if err != nil {
		return nil, err
	}
	return &EmbedResponse{Vectors: vecs, Model: override.model}, nil
}

// HealthCheck pings the OpenAI /v1/models endpoint. Useful at startup
// to surface invalid keys or unreachable endpoints before the first
// real request fails a finding write.
func (e *OpenAIEmbedder) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.endpoint+"/models", nil)
	if err != nil {
		return fmt.Errorf("creating openai health check request: %w", err)
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("openai embeddings endpoint unreachable at %s: %w", e.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("openai embeddings at %s returned 401 — check API key", e.endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openai embeddings at %s returned status %d", e.endpoint, resp.StatusCode)
	}
	return nil
}

// Embed batches the inputs and fans out up to e.batchSize at a time.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vectors, err := e.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		if len(vectors) != len(batch) {
			return nil, fmt.Errorf("openai embeddings: got %d vectors for %d inputs", len(vectors), len(batch))
		}
		copy(results[start:end], vectors)
	}
	return results, nil
}

// embedBatch posts a single /v1/embeddings request. Retries on 429 / 5xx
// with exponential backoff; 4xx errors are returned immediately.
func (e *OpenAIEmbedder) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	req := oaiEmbeddingRequest{Model: e.model, Input: batch}
	if e.dimensions > 0 {
		req.Dimensions = e.dimensions
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling embeddings request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", e.endpoint+"/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating embeddings request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if e.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
		}

		resp, err := e.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if attempt == e.maxRetries {
				return nil, fmt.Errorf("openai embeddings request failed: %w", err)
			}
			if backoffErr := openaiEmbedBackoff(ctx, attempt); backoffErr != nil {
				return nil, backoffErr
			}
			continue
		}

		// 429 / 5xx → retryable
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("openai embeddings returned status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
			if attempt == e.maxRetries {
				return nil, lastErr
			}
			if backoffErr := openaiEmbedBackoff(ctx, attempt); backoffErr != nil {
				return nil, backoffErr
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("openai embeddings returned status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
		}

		var oaiResp oaiEmbeddingResponse
		if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding openai embeddings response: %w", err)
		}
		resp.Body.Close()

		// Sort by index since the OpenAI spec is order-stable but we
		// defend against providers that may reorder.
		vectors := make([][]float32, len(oaiResp.Data))
		for _, item := range oaiResp.Data {
			if item.Index < 0 || item.Index >= len(vectors) {
				return nil, fmt.Errorf("openai embeddings: index %d out of range", item.Index)
			}
			vectors[item.Index] = item.Embedding
		}
		return vectors, nil
	}
	return nil, fmt.Errorf("openai embeddings failed after %d retries: %w", e.maxRetries, lastErr)
}

func openaiEmbedBackoff(ctx context.Context, attempt int) error {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	jitter := time.Duration(rand.Int63n(int64(time.Second)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff + jitter):
		return nil
	}
}

// --- Ollama Embedder ---

// OllamaEmbedderConfig configures the Ollama embedding client.
type OllamaEmbedderConfig struct {
	Endpoint   string
	Model      string // nomic-embed-text, mxbai-embed-large, etc.
	Dimensions int
	KeepAlive  string        // Ollama model unload window, e.g. "5m"; "" → server default
	Timeout    time.Duration // 0 → 60s (Ollama first-call cold start is slow)
}

// OllamaEmbedder calls POST /api/embeddings on an Ollama daemon. Useful
// for air-gapped deployments and in mainland China where domestic
// cloud LLM APIs are not always reachable.
type OllamaEmbedder struct {
	endpoint   string
	model      string
	dimensions int
	keepAlive  string
	timeout    time.Duration
	httpClient *http.Client
}

// NewOllamaEmbedder builds the embedder with sensible defaults.
func NewOllamaEmbedder(cfg OllamaEmbedderConfig) *OllamaEmbedder {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &OllamaEmbedder{
		endpoint:   cfg.Endpoint,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		keepAlive:  cfg.KeepAlive,
		timeout:    cfg.Timeout,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

type ollamaEmbeddingRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	KeepAlive string `json:"keep_alive,omitempty"`
}

type ollamaEmbeddingResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (e *OllamaEmbedder) Dimensions() int  { return e.dimensions }
func (e *OllamaEmbedder) ModelName() string { return e.model }

// EmbedBatch is sugar for Embed.
func (e *OllamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}

// EmbedRequest honours per-call Model / Dimensions overrides by
// re-issuing with a one-off client. The Embedder fast path applies
// when no overrides are set.
func (e *OllamaEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if req.Model == "" && (req.Dimensions == 0 || req.Dimensions == e.dimensions) {
		vecs, err := e.Embed(ctx, req.Input)
		if err != nil {
			return nil, err
		}
		return &EmbedResponse{Vectors: vecs, Model: e.model}, nil
	}
	override := *e
	if req.Model != "" {
		override.model = req.Model
	}
	if req.Dimensions > 0 {
		override.dimensions = req.Dimensions
	}
	vecs, err := override.Embed(ctx, req.Input)
	if err != nil {
		return nil, err
	}
	return &EmbedResponse{Vectors: vecs, Model: override.model}, nil
}

// HealthCheck queries the Ollama /api/tags endpoint and verifies the
// configured model is installed. Same semantics as OllamaProvider.HealthCheck.
func (e *OllamaEmbedder) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.endpoint+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("creating ollama embeddings health check: %w", err)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama embeddings endpoint unreachable at %s: %w", e.endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama embeddings at %s returned status %d", e.endpoint, resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("parsing ollama tags response: %w", err)
	}
	for _, m := range body.Models {
		if m.Name == e.model || m.Name == e.model+":latest" {
			return nil
		}
	}
	return fmt.Errorf("ollama embedding model %q not found — run: ollama pull %s", e.model, e.model)
}

// Embed hits Ollama once per text. Ollama does not currently expose a
// batch embeddings endpoint, so we parallelise lightly with a bounded
// worker pool sized to min(len(texts), 8) to avoid overloading a local
// daemon that may be CPU-bound.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))
	workers := len(texts)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}

	type job struct {
		i    int
		text string
	}
	jobs := make(chan job)
	var wg sync.WaitGroup

	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				vec, err := e.embedOne(ctx, j.text)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				results[j.i] = vec
			}
		}()
	}
	for i, t := range texts {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		case jobs <- job{i: i, text: t}:
		}
	}
	close(jobs)
	wg.Wait()
	close(errCh)
	if err, ok := <-errCh; ok {
		return nil, err
	}
	return results, nil
}

func (e *OllamaEmbedder) embedOne(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaEmbeddingRequest{
		Model:     e.model,
		Prompt:    text,
		KeepAlive: e.keepAlive,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling ollama embedding request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", e.endpoint+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating ollama embedding request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama embedding request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embedding returned status %d: %s", resp.StatusCode, string(respBody))
	}
	var oResp ollamaEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
		return nil, fmt.Errorf("decoding ollama embedding response: %w", err)
	}
	if len(oResp.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embedding: empty vector for input %q", text)
	}
	return oResp.Embedding, nil
}

// --- Noop Embedder ---

// NoopEmbedder returns a fixed-dimension zero vector for every input.
// Useful for unit tests, local dev without an embedding API key, and
// disabling the embeddings feature with a single config flip.
type NoopEmbedder struct {
	dimensions int
	model      string
}

// NewNoopEmbedder builds a NoopEmbedder of the given vector width.
// dim must be > 0; a zero value is replaced with 1536 to match the
// default pgvector column width.
func NewNoopEmbedder(dim int) *NoopEmbedder {
	if dim <= 0 {
		dim = 1536
	}
	return &NoopEmbedder{dimensions: dim, model: "noop"}
}

func (e *NoopEmbedder) Dimensions() int  { return e.dimensions }
func (e *NoopEmbedder) ModelName() string { return e.model }

// Embed returns len(texts) zero vectors. Errors are never returned.
func (e *NoopEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, e.dimensions)
	}
	return out, nil
}

// EmbedBatch is sugar for Embed.
func (e *NoopEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}

// EmbedRequest always returns zero vectors regardless of the override
// fields. The Noop embedder is intentionally opaque to its inputs.
func (e *NoopEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	vecs, err := e.Embed(ctx, req.Input)
	if err != nil {
		return nil, err
	}
	return &EmbedResponse{Vectors: vecs, Model: e.model}, nil
}

// HealthCheck is a no-op for the in-memory NoopEmbedder.
func (e *NoopEmbedder) HealthCheck(ctx context.Context) error { return nil }

// --- LocalStub Embedder (deterministic, hash-based) ---

// LocalStubConfig configures the hash-based deterministic embedder.
type LocalStubConfig struct {
	// Dimensions of the generated vectors. 0 → 384 (matches the
	// all-MiniLM-L6-v2 / BGE-small dimensionality that is most
	// common for local-embedding models).
	Dimensions int
	// Model is the advertised model name. Empty → "local-stub".
	Model string
}

// LocalStubEmbedder is a hash-based deterministic embedder suitable
// for offline dev, CI, and unit tests where invoking a real LLM is
// either impossible (no network) or undesirable (slow / non-deterministic).
//
// Every input string is mapped to a fixed-dimension float32 vector
// derived from a SHA-256 seed. The mapping is:
//   1. Compute SHA-256(input) → 32 bytes of seed material.
//   2. Stretch the seed into the requested number of dimensions via
//      a deterministic counter-mode PRNG (no math/rand — output is
//      stable across processes and Go versions).
//   3. Project each generated value into [-1, 1] and apply L2
//      normalisation so cosine similarity behaves intuitively.
//
// The vectors are NOT semantically meaningful (they are a hash
// projection, not a learned model), but they satisfy the contract
// every caller cares about: identical inputs → identical vectors,
// different inputs → almost-always-different vectors, and the output
// shape matches the real model. This is exactly what tests need to
// validate wiring and persistence without paying for an API call.
type LocalStubEmbedder struct {
	dimensions int
	model      string
}

// NewLocalStubEmbedder builds a deterministic stub embedder.
func NewLocalStubEmbedder(cfg LocalStubConfig) *LocalStubEmbedder {
	dim := cfg.Dimensions
	if dim <= 0 {
		dim = 384
	}
	model := cfg.Model
	if model == "" {
		model = "local-stub"
	}
	return &LocalStubEmbedder{dimensions: dim, model: model}
}

func (e *LocalStubEmbedder) Dimensions() int  { return e.dimensions }
func (e *LocalStubEmbedder) ModelName() string { return e.model }

// HealthCheck is a no-op for the in-process stub.
func (e *LocalStubEmbedder) HealthCheck(ctx context.Context) error { return nil }

// Embed returns a deterministic vector per input. Two calls with the
// same input always produce byte-identical vectors; different inputs
// almost always produce distinct vectors (the only collisions come
// from the SHA-256 → float32 stretch, which has 2^-128 collision
// probability).
func (e *LocalStubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = deterministicVector(t, e.dimensions)
	}
	return out, nil
}

// EmbedBatch is sugar for Embed.
func (e *LocalStubEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.Embed(ctx, texts)
}

// EmbedRequest honours per-call Dimensions overrides by computing the
// vector at the new dimensionality; Model is accepted but ignored
// (the stub has only one logical model).
func (e *LocalStubEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	dim := e.dimensions
	if req.Dimensions > 0 {
		dim = req.Dimensions
	}
	out := make([][]float32, len(req.Input))
	for i, t := range req.Input {
		out[i] = deterministicVector(t, dim)
	}
	return &EmbedResponse{Vectors: out, Model: e.model}, nil
}

// deterministicVector stretches a SHA-256 seed into a normalised
// vector of the requested dimension. The algorithm is:
//
//   - 32 bytes of seed material from SHA-256(text).
//   - For each dimension d in [0, dims): hash the seed || d-le-u32
//     to produce 4 bytes; reinterpret as a uint32; map to [-1, 1]
//     via  (uint32/2^32)*2 - 1.
//   - Normalise the whole vector to unit L2 length so cosine
//     similarity is meaningful.
//
// Determinism is preserved across processes, Go versions and CPUs
// because no math/rand is involved.
func deterministicVector(text string, dims int) []float32 {
	if dims <= 0 {
		return nil
	}
	v := make([]float32, dims)
	var nonce [4]byte
	for d := 0; d < dims; d++ {
		// Encode d as big-endian uint32 into nonce.
		nonce[0] = byte(d >> 24)
		nonce[1] = byte(d >> 16)
		nonce[2] = byte(d >> 8)
		nonce[3] = byte(d)
		h := sha256.Sum256(append([]byte(text), nonce[:]...))
		// Use the first 4 bytes of the digest as a uint32 in [0, 2^32).
		u := binary.BigEndian.Uint32(h[:4])
		// Map [0, 2^32) → [-1, 1).
		v[d] = float32(u)/float32(1<<31) - 1.0
	}
	// L2 normalise so cosine similarity between any two vectors is
	// bounded in [-1, 1] like a real model.
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	norm := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= norm
	}
	return v
}

// --- Cached Embedder (see embeddings_cache.go) ---

// --- Factory ---

// NewEmbedder dispatches to the right embedder implementation by name.
// Mirrors the chat-completion provider preset taxonomy so that picking
// "deepseek" for chat and "deepseek" for embeddings is symmetric.
func NewEmbedder(provider, apiKey, endpoint, model string, dimensions int) (Embedder, error) {
	switch provider {
	case "openai", "deepseek", "glm", "zhipu", "kimi", "moonshot", "qwen", "dashscope", "tongyi":
		if apiKey == "" {
			return nil, fmt.Errorf("embedder %q requires api_key", provider)
		}
		if endpoint == "" {
			endpoint = embedderEndpointFor(provider)
		}
		return NewOpenAIEmbedder(OpenAIEmbedderConfig{
			APIKey:     apiKey,
			Endpoint:   endpoint,
			Model:      embedderModelFor(provider, model),
			Dimensions: dimensions,
		}), nil
	case "ollama":
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		return NewOllamaEmbedder(OllamaEmbedderConfig{
			Endpoint:   endpoint,
			Model:      embedderModelFor(provider, model),
			Dimensions: dimensions,
		}), nil
	case "noop", "none", "disabled":
		return NewNoopEmbedder(dimensions), nil
	case "local_stub", "local-stub", "stub":
		return NewLocalStubEmbedder(LocalStubConfig{Dimensions: dimensions, Model: model}), nil
	default:
		return nil, fmt.Errorf("unknown embedder provider %q — use openai, deepseek, glm, kimi, qwen, ollama, noop, or local_stub", provider)
	}
}

// embedderEndpointFor maps provider preset → base URL. Mirrors the
// chat-completion preset list in factory.go so users can mix-and-match
// chat provider (e.g. Claude) and embedder provider (e.g. OpenAI) freely.
func embedderEndpointFor(provider string) string {
	switch provider {
	case "deepseek":
		return "https://api.deepseek.com"
	case "glm", "zhipu":
		return "https://open.bigmodel.cn/api/paas/v4"
	case "kimi", "moonshot":
		return "https://api.moonshot.cn"
	case "qwen", "dashscope", "tongyi":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	default:
		return "https://api.openai.com/v1"
	}
}

// embedderModelFor returns a sensible default model for each preset.
// Models marked (embed) are the vendor's own embeddings endpoint; the
// OpenAI-compatible preset defaults to text-embedding-3-small since
// most vendors offer an OpenAI-style alias.
func embedderModelFor(provider, model string) string {
	if model != "" {
		return model
	}
	switch provider {
	case "deepseek":
		return "deepseek-embedding" // (current public name as of 2026-06)
	case "glm", "zhipu":
		return "embedding-2"
	case "kimi", "moonshot":
		return "moonshot-v1-128k-embedding"
	case "qwen", "dashscope", "tongyi":
		return "text-embedding-v3"
	case "ollama":
		return "nomic-embed-text"
	default:
		return "text-embedding-3-small"
	}
}

// --- Dimension reconciliation ---

// ResolveEmbeddingDimensions picks the embedder's declared dimension,
// falling back to the schema's pgvector column width (1536) when the
// embedder reports 0. Keeps the schema consistent across deployments.
func ResolveEmbeddingDimensions(e Embedder) int {
	if e == nil {
		return 0
	}
	if d := e.Dimensions(); d > 0 {
		return d
	}
	return 1536
}

// VectorToBytes serializes a float32 vector to a binary representation
// suitable for use as a cache key suffix. Currently used only for
// debugging; not the on-wire pgvector format (that uses the textual
// "[x,y,z]" form produced by postgres.go:embeddingArg).
func VectorToBytes(v []float32) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, len(v)*4))
	_ = binary.Write(buf, binary.LittleEndian, v)
	return buf.Bytes()
}
