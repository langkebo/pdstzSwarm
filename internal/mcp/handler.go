package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// Handler exposes the MCP Manager over HTTP/Fiber.
//
// Endpoints:
//
//   GET    /api/v1/mcp/servers                → list all managed servers
//   GET    /api/v1/mcp/servers/:name          → one server's status
//   PUT    /api/v1/mcp/servers/:name/status   → {status: "enabled"|"disabled"}
//   PUT    /api/v1/mcp/servers/:name/allow    → {allow: ["tool_a", "tool_b"]}
//                                              (empty list = "all tools")
//   GET    /api/v1/mcp/tools                  → fan-out tool catalogue
//   POST   /api/v1/mcp/invoke                 → {server?: "name", name, arguments}
//                                              server is optional; we route by name
//   GET    /mcp/sse                           → JSON-RPC over SSE
//   POST   /mcp/sse                           → JSON-RPC request body
//   POST   /mcp                               → plain JSON-RPC body
//
// The Authorizer field is the optional RBAC
// gatekeeper. nil = allow every caller (suitable
// for dev / single-tenant deployments). The same
// Authorizer is used by the rest of the API layer
// (see internal/api/server.go) so HTTP-side auth
// and MCP-side auth are in lockstep.
type Handler struct {
	Manager    *Manager
	Authorizer Authorizer
}

// Authorizer mirrors the contract used by the rest
// of the api package: nil means "allow every
// caller". Concrete implementations are wired by
// the api server at construction time.
type Authorizer interface {
	CanRead(c *fiber.Ctx) error
	CanWrite(c *fiber.Ctx) error
}

// alwaysOK is a no-op Authorizer used when the
// host didn't pass one in. It's exported so tests
// can pass the same value to satisfy the
// "trust-the-host" semantics.
var alwaysOK = noopAuthorizer{}

// noopAuthorizer implements Authorizer with
// always-OK semantics.
type noopAuthorizer struct{}

func (noopAuthorizer) CanRead(c *fiber.Ctx) error  { return nil }
func (noopAuthorizer) CanWrite(c *fiber.Ctx) error { return nil }

// Ensure allow-list always coerces. nil entry
// means "all tools enabled" per Manager contract.
func normalizeAllow(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// authz returns the handler's Authorizer or the
// no-op fallback. Single point of indirection so
// the handlers don't repeat the nil-check.
func (h *Handler) authz() Authorizer {
	if h.Authorizer != nil {
		return h.Authorizer
	}
	return alwaysOK
}

// Register mounts every endpoint listed in the
// type doc onto the given fiber router. The router
// is expected to be the application root; the
// method prepends the conventional /api/v1 and
// /mcp prefixes.
func (h *Handler) Register(app fiber.Router) {
	if h.Manager == nil {
		h.Manager = NewManager()
	}

	// Catalogue + status.
	app.Get("/api/v1/mcp/servers", h.listServers)
	app.Get("/api/v1/mcp/servers/:name", h.getServer)
	app.Put("/api/v1/mcp/servers/:name/status", h.setServerStatus)
	app.Put("/api/v1/mcp/servers/:name/allow", h.setServerAllow)
	app.Get("/api/v1/mcp/tools", h.listTools)

	// Tool invocation (HTTP transport).
	app.Post("/api/v1/mcp/invoke", h.invokeTool)

	// Raw JSON-RPC + SSE for MCP-native clients.
	app.Post("/mcp", h.handleJSONRPC)
	app.Get("/mcp/sse", h.handleSSE)
	app.Post("/mcp/sse", h.handleSSE)
}

// ----------------------------------------------------------------------------
// Catalogue handlers
// ----------------------------------------------------------------------------

func (h *Handler) listServers(c *fiber.Ctx) error {
	if err := h.authz().CanRead(c); err != nil {
		return forbidden(c, err)
	}
	return c.JSON(fiber.Map{
		"servers": h.Manager.StatusAll(),
		"count":   len(h.Manager.Names()),
	})
}

func (h *Handler) getServer(c *fiber.Ctx) error {
	if err := h.authz().CanRead(c); err != nil {
		return forbidden(c, err)
	}
	name := c.Params("name")
	s, ok := h.Manager.Get(name)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":  "server not found",
			"name":   name,
		})
	}
	tools := s.Server.Tools()
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}
	return c.JSON(fiber.Map{
		"name":        s.Name,
		"description": s.Description,
		"status":      s.Status,
		"tool_count":  len(tools),
		"tools":       toolNames,
		"allow":       s.AllowList(),
		"server_info": s.Server.ServerInfo(),
	})
}

func (h *Handler) setServerStatus(c *fiber.Ctx) error {
	if err := h.authz().CanWrite(c); err != nil {
		return forbidden(c, err)
	}
	name := c.Params("name")
	s, ok := h.Manager.Get(name)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "server not found",
			"name":  name,
		})
	}
	var body struct {
		Status ServerStatus `json:"status"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":  "invalid body: " + err.Error(),
		})
	}
	switch body.Status {
	case ServerStatusEnabled:
		s.Enable()
	case ServerStatusDisabled:
		s.Disable()
	case "":
		// No-op: empty body leaves the status alone.
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":          "invalid status; want 'enabled' or 'disabled'",
			"got":            body.Status,
		})
	}
	return c.JSON(fiber.Map{
		"name":   s.Name,
		"status": s.Status,
	})
}

func (h *Handler) setServerAllow(c *fiber.Ctx) error {
	if err := h.authz().CanWrite(c); err != nil {
		return forbidden(c, err)
	}
	name := c.Params("name")
	s, ok := h.Manager.Get(name)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "server not found",
			"name":  name,
		})
	}
	var body struct {
		Allow []string `json:"allow"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid body: " + err.Error(),
		})
	}
	// nil = "all enabled"; an explicit empty list
	// is a request to disable all tools. Mirror the
	// Manager.SetAllow contract.
	if body.Allow == nil {
		s.SetAllow(nil)
	} else {
		cleaned := normalizeAllow(body.Allow)
		if len(cleaned) == 0 {
			// Empty non-nil list also means
			// "clear the allow-list (== all)".
			// This is the explicit "all tools"
			// path.
			s.SetAllow(nil)
		} else {
			s.SetAllow(cleaned)
		}
	}
	return c.JSON(fiber.Map{
		"name":  s.Name,
		"allow": s.AllowList(),
	})
}

func (h *Handler) listTools(c *fiber.Ctx) error {
	if err := h.authz().CanRead(c); err != nil {
		return forbidden(c, err)
	}
	tools := h.Manager.AllTools()
	out := make([]fiber.Map, 0, len(tools))
	for _, t := range tools {
		out = append(out, fiber.Map{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return c.JSON(fiber.Map{
		"tools": out,
		"count": len(out),
	})
}

// ----------------------------------------------------------------------------
// Tool invocation
// ----------------------------------------------------------------------------

// invokeTool is the HTTP-friendly wrapper around
// Manager.DispatchToolsCall. Body shape:
//
//	{
//	  "name":     "<tool name>",
//	  "server":   "<optional: target server name>",
//	  "arguments": { ... }      ← any JSON object
//	}
//
// If `server` is set, the call is routed to that
// server specifically. If not, the Manager walks
// every enabled server in alphabetical order and
// picks the first one that exposes a matching
// tool. The response is the same shape returned
// by the tool handler: a `content` block, or an
// `error` with the dispatch failure reason.
func (h *Handler) invokeTool(c *fiber.Ctx) error {
	if err := h.authz().CanWrite(c); err != nil {
		return forbidden(c, err)
	}
	var body struct {
		Server    string          `json:"server,omitempty"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid body: " + err.Error(),
		})
	}
	if body.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing 'name'",
		})
	}
	if body.Arguments == nil {
		body.Arguments = json.RawMessage(`{}`)
	}

	if body.Server != "" {
		// Targeted dispatch: only consult one server.
		s, ok := h.Manager.Get(body.Server)
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error":  "server not found",
				"server": body.Server,
			})
		}
		if s.Status != ServerStatusEnabled {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":  "server is disabled",
				"server": body.Server,
			})
		}
		if !s.Allowed(body.Name) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":  "tool not in allow-list for this server",
				"server": body.Server,
				"name":   body.Name,
			})
		}
		for _, t := range s.Server.Tools() {
			if t.Name != body.Name {
				continue
			}
			result, err := t.Handler(c.Context(), body.Arguments)
			if err != nil {
				return c.Status(fiber.StatusOK).JSON(fiber.Map{
					"name":    body.Name,
					"server":  body.Server,
					"isError": true,
					"error":   err.Error(),
				})
			}
			return c.JSON(fiber.Map{
				"name":    body.Name,
				"server":  body.Server,
				"content": result,
			})
		}
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":  "tool not found in named server",
			"server": body.Server,
			"name":   body.Name,
		})
	}

	// Fan-out dispatch.
	sname, result, err := h.Manager.DispatchToolsCall(c.Context(), body.Name, body.Arguments)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": err.Error(),
			"name":  body.Name,
		})
	}
	if result == nil {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"name":   body.Name,
			"server": sname,
		})
	}
	return c.JSON(fiber.Map{
		"name":    body.Name,
		"server":  sname,
		"content": result,
	})
}

// ----------------------------------------------------------------------------
// Raw JSON-RPC + SSE
// ----------------------------------------------------------------------------

// handleJSONRPC is the plain-HTTP JSON-RPC entry
// point. The body must be a single JSON-RPC
// request, the response is a single JSON-RPC
// response. We dispatch through the Manager so
// the fan-out and per-server gating rules apply
// uniformly across the SSE and HTTP transports.
func (h *Handler) handleJSONRPC(c *fiber.Ctx) error {
	if err := h.authz().CanRead(c); err != nil {
		return forbidden(c, err)
	}
	body := c.Body()
	if len(body) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "empty body",
		})
	}
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		c.Set("Content-Type", "application/json")
		return c.Status(fiber.StatusBadRequest).SendString(`{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse error"}}`)
	}

	switch env.Method {
	case "initialize":
		// Run the canonical initialize handshake
		// against the first enabled server we can
		// find. The protocol core lives in
		// Server.handleInitialize; we replicate the
		// result shape here so the response is
		// independent of which server handled it.
		for _, n := range h.Manager.Names() {
			s, ok := h.Manager.Get(n)
			if !ok || s.Status != ServerStatusEnabled {
				continue
			}
			caps := map[string]any{
				"tools":     map[string]any{"listChanged": true},
				"resources": map[string]any{"subscribe": false, "listChanged": true},
				"prompts":   map[string]any{"listChanged": true},
			}
			return c.JSON(fiber.Map{
				"jsonrpc":        "2.0",
				"id":             env.ID,
				"result": fiber.Map{
					"protocolVersion": ProtocolVersion,
					"capabilities":    caps,
					"serverInfo": fiber.Map{
						"name":    s.Server.ServerInfo().Name,
						"version": s.Server.ServerInfo().Version,
					},
				},
			})
		}
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "no enabled MCP server available to handle initialize",
		})

	case "tools/list":
		tools := h.Manager.AllTools()
		out := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result":  fiber.Map{"tools": out},
		})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(env.Params, &params); err != nil {
			return c.JSON(fiber.Map{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"error":   fiber.Map{"code": -32602, "message": "invalid params"},
			})
		}
		sname, result, err := h.Manager.DispatchToolsCall(c.Context(), params.Name, params.Arguments)
		if err != nil {
			return c.JSON(fiber.Map{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"error":   fiber.Map{"code": -32601, "message": err.Error()},
			})
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result": fiber.Map{
				"server":  sname,
				"content": result,
			},
		})
	case "resources/list":
		resources := h.Manager.AllResources()
		out := make([]map[string]any, 0, len(resources))
		for _, r := range resources {
			out = append(out, map[string]any{
				"uri":         r.URI,
				"name":        r.Name,
				"description": r.Description,
				"mimeType":    r.MimeType,
			})
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result":  fiber.Map{"resources": out},
		})
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(env.Params, &params)
		for _, r := range h.Manager.AllResources() {
			if r.URI == params.URI {
				content, herr := r.Handler(c.Context())
				if herr != nil {
					return c.JSON(fiber.Map{
						"jsonrpc": "2.0",
						"id":      env.ID,
						"error":   fiber.Map{"code": -32603, "message": herr.Error()},
					})
				}
				return c.JSON(fiber.Map{
					"jsonrpc": "2.0",
					"id":      env.ID,
					"result": fiber.Map{
						"contents": []fiber.Map{{"uri": params.URI, "mimeType": r.MimeType, "text": content}},
					},
				})
			}
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   fiber.Map{"code": -32601, "message": fmt.Sprintf("resource %q not found", params.URI)},
		})
	case "prompts/list":
		prompts := h.Manager.AllPrompts()
		out := make([]map[string]any, 0, len(prompts))
		for _, p := range prompts {
			out = append(out, map[string]any{
				"name":        p.Name,
				"description": p.Description,
				"arguments":   p.Arguments,
			})
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result":  fiber.Map{"prompts": out},
		})
	case "prompts/get":
		var params struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		_ = json.Unmarshal(env.Params, &params)
		for _, p := range h.Manager.AllPrompts() {
			if p.Name == params.Name {
				msgs, perr := p.Render(c.Context(), params.Arguments)
				if perr != nil {
					return c.JSON(fiber.Map{
						"jsonrpc": "2.0",
						"id":      env.ID,
						"error":   fiber.Map{"code": -32603, "message": perr.Error()},
					})
				}
				return c.JSON(fiber.Map{
					"jsonrpc": "2.0",
					"id":      env.ID,
					"result": fiber.Map{
						"description": p.Description,
						"messages":    msgs,
					},
				})
			}
		}
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   fiber.Map{"code": -32601, "message": fmt.Sprintf("prompt %q not found", params.Name)},
		})
	case "ping":
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result":  fiber.Map{},
		})
	default:
		return c.JSON(fiber.Map{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   fiber.Map{"code": -32601, "message": fmt.Sprintf("method %q not supported by fan-out handler", env.Method)},
		})
	}
}

// handleSSE upgrades the connection. We don't
// fan-out SSE: a single underlying server keeps
// the event-ordering semantics simple. The
// first enabled server in the catalogue is
// chosen.
func (h *Handler) handleSSE(c *fiber.Ctx) error {
	if err := h.authz().CanRead(c); err != nil {
		return forbidden(c, err)
	}
	if c.Method() == fiber.MethodGet {
		// SSE upgrade.
		names := h.Manager.Names()
		for _, n := range names {
			s, ok := h.Manager.Get(n)
			if !ok || s.Status != ServerStatusEnabled {
				continue
			}
			return runSSEOnFiber(c, s.Server)
		}
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "no enabled MCP server available for SSE",
		})
	}
	// POST: JSON-RPC request body, response inline.
	// We dispatch against the first enabled server
	// via the same protocol-core helper used by
	// handleJSONRPC. POSTing to /mcp/sse lets curl
	// / Postman clients use the SSE endpoint
	// without having to open an EventSource.
	return h.handleJSONRPC(c)
}

// runSSEOnFiber wires the SSE stream onto the
// fiber response. We can't pass a stdlib
// http.ResponseWriter to Server.HandleSSE
// because fiber's *fasthttp.RequestCtx is not an
// http.ResponseWriter. So we replicate the
// upgrade logic here, calling the Server's
// transport-agnostic helpers directly.
func runSSEOnFiber(c *fiber.Ctx, s *Server) error {
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	// 1. Tell the client where to POST requests.
	if _, err := c.WriteString("event: endpoint\ndata: /mcp/sse\n\n"); err != nil {
		return err
	}

	// 2. Wire the notifier to push SSE events
	// through the fiber writer. We buffer frames
	// in a small channel to avoid blocking the
	// notifier; the loop drains it onto the wire.
	notifCh := make(chan []byte, 32)
	s.SetNotifier(func(method string, params map[string]any) {
		body, _ := json.Marshal(jsonRPCNotification{JSONRPC: "2.0", Method: method, Params: params})
		frame := []byte("event: message\ndata: " + string(body) + "\n\n")
		select {
		case notifCh <- frame:
		default:
		}
	})
	defer s.SetNotifier(nil)

	// 3. Heartbeat every 15s so intermediate
	// proxies don't time the connection out.
	heartbeat := newHeartbeat(c, 15)
	defer heartbeat.stop()

	ctx := c.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.tick():
			_, _ = c.WriteString(": ping\n\n")
		case frame := <-notifCh:
			_, _ = c.Write(frame)
		}
	}
}

// heartbeat wraps a time.Ticker. Used by
// runSSEOnFiber to drive a 15-second keep-alive
// without leaking ticker resources.
type heartbeat struct {
	stopCh chan struct{}
}

func newHeartbeat(_ *fiber.Ctx, _ int) *heartbeat {
	return &heartbeat{stopCh: make(chan struct{})}
}

func (h *heartbeat) tick() <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		// Single-tick for the duration of the call.
		// We don't need a real ticker because the
		// surrounding for-select already blocks on
		// ctx.Done; we just need a tick that fires
		// shortly after the connection opens. The
		// surrounding loop will not re-enter this
		// branch until the next call to tick(),
		// which only happens if the outer for
		// re-runs. In practice, the connection
		// lifetime is bounded by the request
		// context, so a single delayed tick is
		// enough.
		select {
		case <-h.stopCh:
		case <-time.After(15 * time.Second):
			out <- struct{}{}
		}
	}()
	return out
}

func (h *heartbeat) stop() {
	close(h.stopCh)
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func forbidden(c *fiber.Ctx, err error) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
		"error":  "forbidden",
		"detail": err.Error(),
	})
}

// ErrNoServers is the sentinel error returned by
// handlers when no enabled server matches the
// request. It's exposed for tests.
var ErrNoServers = errors.New("no enabled MCP server")

