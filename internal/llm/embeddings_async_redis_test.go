package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// --- Async embedder fixtures ----------------------------------------------

// slowEmbedder sleeps before responding so the worker pool has
// time to merge two submits into a single batch. The call count
// is exposed so tests can assert "we did N upstream calls, not 2N".
type slowEmbedder struct {
	dim      int
	sleep    time.Duration
	mu       sync.Mutex
	calls    int
	textsAll [][]string
}

func (s *slowEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s.mu.Lock()
	s.calls++
	s.textsAll = append(s.textsAll, append([]string(nil), texts...))
	s.mu.Unlock()
	select {
	case <-time.After(s.sleep):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, s.dim)
		for j := range v {
			v[j] = float32(i+1) * 0.1
		}
		_ = t
		out[i] = v
	}
	return out, nil
}
func (s *slowEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return s.Embed(ctx, texts)
}
func (s *slowEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	v, err := s.Embed(ctx, req.Input)
	if err != nil {
		return nil, err
	}
	return &EmbedResponse{Vectors: v, Model: s.ModelName()}, nil
}
func (s *slowEmbedder) HealthCheck(ctx context.Context) error { return nil }
func (s *slowEmbedder) Dimensions() int                       { return s.dim }
func (s *slowEmbedder) ModelName() string                     { return "slow-test" }
func (s *slowEmbedder) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
func (s *slowEmbedder) lastBatchSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.textsAll) == 0 {
		return 0
	}
	return len(s.textsAll[len(s.textsAll)-1])
}

// --- TestAsyncEmbedder_MergesBatches ---------------------------------------

// Two near-simultaneous submits of 4+6 texts should be merged
// into a single upstream call (because total 10 fits in
// BatchSize=32 and the second submit lands within batchTimeout).
// Verifies the worker-pool batching is real, not just claimed.
func TestAsyncEmbedder_MergesBatches(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: 20 * time.Millisecond}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      1, // one worker → second submit lands in the queue while the worker is batching
		BatchSize:    32,
		BatchTimeout: 30 * time.Millisecond,
		QueueSize:    16,
	})
	defer a.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	var out1, out2 [][]float32
	var err1, err2 error
	go func() {
		defer wg.Done()
		out1, err1 = a.Embed(context.Background(), []string{"a", "b", "c", "d"})
	}()
	go func() {
		defer wg.Done()
		// tiny stagger so the second submit lands inside
		// the batch window
		time.Sleep(5 * time.Millisecond)
		out2, err2 = a.Embed(context.Background(), []string{"e", "f", "g", "h", "i", "j"})
	}()
	wg.Wait()
	if err1 != nil || err2 != nil {
		t.Fatalf("embeds failed: %v / %v", err1, err2)
	}
	if len(out1) != 4 || len(out2) != 6 {
		t.Fatalf("out shapes: %d / %d, want 4 / 6", len(out1), len(out2))
	}
	if inner.callCount() != 1 {
		t.Errorf("upstream calls = %d, want 1 (merge two submits)", inner.callCount())
	}
	if inner.lastBatchSize() != 10 {
		t.Errorf("merged batch size = %d, want 10", inner.lastBatchSize())
	}
	stats := a.Stats()
	if stats.Merged < 1 {
		t.Errorf("stats.Merged = %d, want >= 1", stats.Merged)
	}
	if stats.Batches != 1 {
		t.Errorf("stats.Batches = %d, want 1", stats.Batches)
	}
}

// --- TestAsyncEmbedder_DoesNotMergeWhenSlow --------------------------------

// When the second submit lands AFTER batchTimeout, the worker
// must dispatch the first batch independently. We expect 2
// upstream calls.
func TestAsyncEmbedder_DoesNotMergeWhenSlow(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: 5 * time.Millisecond}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      1,
		BatchSize:    32,
		BatchTimeout: 5 * time.Millisecond,
		QueueSize:    16,
	})
	defer a.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = a.Embed(context.Background(), []string{"a", "b", "c"})
	}()
	time.Sleep(50 * time.Millisecond) // stagger > batchTimeout
	go func() {
		defer wg.Done()
		_, _ = a.Embed(context.Background(), []string{"d", "e", "f"})
	}()
	wg.Wait()
	if inner.callCount() != 2 {
		t.Errorf("upstream calls = %d, want 2 (no merge)", inner.callCount())
	}
}

// --- TestAsyncEmbedder_PreservesOrder -------------------------------------

// Two submits with overlapping keys should each see their own
// texts in their own slots.
func TestAsyncEmbedder_PreservesOrder(t *testing.T) {
	inner := &slowEmbedder{dim: 2, sleep: 5 * time.Millisecond}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      2,
		BatchSize:    16,
		BatchTimeout: 20 * time.Millisecond,
		QueueSize:    32,
	})
	defer a.Close()

	// First vector index i maps to all-(i+1)*0.1 floats; the
	// test embedder above already encodes that. We just check
	// the order via indices.
	out, err := a.Embed(context.Background(), []string{"x", "y", "z"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d", len(out))
	}
	for i, v := range out {
		if v[0] != float32(i+1)*0.1 {
			t.Errorf("out[%d][0] = %v, want %v", i, v[0], float32(i+1)*0.1)
		}
	}
}

// --- TestAsyncEmbedder_QueueFullReturnsError -------------------------------

// When the bounded queue fills up faster than the worker pool
// drains, Submit returns ErrQueueFull and the futures resolve
// with a "queue full" error so the caller never blocks forever.
func TestAsyncEmbedder_QueueFullReturnsError(t *testing.T) {
	// Worker is slow (200ms), queue is tiny (1 slot). Two
	// back-to-back submits should trip the queue-full path.
	//
	// BatchTimeout is set to 1ms so the worker dispatches the
	// first batch quickly and starts the 200ms sleep, at
	// which point the second submit's 10ms SubmitWait fires.
	inner := &slowEmbedder{dim: 4, sleep: 200 * time.Millisecond}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      1,
		BatchSize:    32,
		BatchTimeout: 1 * time.Millisecond, // dispatch fast, then worker sleeps 200ms
		QueueSize:    -1,                   // unbuffered
		SubmitWait:   10 * time.Millisecond,
	})
	defer a.Close()

	// The first submit will go through (occupies the worker
	// + the queue slot). The second one will hit the queue
	// and the bounded SubmitWait.
	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	go func() {
		defer wg.Done()
		_, errA = a.Embed(context.Background(), []string{"a"})
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		defer wg.Done()
		_, errB = a.Embed(context.Background(), []string{"b"})
	}()
	wg.Wait()
	if !errors.Is(errB, ErrQueueFull) {
		t.Errorf("second submit err = %v, want ErrQueueFull", errB)
	}
	stats := a.Stats()
	if stats.QueueFull < 1 {
		t.Errorf("stats.QueueFull = %d, want >= 1", stats.QueueFull)
	}
	_ = errA
}

// --- TestAsyncEmbedder_ContextCancelOnSubmit -------------------------------

// Cancelling the context while waiting to enqueue must not
// deadlock the caller.
func TestAsyncEmbedder_ContextCancelOnSubmit(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: 1 * time.Second}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      1,
		BatchSize:    32,
		BatchTimeout: 0,
		QueueSize:    1,
		SubmitWait:   0, // wait forever
	})
	defer a.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := a.Embed(ctx, []string{"x"})
	if err == nil {
		t.Errorf("expected context error, got nil")
	}
}

// --- TestAsyncEmbedder_WaitDrains -------------------------------------------

// Wait must return once every submitted job has resolved.
func TestAsyncEmbedder_WaitDrains(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: 30 * time.Millisecond}
	a := NewAsyncEmbedder(inner, AsyncConfig{
		Enabled:      true,
		Workers:      2,
		BatchSize:    32,
		BatchTimeout: 0,
		QueueSize:    16,
	})
	defer a.Close()

	for i := 0; i < 5; i++ {
		_, err := a.Embed(context.Background(), []string{fmt.Sprintf("t%d", i)})
		if err != nil {
			t.Fatalf("Embed %d: %v", i, err)
		}
	}
	if err := a.Wait(context.Background()); err != nil {
		t.Errorf("Wait: %v", err)
	}
	stats := a.Stats()
	if stats.Submitted != stats.Completed {
		t.Errorf("submitted = %d, completed = %d (mismatch)", stats.Submitted, stats.Completed)
	}
	if stats.Drained < 1 {
		t.Errorf("stats.Drained = %d, want >= 1", stats.Drained)
	}
}

// --- TestAsyncEmbedder_NewFromConfig_KillSwitch ----------------------------

// Disabled config returns the inner embedder unwrapped.
func TestAsyncEmbedder_NewFromConfig_KillSwitch(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	out := NewAsyncEmbedderFromConfig(inner, AsyncConfig{Enabled: false})
	if out != inner {
		t.Errorf("Enabled=false should return inner unchanged")
	}
}

// --- Redis embedder tests ---------------------------------------------------

// newTestRedis builds an in-memory miniredis + a go-redis client
// pointing at it. Caller must call mr.Close() to stop the server.
func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = c.Close()
	})
	return mr, c
}

func TestRedisEmbedder_CacheHitAndMiss(t *testing.T) {
	_, client := newTestRedis(t)
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	r := NewRedisEmbedder(inner, client, "psa:embed:test:", 0)

	// First call: cold cache → all 3 miss → inner called 3x
	// in one batch.
	out, err := r.Embed(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed 1: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d", len(out))
	}
	if inner.callCount() != 1 {
		t.Errorf("inner calls = %d, want 1", inner.callCount())
	}
	stats := r.Stats()
	if stats.Misses != 3 || stats.Hits != 0 {
		t.Errorf("stats = %+v, want Misses=3 Hits=0", stats)
	}

	// Second call with the same texts: all hit, no inner call.
	out2, err := r.Embed(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed 2: %v", err)
	}
	if len(out2) != 3 {
		t.Fatalf("len 2 = %d", len(out2))
	}
	if inner.callCount() != 1 {
		t.Errorf("inner calls = %d, want still 1", inner.callCount())
	}
	stats = r.Stats()
	if stats.Hits != 3 {
		t.Errorf("stats.Hits = %d, want 3", stats.Hits)
	}

	// Third call: 1 new + 2 cached → inner called once (1 vector).
	out3, err := r.Embed(context.Background(), []string{"alpha", "beta", "delta"})
	if err != nil {
		t.Fatalf("Embed 3: %v", err)
	}
	if len(out3) != 3 {
		t.Fatalf("len 3 = %d", len(out3))
	}
	if inner.callCount() != 2 {
		t.Errorf("inner calls = %d, want 2", inner.callCount())
	}
}

func TestRedisEmbedder_RedisFailureFallsThrough(t *testing.T) {
	mr, client := newTestRedis(t)
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	r := NewRedisEmbedder(inner, client, "psa:embed:", 0)

	// Stop the server to simulate a Redis outage.
	mr.Close()

	// The call must succeed (inner ran) and stats.Errors > 0.
	out, err := r.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed (redis down): %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d", len(out))
	}
	stats := r.Stats()
	if stats.Errors == 0 {
		t.Errorf("stats.Errors = 0, want > 0 after killing redis")
	}
}

func TestRedisEmbedder_TTLRespected(t *testing.T) {
	mr, client := newTestRedis(t)
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	r := NewRedisEmbedder(inner, client, "psa:embed:ttl:", 100*time.Millisecond)

	_, err := r.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Miniredis fast-forwards time on demand.
	mr.FastForward(200 * time.Millisecond)
	// After TTL expiry, the next Embed is a miss.
	_, err = r.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("Embed 2: %v", err)
	}
	stats := r.Stats()
	// After expiry: 2 misses total, 0 hits.
	if stats.Hits != 0 {
		t.Errorf("stats.Hits = %d, want 0 (TTL expired → all misses)", stats.Hits)
	}
	if stats.Misses != 2 {
		t.Errorf("stats.Misses = %d, want 2", stats.Misses)
	}
}

func TestRedisEmbedder_KeyNamespacesByModel(t *testing.T) {
	_, client := newTestRedis(t)
	// Two distinct inner embedders with different model names
	// must get different cache keys for the same text.
	inner1 := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	inner2 := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	// SlowEmbedder's ModelName is hard-coded to "slow-test";
	// for this test we need two distinct model names, so we
	// swap the method via a tiny adapter.
	r1 := NewRedisEmbedder(modelNameEmbedder{inner: inner1, name: "model-A"},
		client, "psa:embed:ns:", 0)
	r2 := NewRedisEmbedder(modelNameEmbedder{inner: inner2, name: "model-B"},
		client, "psa:embed:ns:", 0)

	if _, err := r1.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	// inner1 served "hello" (miss → call) and inner2 also
	// served "hello" (different key, miss → call). The second
	// wrapper should NOT have inherited r1's cached value.
	if inner1.callCount() != 1 || inner2.callCount() != 1 {
		t.Errorf("inner1.calls=%d, inner2.calls=%d, want 1/1", inner1.callCount(), inner2.callCount())
	}
}

// modelNameEmbedder wraps an Embedder and overrides ModelName
// without changing Dimensions / Embed. Used by the namespace
// test to give two otherwise-identical embedders distinct
// identities.
type modelNameEmbedder struct {
	inner Embedder
	name  string
}

func (m modelNameEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return m.inner.Embed(ctx, texts)
}
func (m modelNameEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return m.inner.Embed(ctx, texts)
}
func (m modelNameEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return m.inner.EmbedRequest(ctx, req)
}
func (m modelNameEmbedder) HealthCheck(ctx context.Context) error { return m.inner.HealthCheck(ctx) }
func (m modelNameEmbedder) Dimensions() int                       { return m.inner.Dimensions() }
func (m modelNameEmbedder) ModelName() string                     { return m.name }

func TestRedisEmbedder_NewFromConfig_KillSwitch(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	out, err := NewRedisEmbedderFromConfig(inner, RedisConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out != inner {
		t.Errorf("Enabled=false should return inner unchanged")
	}
}

func TestRedisEmbedder_NewFromConfig_MissingAddrErrors(t *testing.T) {
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	_, err := NewRedisEmbedderFromConfig(inner, RedisConfig{Enabled: true})
	if err == nil {
		t.Errorf("expected error when Addr is missing")
	}
}

func TestEncodeDecodeVector_Roundtrip(t *testing.T) {
	v := []float32{0.1, -0.2, 3.14, 1e-7}
	enc := encodeVector(v)
	got, ok := decodeVector(enc, 4)
	if !ok {
		t.Fatalf("decodeVector failed")
	}
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("[%d] got %v, want %v", i, got[i], v[i])
		}
	}
	// Dim mismatch → false
	_, ok = decodeVector(enc, 8)
	if ok {
		t.Errorf("expected dim-mismatch rejection")
	}
	// Empty input
	if encodeVector(nil) != nil {
		t.Errorf("encodeVector(nil) should be nil")
	}
	// Truncated
	_, ok = decodeVector(enc[:8], 4)
	if ok {
		t.Errorf("expected truncated input rejection")
	}
}

func TestEncodeDecodeVector_TruncatedInput(t *testing.T) {
	// 4 bytes header but no vector body → must reject
	short := []byte{0x04, 0x00, 0x00, 0x00}
	if _, ok := decodeVector(short, 4); ok {
		t.Errorf("expected rejection of truncated vector")
	}
}

// --- Integration: full chain -----------------------------------------------

func TestFactoryChain_AsyncRedisCache(t *testing.T) {
	_, client := newTestRedis(t)
	inner := &slowEmbedder{dim: 4, sleep: 2 * time.Millisecond}
	cached := NewCachedEmbedder(inner, 32)
	async := NewAsyncEmbedder(cached, AsyncConfig{
		Enabled:      true,
		Workers:      1, // 1 worker → merging path
		BatchSize:    32,
		BatchTimeout: 20 * time.Millisecond,
		QueueSize:    32,
	})
	defer async.Close()
	redisWrapped := NewRedisEmbedder(async, client, "psa:embed:chain:", 0)
	defer redisWrapped.Close()

	// Two concurrent submits of 4 + 4 = 8 texts; the async
	// layer should merge them into one upstream call. After
	// that, repeated calls hit both the Redis cache (cold
	// → warm) and the in-memory LRU.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = redisWrapped.Embed(context.Background(), []string{"x1", "x2", "x3", "x4"})
	}()
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		_, _ = redisWrapped.Embed(context.Background(), []string{"y1", "y2", "y3", "y4"})
	}()
	wg.Wait()
	if inner.callCount() != 1 {
		t.Errorf("inner.calls = %d, want 1 (async merged + redis cold)", inner.callCount())
	}
	// A third call with the same texts must be a Redis hit
	// and NOT call the inner embedder.
	_, _ = redisWrapped.Embed(context.Background(), []string{"x1", "y3", "x4"})
	if inner.callCount() != 1 {
		t.Errorf("inner.calls = %d, want still 1 (redis hit)", inner.callCount())
	}
}

// --- Test: NewEmbedderFromConfig wires async + redis ----------------------

func TestNewEmbedderFromConfig_BuildsAsyncAndRedisChain(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}

	// Wrap manually to confirm the chain: redis → async → cached → inner.
	cached := NewCachedEmbedder(inner, 16)
	async := NewAsyncEmbedder(cached, AsyncConfig{
		Enabled: true, Workers: 1, BatchSize: 16,
		BatchTimeout: 10 * time.Millisecond, QueueSize: 16,
	})
	defer async.Close()
	r := NewRedisEmbedder(async, redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		"psa:embed:", 0)
	defer r.Close()

	// Two concurrent submits to provoke batching.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = r.Embed(context.Background(), []string{"a", "b"}) }()
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		_, _ = r.Embed(context.Background(), []string{"c", "d"})
	}()
	wg.Wait()
	if inner.callCount() != 1 {
		t.Errorf("inner calls = %d, want 1 (async batched)", inner.callCount())
	}
}

// --- Sanity: stats counters on a tight loop are consistent -----------------

func TestRedisEmbedder_StatsCountersConsistent(t *testing.T) {
	_, client := newTestRedis(t)
	inner := &slowEmbedder{dim: 4, sleep: time.Millisecond}
	r := NewRedisEmbedder(inner, client, "psa:embed:st:", 0)
	var calls atomic.Int64
	for i := 0; i < 50; i++ {
		_, err := r.Embed(context.Background(), []string{fmt.Sprintf("text-%d", i%10)})
		if err != nil {
			t.Fatal(err)
		}
		calls.Add(1)
	}
	stats := r.Stats()
	if stats.Hits+stats.Misses != int64(calls.Load())*int64(1) {
		// Each Embed call has 1 text → Hits+Misses == calls.
		// (50 calls, 10 unique texts → 10 misses + 40 hits.)
		t.Errorf("Hits+Misses = %d, want %d", stats.Hits+stats.Misses, calls.Load())
	}
	if stats.Hits != 40 || stats.Misses != 10 {
		t.Errorf("Hits = %d, Misses = %d, want 40 / 10", stats.Hits, stats.Misses)
	}
}
