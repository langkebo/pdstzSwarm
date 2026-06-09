package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPerplexityProvider_NameAndConfigured(t *testing.T) {
	noKey := NewPerplexityProvider(PerplexityConfig{})
	if noKey.Name() != "perplexity" {
		t.Errorf("Name() = %q, want perplexity", noKey.Name())
	}
	if noKey.IsConfigured() {
		t.Error("IsConfigured() = true with empty key, want false")
	}
	withKey := NewPerplexityProvider(PerplexityConfig{APIKey: "pplx-x"})
	if !withKey.IsConfigured() {
		t.Error("IsConfigured() = false with key, want true")
	}
}

func TestPerplexityProvider_Defaults(t *testing.T) {
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k"})
	if p.cfg.Endpoint != "https://api.perplexity.ai/chat/completions" {
		t.Errorf("Endpoint = %q", p.cfg.Endpoint)
	}
	if p.cfg.Model != "sonar" {
		t.Errorf("Model = %q, want sonar", p.cfg.Model)
	}
	if p.cfg.MaxPerQuery != 10 {
		t.Errorf("MaxPerQuery = %d, want 10", p.cfg.MaxPerQuery)
	}
}

func TestPerplexityProvider_RejectsEmptyQuery(t *testing.T) {
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k"})
	_, err := p.Search(context.Background(), Query{Text: ""})
	if err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("expected empty-query error, got %v", err)
	}
}

func TestPerplexityProvider_RejectsUnconfigured(t *testing.T) {
	p := NewPerplexityProvider(PerplexityConfig{})
	_, err := p.Search(context.Background(), Query{Text: "cve"})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestPerplexityProvider_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid"}`))
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "bad", HTTPClient: srv.Client(), Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected auth failure, got %v", err)
	}
}

func TestPerplexityProvider_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
}

func TestPerplexityProvider_BearerSent(t *testing.T) {
	var gotAuth, gotContentType, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[],"search_results":[]}`))
	}))
	defer srv.Close()
	// The test server is mounted at srv.URL/; we want to assert the
	// provider appends /chat/completions only when the endpoint
	// doesn't already include it. We set the endpoint explicitly so
	// we can verify the path the provider calls.
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "pplx-abc", HTTPClient: srv.Client(), Endpoint: srv.URL + "/chat/completions"})
	if _, err := p.Search(context.Background(), Query{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer pplx-abc" {
		t.Errorf("Authorization = %q, want Bearer pplx-abc", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("Path = %q, want /chat/completions", gotPath)
	}
}

func TestPerplexityProvider_DefaultEndpointIncludesPath(t *testing.T) {
	// When the user supplies Endpoint that doesn't include
	// /chat/completions, the provider should still target the right
	// path. We don't auto-append (intentional: users sometimes
	// proxy the API and may mount it at a different sub-path), so
	// this test documents the behavior rather than asserting an
	// auto-append.
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", Endpoint: "https://proxy.example/perplexity"})
	if p.cfg.Endpoint != "https://proxy.example/perplexity" {
		t.Errorf("Endpoint was unexpectedly rewritten: %q", p.cfg.Endpoint)
	}
}

func TestPerplexityProvider_RequestBody(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[],"search_results":[]}`))
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", Model: "sonar-pro", HTTPClient: srv.Client(), Endpoint: srv.URL})
	if _, err := p.Search(context.Background(), Query{Text: "wordpress exploit"}); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "sonar-pro" {
		t.Errorf("model = %v, want sonar-pro", got["model"])
	}
	if got["search_mode"] != "sonar" {
		t.Errorf("search_mode = %v, want sonar", got["search_mode"])
	}
	if got["return_citations"] != true {
		t.Errorf("return_citations = %v, want true", got["return_citations"])
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages = %v, want 2 entries", got["messages"])
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages[0].role = %v, want system", msgs[0].(map[string]any)["role"])
	}
	if msgs[1].(map[string]any)["content"] != "wordpress exploit" {
		t.Errorf("messages[1].content = %v", msgs[1].(map[string]any)["content"])
	}
}

func TestPerplexityProvider_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "WordPress 5.x has CVE-2024-XXXX."}},
			},
			"search_results": []map[string]any{
				{"title": "CVE-2024-XXXX", "url": "https://nvd.nist.gov/vuln/detail/CVE-2024-XXXX", "date": "2024-12-01"},
				{"title": "PoC", "url": "https://github.com/x/poc", "date": "2024-12-15"},
			},
		})
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "wordpress 5 exploit"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].URL != "https://nvd.nist.gov/vuln/detail/CVE-2024-XXXX" {
		t.Errorf("results[0].URL = %q", results[0].URL)
	}
	if results[0].Source != "perplexity" {
		t.Errorf("results[0].Source = %q", results[0].Source)
	}
	if results[0].Snippet != "2024-12-01" {
		t.Errorf("results[0].Snippet = %q, want date", results[0].Snippet)
	}
}

func TestPerplexityProvider_FallbackToAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Yes, CVE-2024-XXXX is exploitable."}},
			},
			"search_results": []map[string]any{},
		})
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "is CVE-2024-XXXX exploitable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (synthesized from answer)", len(results))
	}
	if results[0].URL != "perplexity://answer" {
		t.Errorf("URL = %q, want perplexity://answer", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "exploitable") {
		t.Errorf("Snippet = %q, want to contain the answer text", results[0].Snippet)
	}
}

func TestPerplexityProvider_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"search_results":[]}`))
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (empty payload)", len(results))
	}
}

func TestPerplexityProvider_RespectsMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "x"}}},
			"search_results": []map[string]any{
				{"title": "A", "url": "https://a"},
				{"title": "B", "url": "https://b"},
				{"title": "C", "url": "https://c"},
			},
		})
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q", Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("got %d, want 2 (caller Max)", len(results))
	}
}

func TestPerplexityProvider_TruncatesLongSnippet(t *testing.T) {
	long := strings.Repeat("a", 2000)
	got := truncateForSnippet(long, 800)
	// 800 ASCII chars (1 byte each) + the 3-byte UTF-8 ellipsis
	// character = 803 bytes.
	if len(got) != 803 {
		t.Errorf("len = %d, want 803", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("missing truncation marker")
	}
	// The prefix must be exactly 800 chars of `a`.
	if !strings.HasPrefix(got, strings.Repeat("a", 800)) {
		t.Error("prefix mismatch")
	}
}

func TestTruncateForSnippet(t *testing.T) {
	cases := map[string]struct {
		in, want string
	}{
		"short":         {"hi", "hi"},
		"empty":         {"", ""},
		"exact_boundary": {strings.Repeat("x", 800), strings.Repeat("x", 800)},
		"over":          {strings.Repeat("x", 801), strings.Repeat("x", 800) + "…"},
	}
	for name, c := range cases {
		if got := truncateForSnippet(c.in, 800); got != c.want {
			t.Errorf("%s: got len %d, want %d", name, len(got), len(c.want))
		}
	}
}
