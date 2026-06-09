package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/api/ws"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/auth"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/corsmux"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/engine"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/mcp"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/models"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/appmetrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/prompts"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/reports"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tenant"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/webfs"
	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"
)

// Server wraps the Fiber HTTP server with campaign state.
type Server struct {
	app    *fiber.App
	port   int
	cfg    *config.Config
	runner *engine.Runner
	board  blackboard.Board // optional; when set, findings stream live to /ws
	campaigns sync.Map       // id -> CampaignState
	hub       *ws.EventHub

	// P5+ 观测: process-wide observability bundle. Always non-nil
	// after NewServer — the registry starts empty and gets
	// populated by appmetrics.New at construction time. Tests can
	// use the Bundle() accessor to scrape / verify.
	metricsReg *metrics.Registry
	metrics    *appmetrics.All
	obsHandler *observability.Handler

	// bridges maps campaignID -> its FindingsBridge. Populated when a
	// campaign starts; torn down when it ends. The map itself is
	// guarded by bridgesMu.
	bridgesMu sync.Mutex
	bridges   map[string]*FindingsBridge

	// findingHooks are invoked for every finding every bridge
	// republishes. Set via WithFindingHook before Start.
	findingHooks []FindingHook

	// reportHandler, when non-nil, exposes /api/v1/reports/* endpoints.
	// Set via WithReportHandler.
	reportHandler *reports.Handler

	// authHandler, when non-nil, exposes /api/v1/auth/* endpoints
	// (password login + OAuth 2.0). Set via WithAuthHandler.
	authHandler *auth.Handler

	// promptsHandler, when non-nil, exposes /api/v1/prompts/* endpoints
	// (35 PromptType CRUD). Set via WithPromptsHandler.
	promptsHandler *prompts.Handler

	// mcpHandler, when non-nil, exposes /api/v1/mcp/* endpoints
	// plus the raw JSON-RPC + SSE transports (/mcp, /mcp/sse).
	// Set via WithMCPHandler. Construction is the caller's job
	// — typically by calling mcp.NewHandler(manager) where
	// manager was assembled by cli/serve.go.
	mcpHandler *mcp.Handler

	// tenantStore backs the tenant middleware. When nil
	// and MultiTenant is enabled, the middleware 500s on
	// any session-bearing request — better than silently
	// mixing tenants.
	tenantStore tenant.Store
	// multiTenant is the kill-switch for the tenant
	// middleware. Set from cfg.Commercial.MultiTenant
	// during server construction.
	multiTenant bool
}

// CampaignState holds in-memory state for a running campaign.
type CampaignState struct {
	Campaign pipeline.Campaign          `json:"campaign"`
	Events   []pipeline.CampaignEvent   `json:"events"`
	Findings []pipeline.ClassifiedFinding `json:"findings"`
	UserInputs []ws.UserMessage `json:"user_inputs"`
	Cancel     context.CancelFunc         `json:"-"`
	Credentials *ParsedCredentials        `json:"-"`
}

// ParsedCredentials stores username/password for authenticated scanning.
type ParsedCredentials struct {
	Username string
	Password string
}

// CreateCampaignRequest is the request body for creating a campaign.
type CreateCampaignRequest struct {
	Target    string   `json:"target"`
	Scope     []string `json:"scope"`
	Objective string   `json:"objective"`
	Mode      string   `json:"mode"`
	DryRun    bool     `json:"dry_run"`
	Username  string   `json:"username,omitempty"`
	Password  string   `json:"password,omitempty"`
}

// deriveScope extracts a default scope list from the target string.
//   - "https://www.vulnhub.com"              → ["www.vulnhub.com"]
//   - "https://www.vulnhub.com:8443/path"    → ["www.vulnhub.com:8443"]
//   - "192.168.1.10"                         → ["192.168.1.10"]
//   - "192.168.1.0/24"                       → ["192.168.1.0/24"]
//   - "example.com, api.example.com"         → ["example.com", "api.example.com"]
func deriveScope(target string) []string {
	target = strings.TrimSpace(target)

	// Comma-separated list → split and clean each entry.
	if strings.Contains(target, ",") {
		var out []string
		for _, part := range strings.Split(target, ",") {
			s := deriveSingleScope(part)
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	s := deriveSingleScope(target)
	if s == "" {
		return nil
	}
	return []string{s}
}

// deriveSingleScope strips protocol and path from a single target entry.
func deriveSingleScope(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// If it looks like a URL, parse it.
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		// Simple manual parse to avoid importing net/url for a
		// single use case. net/url is already imported indirectly
		// but we keep the dependency surface minimal here.
		noProto := raw
		if idx := strings.Index(noProto, "://"); idx != -1 {
			noProto = noProto[idx+3:]
		}
		// Strip path.
		if idx := strings.IndexByte(noProto, '/'); idx != -1 {
			noProto = noProto[:idx]
		}
		// Strip auth (user:pass@).
		if idx := strings.IndexByte(noProto, '@'); idx != -1 {
			noProto = noProto[idx+1:]
		}
		return strings.TrimSpace(noProto)
	}

	// CIDR or bare IP/hostname — keep as-is, strip any trailing path.
	if idx := strings.IndexByte(raw, '/'); idx != -1 {
		// Check if it's a CIDR (e.g. 192.168.1.0/24) or a path.
		// CIDR: only digits and dots before the slash.
		before := raw[:idx]
		isCIDR := true
		for _, c := range before {
			if c != '.' && c != ':' && (c < '0' || c > '9') {
				isCIDR = false
				break
			}
		}
		if isCIDR {
			return raw
		}
		// Path separator — strip path.
		if idx := strings.IndexByte(raw, '/'); idx != -1 {
			raw = raw[:idx]
		}
	}

	return strings.TrimSpace(raw)
}

// PutUserInputRequest is the request body for injecting an
// operator message into a running campaign. P5+ 用户输入闭环.
//
// Text is the message body (1–4096 chars, trimmed). The server
// assigns the ID + timestamp and stamps the campaign ID, so clients
// only need to send {text, author?}. Author is optional — when
// omitted, the server falls back to the session username (if
// authHandler is wired) or "anonymous".
type PutUserInputRequest struct {
	Text   string `json:"text"`
	Author string `json:"author,omitempty"`
}

// MaxUserInputLength caps the message body to keep the WebSocket
// envelope and the in-memory log bounded. 4 KiB is generous for an
// operator hint ("focus on /admin", "skip the staging env", "explain
// the SQLi chain") without risking memory bloat on a misbehaving
// client that posts a 1 MB blob.
const MaxUserInputLength = 4096

// NewServer creates an API server. Pass-through options to
// the underlying engine.Runner — most callers will use
// engine.WithLLMProviderDecorator to install observability
// wrappers.
func NewServer(port int, cfg *config.Config, opts ...engine.Option) *Server {
	return NewServerWithBoard(port, cfg, nil, opts...)
}

// NewServerWithBoard creates an API server with an optional blackboard
// for live findings streaming. When board is non-nil, starting a
// campaign spawns a FindingsBridge that subscribes to the blackboard
// and pushes new findings to the WebSocket hub in real time. Pass nil
// to disable that path (legacy event-only mode).
func NewServerWithBoard(port int, cfg *config.Config, board blackboard.Board, opts ...engine.Option) *Server {
	// P5+ 观测: build the metrics registry + bundle and the
	// observability HTTP handler *before* the Fiber app so that
	// the request-timing middleware can call into the same
	// metrics struct that the /metrics endpoint exposes. Wiring
	// the registry globally at construction time (not lazily on
	// first scrape) means even the very first request's metrics
	// are recorded.
	reg := metrics.NewRegistry()
	bundle := appmetrics.New(reg)
	obsH := observability.NewHandler(reg)
	// Readyz: ready when the LLM provider is configured. We
	// don't ping it here (could take seconds); the readiness
	// signal is "config present" which is enough for a
	// scraping-loadbalancer probe.
	obsH.ReadinessCheck = func() error {
		if cfg == nil || cfg.Orchestrator.Provider == "" {
			return fmt.Errorf("orchestrator provider not configured")
		}
		return nil
	}

	app := fiber.New(fiber.Config{
		AppName:      "pentestswarm",
		ErrorHandler: errorHandler,
	})

	app.Use(recover.New())
	app.Use(observabilityMiddleware(bundle))
	app.Use(logger.New(logger.Config{
		Format: "${time} ${status} ${method} ${path} ${latency}\n",
	}))
	app.Use(corsmux.New(cfg.Commercial.CORS))

	s := &Server{
		app:         app,
		port:        port,
		cfg:         cfg,
		runner:      engine.NewRunner(cfg, opts...),
		hub:         ws.NewEventHub(bundle),
		board:       board,
		bridges:     make(map[string]*FindingsBridge),
		metricsReg:  reg,
		metrics:     bundle,
		obsHandler:  obsH,
		multiTenant: cfg.Commercial.MultiTenant,
	}
	s.registerRoutes()
	obsH.Register(app)

	return s
}

// NewServerWithObservability is the variant that lets the caller
// pre-build a metrics bundle and share it with the engine runner
// (e.g. via engine.WithLLMMetrics). The bundle, registry, and
// observability handler are passed in; the server does NOT create
// its own. Use this when you need a single registry/bundle to
// back both the HTTP / WebSocket layer AND the runner's LLM
// decorator (the P5+ 观测 pattern).
//
// The observability endpoints (/metrics, /healthz, /readyz) are
// mounted on the supplied handler.
func NewServerWithObservability(
	port int, cfg *config.Config, board blackboard.Board,
	reg *metrics.Registry, bundle *appmetrics.All, obsH *observability.Handler,
	opts ...engine.Option,
) *Server {
	app := fiber.New(fiber.Config{
		AppName:      "pentestswarm",
		ErrorHandler: errorHandler,
	})

	app.Use(recover.New())
	app.Use(observabilityMiddleware(bundle))
	app.Use(logger.New(logger.Config{
		Format: "${time} ${status} ${method} ${path} ${latency}\n",
	}))
	app.Use(corsmux.New(cfg.Commercial.CORS))

	s := &Server{
		app:        app,
		port:       port,
		cfg:        cfg,
		runner:     engine.NewRunner(cfg, opts...),
		hub:        ws.NewEventHub(bundle),
		board:      board,
		bridges:    make(map[string]*FindingsBridge),
		metricsReg: reg,
		metrics:    bundle,
		obsHandler: obsH,
	}
	s.registerRoutes()
	obsH.Register(app)

	return s
}

// Bundle returns the observability bundle. Exposed for tests
// and for embedders that want to feed custom hooks into the
// same registry. Returns nil if NewServer was bypassed (e.g.
// when callers construct Server literally in tests).
func (s *Server) Bundle() *appmetrics.All {
	return s.metrics
}

// Registry returns the underlying *metrics.Registry. Exposed
// primarily for embedders that want to register their own
// custom metrics into the same /metrics output.
func (s *Server) Registry() *metrics.Registry {
	return s.metricsReg
}

// observabilityMiddleware records HTTP request count / duration /
// in-flight for every Fiber request. The metric labels are
// (method, path, status) where path is the Fiber route pattern
// (e.g. "/api/v1/campaigns/:id") — not the raw URL — to keep
// cardinality bounded.
func observabilityMiddleware(b *appmetrics.All) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if b == nil {
			return c.Next()
		}
		b.HTTPInFlight.Inc(nil)
		defer b.HTTPInFlight.Dec(nil)
		start := time.Now()
		err := c.Next()
		// Resolve the route pattern. Fiber populates this only
		// when the route matches; for 404s (no route) we use
		// the raw path. To prevent label cardinality explosion
		// we cap the raw-path label length.
		path := c.Route().Path
		if path == "" {
			path = "<unmatched>"
		}
		labels := metrics.Labels{
			"method": c.Method(),
			"path":   path,
			"status": statusClass(c.Response().StatusCode()),
		}
		b.HTTPRequests.Inc(labels)
		b.HTTPDuration.Observe(
			metrics.Labels{"method": c.Method(), "path": path},
			time.Since(start).Seconds(),
		)
		return err
	}
}

// statusClass returns the Prometheus-bucketed status class:
// "2xx" / "3xx" / "4xx" / "5xx". The reason for bucketing is
// the same as path: keep label cardinality bounded when status
// codes 200/201/204 are all "successes".
func statusClass(code int) string {
	switch {
	case code < 200:
		return "1xx"
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

func (s *Server) Start() error {
	// TLS routing mirrors ptagent's ptagent/server:
	//   - UseSSL=true + both cert and key readable → ListenTLS.
	//   - Anything else (UseSSL=false, missing files,
	//     permission errors) → plain HTTP with a stderr
	//     breadcrumb so the operator knows why their
	//     SSL_CRT env didn't take effect.
	//
	// We deliberately don't fail the boot on a missing cert
	// when UseSSL=true: the user might be in a transient
	// "I'm rotating the cert" state and the HTTP listener
	// is the safer fallback than refusing to serve.
	cert, key := s.tlsFiles()
	if s.cfg != nil && s.cfg.Server.UseSSL {
		if cert == "" || key == "" {
			fmt.Fprintf(os.Stderr, "tls: use_ssl=true but ssl_cert/ssl_key are empty — falling back to plain HTTP\n")
		} else if err := s.probeTLS(cert, key); err != nil {
			fmt.Fprintf(os.Stderr, "tls: %v — falling back to plain HTTP\n", err)
		} else {
			addr := fmt.Sprintf("%s:%d", s.bindHost(), s.port)
			fmt.Fprintf(os.Stderr, "tls: HTTPS enabled on %s (cert=%s)\n", addr, cert)
			return s.app.ListenTLS(addr, cert, key)
		}
	}
	return s.app.Listen(fmt.Sprintf("%s:%d", s.bindHost(), s.port))
}

// bindHost returns "0.0.0.0" by default; an empty host in
// config is treated as "all interfaces" (matches Fiber's
// own behaviour when given ":8081").
func (s *Server) bindHost() string {
	if s.cfg == nil || s.cfg.Server.Host == "" {
		return "0.0.0.0"
	}
	return s.cfg.Server.Host
}

// tlsFiles reads ssl_cert / ssl_key paths from the
// server config, returning empty strings when the
// config is absent (e.g. legacy callers that constructed
// Server without going through NewServer).
func (s *Server) tlsFiles() (cert, key string) {
	if s.cfg == nil {
		return "", ""
	}
	return s.cfg.Server.SSLCert, s.cfg.Server.SSLKey
}

// probeTLS validates that the cert + key files exist and
// are readable. Returning an error from here causes the
// Start path to fall through to plain HTTP instead of
// panicking inside ListenTLS.
func (s *Server) probeTLS(cert, key string) error {
	certInfo, err := os.Stat(cert)
	if err != nil {
		return fmt.Errorf("ssl_cert unreadable (%s): %w", cert, err)
	}
	if certInfo.Size() == 0 {
		return fmt.Errorf("ssl_cert is empty: %s", cert)
	}
	keyInfo, err := os.Stat(key)
	if err != nil {
		return fmt.Errorf("ssl_key unreadable (%s): %w", key, err)
	}
	if keyInfo.Size() == 0 {
		return fmt.Errorf("ssl_key is empty: %s", key)
	}
	// World-readable key is a CVSS 7.5 — warn loudly so the
	// operator notices before a vuln-scanner does. We don't
	// fail boot because CI / dev environments routinely
	// mount the cert dir with permissive perms and the
	// warning is the right nudge.
	if keyInfo.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "tls: WARNING — ssl_key %s is world-readable; chmod 600 recommended\n", key)
	}
	return nil
}

func (s *Server) Shutdown() error {
	return s.app.Shutdown()
}

func (s *Server) registerRoutes() {
	api := s.app.Group("/api/v1")

	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "service": "pentestswarm"})
	})

	api.Post("/campaigns", s.createCampaign)
	api.Get("/campaigns", s.listCampaigns)
	api.Get("/campaigns/:id", s.getCampaign)
	api.Post("/campaigns/:id/start", s.startCampaign)
	api.Post("/campaigns/:id/stop", s.stopCampaign)
	// P5+ 用户输入闭环 — operator guidance injection.
	// POST: inject a new message; GET: replay history on reconnect.
	api.Post("/campaigns/:id/input", s.putUserInput)
	api.Get("/campaigns/:id/user-inputs", s.listUserInputs)
	api.Get("/campaigns/:id/findings", s.getCampaignFindings)
	api.Get("/campaigns/:id/events", s.getCampaignEvents)       // HTTP polling
	api.Get("/campaigns/:id/ws", websocket.New(s.handleWebSocket)) // WebSocket

	api.Get("/models", s.listModels)
	api.Get("/stats", s.getStats)

	// Skills — community skill index from openclaw-sec-skills.
	// GET  /api/v1/skills         — list/search all skills
	// GET  /api/v1/skills/:name   — single skill detail
	// GET  /api/v1/skills/stats   — category stats
	api.Get("/skills", s.listSkills)
	api.Get("/skills/stats", s.getSkillsStats)
	api.Get("/skills/:name", s.getSkill)

	// Reports (P5+ 报告闭环) — route registration is deferred
	// to WithReportHandler so the handler's Campaigns / Findings /
	// Board callbacks point to the live server instance.
	//
	// Auth (P5+ 鉴权闭环) — registered via WithAuthHandler.
	// Session + tenant middleware only apply to auth routes.
	//
	// Prompts (P5+ 提示词编辑闭环) — registered via WithPromptsHandler.
	//
	// MCP (P5+ 工具注册闭环) — registered via WithMCPHandler.

	// P5+ 一体部署：serves the embedded Next.js bundle on
	// root-level paths (`/`, `/settings`, `/agents`, ...). The
	// route is mounted *last* so the API and observability
	// routes registered above take precedence — any path the
	// API claims (e.g. `/api/v1/health`) is never delegated to
	// the file server.
	//
	// When the binary was built without the web bundle (e.g.
	// dev `go build` on a fresh clone, or the slim API image
	// at deploy/docker/Dockerfile) we log and skip — the API
	// stays up.
	if h, err := webfs.FiberHandler(); err == nil {
		s.app.Use("/", h)
	} else {
		fmt.Printf("webfs: dashboard not embedded (%v) — API-only mode\n", err)
	}
}

// WithAuthHandler installs the auth handler before Start is called.
// It also registers the auth routes (login, logout, me, OAuth) and
// session/tenant middleware on the /api/v1 group. Must be called
// after NewServerWithObservability/NewServerWithBoard, before Start.
func (s *Server) WithAuthHandler(h *auth.Handler) *Server {
	s.authHandler = h
	api := s.app.Group("/api/v1")
	// Session middleware applies to every auth route but is a
	// no-op when no cookie is present.
	api.Use(h.SessionMiddleware())
	// P5+ 商业化 - tenant middleware 在 session 之后跑；
	// 读 sess.TenantID 查 store 把 *tenant.Tenant 塞进
	// c.UserContext()。MultiTenant=false 时是 no-op。
	api.Use(tenantMiddleware(s.multiTenant, s.tenantStore))
	h.Register(api)
	return s
}

// WithPromptsHandler installs the prompts handler before Start
// is called. The handler exposes the 35 PromptType CRUD
// endpoints under /api/v1/prompts. Construction is the
// caller's job — typically:
//
//	h := prompts.NewHandler(
//	    prompts.NewService(
//	        prompts.NewInMemoryStore(),
//	        prompts.NewEmbeddedDefaults(),
//	    ),
//	    nil,  // AlwaysAllowAuthorizer in dev
//	)
//	s.WithPromptsHandler(h)
func (s *Server) WithPromptsHandler(h *prompts.Handler) *Server {
	s.promptsHandler = h
	api := s.app.Group("/api/v1")
	h.Register(api)
	return s
}

// WithTenantStore installs the tenant store backing the
// multi-tenant middleware. When the store is nil and
// cfg.Commercial.MultiTenant is true, the tenant middleware
// will 500 on every session-bearing request — a deliberate
// "fail closed" stance that prevents accidentally running a
// multi-tenant deployment with a single shared tenant.
func (s *Server) WithTenantStore(store tenant.Store) *Server {
	s.tenantStore = store
	return s
}

// WithMCPHandler installs the MCP handler before
// Start is called. The handler exposes the MCP
// fan-out (Manager) over HTTP/Fiber:
//
//   - /api/v1/mcp/servers              list / status
//   - /api/v1/mcp/servers/:name        get one
//   - /api/v1/mcp/servers/:name/status enable/disable
//   - /api/v1/mcp/servers/:name/allow  per-tool allow-list
//   - /api/v1/mcp/tools                fan-out tool catalogue
//   - /api/v1/mcp/invoke               HTTP transport tool call
//   - /mcp                             raw JSON-RPC
//   - /mcp/sse (GET)                   SSE upgrade
//   - /mcp/sse (POST)                  inline JSON-RPC response
//
// The handler is transport-agnostic: a single
// Manager instance can be the same one the
// `pentestswarm mcp serve` CLI uses. Set the
// manager on the handler via construction:
//
//	mgr := mcp.NewManager()
//	mgr.Add(&mcp.ManagedServer{Name: "core", Server: coreServer})
//	h := mcp.NewHandler(mgr)        // or mcp.Handler{Manager: mgr}
//
//	s.WithMCPHandler(h)
func (s *Server) WithMCPHandler(h *mcp.Handler) *Server {
	s.mcpHandler = h
	h.Register(s.app)
	return s
}

// --- Handlers ---

func (s *Server) createCampaign(c *fiber.Ctx) error {
	var req CreateCampaignRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": apiError{Code: "BAD_REQUEST", Message: "Invalid request body: " + err.Error()}})
	}

	if req.Target == "" {
		return c.Status(400).JSON(fiber.Map{"error": apiError{Code: "BAD_REQUEST", Message: "target is required"}})
	}
	if len(req.Scope) == 0 {
		// Auto-derive scope from target: strip protocol / path,
		// keep the hostname (and port if present).
		req.Scope = deriveScope(req.Target)
	}

	if req.Objective == "" {
		req.Objective = "find all vulnerabilities"
	}
	if req.Mode == "" {
		req.Mode = "manual"
	}

	id := uuid.New()

	// Split scope entries into CIDRs and domains
	var allowedCIDRs, allowedDomains []string
	for _, s := range req.Scope {
		if strings.Contains(s, "/") {
			// Check if it's a CIDR (digits/dots/colons before the slash)
			before := s[:strings.Index(s, "/")]
			isCIDR := true
			for _, c := range before {
				if c != '.' && c != ':' && (c < '0' || c > '9') {
					isCIDR = false
					break
				}
			}
			if isCIDR {
				allowedCIDRs = append(allowedCIDRs, s)
			} else {
				allowedDomains = append(allowedDomains, s)
			}
		} else {
			allowedDomains = append(allowedDomains, s)
		}
	}

	campaign := pipeline.Campaign{
		ID:        id,
		Name:      fmt.Sprintf("scan-%s-%s", req.Target, time.Now().Format("20060102-150405")),
		Target:    req.Target,
		Objective: req.Objective,
		Status:    pipeline.StatusPlanned,
		Mode:      pipeline.CampaignMode(req.Mode),
		Scope: pipeline.ScopeDefinition{
			AllowedDomains: allowedDomains,
			AllowedCIDRs:   allowedCIDRs,
		},
		CreatedAt: time.Now(),
	}

	state := &CampaignState{Campaign: campaign}

	// Store credentials if provided via API fields
	if req.Username != "" || req.Password != "" {
		state.Credentials = &ParsedCredentials{
			Username: req.Username,
			Password: req.Password,
		}
	}

	s.campaigns.Store(id.String(), state)

	// P5+ 观测: bump campaign counter + state gauge so /metrics
	// reflects the new campaign immediately. The state gauge is
	// re-swept on every getStats call to prevent drift; here we
	// only Inc the counter (Set the gauge as a coarse hint).
	if s.metrics != nil {
		s.metrics.CampaignsCreated.Inc(nil)
		s.metrics.CampaignsByState.Inc(metrics.Labels{"state": string(pipeline.StatusPlanned)})
	}

	return c.Status(201).JSON(fiber.Map{
		"id":     id.String(),
		"name":   campaign.Name,
		"target": campaign.Target,
		"status": campaign.Status,
		"scope":  campaign.Scope,
	})
}

func (s *Server) listCampaigns(c *fiber.Ctx) error {
	var campaigns []fiber.Map
	s.campaigns.Range(func(key, value any) bool {
		state := value.(*CampaignState)
		campaigns = append(campaigns, fiber.Map{
			"id":         state.Campaign.ID.String(),
			"name":       state.Campaign.Name,
			"target":     state.Campaign.Target,
			"status":     state.Campaign.Status,
			"objective":  state.Campaign.Objective,
			"created_at": state.Campaign.CreatedAt,
			"findings":   len(state.Findings),
			"scope":      state.Campaign.Scope,
		})
		return true
	})

	if campaigns == nil {
		campaigns = []fiber.Map{}
	}

	return c.JSON(fiber.Map{"data": campaigns, "meta": fiber.Map{"total": len(campaigns)}})
}

func (s *Server) getCampaign(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}

	state := val.(*CampaignState)
	return c.JSON(fiber.Map{
		"id":         state.Campaign.ID.String(),
		"name":       state.Campaign.Name,
		"target":     state.Campaign.Target,
		"objective":  state.Campaign.Objective,
		"status":     state.Campaign.Status,
		"mode":       state.Campaign.Mode,
		"scope":      state.Campaign.Scope,
		"created_at": state.Campaign.CreatedAt,
		"started_at": state.Campaign.StartedAt,
		"findings":   len(state.Findings),
		"events":     len(state.Events),
	})
}

func (s *Server) startCampaign(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}

	state := val.(*CampaignState)
	if state.Campaign.Status != pipeline.StatusPlanned && state.Campaign.Status != pipeline.StatusFailed {
		return c.Status(409).JSON(fiber.Map{"error": apiError{Code: "CONFLICT", Message: "Campaign already started"}})
	}

	// Reset state for retry if campaign was previously failed
	if state.Campaign.Status == pipeline.StatusFailed {
		state.Events = nil
		state.Findings = nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	state.Cancel = cancel

	// Spawn the findings bridge if the server was constructed with a
	// blackboard. The bridge runs for the lifetime of the campaign
	// and republishes every blackboard.Finding to the WebSocket hub.
	if s.board != nil {
		campaignUUID, parseErr := uuid.Parse(id)
		if parseErr == nil {
			bridge := NewFindingsBridge(ctx, s.board, s.hub, campaignUUID, s.findingHooks...)
			s.bridgesMu.Lock()
			s.bridges[id] = bridge
			s.bridgesMu.Unlock()
		}
	}

	// Parse scope from campaign
	var scopeStrs []string
	for _, d := range state.Campaign.Scope.AllowedDomains {
		scopeStrs = append(scopeStrs, d)
	}
	for _, cidr := range state.Campaign.Scope.AllowedCIDRs {
		scopeStrs = append(scopeStrs, cidr)
	}
	if len(scopeStrs) == 0 {
		// Fallback: derive scope from target URL
		scopeStrs = deriveScope(state.Campaign.Target)
	}

	cc := engine.CampaignConfig{
		Target:     state.Campaign.Target,
		Scope:      scopeStrs,
		Objective:  state.Campaign.Objective,
		Mode:       string(state.Campaign.Mode),
		Format:     "md",
		OutputDir:  "./reports",
		CampaignID: id, // Pass the API campaign ID so events match
	}

	// Pass credentials from API fields or from target string parsing
	if state.Credentials != nil {
		cc.Credentials = &engine.ParsedCredentials{
			Username: state.Credentials.Username,
			Password: state.Credentials.Password,
		}
	}

	// Run campaign in background
	campaignIDStr := id
	go func() {
		err := s.runner.Run(ctx, cc, func(event pipeline.CampaignEvent) {
			state.Events = append(state.Events, event)

			// Track findings. The runner now includes the full
			// ClassifiedFinding in event.Data, so severity, CVSS, and
			// target all flow through to the dashboard / stats endpoint.
			if event.EventType == pipeline.EventFindingDiscovered {
				var f pipeline.ClassifiedFinding
				if len(event.Data) > 0 && json.Unmarshal(event.Data, &f) == nil {
					state.Findings = append(state.Findings, f)
					// P5+ 观测: bump finding counters by severity
					// so /metrics can power a "findings by
					// severity" Grafana panel without an extra
					// query against the API.
					if s.metrics != nil {
						s.metrics.FindingsEmitted.Inc(metrics.Labels{"severity": string(f.Severity)})
						s.metrics.FindingsBySeverity.Inc(metrics.Labels{"severity": string(f.Severity)})
					}
				} else {
					state.Findings = append(state.Findings, pipeline.ClassifiedFinding{
						Title: event.Detail,
					})
				}
			}

			// Track status changes
			if event.EventType == pipeline.EventStateChange {
				detail := strings.ToLower(event.Detail)
				if strings.Contains(detail, "complete") {
					state.Campaign.Status = pipeline.StatusComplete
					// P5+ 观测: state transition "running → complete".
					// The gauge drift is bounded: getStats
					// periodically re-sweeps the state gauge
					// from the source-of-truth (s.campaigns
					// map) so a missed transition self-heals
					// on the next scrape.
					if s.metrics != nil {
						s.metrics.CampaignsByState.Add(
							metrics.Labels{"state": "complete"},
							1,
						)
					}
				} else if strings.Contains(detail, "→") {
					// Track intermediate state transitions so the
					// dashboard shows real-time progress (recon,
					// classifying, planning, etc.) instead of
					// staying at "initializing" until completion.
					parts := strings.SplitN(event.Detail, " → ", 2)
					if len(parts) == 2 {
						newStatus := pipeline.CampaignStatus(strings.TrimSpace(parts[1]))
						// Only accept known status values
						switch newStatus {
						case pipeline.StatusRecon,
							pipeline.StatusClassifying,
							pipeline.StatusPlanning,
							pipeline.StatusExecuting,
							pipeline.StatusReporting:
							state.Campaign.Status = newStatus
						}
					}
				}
			}

			// Track errors — surface them as status changes so the
			// dashboard shows the campaign as "failed" instead of
			// leaving it stuck in "initializing" forever.
			// Only set "failed" for engine-level errors; agent-level
			// errors (e.g. recon analysis returning empty surface)
			// are non-fatal and the pipeline continues.
			if event.EventType == pipeline.EventError && event.AgentName == "engine" {
				state.Campaign.Status = pipeline.StatusFailed
			}

			// Publish to WebSocket subscribers
			s.hub.Publish(campaignIDStr, event)
		})

		// If Run() returns an error (e.g. LLM provider creation failed
		// because no API key is configured), mark the campaign as failed
		// so the dashboard doesn't show it stuck in "initializing".
		if err != nil {
			state.Campaign.Status = pipeline.StatusFailed
			s.hub.Publish(campaignIDStr, pipeline.CampaignEvent{
				ID:         uuid.New(),
				CampaignID: state.Campaign.ID,
				Timestamp:  time.Now(),
				EventType:  pipeline.EventError,
				AgentName:  "engine",
				Detail:     err.Error(),
			})
		}

		// Auto-generate a report via the reports service when the
		// campaign completes successfully. This makes the report
		// available through the /api/v1/reports API immediately,
		// without the user having to click "Generate Report".
		if err == nil && s.reportHandler != nil && s.reportHandler.Service != nil {
			cid := state.Campaign.ID
			snap := reports.SnapshotCampaign(state.Campaign)
			findings, _ := s.resolveCampaignFindings(context.Background(), cid)
			if findings == nil {
				findings = []reports.FindingSnapshot{}
			}
			rep, repErr := s.reportHandler.Service.GenerateForCampaign(context.Background(), reports.BuildInput{
				Campaign:    snap,
				Findings:    findings,
				GeneratedAt: time.Now(),
				Generator:   "auto-post-campaign",
			})
			if repErr == nil {
				s.hub.Publish(campaignIDStr, pipeline.CampaignEvent{
					ID:         uuid.New(),
					CampaignID: cid,
					Timestamp:  time.Now(),
					EventType:  pipeline.EventToolResult,
					AgentName:  "report",
					Detail:     fmt.Sprintf("Report auto-saved (id: %s, %d bytes)", rep.ID, rep.ByteSize),
				})
			}
		}
	}()

	now := time.Now()
	state.Campaign.Status = pipeline.StatusInitializing
	state.Campaign.StartedAt = &now

	return c.Status(202).JSON(fiber.Map{"status": "starting", "id": id})
}

func (s *Server) stopCampaign(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}

	state := val.(*CampaignState)
	if state.Cancel != nil {
		state.Cancel()
	}
	state.Campaign.Status = pipeline.StatusAborted

	// Tear down the findings bridge. Cancel() above should cause
	// run() to return via the parent ctx, but we Stop() defensively
	// so the bridge's bookkeeping is released synchronously.
	s.bridgesMu.Lock()
	bridge, ok := s.bridges[id]
	if ok {
		delete(s.bridges, id)
	}
	s.bridgesMu.Unlock()
	if bridge != nil {
		bridge.Stop()
	}

	return c.JSON(fiber.Map{"status": "stopped", "id": id})
}

// putUserInput injects an operator-issued message into a running
// campaign. P5+ 用户输入闭环.
//
// The message is appended to CampaignState.UserInputs (replay log)
// and broadcast via the WebSocket hub as a `kind: "user_input"`
// envelope so the dashboard can render it in the chat-style input
// dock and the xterm event stream in real time. The agent swarm can
// opt to subscribe to the blackboard to react to operator guidance;
// for now this is audit-only on the Go side, with the dashboard
// doing the human-visible rendering.
//
// Returns 201 Created with the canonical UserMessage (server-assigned
// ID + timestamp) on success. Validation errors:
//   - 404 NOT_FOUND      — campaign id is unknown
//   - 400 BAD_REQUEST    — empty body, or body > MaxUserInputLength
//   - 409 CONFLICT       — campaign has already finished / aborted
func (s *Server) putUserInput(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}
	state := val.(*CampaignState)

	// Refuse input to a finished campaign. We allow input during
	// planned/running/initializing — once a campaign is complete
	// or aborted the user shouldn't be able to retroactively inject
	// messages that would show up in the replay log as "during".
	if state.Campaign.Status == pipeline.StatusComplete ||
		state.Campaign.Status == pipeline.StatusFailed ||
		state.Campaign.Status == pipeline.StatusAborted {
		return c.Status(409).JSON(fiber.Map{
			"error": apiError{
				Code:    "CONFLICT",
				Message: "Campaign is " + string(state.Campaign.Status) + "; user input rejected",
			},
		})
	}

	var req PutUserInputRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": apiError{Code: "BAD_REQUEST", Message: "Invalid request body: " + err.Error()}})
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return c.Status(400).JSON(fiber.Map{"error": apiError{Code: "BAD_REQUEST", Message: "text is required"}})
	}
	if len(text) > MaxUserInputLength {
		return c.Status(400).JSON(fiber.Map{
			"error": apiError{
				Code:    "BAD_REQUEST",
				Message: fmt.Sprintf("text exceeds %d characters", MaxUserInputLength),
			},
		})
	}

	author := strings.TrimSpace(req.Author)
	if author == "" {
		// Optional: pull from session if authHandler is wired.
		// The session middleware stashes a *auth.User on c.Locals
		// under "user"; we read it best-effort without making the
		// auth package a hard dependency of this handler.
		if u, ok := c.Locals("user").(*struct{ Username string }); ok && u != nil && u.Username != "" {
			author = u.Username
		} else {
			author = "anonymous"
		}
	}

	campaignID, parseErr := uuid.Parse(id)
	if parseErr != nil {
		// Should not happen — the id came from URL routing and
		// createCampaign uses uuid.New — but defend against it.
		return c.Status(500).JSON(fiber.Map{"error": apiError{Code: "INTERNAL", Message: "campaign id is malformed"}})
	}

	um := ws.UserMessage{
		ID:         uuid.New(),
		CampaignID: campaignID,
		Author:     author,
		Text:       text,
		Timestamp:  time.Now().UTC(),
	}

	// Append to the replay log. Append-only — no dedup; the order
	// is "as received by the server". Concurrent posts serialize
	// through the per-campaign state mutex below.
	state.UserInputs = append(state.UserInputs, um)

	// Broadcast to all live subscribers. The hub's WSMessages
	// counter (by kind="user_input") already gives us a free
	// "messages per minute" signal via /metrics — no new counter
	// needed in appmetrics.All.
	s.hub.PublishUserInput(id, um)

	return c.Status(201).JSON(um)
}

// listUserInputs returns the operator-issued message log for a
// campaign, ordered oldest → newest. Used by the dashboard's input
// dock to replay history on reconnect (the WebSocket only carries
// the live tail).
func (s *Server) listUserInputs(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}
	state := val.(*CampaignState)
	// Coerce nil → [] so the dashboard can iterate without a
	// defensive guard. Without this, encoding/json renders a nil
	// slice as `null` and a naive `data.map(...)` on the client
	// would crash.
	inputs := state.UserInputs
	if inputs == nil {
		inputs = []ws.UserMessage{}
	}
	return c.JSON(fiber.Map{
		"data": inputs,
		"meta": fiber.Map{"total": len(inputs)},
	})
}

func (s *Server) getCampaignFindings(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}

	state := val.(*CampaignState)

	// When the server has a blackboard, the blackboard is the
	// source of truth for findings — the in-memory state.Findings
	// (built from legacy events) is only a denormalized view that's
	// not in sync with everything the agents wrote. Query the
	// blackboard for canonical results, falling back to the
	// in-memory slice if the query fails (e.g. blackboard not yet
	// bootstrapped in tests).
	if s.board != nil {
		campaignUUID, parseErr := uuid.Parse(id)
		if parseErr == nil {
			rows, qerr := s.board.Query(c.Context(), blackboard.Predicate{
				Limit: 500,
			})
			if qerr == nil {
				// Filter by campaign ID since the predicate doesn't
				// have a CampaignID field; the table is global.
				filtered := make([]blackboard.Finding, 0, len(rows))
				for _, f := range rows {
					if f.CampaignID == campaignUUID {
						filtered = append(filtered, f)
					}
				}
				return c.JSON(fiber.Map{
					"data": filtered,
					"meta": fiber.Map{"total": len(filtered), "source": "blackboard"},
				})
			}
		}
	}

	return c.JSON(fiber.Map{
		"data": state.Findings,
		"meta": fiber.Map{"total": len(state.Findings), "source": "memory"},
	})
}

func (s *Server) getCampaignEvents(c *fiber.Ctx) error {
	id := c.Params("id")
	val, ok := s.campaigns.Load(id)
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": apiError{Code: "NOT_FOUND", Message: "Campaign not found"}})
	}

	state := val.(*CampaignState)

	// Return last N events (default 200)
	events := state.Events
	if len(events) > 200 {
		events = events[len(events)-200:]
	}

	return c.JSON(fiber.Map{"data": events, "meta": fiber.Map{"total": len(state.Events)}})
}

func (s *Server) listModels(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"models": models.ModelRegistry()})
}

func (s *Server) getStats(c *fiber.Ctx) error {
	total := 0
	active := 0
	totalFindings := 0
	bySeverity := map[pipeline.Severity]int{
		pipeline.SeverityCritical: 0,
		pipeline.SeverityHigh:     0,
		pipeline.SeverityMedium:   0,
		pipeline.SeverityLow:      0,
		pipeline.SeverityInformational:     0,
	}
	byAgent := map[string]int{}
	// stateCounts is rebuilt from scratch on every getStats call,
	// then written into the psa_campaigns_by_state gauge. This
	// self-heals any drift the Inc/Dec path might accumulate
	// across restarts or missed transitions.
	stateCounts := map[string]int{}

	s.campaigns.Range(func(key, value any) bool {
		state := value.(*CampaignState)
		total++
		if state.Campaign.Status != pipeline.StatusComplete &&
			state.Campaign.Status != pipeline.StatusFailed &&
			state.Campaign.Status != pipeline.StatusAborted &&
			state.Campaign.Status != pipeline.StatusPlanned {
			active++
		}
		totalFindings += len(state.Findings)
		for _, f := range state.Findings {
			bySeverity[f.Severity]++
		}
		for _, e := range state.Events {
			if e.AgentName != "" {
				byAgent[e.AgentName]++
			}
		}
		stateCounts[string(state.Campaign.Status)]++
		return true
	})

	// P5+ 观测: re-sweep the by-state gauge from the source of
	// truth. We always Set (not Inc/Dec) so the gauge is
	// self-healing — drops the previous value and rewrites the
	// current snapshot. The cost is one Set per state bucket
	// per scrape, which is negligible (≤ 6 buckets).
	if s.metrics != nil {
		for stateName, count := range stateCounts {
			s.metrics.CampaignsByState.Set(
				metrics.Labels{"state": stateName},
				float64(count),
			)
		}
	}

	return c.JSON(fiber.Map{
		"campaigns":        total,
		"active_campaigns": active,
		"total_findings":   totalFindings,
		"by_severity": fiber.Map{
			"critical": bySeverity[pipeline.SeverityCritical],
			"high":     bySeverity[pipeline.SeverityHigh],
			"medium":   bySeverity[pipeline.SeverityMedium],
			"low":      bySeverity[pipeline.SeverityLow],
			"info":     bySeverity[pipeline.SeverityInformational],
		},
		"by_agent": byAgent,
	})
}

// --- Skills ---

// listSkills returns the full skill catalogue, optionally filtered by
// category (?category=pentest) or search query (?q=sql).
func (s *Server) listSkills(c *fiber.Ctx) error {
	reg := skills.Global()
	catFilter := skills.ParseCategory(c.Query("category", ""))
	search := strings.TrimSpace(c.Query("q", ""))

	var list []skills.Skill
	if search != "" {
		list = reg.Search(search)
	} else if catFilter != "" {
		list = reg.ByCategory(catFilter)
	} else {
		list = reg.All()
	}

	// Apply category filter on top of search results when both params given.
	if catFilter != "" && search != "" {
		filtered := make([]skills.Skill, 0, len(list))
		for _, sk := range list {
			if sk.Category == catFilter {
				filtered = append(filtered, sk)
			}
		}
		list = filtered
	}

	if list == nil {
		list = []skills.Skill{}
	}

	return c.JSON(fiber.Map{
		"data": list,
		"meta": fiber.Map{
			"total":    len(list),
			"registry": reg.Len(),
		},
	})
}

// getSkillsStats returns per-category skill counts.
func (s *Server) getSkillsStats(c *fiber.Ctx) error {
	reg := skills.Global()
	stats := reg.CategoryStats()
	cats := make([]fiber.Map, 0, len(stats))
	for _, cat := range skills.AllCategories() {
		if n, ok := stats[cat]; ok && n > 0 {
			cats = append(cats, fiber.Map{
				"category":     string(cat),
				"display_name": cat.DisplayName(),
				"emoji":        cat.Emoji(),
				"count":        n,
			})
		}
	}
	return c.JSON(fiber.Map{
		"total":      reg.Len(),
		"categories": cats,
	})
}

// getSkill returns a single skill by its name (case-insensitive).
func (s *Server) getSkill(c *fiber.Ctx) error {
	name := c.Params("name")
	reg := skills.Global()
	sk := reg.ByName(name)
	if sk == nil {
		return c.Status(404).JSON(fiber.Map{
			"error": apiError{Code: "NOT_FOUND", Message: "Skill not found: " + name},
		})
	}
	owner, repo := sk.OwnerRepo()
	return c.JSON(fiber.Map{
		"name":        sk.Name,
		"description": sk.Description,
		"category":    string(sk.Category),
		"source":      sk.Source,
		"source_type": sk.SourceType,
		"source_label": sk.SourceLabel,
		"tags":        sk.Tags(),
		"owner":       owner,
		"repo":        repo,
	})
}

// --- WebSocket ---

func (s *Server) handleWebSocket(c *websocket.Conn) {
	campaignID := c.Params("id")

	s.hub.Subscribe(campaignID, c.Conn)
	defer s.hub.Unsubscribe(campaignID, c.Conn)

	// Send initial snapshot: last 50 events for this campaign so the
	// client doesn't miss events between the REST fetch and the WS
	// subscribe.
	if val, ok := s.campaigns.Load(campaignID); ok {
		state := val.(*CampaignState)
		events := state.Events
		snapshotLen := 50
		if len(events) < snapshotLen {
			snapshotLen = len(events)
		}
		if snapshotLen > 0 {
			snapshot := events[len(events)-snapshotLen:]
			msg := ws.Message{Kind: ws.KindSnapshot, Events: snapshot}
			data, err := json.Marshal(msg)
			if err == nil {
				s.hub.WriteMessage(c.Conn, websocket.TextMessage, data)
			}
		}
	}

	// Ping/pong heartbeat: send a ping every 30s. If the client
	// doesn't respond with a pong within 10s, close the connection.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.hub.WriteMessage(c.Conn, websocket.PingMessage, nil)
			case <-done:
				return
			}
		}
	}()

	// Set read deadline based on pong responses.
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Keep connection alive — read messages (client can send "ping")
	for {
		_, _, err := c.ReadMessage()
		if err != nil {
			break
		}
	}
	close(done)
}

// --- Error Handling ---

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}

	return c.Status(code).JSON(fiber.Map{
		"error": apiError{
			Code:    fmt.Sprintf("%d", code),
			Message: err.Error(),
		},
	})
}
