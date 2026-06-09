package llm

import (
	"context"
	"fmt"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
)

// NewProvider creates the appropriate LLM provider based on configuration.
func NewProvider(cfg config.OrchestratorConfig) (Provider, error) {
	return newProviderFromParams(cfg.Provider, cfg.APIKey, cfg.Model, cfg.Endpoint, cfg.ContextWindow)
}

// NewAgentProvider creates an LLM provider for a specialist agent.
// If the agent has no provider configured, it inherits from the orchestrator.
// This means with just a Claude API key, ALL agents use Claude — zero Ollama needed.
func NewAgentProvider(agentCfg config.AgentModelConfig, orchestratorCfg config.OrchestratorConfig) (Provider, error) {
	// Inherit from orchestrator if agent provider is not set
	provider := agentCfg.Provider
	if provider == "" {
		provider = orchestratorCfg.Provider
	}

	apiKey := agentCfg.APIKey
	if apiKey == "" {
		apiKey = orchestratorCfg.APIKey
	}

	model := agentCfg.Model
	if model == "" {
		model = orchestratorCfg.Model
	}

	endpoint := agentCfg.Endpoint
	if endpoint == "" {
		endpoint = orchestratorCfg.Endpoint
	}

	contextWindow := orchestratorCfg.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 200000
	}

	return newProviderFromParams(provider, apiKey, model, endpoint, contextWindow)
}

func newProviderFromParams(provider, apiKey, model, endpoint string, contextWindow int) (Provider, error) {
	switch provider {
	case "claude":
		if apiKey == "" {
			return nil, fmt.Errorf("claude provider requires api_key — set PENTESTSWARM_ORCHESTRATOR_API_KEY or orchestrator.api_key in config.yaml")
		}
		return NewClaudeProvider(ClaudeProviderConfig{
			APIKey:        apiKey,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "ollama":
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		return NewOllamaProvider(OllamaProviderConfig{
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "lmstudio":
		if endpoint == "" {
			endpoint = "http://localhost:1234"
		}
		return NewLMStudioProvider(LMStudioProviderConfig{
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "openai":
		// Covers OpenAI itself plus any OpenAI-API-compatible provider:
		// Together AI, DeepSeek, Moonshot/Kimi, Groq, etc. Pick via the
		// endpoint base URL; one provider implementation, many vendors.
		if apiKey == "" {
			return nil, fmt.Errorf("openai provider requires api_key — set PENTESTSWARM_ORCHESTRATOR_API_KEY or orchestrator.api_key in config.yaml")
		}
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:        apiKey,
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	// -----------------------------------------------------------------
	// Domestic Chinese LLM presets (ptagent-inspired: zero-config switch
	// between DeepSeek, GLM, Kimi, Qwen). Each preset auto-fills the
	// vendor endpoint and a sensible default model. The user only has
	// to set api_key. Override endpoint / model explicitly if the
	// vendor publishes a newer host.
	// -----------------------------------------------------------------
	case "deepseek":
		if apiKey == "" {
			return nil, fmt.Errorf("deepseek provider requires api_key — apply at https://platform.deepseek.com")
		}
		if endpoint == "" {
			endpoint = "https://api.deepseek.com"
		}
		if model == "" {
			// Default to the production V4 model. V4 was
			// released in 2026-Q1; the legacy alias
			// `deepseek-chat` still resolves server-side to
			// `deepseek-v4-flash` for backwards compat, but
			// operators running campaigns want the pro tier.
			model = "deepseek-v4-pro"
		}
		if contextWindow <= 0 {
			contextWindow = 128000
		}
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:        apiKey,
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "glm", "zhipu", "zhipuai":
		// Zhipu AI (智谱) — OpenAI-compatible endpoint.
		if apiKey == "" {
			return nil, fmt.Errorf("glm provider requires api_key — apply at https://open.bigmodel.cn")
		}
		if endpoint == "" {
			endpoint = "https://open.bigmodel.cn/api/paas/v4"
		}
		if model == "" {
			model = "glm-4.6"
		}
		if contextWindow <= 0 {
			contextWindow = 128000
		}
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:        apiKey,
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "kimi", "moonshot":
		// Moonshot AI (月之暗面) — OpenAI-compatible endpoint.
		if apiKey == "" {
			return nil, fmt.Errorf("kimi provider requires api_key — apply at https://platform.moonshot.cn")
		}
		if endpoint == "" {
			endpoint = "https://api.moonshot.cn"
		}
		if model == "" {
			model = "moonshot-v1-128k"
		}
		if contextWindow <= 0 {
			contextWindow = 128000
		}
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:        apiKey,
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	case "qwen", "dashscope", "aliyun", "tongyi":
		// Alibaba DashScope (阿里通义千问) — OpenAI-compatible mode.
		if apiKey == "" {
			return nil, fmt.Errorf("qwen provider requires api_key — apply at https://dashscope.aliyun.com")
		}
		if endpoint == "" {
			endpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		if model == "" {
			model = "qwen-plus"
		}
		if contextWindow <= 0 {
			contextWindow = 128000
		}
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:        apiKey,
			Endpoint:      endpoint,
			Model:         model,
			ContextWindow: contextWindow,
		}), nil

	default:
		return nil, fmt.Errorf("unknown provider %q — use claude, openai, ollama, lmstudio, deepseek, glm, kimi, or qwen", provider)
	}
}

// ValidateProvider verifies a provider is reachable and meets minimum requirements.
func ValidateProvider(ctx context.Context, p Provider) error {
	if err := p.HealthCheck(ctx); err != nil {
		return fmt.Errorf("provider health check failed: %w", err)
	}

	if p.ContextWindow() < 32000 {
		return fmt.Errorf("provider context window (%d) is below minimum 32,000 tokens", p.ContextWindow())
	}

	return nil
}

// --- P3-2: Embedder + Summarizer factory methods ---
//
// The factory now also owns embedder and summarizer construction. The
// design intent is symmetry: a single config object can describe the
// chat provider, the embedder, and the summariser. This keeps the
// `cmd/` startup glue minimal and centralises all LLM-family vendor
// knowledge in one file.

// EmbedderConfig mirrors the wire-level config keys an operator would
// put under `llm.embedding:` in config.yaml. The factory accepts the
// struct directly so callers (cmd/swarmd) can wire the YAML fields in
// without hand-rolling a parameter list.
type EmbedderConfig struct {
	Provider   string // openai, deepseek, glm, kimi, qwen, ollama, noop
	APIKey     string
	Endpoint   string
	Model      string
	Dimensions int
	BatchSize  int
	Timeout    int // seconds; 0 → 30
	CacheSize  int // 0 → disabled (no LRU wrap)

	// P5+ async + redis layers. Both are optional and default
	// to "off" so existing call sites (which only populated the
	// legacy fields above) keep their behaviour. When the
	// caller populates these, NewEmbedderFromConfig returns an
	// AsyncEmbedder / RedisEmbedder chain wrapping the base
	// provider.
	Async AsyncConfig
	Redis RedisConfig
}

// NewEmbedderFromConfig is the struct-driven counterpart to the
// package-level NewEmbedder(...strings...). A blank provider returns a
// Noop embedder of the configured dimensions so an operator who hasn't
// configured embeddings gets a graceful zero-vector fallback rather
// than a startup failure.
//
// The full chain is:
//
//	Redis (opt)  →  Async (opt)  →  Cached (LRU, opt)  →  base provider
//
// Each layer is independently kill-switched by its Enabled flag.
// Adding a new layer is a 3-line change in this function; the rest
// of the codebase keeps using the Embedder interface unchanged.
func NewEmbedderFromConfig(cfg EmbedderConfig) (Embedder, error) {
	if cfg.Provider == "" {
		return NewNoopEmbedder(cfg.Dimensions), nil
	}
	e, err := newEmbedderByName(cfg.Provider, cfg.APIKey, cfg.Endpoint, cfg.Model, cfg.Dimensions)
	if err != nil {
		return nil, err
	}
	if cfg.CacheSize > 0 {
		e = NewCachedEmbedder(e, cfg.CacheSize)
	}
	// P5+ async layer. Opt-in; an explicit "false" (the zero
	// value) skips the wrapper. The wrapper is a no-op wrapper
	// in terms of the Embedder contract — calling code is
	// unchanged.
	if cfg.Async.Enabled {
		e = NewAsyncEmbedder(e, cfg.Async)
	}
	// P5+ Redis layer. Opt-in. The Redis client is built
	// inside NewRedisEmbedderFromConfig and is owned by the
	// returned wrapper; on shutdown the caller should defer a
	// Close() on a handle retrieved via Unwrap() if available,
	// or just let the process exit (the client uses a 500ms
	// timeout by default so leaked connections die quickly).
	if cfg.Redis.Enabled {
		redisWrapped, err := NewRedisEmbedderFromConfig(e, cfg.Redis)
		if err != nil {
			return nil, err
		}
		e = redisWrapped
	}
	return e, nil
}

// newEmbedderByName is a thin shim around the package-level string-
// dispatched NewEmbedder (in embeddings.go). Kept as a separate function
// so the config-driven path can later add knobs (e.g. timeout, batch
// size, cache wrapping) without touching the strings-only path used by
// ad-hoc callers and tests.
func newEmbedderByName(provider, apiKey, endpoint, model string, dimensions int) (Embedder, error) {
	return NewEmbedder(provider, apiKey, endpoint, model, dimensions)
}

// --- Summarizer factory ---

// NewSummarizerFromConfig builds a Summarizer wired to the given
// provider, using the SummarizerConfig struct defined in summarizer.go.
// Returns (nil, nil) when p is nil so callers can chain `if err != nil`
// and `if s == nil` checks uniformly.
func NewSummarizerFromConfig(p Provider, cfg SummarizerConfig) (*Summarizer, error) {
	if p == nil {
		return nil, nil
	}
	return NewSummarizer(p, cfg), nil
}

// FactoryConfig is the umbrella config object cmd/ reads. Holding all
// the sub-configs in one place lets a single viper.Unmarshal call
// populate the entire LLM family in one shot. Optional fields stay
// zero-valued when omitted from config.yaml.
type FactoryConfig struct {
	Provider      Provider         // chat provider (or nil if not yet built)
	Embedder      Embedder         // embeddings backend (or nil)
	Summarizer    *Summarizer      // summariser (or nil)
	EmbedderCfg   EmbedderConfig   `mapstructure:",squash"` // raw config for late wiring
	SummarizerCfg SummarizerConfig `mapstructure:",squash"` // raw config for late wiring
}
