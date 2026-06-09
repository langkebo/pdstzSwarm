// Package webfs embeds the Next.js static export produced by
// `web/out` so the swarm binary can serve the dashboard on the
// same port as the API.
//
// At build time the Dockerfile copies web/out into this directory
// (see /Dockerfile stage 1 → stage 2), then the //go:embed
// directive below bakes the bundle into the binary. This avoids
// a separate web container in the integrated deployment: the
// pentestswarm binary IS the web server.
//
// The fall-back `embed.FS` only contains the .gitkeep file, which
// keeps `go build` working in a fresh checkout where the dev
// hasn't built the web yet — Has() returns false and the API
// server logs "web bundle not embedded" instead of crashing.
package webfs

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gofiber/fiber/v2"
)

//go:embed all:out
var bundle embed.FS

// ErrNoBundle is returned by FS() when the binary was built without
// the web bundle (e.g. `go build` from a fresh clone). Callers
// should treat this as "web dashboard is unavailable" rather than a
// fatal error — the API server should keep running on /api/v1 and
// /healthz.
var ErrNoBundle = errors.New("webfs: bundle not embedded (build the web or use the slim API image)")

// FS returns the embedded web filesystem rooted at the bundle's
// top-level directory. When the embed is empty (only the .gitkeep
// marker), FS returns ErrNoBundle so the caller can fall back to
// an external reverse proxy or a degraded error page.
//
// We strip the leading "out/" prefix so callers can use the
// returned FS as if it were the web/out directory itself — that's
// what the build pipeline writes, and we want zero translation
// between "what's on disk during dev" and "what's in the binary".
func FS() (fs.FS, error) {
	sub, err := fs.Sub(bundle, "out")
	if err != nil {
		return nil, err
	}
	// Probe for the Next.js index.html that every `next build`
	// produces. If it isn't there, the embed only contains the
	// .gitkeep placeholder and the bundle is effectively empty.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNoBundle
	}
	return sub, nil
}

// MustFS is the panic-on-error variant for boot-time wiring where
// a missing bundle is a configuration bug, not a runtime
// degradation. Use sparingly — prefer FS() in long-lived servers.
func MustFS() fs.FS {
	f, err := FS()
	if err != nil {
		panic(err)
	}
	return f
}

// FiberHandler returns a fiber.Handler that serves the embedded
// bundle. We provide a Fiber-native variant of Handler() because
// the http.Handler-to-fiber adaptor path (fasthttpadaptor) has
// subtle issues with io.Copy under Fiber's response lifecycle:
//
//   - the body stream gets closed by `c.Response().Reset()`
//   - the fasthttpadaptor writer then no-ops silently
//   - 0-byte responses with 200 status leak out
//
// Implementing the handler natively lets us stream bytes
// directly through c.Send, which Fiber tracks correctly
// (length / content-length / observability middleware).
//
// Routing rules are the same as Handler():
//   - GET /                  → index.html (SPA fallback)
//   - GET /<file>            → asset if it exists, else index.html
func FiberHandler() (fiber.Handler, error) {
	root, err := FS()
	if err != nil {
		return nil, err
	}
	// Pre-open the SPA shell once at startup. Reading the
	// file on every 404 is wasteful and a denial-of-service
	// vector (the embed.FS is in-memory, so the cost is
	// mostly a 1KB copy). indexBytes is the wire-ready
	// form of the shell.
	indexBytes, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return nil, fmt.Errorf("webfs: read index.html: %w", err)
	}

	return func(c *fiber.Ctx) error {
		// P5+ 一体部署：先让 Fiber 的 router 跑一遍。
		// 真正的 API 路由（/healthz /readyz /metrics
		// /api/v1/* /ws/*）会在这里 handle。如果 c.Next()
		// 返回 nil 且状态码 != 404，说明有 route 处理了
		// 请求，我们不要再写响应。
		if err := c.Next(); err == nil {
			status := c.Response().StatusCode()
			if status != 404 {
				return nil
			}
			// 状态码为 404 时，需要区分两种场景：
			//   1. 没有匹配的路由 → c.Route().Path 为空 → SPA fallback
			//   2. 路由匹配但 handler 返回了 404（如 /api/v1/campaigns/:id 未找到）→ 保留 JSON 响应
			if c.Route().Path != "" {
				return nil
			}
		}
		// Fall-through: 没有 API 路由匹配（404）。
		// 清理响应状态，serve SPA bundle。
		// Strip leading slash, resolve "." → index.
		p := strings.TrimPrefix(path.Clean(c.Path()), "/")
		if p == "" || p == "." {
			p = "index.html"
		}
		// SPA fallback: unknown path → index.html.
		// Try the exact path first, then try appending ".html"
		// (Next.js static export generates /settings.html for
		// the /settings route). Also handle the case where the
		// path is a directory (e.g. /reports contains
		// /reports/detail.html).
		//
		// IMPORTANT: paths that do NOT look like SPA routes
		// (e.g. /@vite/client, missing .js/.css assets) must
		// NOT be served as index.html — returning HTML with a
		// 200 for a JS request causes "Expected a JavaScript
		// module but the server responded with text/html".
		if fi, err := fs.Stat(root, p); err != nil || fi.IsDir() {
			if err == nil && fi.IsDir() {
				// Path is a directory — try index.html inside it,
				// then try p.html alongside it.
				if _, err2 := fs.Stat(root, p+"/index.html"); err2 == nil {
					p = p + "/index.html"
				} else if _, err2 := fs.Stat(root, p+".html"); err2 == nil {
					p = p + ".html"
				}
			} else if ext := path.Ext(p); ext == "" {
				if _, err2 := fs.Stat(root, p+".html"); err2 == nil {
					p = p + ".html"
				}
			}
		}
		if _, err := fs.Stat(root, p); err != nil {
			// If the path clearly isn't an SPA route (has a known
			// static extension or starts with '@'), return 404
			// instead of serving index.html as HTML for a JS/CSS
			// request. This prevents the "Expected a JavaScript
			// module but server responded with text/html" error.
			if isNonSPAPath(p) {
				return c.SendStatus(404)
			}
			p = "index.html"
		}
		// Reset response state from the 404 so our 200
		// + body land cleanly on the wire. Header.Reset
		// wipes the 404's headers but we want the
		// Content-Type / Cache-Control we just set, so
		// re-apply them AFTER the reset.
		c.Response().Header.Reset()
		// Cache-Control: hashed assets are immutable;
		// the SPA shell is always revalidated.
		if p == "index.html" {
			c.Set("Cache-Control", "no-cache")
		} else {
			c.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		// Content-Type by extension. Empty fallback
		// lets the browser sniff for unknown MIME.
		if ct := mimeFor(p); ct != "" {
			c.Set("Content-Type", ct)
		}
		c.Status(200)
		// Optimisation: the SPA shell is small and
		// always-in-memory. c.Send bytes is one
		// allocation, no io.Copy.
		if p == "index.html" {
			return c.Send(indexBytes)
		}
		// Hashed asset: read on demand.
		asset, err := fs.ReadFile(root, p)
		if err != nil {
			return c.Send(indexBytes) // SPA fallback
		}
		return c.Send(asset)
	}, nil
}

// Handler returns an http.Handler that serves the embedded bundle.
// This is the stdlib variant used for tests and for embedding
// inside an http.ServeMux. The Fiber-native variant
// (FiberHandler) is preferred in production.
func Handler() (http.Handler, error) {
	root, err := FS()
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip leading slash so path.Clean doesn't escape the
		// root. The embed FS is rooted at the bundle's top dir,
		// not at "/".
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			p = "index.html"
		}
		// SPA fallback: if the file doesn't exist, try
		// appending ".html" (Next.js static export generates
		// /settings.html for the /settings route). Also handle
		// directories (e.g. /reports).
		if fi, err := fs.Stat(root, p); err != nil || fi.IsDir() {
			if err == nil && fi.IsDir() {
				if _, err2 := fs.Stat(root, p+"/index.html"); err2 == nil {
					p = p + "/index.html"
				} else if _, err2 := fs.Stat(root, p+".html"); err2 == nil {
					p = p + ".html"
				}
			} else if ext := path.Ext(p); ext == "" {
				if _, err2 := fs.Stat(root, p+".html"); err2 == nil {
					p = p + ".html"
				}
			}
		}
		if _, err := fs.Stat(root, p); err != nil {
			p = "index.html"
		}

		f, err := root.Open(p)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		// Set Content-Type before writing so the browser
		// doesn't have to re-parse.
		if ct := mimeFor(p); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		// Disable caching for index.html (SPA shell) and
		// cache assets aggressively. Next.js's hashed URLs
		// (`/_next/static/...`) are content-addressed so
		// they're safe to cache forever.
		if p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

		if _, err := io.Copy(w, f); err != nil {
			// Headers already written — best we can do is
			// log. Browsers will see a truncated body.
			return
		}
	}), nil
}

// isNonSPAPath returns true when the path looks like a static asset
// request (not a client-side SPA route). Paths starting with '@'
// (e.g. /@vite/client) or ending with known static extensions that
// don't exist in the bundle should return 404, not index.html.
func isNonSPAPath(p string) bool {
	// Vite dev-server paths injected by stale builds or browser cache.
	if strings.HasPrefix(p, "@") {
		return true
	}
	// Known static extensions: if the file doesn't exist, don't
	// serve the SPA shell — the browser already knows it's asking
	// for a specific asset type.
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".mjs", ".css", ".json", ".svg", ".png",
		".jpg", ".jpeg", ".gif", ".webp", ".ico",
		".woff", ".woff2", ".ttf", ".map":
		return true
	}
	return false
}

func mimeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".map":
		return "application/json; charset=utf-8"
	}
	return ""
}
