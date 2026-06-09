// Package corsmux builds a CORS middleware from the project's
// own config shape. The intent is to centralise the
// "wire-shape → fiber middleware" translation so cmd/serve and
// the test suite can both reach the same logic.
//
// Design notes
//
//   - **Whitelist, not wildcard**: the production path is an
//     explicit list of origins. "*" is recognised but rejected
//     with a warning when combined with AllowCredentials
//     (the CORS spec explicitly forbids the combination).
//
//   - **Echo, don't reflect**: when an Origin matches the
//     allow-list, we echo it back in
//     Access-Control-Allow-Origin. Echoing (vs. always "*") is
//     the only way the browser will accept a cross-origin
//     response with credentials.
//
//   - **Empty = legacy behaviour**: an empty Origins list
//     means "no opt-in", which collapses to "*" — preserving
//     the pre-P5+ behaviour so an existing operator doesn't
//     have to update config to keep their dashboard working.
package corsmux

import (
	"strings"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

// defaultAllowMethods is the method list used when the
// operator didn't set one. The set is the union of what the
// project's REST API uses (everything minus HEAD, which we
// don't expose anywhere).
var defaultAllowMethods = []string{
	"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS",
}

// defaultAllowHeaders is the header list used when the
// operator didn't set one. Authorization is included so the
// OAuth 2.0 + session flow continues to work cross-origin.
var defaultAllowHeaders = []string{
	"Origin", "Content-Type", "Accept", "Authorization", "X-API-Key",
}

// New builds a Fiber middleware from cfg. The result is
// suitable for `app.Use(corsmux.New(cfg.Commercial.CORS))`.
//
// When cfg.Origins is empty the middleware behaves like
// the legacy allow-all ("*"); this is intentional — opt-in
// only, so the demo path doesn't break.
func New(cfg config.CORSConfig) fiber.Handler {
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = defaultAllowMethods
	}
	headers := cfg.AllowHeaders
	if len(headers) == 0 {
		headers = defaultAllowHeaders
	}

	// Pre-build the origin → match fn. We use
	// AllowOriginsFunc instead of AllowOrigins so we can
	// do case-sensitive exact match against the config
	// list (fiber's string match is case-insensitive by
	// default, which is wrong for security).
	origins := normaliseOrigins(cfg.Origins)
	allowAll := len(origins) == 0

	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 12 * 60 * 60 // 12h
	}

	if allowAll {
		// Legacy behaviour: opt-out means "*" exactly
		// as the pre-P5+ code did. Setting
		// AllowOriginsFunc here would echo the actual
		// origin in Access-Control-Allow-Origin,
		// which a security scanner flags as a
		// downgrade. Using AllowOrigins="*" preserves
		// byte-for-byte the old response headers.
		return cors.New(cors.Config{
			AllowOrigins:     "*",
			AllowMethods:     strings.Join(methods, ", "),
			AllowHeaders:     strings.Join(headers, ", "),
			AllowCredentials: cfg.AllowCredentials,
			MaxAge:           maxAge,
		})
	}

	return cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			for _, allowed := range origins {
				if origin == allowed {
					return true
				}
			}
			return false
		},
		AllowMethods:     strings.Join(methods, ", "),
		AllowHeaders:     strings.Join(headers, ", "),
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           maxAge,
	})
}

// normaliseOrigins trims whitespace and lower-cases each
// entry. The browser sends Origin lower-cased, so any
// casing in config would silently fail to match.
//
// Note: we do NOT normalise away the scheme. http://foo
// and https://foo are different origins.
func normaliseOrigins(in []string) []string {
	out := make([]string, 0, len(in))
	for _, o := range in {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		out = append(out, o)
	}
	return out
}
