package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes a YAML config to a temp file and returns
// the path. The caller is responsible for the file's lifetime.
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("creating temp config: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	_ = f.Close()
	return f.Name()
}

// TestLoad_Defaults_LLMBlock — when no `llm:` block is present, the
// loader must still produce a valid EmbeddingsConfig / SummarizerConfig
// (with the documented defaults) so the cmd/ startup path never
// sees a nil-embedding embedder.
func TestLoad_Defaults_LLMBlock(t *testing.T) {
	path := writeTempConfig(t, `
server:
  host: 0.0.0.0
  port: 8080
database:
  host: localhost
  name: pentestswarm
orchestrator:
  provider: claude
  model: claude-sonnet-4-6
  api_key: sk-test
`)
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Embeddings.Provider != "noop" {
		t.Errorf("Embeddings.Provider = %q, want noop (default)", cfg.LLM.Embeddings.Provider)
	}
	if cfg.LLM.Summarizer.MaxTokens != 2048 {
		t.Errorf("Summarizer.MaxTokens = %d, want 2048", cfg.LLM.Summarizer.MaxTokens)
	}
	if cfg.LLM.Summarizer.TriggerAt != 0.7 {
		t.Errorf("Summarizer.TriggerAt = %f, want 0.7", cfg.LLM.Summarizer.TriggerAt)
	}
	if !cfg.LLM.Summarizer.PreserveSystemPrompt {
		t.Error("PreserveSystemPrompt should default to true")
	}
}

// TestLoad_LLMBlock_Overrides — explicit values in YAML must win over
// the defaults.
func TestLoad_LLMBlock_Overrides(t *testing.T) {
	path := writeTempConfig(t, `
server:
  host: 0.0.0.0
  port: 8080
database:
  host: localhost
  name: pentestswarm
orchestrator:
  provider: claude
  model: claude-sonnet-4-6
  api_key: sk-test
llm:
  embeddings:
    provider: ollama
    model: nomic-embed-text
    dimensions: 768
    endpoint: http://gpu-host:11434
    cache:
      enabled: true
      size: 2048
      ttl: 30m
  summarizer:
    model: claude-haiku
    max_tokens: 512
    temperature: 0.2
    trigger_at: 0.6
    warning_at: 0.75
    keep_recent_ratio: 0.4
    min_messages_to_summarize: 6
    preserve_system_prompt: false
    custom_user_prompt_tpl: "Compress:\n{summary}"
`)
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Embeddings.Provider != "ollama" {
		t.Errorf("Embeddings.Provider = %q, want ollama", cfg.LLM.Embeddings.Provider)
	}
	if cfg.LLM.Embeddings.Model != "nomic-embed-text" {
		t.Errorf("Embeddings.Model = %q, want nomic-embed-text", cfg.LLM.Embeddings.Model)
	}
	if cfg.LLM.Embeddings.Dimensions != 768 {
		t.Errorf("Embeddings.Dimensions = %d, want 768", cfg.LLM.Embeddings.Dimensions)
	}
	if !cfg.LLM.Embeddings.Cache.Enabled {
		t.Error("Cache.Enabled should be true")
	}
	if cfg.LLM.Embeddings.Cache.Size != 2048 {
		t.Errorf("Cache.Size = %d, want 2048", cfg.LLM.Embeddings.Cache.Size)
	}
	if cfg.LLM.Summarizer.MaxTokens != 512 {
		t.Errorf("Summarizer.MaxTokens = %d, want 512", cfg.LLM.Summarizer.MaxTokens)
	}
	if cfg.LLM.Summarizer.TriggerAt != 0.6 {
		t.Errorf("Summarizer.TriggerAt = %f, want 0.6", cfg.LLM.Summarizer.TriggerAt)
	}
	if cfg.LLM.Summarizer.PreserveSystemPrompt {
		t.Error("PreserveSystemPrompt should be false (explicit override)")
	}
	if cfg.LLM.Summarizer.CustomUserPromptTpl != "Compress:\n{summary}" {
		t.Errorf("CustomUserPromptTpl = %q", cfg.LLM.Summarizer.CustomUserPromptTpl)
	}
}

// TestValidate_EmbeddingsProvider_RejectsUnknown — the validate path
// must surface a clear error for an unknown provider name.
func TestValidate_EmbeddingsProvider_RejectsUnknown(t *testing.T) {
	cfg := &Config{
		Orchestrator: OrchestratorConfig{Provider: "claude", Model: "m", APIKey: "k", Temperature: 0.1},
		Database:     DatabaseConfig{Host: "h", Name: "n"},
		Server:       ServerConfig{Port: 8080},
		LLM: LLMConfig{
			Embeddings: EmbeddingsConfig{Provider: "bogus-provider"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for unknown embeddings provider")
	}
	if !contains(err.Error(), "bogus-provider") {
		t.Errorf("error should mention the bad provider: %v", err)
	}
}

// TestValidate_SummarizerThresholds_OutOfRange — out-of-range
// ratios must fail validation.
func TestValidate_SummarizerThresholds_OutOfRange(t *testing.T) {
	cfg := &Config{
		Orchestrator: OrchestratorConfig{Provider: "claude", Model: "m", APIKey: "k", Temperature: 0.1},
		Database:     DatabaseConfig{Host: "h", Name: "n"},
		Server:       ServerConfig{Port: 8080},
		LLM: LLMConfig{
			Summarizer: SummarizerConfig{TriggerAt: 1.5, WarningAt: 0.8, KeepRecentRatio: 0.5},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for out-of-range trigger_at")
	}
	if !contains(err.Error(), "trigger_at") {
		t.Errorf("error should mention trigger_at: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || (len(s) > 0 && (s[:len(sub)] == sub || containsTail(s, sub)))))
}

func containsTail(s, sub string) bool {
	for i := 1; i < len(s); i++ {
		if s[i:] == "" {
			break
		}
		if len(s)-i >= len(sub) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// make sure the temp file cleanup stays simple.
var _ = filepath.Join
