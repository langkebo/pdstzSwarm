package llm

import (
	"context"
	"fmt"
)

// BudgetManager tracks token usage and manages context window limits.
// As of P3-2 the summarisation step is delegated to a Summarizer when
// one is configured; otherwise the original hard-coded behaviour is
// preserved for backward compatibility with code constructed via
// NewBudgetManager.
type BudgetManager struct {
	contextWindow    int
	warningThreshold float64 // percentage of context window to warn at (0.8 = 80%)
	summarizeAt      float64 // percentage to trigger summarization (0.7 = 70%)

	// Optional parameterised summarisation path. nil → fall back to the
	// legacy hard-coded summary below.
	summarizer *Summarizer

	// Legacy hard-coded parameters. Used only when summarizer is nil.
	// Kept here as exported fields so callers (and the factory) can
	// override them without having to construct a full Summarizer.
	MaxTokens   int
	Temperature float64
	Model       string
}

// NewBudgetManager creates a new token budget manager. The signature is
// unchanged from before P3-2; the result behaves identically to the
// pre-P3-2 implementation, with the added ability to attach a
// parameterised summariser later via SetSummarizer.
func NewBudgetManager(contextWindow int) *BudgetManager {
	return &BudgetManager{
		contextWindow:    contextWindow,
		warningThreshold: 0.80,
		summarizeAt:      0.70,
		MaxTokens:        2048,
		Temperature:      0,
	}
}

// NewBudgetManagerWithSummarizer creates a budget manager that delegates
// all summarisation to the provided Summarizer. The context window is
// taken from the Summarizer's underlying provider.
func NewBudgetManagerWithSummarizer(s *Summarizer) *BudgetManager {
	window := 0
	if s != nil && s.provider != nil {
		window = s.provider.ContextWindow()
	}
	b := NewBudgetManager(window)
	b.summarizer = s
	return b
}

// SetSummarizer attaches (or detaches, when nil) a parameterised
// summariser. Useful for callers that built the budget manager with the
// legacy constructor and want to opt into the new path at runtime.
func (b *BudgetManager) SetSummarizer(s *Summarizer) {
	b.summarizer = s
	if s != nil && s.provider != nil && s.provider.ContextWindow() > 0 {
		b.contextWindow = s.provider.ContextWindow()
	}
}

// EstimateTokens gives a rough estimate of tokens in a message list.
// Uses ~4 chars per token as a rough heuristic for English text.
func EstimateTokens(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
		total += 4 // overhead per message (role tokens, formatting)
	}
	return total
}

// NeedsSummarization returns true if the messages are approaching the context limit.
func (b *BudgetManager) NeedsSummarization(messages []Message) bool {
	if b.summarizer != nil {
		return b.summarizer.NeedsSummarization(messages)
	}
	estimated := EstimateTokens(messages)
	threshold := int(float64(b.contextWindow) * b.summarizeAt)
	return estimated >= threshold
}

// IsNearLimit returns true if messages are at the warning threshold.
func (b *BudgetManager) IsNearLimit(messages []Message) bool {
	estimated := EstimateTokens(messages)
	threshold := int(float64(b.contextWindow) * b.warningThreshold)
	return estimated >= threshold
}

// Summarize compresses the oldest messages by asking the LLM to summarize them.
// Keeps the most recent messages intact for context continuity.
//
// When a Summarizer has been attached (via NewBudgetManagerWithSummarizer
// or SetSummarizer) the request is delegated to it. Otherwise the legacy
// hard-coded parameters (MaxTokens, Temperature, Model) are honoured, with
// the same fall-back-on-error semantics as before P3-2.
func (b *BudgetManager) Summarize(ctx context.Context, messages []Message, provider Provider) ([]Message, error) {
	if b.summarizer != nil {
		return b.summarizer.Summarize(ctx, messages)
	}
	return b.legacySummarize(ctx, messages, provider)
}

// legacySummarize preserves the exact pre-P3-2 hard-coded behaviour:
// split 50/50, MaxTokens 2048, Temperature 0, fixed system prompt. If
// the LLM call fails, the original messages are returned unchanged.
func (b *BudgetManager) legacySummarize(ctx context.Context, messages []Message, provider Provider) ([]Message, error) {
	if len(messages) < 4 {
		return messages, nil // nothing to summarize
	}

	// Split: summarize oldest 50%, keep newest 50%
	splitPoint := len(messages) / 2
	toSummarize := messages[:splitPoint]
	toKeep := messages[splitPoint:]

	// Build the content to summarize
	var summaryInput string
	for _, m := range toSummarize {
		summaryInput += fmt.Sprintf("[%s]: %s\n\n", m.Role, m.Content)
	}

	summaryReq := CompletionRequest{
		SystemPrompt: "You are a summarization assistant. Summarize the following conversation concisely, preserving all key facts, decisions, findings, and action items. Do not lose any technical details about targets, vulnerabilities, or commands.",
		Messages: []Message{
			{Role: "user", Content: fmt.Sprintf("Summarize this conversation:\n\n%s", summaryInput)},
		},
		MaxTokens:   b.MaxTokens,
		Temperature: b.Temperature,
	}
	if b.MaxTokens <= 0 {
		summaryReq.MaxTokens = 2048
	}

	resp, err := provider.Complete(ctx, summaryReq)
	if err != nil {
		// If summarization fails, just return original messages
		return messages, nil
	}

	// Replace old messages with summary + kept messages
	summarized := []Message{
		{Role: "user", Content: "[Previous conversation summary]: " + resp.Content},
	}
	summarized = append(summarized, toKeep...)

	return summarized, nil
}

// RemainingTokens returns the estimated remaining tokens in the budget.
func (b *BudgetManager) RemainingTokens(messages []Message) int {
	used := EstimateTokens(messages)
	remaining := b.contextWindow - used
	if remaining < 0 {
		return 0
	}
	return remaining
}
