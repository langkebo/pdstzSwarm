package appmetrics

import (
	"context"
	"strings"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
)

// fakeProvider is a minimal llm.Provider used to verify the
// MetricsProvider decorator records counters/histograms.
type fakeProvider struct {
	name    string
	model   string
	tokens  llm.Usage
	err     error
	delayOK bool
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &llm.CompletionResponse{
		Content: "ok",
		Usage:   f.tokens,
	}, nil
}
func (f *fakeProvider) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan llm.StreamChunk, 1)
	out <- llm.StreamChunk{Done: true}
	close(out)
	return out, nil
}
func (f *fakeProvider) HealthCheck(ctx context.Context) error { return nil }
func (f *fakeProvider) ModelName() string                     { return f.model }
func (f *fakeProvider) ContextWindow() int                    { return 200000 }
func (f *fakeProvider) SupportsToolUse() bool                 { return true }

// TestMetricsProvider_CompleteRecordsAll verifies the decorator
// records psa_llm_calls_total / _errors_total / _tokens_*
// after a successful Complete call.
func TestMetricsProvider_CompleteRecordsAll(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := New(reg)
	inner := &fakeProvider{
		model:  "claude-3-5-sonnet",
		tokens: llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
	prov := NewMetricsProvider(inner, bundle)
	resp, err := prov.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %v, want ok", resp)
	}

	var b strings.Builder
	if err := reg.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`psa_llm_calls_total{model="claude-3-5-sonnet",provider="claude"} 1`,
		`psa_llm_tokens_in_total{model="claude-3-5-sonnet",provider="claude"} 100`,
		`psa_llm_tokens_out_total{model="claude-3-5-sonnet",provider="claude"} 50`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// TestMetricsProvider_ErrorCallIncrementsErrorCounter verifies
// errors are recorded without swallowing them and without
// recording tokens.
func TestMetricsProvider_ErrorCallIncrementsErrorCounter(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := New(reg)
	inner := &fakeProvider{
		model: "gpt-4o",
		err:   &testError{"synthetic"},
	}
	prov := NewMetricsProvider(inner, bundle)
	_, err := prov.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected error to propagate")
	}

	var b strings.Builder
	if err := reg.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `psa_llm_errors_total{model="gpt-4o",provider="gpt"} 1`) {
		t.Errorf("missing error counter in output:\n%s", out)
	}
	if !strings.Contains(out, `psa_llm_calls_total{model="gpt-4o",provider="gpt"} 1`) {
		t.Errorf("missing call counter in output:\n%s", out)
	}
	if strings.Contains(out, `psa_llm_tokens_in_total{model="gpt-4o"`) {
		t.Errorf("tokens should not be recorded on error, got:\n%s", out)
	}
}

// TestMetricsProvider_LatencyHistogramObserves verifies the
// psa_llm_call_duration_seconds histogram records a sample per
// call.
func TestMetricsProvider_LatencyHistogramObserves(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := New(reg)
	inner := &fakeProvider{model: "qwen-plus", tokens: llm.Usage{}}
	prov := NewMetricsProvider(inner, bundle)
	for i := 0; i < 3; i++ {
		if _, err := prov.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatal(err)
		}
	}

	if got := bundle.LLMLatency.Count(metrics.Labels{"provider": "qwen", "model": "qwen-plus"}); got != 3 {
		t.Errorf("histogram count = %d, want 3", got)
	}
}

// TestMetricsProvider_StreamRecords verifies Stream also bumps
// the call counter.
func TestMetricsProvider_StreamRecords(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := New(reg)
	inner := &fakeProvider{model: "deepseek-chat", tokens: llm.Usage{}}
	prov := NewMetricsProvider(inner, bundle)
	ch, err := prov.Stream(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
		// drain
	}
	if got := bundle.LLMCalls.Get(metrics.Labels{"provider": "deepseek", "model": "deepseek-chat"}); got != 1 {
		t.Errorf("calls = %v, want 1", got)
	}
}

// TestAll_WrapSatisfiesEngineInterface is a compile-time guard
// that *All satisfies the engine.MetricsBundle interface (Wrap).
// We don't import engine here (would cycle); we instead verify
// the method exists and returns a non-nil provider.
func TestAll_WrapSatisfiesEngineInterface(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := New(reg)
	inner := &fakeProvider{model: "test"}
	wrapped := bundle.Wrap(inner)
	if wrapped == nil {
		t.Fatal("Wrap returned nil")
	}
	// Wrapped must be a different pointer.
	if wrapped == llm.Provider(inner) {
		t.Error("Wrap returned the same instance; expected a decorator")
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
