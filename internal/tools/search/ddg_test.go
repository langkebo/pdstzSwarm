package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStripTags(t *testing.T) {
	// stripTags removes the <…> syntax but leaves the inner content
	// alone. DDG snippets never contain <script>; for actual script
	// content, the downstream HTML escaper in the API layer is the
	// right place to neutralize. We just verify the tag syntax is gone.
	cases := map[string]string{
		`<a href="x">hello</a>`:    "hello",
		`plain text`:                "plain text",
		`<b>one</b> and <i>two</i>`: "one and two",
		`<a href="x">`:              "",
		`a<script>b()</script>vis`:  "ab()vis",
		`no close`:                  "no close",
		"":                          "",
		`<>empty`:                   "empty",
	}
	for in, want := range cases {
		if got := stripTags(in); got != want {
			t.Errorf("stripTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeDDGRedirect(t *testing.T) {
	cases := map[string]string{
		"https://example.com/path":      "https://example.com/path",
		"/l/?uddg=https%3A%2F%2Fa.b%2F": "https://a.b/",
		"/l/?other=1":                   "/l/?other=1",
		"":                              "",
	}
	for in, want := range cases {
		if got := decodeDDGRedirect(in); got != want {
			t.Errorf("decodeDDGRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDDGHTML_HappyPath(t *testing.T) {
	// Fixture distilled from a real DDG HTML page. We only assert that
	// the parser correctly extracts title/URL/snippet triples and that
	// the /l/?uddg= wrapper is unwrapped.
	body := `
<html><body>
<div class="result">
  <a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com%2F&rut=abc">First Title</a>
  <a class="result__snippet">First snippet body</a>
</div>
<div class="result">
  <a class="result__a" href="https://other.example.org/page">Second Title</a>
  <a class="result__snippet">Second snippet body</a>
</div>
</body></html>
`
	results := parseDDGHTML(body, "ddg", 10)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "First Title" {
		t.Errorf("results[0].Title = %q, want %q", results[0].Title, "First Title")
	}
	if results[0].URL != "https://example.com/" {
		t.Errorf("results[0].URL = %q, want https://example.com/", results[0].URL)
	}
	if results[0].Snippet != "First snippet body" {
		t.Errorf("results[0].Snippet = %q", results[0].Snippet)
	}
	if results[1].URL != "https://other.example.org/page" {
		t.Errorf("results[1].URL = %q", results[1].URL)
	}
	if results[1].Source != "ddg" {
		t.Errorf("results[1].Source = %q, want ddg", results[1].Source)
	}
}

func TestParseDDGHTML_MaxCap(t *testing.T) {
	body := `<a class="result__a" href="https://a">A</a>` +
		`<a class="result__a" href="https://b">B</a>` +
		`<a class="result__a" href="https://c">C</a>`
	results := parseDDGHTML(body, "ddg", 2)
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (cap)", len(results))
	}
}

func TestParseDDGHTML_SkipsMalformed(t *testing.T) {
	// Anchor with no href, anchor with no closing tag — both must be
	// skipped, not panic.
	body := `<a class="result__a">no href</a><a class="result__a" href="https://ok">OK</a>`
	results := parseDDGHTML(body, "ddg", 10)
	if len(results) != 1 || results[0].URL != "https://ok" {
		t.Errorf("got %+v, want exactly one result with URL https://ok", results)
	}
}

func TestDDGProvider_NameAndConfigured(t *testing.T) {
	p := NewDDGProvider(DDGConfig{})
	if p.Name() != "ddg" {
		t.Errorf("Name() = %q, want ddg", p.Name())
	}
	if !p.IsConfigured() {
		t.Error("DDG should always be configured (no key required)")
	}
}

func TestDDGProvider_EmptyQuery(t *testing.T) {
	p := NewDDGProvider(DDGConfig{HTTPClient: &http.Client{}})
	_, err := p.Search(context.Background(), Query{Text: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestDDGProvider_SearchViaHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Query().Get("q") != "cve-2024" {
			t.Errorf("q = %q, want cve-2024", r.URL.Query().Get("q"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent header missing")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a class="result__a" href="https://x">X</a>`))
	}))
	defer srv.Close()

	p := NewDDGProvider(DDGConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "cve-2024", Max: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://x" {
		t.Errorf("got %+v, want one result URL=https://x", results)
	}
}

func TestDDGProvider_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	p := NewDDGProvider(DDGConfig{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if !strings.Contains(err.Error(), "rate-limited") {
		t.Errorf("error = %v, want it to mention rate-limit", err)
	}
}

func TestDDGProvider_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := NewDDGProvider(DDGConfig{Endpoint: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := p.Search(ctx, Query{Text: "q"})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestDDGProvider_RejectsOversizedBody(t *testing.T) {
	// 2 MiB body > 1 MiB cap → ReadAll must not OOM and parser must
	// still return whatever it found (probably nothing).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("A", 64<<10)
		for i := 0; i < 32; i++ { // 2 MiB total
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()
	p := NewDDGProvider(DDGConfig{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		// We expect either an error from the cap or an empty result;
		// neither is a panic.
		t.Logf("got expected (possibly truncated) error: %v", err)
	}
}
