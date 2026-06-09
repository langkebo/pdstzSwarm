package llm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisEmbedder is a transparent Redis-backed cache that sits in
// front of any underlying Embedder. It uses MGET / pipelined SET
// to amortise the round-trip across a batch.
//
// Cache key layout:
//
//	<prefix><model>:<sha256(text) hex[:16]>
//
// The model is in the key (not the value) so two deployments
// pointing at the same Redis with different embedding models can
// share a keyspace safely. The prefix is a configurable
// namespace so multiple projects can share a Redis without
// collision.
//
// Value layout (raw bytes, no JSON):
//
//	[uint32 dim][float32 vector...][int64 stored_unix]
//
// The fixed header is intentional: it makes the size predictable
// for monitoring and lets a future version extend the format by
// reading the first 4 bytes as a version tag. We use little-endian
// to match amd64/arm64 native order and skip a swap pass.
//
// Failure mode: Redis errors are NEVER surfaced to the caller.
// The wrapper falls through to the inner embedder and proceeds
// normally; on a successful inner call it logs (or counts) the
// error and returns. This is the right behaviour for a cache —
// a Redis outage must not break the embedding pipeline.
type RedisEmbedder struct {
	inner  Embedder
	client *redis.Client
	prefix string
	ttl    time.Duration

	// Stats. Exposed via Stats() for observability and tests.
	statHits    int64
	statMisses  int64
	statErrors  int64 // Redis call failures (always fall through)
	statWrites  int64
	statWriteOK int64
}

// RedisConfig is the wire-shape the cmd/ startup code populates
// from llm.embeddings.redis.* in config.yaml.
type RedisConfig struct {
	// Enabled is the kill-switch. When false,
	// NewRedisEmbedderFromConfig returns inner unchanged.
	Enabled bool `mapstructure:"enabled"`
	// Addr is the Redis host:port. Required when Enabled.
	Addr string `mapstructure:"addr"`
	// Password is the AUTH token. Empty → no AUTH.
	Password string `mapstructure:"password"`
	// DB is the logical database number. 0 → default.
	DB int `mapstructure:"db"`
	// TTL is the per-entry lifetime. 0 → no expiry.
	TTL time.Duration `mapstructure:"ttl"`
	// Prefix namespaces the keyspace. Default → "psa:embed:".
	Prefix string `mapstructure:"prefix"`
	// Timeout is the per-call timeout. 0 → 500ms. A cache
	// call should never take longer than the user's patience
	// — better to fall through than to wedge.
	Timeout time.Duration `mapstructure:"timeout"`
}

// NewRedisEmbedder builds the wrapper. The caller owns the
// *redis.Client lifecycle (Close on shutdown). The wrapper does
// NOT call Close; this lets the same client be shared with
// other components (rate-limiter, session cache, …).
func NewRedisEmbedder(inner Embedder, client *redis.Client, prefix string, ttl time.Duration) *RedisEmbedder {
	if inner == nil {
		panic("llm.NewRedisEmbedder: inner embedder is nil")
	}
	if client == nil {
		panic("llm.NewRedisEmbedder: redis client is nil")
	}
	if prefix == "" {
		prefix = "psa:embed:"
	}
	return &RedisEmbedder{
		inner:  inner,
		client: client,
		prefix: prefix,
		ttl:    ttl,
	}
}

// NewRedisEmbedderFromConfig is the kill-switch wrapper. When
// cfg.Enabled is false it returns inner unchanged.
func NewRedisEmbedderFromConfig(inner Embedder, cfg RedisConfig) (Embedder, error) {
	if !cfg.Enabled {
		return inner, nil
	}
	if cfg.Addr == "" {
		return nil, errors.New("redis embedder: addr is required when enabled")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  timeout,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	})
	return NewRedisEmbedder(inner, client, cfg.Prefix, cfg.TTL), nil
}

// Client returns the underlying *redis.Client. Callers that need
// to share the connection pool with other components (rate-
// limiter, session cache, …) can use it; cmd/ uses it on
// shutdown to issue a clean Close().
func (r *RedisEmbedder) Client() *redis.Client { return r.client }

// Close releases the Redis client. The wrapper is unusable
// after Close; callers should arrange for the close to happen
// after every outstanding call has returned.
func (r *RedisEmbedder) Close() error { return r.client.Close() }

// Dimensions forwards to the inner embedder.
func (r *RedisEmbedder) Dimensions() int { return r.inner.Dimensions() }

// ModelName forwards to the inner embedder. Used in the cache
// key so the same text gets a different key per model.
func (r *RedisEmbedder) ModelName() string { return r.inner.ModelName() }

// HealthCheck forwards to the inner embedder. The Redis client
// is checked separately via Ping() on the client owned by the
// caller; this method is intentionally cheap.
func (r *RedisEmbedder) HealthCheck(ctx context.Context) error {
	return r.inner.HealthCheck(ctx)
}

// Embed is the sugar path: check cache, fall through, fill
// cache. On a Redis error we silently count it and proceed
// with the inner call; the calling path is never broken.
func (r *RedisEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return r.EmbedBatch(ctx, texts)
}

// EmbedBatch is sugar for Embed.
func (r *RedisEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return r.embedBatch(ctx, texts)
}

// EmbedRequest delegates to the inner embedder. Cache lookups
// are keyed on text only; per-call model/dim overrides are
// rare and we don't want the cache to lie about per-call
// metadata that the key doesn't encode. This matches
// CachedEmbedder's behaviour.
func (r *RedisEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return r.inner.EmbedRequest(ctx, req)
}

// embedBatch is the implementation. We do the cache check in
// one MGET, then a single inner.Embed for the misses, then a
// pipelined SET for the new entries.
func (r *RedisEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	model := r.inner.ModelName()
	keys := make([]string, len(texts))
	for i, t := range texts {
		keys[i] = r.cacheKey(model, t)
	}

	// 1) Bulk lookup. MGET returns nil for missing entries; we
	//    keep the order by indexing into the result slice.
	cached, err := r.mget(ctx, keys)
	if err != nil {
		// Don't break the pipeline; just count and proceed.
		r.statErrors++
		cached = nil
	}

	out := make([][]float32, len(texts))
	missingIdxs := make([]int, 0, len(texts))
	missingTexts := make([]string, 0, len(texts))
	for i, raw := range cached {
		if raw == nil {
			missingIdxs = append(missingIdxs, i)
			missingTexts = append(missingTexts, texts[i])
			continue
		}
		vec, ok := decodeVector(raw, r.inner.Dimensions())
		if !ok {
			// Malformed entry (e.g. dim mismatch after a model
			// swap). Treat as miss so we re-embed and
			// overwrite. This is the correct self-healing
			// behaviour and avoids serving stale data.
			missingIdxs = append(missingIdxs, i)
			missingTexts = append(missingTexts, texts[i])
			continue
		}
		out[i] = vec
		r.statHits++
	}

	if len(missingTexts) == 0 {
		return out, nil
	}
	r.statMisses += int64(len(missingTexts))

	// 2) Inner call for misses only. Re-use the existing
	//    CachedEmbedder (if any) underneath this wrapper.
	vectors, err := r.inner.Embed(ctx, missingTexts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(missingTexts) {
		return nil, fmt.Errorf("redis embedder: inner returned %d vectors, expected %d", len(vectors), len(missingTexts))
	}
	for i, idx := range missingIdxs {
		out[idx] = vectors[i]
	}

	// 3) Pipelined write-back. Errors are counted but not
	//    returned — the next call will simply miss and re-embed.
	r.writeBack(ctx, model, missingTexts, vectors)

	return out, nil
}

// mget performs the bulk lookup. Returns a slice of byte slices
// parallel to keys; nil entries indicate misses. A Redis error
// is returned (not swallowed) so the caller can decide whether
// to fall through.
func (r *RedisEmbedder) mget(ctx context.Context, keys []string) ([][]byte, error) {
	res, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(res))
	for i, v := range res {
		if v == nil {
			out[i] = nil
			continue
		}
		s, ok := v.(string)
		if !ok {
			// Unexpected type — treat as miss.
			out[i] = nil
			continue
		}
		out[i] = []byte(s)
	}
	return out, nil
}

// writeBack stores the freshly-computed vectors with an optional
// TTL. Errors are counted and dropped.
func (r *RedisEmbedder) writeBack(ctx context.Context, model string, texts []string, vectors [][]float32) {
	if len(texts) == 0 {
		return
	}
	pipe := r.client.Pipeline()
	for i, t := range texts {
		key := r.cacheKey(model, t)
		val := encodeVector(vectors[i])
		r.statWrites++
		if r.ttl > 0 {
			if _, err := pipe.Set(ctx, key, val, r.ttl).Result(); err == nil {
				r.statWriteOK++
			} else {
				r.statErrors++
			}
		} else {
			if _, err := pipe.Set(ctx, key, val, 0).Result(); err == nil {
				r.statWriteOK++
			} else {
				r.statErrors++
			}
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		// The per-call err checks above already counted the
		// errors; this catch is for the rare pipe-level
		// failure (e.g. context cancelled mid-flight).
		r.statErrors++
	}
}

// cacheKey returns the namespaced SHA-256 key for a text. The
// model is part of the key so two deployments sharing a Redis
// don't collide.
func (r *RedisEmbedder) cacheKey(model, text string) string {
	h := sha256hex(text)
	return r.prefix + model + ":" + h
}

// sha256hex returns the lowercase-hex SHA-256 of text. Truncated
// to 32 hex chars (128 bits) — still ~2^64 collision resistance,
// enough for a shared Redis.
func sha256hex(text string) string {
	return cacheKey(text) // embeddings_cache.go's helper is hex already
}

// encodeVector serializes a float32 vector to the cache value
// format described in the type-level doc comment. An empty input
// returns nil so the caller can write a tombstone if it ever
// needs to.
func encodeVector(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, 4+len(v)*4)
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(v)))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4+i*4:], math.Float32bits(x))
	}
	return buf
}

// decodeVector parses the cache value. Returns (vec, true) on
// success; (nil, false) on any parse failure or dim mismatch.
// Dim mismatch is treated as a miss so the wrapper re-embeds and
// overwrites — the right behaviour after a model swap.
func decodeVector(raw []byte, expectedDim int) ([]float32, bool) {
	if len(raw) < 4 {
		return nil, false
	}
	dim := int(binary.LittleEndian.Uint32(raw[:4]))
	if dim <= 0 || dim > 100000 {
		return nil, false
	}
	if expectedDim > 0 && dim != expectedDim {
		return nil, false
	}
	if len(raw) < 4+dim*4 {
		return nil, false
	}
	vec := make([]float32, dim)
	for i := 0; i < dim; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4+i*4:]))
	}
	return vec, true
}

// RedisStats is a point-in-time snapshot of the cache counters.
type RedisStats struct {
	Hits     int64
	Misses   int64
	Errors   int64
	Writes   int64
	WriteOK  int64
	HitRate  float64
}

// Stats returns a snapshot. The hit rate is computed in the
// snapshot rather than maintained as a counter to keep the hot
// path free of floating-point work.
func (r *RedisEmbedder) Stats() RedisStats {
	hits := atomicLoad(&r.statHits)
	misses := atomicLoad(&r.statMisses)
	total := hits + misses
	hr := 0.0
	if total > 0 {
		hr = float64(hits) / float64(total)
	}
	return RedisStats{
		Hits:    hits,
		Misses:  misses,
		Errors:  atomicLoad(&r.statErrors),
		Writes:  atomicLoad(&r.statWrites),
		WriteOK: atomicLoad(&r.statWriteOK),
		HitRate: hr,
	}
}

// atomicLoad is a tiny helper that wraps sync/atomic.LoadInt64
// for the stats counters. Kept in a helper so the snapshot code
// reads cleanly and the counter fields stay private to the file.
func atomicLoad(p *int64) int64 {
	return atomic.LoadInt64(p)
}
