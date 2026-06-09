package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// BenchmarkDDGParse_RealisticPage measures parseDDGHTML on a
// representative DDG HTML page (~10 results). Used to back the
// performance-comparison claim in the optimization report.
func BenchmarkDDGParse_RealisticPage(b *testing.B) {
	page := buildDDGFixture(10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDDGHTML(page, "ddg", 10)
	}
}

func BenchmarkDDGParse_LargePage(b *testing.B) {
	page := buildDDGFixture(50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDDGHTML(page, "ddg", 50)
	}
}

// BenchmarkDDGProvider_SearchLocalServer measures end-to-end provider
// cost when talking to an httptest server. The HTTP overhead is the
// dominant cost; this number should be dominated by the transport, not
// the parser.
func BenchmarkDDGProvider_SearchLocalServer(b *testing.B) {
	page := buildDDGFixture(10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()
	p := NewDDGProvider(DDGConfig{Endpoint: srv.URL})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.Search(ctx, Query{Text: "cve-2024", Max: 10})
	}
}

// buildDDGFixture assembles a synthetic DDG HTML page with n results.
// Title / URL / snippet text is unique per result so the parser does
// real work.
func buildDDGFixture(n int) string {
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for i := 0; i < n; i++ {
		b.WriteString(`<div class="result">`)
		// Use the /l/?uddg= wrapper to exercise the redirect decoder.
		b.WriteString(`<a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample`)
		b.WriteString(strings.Repeat("a", i+1))
		b.WriteString(`%2F&rut=`)
		b.WriteString(strings.Repeat("b", i+1))
		b.WriteString(`">Title `)
		b.WriteString(strings.Repeat("T", i+1))
		b.WriteString(`</a>`)
		b.WriteString(`<a class="result__snippet">Snippet `)
		b.WriteString(strings.Repeat("S", i+1))
		b.WriteString(`</a>`)
		b.WriteString(`</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}
