package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server       ServerConfig       `mapstructure:"server"`
	Database     DatabaseConfig     `mapstructure:"database"`
	Redis        RedisConfig        `mapstructure:"redis"`
	Orchestrator OrchestratorConfig `mapstructure:"orchestrator"`
	Agents       AgentsConfig       `mapstructure:"agents"`
	Tools        ToolsConfig        `mapstructure:"tools"`
	Scope        ScopeConfig        `mapstructure:"scope"`
	Monitor      MonitorConfig      `mapstructure:"monitor"`
	ASM          ASMConfig          `mapstructure:"asm"`
	BugBounty    BugBountyConfig    `mapstructure:"bugbounty"`
	Intelligence IntelligenceConfig `mapstructure:"intelligence"`
	Integrations IntegrationsConfig `mapstructure:"integrations"`
	Logging      LoggingConfig      `mapstructure:"logging"`

	// Commercial wires the P5+ 商业化议程:
	//   - LicenseKey  : Ed25519-signed license blob
	//   - CORS.Origins: explicit allow-list (replaces legacy "*")
	//   - MultiTenant : enables tenant_id column + Postgres RLS
	// All sub-fields default to "off" so the OSS / demo
	// experience is byte-for-byte unchanged.
	Commercial CommercialConfig `mapstructure:"commercial"`

	// LLM wraps the three pieces of language-model plumbing that
	// previously lived as scattered fields on OrchestratorConfig and
	// inside the LLM package's hard-coded defaults:
	//   - Embeddings: which embedder to use for finding vectors, with
	//     cache + batch + retry knobs.
	//   - Summarizer: the parameterised conversation-history
	//     compressor (replaces the hard-coded 0.7/0.8 thresholds).
	// Zero values fall back to the LLM package's own defaults, so
	// deployments that don't touch this block continue to work.
	LLM LLMConfig `mapstructure:"llm"`
}

// LLMConfig groups every language-model-side configuration knob that
// previously lived as hard-coded constants inside internal/llm.
// Fields are deliberately split so the cmd/ startup path can build
// each piece (provider, embedder, summariser) independently.
type LLMConfig struct {
	// Embeddings configures the vector embedder used for finding
	// embeddings and similarity search.
	Embeddings EmbeddingsConfig `mapstructure:"embeddings"`
	// Summarizer configures the conversation-history compressor.
	// Wired into the existing BudgetManager and agent loops at
	// startup; agents that don't set it get the BudgetManager's
	// hard-coded behaviour.
	Summarizer SummarizerConfig `mapstructure:"summarizer"`
}

// EmbeddingsConfig is the wire shape for the
// `llm.embeddings:` block of config.yaml. Mirrors
// internal/llm.EmbedderConfig and the lower-level
// OpenAIEmbedderConfig / OllamaEmbedderConfig.
//
// Provider dispatches to the factory; the remaining fields are
// only consulted by their respective implementations.
type EmbeddingsConfig struct {
	// Provider is the dispatch key. Supported values: openai,
	// deepseek, glm, kimi, qwen, ollama, noop, local_stub.
	Provider string `mapstructure:"provider"`
	// Model is the embedding model name. Empty → provider default.
	Model string `mapstructure:"model"`
	// Dimensions is the vector width. 0 → provider default.
	Dimensions int `mapstructure:"dimensions"`
	// Endpoint is the base URL (no trailing path). Empty → provider
	// default. Note: DeepSeek / GLM / Kimi / Qwen do not all serve
	// embeddings; pick a provider whose endpoint actually returns
	// them (typically OpenAI itself or a custom proxy).
	Endpoint string `mapstructure:"endpoint"`
	// APIKey is the bearer token. Empty → read from
	// $EMBEDDING_API_KEY (or whatever env var Viper is configured to
	// honour for this block).
	APIKey string `mapstructure:"api_key"`
	// BatchSize is the maximum number of texts per upstream
	// request. 0 → provider default (OpenAI: 100; Ollama: 1).
	BatchSize int `mapstructure:"batch_size"`
	// Timeout is the per-request timeout. 0 → 30s.
	Timeout time.Duration `mapstructure:"timeout"`
	// Cache configures the in-memory LRU wrapper. Disabled by
	// default to keep the cold path simple.
	Cache EmbeddingCacheConfig `mapstructure:"cache"`
	// Async configures the worker-pool wrapper. Disabled by
	// default; turn on for high-throughput batch pipelines.
	Async EmbeddingAsyncConfig `mapstructure:"async"`
	// Redis configures the cross-process cache. Disabled by
	// default; turn on for multi-instance deployments where
	// the in-memory LRU misses on every restart.
	Redis EmbeddingRedisConfig `mapstructure:"redis"`
}

// EmbeddingCacheConfig configures the CachedEmbedder wrapper.
type EmbeddingCacheConfig struct {
	Enabled bool           `mapstructure:"enabled"`
	Size    int            `mapstructure:"size"`
	TTL     time.Duration  `mapstructure:"ttl"`
}

// EmbeddingAsyncConfig configures the AsyncEmbedder worker pool
// wrapper. The async wrapper collapses N concurrent Embed calls
// into M <= N upstream calls and lets hot paths (blackboard
// Write, classifier batch scoring, …) return to the caller as
// soon as the job is queued, not when the network round-trip
// finishes. See internal/llm/embeddings_async.go for details.
type EmbeddingAsyncConfig struct {
	// Enabled is the kill-switch. When false, the factory skips
	// the async wrapper entirely.
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of goroutines consuming the queue.
	// 0 → 4.
	Workers int `mapstructure:"workers"`
	// BatchSize is the maximum number of texts a single
	// upstream call can carry. 0 → 32. The async wrapper will
	// NOT split a single submit across multiple upstream
	// calls; it only merges across submits.
	BatchSize int `mapstructure:"batch_size"`
	// BatchTimeout is the wait window for opportunistic
	// merging. 0 → 50ms. Set to a value smaller than the
	// round-trip latency for the underlying embedder to
	// maximise batching without inflating tail latency.
	BatchTimeout time.Duration `mapstructure:"batch_timeout"`
	// QueueSize is the bounded-buffer length. 0 → 1024. Submit
	// returns an error when the queue is full so the caller
	// can apply backpressure rather than build up memory.
	QueueSize int `mapstructure:"queue_size"`
	// SubmitWait is the max time Submit blocks when the queue
	// is full. 0 → wait forever.
	SubmitWait time.Duration `mapstructure:"submit_wait"`
}

// EmbeddingRedisConfig configures the RedisEmbedder cache layer.
// The cache sits in front of the in-memory LRU and the inner
// embedder; keys are namespaced by model so two deployments
// sharing a Redis don't collide. See
// internal/llm/embeddings_redis.go.
type EmbeddingRedisConfig struct {
	// Enabled is the kill-switch. When false, the factory skips
	// the Redis wrapper.
	Enabled bool `mapstructure:"enabled"`
	// Addr is the Redis host:port. Required when Enabled.
	Addr string `mapstructure:"addr"`
	// Password is the AUTH token. Empty → no AUTH.
	Password string `mapstructure:"password"`
	// DB is the logical database number. 0 → default.
	DB int `mapstructure:"db"`
	// TTL is the per-entry lifetime. 0 → no expiry.
	TTL time.Duration `mapstructure:"ttl"`
	// Prefix namespaces the keyspace. Default → "psa:embed:".
	Prefix string `mapstructure:"prefix"`
	// Timeout is the per-call timeout. 0 → 500ms. A cache call
	// should never take longer than the user's patience —
	// better to fall through than to wedge.
	Timeout time.Duration `mapstructure:"timeout"`
}

// SummarizerConfig mirrors internal/llm.SummarizerConfig. Kept as
// a separate type here so that the config package does not need
// to import internal/llm (and vice-versa) — the cmd/ startup path
// copies field-by-field when constructing the actual Summarizer.
type SummarizerConfig struct {
	Model                 string   `mapstructure:"model"`
	MaxTokens             int      `mapstructure:"max_tokens"`
	Temperature           float64  `mapstructure:"temperature"`
	TriggerAt             float64  `mapstructure:"trigger_at"`
	WarningAt             float64  `mapstructure:"warning_at"`
	KeepRecentRatio       float64  `mapstructure:"keep_recent_ratio"`
	MinMessagesToSummarize int     `mapstructure:"min_messages_to_summarize"`
	PreserveSystemPrompt  bool     `mapstructure:"preserve_system_prompt"`
	CustomSystemPrompt    string   `mapstructure:"custom_system_prompt"`
	CustomUserPromptTpl   string   `mapstructure:"custom_user_prompt_tpl"`
}

// MonitorConfig configures the swarm's execution monitor (3-layer
// tool-call guard). Zero values disable the corresponding cap; the
// monitor is opt-in hardening, not a blanket throttle. Inspired by
// ptagent's EXECUTION_MONITOR_* family.
type MonitorConfig struct {
	Enabled           bool   `mapstructure:"enabled"`
	MaxTotalPerAgent  int    `mapstructure:"max_total_per_agent"`
	MaxSameToolStreak int    `mapstructure:"max_same_tool_streak"`
	MaxPerWindow      int    `mapstructure:"max_per_window"`
	WindowSeconds     int    `mapstructure:"window_seconds"`
}

type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`

	// SSLCert / SSLKey / UseSSL — TLS termination.
	//
	// When UseSSL is true AND both SSLCert and SSLKey point at
	// readable PEM files, the API server starts with `app.ListenTLS`
	// instead of `app.Listen`. Empty / missing files fall through
	// to plain HTTP with a one-line warning, so an operator who
	// ships only the cert (not the key) doesn't end up with a
	// half-configured TLS listener.
	//
	// The mapstructure keys `sslcrt` / `sslkey` (no inner
	// underscore) translate to env vars
	// `PENTESTSWARM_SERVER_SSLCRT` / `PENTESTSWARM_SERVER_SSLKEY`
	// via Viper's dot→underscore replacer. Using `ssl_crt` /
	// `ssl_key` (with inner underscore) would NOT work: Viper's
	// replacer is symmetric, so the env var `SSL_CRT` round-trips
	// back to a key `ssl_crt` (no dot), which doesn't match
	// any mapstructure tag and silently fails to bind.
	SSLCert string `mapstructure:"sslcrt"`
	SSLKey  string `mapstructure:"sslkey"`
	UseSSL  bool   `mapstructure:"use_ssl"`
}

// CommercialConfig groups every knob that turns the OSS build
// into a distributable commercial product. The single-tenant
// demo path requires nothing here; flipping Enabled turns on
// license enforcement and per-tenant isolation.
type CommercialConfig struct {
	// LicenseKey is the Ed25519-signed license. Empty + dev
	// mode = the server skips all commercial checks (the
	// default; matches ptagent's "no license" demo path).
	// In production deployments this is set via
	// PENTESTSWARM_COMMERCIAL_LICENSE_KEY.
	LicenseKey string `mapstructure:"license_key"`
	// PublicKey is the Ed25519 public key (hex) used to
	// verify the license. The corresponding private key is
	// held by the issuing party (the company) and never
	// shipped with the binary. When LicenseKey is non-empty
	// and PublicKey is empty, validation falls back to a
	// built-in demo public key so dev / CI can run.
	PublicKey string `mapstructure:"public_key"`
	// InstallationID is the per-deployment ID checked
	// against the license. Auto-generated on first start
	// if empty; persisted via the license cache file.
	InstallationID string `mapstructure:"installation_id"`
	// CacheFile is the on-disk path used to persist the
	// installation ID between restarts. Default
	// "~/.pentestswarm/installation_id".
	CacheFile string `mapstructure:"cache_file"`
	// CORS configures the CORS allow-list. The legacy
	// build hard-codes AllowOrigins="*"; this struct lets
	// operators lock the API down to a known set of
	// front-end origins.
	CORS CORSConfig `mapstructure:"cors"`
	// MultiTenant toggles the per-tenant isolation layer.
	// When true, the auth middleware stamps every request
	// with a tenant id and the Postgres board wraps every
	// write/read in a tx that sets app.tenant_id for RLS.
	MultiTenant bool `mapstructure:"multi_tenant"`
}

// CORSConfig configures the CORS middleware. Origins matches
// the request's Origin header (exact, case-sensitive). "*" is
// recognised as a wildcard (dev-only). AllowCredentials, when
// true, causes the middleware to echo the request's Origin
// instead of "*" so the cookie / Authorization headers the
// browser sends actually survive preflight.
type CORSConfig struct {
	// Origins is the explicit allow-list. Empty = the
	// middleware behaves as if Origins=["*"]; this matches
	// the legacy behaviour so the dev experience is
	// unchanged until an operator opts in.
	Origins []string `mapstructure:"origins"`
	// AllowMethods is the list of HTTP methods the browser
	// may send. Empty → sensible default
	// (GET/POST/PUT/PATCH/DELETE/OPTIONS).
	AllowMethods []string `mapstructure:"allow_methods"`
	// AllowHeaders is the list of request headers the
	// browser may send. Empty → sensible default
	// (Origin, Content-Type, Accept, Authorization,
	// X-API-Key).
	AllowHeaders []string `mapstructure:"allow_headers"`
	// AllowCredentials is whether the browser may send
	// cookies / Authorization on cross-origin requests.
	// Production deployments that need this MUST list
	// explicit origins (no wildcard) per the CORS spec.
	AllowCredentials bool `mapstructure:"allow_credentials"`
	// MaxAge is the preflight cache lifetime. 0 → 12h.
	MaxAge int `mapstructure:"max_age"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Name     string `mapstructure:"name"`
	SSLMode  string `mapstructure:"sslmode"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode)
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

type OrchestratorConfig struct {
	Provider      string  `mapstructure:"provider"` // claude, openai, ollama, lmstudio
	Model         string  `mapstructure:"model"`
	APIKey        string  `mapstructure:"api_key"`
	Endpoint      string  `mapstructure:"endpoint"`
	ContextWindow int     `mapstructure:"context_window"`
	MaxTokens     int     `mapstructure:"max_tokens"`
	Temperature   float64 `mapstructure:"temperature"`

	// Fallback is an optional less-restrictive provider used when the
	// primary refuses a request (e.g. Claude's safety filter triggering
	// on legitimate offensive-security prompts). When configured, the
	// prompts.NewProviderWithRetry factory wraps the primary in
	// prompts.RetryProvider with this as the fallback.
	Fallback FallbackConfig `mapstructure:"fallback"`
}

// FallbackConfig holds the secondary provider used by the refusal-retry
// path. Typically points at Together AI / DeepSeek / Ollama — providers
// less prone to refusing offensive-security prompts than Claude.
type FallbackConfig struct {
	Provider string `mapstructure:"provider"` // openai, ollama, lmstudio
	Model    string `mapstructure:"model"`
	APIKey   string `mapstructure:"api_key"`
	Endpoint string `mapstructure:"endpoint"`
}

type AgentModelConfig struct {
	Provider string `mapstructure:"provider"` // claude, openai, ollama, lmstudio — empty means inherit from orchestrator
	Model    string `mapstructure:"model"`
	APIKey   string `mapstructure:"api_key"` // empty means inherit from orchestrator
	Endpoint string `mapstructure:"endpoint"`
}

type AgentsConfig struct {
	Recon      AgentModelConfig `mapstructure:"recon"`
	Classifier AgentModelConfig `mapstructure:"classifier"`
	Exploit    AgentModelConfig `mapstructure:"exploit"`
	Report     AgentModelConfig `mapstructure:"report"`
}

type ToolsConfig struct {
	DefaultTimeout int            `mapstructure:"default_timeout"` // seconds
	Subfinder      SubfinderOpts  `mapstructure:"subfinder"`
	Httpx          HttpxOpts      `mapstructure:"httpx"`
	Nuclei         NucleiOpts     `mapstructure:"nuclei"`
	Naabu          NaabuOpts      `mapstructure:"naabu"`
	Katana         KatanaOpts     `mapstructure:"katana"`
}

type SubfinderOpts struct {
	Recursive bool `mapstructure:"recursive"`
	Timeout   int  `mapstructure:"timeout"`
	RateLimit int  `mapstructure:"rate_limit"`
}

type HttpxOpts struct {
	FollowRedirects bool `mapstructure:"follow_redirects"`
	Timeout         int  `mapstructure:"timeout"`
	Threads         int  `mapstructure:"threads"`
}

type NucleiOpts struct {
	TemplatePath string   `mapstructure:"template_path"`
	Severity     []string `mapstructure:"severity"`
	RateLimit    int      `mapstructure:"rate_limit"`
	Timeout      int      `mapstructure:"timeout"`
}

type NaabuOpts struct {
	Ports   string `mapstructure:"ports"` // "top-1000", "80,443,8080", etc.
	Rate    int    `mapstructure:"rate"`
	Timeout int    `mapstructure:"timeout"`
}

type KatanaOpts struct {
	Depth   int  `mapstructure:"depth"`
	JSCrawl bool `mapstructure:"js_crawl"`
	Timeout int  `mapstructure:"timeout"`
}

type ScopeConfig struct {
	EnforceStrict bool `mapstructure:"enforce_strict"` // always true, cannot be disabled
}

type ASMConfig struct {
	Enabled           bool   `mapstructure:"enabled"`
	DefaultSchedule   string `mapstructure:"default_schedule"`   // e.g. "24h"
	MaxAutoCampaigns  int    `mapstructure:"max_auto_campaigns"` // per 24h per scope
	NotificationSlack string `mapstructure:"notification_slack"`
	NotificationEmail string `mapstructure:"notification_email"`
}

type BugBountyConfig struct {
	HackerOneAPIKey   string `mapstructure:"hackerone_api_key"`
	HackerOneUsername string `mapstructure:"hackerone_username"`
	BugcrowdAPIKey    string `mapstructure:"bugcrowd_api_key"`
}

type IntelligenceConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	SharePatterns   bool   `mapstructure:"share_patterns"`
	ConsumePatterns bool   `mapstructure:"consume_patterns"`
	APIEndpoint     string `mapstructure:"api_endpoint"`
}

type IntegrationsConfig struct {
	Jira  JiraConfig  `mapstructure:"jira"`
	Slack SlackConfig `mapstructure:"slack"`
}

type JiraConfig struct {
	URL       string `mapstructure:"url"`
	APIToken  string `mapstructure:"api_token"`
	Project   string `mapstructure:"project"`
	IssueType string `mapstructure:"issue_type"`
}

type SlackConfig struct {
	BotToken      string `mapstructure:"bot_token"`
	SigningSecret string `mapstructure:"signing_secret"`
	Channel       string `mapstructure:"channel"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"` // debug, info, warn, error
	Format string `mapstructure:"format"` // json, console
}

// Load reads configuration from file and environment variables.
func Load(path string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.use_ssl", false)
	v.SetDefault("server.ssl_crt", "")
	v.SetDefault("server.ssl_key", "")
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "pentestswarm")
	v.SetDefault("database.name", "pentestswarm")
	v.SetDefault("database.sslmode", "disable")
	v.SetDefault("redis.host", "localhost")
	v.SetDefault("redis.port", 6379)
	v.SetDefault("redis.db", 0)
	v.SetDefault("orchestrator.provider", "claude")
	v.SetDefault("orchestrator.model", "")
	v.SetDefault("orchestrator.context_window", 200000)
	v.SetDefault("orchestrator.max_tokens", 8192)
	v.SetDefault("orchestrator.temperature", 0.1)
	// Agent defaults: empty provider/api_key = inherit from orchestrator.
	// This means with just a Claude API key, ALL agents use Claude — no Ollama needed.
	v.SetDefault("agents.recon.provider", "")
	v.SetDefault("agents.recon.model", "")
	v.SetDefault("agents.classifier.provider", "")
	v.SetDefault("agents.classifier.model", "")
	v.SetDefault("agents.exploit.provider", "")
	v.SetDefault("agents.exploit.model", "")
	v.SetDefault("agents.report.provider", "")
	v.SetDefault("agents.report.model", "")
	v.SetDefault("tools.default_timeout", 300)
	v.SetDefault("tools.subfinder.recursive", false)
	v.SetDefault("tools.subfinder.timeout", 300)
	v.SetDefault("tools.subfinder.rate_limit", 10)
	v.SetDefault("tools.httpx.follow_redirects", true)
	v.SetDefault("tools.httpx.timeout", 30)
	v.SetDefault("tools.httpx.threads", 50)
	v.SetDefault("tools.nuclei.severity", []string{"critical", "high", "medium"})
	v.SetDefault("tools.nuclei.rate_limit", 150)
	v.SetDefault("tools.nuclei.timeout", 300)
	v.SetDefault("tools.naabu.ports", "top-1000")
	v.SetDefault("tools.naabu.rate", 1000)
	v.SetDefault("tools.naabu.timeout", 300)
	v.SetDefault("tools.katana.depth", 3)
	v.SetDefault("tools.katana.js_crawl", true)
	v.SetDefault("tools.katana.timeout", 300)
	v.SetDefault("scope.enforce_strict", true)
	v.SetDefault("monitor.enabled", false)
	v.SetDefault("monitor.max_total_per_agent", 0)
	v.SetDefault("monitor.max_same_tool_streak", 0)
	v.SetDefault("monitor.max_per_window", 0)
	v.SetDefault("monitor.window_seconds", 60)
	v.SetDefault("asm.enabled", false)
	v.SetDefault("asm.default_schedule", "24h")
	v.SetDefault("asm.max_auto_campaigns", 3)
	v.SetDefault("intelligence.enabled", false)
	v.SetDefault("intelligence.share_patterns", false)
	v.SetDefault("intelligence.consume_patterns", false)
	v.SetDefault("intelligence.api_endpoint", "https://api.pentestswarm.ai")
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "console")
	// LLM/embeddings defaults — explicit "noop" by default so that
	// installations that haven't set OPENAI_API_KEY don't fail to
	// boot. Production deployments override embeddings.provider to
	// "openai" / "ollama" in their config.yaml.
	v.SetDefault("llm.embeddings.provider", "noop")
	v.SetDefault("llm.embeddings.model", "")
	v.SetDefault("llm.embeddings.dimensions", 0)
	v.SetDefault("llm.embeddings.batch_size", 0)
	v.SetDefault("llm.embeddings.cache.enabled", false)
	v.SetDefault("llm.embeddings.cache.size", 1024)
	v.SetDefault("llm.embeddings.cache.ttl", 0)
	// Embeddings async wrapper — disabled by default. Turning
	// this on collapses concurrent Embed calls into fewer
	// upstream calls via an in-process worker pool.
	v.SetDefault("llm.embeddings.async.enabled", false)
	v.SetDefault("llm.embeddings.async.workers", 4)
	v.SetDefault("llm.embeddings.async.batch_size", 32)
	v.SetDefault("llm.embeddings.async.batch_timeout", "50ms")
	v.SetDefault("llm.embeddings.async.queue_size", 1024)
	v.SetDefault("llm.embeddings.async.submit_wait", "1s")
	// Embeddings Redis cache — disabled by default. The cache
	// lives in front of the in-memory LRU and survives
	// restarts, making it a good fit for multi-instance
	// deployments.
	v.SetDefault("llm.embeddings.redis.enabled", false)
	v.SetDefault("llm.embeddings.redis.addr", "localhost:6379")
	v.SetDefault("llm.embeddings.redis.password", "")
	v.SetDefault("llm.embeddings.redis.db", 0)
	v.SetDefault("llm.embeddings.redis.ttl", "24h")
	v.SetDefault("llm.embeddings.redis.prefix", "psa:embed:")
	v.SetDefault("llm.embeddings.redis.timeout", "500ms")
	// LLM/summarizer defaults — keep the previous hard-coded values
	// so existing behaviour is preserved when the block is omitted.
	v.SetDefault("llm.summarizer.model", "")
	v.SetDefault("llm.summarizer.max_tokens", 2048)
	v.SetDefault("llm.summarizer.temperature", 0.0)
	v.SetDefault("llm.summarizer.trigger_at", 0.7)
	v.SetDefault("llm.summarizer.warning_at", 0.8)
	v.SetDefault("llm.summarizer.keep_recent_ratio", 0.5)
	v.SetDefault("llm.summarizer.min_messages_to_summarize", 4)
	v.SetDefault("llm.summarizer.preserve_system_prompt", true)
	v.SetDefault("llm.summarizer.custom_system_prompt", "")
	v.SetDefault("llm.summarizer.custom_user_prompt_tpl", "")

	// Environment variable overrides: PENTESTSWARM_SERVER_PORT, etc.
	v.SetEnvPrefix("PENTESTSWARM")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// P5+ 一体部署：viper 的 AutomaticEnv 不会在 YAML
	// 里没出现的 key 上读取 env var（必须是已知 key 才能
	// 被 env 覆盖）。这会让 `docker compose up` 启动
	// 时报 "POSTGRES_PASSWORD missing" 即使 env 已注入。
	// 用 BindEnv 显式声明这些关键 key，让 PENTESTSWARM_*
	// 在任何情况下都能 override 默认值 / YAML。
	//
	// 注意：mapstructure tag 里的 `,remain` 之类保留字
	// 不需要绑定。下面的列表是
	// 1) compose 默认会注入的关键环境变量
	// 2) operator 在 config.example.yaml 里能找到的
	//    关键路径。错过的 key 用 YAML 兜底即可。
	criticalKeys := []string{
		"server.host", "server.port",
		"server.sslcrt", "server.sslkey", "server.use_ssl",
		"database.host", "database.port", "database.user",
		"database.password", "database.name", "database.sslmode",
		"redis.host", "redis.port", "redis.password", "redis.db",
		"orchestrator.provider", "orchestrator.model",
		"orchestrator.api_key", "orchestrator.endpoint",
		"llm.embeddings.provider", "llm.embeddings.api_key",
		"llm.embeddings.model", "llm.embeddings.endpoint",
		"llm.embeddings.async.enabled",
		"llm.embeddings.redis.enabled", "llm.embeddings.redis.addr",
		"llm.embeddings.redis.password",
		"commercial.license_key", "commercial.public_key",
		"commercial.multi_tenant",
		"commercial.cors.origins",
		"ps_auth.oauth.google.client_id", "ps_auth.oauth.google.client_secret",
		"ps_auth.oauth.github.client_id", "ps_auth.oauth.github.client_secret",
		"psa_public_base_url",
	}
	for _, k := range criticalKeys {
		_ = v.BindEnv(k)
	}

	// Read config file
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.pentestswarm")
		v.AddConfigPath("/etc/pentestswarm")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		// Config file not found is OK — we use defaults + env vars
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// Scope enforcement is always on — this is a hard-coded safety constraint
	cfg.Scope.EnforceStrict = true

	return &cfg, nil
}

// LoadFromPath loads config from a specific file path, returning an error if not found.
func LoadFromPath(path string) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("config file not found: %s", path)
	}
	return Load(path)
}

// Validate checks that all required configuration values are set and valid.
func Validate(cfg *Config) error {
	var errs []string

	// Orchestrator validation
	switch cfg.Orchestrator.Provider {
	case "claude":
		if cfg.Orchestrator.APIKey == "" {
			errs = append(errs, "orchestrator.api_key is required when provider is 'claude'")
		}
	case "ollama", "lmstudio":
		if cfg.Orchestrator.Endpoint == "" {
			errs = append(errs, "orchestrator.endpoint is required when provider is 'ollama' or 'lmstudio'")
		}
		if _, err := url.Parse(cfg.Orchestrator.Endpoint); err != nil {
			errs = append(errs, fmt.Sprintf("orchestrator.endpoint is not a valid URL: %s", err))
		}
	case "":
		errs = append(errs, "orchestrator.provider is required (claude, openai, deepseek, glm, kimi, qwen, ollama, or lmstudio)")
	case "openai", "deepseek", "glm", "zhipu", "kimi", "moonshot", "qwen":
		// OpenAI-API-compatible vendors. Each preset auto-fills
		// endpoint + model when blank, but the operator is free
		// to override via config.yaml. No further check here
		// beyond "api_key is required" — the LLM factory handles
		// the rest.
		if cfg.Orchestrator.APIKey == "" {
			errs = append(errs, fmt.Sprintf("orchestrator.api_key is required when provider is %q", cfg.Orchestrator.Provider))
		}
		if cfg.Orchestrator.Endpoint != "" {
			if _, err := url.Parse(cfg.Orchestrator.Endpoint); err != nil {
				errs = append(errs, fmt.Sprintf("orchestrator.endpoint is not a valid URL: %s", err))
			}
		}
	default:
		errs = append(errs, fmt.Sprintf("orchestrator.provider '%s' is not valid — use claude, openai, deepseek, glm, kimi, qwen, ollama, or lmstudio", cfg.Orchestrator.Provider))
	}

	// Model name is validated by the LLM factory, which provides sensible
	// defaults per provider (deepseek-v4-pro for deepseek, claude-sonnet-4-6
	// for claude, etc.). An empty model means "use the provider default".

	// Database validation
	if cfg.Database.Host == "" {
		errs = append(errs, "database.host is required")
	}
	if cfg.Database.Name == "" {
		errs = append(errs, "database.name is required")
	}

	// Server port validation
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		errs = append(errs, "server.port must be between 1 and 65535")
	}

	// Temperature validation
	if cfg.Orchestrator.Temperature < 0 || cfg.Orchestrator.Temperature > 2 {
		errs = append(errs, "orchestrator.temperature must be between 0 and 2")
	}

	// Embeddings provider validation — only flag outright unknowns.
	// Empty and the documented "noop" / "local_stub" aliases are
	// accepted as a no-op configuration.
	switch cfg.LLM.Embeddings.Provider {
	case "", "noop", "local_stub", "local-stub", "openai",
		"deepseek", "glm", "kimi", "qwen", "ollama":
		// known-good
	default:
		errs = append(errs, fmt.Sprintf("llm.embeddings.provider '%s' is not recognised — use openai, ollama, noop, or local_stub", cfg.LLM.Embeddings.Provider))
	}
	if cfg.LLM.Embeddings.Dimensions < 0 {
		errs = append(errs, "llm.embeddings.dimensions must be >= 0 (0 = provider default)")
	}

	// Summarizer threshold sanity.
	if s := cfg.LLM.Summarizer; s.TriggerAt != 0 && (s.TriggerAt <= 0 || s.TriggerAt >= 1) {
		errs = append(errs, "llm.summarizer.trigger_at must be in (0, 1)")
	}
	if s := cfg.LLM.Summarizer; s.WarningAt != 0 && (s.WarningAt <= 0 || s.WarningAt >= 1) {
		errs = append(errs, "llm.summarizer.warning_at must be in (0, 1)")
	}
	if s := cfg.LLM.Summarizer; s.KeepRecentRatio != 0 && (s.KeepRecentRatio <= 0 || s.KeepRecentRatio >= 1) {
		errs = append(errs, "llm.summarizer.keep_recent_ratio must be in (0, 1)")
	}

	if len(errs) > 0 {
		return fmt.Errorf("configuration errors:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return nil
}
