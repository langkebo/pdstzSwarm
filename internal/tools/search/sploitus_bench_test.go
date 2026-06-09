package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkSploitusParse_RealisticPage measures parseSploitusHTML
// on a representative Sploitus HTML page (~10 cards). The recon
// agent calls this in tight loops, so the per-call cost matters
// for the per-finding latency budget.
func BenchmarkSploitusParse_RealisticPage(b *testing.B) {
	page := buildSploitusFixture(10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSploitusHTML(page, "sploitus", 10)
	}
}

func BenchmarkSploitusParse_LargePage(b *testing.B) {
	page := buildSploitusFixture(50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSploitusHTML(page, "sploitus", 50)
	}
}

// BenchmarkSploitusProvider_SearchLocalServer measures end-to-end
// cost against a local httptest server. The HTTP transport
// dominates; the parser is well under a microsecond per page.
func BenchmarkSploitusProvider_SearchLocalServer(b *testing.B) {
	page := buildSploitusFixture(10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.Search(ctx, Query{Text: "cve-2024", Max: 10})
	}
}

// BenchmarkSEARXNGProvider_SearchLocalServer measures SEARXNG's
// JSON-decode cost on the same scenario. SEARXNG is a thin client
// over a JSON API, so it should be faster than the HTML scrapers.
func BenchmarkSEARXNGProvider_SearchLocalServer(b *testing.B) {
	payload := map[string]any{
		"results": make([]map[string]any, 10),
	}
	for i := 0; i < 10; i++ {
		payload["results"].([]map[string]any)[i] = map[string]any{
			"title":   "Title " + strings.Repeat("T", i+1),
			"url":     "https://example.com/" + strings.Repeat("a", i+1),
			"content": "Snippet " + strings.Repeat("S", i+1),
			"engine":  "google",
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()
	p := NewSEARXNGProvider(SEARXNGConfig{Endpoint: srv.URL})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.Search(ctx, Query{Text: "cve-2024", Max: 10})
	}
}

// BenchmarkPerplexityProvider_SearchLocalServer measures Perplexity
// JSON-decode cost. The provider parses both `choices[]` and
// `search_results[]` so it's slightly heavier than SEARXNG.
func BenchmarkPerplexityProvider_SearchLocalServer(b *testing.B) {
	payload := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": "A summarized answer about CVE-2024."}},
		},
		"search_results": make([]map[string]any, 10),
	}
	for i := 0; i < 10; i++ {
		payload["search_results"].([]map[string]any)[i] = map[string]any{
			"title": "Result " + strings.Repeat("R", i+1),
			"url":   "https://example.com/" + strings.Repeat("u", i+1),
			"date":  "2024-12-15",
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()
	p := NewPerplexityProvider(PerplexityConfig{APIKey: "k", HTTPClient: srv.Client(), Endpoint: srv.URL})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.Search(ctx, Query{Text: "cve-2024", Max: 10})
	}
}

// buildSploitusFixture assembles a synthetic Sploitus HTML page
// with n exploit cards. Title / URL / description text is unique
// per card so the parser does real work.
func buildSploitusFixture(n int) string {
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for i := 0; i < n; i++ {
		b.WriteString(`<div class="exploit-card">`)
		b.WriteString(`<a class="exploit-card__link" href="https://example.com/exploit/`)
		b.WriteString(strings.Repeat("a", i+1))
		b.WriteString(`">Title `)
		b.WriteString(strings.Repeat("T", i+1))
		b.WriteString(`</a>`)
		b.WriteString(`<div class="exploit-card__description">Description `)
		b.WriteString(strings.Repeat("D", i+1))
		b.WriteString(`</div>`)
		b.WriteString(`</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}
