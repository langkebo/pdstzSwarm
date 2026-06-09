// Package search provides reconnaissance search providers that the
// recon agent can invoke as black-box tools. Inspired by ptagent's
// 6-provider intelligence stack (DDG, Sploitus, SEARXNG, Google,
// Tavily, Perplexity) — this package implements the two highest-leverage
// adapters:
//
//   - DuckDuckGo (free, no key) for wide-net enumeration.
//   - Tavily (AI-tuned, paid) for citation-quality retrieval.
//
// Adding Sploitus / Perplexity is a copy of the Tavily pattern — the
// Provider interface below is small and explicit so new backends
// should be ≤ 80 LOC.
package search

import (
	"context"
	"time"
)

// Result is a single search hit, normalized across providers.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"` // provider name that produced it
}

// Query is the input to Search. Max caps the result count.
type Query struct {
	Text string
	Max  int
}

// Provider is the interface every search backend must implement.
type Provider interface {
	// Name returns the provider identifier (e.g. "ddg", "tavily").
	Name() string

	// Search returns up to q.Max results. Must return early on ctx
	// cancellation; must never return partial results with an error
	// (return either the slice you have or wrap ctx.Err()).
	Search(ctx context.Context, q Query) ([]Result, error)

	// IsConfigured reports whether the provider has the credentials
	// it needs (api key, etc.). Allows the recon agent to skip
	// unconfigured providers without an error.
	IsConfigured() bool
}

// DefaultTimeout is the per-call budget we apply when the caller
// doesn't pass a context deadline. 8 s matches the nmap default.
const DefaultTimeout = 8 * time.Second
