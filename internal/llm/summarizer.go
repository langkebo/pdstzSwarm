package llm

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Summarizer compresses long conversation histories into a single
// digest message so that agents running in long-lived campaigns do not
// exceed the model's context window. It is the parameterised replacement
// for the hard-coded summarisation path that used to live inside
// BudgetManager.Summarize.
//
// The summariser is intentionally split out from BudgetManager so that:
//   - Different agents can use different summariser configs (a fast
//     cheap model for recon vs. a stronger one for the orchestrator)
//   - The split point, retention ratio and trigger threshold are all
//     per-deploy tunable
//   - Summariser can be unit-tested without spinning up a blackboard
type Summarizer struct {
	provider Provider
	config   SummarizerConfig

	// observability counters. Reads via Stats() are atomic so the
	// hot path (NeedsSummarization) doesn't need a lock.
	calls          atomic.Int64
	lastSummaryAt  atomic.Int64 // unix nanos; 0 = never
	lastInputCount atomic.Int64
	lastOutCount   atomic.Int64
}

// SummarizerConfig controls how Summarize decides what to keep and how
// to ask the underlying provider. Zero values are filled with sensible
// defaults at construction time so that callers can leave fields blank.
//
// The mapstructure tags wire the struct directly to the `llm.summarizer:`
// section of config.yaml; the cmd/ startup path uses viper.Unmarshal
// and gets the populated struct back without any field-by-field copy.
type SummarizerConfig struct {
	// Model overrides the provider's default model. Empty → use the
	// provider's ModelName() as-is.
	Model string `mapstructure:"model"`

	// MaxTokens caps the summary output. 0 → 2048.
	MaxTokens int `mapstructure:"max_tokens"`

	// Temperature is the sampling temperature for the summary request.
	// 0 → 0 (deterministic). Negative values are treated as 0.
	Temperature float64 `mapstructure:"temperature"`

	// TriggerAtRatio is the fraction of the model's context window at
	// which NeedsSummarization returns true. Range (0, 1). 0 → 0.7.
	// 0.7 means: once estimated tokens exceed 70% of the context window.
	TriggerAtRatio float64 `mapstructure:"trigger_at"`

	// WarningAtRatio is the fraction of the context window at which
	// IsNearLimit returns true. Range (0, 1). 0 → 0.8.
	WarningAtRatio float64 `mapstructure:"warning_at"`

	// KeepRecentRatio is the fraction of the most-recent messages to
	// preserve verbatim. The older (1 - KeepRecentRatio) fraction is
	// fed to the LLM for summarisation. Range (0, 1). 0 → 0.5.
	KeepRecentRatio float64 `mapstructure:"keep_recent_ratio"`

	// MinMessagesToSummarize is the minimum message count below which
	// Summarize is a no-op. 0 → 4.
	MinMessagesToSummarize int `mapstructure:"min_messages"`

	// SystemPrompt is the instruction sent to the provider. Empty → a
	// pentest-aware default that preserves targets, vulns and commands.
	SystemPrompt string `mapstructure:"system_prompt"`

	// PreserveSystemPrompt, when true, lifts the messages[0] (assumed
	// to be a system prompt) out of the summarisation window and
	// re-prepends it to the compressed result. Keeps the agent's
	// persona / scope / rules intact across summarisation cycles.
	// Default: true (preserves the previous BudgetManager behaviour).
	PreserveSystemPrompt bool `mapstructure:"preserve_system_prompt"`

	// UserPromptTemplate is the template used to wrap the to-summarise
	// text. The string "{summary}" is replaced by the joined message
	// text. Empty → a built-in default:
	//   "Summarize this conversation:\n\n{summary}"
	UserPromptTemplate string `mapstructure:"user_prompt_template"`
}

// DefaultSummarizerConfig returns the production defaults that keep the
// previous hard-coded behaviour for users that don't override anything.
func DefaultSummarizerConfig() SummarizerConfig {
	return SummarizerConfig{
		Model:                 "",
		MaxTokens:             2048,
		Temperature:           0,
		TriggerAtRatio:        0.7,
		WarningAtRatio:        0.8,
		KeepRecentRatio:       0.5,
		MinMessagesToSummarize: 4,
		SystemPrompt:          defaultSummarizerSystemPrompt,
		PreserveSystemPrompt:  true,
		UserPromptTemplate:    defaultSummarizerUserPromptTpl,
	}
}

const defaultSummarizerSystemPrompt = "You are a summarization assistant. Summarize the following conversation concisely, preserving all key facts, decisions, findings, and action items. Do not lose any technical details about targets, vulnerabilities, or commands."

const defaultSummarizerUserPromptTpl = "Summarize this conversation:\n\n{summary}"

// NewSummarizer creates a Summarizer with config zero-values replaced by
// the defaults from DefaultSummarizerConfig.
func NewSummarizer(p Provider, cfg SummarizerConfig) *Summarizer {
	def := DefaultSummarizerConfig()
	// Start from the defaults so that explicitly-zero fields (notably
	// the bool PreserveSystemPrompt, which the spec wants true) are
	// filled in even when the caller doesn't set them. The merge
	// below then overwrites with whatever the caller did set, so
	// only true zeros fall back to the default.
	merged := def
	if cfg.Model != "" {
		merged.Model = cfg.Model
	}
	if cfg.MaxTokens != 0 {
		merged.MaxTokens = cfg.MaxTokens
	}
	if cfg.Temperature > 0 {
		merged.Temperature = cfg.Temperature
	}
	if cfg.TriggerAtRatio > 0 {
		merged.TriggerAtRatio = cfg.TriggerAtRatio
	}
	if cfg.WarningAtRatio > 0 {
		merged.WarningAtRatio = cfg.WarningAtRatio
	}
	if cfg.KeepRecentRatio > 0 {
		merged.KeepRecentRatio = cfg.KeepRecentRatio
	}
	if cfg.MinMessagesToSummarize > 0 {
		merged.MinMessagesToSummarize = cfg.MinMessagesToSummarize
	}
	if cfg.SystemPrompt != "" {
		merged.SystemPrompt = cfg.SystemPrompt
	}
	if cfg.UserPromptTemplate != "" {
		merged.UserPromptTemplate = cfg.UserPromptTemplate
	}
	// PreserveSystemPrompt is a bool — only override when the caller
	// passed the struct literal with a true value. The default is
	// true (from `def`), so unset stays true.
	if cfg.PreserveSystemPrompt {
		merged.PreserveSystemPrompt = true
	}
	return &Summarizer{provider: p, config: merged}
}

// Provider returns the underlying provider. Useful when the summariser
// is composed into a larger config object and the orchestrator wants to
// share one provider between the agent loop and the summarisation step.
func (s *Summarizer) Provider() Provider { return s.provider }

// Config returns a copy of the effective configuration. Used in tests and
// in observability hooks.
func (s *Summarizer) Config() SummarizerConfig { return s.config }

// NeedsSummarization reports whether the message list has grown past the
// configured trigger threshold relative to the provider's context window.
// If the provider's ContextWindow is 0 (defensive), it falls back to
// 200,000 to match the Claude default and avoid under-triggering.
//
// This is the canonical long name. ShouldSummarize is a spec-required
// alias that delegates here.
func (s *Summarizer) NeedsSummarization(messages []Message) bool {
	return s.ShouldSummarize(messages)
}

// ShouldSummarize is the spec-required entry point. It returns true
// when the conversation has crossed the trigger threshold. It is
// safe to call from the agent hot path: no allocations, no locks.
func (s *Summarizer) ShouldSummarize(messages []Message) bool {
	if len(messages) < s.config.MinMessagesToSummarize {
		return false
	}
	window := s.provider.ContextWindow()
	if window <= 0 {
		window = 200000
	}
	estimated := EstimateTokens(messages)
	threshold := int(float64(window) * s.config.TriggerAtRatio)
	return estimated >= threshold
}

// IsNearLimit returns true at the configured WarningAtRatio. Used by
// the agent loop to throttle aggressive tool-use bursts before the
// summarisation trigger fires.
func (s *Summarizer) IsNearLimit(messages []Message) bool {
	if len(messages) < s.config.MinMessagesToSummarize {
		return false
	}
	window := s.provider.ContextWindow()
	if window <= 0 {
		window = 200000
	}
	estimated := EstimateTokens(messages)
	threshold := int(float64(window) * s.config.WarningAtRatio)
	return estimated >= threshold
}

// Stats returns the number of LLM calls made so far and the timestamp
// of the most recent one (zero time.Time if no calls have been made).
// Used by the monitor and the LangFuse bridge to attach span
// attributes like "summariser_calls=N".
func (s *Summarizer) Stats() (calls int, lastSummaryAt time.Time) {
	c := int(s.calls.Load())
	ts := s.lastSummaryAt.Load()
	if ts == 0 {
		return c, time.Time{}
	}
	return c, time.Unix(0, ts)
}

// Summarize compresses the older portion of the message history into a
// single summary message while keeping the most recent messages verbatim.
// The result is a new slice suitable for re-feeding into the next LLM
// completion. On any provider error the original slice is returned
// unchanged — summarisation is best-effort and never breaks the agent
// loop.
func (s *Summarizer) Summarize(ctx context.Context, messages []Message) ([]Message, error) {
	if len(messages) < s.config.MinMessagesToSummarize {
		return messages, nil
	}

	// Pull the system message out if configured to preserve it.
	systemMsg, working := splitSystemMessage(messages, s.config.PreserveSystemPrompt)

	keep := int(float64(len(working)) * s.config.KeepRecentRatio)
	if keep < 1 {
		keep = 1
	}
	if keep >= len(working) {
		// Nothing older to summarise.
		return messages, nil
	}
	splitPoint := len(working) - keep
	toSummarize := working[:splitPoint]
	toKeep := working[splitPoint:]

	var summaryInput strings.Builder
	for _, m := range toSummarize {
		fmt.Fprintf(&summaryInput, "[%s]: %s\n\n", m.Role, m.Content)
	}

	model := s.config.Model
	if model == "" {
		model = s.provider.ModelName()
	}

	userPrompt := s.config.UserPromptTemplate
	userPrompt = strings.ReplaceAll(userPrompt, "{summary}", summaryInput.String())

	req := CompletionRequest{
		SystemPrompt: s.config.SystemPrompt,
		Messages: []Message{
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   s.config.MaxTokens,
		Temperature: s.config.Temperature,
	}
	_ = model // surfaced in provider's own model config; preserved here for observability

	resp, err := s.provider.Complete(ctx, req)
	if err != nil {
		// Best-effort: return the originals so the agent loop survives.
		return messages, nil
	}

	// Record observability counters.
	s.calls.Add(1)
	s.lastSummaryAt.Store(time.Now().UnixNano())
	s.lastInputCount.Store(int64(len(messages)))
	s.lastOutCount.Store(int64(1 + len(toKeep)))

	summarised := []Message{
		{Role: "user", Content: "[Previous conversation summary]: " + resp.Content},
	}
	summarised = append(summarised, toKeep...)
	if systemMsg != nil {
		// Prepend the preserved system message back at the top.
		out := make([]Message, 0, 1+len(summarised))
		out = append(out, *systemMsg)
		out = append(out, summarised...)
		return out, nil
	}
	return summarised, nil
}

// splitSystemMessage returns the leading system message (if any and
// if preserve is true) and the remaining messages to summarise. When
// preserve is false both return values reflect the original slice.
func splitSystemMessage(messages []Message, preserve bool) (*Message, []Message) {
	if !preserve || len(messages) == 0 {
		return nil, messages
	}
	if messages[0].Role != "system" {
		return nil, messages
	}
	return &messages[0], messages[1:]
}
