package langfuse

import (
	"os"
	"testing"
	"time"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Unset the env vars we're interested in and verify the
	// resulting config is the "disabled" default.
	for _, k := range []string{
		"LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
		"LANGFUSE_HOST", "LANGFUSE_BATCH", "LANGFUSE_FLUSH_SEC",
	} {
		_ = os.Unsetenv(k)
	}
	c := ConfigFromEnv()
	if c.IsEnabled() {
		t.Error("ConfigFromEnv with no keys should be disabled")
	}
	if c.Endpoint != "" {
		t.Errorf("Endpoint = %q, want empty (defaulted in NewClient)", c.Endpoint)
	}
}

func TestConfigFromEnv_BothKeysEnable(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
	c := ConfigFromEnv()
	if !c.IsEnabled() {
		t.Error("expected IsEnabled() = true with both keys set")
	}
}

func TestConfigFromEnv_OnlyPublicKeyDoesNotEnable(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "")
	if ConfigFromEnv().IsEnabled() {
		t.Error("public key alone should not enable")
	}
}

func TestConfigFromEnv_Host(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk")
	t.Setenv("LANGFUSE_HOST", "https://langfuse.example.com")
	c := ConfigFromEnv()
	if c.Endpoint != "https://langfuse.example.com" {
		t.Errorf("Endpoint = %q", c.Endpoint)
	}
}

func TestConfigFromEnv_BatchAndFlush(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk")
	t.Setenv("LANGFUSE_BATCH", "100")
	t.Setenv("LANGFUSE_FLUSH_SEC", "30")
	c := ConfigFromEnv()
	if c.MaxBatchSize != 100 {
		t.Errorf("MaxBatchSize = %d", c.MaxBatchSize)
	}
	if c.FlushInterval != 30*time.Second {
		t.Errorf("FlushInterval = %v, want 30s", c.FlushInterval)
	}
}

func TestConfigFromEnv_MalformedValuesIgnored(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk")
	t.Setenv("LANGFUSE_BATCH", "not-a-number")
	t.Setenv("LANGFUSE_FLUSH_SEC", "")
	c := ConfigFromEnv()
	// Malformed values must not blow up; they should fall
	// through to NewClient's defaults (0 in Config, 500 / 5s after).
	if c.MaxBatchSize != 0 {
		t.Errorf("malformed MaxBatchSize leaked: %d", c.MaxBatchSize)
	}
	if c.FlushInterval != 0 {
		t.Errorf("malformed FlushInterval leaked: %v", c.FlushInterval)
	}
}

func TestConfigFromEnvEnabled_BothKeys(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk")
	if _, ok := ConfigFromEnvEnabled(); !ok {
		t.Error("ConfigFromEnvEnabled() = (_, false), want true")
	}
}
