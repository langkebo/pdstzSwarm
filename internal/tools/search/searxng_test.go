package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSEARXNGProvider_NameAndConfigured(t *testing.T) {
	noEndpoint := NewSEARXNGProvider(SEARXNGConfig{})
	if noEndpoint.Name() != "searxng" {
		t.Errorf("Name() = %q, want searxng", noEndpoint.Name())
	}
	if noEndpoint.IsConfigured() {
		t.Error("IsConfigured() = true with empty endpoint, want false")
	}
	withEndpoint := NewSEARXNGProvider(SEARXNGConfig{Endpoint: "https://searx.be"})
	if !withEndpoint.IsConfigured() {
		t.Error("IsConfigured() = false with endpoint, want true")
	}
}

func TestSEARXNGProvider_RejectsEmptyQuery(t *testing.T) {
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: "https://x"})
	_, err := p.Search(context.Background(), Query{Text: ""})
	if err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("expected empty-query error, got %v", err)
	}
}

func TestSEARXNGProvider_RejectsUnconfigured(t *testing.T) {
	p := NewSEARXNGProvider(SEARXNGConfig{})
	_, err := p.Search(context.Background(), Query{Text: "cve"})
	if err == nil || !strings.Contains(err.Error(), "endpoint not set") {
		t.Fatalf("expected endpoint-not-set error, got %v", err)
	}
}

func TestSEARXNGProvider_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid token"}`))
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL, APIKey: "bad"})
	_, err := p.Search(context.Background(), Query{Text: "cve"})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected auth-failure error, got %v", err)
	}
}

func TestSEARXNGProvider_BearerKeySent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL, APIKey: "secret"})
	if _, err := p.Search(context.Background(), Query{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
	}
}

func TestSEARXNGProvider_QueryStringBuilt(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	if _, err := p.Search(context.Background(), Query{Text: "cve 2024", Max: 5}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"q=cve+2024", "format=json", "limit=5"} {
		if !strings.Contains(gotURL, want) {
			t.Errorf("query string = %q, missing %q", gotURL, want)
		}
	}
}

func TestSEARXNGProvider_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "A", "url": "https://a.example/", "content": "A snippet", "engine": "google"},
				{"title": "B", "url": "https://b.example/", "content": "B snippet", "engine": "bing"},
			},
		})
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "cve-2024", Max: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "A" || results[0].URL != "https://a.example/" || results[0].Source != "searxng" {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1].Snippet != "B snippet" {
		t.Errorf("results[1].Snippet = %q", results[1].Snippet)
	}
}

func TestSEARXNGProvider_RejectsRelativeURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "rel", "url": "/relative/path", "content": ""},
				{"title": "ok", "url": "https://ok", "content": ""},
			},
		})
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].URL != "https://ok" {
		t.Errorf("got %+v, want exactly one relative-rejected result", results)
	}
}

func TestSEARXNGProvider_RespectsMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "A", "url": "https://a"},
				{"title": "B", "url": "https://b"},
				{"title": "C", "url": "https://c"},
			},
		})
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q", Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("got %d, want 2 (caller Max)", len(results))
	}
}

func TestSEARXNGProvider_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate"))
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "429") && !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected 429 error, got %v", err)
	}
}

func TestSEARXNGProvider_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, Query{Text: "q"}); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}
