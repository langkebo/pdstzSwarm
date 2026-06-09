package search

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTavilyProvider_NameAndConfigured(t *testing.T) {
	noKey := NewTavilyProvider(TavilyConfig{})
	if noKey.Name() != "tavily" {
		t.Errorf("Name() = %q, want tavily", noKey.Name())
	}
	if noKey.IsConfigured() {
		t.Error("IsConfigured() = true with empty key, want false")
	}
	withKey := NewTavilyProvider(TavilyConfig{APIKey: "tvly-x"})
	if !withKey.IsConfigured() {
		t.Error("IsConfigured() = false with key, want true")
	}
}

func TestTavilyProvider_RejectsEmptyQuery(t *testing.T) {
	p := NewTavilyProvider(TavilyConfig{APIKey: "tvly-x"})
	_, err := p.Search(context.Background(), Query{Text: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestTavilyProvider_RejectsUnconfigured(t *testing.T) {
	p := NewTavilyProvider(TavilyConfig{})
	_, err := p.Search(context.Background(), Query{Text: "cve"})
	if err == nil {
		t.Fatal("expected error when unconfigured")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error = %v, want api_key mention", err)
	}
}

func TestTavilyProvider_AuthFailure(t *testing.T) {
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid key"}`))
	})
	rec := roundTripRecorder{handler: srv}
	p := NewTavilyProvider(TavilyConfig{APIKey: "bad", HTTPClient: &http.Client{Transport: rec}})
	_, err := p.Search(context.Background(), Query{Text: "cve"})
	if err == nil {
		t.Fatal("expected auth-failure error")
	}
	if !strings.Contains(err.Error(), "auth failed") {
		t.Errorf("error = %v, want auth failed", err)
	}
}

func TestTavilyProvider_HappyPath(t *testing.T) {
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["api_key"] != "tvly-x" {
			t.Errorf("api_key = %v, want tvly-x", body["api_key"])
		}
		if body["query"] != "cve-2024" {
			t.Errorf("query = %v, want cve-2024", body["query"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "A", "url": "https://a.example/", "content": "A snippet"},
				{"title": "B", "url": "https://b.example/", "content": "B snippet"},
			},
		})
	})
	rec := roundTripRecorder{handler: srv}
	p := NewTavilyProvider(TavilyConfig{APIKey: "tvly-x", HTTPClient: &http.Client{Transport: rec}})
	results, err := p.Search(context.Background(), Query{Text: "cve-2024", Max: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].URL != "https://a.example/" || results[0].Source != "tavily" {
		t.Errorf("results[0] = %+v", results[0])
	}
}

func TestTavilyProvider_RespectsMax(t *testing.T) {
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "A", "url": "https://a", "content": ""},
				{"title": "B", "url": "https://b", "content": ""},
				{"title": "C", "url": "https://c", "content": ""},
			},
		})
	})
	rec := roundTripRecorder{handler: srv}
	p := NewTavilyProvider(TavilyConfig{APIKey: "tvly-x", HTTPClient: &http.Client{Transport: rec}})
	results, err := p.Search(context.Background(), Query{Text: "q", Max: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (caller Max)", len(results))
	}
}

// roundTripRecorder adapts an http.Handler into a RoundTripper so we
// can drive TavilyProvider without spinning up an httptest.Server.
type roundTripRecorder struct {
	handler http.Handler
}

func (r roundTripRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := &recordingResponseWriter{header: http.Header{}}
	r.handler.ServeHTTP(rec, req)
	return rec.toResponse(req), nil
}

type recordingResponseWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func (w *recordingResponseWriter) Header() http.Header { return w.header }

func (w *recordingResponseWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *recordingResponseWriter) WriteHeader(s int) { w.status = s }

func (w *recordingResponseWriter) toResponse(req *http.Request) *http.Response {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{
		StatusCode:    w.status,
		Status:        http.StatusText(w.status),
		Header:        w.header,
		Body:          readCloserFromString(w.body.String()),
		ContentLength: int64(w.body.Len()),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Request:       req,
	}
}
