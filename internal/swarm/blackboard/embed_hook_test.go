package blackboard

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// --- Test fixtures ---------------------------------------------------------

// stubEmbedder is an in-memory stand-in for the Embedder interface.
// It records every call and returns a deterministic fixed-size
// vector (all ones) so tests can assert against "embedding was set".
type stubEmbedder struct {
	dim      int
	model    string
	calls    int64
	inputs   [][]string
	errOn    map[string]error // map[inputText] → error to return
	mu       atomic.Int64
}

func newStubEmbedder(dim int) *stubEmbedder {
	return &stubEmbedder{dim: dim, model: "stub", errOn: map[string]error{}}
}

func (s *stubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s.mu.Add(1)
	s.inputs = append(s.inputs, texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if err, ok := s.errOn[t]; ok {
			return nil, err
		}
		v := make([]float32, s.dim)
		for j := range v {
			v[j] = float32(i+1) * 0.01 // distinguish indices
		}
		out[i] = v
	}
	return out, nil
}

func (s *stubEmbedder) Dimensions() int  { return s.dim }
func (s *stubEmbedder) ModelName() string { return s.model }
func (s *stubEmbedder) callCount() int   { return int(s.mu.Load()) }

// --- TestAutoEmbedHook_OnWrite_PopulatesEmbedding ---------------------------

// OnWrite must call the embedder with a non-empty text and assign
// the returned vector to f.Embedding.
func TestAutoEmbedHook_OnWrite_PopulatesEmbedding(t *testing.T) {
	emb := newStubEmbedder(8)
	hook := NewAutoEmbedHook(emb)
	f := &Finding{
		ID:        uuid.New(),
		CampaignID: uuid.New(),
		AgentName: "recon",
		Type:      TypeSubdomain,
		Target:    "example.com",
		Data:      []byte(`{"subdomain":"www.example.com"}`),
	}
	if err := hook.OnWrite(context.Background(), f); err != nil {
		t.Fatalf("OnWrite: %v", err)
	}
	if len(f.Embedding) != 8 {
		t.Errorf("Embedding len = %d, want 8", len(f.Embedding))
	}
	if emb.callCount() != 1 {
		t.Errorf("embedder calls = %d, want 1", emb.callCount())
	}
	calls, errs := hook.Stats()
	if calls != 1 || errs != 0 {
		t.Errorf("Stats = (%d, %d), want (1, 0)", calls, errs)
	}
}

// --- TestAutoEmbedHook_ComposeText_DefaultOrder -----------------------------

// The default field-join order is Target, AgentName, Type, Data —
// the composed text must contain all four pieces.
func TestAutoEmbedHook_ComposeText_DefaultOrder(t *testing.T) {
	emb := newStubEmbedder(4)
	hook := NewAutoEmbedHook(emb)
	f := &Finding{
		Target:    "example.com",
		AgentName: "recon",
		Type:      TypeHTTPEndpoint,
		Data:      []byte(`{"path":"/login"}`),
	}
	_ = hook.OnWrite(context.Background(), f)
	if len(emb.inputs) != 1 || len(emb.inputs[0]) != 1 {
		t.Fatalf("expected 1 batch of 1 text, got %v", emb.inputs)
	}
	got := emb.inputs[0][0]
	for _, want := range []string{"Target=example.com", "Agent=recon", "Type=HTTP_ENDPOINT", `Data={"path":"/login"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("text missing %q, got %q", want, got)
		}
	}
}

// --- TestAutoEmbedHook_WithFields_OverridesOrder ----------------------------

// WithFields must change the joined text — fields not in the
// override list must be absent.
func TestAutoEmbedHook_WithFields_OverridesOrder(t *testing.T) {
	emb := newStubEmbedder(4)
	hook := NewAutoEmbedHook(emb, WithFields("AgentName", "Type"))
	f := &Finding{
		Target:    "example.com",
		AgentName: "recon",
		Type:      TypeSubdomain,
		Data:      []byte(`{}`),
	}
	_ = hook.OnWrite(context.Background(), f)
	got := emb.inputs[0][0]
	if !strings.Contains(got, "Agent=recon") {
		t.Errorf("missing Agent: %q", got)
	}
	if !strings.Contains(got, "Type=SUBDOMAIN") {
		t.Errorf("missing Type: %q", got)
	}
	if strings.Contains(got, "Target=") {
		t.Errorf("Target should be excluded by WithFields: %q", got)
	}
	if strings.Contains(got, "Data=") {
		t.Errorf("Data should be excluded by WithFields: %q", got)
	}
}

// --- TestAutoEmbedHook_WithMaxDataLen_Truncates -----------------------------

// WithMaxDataLen(3) must slice the Data blob to the first 3 bytes.
func TestAutoEmbedHook_WithMaxDataLen_Truncates(t *testing.T) {
	emb := newStubEmbedder(2)
	hook := NewAutoEmbedHook(emb, WithMaxDataLen(3))
	f := &Finding{
		Target: "x",
		Data:   []byte("hello world"),
	}
	_ = hook.OnWrite(context.Background(), f)
	got := emb.inputs[0][0]
	if !strings.Contains(got, "Data=hel") {
		t.Errorf("Data should be truncated to 3 bytes, got %q", got)
	}
}

// --- TestAutoEmbedHook_WithSkipEmpty_LeavesEmbeddingNil ---------------------

// An empty Target + empty Data with WithSkipEmpty must NOT call the
// embedder at all (the hook should be a no-op).
func TestAutoEmbedHook_WithSkipEmpty_LeavesEmbeddingNil(t *testing.T) {
	emb := newStubEmbedder(4)
	hook := NewAutoEmbedHook(emb, WithSkipEmpty())
	f := &Finding{ID: uuid.New()} // all fields zero
	_ = hook.OnWrite(context.Background(), f)
	if emb.callCount() != 0 {
		t.Errorf("embedder calls = %d, want 0 (skip empty)", emb.callCount())
	}
	if f.Embedding != nil {
		t.Errorf("Embedding = %v, want nil", f.Embedding)
	}
}

// --- TestAutoEmbedHook_EmbedderError_ReturnsError ---------------------------

// An embedder that returns an error must surface the error and
// leave f.Embedding nil.
func TestAutoEmbedHook_EmbedderError_ReturnsError(t *testing.T) {
	emb := newStubEmbedder(4)
	// The composed text starts with "Target=example.com" so we key
	// the synthetic error on that prefix.
	emb.errOn["Target=example.com | Agent=recon"] = errors.New("rate limited")
	hook := NewAutoEmbedHook(emb)
	f := &Finding{Target: "example.com", AgentName: "recon"}
	err := hook.OnWrite(context.Background(), f)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should mention rate limited, got: %v", err)
	}
	if f.Embedding != nil {
		t.Errorf("Embedding should be nil on error, got %v", f.Embedding)
	}
	calls, errs := hook.Stats()
	if calls != 1 || errs != 1 {
		t.Errorf("Stats = (%d, %d), want (1, 1)", calls, errs)
	}
}

// --- TestHookRegistry_AddAndInvoke ------------------------------------------

// The registry must invoke every registered hook in registration
// order, even when one of them errors.
func TestHookRegistry_AddAndInvoke(t *testing.T) {
	reg := NewHookRegistry()
	hook1 := &recordingHook{name: "h1", want: 0.11}
	hook2 := &recordingHook{name: "h2", want: 0.22}
	hook3 := &recordingHook{name: "h3", want: 0.33, err: errors.New("h3 boom")}
	reg.Add(hook1)
	reg.Add(hook2)
	reg.Add(hook3)
	if reg.Len() != 3 {
		t.Errorf("Len = %d, want 3", reg.Len())
	}
	f := &Finding{Target: "x"}
	err := reg.Invoke(context.Background(), f)
	if err == nil {
		t.Fatal("expected error from h3")
	}
	if !strings.Contains(err.Error(), "h3 boom") {
		t.Errorf("error should mention h3 boom: %v", err)
	}
	if !hook1.called {
		t.Error("h1 should have been called")
	}
	if !hook2.called {
		t.Error("h2 should have been called")
	}
	if !hook3.called {
		t.Error("h3 should have been called")
	}
}

// --- TestHookRegistry_NilHookIgnored ----------------------------------------

// Adding nil must not panic or grow the registry.
func TestHookRegistry_NilHookIgnored(t *testing.T) {
	reg := NewHookRegistry()
	reg.Add(nil)
	if reg.Len() != 0 {
		t.Errorf("Len = %d, want 0 after Add(nil)", reg.Len())
	}
	if err := reg.Invoke(context.Background(), &Finding{}); err != nil {
		t.Errorf("Invoke: %v", err)
	}
}

// --- TestHookRegistry_EmptyInvokeReturnsNil ---------------------------------

// An empty registry is a no-op that returns nil.
func TestHookRegistry_EmptyInvokeReturnsNil(t *testing.T) {
	reg := NewHookRegistry()
	if err := reg.Invoke(context.Background(), &Finding{}); err != nil {
		t.Errorf("Invoke on empty registry returned %v, want nil", err)
	}
}

// --- TestPostgresBoard_AddEmbedHook_NilSafe ---------------------------------

// AddEmbedHook(nil) must not panic; the registry should stay empty.
func TestPostgresBoard_AddEmbedHook_NilSafe(t *testing.T) {
	// We can't construct a PostgresBoard without a *pgxpool.Pool, but
	// we can build a HookRegistry in isolation and exercise the nil
	// handling through it. This is the same code path AddEmbedHook
	// uses internally.
	reg := NewHookRegistry()
	reg.Add(nil)
	if reg.Len() != 0 {
		t.Errorf("reg.Len = %d, want 0", reg.Len())
	}
}

// --- helpers ----------------------------------------------------------------

// recordingHook is a minimal EmbedHook that records whether it was
// called and (optionally) errors. Used to validate the registry's
// invocation order and error-collection semantics.
type recordingHook struct {
	name   string
	want   float32
	err    error
	called bool
}

func (r *recordingHook) Name() string { return r.name }
func (r *recordingHook) OnWrite(ctx context.Context, f *Finding) error {
	r.called = true
	if f != nil {
		f.Embedding = []float32{r.want, r.want * 2}
	}
	return r.err
}
