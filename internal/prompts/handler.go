package prompts

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// Handler is the Fiber-side HTTP layer for the prompts
// editor. It exposes four endpoints:
//
//   GET    /api/v1/prompts          — list all 35 (with overrides)
//   GET    /api/v1/prompts/:type    — get one (override OR default)
//   PUT    /api/v1/prompts/:type    — save override
//   DELETE /api/v1/prompts/:type    — reset to default
//
// All four sit on top of Service; the Handler does no
// business logic, only param parsing + status-code mapping.
type Handler struct {
	svc   *Service
	authz Authorizer
}

// Authorizer decides whether a given user (from the session
// middleware) is allowed to write. We default to "any
// authenticated user" in WithAuthz or to "always allow"
// when WithAuthz is not called (dev / demo mode).
type Authorizer interface {
	// CanWrite returns nil if the user is allowed to save,
	// reset, or delete. The user string is the c.Locals
	// "user" field set by the auth.SessionMiddleware, or
	// empty if no session is active.
	CanWrite(c *fiber.Ctx, user string) error
}

// AlwaysAllowAuthorizer is the dev / demo fallback.
type AlwaysAllowAuthorizer struct{}

// CanWrite implements Authorizer.
func (AlwaysAllowAuthorizer) CanWrite(c *fiber.Ctx, user string) error {
	return nil
}

// NewHandler returns a Handler backed by svc. If authz is
// nil, AlwaysAllowAuthorizer is used.
func NewHandler(svc *Service, authz Authorizer) *Handler {
	if authz == nil {
		authz = AlwaysAllowAuthorizer{}
	}
	return &Handler{svc: svc, authz: authz}
}

// Register mounts the four routes on the given Fiber
// router. We accept fiber.Router (not *fiber.App) so this
// works for the (app) sub-group, a per-resource group, or
// the top-level app.
func (h *Handler) Register(app fiber.Router) {
	app.Get("/prompts", h.list)
	app.Get("/prompts/:type", h.get)
	app.Put("/prompts/:type", h.put)
	app.Delete("/prompts/:type", h.delete)
}

// list — GET /api/v1/prompts. Returns the full set of 35
// prompts. The frontend renders the list as the left rail
// of the editor.
func (h *Handler) list(c *fiber.Ctx) error {
	out, err := h.svc.List(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "list_failed",
			"detail": err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"count":      len(out),
		"total":      PromptTypeCount,
		"prompts":    out,
	})
}

// get — GET /api/v1/prompts/:type. 200 with the body;
// 404 if the type is valid but has no default and no
// override (i.e. it's a custom-only type with no .tmpl).
func (h *Handler) get(c *fiber.Ctx) error {
	t := PromptType(c.Params("type"))
	if !ValidType(t) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid_type",
			"type":  t,
		})
	}
	p, err := h.svc.Get(c.Context(), t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "not_found",
				"type":  t,
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "get_failed",
			"detail": err.Error(),
		})
	}
	return c.JSON(p)
}

// put — PUT /api/v1/prompts/:type with body { body: string }.
// Returns the saved prompt (with the new version).
func (h *Handler) put(c *fiber.Ctx) error {
	if err := h.authz.CanWrite(c, currentUser(c)); err != nil {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "forbidden",
			"detail": err.Error(),
		})
	}
	t := PromptType(c.Params("type"))
	if !ValidType(t) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid_type",
			"type":  t,
		})
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "bad_json",
			"detail": err.Error(),
		})
	}
	p, err := h.svc.Set(c.Context(), t, req.Body, currentUser(c))
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidType):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "invalid_type",
				"type":  t,
			})
		case errors.Is(err, ErrBodyEmpty):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "empty_body",
			})
		case errors.Is(err, ErrReadOnly):
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "read_only",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "save_failed",
			"detail": err.Error(),
		})
	}
	return c.JSON(p)
}

// delete — DELETE /api/v1/prompts/:type. Resets the override
// to the embedded default.
func (h *Handler) delete(c *fiber.Ctx) error {
	if err := h.authz.CanWrite(c, currentUser(c)); err != nil {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "forbidden",
			"detail": err.Error(),
		})
	}
	t := PromptType(c.Params("type"))
	if !ValidType(t) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid_type",
			"type":  t,
		})
	}
	if err := h.svc.Reset(c.Context(), t); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "reset_failed",
			"detail": err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"type":   t,
		"status": "reset",
	})
}

// currentUser reads the username injected by the auth
// middleware. The auth package is imported by the
// serve-time wiring; we don't import it here to keep this
// file independent. The string is "" when no session.
func currentUser(c *fiber.Ctx) string {
	if u, ok := c.Locals("user").(string); ok {
		return strings.TrimSpace(u)
	}
	if u, ok := c.Locals("username").(string); ok {
		return strings.TrimSpace(u)
	}
	return ""
}
