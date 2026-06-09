package blackboard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// EmbedHook is the contract an embedding plugin must satisfy to be
// called from PostgresBoard.Write. The hook receives a *Finding after
// the board has finished validating it (campaign_id / type / agent_name
// all set) and may mutate the Embedding field before the row is
// inserted. If the hook returns an error the finding is still
// written, but with a NULL embedding — the writing path is best-effort.
//
// The hook is invoked synchronously inside Write, so implementations
// MUST return promptly. Long-running work (network calls, LLM
// inference) should be done in a worker pool owned by the hook itself.
type EmbedHook interface {
	// OnWrite populates f.Embedding from f's textual content. The
	// returned error is logged but does not block the Write.
	OnWrite(ctx context.Context, f *Finding) error
	// Name is a short identifier for observability (LangFuse spans,
	// log lines, etc.).
	Name() string
}

// --- AutoEmbedHook -------------------------------------------------------

// AutoEmbedHook is the canonical EmbedHook: it takes a *Finding,
// concatenates the configured text fields (Target, AgentName, Type,
// plus a prefix of the Data blob), and embeds the result.
//
// The hook is intentionally unaware of the Embedder's wire protocol
// — it just calls Embed(ctx, []string{text}) and assigns the first
// returned vector. This keeps the hook composable with any
// implementation that satisfies the embedder contract defined here
// (a local interface to avoid an import cycle on internal/llm).
type AutoEmbedHook struct {
	embedder   FindingEmbedder
	skipEmpty  bool
	maxDataLen int
	fields     []string
	mu         sync.Mutex
	calls      int64
	errs       int64
}

// AutoEmbedOption configures an AutoEmbedHook at construction time.
type AutoEmbedOption func(*AutoEmbedHook)

// WithSkipEmpty causes the hook to leave Embedding nil when the
// joined text is empty (after trimming). The default behaviour is
// to embed the empty string — useful for tests, less useful in
// production where the resulting vector carries no information.
func WithSkipEmpty() AutoEmbedOption {
	return func(h *AutoEmbedHook) { h.skipEmpty = true }
}

// WithMaxDataLen caps the slice of the Data blob that goes into the
// embedding input. Default is 500 bytes; giant payloads are truncated
// to keep the embedder's input well below its token limit.
func WithMaxDataLen(n int) AutoEmbedOption {
	return func(h *AutoEmbedHook) {
		if n > 0 {
			h.maxDataLen = n
		}
	}
}

// WithFields overrides the default field-join order. The default
// order is ["Target", "AgentName", "Type", "Data"] and the joined
// string is "field=value | field=value | ...". Override to embed
// only Title / Description (if those fields exist on the finding) or
// any other projection.
func WithFields(fields ...string) AutoEmbedOption {
	return func(h *AutoEmbedHook) {
		if len(fields) > 0 {
			h.fields = fields
		}
	}
}

// FindingEmbedder is re-exported here (the original declaration is in
// postgres.go where the PostgresBoard uses it directly) so callers
// that only depend on the EmbedHook surface don't need to import the
// postgres-specific file. No new declaration: FindingEmbedder is the
// same interface in both files.

// NewAutoEmbedHook builds a hook that uses the given embedder.
//
// The embedder is captured by reference; callers that later replace
// the embedder (e.g. swap the OpenAI API key at runtime) should
// construct a new hook. The hook's fields default to the most
// useful order for finding-level embeddings: Target, AgentName,
// Type, then a prefix of Data. Override with WithFields.
func NewAutoEmbedHook(emb FindingEmbedder, opts ...AutoEmbedOption) *AutoEmbedHook {
	h := &AutoEmbedHook{
		embedder:   emb,
		maxDataLen: 500,
		fields:     []string{"Target", "AgentName", "Type", "Data"},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Name returns "auto-embed" for observability. Override by wrapping
// the hook in a struct that also implements the EmbedHook interface.
func (h *AutoEmbedHook) Name() string { return "auto-embed" }

// OnWrite computes a vector for the finding and assigns it to
// f.Embedding. Empty inputs are handled per WithSkipEmpty.
func (h *AutoEmbedHook) OnWrite(ctx context.Context, f *Finding) error {
	if h == nil || h.embedder == nil || f == nil {
		return nil
	}
	text := h.composeText(f)
	if strings.TrimSpace(text) == "" {
		if h.skipEmpty {
			return nil
		}
		// Fall through with empty text — caller will get a real
		// (zero-ish) vector from the embedder.
	}

	atomic.AddInt64(&h.calls, 1)
	vecs, err := h.embedder.Embed(ctx, []string{text})
	if err != nil {
		atomic.AddInt64(&h.errs, 1)
		return fmt.Errorf("auto-embed hook: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		atomic.AddInt64(&h.errs, 1)
		return fmt.Errorf("auto-embed hook: empty vector for finding %s", f.ID)
	}
	f.Embedding = vecs[0]
	return nil
}

// composeText joins the configured fields into a single string the
// embedder can vectorise. Unknown field names are silently skipped
// (we want a graceful no-op if the Finding type later drops a field).
func (h *AutoEmbedHook) composeText(f *Finding) string {
	var parts []string
	for _, name := range h.fields {
		switch name {
		case "Target":
			if f.Target != "" {
				parts = append(parts, "Target="+f.Target)
			}
		case "AgentName":
			if f.AgentName != "" {
				parts = append(parts, "Agent="+f.AgentName)
			}
		case "Type":
			if f.Type != "" {
				parts = append(parts, "Type="+string(f.Type))
			}
		case "ID":
			parts = append(parts, "ID="+f.ID.String())
		case "Data":
			if len(f.Data) > 0 {
				data := f.Data
				if h.maxDataLen > 0 && len(data) > h.maxDataLen {
					data = data[:h.maxDataLen]
				}
				parts = append(parts, "Data="+string(data))
			}
		}
	}
	return strings.Join(parts, " | ")
}

// Stats returns the number of Embed calls attempted and the number
// that returned an error. Useful in observability hooks and tests.
func (h *AutoEmbedHook) Stats() (calls, errs int64) {
	return atomic.LoadInt64(&h.calls), atomic.LoadInt64(&h.errs)
}

// --- Hook registry -------------------------------------------------------

// HookRegistry holds the EmbedHook instances attached to a
// PostgresBoard. Write invokes each hook in registration order; an
// error from one hook does not short-circuit the others, so partial
// failures degrade gracefully (a vector is filled in if at least one
// hook succeeds).
//
// The registry also holds a separate list of GraphHook values
// (the P4 knowledge-graph layer). They are invoked AFTER the
// embed-hook chain so the finding's Embedding is already
// populated by the time the graph layer extracts entities.
type HookRegistry struct {
	mu    sync.RWMutex
	hooks []EmbedHook
	graph []GraphHook
}

// NewHookRegistry builds an empty registry.
func NewHookRegistry() *HookRegistry { return &HookRegistry{} }

// Add registers a hook. Duplicates are allowed; the registration
// order is preserved and is the invocation order at Write time.
func (r *HookRegistry) Add(h EmbedHook) {
	if h == nil {
		return
	}
	r.mu.Lock()
	r.hooks = append(r.hooks, h)
	r.mu.Unlock()
}

// AddGraph registers a knowledge-graph hook. Invoked in
// registration order after the embed-hook chain. The hook is
// best-effort: errors are collected but never abort the write.
func (r *HookRegistry) AddGraph(h GraphHook) {
	if h == nil {
		return
	}
	r.mu.Lock()
	r.graph = append(r.graph, h)
	r.mu.Unlock()
}

// Invoke runs every registered hook against the finding, collecting
// errors but never aborting. Returns the joined error (if any) for
// the caller to log.
func (r *HookRegistry) Invoke(ctx context.Context, f *Finding) error {
	r.mu.RLock()
	hooks := make([]EmbedHook, len(r.hooks))
	copy(hooks, r.hooks)
	r.mu.RUnlock()

	var errs []string
	for _, h := range hooks {
		if err := h.OnWrite(ctx, f); err != nil {
			errs = append(errs, h.Name()+": "+err.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("embed hook(s) failed: %s", strings.Join(errs, "; "))
}

// InvokeGraph runs every registered graph hook against the finding.
// Errors are collected and joined but never abort. The returned
// error is logged by PostgresBoard.Write so a flaky graph store
// does not break the writing path.
func (r *HookRegistry) InvokeGraph(ctx context.Context, f *Finding) error {
	r.mu.RLock()
	hooks := make([]GraphHook, len(r.graph))
	copy(hooks, r.graph)
	r.mu.RUnlock()

	var errs []string
	for _, h := range hooks {
		if err := h.OnWrite(ctx, f); err != nil {
			errs = append(errs, "graph hook: "+err.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

// Len returns the number of registered embed hooks.
func (r *HookRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.hooks)
}

// GraphLen returns the number of registered graph hooks.
func (r *HookRegistry) GraphLen() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.graph)
}
