package langfuse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

type recordingTransport struct {
	mu          sync.Mutex
	calls       []recordedRequest
	respondWith func(*recordedRequest) (int, string)
}

type recordedRequest struct {
	Method     string
	Path       string
	Headers    http.Header
	Body       []byte
	ReceivedAt time.Time
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{
		respondWith: func(*recordedRequest) (int, string) {
			return http.StatusOK, `{"successes":[],"errors":[]}`
		},
	}
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	auth := req.Header.Get("Authorization")
	rec := recordedRequest{
		Method:     req.Method,
		Path:       req.URL.Path,
		Headers:    req.Header.Clone(),
		Body:       body,
		ReceivedAt: time.Now(),
	}
	rec.Headers.Set("Authorization", auth) // keep the value

	t.mu.Lock()
	t.calls = append(t.calls, rec)
	idx := len(t.calls) - 1
	responder := t.respondWith
	t.mu.Unlock()

	status, body2 := responder(&t.calls[idx])
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body2)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func (t *recordingTransport) Last() recordedRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls[len(t.calls)-1]
}

func (t *recordingTransport) Calls() []recordedRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]recordedRequest, len(t.calls))
	copy(out, t.calls)
	return out
}

// --- Client tests --------------------------------------------------------

func TestNewClient_NoopWhenKeysMissing(t *testing.T) {
	c := NewClient(Config{})
	if c.Enabled() {
		t.Error("Enabled() = true with no keys, want false")
	}
	if got := c.QueueLen(); got != 0 {
		t.Errorf("QueueLen() = %d, want 0", got)
	}
	c.Enqueue(Event{ID: "x", Type: "x", Body: Body{Type: BodyEventCreate}})
	if got := c.QueueLen(); got != 0 {
		t.Errorf("noop Enqueue should not enqueue, got %d", got)
	}
	// Close should not panic on noop.
	c.Close()
}

func TestClient_DefaultEndpoint(t *testing.T) {
	if DefaultEndpoint != "https://cloud.langfuse.com" {
		t.Errorf("DefaultEndpoint = %q", DefaultEndpoint)
	}
	if IngestionPath != "/api/public/ingestion" {
		t.Errorf("IngestionPath = %q", IngestionPath)
	}
}

func TestClient_PostsBasicAuthAndJSON(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk",
		SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	defer c.Close()

	c.Enqueue(Event{
		ID:        "ev_1",
		Timestamp: time.Unix(1700000000, 0).UTC(),
		Type:      string(BodyEventCreate),
		Body:      Body{Type: BodyEventCreate, ID: "ev_1", Name: "test"},
	})
	c.Flush()

	// Wait briefly for the worker to send.
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("worker didn't POST to ingestion")
	}
	calls := tr.Calls()
	rec := calls[len(calls)-1]
	if rec.Method != http.MethodPost {
		t.Errorf("Method = %s, want POST", rec.Method)
	}
	if rec.Path != "/api/public/ingestion" {
		t.Errorf("Path = %s", rec.Path)
	}
	if rec.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", rec.Headers.Get("Content-Type"))
	}
	auth := rec.Headers.Get("Authorization")
	if auth == "" {
		t.Fatal("Authorization header missing")
	}
	// HTTP Basic of "pk:sk" base64 = cGs6c2s=
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("pk:sk")); auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
	var payload IngestionPayload
	if err := json.Unmarshal(rec.Body, &payload); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if len(payload.Batch) != 1 {
		t.Errorf("batch len = %d, want 1", len(payload.Batch))
	}
	if payload.Batch[0].ID != "ev_1" {
		t.Errorf("ID = %q, want ev_1", payload.Batch[0].ID)
	}
}

func TestClient_BatchSizeBounded(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey:    "pk",
		SecretKey:    "sk",
		HTTPClient:   &http.Client{Transport: tr},
		MaxBatchSize: 100,
	})
	defer c.Close()
	// Enqueue + flush + wait, three iterations. The wait is needed
	// because c.flush() drains the whole queue in one go; rapid
	// enqueues therefore land in a single HTTP request. Spacing
	// the iterations gives the worker time to send the batch
	// before the next enqueue.
	for i := 0; i < 3; i++ {
		c.Enqueue(Event{ID: string(rune('a' + i)), Type: "x", Body: Body{Type: BodyEventCreate}})
		c.Flush()
		// Wait until the worker has sent at least i+1 requests.
		if !waitFor(t, 1*time.Second, func() bool { return len(tr.Calls()) >= i+1 }) {
			t.Fatalf("after %d iterations, got %d calls, want >= %d", i+1, len(tr.Calls()), i+1)
		}
	}
	if got := len(tr.Calls()); got != 3 {
		t.Errorf("got %d HTTP calls, want exactly 3", got)
	}
}

func TestClient_BackoffOnHTTPError(t *testing.T) {
	tr := newRecordingTransport()
	var hits int32
	tr.respondWith = func(*recordedRequest) (int, string) {
		atomic.AddInt32(&hits, 1)
		return http.StatusInternalServerError, `{"error":"server down"}`
	}
	c := NewClient(Config{
		PublicKey:  "pk",
		SecretKey:  "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	defer c.Close()
	c.Enqueue(Event{ID: "x", Type: "x", Body: Body{Type: BodyEventCreate}})
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) > 0 }) {
		t.Fatal("server not hit")
	}
	// Allow a moment for the worker to record the error.
	time.Sleep(50 * time.Millisecond)
	if err := c.LastError(); err == nil {
		t.Error("expected LastError() != nil after 500")
	}
}

func TestClient_CloseFlushesPending(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey:  "pk",
		SecretKey:  "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	c.Enqueue(Event{ID: "x", Type: "x", Body: Body{Type: BodyEventCreate}})
	if c.QueueLen() != 1 {
		t.Fatalf("QueueLen = %d, want 1", c.QueueLen())
	}
	c.Close()
	if c.QueueLen() != 0 {
		t.Errorf("queue should be drained after Close, got %d", c.QueueLen())
	}
	if len(tr.Calls()) == 0 {
		t.Error("Close did not flush")
	}
}

func TestClient_CloseIdempotent(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{PublicKey: "pk", SecretKey: "sk", HTTPClient: &http.Client{Transport: tr}})
	c.Close()
	c.Close() // must not panic
}

// waitFor polls cond until it returns true or the deadline elapses.
// Returns the final value of cond() (true or false). Callers should
// check the return value before using any side effect of cond.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Last poll, but callers must check the return before doing
	// anything stateful. We don't return cond() to avoid extra
	// calls that may panic on empty state.
	return false
}

// --- Tracer tests --------------------------------------------------------

func TestTracer_NoopWhenDisabled(t *testing.T) {
	c := NewClient(Config{})
	tr := NewTracer(c, "camp-1", "user-1")
	ctx, end := tr.StartSpan(context.Background(), "agent.recon", swarmAttr("agent.name", "recon"))
	if ctx == nil {
		t.Error("StartSpan returned nil ctx")
	}
	end(nil) // must not panic
}

func TestTracer_EmitsSpanAndUpdate(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	tracer := NewTracer(c, "camp-1", "user-1")
	defer c.Close()

	ctx, end := tracer.StartSpan(context.Background(), "agent.recon", swarmAttr("agent.name", "recon"))
	if ctx == nil {
		t.Fatal("nil ctx")
	}
	end(nil)

	// Force flush.
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	// Decode the body and look for a span-create + span-update.
	rec := tr.Last()
	var payload IngestionPayload
	if err := json.Unmarshal(rec.Body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawCreate, sawUpdate bool
	for _, e := range payload.Batch {
		switch BodyType(e.Type) {
		case BodySpanCreate:
			sawCreate = true
			if e.Body.Name == "" {
				t.Error("span-create missing name")
			}
		case BodySpanUpdate:
			sawUpdate = true
			if e.Body.EndTime == nil {
				t.Error("span-update missing endTime")
			}
		case BodyTraceCreate:
			if e.Body.SessionID != "camp-1" {
				t.Errorf("trace sessionID = %q", e.Body.SessionID)
			}
			if e.Body.UserID != "user-1" {
				t.Errorf("trace userID = %q", e.Body.UserID)
			}
		}
	}
	if !sawCreate {
		t.Error("missing span-create")
	}
	if !sawUpdate {
		t.Error("missing span-update")
	}
}

func TestTracer_ChildSpanAttachedToParent(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	tracer := NewTracer(c, "sess", "u")
	defer c.Close()

	parent, parentEnd := tracer.StartSpan(context.Background(), "outer")
	_, childEnd := tracer.StartSpan(parent, "inner", swarmAttr("agent.name", "recon"))
	if parent == nil {
		t.Fatal("nil ctx")
	}
	childEnd(nil)
	parentEnd(nil)
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	calls := tr.Calls()
	rec := calls[len(calls)-1]
	var payload IngestionPayload
	_ = json.Unmarshal(rec.Body, &payload)

	// The child span's display name is "inner:recon" because the
	// tracer promotes "agent.name" attrs into the span name.
	var innerSpan *Event
	for i, e := range payload.Batch {
		if BodyType(e.Type) == BodySpanCreate && e.Body.Name == "inner:recon" {
			innerSpan = &payload.Batch[i]
		}
	}
	if innerSpan == nil {
		t.Fatal("inner span not found in batch (looking for name=inner:recon)")
	}
	if innerSpan.Body.ParentID == "" {
		t.Error("inner span missing parentSpanId")
	}
	if innerSpan.Body.TraceID == "" {
		t.Error("inner span missing traceId")
	}
}

func TestTracer_EndSpanWithError(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	tracer := NewTracer(c, "s", "u")
	defer c.Close()
	_, end := tracer.StartSpan(context.Background(), "x")
	end(io.EOF)
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	rec := tr.Last()
	var payload IngestionPayload
	_ = json.Unmarshal(rec.Body, &payload)
	var found bool
	for _, e := range payload.Batch {
		if BodyType(e.Type) == BodySpanUpdate && e.Body.Level == "ERROR" {
			if e.Body.StatusMessage != "EOF" {
				t.Errorf("status message = %q, want EOF", e.Body.StatusMessage)
			}
			found = true
		}
	}
	if !found {
		t.Error("ERROR-level span-update not found")
	}
}

// swarmAttr is a thin constructor over swarm.Attr.
func swarmAttr(k, v string) swarm.Attr {
	return swarm.Attr{Key: k, Value: v}
}

// --- Provider wrapper tests ---------------------------------------------

type fakeProvider struct {
	completeCalls int32
	streamCalls   int32
	completeResp  llm.CompletionResponse
	streamDeltas  []string
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	atomic.AddInt32(&f.completeCalls, 1)
	r := f.completeResp
	return &r, nil
}

func (f *fakeProvider) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	atomic.AddInt32(&f.streamCalls, 1)
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		for _, d := range f.streamDeltas {
			out <- llm.StreamChunk{Delta: d}
		}
		out <- llm.StreamChunk{Done: true}
	}()
	return out, nil
}

func (f *fakeProvider) HealthCheck(ctx context.Context) error { return nil }
func (f *fakeProvider) ModelName() string                     { return "fake-model" }
func (f *fakeProvider) ContextWindow() int                    { return 8192 }
func (f *fakeProvider) SupportsToolUse() bool                 { return true }

func TestObservingProvider_NoopWhenDisabled(t *testing.T) {
	c := NewClient(Config{})
	inner := &fakeProvider{
		completeResp: llm.CompletionResponse{Content: "ok", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	op := NewObservingProvider(inner, c)
	resp, err := op.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q", resp.Content)
	}
	if inner.completeCalls != 1 {
		t.Errorf("inner not called")
	}
}

func TestObservingProvider_CompleteRecordsGeneration(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	inner := &fakeProvider{
		completeResp: llm.CompletionResponse{Content: "ok", Usage: llm.Usage{InputTokens: 12, OutputTokens: 7}},
	}
	op := NewObservingProvider(inner, c)
	defer c.Close()

	resp, err := op.Complete(context.Background(), llm.CompletionRequest{
		MaxTokens: 100, Temperature: 0.3,
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q", resp.Content)
	}
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	rec := tr.Last()
	var payload IngestionPayload
	if err := json.Unmarshal(rec.Body, &payload); err != nil {
		t.Fatal(err)
	}
	var sawCreate, sawUpdate bool
	for _, e := range payload.Batch {
		switch BodyType(e.Type) {
		case BodyGenerationCreate:
			sawCreate = true
			if e.Body.Model != "fake-model" {
				t.Errorf("Model = %q, want fake-model", e.Body.Model)
			}
			// System prompt length, message count etc captured.
			pf, ok := e.Body.Prompt.(map[string]any)
			if !ok {
				t.Errorf("Prompt = %T", e.Body.Prompt)
			} else if pf["messages"] == nil {
				t.Error("Prompt missing messages key")
			}
		case BodyGenerationUpdate:
			sawUpdate = true
			if e.Body.Usage == nil {
				t.Error("Usage nil")
			} else if e.Body.Usage.Input != 12 || e.Body.Usage.Output != 7 || e.Body.Usage.Total != 19 {
				t.Errorf("Usage = %+v, want 12/7/19", e.Body.Usage)
			}
			if e.Body.Completion != "ok" {
				t.Errorf("Completion = %v, want ok", e.Body.Completion)
			}
		}
	}
	if !sawCreate {
		t.Error("missing generation-create")
	}
	if !sawUpdate {
		t.Error("missing generation-update")
	}
}

func TestObservingProvider_StreamRecordsGeneration(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	inner := &fakeProvider{streamDeltas: []string{"Hello", " world"}}
	op := NewObservingProvider(inner, c)
	defer c.Close()

	stream, err := op.Stream(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	for chunk := range stream {
		got.WriteString(chunk.Delta)
	}
	if got.String() != "Hello world" {
		t.Errorf("stream content = %q", got.String())
	}
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	rec := tr.Last()
	var payload IngestionPayload
	_ = json.Unmarshal(rec.Body, &payload)
	var found bool
	for _, e := range payload.Batch {
		if BodyType(e.Type) == BodyGenerationUpdate {
			if c, ok := e.Body.Completion.(string); ok && c == "Hello world" {
				found = true
			}
		}
	}
	if !found {
		t.Error("stream completion not recorded as 'Hello world'")
	}
}

func TestObservingProvider_StreamErrorPropagates(t *testing.T) {
	c := NewClient(Config{}) // disabled
	inner := &fakeProvider{streamDeltas: []string{"x"}}
	op := NewObservingProvider(inner, c)
	stream, err := op.Stream(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range stream { // drain
	}
}

func TestPromptFingerprint_NeverExposesContent(t *testing.T) {
	// The fingerprint must NOT carry message bodies. Regression:
	// the operator's prompt may include harvested creds or target
	// strings; logging them by accident is a data leak.
	req := llm.CompletionRequest{
		SystemPrompt: "secret-privkey-AAAA",
		Messages: []llm.Message{
			{Role: "user", Content: "pwn-the-server-with-privkey-AAAA"},
		},
	}
	fp := promptFingerprint(req)
	if bytes, _ := json.Marshal(fp); strings.Contains(string(bytes), "AAAA") {
		t.Errorf("promptFingerprint leaks content: %s", string(bytes))
	}
}

// --- Finding observer tests ---------------------------------------------

func TestFindingObserver_NoopWhenDisabled(t *testing.T) {
	c := NewClient(Config{})
	fo := NewFindingObserver(c)
	fo.OnFinding(sampleFinding(t))
	// No panic, no events enqueued.
}

func TestFindingObserver_EmitsEventCreate(t *testing.T) {
	tr := newRecordingTransport()
	c := NewClient(Config{
		PublicKey: "pk", SecretKey: "sk",
		HTTPClient: &http.Client{Transport: tr},
	})
	fo := NewFindingObserver(c)
	defer c.Close()
	fo.OnFinding(sampleFinding(t))
	c.Flush()
	if !waitFor(t, 2*time.Second, func() bool { return len(tr.Calls()) > 0 }) {
		t.Fatal("no flush")
	}
	rec := tr.Last()
	var payload IngestionPayload
	if err := json.Unmarshal(rec.Body, &payload); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range payload.Batch {
		if BodyType(e.Type) == BodyEventCreate {
			if !strings.Contains(e.Body.Name, "EXPLOIT_CHAIN") {
				t.Errorf("name = %q, want substring EXPLOIT_CHAIN", e.Body.Name)
			}
			if e.Body.Level != "WARNING" {
				t.Errorf("level = %q, want WARNING for exploit-chain", e.Body.Level)
			}
			found = true
		}
	}
	if !found {
		t.Error("event-create not found")
	}
}

// sampleFinding builds a representative blackboard.Finding for
// the observer tests.
func sampleFinding(t *testing.T) blackboard.Finding {
	t.Helper()
	return blackboard.Finding{
		ID:         uuidMust(t),
		CampaignID: uuidMust(t),
		AgentName:  "exploiter",
		Type:       blackboard.TypeExploitChain,
		Target:     "https://target.example/admin",
		Data:       []byte(`{"chain":["cve-1","cve-2"]}`),
		PheromoneBase: 0.9,
		HalfLifeSec:   3600,
		CreatedAt:     time.Unix(1700000000, 0).UTC(),
	}
}

func uuidMust(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTruncateForLog_UTF8Safe(t *testing.T) {
	// Multi-byte emoji should not be cut mid-rune.
	s := "secret🔑🔑🔑🔑"
	got := truncateForLog(s, 8) // cuts inside the first emoji
	// Must not contain a broken UTF-8 sequence.
	for _, r := range got {
		if r == '\uFFFD' {
			t.Errorf("truncate introduced replacement rune: %q", got)
		}
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("missing ellipsis: %q", got)
	}
}

func TestTruncateForLog_ShortUntouched(t *testing.T) {
	if got := truncateForLog("hello", 100); got != "hello" {
		t.Errorf("short string altered: %q", got)
	}
}
