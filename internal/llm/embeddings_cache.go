package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// This file contains the CachedEmbedder: a transparent in-memory
// cache wrapper that sits in front of any Embedder. The cache key
// is the SHA-256 of the input text — collisions are not a concern
// at expected LRU sizes. Caching is most useful for finding-embedding
// workloads where the same finding title may be re-embedded after
// a restart of the same campaign, or when the same swarm agent asks
// for an embedding twice within a short window.
//
// The implementation is FIFO (insertion order) rather than true LRU —
// this is deliberate. A true LRU is more useful when access patterns
// are skewed (recent items dominate), but finding-embedding traffic
// has very uniform access patterns: a finding is embedded once on
// write and then re-embedded only on restart. FIFO is simpler,
// has better worst-case behaviour, and is sufficient for the
// observed workload. A future iteration can swap the eviction
// algorithm without changing the public API.

// --- Cache config ----------------------------------------------------------

// EmbeddingCacheConfig configures a CachedEmbedder. It is the
// wire-shape that the cmd/ startup code populates from
// llm.embeddings.cache.* in config.yaml.
type EmbeddingCacheConfig struct {
	// Enabled is a kill-switch. When false, the constructor returns
	// the inner embedder unwrapped.
	Enabled bool `mapstructure:"enabled"`
	// Size is the maximum number of entries to retain. 0 → 1024.
	Size int `mapstructure:"size"`
	// TTL is the per-entry lifetime. 0 → no expiry (cache is
	// bounded only by Size). The sweep runs lazily on read/write.
	TTL time.Duration `mapstructure:"ttl"`
}

// CachedEmbedder is a transparent in-memory cache wrapper around any
// underlying Embedder.
type CachedEmbedder struct {
	inner Embedder

	mu       sync.Mutex
	entries  map[string]cacheEntry
	maxSize  int
	eviction []string // FIFO eviction queue, indexed by insertion order
	ttl      time.Duration
}

type cacheEntry struct {
	vector  []float32
	expires time.Time
}

// NewCachedEmbedder wraps inner with a FIFO cache of the given size.
// A size of 0 (or negative) defaults to 1024 entries; values above
// ~100k risk O(n) eviction on the hot path. ttl of 0 disables expiry.
func NewCachedEmbedder(inner Embedder, size int) *CachedEmbedder {
	if size <= 0 {
		size = 1024
	}
	return &CachedEmbedder{
		inner:   inner,
		entries: make(map[string]cacheEntry, size),
		maxSize: size,
	}
}

// NewCachedEmbedderFromConfig builds a CachedEmbedder honouring the
// EmbeddingCacheConfig. When Enabled is false the inner embedder is
// returned unchanged. When the resulting CachedEmbedder would wrap a
// CachedEmbedder (double-wrap) the outer call is a no-op and the
// existing CachedEmbedder is returned.
func NewCachedEmbedderFromConfig(inner Embedder, cfg EmbeddingCacheConfig) Embedder {
	if !cfg.Enabled {
		return inner
	}
	if _, ok := inner.(*CachedEmbedder); ok {
		return inner
	}
	c := NewCachedEmbedder(inner, cfg.Size)
	c.ttl = cfg.TTL
	return c
}

func (c *CachedEmbedder) Dimensions() int  { return c.inner.Dimensions() }
func (c *CachedEmbedder) ModelName() string { return c.inner.ModelName() }

// EmbedBatch is sugar for Embed.
func (c *CachedEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return c.Embed(ctx, texts)
}

// EmbedRequest delegates to the inner embedder. The cache check is
// bypassed because the request shape is generic — the inner embedder
// will call its own Embed path which goes through the cache. This is
// only slightly wasteful in the common no-override case (one extra
// wrapper call); correctness is preserved.
func (c *CachedEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return c.inner.EmbedRequest(ctx, req)
}

// HealthCheck delegates to the inner embedder. Useful in startup code
// that wires the cache in front of the real backend.
func (c *CachedEmbedder) HealthCheck(ctx context.Context) error {
	return c.inner.HealthCheck(ctx)
}

// Embed checks the cache first; only misses hit the underlying embedder.
// The result is returned in the same order as the input slice.
//
// Expired entries are dropped lazily on read, then any leftover
// re-validation happens on the next sweep. The sweep is intentionally
// O(1) per lookup rather than a periodic background scan to keep the
// memory footprint low — there is no goroutine spun up here.
func (c *CachedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))
	missing := make([]int, 0, len(texts))
	missingTexts := make([]string, 0, len(texts))

	now := time.Now()

	c.mu.Lock()
	for i, t := range texts {
		key := cacheKey(t)
		if e, ok := c.entries[key]; ok {
			if c.ttl == 0 || now.Before(e.expires) {
				results[i] = e.vector
				continue
			}
			// expired — drop and re-fetch
			delete(c.entries, key)
		}
		missing = append(missing, i)
		missingTexts = append(missingTexts, t)
	}
	c.mu.Unlock()

	if len(missing) == 0 {
		return results, nil
	}

	vectors, err := c.inner.Embed(ctx, missingTexts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(missing) {
		return nil, fmt.Errorf("cached embedder: inner returned %d vectors, expected %d", len(vectors), len(missing))
	}

	c.mu.Lock()
	for i, idx := range missing {
		key := cacheKey(missingTexts[i])
		entry := cacheEntry{vector: vectors[i]}
		if c.ttl > 0 {
			entry.expires = now.Add(c.ttl)
		}
		c.entries[key] = entry
		c.eviction = append(c.eviction, key)
		results[idx] = vectors[i]
	}
	for len(c.eviction) > c.maxSize {
		oldest := c.eviction[0]
		c.eviction = c.eviction[1:]
		delete(c.entries, oldest)
	}
	c.mu.Unlock()
	return results, nil
}

// Len reports the number of entries currently in the cache. Useful for
// observability and test assertions.
func (c *CachedEmbedder) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Sweep removes every expired entry. Returns the number of entries
// evicted. Intended for use in monitor ticks or shutdown paths; the
// hot path does not need to call it (lazy expiry is sufficient).
func (c *CachedEmbedder) Sweep() int {
	if c.ttl == 0 {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	evicted := 0
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
			evicted++
		}
	}
	if evicted > 0 {
		// Rebuild the eviction queue to drop now-missing keys.
		filtered := c.eviction[:0]
		for _, k := range c.eviction {
			if _, ok := c.entries[k]; ok {
				filtered = append(filtered, k)
			}
		}
		c.eviction = filtered
	}
	return evicted
}

func cacheKey(text string) string {
	h := sha256.Sum256([]byte(text))
	// Truncate to 16 bytes — SHA-256 collision space is still 2^128,
	// more than enough for a per-process in-memory LRU.
	return string(h[:16])
}
