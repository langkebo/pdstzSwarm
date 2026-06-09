package webfs

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFS_WithPlaceholder verifies the FS() probe: the placeholder
// index.html we ship in fresh checkouts must be detected as a
// valid bundle, not as ErrNoBundle.
func TestFS_WithPlaceholder(t *testing.T) {
	root, err := FS()
	if err != nil {
		t.Fatalf("FS() returned error with placeholder: %v", err)
	}
	b, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	if !strings.Contains(string(b), "控制台") {
		t.Errorf("placeholder index.html missing '控制台' marker")
	}
}

// TestHandler_ServesIndex verifies the http.Handler serves
// index.html for "/", sets the right Content-Type, and falls
// back to index.html for unknown paths (SPA routing).
func TestHandler_ServesIndex(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler() returned error: %v", err)
	}

	// 1. Root path → index.html
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("root: expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("root: expected html content-type, got %q", got)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "控制台") {
		t.Errorf("root: body missing 控制台 marker")
	}
	indexBody := string(body) // save for comparison below

	// 2. Known route with .html extension: /settings/mcp
	//    should serve settings/mcp.html, not index.html.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/settings/mcp", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("settings/mcp: expected 200, got %d", rr.Code)
	}
	body, _ = io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "控制台") {
		t.Errorf("settings/mcp: body should contain 控制台")
	}
	// The settings/mcp page should NOT be identical to index.html
	// (it has its own RSC payload).
	if string(body) == indexBody {
		t.Errorf("settings/mcp: should serve its own page, not index.html")
	}

	// 3. Truly unknown path (SPA fallback): /agents/nonexistent
	//    should serve index.html.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/agents/nonexistent", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("spa fallback: expected 200, got %d", rr.Code)
	}
	body, _ = io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "控制台") {
		t.Errorf("spa fallback: body should be index.html")
	}
}
