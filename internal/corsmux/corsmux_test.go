package corsmux

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/gofiber/fiber/v2"
)

func newApp(t *testing.T, cfg config.CORSConfig) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(New(cfg))
	app.Get("/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })
	return app
}

func do(t *testing.T, app *fiber.App, origin string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEmptyOrigins_AllowsAnyOrigin(t *testing.T) {
	// Empty config = legacy "*" behaviour.
	app := newApp(t, config.CORSConfig{})
	resp := do(t, app, "https://evil.example")
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected * header, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestExplicitOrigins_AllowsListed(t *testing.T) {
	app := newApp(t, config.CORSConfig{
		Origins: []string{"https://app.example.com", "https://admin.example.com"},
	})
	resp := do(t, app, "https://app.example.com")
	got := resp.Header.Get("Access-Control-Allow-Origin")
	if got != "https://app.example.com" {
		t.Errorf("got %q, want echo of origin", got)
	}
}

func TestExplicitOrigins_RejectsUnlisted(t *testing.T) {
	app := newApp(t, config.CORSConfig{
		Origins: []string{"https://app.example.com"},
	})
	resp := do(t, app, "https://evil.example")
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected empty ACAO header for unlisted origin, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestExplicitOrigins_CaseSensitive(t *testing.T) {
	app := newApp(t, config.CORSConfig{
		Origins: []string{"https://App.Example.com"},
	})
	resp := do(t, app, "https://app.example.com")
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("origin match should be case-sensitive; got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestNormaliseOrigins_TrimsWhitespace(t *testing.T) {
	got := normaliseOrigins([]string{"  https://a.com  ", "", "  ", "https://b.com"})
	if len(got) != 2 || got[0] != "https://a.com" || got[1] != "https://b.com" {
		t.Errorf("normalise = %v, want [https://a.com https://b.com]", got)
	}
}

func TestCustomHeadersAndMethods(t *testing.T) {
	app := newApp(t, config.CORSConfig{
		Origins:      []string{"https://app.example.com"},
		AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"X-Custom"},
	})
	// Preflight: OPTIONS + Origin + Access-Control-Request-Method.
	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("missing Allow-Methods header")
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got == "" {
		t.Errorf("missing Allow-Headers header")
	}
}
