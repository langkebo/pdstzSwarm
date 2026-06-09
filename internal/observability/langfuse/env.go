package langfuse

import (
	"os"
	"strconv"
	"time"
)

// ConfigFromEnv returns a Config populated from the standard
// LANGFUSE_* environment variables. The function never panics on
// missing or malformed values — it falls back to safe defaults so
// the bridge can be enabled by simply exporting two keys.
//
// Env contract:
//
//   - LANGFUSE_PUBLIC_KEY  project public key (required to enable)
//   - LANGFUSE_SECRET_KEY  project secret key (required to enable)
//   - LANGFUSE_HOST        ingestion endpoint (default: cloud.langfuse.com)
//   - LANGFUSE_BATCH       max events per flush (default: 500)
//   - LANGFUSE_FLUSH_SEC   flush interval seconds (default: 5)
//
// Any of LANGFUSE_BATCH / LANGFUSE_FLUSH_SEC with a malformed
// integer is silently ignored; we don't want a typo in an env
// file to break the agent boot.
func ConfigFromEnv() Config {
	c := Config{
		PublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"),
		SecretKey: os.Getenv("LANGFUSE_SECRET_KEY"),
		Endpoint:  os.Getenv("LANGFUSE_HOST"),
	}
	if v := os.Getenv("LANGFUSE_BATCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxBatchSize = n
		}
	}
	if v := os.Getenv("LANGFUSE_FLUSH_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.FlushInterval = time.Duration(n) * time.Second
		}
	}
	return c
}

// IsEnabled reports whether the env-derived config has both keys
// set. Callers can use this for a single-source-of-truth check
// before constructing the bridge.
func (c Config) IsEnabled() bool {
	return c.PublicKey != "" && c.SecretKey != ""
}

// ConfigFromEnvEnabled is a convenience that returns (Config, true)
// when both keys are present. Useful in cmd-line code that needs
// a one-line check.
func ConfigFromEnvEnabled() (Config, bool) {
	c := ConfigFromEnv()
	return c, c.IsEnabled()
}
