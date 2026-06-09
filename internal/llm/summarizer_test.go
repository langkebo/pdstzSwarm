package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- mocks ------------------------------------------------------------------

// mockProvider records the most recent CompletionRequest and replies
// with the canned response (or a simple deterministic string when nil).
type mockProvider struct {
	contextWindow  int
	modelName      string
	lastReq        *CompletionRequest
	completeResp   string
	completeErr    error
	supportsTools  bool
}

func (m *mockProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	m.lastReq = &req
	if m.completeErr != nil {
		return nil, m.completeErr
	}
	content := m.completeResp
	if content == "" {
		content = "summary text"
	}
	return &CompletionResponse{Content: content, StopReason: "end_turn"}, nil
}
func (m *mockProvider) Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Done: true}
	close(ch)
	return ch, nil
}
func (m *mockProvider) HealthCheck(ctx context.Context) error { return nil }
func (m *mockProvider) ModelName() string                    { return m.modelName }
func (m *mockProvider) ContextWindow() int                   { return m.contextWindow }
func (m *mockProvider) SupportsToolUse() bool                { return m.supportsTools }

// --- TestSummarizer_NeedsSummarization_BelowThreshold ------------------------

// Few short messages with a generous context window must not trigger
// summarisation.
func TestSummarizer_NeedsSummarization_BelowThreshold(t *testing.T) {
	p := &mockProvider{contextWindow: 1000000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{TriggerAtRatio: 0.7})

	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	if s.NeedsSummarization(msgs) {
		t.Error("NeedsSummarization should be false for short messages")
	}
}

// --- TestSummarizer_NeedsSummarization_AboveThreshold ------------------------

// Many long messages must trigger summarisation at 70% of the window.
func TestSummarizer_NeedsSummarization_AboveThreshold(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"} // 700 token threshold
	s := NewSummarizer(p, SummarizerConfig{TriggerAtRatio: 0.7})

	// 80 messages × ~25 tokens each (100 chars) = ~2000 tokens >> 700.
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			Role:    "user",
			Content: strings.Repeat("a", 100),
		})
	}
	if !s.NeedsSummarization(msgs) {
		t.Error("NeedsSummarization should be true for 80 × 100-char messages")
	}
}

// --- TestSummarizer_Summarize_KeepsRecentHalf --------------------------------

// 10 messages with keep_recent_ratio=0.5 → 5 kept + 1 summary = 6.
func TestSummarizer_Summarize_KeepsRecentHalf(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{
		KeepRecentRatio: 0.5,
		MinMessagesToSummarize: 4,
	})
	p.completeResp = "concise summary of older half"

	msgs := make([]Message, 10)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: "msg-" + string(rune('a'+i))}
	}

	out, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// Expect: 1 summary + 5 kept = 6.
	if len(out) != 6 {
		t.Fatalf("len(out) = %d, want 6", len(out))
	}
	if !strings.Contains(out[0].Content, "summary") {
		t.Errorf("first message = %q, want it to contain a summary", out[0].Content)
	}
	if out[0].Role != "user" {
		t.Errorf("summary role = %q, want user", out[0].Role)
	}
	// The last 5 kept messages should be in their original order.
	for i := 0; i < 5; i++ {
		if out[1+i].Content != "msg-"+string(rune('a'+5+i)) {
			t.Errorf("kept[%d] = %q, want msg-%c", i, out[1+i].Content, 'a'+5+i)
		}
	}
}

// --- TestSummarizer_Summarize_TooFewMessages_NoOp ----------------------------

// < MinMessagesToSummarize → input is returned unchanged, no LLM call.
func TestSummarizer_Summarize_TooFewMessages_NoOp(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{MinMessagesToSummarize: 4})

	msgs := []Message{{Role: "user", Content: "a"}, {Role: "user", Content: "b"}}
	out, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len(out) = %d, want 2 (no-op)", len(out))
	}
	if p.lastReq != nil {
		t.Error("LLM should not have been called for too-few messages")
	}
}

// --- TestSummarizer_DefaultConfig_AppliedWhenZero ----------------------------

// A zero-value SummarizerConfig must be filled with sensible defaults
// (MaxTokens=2048, Temperature=0, TriggerAtRatio=0.7, etc.) so callers
// can leave the struct blank.
func TestSummarizer_DefaultConfig_AppliedWhenZero(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{})

	cfg := s.Config()
	if cfg.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", cfg.MaxTokens)
	}
	if cfg.Temperature != 0 {
		t.Errorf("Temperature = %f, want 0", cfg.Temperature)
	}
	if cfg.TriggerAtRatio != 0.7 {
		t.Errorf("TriggerAtRatio = %f, want 0.7", cfg.TriggerAtRatio)
	}
	if cfg.KeepRecentRatio != 0.5 {
		t.Errorf("KeepRecentRatio = %f, want 0.5", cfg.KeepRecentRatio)
	}
	if cfg.MinMessagesToSummarize != 4 {
		t.Errorf("MinMessagesToSummarize = %d, want 4", cfg.MinMessagesToSummarize)
	}
	if cfg.SystemPrompt == "" {
		t.Error("SystemPrompt should be filled with the pentest default")
	}
}

// --- TestSummarizer_ProviderError_ReturnsOriginal ----------------------------

// When the LLM call fails the original messages must be returned
// unchanged — summarisation is best-effort, it must never break the
// agent loop.
func TestSummarizer_ProviderError_ReturnsOriginal(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test", completeErr: errProviderFailed}
	s := NewSummarizer(p, SummarizerConfig{MinMessagesToSummarize: 4})

	msgs := []Message{{Role: "user", Content: "a"}, {Role: "user", Content: "b"}, {Role: "user", Content: "c"}, {Role: "user", Content: "d"}}
	out, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out) != 4 {
		t.Errorf("len(out) = %d, want 4 (originals returned on error)", len(out))
	}
}

var errProviderFailed = &testError{msg: "synthetic failure"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// --- TestSummarizer_ShouldSummarize_SpecAlias -------------------------------

// ShouldSummarize is the spec-required name and must behave
// identically to NeedsSummarization.
func TestSummarizer_ShouldSummarize_SpecAlias(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{TriggerAtRatio: 0.5, MinMessagesToSummarize: 2})
	if s.ShouldSummarize([]Message{{Role: "user", Content: "x"}}) {
		t.Error("ShouldSummarize should be false for 1 message")
	}
	// 30 messages × 100 chars ≈ 750 tokens > 500 (50% of 1000).
	var msgs []Message
	for i := 0; i < 30; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("a", 100)})
	}
	if !s.ShouldSummarize(msgs) {
		t.Error("ShouldSummarize should be true at ~750 tokens / 50% threshold of 1000")
	}
}

// --- TestSummarizer_Stats_CountsCalls ---------------------------------------

// Each successful Summarize must increment the call counter; a
// provider error must NOT increment (it returns the originals and
// is best-effort).
func TestSummarizer_Stats_CountsCalls(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	p.completeResp = "summary"
	s := NewSummarizer(p, SummarizerConfig{MinMessagesToSummarize: 4, KeepRecentRatio: 0.5})
	msgs := []Message{
		{Role: "user", Content: "a"},
		{Role: "user", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "user", Content: "d"},
		{Role: "user", Content: "e"},
		{Role: "user", Content: "f"},
	}
	calls, before := s.Stats()
	if calls != 0 || !before.IsZero() {
		t.Errorf("initial Stats = (%d, %v), want (0, zero)", calls, before)
	}
	if _, err := s.Summarize(context.Background(), msgs); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	calls, after := s.Stats()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if after.IsZero() {
		t.Error("lastSummaryAt should be set after a successful call")
	}
	if time.Since(after) > time.Second {
		t.Errorf("lastSummaryAt too old: %v", after)
	}
}

// --- TestSummarizer_PreserveSystemPrompt_KeepsItOnTop -----------------------

// When PreserveSystemPrompt is true (the default), the first
// message — if it is a system message — must survive the
// summarisation round-trip and end up back at the head of the
// result slice.
func TestSummarizer_PreserveSystemPrompt_KeepsItOnTop(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	p.completeResp = "summary text"
	s := NewSummarizer(p, SummarizerConfig{
		MinMessagesToSummarize: 4,
		KeepRecentRatio:        0.5,
	})
	msgs := []Message{
		{Role: "system", Content: "you are a pentest agent"},
		{Role: "user", Content: "msg-1"},
		{Role: "assistant", Content: "msg-2"},
		{Role: "user", Content: "msg-3"},
		{Role: "assistant", Content: "msg-4"},
		{Role: "user", Content: "msg-5"},
	}
	out, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out) == 0 || out[0].Role != "system" {
		t.Fatalf("expected system message at out[0], got %+v", out)
	}
	if out[0].Content != "you are a pentest agent" {
		t.Errorf("system content lost: %q", out[0].Content)
	}
	if out[1].Role != "user" || !strings.Contains(out[1].Content, "summary") {
		t.Errorf("expected summary at out[1], got %+v", out[1])
	}
}

// --- TestSummarizer_CustomSystemPrompt_Overridden ---------------------------

// A user-provided SystemPrompt must replace the default. Verified by
// inspecting the request the mock provider receives.
func TestSummarizer_CustomSystemPrompt_Overridden(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	p.completeResp = "ok"
	s := NewSummarizer(p, SummarizerConfig{
		MinMessagesToSummarize: 4,
		KeepRecentRatio:        0.5,
		SystemPrompt:           "CUSTOM-SYS-PROMPT",
	})
	msgs := []Message{
		{Role: "user", Content: "a"},
		{Role: "user", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "user", Content: "d"},
		{Role: "user", Content: "e"},
		{Role: "user", Content: "f"},
	}
	_, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if p.lastReq == nil {
		t.Fatal("provider not called")
	}
	if p.lastReq.SystemPrompt != "CUSTOM-SYS-PROMPT" {
		t.Errorf("SystemPrompt = %q, want CUSTOM-SYS-PROMPT", p.lastReq.SystemPrompt)
	}
}

// --- TestSummarizer_CustomUserPromptTpl_Honoured ----------------------------

// UserPromptTemplate must wrap the joined text. {summary} is the
// single placeholder.
func TestSummarizer_CustomUserPromptTpl_Honoured(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	p.completeResp = "ok"
	s := NewSummarizer(p, SummarizerConfig{
		MinMessagesToSummarize: 4,
		KeepRecentRatio:        0.5,
		UserPromptTemplate:     "Please compress:\n{summary}\nEnd of input.",
	})
	msgs := []Message{
		{Role: "user", Content: "alpha"},
		{Role: "user", Content: "beta"},
		{Role: "user", Content: "gamma"},
		{Role: "user", Content: "delta"},
		{Role: "user", Content: "epsilon"},
		{Role: "user", Content: "zeta"},
	}
	_, err := s.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if p.lastReq == nil || len(p.lastReq.Messages) == 0 {
		t.Fatal("provider not called or no messages")
	}
	got := p.lastReq.Messages[0].Content
	if !strings.HasPrefix(got, "Please compress:") || !strings.HasSuffix(got, "End of input.") {
		t.Errorf("template not applied: %q", got)
	}
}

// --- TestSummarizer_IsNearLimit_WarningThreshold ----------------------------

// The WarningAtRatio gate must trigger between the trigger threshold
// and the upper bound.
func TestSummarizer_IsNearLimit_WarningThreshold(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{
		TriggerAtRatio:         0.5,
		WarningAtRatio:         0.8,
		MinMessagesToSummarize: 2,
	})
	// 5 messages × 100 chars ≈ 125 tokens: between 0% and 50% (i.e. below
	// the trigger threshold).
	var msgs []Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, Message{Role: "user", Content: strings.Repeat("a", 100)})
	}
	if s.ShouldSummarize(msgs) {
		t.Error("ShouldSummarize should be false at 125 tokens / 50% threshold")
	}
	if s.IsNearLimit(msgs) {
		t.Error("IsNearLimit should be false at 125 tokens / 80% threshold")
	}
	// 40 messages × 100 chars ≈ 1000 tokens → ~1000/1000 = 100% ≥ 80%.
	var longMsgs []Message
	for i := 0; i < 40; i++ {
		longMsgs = append(longMsgs, Message{Role: "user", Content: strings.Repeat("a", 100)})
	}
	if !s.IsNearLimit(longMsgs) {
		t.Error("IsNearLimit should be true at ~1000 tokens / 80% threshold")
	}
}
