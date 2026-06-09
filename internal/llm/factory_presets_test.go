package llm

import (
	"strings"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
)

func TestNewProvider_DeepSeekPreset(t *testing.T) {
	p, err := NewProvider(config.OrchestratorConfig{
		Provider: "deepseek",
		APIKey:   "sk-test",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p.ModelName() != "deepseek-v4-pro" {
		t.Errorf("ModelName() = %q, want deepseek-v4-pro (auto-filled default)", p.ModelName())
	}
	if p.ContextWindow() != 128000 {
		t.Errorf("ContextWindow() = %d, want 128000 (auto-filled default)", p.ContextWindow())
	}
}

func TestNewProvider_DeepSeekOverrides(t *testing.T) {
	// Explicit endpoint / model / context window must win over the
	// preset defaults. Lets an operator pin a particular model version
	// without forking the binary.
	p, err := NewProvider(config.OrchestratorConfig{
		Provider:      "deepseek",
		APIKey:        "sk-test",
		Endpoint:      "https://custom.example.com",
		Model:         "deepseek-reasoner",
		ContextWindow: 64000,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p.ModelName() != "deepseek-reasoner" {
		t.Errorf("ModelName() = %q, want deepseek-reasoner (explicit override)", p.ModelName())
	}
	if p.ContextWindow() != 64000 {
		t.Errorf("ContextWindow() = %d, want 64000 (explicit override)", p.ContextWindow())
	}
}

func TestNewProvider_DeepSeekMissingKey(t *testing.T) {
	_, err := NewProvider(config.OrchestratorConfig{Provider: "deepseek"})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should mention api_key, got: %v", err)
	}
}

func TestNewProvider_GLMAlaises(t *testing.T) {
	aliases := []string{"glm", "zhipu", "zhipuai"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			p, err := NewProvider(config.OrchestratorConfig{
				Provider: alias,
				APIKey:   "sk-test",
			})
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", alias, err)
			}
			if p.ModelName() != "glm-4.6" {
				t.Errorf("ModelName() = %q, want glm-4.6", p.ModelName())
			}
		})
	}
}

func TestNewProvider_KimiAlaises(t *testing.T) {
	aliases := []string{"kimi", "moonshot"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			p, err := NewProvider(config.OrchestratorConfig{
				Provider: alias,
				APIKey:   "sk-test",
			})
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", alias, err)
			}
			if p.ModelName() != "moonshot-v1-128k" {
				t.Errorf("ModelName() = %q, want moonshot-v1-128k", p.ModelName())
			}
		})
	}
}

func TestNewProvider_QwenAlaises(t *testing.T) {
	aliases := []string{"qwen", "dashscope", "aliyun", "tongyi"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			p, err := NewProvider(config.OrchestratorConfig{
				Provider: alias,
				APIKey:   "sk-test",
			})
			if err != nil {
				t.Fatalf("NewProvider(%q): %v", alias, err)
			}
			if p.ModelName() != "qwen-plus" {
				t.Errorf("ModelName() = %q, want qwen-plus", p.ModelName())
			}
		})
	}
}

func TestNewProvider_AllPresetsHaveAtLeast32k(t *testing.T) {
	// All preset context windows must be ≥ 32,000 to satisfy
	// ValidateProvider's contract.
	presets := []struct {
		provider string
		key      string
	}{
		{"deepseek", "sk-a"},
		{"glm", "sk-b"},
		{"kimi", "sk-c"},
		{"qwen", "sk-d"},
	}
	for _, ps := range presets {
		t.Run(ps.provider, func(t *testing.T) {
			p, err := NewProvider(config.OrchestratorConfig{Provider: ps.provider, APIKey: ps.key})
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			if p.ContextWindow() < 32000 {
				t.Errorf("ContextWindow() = %d, must be ≥ 32000", p.ContextWindow())
			}
		})
	}
}

func TestNewProvider_UnknownProvider(t *testing.T) {
	_, err := NewProvider(config.OrchestratorConfig{Provider: "fictional", APIKey: "x"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error should mention unknown, got: %v", err)
	}
	// The error must now list the new domestic options so users can
	// discover them via the error message alone.
	for _, expect := range []string{"deepseek", "glm", "kimi", "qwen"} {
		if !strings.Contains(err.Error(), expect) {
			t.Errorf("unknown-provider error should mention %q, got: %v", expect, err)
		}
	}
}
