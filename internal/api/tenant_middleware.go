package api

import (
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/auth"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tenant"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// tenantMiddleware stamps the request's user context with a
// *tenant.Tenant derived from the session. The storage
// layer can then call tenant.FromContext(c.UserContext()) to
// know which tenant the request belongs to without re-reading
// the session.
//
// Activation: only when cfg.Commercial.MultiTenant is true.
// In the single-tenant / demo path the middleware is a
// no-op and the storage layer falls back to the
// "default" tenant (see internal/tenant.DefaultTenantID).
//
// Failure mode: a session with no TenantID in a multi-tenant
// deployment is a server-side bug (it means the user record
// was inserted without a tenant_id, or the Session was
// built from a User that predates the migration). We 500
// rather than silently fall back to the default tenant —
// silent fallback is what got us into the "where did this
// row go?" conversation in the first place.
func tenantMiddleware(enabled bool, store tenant.Store) fiber.Handler {
	if !enabled {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return func(c *fiber.Ctx) error {
		sess, _ := c.Locals("session").(*auth.Session)
		if sess == nil {
			// Anonymous request — auth middleware will
			// 401 it on protected paths; we don't stamp a
			// tenant.
			return c.Next()
		}
		if sess.TenantID == "" {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "session has no tenant binding",
			})
		}
		uid, err := uuid.Parse(sess.TenantID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "session tenant id is not a valid uuid",
			})
		}
		t, err := store.Get(c.UserContext(), uid)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "session tenant not found",
			})
		}
		// Stamp Fiber's UserContext so downstream
		// handlers / storage calls see the tenant.
		c.SetUserContext(tenant.NewContext(c.UserContext(), t))
		return c.Next()
	}
}
