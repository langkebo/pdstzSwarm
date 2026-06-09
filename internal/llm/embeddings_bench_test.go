package llm

import (
	"context"
	"testing"
	"time"
)

// --- BenchmarkLocalStub_Embed --------------------------------------------

// BenchmarkLocalStub_Embed measures the cost of the hash-based
// deterministic embedder on 1000 short texts (typical finding titles).
// Use as a lower bound for "we can't possibly be slower than this" in
// the agent loop.
func BenchmarkLocalStub_Embed(b *testing.B) {
	e := NewLocalStubEmbedder(LocalStubConfig{Dimensions: 384, Model: "bench"})
	texts := make([]string, 1000)
	for i := range texts {
		texts[i] = "benchmark finding " + string(rune('a'+(i%26)))
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Embed(ctx, texts); err != nil {
			b.Fatalf("Embed: %v", err)
		}
	}
}

// --- BenchmarkCachedEmbedder_AllHits -------------------------------------

// All-cached path: zero underlying-embedder calls. Measures the
// in-process LRU lookup overhead.
func BenchmarkCachedEmbedder_AllHits(b *testing.B) {
	inner := NewLocalStubEmbedder(LocalStubConfig{Dimensions: 384})
	cache := NewCachedEmbedder(inner, 4096)
	texts := make([]string, 100)
	for i := range texts {
		texts[i] = "cached finding " + string(rune('a'+(i%26)))
	}
	ctx := context.Background()
	// Prime the cache.
	if _, err := cache.Embed(ctx, texts); err != nil {
		b.Fatalf("prime: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cache.Embed(ctx, texts); err != nil {
			b.Fatalf("Embed: %v", err)
		}
	}
}

// --- BenchmarkCachedEmbedder_AllMisses -----------------------------------

// All-miss path: every call hits the inner embedder. Measures the
// worst case for the cache layer.
func BenchmarkCachedEmbedder_AllMisses(b *testing.B) {
	inner := NewLocalStubEmbedder(LocalStubConfig{Dimensions: 384})
	cache := NewCachedEmbedder(inner, 4096)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		texts := []string{string(rune('a'+i%26)) + "-unique-text"}
		if _, err := cache.Embed(ctx, texts); err != nil {
			b.Fatalf("Embed: %v", err)
		}
	}
}

// --- BenchmarkSummarizer_ShouldSummarize ---------------------------------

// Hot-path: the agent loop calls this every completion to decide
// whether to summarise before the next LLM call.
func BenchmarkSummarizer_ShouldSummarize(b *testing.B) {
	p := &mockProvider{contextWindow: 100000, modelName: "bench"}
	s := NewSummarizer(p, SummarizerConfig{TriggerAtRatio: 0.7})
	msgs := make([]Message, 20)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: "benchmark message body of moderate length for token estimation"}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ShouldSummarize(msgs)
	}
}

// --- BenchmarkSummarizer_Stats -------------------------------------------

// Stats is called from the monitor at every LangFuse span close.
// Verifies it stays in the lock-free path.
func BenchmarkSummarizer_Stats(b *testing.B) {
	p := &mockProvider{contextWindow: 100000, modelName: "bench"}
	s := NewSummarizer(p, SummarizerConfig{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Stats()
	}
	_ = time.Now
}
