package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// AsyncEmbedder wraps an inner Embedder with a fixed-size worker pool
// and a bounded queue so the calling goroutine can submit batches and
// return immediately. The classic use case is the blackboard Write
// path, which would otherwise block on a network round-trip per
// finding; under load the worker pool collapses N near-simultaneous
// submits into 1 upstream call (see BatchSize / BatchTimeout).
//
// API surface:
//
//   - Embed(ctx, texts) is sugar for Submit+Wait; existing call
//     sites that expect a synchronous answer keep working.
//   - Submit(ctx, texts) returns one *Future per input text. The
//     caller can either block on the future or hand it to a
//     background writer (e.g. an UPDATE-after-the-fact loop).
//   - Wait(ctx) blocks until every in-flight job has resolved,
//     which is the right behaviour on shutdown / campaign end.
//
// The wrapper is a transparent Decorator — the inner embedder
// (OpenAI / Ollama / Noop / CachedEmbedder / …) is consulted only
// when the worker pool is ready to make a real call, and the
// underlying contract is unchanged. HealthCheck / Dimensions /
// ModelName are forwarded.
type AsyncEmbedder struct {
	inner Embedder

	// Configuration (immutable after construction).
	workers      int
	batchSize    int
	batchTimeout time.Duration
	queueSize    int
	submitWait   time.Duration // 0 → wait forever

	// Queue of pending jobs. Each job carries its own slice of
	// texts and a parallel slice of futures to resolve on the
	// worker side. The job is a unit of "batching": the worker
	// may opportunistically merge N small jobs into one upstream
	// call when they arrive within batchTimeout of each other.
	jobs chan *embedJob

	wg     sync.WaitGroup
	closed atomic.Bool

	closeOnce sync.Once

	// Stats. Exposed via Stats() for observability and tests.
	statSubmitted atomic.Int64
	statCompleted atomic.Int64
	statFailed    atomic.Int64
	statBatches   atomic.Int64
	statMerged    atomic.Int64 // extra texts folded into a batch beyond the first
	statQueueFull atomic.Int64
	statDrained   atomic.Int64
}

// embedJob is a single submission. Multiple jobs may be merged into
// one upstream call inside the worker loop; in that case the worker
// iterates each sub-job and resolves its futures in order.
type embedJob struct {
	texts   []string
	futures []*embedFuture
}

// embedFuture is the per-text result handle. A future is shared
// between the caller (which awaits it) and the worker (which
// closes it). sync.Once protects the close path from the
// error-from-merged-batch case where the same future may be
// resolved twice by accident.
type embedFuture struct {
	done chan struct{}
	vec  []float32
	err  error

	once sync.Once
}

func newEmbedFuture() *embedFuture {
	return &embedFuture{done: make(chan struct{})}
}

// Get blocks until the future resolves or ctx is cancelled. Returns
// the resolved vector or the first non-nil error.
func (f *embedFuture) Get(ctx context.Context) ([]float32, error) {
	if f == nil {
		return nil, errors.New("embed future: nil handle")
	}
	select {
	case <-f.done:
		return f.vec, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Done returns a channel closed when the future resolves. Useful
// for select-driven fan-in ("wait for any of N futures").
func (f *embedFuture) Done() <-chan struct{} { return f.done }

// resolve is called by the worker exactly once per future.
func (f *embedFuture) resolve(vec []float32, err error) {
	f.once.Do(func() {
		f.vec, f.err = vec, err
		close(f.done)
	})
}

// AsyncConfig is the wire-shape the cmd/ startup code populates
// from llm.embeddings.async.* in config.yaml. The field tags
// match viper's mapstructure expectations.
type AsyncConfig struct {
	// Enabled is the kill-switch. When false, NewAsyncEmbedderFromConfig
	// returns the inner embedder unchanged.
	Enabled bool `mapstructure:"enabled"`
	// Workers is the number of goroutines consuming the queue.
	// 0 → 4. Values above the inner embedder's effective
	// concurrency limit (e.g. OpenAI's 100-RPS cap) waste
	// goroutines; values below starve it.
	Workers int `mapstructure:"workers"`
	// BatchSize caps how many texts a single upstream call can
	// carry. 0 → 32. The worker will not split a single submit
	// across multiple upstream calls; it will only merge across
	// submits.
	BatchSize int `mapstructure:"batch_size"`
	// BatchTimeout is the wait window for opportunistic merging.
	// 0 → 50ms. Set to 0 to disable merging (each submit becomes
	// its own upstream call), which is the right choice for
	// interactive single-text latency.
	BatchTimeout time.Duration `mapstructure:"batch_timeout"`
	// QueueSize is the buffered channel length. 0 → 1024.
	// A negative value means "unbuffered" — every Submit
	// blocks until a worker is ready to pick it up. This is
	// the right setting for tests that need to observe
	// backpressure deterministically.
	QueueSize int `mapstructure:"queue_size"`
	// SubmitWait is how long Submit blocks when the queue is
	// full. 0 → wait forever; in practice callers should set a
	// short timeout so a slow embedder doesn't wedge writers.
	SubmitWait time.Duration `mapstructure:"submit_wait"`
}

// NewAsyncEmbedder wraps inner with a worker pool. The pool is
// started immediately; callers must invoke Close to drain it on
// shutdown.
//
// Zero values in cfg are replaced with production defaults so a
// caller can pass `AsyncConfig{Enabled: true}` and get a
// reasonable pool.
func NewAsyncEmbedder(inner Embedder, cfg AsyncConfig) *AsyncEmbedder {
	if inner == nil {
		// A nil inner is a programming error; surface it loudly
		// rather than silently no-op (which would mask wiring
		// bugs in cmd/ startup).
		panic("llm.NewAsyncEmbedder: inner embedder is nil")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 50 * time.Millisecond
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 1024
	}
	if cfg.QueueSize < 0 {
		// -1 → unbuffered; the caller opted into "block
		// until a worker is ready". Don't fall through to
		// the default.
		cfg.QueueSize = -1
	}
	a := &AsyncEmbedder{
		inner:        inner,
		workers:      cfg.Workers,
		batchSize:    cfg.BatchSize,
		batchTimeout: cfg.BatchTimeout,
		queueSize:    cfg.QueueSize,
		submitWait:   cfg.SubmitWait,
	}
	if cfg.QueueSize < 0 {
		a.jobs = make(chan *embedJob)
	} else {
		a.jobs = make(chan *embedJob, cfg.QueueSize)
	}
	for i := 0; i < a.workers; i++ {
		a.wg.Add(1)
		go a.workerLoop(i)
	}
	return a
}

// NewAsyncEmbedderFromConfig is the kill-switch wrapper. When
// cfg.Enabled is false it returns inner unchanged so the call
// site can chain unconditionally:
//
//	llm.NewAsyncEmbedderFromConfig(llm.NewCachedEmbedder(...), cfg)
func NewAsyncEmbedderFromConfig(inner Embedder, cfg AsyncConfig) Embedder {
	if !cfg.Enabled {
		return inner
	}
	return NewAsyncEmbedder(inner, cfg)
}

// Dimensions forwards to the inner embedder.
func (a *AsyncEmbedder) Dimensions() int { return a.inner.Dimensions() }

// ModelName forwards to the inner embedder.
func (a *AsyncEmbedder) ModelName() string { return a.inner.ModelName() }

// HealthCheck forwards to the inner embedder. The pool itself is
// always "healthy" once started; an inner-embedder failure is
// reported to the caller (and visible in Stats).
func (a *AsyncEmbedder) HealthCheck(ctx context.Context) error {
	return a.inner.HealthCheck(ctx)
}

// Embed is the sugar path: submit and wait for every future.
// Existing call sites that expect a synchronous answer keep
// working; the only observable difference from the inner embedder
// is the opportunistic batching across concurrent calls.
func (a *AsyncEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return a.EmbedBatch(ctx, texts)
}

// EmbedBatch is sugar for Embed (kept for interface symmetry).
func (a *AsyncEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	futures, err := a.Submit(ctx, texts)
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, f := range futures {
		vec, err := f.Get(ctx)
		if err != nil {
			return nil, fmt.Errorf("async embedder: %w", err)
		}
		out[i] = vec
	}
	return out, nil
}

// EmbedRequest delegates to the inner embedder. The wrapper's
// queueing is only useful for the hot-path Embed call; the
// per-call override path is rare and synchronous callers can
// afford a network round-trip.
func (a *AsyncEmbedder) EmbedRequest(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return a.inner.EmbedRequest(ctx, req)
}

// Submit enqueues texts and returns one *embedFuture per input.
// The futures are returned in the same order as the input texts;
// the caller can await them in any order (Get / Done).
//
// Backpressure: if the queue is full, Submit blocks for at most
// cfg.SubmitWait. After that it returns ErrQueueFull and the
// caller can fall back to a synchronous inner call or shed load.
//
// Submit is safe to call from many goroutines.
func (a *AsyncEmbedder) Submit(ctx context.Context, texts []string) ([]*embedFuture, error) {
	if a.closed.Load() {
		return nil, errors.New("async embedder: closed")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > a.batchSize {
		// A single submit larger than BatchSize must be split —
		// the worker won't split a job. We split here and return
		// the concatenation of futures.
		futures := make([]*embedFuture, 0, len(texts))
		for start := 0; start < len(texts); start += a.batchSize {
			end := start + a.batchSize
			if end > len(texts) {
				end = len(texts)
			}
			sub, err := a.Submit(ctx, texts[start:end])
			if err != nil {
				return nil, err
			}
			futures = append(futures, sub...)
		}
		return futures, nil
	}
	futures := make([]*embedFuture, len(texts))
	for i := range texts {
		futures[i] = newEmbedFuture()
	}
	job := &embedJob{texts: texts, futures: futures}
	a.statSubmitted.Add(int64(len(texts)))

	if a.submitWait <= 0 {
		// Block forever — or until ctx is cancelled, which is
		// usually the caller's best signal that they don't want
		// to wait any longer.
		select {
		case a.jobs <- job:
		case <-ctx.Done():
			a.resolveAll(futures, nil, ctx.Err())
			return nil, ctx.Err()
		}
	} else {
		timer := time.NewTimer(a.submitWait)
		select {
		case a.jobs <- job:
			timer.Stop()
		case <-timer.C:
			a.statQueueFull.Add(1)
			a.resolveAll(futures, nil, errors.New("async embedder: queue full"))
			return nil, ErrQueueFull
		case <-ctx.Done():
			timer.Stop()
			a.resolveAll(futures, nil, ctx.Err())
			return nil, ctx.Err()
		}
	}
	return futures, nil
}

// resolveAll is the failure-path helper that closes every future
// in `futures` with the same error. Used when the submit itself
// fails (ctx cancelled, queue full) so the caller can `select` on
// the returned futures and never deadlock.
func (a *AsyncEmbedder) resolveAll(futures []*embedFuture, _ [][]float32, err error) {
	for _, f := range futures {
		f.resolve(nil, err)
	}
}

// ErrQueueFull is returned by Submit when the bounded buffer
// is exhausted and the submitWait elapses. Callers may treat
// this as a signal to shed load (skip embedding for this
// finding, retry later, etc.) rather than block indefinitely.
var ErrQueueFull = errors.New("async embedder: queue full")

// Wait blocks until every job that was already in the queue (or
// the worker is currently processing) has resolved. New submits
// that arrive during Wait are NOT awaited — callers who want
// "drain everything" should stop submitting first, then call Wait
// with a timeout.
//
// The intent is to give a clean "flush" semantic at campaign end:
// the caller has stopped emitting new findings, the in-flight
// ones finish, and the embedder has nothing more to do.
func (a *AsyncEmbedder) Wait(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if a.inFlight() == 0 {
			a.statDrained.Add(1)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// inFlight returns submitted - completed - failed. It's an
// estimate (counters are not atomic snapshot) but good enough
// for the drain loop. We only care that the count is zero
// *eventually*.
func (a *AsyncEmbedder) inFlight() int64 {
	s := a.statSubmitted.Load()
	return s - a.statCompleted.Load() - a.statFailed.Load()
}

// Close drains the queue (Wait with a short grace period) and
// signals the worker goroutines to exit. Idempotent.
func (a *AsyncEmbedder) Close() error {
	var firstErr error
	a.closeOnce.Do(func() {
		a.closed.Store(true)
		// Close the jobs channel; workers will drain any
		// pending entries then exit.
		close(a.jobs)
		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			firstErr = errors.New("async embedder: workers did not exit within 2s")
		}
	})
	return firstErr
}

// workerLoop is the per-worker body. It opportunistically merges
// consecutive jobs into a single upstream call up to BatchSize.
func (a *AsyncEmbedder) workerLoop(id int) {
	defer a.wg.Done()
	for job := range a.jobs {
		a.processOne(job, id)
	}
}

// processOne handles a single job, optionally merging in any
// extra jobs that arrive within batchTimeout. The merged
// batch is dispatched as a single inner.Embed call; results
// are written back to the per-text futures in input order.
func (a *AsyncEmbedder) processOne(first *embedJob, workerID int) {
	batch := first
	merged := 0
	deadline := time.NewTimer(a.batchTimeout)
	defer deadline.Stop()
	// Try to merge up to BatchSize - len(first.texts) more
	// texts from the queue. We block on the channel up to
	// BatchTimeout but break out as soon as a job would push us
	// over BatchSize (splitting a job across batches is
	// forbidden — semantics get confusing).
	for len(batch.texts) < a.batchSize {
		select {
		case extra, ok := <-a.jobs:
			if !ok {
				// Channel closed; send what we have.
				a.dispatch(batch, workerID)
				return
			}
			room := a.batchSize - len(batch.texts)
			if len(extra.texts) > room {
				// Won't fit. The naive choice is to
				// drop extra on the floor — but that
				// would leak the futures. Instead
				// dispatch what we have, then loop
				// back and dispatch extra as the
				// next job.
				a.dispatch(batch, workerID)
				a.processOne(extra, workerID)
				return
			}
			batch.texts = append(batch.texts, extra.texts...)
			batch.futures = append(batch.futures, extra.futures...)
			merged++
		case <-deadline.C:
			goto dispatch
		}
	}
dispatch:
	if merged > 0 {
		a.statMerged.Add(int64(merged))
	}
	a.dispatch(batch, workerID)
}

// dispatch calls the inner embedder and resolves every future
// in the batch in order.
func (a *AsyncEmbedder) dispatch(batch *embedJob, workerID int) {
	a.statBatches.Add(1)
	vecs, err := a.inner.Embed(context.Background(), batch.texts)
	if err != nil {
		a.statFailed.Add(int64(len(batch.texts)))
		for _, f := range batch.futures {
			f.resolve(nil, fmt.Errorf("async embedder (worker %d): %w", workerID, err))
		}
		return
	}
	if len(vecs) != len(batch.texts) {
		err := fmt.Errorf("async embedder (worker %d): inner returned %d vectors for %d inputs", workerID, len(vecs), len(batch.texts))
		a.statFailed.Add(int64(len(batch.futures)))
		for _, f := range batch.futures {
			f.resolve(nil, err)
		}
		return
	}
	a.statCompleted.Add(int64(len(vecs)))
	for i, f := range batch.futures {
		f.resolve(vecs[i], nil)
	}
}

// AsyncStats is a point-in-time snapshot of the worker's counters.
type AsyncStats struct {
	Submitted  int64
	Completed  int64
	Failed     int64
	Batches    int64
	Merged     int64 // number of jobs folded into another worker's batch
	QueueFull  int64
	Drained    int64
	QueueDepth int // current channel length
	Workers    int
}

// Stats returns a snapshot. Useful for observability and tests.
func (a *AsyncEmbedder) Stats() AsyncStats {
	return AsyncStats{
		Submitted:  a.statSubmitted.Load(),
		Completed:  a.statCompleted.Load(),
		Failed:     a.statFailed.Load(),
		Batches:    a.statBatches.Load(),
		Merged:     a.statMerged.Load(),
		QueueFull:  a.statQueueFull.Load(),
		Drained:    a.statDrained.Load(),
		QueueDepth: len(a.jobs),
		Workers:    a.workers,
	}
}
