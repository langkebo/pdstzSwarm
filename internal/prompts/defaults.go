package prompts

import (
	"context"
	"sync"

	agentprompts "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/prompts"
)

// EmbeddedDefaults is the production DefaultsSource. It
// delegates to internal/agent/prompts.Load for any type whose
// .tmpl file exists in the embedded templates/ directory.
//
// Types without a .tmpl (the 5 categories × {system, recon,
// recon_strict, exploit, exploit_strict} = 20 + the
// cross-cutting + meta types) return ("", false) on
// DefaultBody, which the Service treats as "no default, only
// overrides". The editor's UI shows these as "custom only".
type EmbeddedDefaults struct {
	// contextFn lets the caller inject a Context. Default is
	// a zero-value Context, which still renders cleanly (the
	// templates all have `if .X` fallbacks).
	contextFn func() agentprompts.Context

	// cache: most defaults are looked up repeatedly across
	// List() calls in a single List round-trip. The cache
	// is invalidated implicitly by build-time embed, so
	// "version" doesn't matter — the cache lives for the
	// process lifetime.
	once  sync.Once
	cache map[PromptType]string
}

// NewEmbeddedDefaults returns an EmbeddedDefaults that reads
// from the embedded templates/ directory.
func NewEmbeddedDefaults() *EmbeddedDefaults {
	return &EmbeddedDefaults{
		contextFn: func() agentprompts.Context { return agentprompts.Context{} },
	}
}

// WithContext returns a new EmbeddedDefaults that injects
// the given context. Used when the Service is constructed
// inside the API server (which has a real engagement scope
// to pass through).
func (d *EmbeddedDefaults) WithContext(fn func() agentprompts.Context) *EmbeddedDefaults {
	return &EmbeddedDefaults{
		contextFn: fn,
		cache:     d.cache,
	}
}

// DefaultBody implements DefaultsSource. Returns ("", false)
// for any type whose .tmpl is missing, so the editor can
// distinguish "no default" from "empty default".
func (d *EmbeddedDefaults) DefaultBody(t PromptType) (string, bool) {
	d.once.Do(func() { d.cache = make(map[PromptType]string) })
	if body, ok := d.cache[t]; ok {
		return body, body != ""
	}
	body, err := agentprompts.Load(string(t), d.contextFn())
	if err != nil {
		// Missing .tmpl or parse error → cache the negative
		// answer so we don't re-do the disk read on every call.
		d.cache[t] = ""
		return "", false
	}
	d.cache[t] = body
	return body, true
}

// Pre-warm the cache for all 35 types. Useful for the CLI
// startup path to surface missing-templates early (e.g. if
// a contributor added a new PromptType to the catalogue
// but forgot to ship the .tmpl).
func (d *EmbeddedDefaults) Warmup(ctx context.Context) (missing []PromptType) {
	for _, t := range AllPromptTypes {
		if _, ok := d.DefaultBody(t); !ok {
			missing = append(missing, t)
		}
	}
	return
}
