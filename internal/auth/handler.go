package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// Handler is the Fiber glue between HTTP and Service. It exposes
// every endpoint the front-end needs:
//
//	POST   /auth/login                    {username, password, remember?}
//	POST   /auth/logout                   (cookie cleared)
//	GET    /auth/me                       → 200 user / 401 guest
//	GET    /auth/oauth/:provider          → 302 to IdP
//	GET    /auth/oauth/callback?…         → 302 to /campaigns (or 401)
//	GET    /auth/providers                → 200 {providers: [{name, label}]}
//
// All endpoints are JSON in / JSON out, except for the OAuth flow
// which uses 302 redirects as the protocol demands.
type Handler struct {
	Service         *Service
	BaseURL         string
	CookieDomain    string
	CookieSecure    bool
	OAuthCallbackPath string // default "/api/v1/auth/oauth/callback"
}

// NewHandler builds a handler. baseURL is the public origin
// (e.g. "https://app.pentestswarm.ai") used to build absolute
// callback URLs for OAuth providers.
func NewHandler(svc *Service, baseURL string, cookieSecure bool) *Handler {
	return &Handler{
		Service:           svc,
		BaseURL:           strings.TrimRight(baseURL, "/"),
		CookieSecure:      cookieSecure,
		OAuthCallbackPath: "/api/v1/auth/oauth/callback",
	}
}

// Register wires the auth endpoints into a router group. The router
// is typically the `/api/v1` group already mounted by the api
// server.
func (h *Handler) Register(router fiber.Router) {
	router.Post("/auth/login", h.handleLogin)
	router.Post("/auth/logout", h.handleLogout)
	router.Get("/auth/me", h.handleMe)
	router.Get("/auth/providers", h.handleProviders)
	router.Get("/auth/oauth/:provider", h.handleOAuthBegin)
	router.Get("/auth/oauth/callback", h.handleOAuthCallback)
}

// SessionMiddleware reads the session cookie and, when valid,
// attaches the user to the request locals (`c.Locals("user", u)`).
// The /auth/login + /auth/oauth/* endpoints are exempt because they
// are the ones that *create* the session.
//
// Usage in a Fiber handler: `u, _ := c.Locals("user").(*auth.User)`.
func (h *Handler) SessionMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		cookie := c.Cookies(CookieName)
		if cookie == "" {
			return c.Next()
		}
		u, sess, err := h.Service.AuthenticateSession(cookie)
		if err != nil {
			// Cookie present but invalid: clear it so the next
			// request doesn't re-trigger the auth round-trip.
			h.clearSessionCookie(c)
			return c.Next()
		}
		c.Locals("user", u)
		c.Locals("session", sess)
		return c.Next()
	}
}

// RequireAuth is a middleware that 401s any request without a
// valid session. Use it on protected routes, e.g.:
//
//	api := app.Group("/api/v1", auth.RequireAuth())
func (h *Handler) RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if _, ok := c.Locals("user").(*User); !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "unauthorized",
				"hint":  "POST /api/v1/auth/login or /api/v1/auth/oauth/<provider>",
			})
		}
		return c.Next()
	}
}

// ---------- /auth/login ----------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

type sessionResponse struct {
	User        *User     `json:"user"`
	ExpiresAt   time.Time `json:"expires_at"`
	IssuedAt    time.Time `json:"issued_at"`
	OAuthProviders []string `json:"oauth_providers"`
}

func (h *Handler) handleLogin(c *fiber.Ctx) error {
	var req loginRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid_request",
			"hint":  "expected JSON body {username, password, remember?}",
		})
	}
	sess, u, err := h.Service.LoginWithPassword(req.Username, req.Password, req.Remember)
	if err != nil {
		// Generic error to avoid username enumeration.
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error":   "invalid_credentials",
			"message": "username or password is incorrect",
		})
	}
	h.setSessionCookie(c, sess.ID, sess.ExpiresAt)
	return c.JSON(sessionResponse{
		User:           u,
		ExpiresAt:      sess.ExpiresAt,
		IssuedAt:       sess.CreatedAt,
		OAuthProviders: h.Service.Providers.Names(),
	})
}

// ---------- /auth/logout ----------

func (h *Handler) handleLogout(c *fiber.Ctx) error {
	if cookie := c.Cookies(CookieName); cookie != "" {
		_ = h.Service.Logout(cookie)
	}
	h.clearSessionCookie(c)
	return c.JSON(fiber.Map{"ok": true})
}

// ---------- /auth/me ----------

func (h *Handler) handleMe(c *fiber.Ctx) error {
	u, ok := c.Locals("user").(*User)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "unauthorized",
		})
	}
	return c.JSON(fiber.Map{
		"user":           u,
		"oauth_providers": h.Service.Providers.Names(),
	})
}

// ---------- /auth/providers ----------

func (h *Handler) handleProviders(c *fiber.Ctx) error {
	names := h.Service.Providers.Names()
	out := make([]fiber.Map, 0, len(names))
	for _, n := range names {
		p := h.Service.Providers.Get(n)
		if p == nil {
			continue
		}
		out = append(out, fiber.Map{
			"name": n,
			"label": p.DisplayName(),
		})
	}
	return c.JSON(fiber.Map{"providers": out})
}

// ---------- /auth/oauth/:provider ----------

func (h *Handler) handleOAuthBegin(c *fiber.Ctx) error {
	providerName := c.Params("provider")
	if providerName == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing_provider",
		})
	}
	redirectAfter := c.Query("redirect", "/campaigns")
	state, _, err := h.Service.BeginOAuth(providerName, redirectAfter)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":  "oauth_begin_failed",
			"detail": err.Error(),
		})
	}
	redirectURI := h.BaseURL + h.OAuthCallbackPath
	authURL, err := h.Service.BuildAuthURL(providerName, state, redirectURI)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":  "oauth_build_url_failed",
			"detail": err.Error(),
		})
	}
	return c.Redirect(authURL, fiber.StatusFound)
}

// ---------- /auth/oauth/callback ----------

func (h *Handler) handleOAuthCallback(c *fiber.Ctx) error {
	state := c.Query("state")
	code := c.Query("code")
	if state == "" || code == "" {
		return c.Redirect("/login?error=missing_state_or_code", fiber.StatusFound)
	}
	redirectURI := h.BaseURL + h.OAuthCallbackPath
	sess, _, err := h.Service.CompleteOAuth(c.Context(), state, code, redirectURI)
	if err != nil {
		// Don't leak internal errors to the browser URL — keep them
		// in the server log only.
		fmt.Printf("[auth] oauth callback failed: %v\n", err)
		return c.Redirect("/login?error=oauth_failed", fiber.StatusFound)
	}
	h.setSessionCookie(c, sess.ID, sess.ExpiresAt)
	// Look up the original redirect target from the consumed state.
	// We can't do that here because CompleteOAuth already consumed
	// the state — the front-end reads `redirect` from a same-origin
	// query string or falls back to /campaigns.
	return c.Redirect("/campaigns", fiber.StatusFound)
}

// ---------- Cookie helpers ----------

func (h *Handler) setSessionCookie(c *fiber.Ctx, id string, expires time.Time) {
	cookie := &fiber.Cookie{
		Name:     CookieName,
		Value:    id,
		Expires:  expires,
		HTTPOnly: true,
		Secure:   h.CookieSecure,
		SameSite: "Lax", // OAuth callback requires Lax
		Path:     "/",
	}
	if h.CookieDomain != "" {
		cookie.Domain = h.CookieDomain
	}
	c.Cookie(cookie)
}

func (h *Handler) clearSessionCookie(c *fiber.Ctx) {
	cookie := &fiber.Cookie{
		Name:     CookieName,
		Value:    "",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HTTPOnly: true,
		Secure:   h.CookieSecure,
		SameSite: "Lax",
		Path:     "/",
	}
	if h.CookieDomain != "" {
		cookie.Domain = h.CookieDomain
	}
	c.Cookie(cookie)
}

// ---------- Errors ----------

// errResponse is a small helper for consistent error shapes.
func errResponse(c *fiber.Ctx, status int, code, msg string) error {
	return c.Status(status).JSON(fiber.Map{
		"error":   code,
		"message": msg,
	})
}

// Unused but reserved for future structured logging.
var _ = errors.New
var _ = url.QueryEscape
