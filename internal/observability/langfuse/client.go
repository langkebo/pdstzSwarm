// Package langfuse bridges Pentest-Swarm-AI's tracing and LLM
// observability into LangFuse without taking on the SDK dependency.
//
// The bridge has three layers:
//
//   - Client (this file) — HTTP POST to the LangFuse /api/public/ingestion
//     endpoint. Authenticated with HTTP Basic over public+secret keys.
//   - Tracer (tracer.go) — Implements swarm.Tracer; emits trace+span
//     events on StartSpan / EndSpan.
//   - LLMObserver (provider.go) — Decorator over llm.Provider; emits
//     generation events with token usage on every Complete / Stream call.
//
// All three layers are optional. When the bridge is not configured
// (no PublicKey / SecretKey), every constructor returns a noop and
// the cost is one pointer comparison per call.
//
// The bridge batches events in-memory and flushes them on a timer
// or when the batch hits MaxBatchSize. This keeps the per-call
// cost on the agent hot path bounded (we never block on the
// network) and matches the LangFuse ingestion contract.
package langfuse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DefaultEndpoint is the LangFuse cloud ingestion URL. Operators
// running a self-hosted LangFuse override this via Config.Endpoint.
const DefaultEndpoint = "https://cloud.langfuse.com"

// IngestionPath is the LangFuse public ingestion endpoint. Auth is
// HTTP Basic with public_key:secret_key.
const IngestionPath = "/api/public/ingestion"

// DefaultMaxBatchSize is the per-flush cap. LangFuse accepts up to
// ~3,500 events per request in practice; we keep below for safety.
const DefaultMaxBatchSize = 500

// DefaultFlushInterval bounds how long events sit in the in-memory
// queue before the worker flushes them. Short enough that an
// operator reading the LangFuse UI sees data within seconds; long
// enough that a steady agent loop never wakes the worker.
const DefaultFlushInterval = 5 * time.Second

// Config configures the LangFuse client. Empty PublicKey /
// SecretKey disables the bridge (NewClient returns a noop).
type Config struct {
	// PublicKey (a.k.a. project public key). Required for non-noop.
	PublicKey string

	// SecretKey (a.k.a. project secret key). Required for non-noop.
	SecretKey string

	// Endpoint defaults to https://cloud.langfuse.com. Override
	// for self-hosted deployments.
	Endpoint string

	// MaxBatchSize is the per-flush event cap. Default
	// DefaultMaxBatchSize.
	MaxBatchSize int

	// FlushInterval is the periodic flush cadence. Default
	// DefaultFlushInterval.
	FlushInterval time.Duration

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client

	// Now is overridable for tests; defaults to time.Now.
	Now func() time.Time
}

// Client posts events to LangFuse in batches. Construct with
// NewClient; Close the client on shutdown to flush in-flight events.
//
// A Client is safe for concurrent use from many goroutines.
type Client struct {
	cfg     Config
	http    *http.Client
	now     func() time.Time
	enabled bool

	// queue / mu
	mu       sync.Mutex
	queue    []Event
	lastErr  error
	stopCh   chan struct{}
	flushCh  chan struct{}
	closeCh  chan struct{}
	closed   bool
	closeWg  sync.WaitGroup
}

// NewClient returns a configured Client. If PublicKey or SecretKey
// is empty, the returned client is a noop (Enabled() == false); all
// methods are safe to call and are no-ops.
func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.MaxBatchSize <= 0 {
		cfg.MaxBatchSize = DefaultMaxBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}

	enabled := cfg.PublicKey != "" && cfg.SecretKey != ""
	c := &Client{
		cfg:     cfg,
		http:    cfg.HTTPClient,
		now:     cfg.Now,
		enabled: enabled,
		stopCh:  make(chan struct{}),
		flushCh: make(chan struct{}, 1),
		closeCh: make(chan struct{}),
	}
	if enabled {
		c.closeWg.Add(1)
		go c.run()
	}
	return c
}

// Enabled reports whether the client is configured. Callers can
// short-circuit observation code paths on false to avoid even the
// cost of building an Event struct.
func (c *Client) Enabled() bool { return c.enabled }

// Enqueue buffers an event for the next batch flush. Safe for
// concurrent use. If the queue is full, Enqueue drops the event
// and returns a counter increment — we never block the caller on
// the LangFuse path.
func (c *Client) Enqueue(e Event) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	overflow := len(c.queue) >= c.cfg.MaxBatchSize
	if !overflow {
		c.queue = append(c.queue, e)
		// Wake the worker if we're at or past the soft half-full
		// mark so latency stays bounded even on bursty input.
		if len(c.queue) >= c.cfg.MaxBatchSize/2 {
			select {
			case c.flushCh <- struct{}{}:
			default:
			}
		}
	}
	c.mu.Unlock()
}

// Flush forces an immediate send of any buffered events. Safe to
// call from any goroutine; the actual HTTP send happens in the
// worker's loop.
func (c *Client) Flush() {
	if !c.enabled {
		return
	}
	select {
	case c.flushCh <- struct{}{}:
	default:
	}
}

// Close stops the background worker and flushes any pending events.
// Blocks until the worker exits. Safe to call multiple times.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	close(c.stopCh)
	c.closeWg.Wait()
}

// QueueLen returns the current depth of the in-memory queue. Used
// by tests and the /stats endpoint.
func (c *Client) QueueLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

func (c *Client) run() {
	defer c.closeWg.Done()
	t := time.NewTicker(c.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			c.flush()
			return
		case <-c.flushCh:
			c.flush()
		case <-t.C:
			c.flush()
		}
	}
}

func (c *Client) flush() {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}
	batch := c.queue
	c.queue = nil
	c.mu.Unlock()

	if err := c.send(batch); err != nil {
		// We don't requeue — LangFuse may be down, the campaign
		// may be tearing down. Production callers can inspect
		// LastError() for diagnostics. In a future iteration we
		// can add a bounded retry buffer.
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
	}
}

// LastError returns the most recent flush error, or nil. Useful
// for /stats and for tests.
func (c *Client) LastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// send POSTs the batch to LangFuse. Errors are returned to the
// caller (the worker logs / records them).
func (c *Client) send(batch []Event) error {
	if len(batch) == 0 {
		return nil
	}
	body, err := json.Marshal(IngestionPayload{Batch: batch})
	if err != nil {
		return fmt.Errorf("langfuse: marshal: %w", err)
	}

	endpoint, err := url.JoinPath(c.cfg.Endpoint, IngestionPath)
	if err != nil {
		return fmt.Errorf("langfuse: bad endpoint: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("langfuse: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// HTTP Basic with public_key:secret_key is the LangFuse
	// documented auth scheme.
	req.SetBasicAuth(c.cfg.PublicKey, c.cfg.SecretKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("langfuse: http: %w", err)
	}
	defer resp.Body.Close()
	// LangFuse returns 207 with per-event statuses on partial
	// success. We accept anything in [200, 300) and let
	// operators inspect the body if curious.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("langfuse: status %d: %s", resp.StatusCode, string(raw))
	}
	// Drain body so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10))
	return nil
}

// Event is a single LangFuse ingestion event. The union of the
// `Body` variants is dispatched on Body.Type by LangFuse; we
// always set it explicitly so a missing field never causes the
// server to silently drop the event.
//
// Use one of the constructor helpers (Trace, Span, Generation,
// Event) to build an event with the right Body — those keep the
// field set consistent.
type Event struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Body      Body      `json:"body"`
}

// IngestionPayload is the wire format LangFuse expects at
// /api/public/ingestion.
type IngestionPayload struct {
	Batch []Event `json:"batch"`
}

// BodyType enumerates the values of Event.Body.Type.
type BodyType string

const (
	BodyTraceCreate      BodyType = "trace-create"
	BodySpanCreate       BodyType = "span-create"
	BodySpanUpdate       BodyType = "span-update"
	BodyGenerationCreate BodyType = "generation-create"
	BodyGenerationUpdate BodyType = "generation-update"
	BodyEventCreate      BodyType = "event-create"
)

// Body is the union of all per-event-type payloads. The Type
// field discriminates; the other fields are populated based on
// type (LangFuse ignores irrelevant ones).
type Body struct {
	Type BodyType `json:"type"`

	// Common identifiers — set on every event.
	ID        string `json:"id,omitempty"`
	TraceID   string `json:"traceId,omitempty"`
	ParentID  string `json:"parentSpanId,omitempty"`
	Name      string `json:"name,omitempty"`
	StartTime time.Time `json:"startTime,omitempty"`
	EndTime   *time.Time `json:"endTime,omitempty"`

	// Trace-level metadata (BodyTraceCreate only).
	UserID   string `json:"userId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Tags     []string `json:"tags,omitempty"`

	// Span/Event input/output (BodySpanCreate, BodyEventCreate).
	Input  any `json:"input,omitempty"`
	Output any `json:"output,omitempty"`
	Level  string `json:"level,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`

	// Generation-specific (BodyGenerationCreate/Update).
	Model       string `json:"model,omitempty"`
	ModelParams map[string]any `json:"modelParameters,omitempty"`
	Usage       *Usage `json:"usage,omitempty"`
	Prompt      any `json:"prompt,omitempty"`
	Completion  any `json:"completion,omitempty"`
}

// Usage mirrors the token fields LangFuse expects. Names match the
// ingestion schema exactly so we don't have to translate on the
// client.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Total      int `json:"total"`
	Unit       string `json:"unit"` // typically "TOKENS"
	InputCost  float64 `json:"inputCost,omitempty"`
	OutputCost float64 `json:"outputCost,omitempty"`
	TotalCost  float64 `json:"totalCost,omitempty"`
}
