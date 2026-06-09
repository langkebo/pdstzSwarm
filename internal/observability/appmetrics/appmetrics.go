// Package appmetrics defines the application-specific metrics for
// Pentest-Swarm-AI. This is the single place to add a new metric —
// keep it close to the metric name so future readers can grep
// `appmetrics.<Name>` and find every call site.
package appmetrics

import (
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
)

// Wrap decorates an llm.Provider with a Prometheus metrics
// recorder. This is the entry point used by cli/serve.go and by
// the engine runner (via engine.MetricsBundle). The returned
// provider satisfies engine.MetricsBundle via:
//
//	var _ engine.MetricsBundle = (*appmetrics.All)(nil)
//
// (i.e. *All itself is a bundle — no constructor needed).
func (a *All) Wrap(p llm.Provider) llm.Provider {
	return NewMetricsProvider(p, a)
}

// All holds the standard set of metrics every Pentest-Swarm-AI
// process should expose. Use the package-level singletons
// (HTTPRequests, FindingsEmitted, etc.) below.
type All struct {
	// HTTP layer
	HTTPRequests   *metrics.Counter
	HTTPDuration   *metrics.Histogram
	HTTPInFlight   *metrics.Gauge

	// WebSocket layer
	WSConnections  *metrics.Gauge
	WSMessages     *metrics.Counter
	WSBroadcastErr *metrics.Counter

	// Campaign / finding
	CampaignsCreated *metrics.Counter
	CampaignsByState *metrics.Gauge
	FindingsEmitted  *metrics.Counter
	FindingsBySeverity *metrics.Counter

	// LLM
	LLMCalls         *metrics.Counter
	LLMErrorCalls    *metrics.Counter
	LLMTokensIn      *metrics.Counter
	LLMTokensOut     *metrics.Counter
	LLMLatency       *metrics.Histogram

	// Tool execution
	ToolCalls        *metrics.Counter
	ToolErrors       *metrics.Counter
	ToolLatency      *metrics.Histogram

	// Build info
	BuildInfo        *metrics.Gauge

	// Process (process_start_time_seconds, process_uptime_seconds)
	// are added by NewRegistry automatically.
}

// New registers and returns the canonical metric bundle against
// the supplied registry. Re-registering with the same name is a
// no-op (Register returns nil) so it's safe to call New in
// integration tests.
func New(r *metrics.Registry) *All {
	a := &All{
		HTTPRequests: metrics.NewCounter(
			"psa_http_requests_total",
			"Total number of HTTP requests handled, by method / path / status.",
		),
		HTTPDuration: metrics.NewHistogramWithBuckets(
			"psa_http_request_duration_seconds",
			"HTTP request latency in seconds, by method / path.",
			[]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		),
		HTTPInFlight: metrics.NewGauge(
			"psa_http_in_flight_requests",
			"Number of HTTP requests currently being handled.",
		),

		WSConnections: metrics.NewGauge(
			"psa_ws_active_connections",
			"Number of active WebSocket connections, by campaign.",
		),
		WSMessages: metrics.NewCounter(
			"psa_ws_messages_total",
			"Total number of WebSocket messages broadcast, by event type.",
		),
		WSBroadcastErr: metrics.NewCounter(
			"psa_ws_broadcast_errors_total",
			"Total number of WebSocket broadcast errors, by campaign.",
		),

		CampaignsCreated: metrics.NewCounter(
			"psa_campaigns_created_total",
			"Total number of campaigns created.",
		),
		CampaignsByState: metrics.NewGauge(
			"psa_campaigns_by_state",
			"Number of campaigns currently in each lifecycle state.",
		),
		FindingsEmitted: metrics.NewCounter(
			"psa_findings_emitted_total",
			"Total number of findings emitted by the swarm, by severity.",
		),
		FindingsBySeverity: metrics.NewCounter(
			"psa_findings_by_severity_total",
			"Total number of findings emitted, by severity bucket.",
		),

		LLMCalls: metrics.NewCounter(
			"psa_llm_calls_total",
			"Total number of LLM calls, by provider / model.",
		),
		LLMErrorCalls: metrics.NewCounter(
			"psa_llm_errors_total",
			"Total number of failed LLM calls, by provider / model.",
		),
		LLMTokensIn: metrics.NewCounter(
			"psa_llm_tokens_in_total",
			"Total number of input tokens consumed, by provider / model.",
		),
		LLMTokensOut: metrics.NewCounter(
			"psa_llm_tokens_out_total",
			"Total number of output tokens generated, by provider / model.",
		),
		LLMLatency: metrics.NewHistogramWithBuckets(
			"psa_llm_call_duration_seconds",
			"LLM call latency in seconds, by provider / model.",
			[]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		),

		ToolCalls: metrics.NewCounter(
			"psa_tool_calls_total",
			"Total number of tool calls, by tool name.",
		),
		ToolErrors: metrics.NewCounter(
			"psa_tool_errors_total",
			"Total number of failed tool calls, by tool name.",
		),
		ToolLatency: metrics.NewHistogramWithBuckets(
			"psa_tool_call_duration_seconds",
			"Tool call latency in seconds, by tool name.",
			[]float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
		),

		BuildInfo: metrics.NewGauge(
			"psa_build_info",
			"Build information, always 1.0 — labels carry the version.",
		),
	}
	for _, m := range []metrics.Metric{
		a.HTTPRequests, a.HTTPDuration, a.HTTPInFlight,
		a.WSConnections, a.WSMessages, a.WSBroadcastErr,
		a.CampaignsCreated, a.CampaignsByState,
		a.FindingsEmitted, a.FindingsBySeverity,
		a.LLMCalls, a.LLMErrorCalls, a.LLMTokensIn, a.LLMTokensOut, a.LLMLatency,
		a.ToolCalls, a.ToolErrors, a.ToolLatency,
		a.BuildInfo,
	} {
		r.MustRegister(m)
	}
	return a
}
