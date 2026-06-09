package graph

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
)

// GraphHook wires a GraphStore + EntityExtractor into the
// blackboard Write path: every Finding becomes an Episode that is
// auto-extracted and persisted in the graph.
//
// The hook is intentionally NOT a blackboard.EmbedHook: it has a
// richer signature (it takes both a store and an extractor) and
// fires asynchronously, so it does not slow the Write path. The
// PostgresBoard integration lives in internal/swarm/blackboard
// (see PostgresBoard.AddGraphHook).
//
// The hook composition mirrors AutoEmbedHook in
// internal/swarm/blackboard/embed_hook.go: same option pattern,
// same stats() surface, same fail-soft semantics.
type GraphHook struct {
	store     GraphStore
	extractor EntityExtractor
	logger    *slog.Logger
	skipTypes map[blackboard.FindingType]struct{}
	// includeAllTypes inverts skipTypes: every type is processed
	// when true. Default: false (only the non-skipped types).
	includeAllTypes bool

	// Stats — atomic, no lock on the hot path.
	calls     int64
	skipped   int64
	errs      int64
	extracted int64
}

// GraphHookOption configures a GraphHook at construction time.
type GraphHookOption func(*GraphHook)

// WithGraphHookLogger attaches a logger. Default is slog.Default().
func WithGraphHookLogger(l *slog.Logger) GraphHookOption {
	return func(h *GraphHook) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithSkipTypes registers FindingTypes the hook should NOT process.
// Common config: TypeAgentError and TypeCampaignComplete — neither
// carries graph-relevant entities.
func WithSkipTypes(types ...blackboard.FindingType) GraphHookOption {
	return func(h *GraphHook) {
		for _, t := range types {
			h.skipTypes[t] = struct{}{}
		}
	}
}

// WithIncludeAllTypes disables skip-list filtering. Use with care:
// the hook will try to extract entities from every Finding, including
// TypeAgentError which usually doesn't contain useful signals.
func WithIncludeAllTypes() GraphHookOption {
	return func(h *GraphHook) { h.includeAllTypes = true }
}

// NewGraphHook builds the hook. The store and extractor are
// captured by reference — replacing them at runtime requires
// constructing a new hook. The PostgresBoard.AddGraphHook helper
// handles the lifetime: it stashes the hook in its HookRegistry,
// so the board and hook share a single point of replacement.
func NewGraphHook(store GraphStore, ext EntityExtractor, opts ...GraphHookOption) *GraphHook {
	h := &GraphHook{
		store:     store,
		extractor: ext,
		logger:    slog.Default(),
		skipTypes: map[blackboard.FindingType]struct{}{
			blackboard.TypeAgentError:       {},
			blackboard.TypeCampaignComplete: {},
		},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Name returns "graph" for observability. Mirrors AutoEmbedHook.
func (h *GraphHook) Name() string { return "graph" }

// OnWrite extracts the finding's textual summary and persists the
// resulting entities / relations. Returns nil on success, the
// underlying error on failure. The PostgresBoard caller treats
// the error as best-effort (logged, not propagated) so a flaky
// LLM never blocks the writing path.
func (h *GraphHook) OnWrite(ctx context.Context, f *blackboard.Finding) error {
	if h == nil || h.store == nil || h.extractor == nil || f == nil {
		return nil
	}
	atomic.AddInt64(&h.calls, 1)

	if !h.includeAllTypes {
		if _, skip := h.skipTypes[f.Type]; skip {
			atomic.AddInt64(&h.skipped, 1)
			return nil
		}
	}

	text := composeFindingText(f)
	if text == "" {
		atomic.AddInt64(&h.skipped, 1)
		return nil
	}

	ep := Episode{
		Source:     "finding",
		SourceID:   f.ID.String(),
		CampaignID: f.CampaignID,
		Text:       text,
		OccurredAt: f.CreatedAt,
	}
	if ep.OccurredAt.IsZero() {
		ep.OccurredAt = time.Now().UTC()
	}

	// Run extraction synchronously. PostgresGraphStore.AddEpisode
	// runs in a single transaction, so the LLM call is the only
	// non-DB latency. Operators that want to keep the Write path
	// snappy can wrap the hook in a worker pool; the contract
	// here is "best-effort, never blocks the writing path on
	// failure".
	if err := h.store.AddEpisode(ctx, ep, h.extractor); err != nil {
		atomic.AddInt64(&h.errs, 1)
		// A nil-episode-result from the extractor is a normal
		// outcome (e.g. the LLM extracted nothing useful). It is
		// NOT an error.
		if errors.Is(err, context.Canceled) {
			return err
		}
		h.logger.Debug("graph hook: AddEpisode failed",
			slog.String("finding_id", f.ID.String()),
			slog.String("type", string(f.Type)),
			slog.String("err", err.Error()))
		return err
	}
	atomic.AddInt64(&h.extracted, 1)
	return nil
}

// Stats returns counters for observability and tests.
func (h *GraphHook) Stats() (calls, skipped, errs, extracted int64) {
	return atomic.LoadInt64(&h.calls), atomic.LoadInt64(&h.skipped), atomic.LoadInt64(&h.errs), atomic.LoadInt64(&h.extracted)
}

// composeFindingText assembles the textual input for the LLM / rule
// extractor. Mirrors buildFindingText in
// internal/swarm/blackboard/postgres.go so the two paths stay
// consistent.
func composeFindingText(f *blackboard.Finding) string {
	if f == nil {
		return ""
	}
	var b []byte
	if f.Target != "" {
		b = append(b, f.Target...)
		b = append(b, " | "...)
	}
	if f.AgentName != "" {
		b = append(b, f.AgentName...)
		b = append(b, " | "...)
	}
	b = append(b, string(f.Type)...)
	b = append(b, ": "...)
	if len(f.Data) > 0 {
		data := f.Data
		if len(data) > 1024 {
			data = data[:1024]
		}
		b = append(b, data...)
	}
	return string(b)
}
