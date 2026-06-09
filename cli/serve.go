package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/api"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/auth"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/db"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/engine"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/mcp"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/appmetrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/langfuse"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/prompts"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/reports"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tenant"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the API server and web dashboard",
	Example: `  pentestswarm serve
  pentestswarm serve --port 9090`,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")

		cfg, err := config.Load(cfgFile)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		// Check env for API key
		if cfg.Orchestrator.APIKey == "" {
			if key := os.Getenv("PENTESTSWARM_ORCHESTRATOR_API_KEY"); key != "" {
				cfg.Orchestrator.APIKey = key
			} else if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
				cfg.Orchestrator.APIKey = key
			}
		}

		// P5+ 一体部署：启动期强制校验必需 env，让
		// `docker compose up` 看到 binary 立即退出而非挂着
		// 等 healthcheck 60s 失败。
		//
		// 为什么放在加载配置之后：cfg.Orchestrator.APIKey
		// 可能从 config.yaml 或 PENTESTSWARM_* / ANTHROPIC_*
		// env 来；先 resolve 完再校验才能给出精确错误。
		//
		// 跳过条件：PENTESTSWARM_SKIP_REQUIRED=1 用于离线
		// 演示场景（例如有 LICENSE_KEY 的 OSS 版无 LLM
		// 即可启动）。
		if os.Getenv("PENTESTSWARM_SKIP_REQUIRED") != "1" {
			var missing []string
			if cfg.Orchestrator.Provider != "" && cfg.Orchestrator.APIKey == "" {
				missing = append(missing, "ORCHESTRATOR_API_KEY (and PENTESTSWARM_ORCHESTRATOR_API_KEY / ANTHROPIC_API_KEY fallbacks)")
			}
			if cfg.Database.Host != "" && cfg.Database.Password == "" {
				missing = append(missing, "POSTGRES_PASSWORD (set database.password in config.yaml or PENTESTSWARM_DATABASE_PASSWORD)")
			}
			if len(missing) > 0 {
				return fmt.Errorf("required environment variables are missing:\n  - %s\n\nSet them in .env (cp .env.example .env) or pass them via -e flags.\nOr set PENTESTSWARM_SKIP_REQUIRED=1 to skip this check (demo mode).", strings.Join(missing, "\n  - "))
			}
		}

		// LangFuse observability — opt-in via env. When the keys
		// are present we build a Client + FindingObserver and wire
		// the observer as a FindingHook on the API server. We also
		// pass a ProviderDecorator to the runner so the LLM is
		// wrapped with ObservingProvider and every Complete/Stream
		// call emits a generation event.
		//
		// When the keys are absent the Client/Observer are nil and
		// their methods are no-ops, so we never pay for what we
		// don't use.
		var lfClient *langfuse.Client
		var lfObserver *langfuse.FindingObserver
		var decorator func(llm.Provider) llm.Provider
		if lfCfg, ok := langfuse.ConfigFromEnvEnabled(); ok {
			lfClient = langfuse.NewClient(lfCfg)
			lfObserver = langfuse.NewFindingObserver(lfClient)
			decorator = func(p llm.Provider) llm.Provider {
				return langfuse.NewObservingProvider(p, lfClient)
			}
			fmt.Fprintf(os.Stderr, "langfuse: tracing enabled (endpoint=%s)\n", lfCfg.Endpoint)
		} else {
			fmt.Fprintln(os.Stderr, "langfuse: tracing disabled (set LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY to enable)")
		}

		// Always close the LangFuse client on shutdown so any
		// in-flight events flush. We install a signal handler
		// below; the defer here covers the early-exit paths.
		defer func() {
			if lfClient != nil {
				lfClient.Close()
			}
		}()

		fmt.Println(colorCyan(`
  ██████  ██     ██  █████  ██████  ███    ███
  ██      ██     ██ ██   ██ ██   ██ ████  ████
  ███████ ██  █  ██ ███████ ██████  ██ ████ ██
       ██ ██ ███ ██ ██   ██ ██   ██ ██  ██  ██
  ███████  ███ ███  ██   ██ ██   ██ ██      ██`))
	scheme := "http"
	if cfg.Server.UseSSL {
		scheme = "https"
	}
	fmt.Printf("\n  API Server:  %s://localhost:%d/api/v1\n", scheme, port)
	fmt.Printf("  Health:      %s://localhost:%d/api/v1/health\n", scheme, port)
	fmt.Printf("  Metrics:     %s://localhost:%d/metrics\n", scheme, port)
	fmt.Printf("  Provider:    %s\n", cfg.Orchestrator.Provider)
	if cfg.Server.UseSSL {
		fmt.Printf("  TLS:         cert=%s key=%s\n", cfg.Server.SSLCert, cfg.Server.SSLKey)
	}
	fmt.Println()

		// P5+ 观测: build the observability stack ONCE and share
		// it between the API server (HTTP + WebSocket hooks) and
		// the engine runner (LLM provider decorator). This is the
		// single registry that backs /metrics, /healthz, /readyz
		// AND every LLM-call counter, so all metrics live in one
		// Prometheus scrape output.
		obsReg := metrics.NewRegistry()
		obsBundle := appmetrics.New(obsReg)
		obsHandler := observability.NewHandler(obsReg)
		obsHandler.ReadinessCheck = func() error {
			if cfg == nil || cfg.Orchestrator.Provider == "" {
				return fmt.Errorf("orchestrator provider not configured")
			}
			return nil
		}

		// Build the runner decorator chain in the right order:
		//  1. langfuse (optional) — emits generation traces
		//  2. metrics — records psa_llm_calls_total / latency
		// The metrics decorator wraps the langfuse one so its
		// latency histogram captures the full observed call time
		// (including langfuse event emission overhead).
		engineOpts := []engine.Option{
			engine.WithLLMProviderDecorator(decorator),
			engine.WithLLMMetrics(obsBundle),
		}
		server := api.NewServerWithObservability(port, cfg, nil, obsReg, obsBundle, obsHandler, engineOpts...)
		if lfObserver != nil {
			server.WithFindingHook(lfObserver.OnFinding)
		}

		// P5+ 报告闭环：装载报告服务（Markdown 生成 + 持久化）。
		// 当前 store 是 in-memory（无 PostgreSQL 时也可工作），如果
		// 启用了数据库，后续可在 cfg 中暴露 pool 句柄以切到
		// reports.NewStore(pool)。
		reportSvc := reports.NewService(reports.NewMemoryStore(), reports.NewBuilder())
		reportHandler := reports.NewHandler(reportSvc, "cli-serve")
		server.WithReportHandler(reportHandler)

		// P5+ 鉴权闭环：装载 auth service（password + OAuth 2.0）。
		// OAuth providers 通过环境变量 PS_AUTH_OAUTH_GOOGLE_CLIENT_ID /
		// PS_AUTH_OAUTH_GITHUB_CLIENT_ID 自动注册。Cookie secure flag
		// 跟随 PUBLIC_BASE_URL 的 scheme。
		authSvc := auth.NewService()
		authBaseURL := os.Getenv("PSA_PUBLIC_BASE_URL")
		if authBaseURL == "" {
			authBaseURL = fmt.Sprintf("http://localhost:%d", port)
		}
		cookieSecure := strings.HasPrefix(strings.ToLower(authBaseURL), "https://")
		authHandler := auth.NewHandler(authSvc, authBaseURL, cookieSecure)
		if gID := os.Getenv("PS_AUTH_OAUTH_GOOGLE_CLIENT_ID"); gID != "" {
			authSvc.Providers.Register(auth.NewGoogleProvider(
				gID,
				os.Getenv("PS_AUTH_OAUTH_GOOGLE_CLIENT_SECRET"),
			))
		}
		if ghID := os.Getenv("PS_AUTH_OAUTH_GITHUB_CLIENT_ID"); ghID != "" {
			authSvc.Providers.Register(auth.NewGitHubProvider(
				ghID,
				os.Getenv("PS_AUTH_OAUTH_GITHUB_CLIENT_SECRET"),
			))
		}
		server.WithAuthHandler(authHandler)
		defer authSvc.Stop()

		// P5+ 提示词编辑闭环：装载 35 PromptType 的 CRUD
		// handler。Store 是 in-memory（与 P5+ 鉴权同模式，
		// Postgres schema 已在 P5+ 商业化阶段预留）；Defaults
		// 从 internal/agent/prompts/templates/ 嵌入读取。
		promptsStore := prompts.NewInMemoryStore()
		promptsSvc := prompts.NewService(promptsStore, prompts.NewEmbeddedDefaults())
		promptsHandler := prompts.NewHandler(promptsSvc, nil)
		server.WithPromptsHandler(promptsHandler)

		// P5+ MCP 工具注册闭环：创建 MCP Manager，注册默认
		// 核心服务器（12 tools, 3 resources, 2 prompts），
		// 挂载到 API server 的 /api/v1/mcp/* 路由。
		mcpMgr := mcp.NewManager()
		mcpSrv := mcp.NewServer(mcp.ServerInfo{
			Name:    "pentestswarm",
			Version: "1.0.0",
		})
		mcp.RegisterDefaultTools(mcpSrv, mcp.ToolDeps{Config: cfg})
		mcp.RegisterDefaultResources(mcpSrv, mcp.ToolDeps{Config: cfg})
		mcp.RegisterDefaultPrompts(mcpSrv, mcp.ToolDeps{Config: cfg})
		mcpMgr.Add(&mcp.ManagedServer{
			Name:        "pentestswarm",
			Description: "Core pentestswarm tool surface (12 tools, 3 resources, 2 prompts).",
			Server:      mcpSrv,
			Status:      mcp.ServerStatusEnabled,
		})
		mcpHandler := &mcp.Handler{Manager: mcpMgr}
		server.WithMCPHandler(mcpHandler)

		// P5+ 商业化 - License 启动校验。空 license = demo
	// build（ptagent 行为兼容）；非空则 Ed25519 验签 +
	// 过期检查 + installation id 绑定检查。失败时返回
	// error 让 cmd 退出码非零，避免在错误 license 下启动
	// 写入数据。
	if err := bootstrapLicense(cfg.Commercial, os.Stderr); err != nil {
		return fmt.Errorf("license: %w", err)
	}

	// P5+ 一体部署：自动应用 SQL 迁移。这是 idempotent
	// 的：db.Migrate 用 schema_migrations 表跳过已应用的
	// 文件，所以并发启动多个副本是安全的（虽然只有一个
	// 会先抢到 advisory lock）。失败时返回 error 让
	// docker-compose 的 healthcheck 一直处于 unhealthy，
	// 阻止依赖该服务的滚动更新。
	//
	// 想跳过这一步（例如生产里 DBA 单独管理 schema），
	// 设置 PENTESTSWARM_SKIP_AUTO_MIGRATE=1。
	if os.Getenv("PENTESTSWARM_SKIP_AUTO_MIGRATE") != "1" {
		dsn := cfg.Database.DSN()
		if dsn != "" {
			mctx, mcancel := context.WithTimeout(context.Background(), 60*time.Second)
			if pool, err := db.Connect(mctx, dsn); err == nil {
				if mErr := db.Migrate(mctx, pool); mErr != nil {
					pool.Close()
					mcancel()
					return fmt.Errorf("auto-migrate: %w", mErr)
				}
				pool.Close()
				fmt.Fprintln(os.Stderr, "migrate: schema is up to date")
			} else {
				fmt.Fprintf(os.Stderr, "migrate: skipped (%v)\n", err)
			}
			mcancel()
		}
	}

		// P5+ 商业化 - 多租户：构造 in-memory tenant store。
		// Postgres 版的 Store 在 P5+ 一体化部署阶段实现；
		// 当前 boot 路径用 InMemoryStore 把 default 租户
		// 种下，让单租户 demo 路径仍然可用。
		tenants := tenant.NewInMemoryStore()
		if err := tenants.Create(context.Background(), &tenant.Tenant{
			ID:   tenant.DefaultTenantID,
			Name: "default",
			Plan: "team",
		}); err != nil && !strings.Contains(err.Error(), "name already in use") {
			return fmt.Errorf("seed default tenant: %w", err)
		}
		if cfg.Commercial.MultiTenant {
			server.WithTenantStore(tenants)
			fmt.Fprintf(os.Stderr, "tenants: multi-tenant mode enabled (in-memory store)\n")
		}

		// P5+ 嵌入异步化：构造 Redis+Async+LRU 三层 embedder
		// 链。每一层都通过 Enabled kill-switch 关闭；只要
		// 任意一层被启用，就能在不破坏现有 PostgresBoard 写入
		// 路径的前提下，让 finding 的 embedding 列异步填入。
		// 三个 Close 在 shutdown 时倒序释放（Redis → Async → 内
		// 存），defer 顺序保证 LIFO。
		embedder, err := llm.NewEmbedderFromConfig(llm.EmbedderConfig{
			Provider:   cfg.LLM.Embeddings.Provider,
			APIKey:     cfg.LLM.Embeddings.APIKey,
			Endpoint:   cfg.LLM.Embeddings.Endpoint,
			Model:      cfg.LLM.Embeddings.Model,
			Dimensions: cfg.LLM.Embeddings.Dimensions,
			BatchSize:  cfg.LLM.Embeddings.BatchSize,
			CacheSize:  effectiveCacheSize(cfg.LLM.Embeddings.Cache),
			Async: llm.AsyncConfig{
				Enabled:      cfg.LLM.Embeddings.Async.Enabled,
				Workers:      cfg.LLM.Embeddings.Async.Workers,
				BatchSize:    cfg.LLM.Embeddings.Async.BatchSize,
				BatchTimeout: cfg.LLM.Embeddings.Async.BatchTimeout,
				QueueSize:    cfg.LLM.Embeddings.Async.QueueSize,
				SubmitWait:   cfg.LLM.Embeddings.Async.SubmitWait,
			},
			Redis: llm.RedisConfig{
				Enabled:  cfg.LLM.Embeddings.Redis.Enabled,
				Addr:     cfg.LLM.Embeddings.Redis.Addr,
				Password: cfg.LLM.Embeddings.Redis.Password,
				DB:       cfg.LLM.Embeddings.Redis.DB,
				TTL:      cfg.LLM.Embeddings.Redis.TTL,
				Prefix:   cfg.LLM.Embeddings.Redis.Prefix,
				Timeout:  cfg.LLM.Embeddings.Redis.Timeout,
			},
		})
		if err != nil {
			return fmt.Errorf("building embedder: %w", err)
		}
		if asyncE, ok := embedder.(*llm.AsyncEmbedder); ok {
			defer func() { _ = asyncE.Close() }()
		}
		if redisE, ok := extractRedisEmbedder(embedder); ok {
			defer func() { _ = redisE.Close() }()
			fmt.Fprintf(os.Stderr, "embedder: redis cache enabled (addr=%s, ttl=%s)\n", cfg.LLM.Embeddings.Redis.Addr, cfg.LLM.Embeddings.Redis.TTL)
		}
		if cfg.LLM.Embeddings.Async.Enabled {
			fmt.Fprintf(os.Stderr, "embedder: async worker pool enabled (workers=%d, batch=%d, timeout=%s)\n",
				cfg.LLM.Embeddings.Async.Workers, cfg.LLM.Embeddings.Async.BatchSize, cfg.LLM.Embeddings.Async.BatchTimeout)
		}
		if cfg.LLM.Embeddings.Cache.Enabled {
			fmt.Fprintf(os.Stderr, "embedder: in-memory LRU enabled (size=%d)\n", cfg.LLM.Embeddings.Cache.Size)
		}
		_ = embedder // wired through the future WithEmbedder(server, embedder) hook

		// Translate SIGINT/SIGTERM into a clean shutdown. We do
		// not start a long-running goroutine for this — the Fiber
		// Shutdown call we issue from the signal handler closes
		// the listening socket, after which the in-flight LangFuse
		// events have time to flush via the defer above.
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		go func() {
			<-ctx.Done()
			_ = server.Shutdown()
		}()

		// Port conflict detection: probe the port before listening.
		// If another process already holds the socket, fail fast
		// with a clear error message instead of timing out.
		if err := checkPortAvailable(port); err != nil {
			return fmt.Errorf("port %d is unavailable: %w", port, err)
		}

		return server.Start()
	},
}

func init() {
	serveCmd.Flags().Int("port", 8080, "server port")
	rootCmd.AddCommand(serveCmd)
}

// effectiveCacheSize maps the on-the-wire cache config to the
// llm factory's int parameter. The factory uses 0 to mean
// "disabled", so we just forward Size when Enabled and return
// 0 otherwise. Keeping the conversion in one place means the
// call site in serveCmd can pass a config struct without a
// second boolean it has to keep in sync.
func effectiveCacheSize(cfg config.EmbeddingCacheConfig) int {
	if !cfg.Enabled {
		return 0
	}
	return cfg.Size
}

// extractRedisEmbedder peeks through a chain of Embedder
// wrappers to find the outermost RedisEmbedder, if any. The
// return value is suitable for a defer-close in serveCmd so
// the Redis client's lifetime matches the embedder's. We
// accept the chain rather than just the topmost type because
// the factory may in the future wrap redis→async→cached→inner,
// and the shutdown order then matters: we want to Close the
// Redis client AFTER any worker that may still be in the middle
// of a pipelined SET.
func extractRedisEmbedder(e llm.Embedder) (*llm.RedisEmbedder, bool) {
	// We don't have an Unwrap() interface; the factory's
	// contract puts Redis at the top of the chain today, so
	// the type assertion is sufficient. If that contract
	// changes the assertion just returns false and the
	// shutdown simply skips the Redis close — the OS will
	// reclaim the socket on exit.
	r, ok := e.(*llm.RedisEmbedder)
	return r, ok
}

// checkPortAvailable probes a TCP port to see if it is already
// bound by another process. Returns nil if the port is free, or
// an error describing the conflict if it is in use.
//
// The check opens a temporary listener and immediately closes it;
// it does not hold the socket. A race between the check and the
// actual Listen() call is possible but vanishingly unlikely on a
// single-user dev machine.
func checkPortAvailable(port int) error {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Try to identify the culprit process for a better
		// error message.
		return fmt.Errorf("port %d is already in use (run `ss -tlnp | grep :%d` to see which process)", port, port)
	}
	_ = ln.Close()
	return nil
}
