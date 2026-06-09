package langfuse

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
)

// BenchmarkClient_Enqueue measures the per-event cost on the
// caller (e.g. LLM observer). Must be in the hundreds-of-ns
// range — anything slower would be visible on the agent hot path.
func BenchmarkClient_Enqueue(b *testing.B) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey:  "pk",
		SecretKey:  "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	defer c.Close()
	ev := Event{
		ID:        "x",
		Type:      string(BodyEventCreate),
		Body:      Body{Type: BodyEventCreate, ID: "x"},
		Timestamp: time.Now(),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Enqueue(ev)
	}
}

// BenchmarkClient_NoopEnqueue measures Enqueue against a
// disabled client. We use this number to validate the "zero
// overhead" claim in the report: a noop Enqueue should be a
// pointer load + a branch.
func BenchmarkClient_NoopEnqueue(b *testing.B) {
	c := NewClient(Config{}) // no keys → disabled
	ev := Event{ID: "x", Type: "x", Body: Body{Type: BodyEventCreate}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Enqueue(ev)
	}
}

// BenchmarkObservingProvider_CompletePassThrough measures the
// decoration overhead on a typical Complete call. The wrapper
// allocates one Event per call; everything else is a JSON
// marshal on the Enqueue path.
func BenchmarkObservingProvider_CompletePassThrough(b *testing.B) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey:  "pk",
		SecretKey:  "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	defer c.Close()
	inner := &fakeProvider{
		completeResp: llm.CompletionResponse{
			Content: "ok",
			Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}
	op := NewObservingProvider(inner, c)
	req := llm.CompletionRequest{
		MaxTokens:   100,
		Temperature: 0.3,
		Messages:    []llm.Message{{Role: "user", Content: "hi"}},
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = op.Complete(ctx, req)
	}
}

// BenchmarkObservingProvider_NoopComplete measures the per-call
// cost when the client is disabled (the production default for
// most environments). Should be within a few % of the raw
// inner.Complete call.
func BenchmarkObservingProvider_NoopComplete(b *testing.B) {
	c := NewClient(Config{}) // disabled
	inner := &fakeProvider{completeResp: llm.CompletionResponse{Content: "ok"}}
	op := NewObservingProvider(inner, c)
	req := llm.CompletionRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = op.Complete(ctx, req)
	}
}

// BenchmarkTracer_StartSpan measures the per-span cost in the
// disabled case (noop tracer).  The fast path must remain fast.
func BenchmarkTracer_StartSpan_Disabled(b *testing.B) {
	c := NewClient(Config{})
	tr := NewTracer(c, "s", "u")
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, end := tr.StartSpan(ctx, "x")
		end(nil)
	}
}

// BenchmarkFindingObserver_OnFinding measures the per-finding
// cost in the disabled case.
func BenchmarkFindingObserver_OnFinding_Disabled(b *testing.B) {
	c := NewClient(Config{})
	fo := NewFindingObserver(c)
	f := sampleFindingBench(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fo.OnFinding(f)
	}
}

func sampleFindingBench(b *testing.B) blackboard.Finding {
	b.Helper()
	return blackboard.Finding{
		AgentName: "exploiter",
		Type:      blackboard.TypeExploitChain,
		Target:    "https://x/y",
		Data:      []byte(`{"chain":["cve-1"]}`),
	}
}
