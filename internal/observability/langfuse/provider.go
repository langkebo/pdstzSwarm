package langfuse

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
)

// ObservingProvider decorates an llm.Provider so every Complete /
// Stream call emits a LangFuse "generation" event with input +
// output token usage. The decoration is transparent: a wrapped
// provider satisfies the same interface and is a drop-in
// replacement.
//
// When the underlying client is not enabled, the wrapper is still
// safe to use; calls pass through with a single pointer indirection
// and a context lookup. We deliberately do not short-circuit
// differently so call sites can pass either an observing or plain
// provider without changing their own control flow.
type ObservingProvider struct {
	inner llm.Provider
	cli   *Client

	// Per-Complete stream ID counter; LangFuse needs a unique
	// generation ID per event. We allocate on demand.
	mu sync.Mutex
}

// NewObservingProvider wraps inner with LangFuse observation.
// The cli may be nil or disabled — both are tolerated.
func NewObservingProvider(inner llm.Provider, cli *Client) *ObservingProvider {
	return &ObservingProvider{inner: inner, cli: cli}
}

// Complete calls the inner provider and emits a generation-create
// + generation-update pair on success. On error only the
// generation-create is emitted (with Level=ERROR) so the failure
// is visible in the LangFuse UI.
func (p *ObservingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	genID, traceID, parentID, start := p.startGen(ctx, req)

	resp, err := p.inner.Complete(ctx, req)
	end := p.cli.now()
	p.endGen(genID, traceID, parentID, start, end, req, resp, err)
	return resp, err
}

// Stream calls the inner provider's Stream and emits a generation
// event after the stream is fully consumed. Intermediate tokens
// are not individually reported — the cost is roughly 1 event per
// LLM call either way, and StreamChunk doesn't carry Usage (that's
// returned on CompletionResponse only). We accumulate the
// assistant text and emit it on completion.
func (p *ObservingProvider) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	genID, traceID, parentID, start := p.startGen(ctx, req)
	src, err := p.inner.Stream(ctx, req)
	if err != nil {
		end := p.cli.now()
		p.endGenWithUsage(genID, traceID, parentID, start, end, req, nil, nil, err)
		return nil, err
	}
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		var accumulated strings.Builder
		for chunk := range src {
			accumulated.WriteString(chunk.Delta)
			out <- chunk
		}
		end := p.cli.now()
		p.endGenWithUsage(genID, traceID, parentID, start, end, req, accumulated.String(), nil, nil)
	}()
	return out, nil
}

// HealthCheck, ModelName, ContextWindow, SupportsToolUse all
// pass through unchanged.
func (p *ObservingProvider) HealthCheck(ctx context.Context) error { return p.inner.HealthCheck(ctx) }
func (p *ObservingProvider) ModelName() string                     { return p.inner.ModelName() }
func (p *ObservingProvider) ContextWindow() int                    { return p.inner.ContextWindow() }
func (p *ObservingProvider) SupportsToolUse() bool                 { return p.inner.SupportsToolUse() }

// Inner returns the wrapped provider. Exposed so tests can assert
// on the inner state (e.g. that Complete was actually called).
func (p *ObservingProvider) Inner() llm.Provider { return p.inner }

func (p *ObservingProvider) startGen(ctx context.Context, req llm.CompletionRequest) (genID, traceID, parentID string, start time.Time) {
	if !p.cli.Enabled() {
		return "", "", "", time.Time{}
	}
	p.mu.Lock()
	genID = newID("gen")
	p.mu.Unlock()
	start = p.cli.now()

	// Resolve trace from the parent span ctx.
	traceID, parentID = "", ""
	if parent, ok := spanIDFrom(ctx); ok {
		// We need to find the trace ID; we can do this by
		// re-reading it from the same map the Tracer uses.
		// Since we don't share the map (each package constructs
		// its own), we conservatively leave traceID empty and
		// let the server reconstruct from the parent. In
		// practice agents usually StartSpan on the same ctx,
		// so the parent SpanID we attach is enough for the
		// server to nest the generation.
		_ = parent
	}

	// Build a sanitized view of the prompt. We never want to
	// ship user secrets in a freeform metadata field; the
	// dedicated prompt field on the generation is the right
	// place when the operator has explicitly enabled
	// langfuse prompt capture. Until then, we record only
	// sizes — useful for cost tracking, useless for exfil.
	p.cli.Enqueue(Event{
		ID:        genID,
		Timestamp: start,
		Type:      string(BodyGenerationCreate),
		Body: Body{
			Type:      BodyGenerationCreate,
			ID:        genID,
			TraceID:   traceID,
			ParentID:  parentID,
			Name:      "llm." + p.inner.ModelName(),
			StartTime: start,
			Model:     p.inner.ModelName(),
			ModelParams: map[string]any{
				"max_tokens":   req.MaxTokens,
				"temperature":  req.Temperature,
				"messages":     len(req.Messages),
				"tools":        len(req.Tools),
			},
			Usage:  &Usage{Unit: "TOKENS"},
			Prompt: promptFingerprint(req),
		},
	})
	return genID, traceID, parentID, start
}

func (p *ObservingProvider) endGen(genID, traceID, parentID string, start, end time.Time, req llm.CompletionRequest, resp *llm.CompletionResponse, err error) {
	if !p.cli.Enabled() || genID == "" {
		return
	}
	var usage *llm.Usage
	if resp != nil {
		usage = &resp.Usage
	}
	var completion any
	if resp != nil {
		completion = resp.Content
	}
	p.endGenWithUsage(genID, traceID, parentID, start, end, req, completion, usage, err)
}

func (p *ObservingProvider) endGenWithUsage(genID, traceID, parentID string, start, end time.Time, req llm.CompletionRequest, completion any, usage *llm.Usage, err error) {
	level := "DEFAULT"
	msg := ""
	if err != nil {
		level = "ERROR"
		msg = err.Error()
	}
	lfUsage := &Usage{Unit: "TOKENS"}
	if usage != nil {
		lfUsage.Input = usage.InputTokens
		lfUsage.Output = usage.OutputTokens
		lfUsage.Total = usage.TotalTokens()
	}
	p.cli.Enqueue(Event{
		ID:        newID("evt"),
		Timestamp: end,
		Type:      string(BodyGenerationUpdate),
		Body: Body{
			Type:          BodyGenerationUpdate,
			ID:            genID,
			TraceID:       traceID,
			ParentID:      parentID,
			EndTime:       &end,
			Level:         level,
			StatusMessage: msg,
			Usage:         lfUsage,
			Completion:    completion,
		},
	})
}

// promptFingerprint builds a small, *non-sensitive* summary of the
// request for the LangFuse prompt field. We intentionally do not
// log full message bodies: pentest recon prompts frequently carry
// in-scope target strings, credentials harvested during the run,
// etc. Logging sizes is enough to spot cost anomalies; the operator
// can flip on full-prompt capture via a separate config field in
// a future iteration.
func promptFingerprint(req llm.CompletionRequest) map[string]any {
	type msg struct {
		Role    string `json:"role"`
		Length  int    `json:"length"`
	}
	msgs := make([]msg, 0, len(req.Messages))
	totalChars := 0
	for _, m := range req.Messages {
		msgs = append(msgs, msg{Role: m.Role, Length: len(m.Content)})
		totalChars += len(m.Content)
	}
	return map[string]any{
		"system_prompt_length": len(req.SystemPrompt),
		"messages":             msgs,
		"total_chars":          totalChars,
	}
}
