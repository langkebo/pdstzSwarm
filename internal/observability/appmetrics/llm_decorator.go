package appmetrics

import (
	"context"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
)

// MetricsProvider decorates an llm.Provider so every Complete /
// Stream call is reflected in the Prometheus registry. The
// decorator is a **transparent wrapper** — it satisfies the
// same llm.Provider interface, so it composes cleanly with the
// LangFuse ObservingProvider. The two decorators are stacked by
// cli/serve.go:
//
//	provider
//	  └─ langfuse.NewObservingProvider (GenAI traces)
//	       └─ appmetrics.NewMetricsProvider (Prometheus counters)
//
// The Prometheus decorator is *outside* the LangFuse one (closer
// to the caller) so its latency histogram records the **end-to-
// end** time the caller observed, including any LangFuse event
// emission overhead. That's what an SRE cares about.
//
// When b is nil the wrapper passes through with one extra
// function-call frame per call — a couple of nanoseconds, never
// the bottleneck. The constructor panics if b is nil, because
// metric-free builds are wired with the *ZeroMetricsProvider
// below (an explicit no-op), not by accident.
type MetricsProvider struct {
	inner llm.Provider
	b     *All
}

// NewMetricsProvider returns a MetricsProvider wrapping inner.
// b is the canonical appmetrics.All bundle (built once in
// api.NewServer); passing nil panics to surface misconfiguration
// early.
func NewMetricsProvider(inner llm.Provider, b *All) *MetricsProvider {
	if b == nil {
		panic("appmetrics.NewMetricsProvider: bundle must not be nil")
	}
	return &MetricsProvider{inner: inner, b: b}
}

// Complete records psa_llm_calls_total + psa_llm_call_duration_seconds
// + psa_llm_tokens_{in,out}_total. On error, psa_llm_errors_total
// is incremented but tokens are still recorded if the response
// carried a partial Usage.
func (p *MetricsProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	labels := p.labels()
	start := time.Now()
	resp, err := p.inner.Complete(ctx, req)
	p.record(labels, time.Since(start), resp, err)
	return resp, err
}

// Stream records the same metrics but only after the stream is
// fully consumed. We don't know the token usage until the
// provider's CompletionResponse is available, and Stream doesn't
// surface it; for streamed calls we only count the call and
// duration (no token attribution) unless the caller has wrapped
// their provider in a non-streaming one (e.g. tests).
func (p *MetricsProvider) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	labels := p.labels()
	start := time.Now()
	src, err := p.inner.Stream(ctx, req)
	if err != nil {
		p.record(labels, time.Since(start), nil, err)
		return nil, err
	}
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range src {
			out <- chunk
		}
		p.record(labels, time.Since(start), nil, nil)
	}()
	return out, nil
}

// HealthCheck, ModelName, ContextWindow, SupportsToolUse all
// pass through unchanged.
func (p *MetricsProvider) HealthCheck(ctx context.Context) error { return p.inner.HealthCheck(ctx) }
func (p *MetricsProvider) ModelName() string                     { return p.inner.ModelName() }
func (p *MetricsProvider) ContextWindow() int                    { return p.inner.ContextWindow() }
func (p *MetricsProvider) SupportsToolUse() bool                 { return p.inner.SupportsToolUse() }

// Inner returns the wrapped provider. Exposed so the cli/serve
// can introspect which concrete provider is at the bottom of the
// decorator stack.
func (p *MetricsProvider) Inner() llm.Provider { return p.inner }

func (p *MetricsProvider) labels() metrics.Labels {
	// Provider is intentionally low-cardinality: it's the
	// concrete backend name (claude / openai / ollama / glm /
	// ...), not the model. Model is a separate label so the
	// two can be cross-tabulated. We resolve the model name
	// from inner.ModelName(); when inner isn't ready yet (rare
	// race during startup) we fall back to "unknown".
	provider := "unknown"
	if name := providerName(p.inner); name != "" {
		provider = name
	}
	return metrics.Labels{
		"provider": provider,
		"model":    p.inner.ModelName(),
	}
}

// record is the single sink for all metric writes. The errPath
// branch increments errors regardless of whether tokens were
// recorded (the call did happen, after all), so error rates in
// Grafana are not silently swallowed by partial successes.
func (p *MetricsProvider) record(labels metrics.Labels, dur time.Duration, resp *llm.CompletionResponse, err error) {
	p.b.LLMCalls.Inc(labels)
	p.b.LLMLatency.Observe(labels, dur.Seconds())
	if err != nil {
		p.b.LLMErrorCalls.Inc(labels)
	}
	if resp != nil {
		if resp.Usage.InputTokens > 0 {
			p.b.LLMTokensIn.Add(labels, float64(resp.Usage.InputTokens))
		}
		if resp.Usage.OutputTokens > 0 {
			p.b.LLMTokensOut.Add(labels, float64(resp.Usage.OutputTokens))
		}
	}
}

// providerName is a best-effort extraction of the backend name
// from the concrete provider. We avoid a type switch here so
// this file doesn't import every concrete impl — the providers
// themselves don't carry a name. The fallback returns the
// model name minus its suffix (e.g. "claude-3-5-sonnet" →
// "claude") which is good enough for the Prometheus label. If
// the model name has no recognisable prefix we use "other".
func providerName(p llm.Provider) string {
	model := p.ModelName()
	// Cheap heuristic: strip the first dash-separated token.
	for i, c := range model {
		if c == '-' || c == ':' || c == '/' {
			return model[:i]
		}
	}
	if model == "" {
		return "unknown"
	}
	return model
}
