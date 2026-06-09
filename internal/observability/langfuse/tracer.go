package langfuse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm"
)

// Tracer is a swarm.Tracer that emits LangFuse trace + span events.
//
// The tracer creates a new LangFuse "trace" on the first call to
// StartSpan for a given ctx (we key off a sentinel value in the
// ctx). Subsequent calls attach spans to that trace via parentID.
// EndSpan emits a span-update event with the duration + status.
//
// When the underlying Client is not enabled, StartSpan returns
// a no-op EndSpan and the agent loop runs with zero overhead.
//
// Thread-safe; one Tracer is shared across the scheduler and
// every agent.
type Tracer struct {
	client *Client

	// session is the campaign/run ID we attribute all spans to.
	// Optional; if zero, no sessionID is set on the trace.
	session string

	// userID is the operator ID (from config) attached as userID
	// on the trace.
	userID string

	mu        sync.Mutex
	traceByID map[string]string // ctxSpanID -> traceID
}

// NewTracer returns a Tracer backed by the given Client. If the
// client is not enabled, the returned Tracer is still safe to use;
// it just emits no events.
func NewTracer(c *Client, session, userID string) *Tracer {
	return &Tracer{
		client:    c,
		session:   session,
		userID:    userID,
		traceByID: make(map[string]string),
	}
}

// StartSpan implements swarm.Tracer. The first span in a campaign
// becomes the trace root; subsequent spans nest under it via the
// context chain.
//
// attrs is converted to a flat metadata map on the span; the
// "agent.name" attr gets promoted to span.name for nicer UI
// rendering in LangFuse.
func (t *Tracer) StartSpan(ctx context.Context, name string, attrs ...swarm.Attr) (context.Context, swarm.EndSpan) {
	if t == nil || !t.client.Enabled() {
		return ctx, func(error) {}
	}

	spanID := newID("span")
	now := t.client.now()

	// Promote "agent.name" attr to the span name when name is the
	// generic "swarm.agent.handle" placeholder — that's the
	// dominant caller and LangFuse UI shows the name prominently.
	displayName := name
	for _, a := range attrs {
		if a.Key == "agent.name" && a.Value != "" {
			displayName = name + ":" + a.Value
			break
		}
	}

	// Resolve the trace ID. If the parent ctx already carries
	// one, attach to it; otherwise start a new trace.
	traceID, parentID := t.resolveTrace(ctx)

	meta := make(map[string]any, len(attrs))
	for _, a := range attrs {
		meta[a.Key] = a.Value
	}

	body := Body{
		Type:      BodySpanCreate,
		ID:        spanID,
		TraceID:   traceID,
		ParentID:  parentID,
		Name:      displayName,
		StartTime: now,
		Metadata:  meta,
	}
	t.client.Enqueue(Event{
		ID:        spanID,
		Timestamp: now,
		Type:      string(BodySpanCreate),
		Body:      body,
	})

	// Stash the trace↔span mapping in the ctx so child spans
	// (and the EndSpan closure) can find it.
	ctx = withSpanID(ctx, spanID)
	t.mu.Lock()
	t.traceByID[spanID] = traceID
	t.mu.Unlock()

	end := func(err error) {
		endTime := t.client.now()
		duration := endTime.Sub(now)
		_ = duration // reserved for future span metadata; LangFuse
		// derives duration from start/endTime on the server.

		var msg string
		var level string
		if err != nil {
			level = "ERROR"
			msg = err.Error()
		} else {
			level = "DEFAULT"
		}
		updateID := newID("evt")
		t.client.Enqueue(Event{
			ID:        updateID,
			Timestamp: endTime,
			Type:      string(BodySpanUpdate),
			Body: Body{
				Type:          BodySpanUpdate,
				ID:            spanID,
				TraceID:       traceID,
				EndTime:       &endTime,
				Level:         level,
				StatusMessage: msg,
			},
		})

		t.mu.Lock()
		delete(t.traceByID, spanID)
		t.mu.Unlock()
	}
	return ctx, end
}

// resolveTrace looks up the parent span (and therefore trace) in
// the ctx. If absent, this is the root of a new trace — we emit
// a trace-create event.
func (t *Tracer) resolveTrace(ctx context.Context) (traceID, parentID string) {
	if parent, ok := spanIDFrom(ctx); ok {
		t.mu.Lock()
		traceID = t.traceByID[parent]
		t.mu.Unlock()
		if traceID != "" {
			return traceID, parent
		}
	}

	// No parent → start a new trace. Promote the first span's
	// name to the trace name (LangFuse UI shows the trace name).
	traceID = newID("trace")
	t.client.Enqueue(Event{
		ID:        traceID,
		Timestamp: t.client.now(),
		Type:      string(BodyTraceCreate),
		Body: Body{
			Type:      BodyTraceCreate,
			ID:        traceID,
			Name:      "swarm.run",
			UserID:    t.userID,
			SessionID: t.session,
			Metadata:  map[string]any{"source": "pentestswarm"},
			Tags:      []string{"swarm", "pentest"},
		},
	})
	return traceID, ""
}

// --- ctx plumbing ------------------------------------------------------

type ctxKey int

const (
	ctxKeySpanID ctxKey = iota
)

func withSpanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeySpanID, id)
}

func spanIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeySpanID).(string)
	return v, ok
}

// newID returns a 128-bit hex ID with the given prefix. LangFuse
// requires IDs to be 32-char hex (uuid-no-dashes shape) — we use
// 32 hex chars of crypto-random bytes.
func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read should never fail on Linux; if it does, fall
		// back to a time-based ID so we never panic in the agent
		// hot path.
		ts := time.Now().UnixNano()
		return fmt.Sprintf("%s_%016x", prefix, ts)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
