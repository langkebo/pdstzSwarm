package llm

import (
	"context"
	"strings"
	"testing"
)

// --- TestBudgetManager_BackwardCompatibleWithoutSummarizer -------------------

// Legacy callers that build a budget manager with NewBudgetManager and
// then call Summarize(messages, provider) must still get the original
// hard-coded behaviour (50/50 split, MaxTokens=2048, Temperature=0).
func TestBudgetManager_BackwardCompatibleWithoutSummarizer(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	bm := NewBudgetManager(p.ContextWindow())
	// Pre-P3-2 hard-coded knobs are preserved as exported fields.
	if bm.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048 (legacy default)", bm.MaxTokens)
	}
	if bm.Temperature != 0 {
		t.Errorf("Temperature = %f, want 0 (legacy default)", bm.Temperature)
	}

	p.completeResp = "ok"
	msgs := make([]Message, 10)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: "m"}
	}
	out, err := bm.Summarize(context.Background(), msgs, p)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// Legacy: 50/50 split → 1 summary + 5 kept = 6.
	if len(out) != 6 {
		t.Errorf("len(out) = %d, want 6 (legacy 50/50 split)", len(out))
	}
	// And the request used the legacy hard-coded parameters.
	if p.lastReq == nil {
		t.Fatal("LLM not called")
	}
	if p.lastReq.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", p.lastReq.MaxTokens)
	}
	if p.lastReq.Temperature != 0 {
		t.Errorf("Temperature = %f, want 0", p.lastReq.Temperature)
	}
}

// --- TestBudgetManager_UsesSummarizerWhenSet --------------------------------

// When a Summarizer is attached, BudgetManager.Summarize must delegate
// to it; the second-arg provider is ignored.
func TestBudgetManager_UsesSummarizerWhenSet(t *testing.T) {
	p := &mockProvider{contextWindow: 1000, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{
		KeepRecentRatio:        0.5,
		MinMessagesToSummarize: 4,
		MaxTokens:              512,
		Temperature:            0.4,
	})
	bm := NewBudgetManagerWithSummarizer(s)
	if bm.contextWindow != 1000 {
		t.Errorf("contextWindow = %d, want 1000 (from summarizer provider)", bm.contextWindow)
	}

	p.completeResp = "summary"
	msgs := make([]Message, 10)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: "x"}
	}
	out, err := bm.Summarize(context.Background(), msgs, &mockProvider{contextWindow: 100, modelName: "wrong"})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out) != 6 {
		t.Errorf("len(out) = %d, want 6", len(out))
	}
	// Delegated request used the Summarizer's knobs, not the legacy ones.
	if p.lastReq == nil {
		t.Fatal("LLM not called")
	}
	if p.lastReq.MaxTokens != 512 {
		t.Errorf("MaxTokens = %d, want 512 (from Summarizer)", p.lastReq.MaxTokens)
	}
	if p.lastReq.Temperature != 0.4 {
		t.Errorf("Temperature = %f, want 0.4 (from Summarizer)", p.lastReq.Temperature)
	}
}

// --- TestBudgetManager_SetSummarizer -----------------------------------------

// Runtime-attachable: build with NewBudgetManager, then SetSummarizer
// to opt into the parameterised path.
func TestBudgetManager_SetSummarizer(t *testing.T) {
	p := &mockProvider{contextWindow: 2000, modelName: "test"}
	bm := NewBudgetManager(0) // 0 → would default to 200000, but provider has 2000
	// No summarizer yet: needs-summarisation uses the bm's window.
	if bm.contextWindow != 0 {
		t.Errorf("contextWindow = %d, want 0 (untouched)", bm.contextWindow)
	}

	s := NewSummarizer(p, SummarizerConfig{MinMessagesToSummarize: 4})
	bm.SetSummarizer(s)
	if bm.contextWindow != 2000 {
		t.Errorf("contextWindow after SetSummarizer = %d, want 2000", bm.contextWindow)
	}

	// Detach: SetSummarizer(nil) keeps the previously-learned window.
	bm.SetSummarizer(nil)
	if bm.contextWindow != 2000 {
		t.Errorf("contextWindow after detach = %d, want 2000", bm.contextWindow)
	}
}

// --- TestBudgetManager_NeedsSummarization_Delegates --------------------------

// When a Summarizer is attached, NeedsSummarization must call through
// to the summariser's threshold logic.
func TestBudgetManager_NeedsSummarization_Delegates(t *testing.T) {
	p := &mockProvider{contextWindow: 100, modelName: "test"}
	s := NewSummarizer(p, SummarizerConfig{TriggerAtRatio: 0.5, MinMessagesToSummarize: 2})
	bm := NewBudgetManagerWithSummarizer(s)

	shortMsgs := []Message{{Role: "user", Content: "hi"}}
	if bm.NeedsSummarization(shortMsgs) {
		t.Error("NeedsSummarization should be false for 1 short message (below MinMessagesToSummarize)")
	}

	// 5 × 100 chars ≈ 125 tokens > 50% of 100-token window.
	var longMsgs []Message
	for i := 0; i < 5; i++ {
		longMsgs = append(longMsgs, Message{Role: "user", Content: strings.Repeat("z", 100)})
	}
	if !bm.NeedsSummarization(longMsgs) {
		t.Error("NeedsSummarization should be true for 5 long messages")
	}
}
