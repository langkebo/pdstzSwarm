package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSploitusProvider_NameAndConfigured(t *testing.T) {
	p := NewSploitusProvider(SploitusConfig{})
	if p.Name() != "sploitus" {
		t.Errorf("Name() = %q, want sploitus", p.Name())
	}
	if !p.IsConfigured() {
		t.Error("IsConfigured() = false, Sploitus is key-less so should be true")
	}
}

func TestSploitusProvider_RejectsEmptyQuery(t *testing.T) {
	p := NewSploitusProvider(SploitusConfig{HTTPClient: &http.Client{}})
	_, err := p.Search(context.Background(), Query{Text: ""})
	if err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("expected empty-query error, got %v", err)
	}
}

func TestSploitusProvider_Defaults(t *testing.T) {
	p := NewSploitusProvider(SploitusConfig{})
	if p.cfg.Endpoint != "https://sploitus.com/" {
		t.Errorf("Endpoint = %q, want https://sploitus.com/", p.cfg.Endpoint)
	}
	if p.cfg.MaxPerQuery != 20 {
		t.Errorf("MaxPerQuery = %d, want 20", p.cfg.MaxPerQuery)
	}
	if p.cfg.UserAgent == "" {
		t.Error("UserAgent empty; Sploitus rejects default Go UAs")
	}
}

func TestSploitusProvider_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Query().Get("query") != "log4j" {
			t.Errorf("query = %q, want log4j", r.URL.Query().Get("query"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent missing")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`
<div class="exploit-card">
  <a class="exploit-card__link" href="https://exploit-db.com/exploits/12345">Log4Shell RCE</a>
  <div class="exploit-card__description">A simple Log4j RCE PoC for CVE-2021-44228.</div>
</div>
<div class="exploit-card">
  <a class="exploit-card__link" href="https://github.com/x/log4j-poc">Log4j exploit script</a>
  <div class="exploit-card__description">Nuclei template + curl one-liner.</div>
</div>
`))
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "log4j", Max: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "Log4Shell RCE" {
		t.Errorf("results[0].Title = %q", results[0].Title)
	}
	if results[0].URL != "https://exploit-db.com/exploits/12345" {
		t.Errorf("results[0].URL = %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "Log4j RCE") {
		t.Errorf("results[0].Snippet = %q", results[0].Snippet)
	}
	if results[0].Source != "sploitus" {
		t.Errorf("results[0].Source = %q, want sploitus", results[0].Source)
	}
}

func TestSploitusProvider_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
}

func TestSploitusProvider_TooManyRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), Query{Text: "q"})
	if err == nil || !strings.Contains(err.Error(), "rate-limited") {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
}

func TestSploitusProvider_RespectsMax(t *testing.T) {
	body := strings.Repeat(`<a class="exploit-card__link" href="https://x">X</a>`, 5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q", Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("got %d, want 3 (caller Max)", len(results))
	}
}

func TestSploitusProvider_SkipsInternalAnchors(t *testing.T) {
	// /exploit/?id=… are Sploitus's own in-page anchors; parser
	// must skip them so we don't emit junk results pointing to
	// sploitus.com itself.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
<a class="exploit-card__link" href="/exploit/?id=internal">Internal</a>
<a class="exploit-card__link" href="https://external.example/poc">Real</a>
`))
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	results, err := p.Search(context.Background(), Query{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].URL != "https://external.example/poc" {
		t.Errorf("got %+v, want exactly one external result", results)
	}
}

func TestSploitusProvider_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := NewSploitusProvider(SploitusConfig{Endpoint: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, Query{Text: "q"}); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestParseSploitusHTML_MaxCap(t *testing.T) {
	body := strings.Repeat(`<a class="exploit-card__link" href="https://x">X</a>`, 10)
	results := parseSploitusHTML(body, "sploitus", 4)
	if len(results) != 4 {
		t.Errorf("got %d, want 4 (cap)", len(results))
	}
}

func TestParseSploitusHTML_SkipsMalformed(t *testing.T) {
	body := `
<a class="exploit-card__link">no href</a>
<a class="exploit-card__link" href="https://ok">OK</a>
`
	results := parseSploitusHTML(body, "sploitus", 10)
	if len(results) != 1 || results[0].URL != "https://ok" {
		t.Errorf("got %+v, want exactly one result with URL https://ok", results)
	}
}

func TestDecodeSploitusRedirect(t *testing.T) {
	cases := map[string]string{
		"https://example.com/path":        "https://example.com/path",
		"/link/?url=https%3A%2F%2Fa.b%2F": "https://a.b/",
		"/exploit/?id=123":                "",
		"http://x":                        "http://x",
		"":                                "",
	}
	for in, want := range cases {
		if got := decodeSploitusRedirect(in); got != want {
			t.Errorf("decodeSploitusRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}
